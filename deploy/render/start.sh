#!/usr/bin/env bash
set -Eeuo pipefail

: "${PORT:=10000}"
: "${JWT_SECRET:?JWT_SECRET is required}"
: "${CORS_ALLOWED_ORIGINS:?CORS_ALLOWED_ORIGINS must contain the GitHub Pages origin}"
: "${MYSQL_TLS_CA_PEM:?MYSQL_TLS_CA_PEM must contain the database provider CA certificate}"
: "${AUTH_DATABASE_URL:?AUTH_DATABASE_URL is required}"
: "${AUTH_MIGRATE_DATABASE_URL:?AUTH_MIGRATE_DATABASE_URL is required}"
: "${PROJECT_DATABASE_URL:?PROJECT_DATABASE_URL is required}"
: "${PROJECT_MIGRATE_DATABASE_URL:?PROJECT_MIGRATE_DATABASE_URL is required}"
: "${ISSUE_DATABASE_URL:?ISSUE_DATABASE_URL is required}"
: "${ISSUE_MIGRATE_DATABASE_URL:?ISSUE_MIGRATE_DATABASE_URL is required}"
: "${ACTIVITY_DATABASE_URL:?ACTIVITY_DATABASE_URL is required}"
: "${ACTIVITY_MIGRATE_DATABASE_URL:?ACTIVITY_MIGRATE_DATABASE_URL is required}"
if [[ -n "${OPENROUTER_API_KEY:-}" ]]; then
  : "${AI_DATABASE_URL:?AI_DATABASE_URL is required when OPENROUTER_API_KEY is configured}"
  : "${AI_MIGRATE_DATABASE_URL:?AI_MIGRATE_DATABASE_URL is required when OPENROUTER_API_KEY is configured}"
  export AI_SERVICE_URL=http://127.0.0.1:8087
else
  export AI_SERVICE_URL=
fi

children=()
shutdown() {
  local pid
  trap - EXIT TERM INT
  for pid in "${children[@]}"; do
    kill -TERM "$pid" 2>/dev/null || true
  done
  for pid in "${children[@]}"; do
    wait "$pid" 2>/dev/null || true
  done
}
trap shutdown EXIT
trap 'exit 143' TERM
trap 'exit 130' INT

MYSQL_TLS_CA_FILE=/tmp/trackline-mysql-ca.pem
printf '%s\n' "$MYSQL_TLS_CA_PEM" > "$MYSQL_TLS_CA_FILE"
chmod 0600 "$MYSQL_TLS_CA_FILE"
export MYSQL_TLS_CA_FILE
unset MYSQL_TLS_CA_PEM

start_child() {
  "$@" &
  children+=("$!")
}

wait_http() {
  local name="$1" url="$2" attempt pid
  for attempt in {1..60}; do
    for pid in "${children[@]}"; do
      if ! kill -0 "$pid" 2>/dev/null; then
        echo "$name process exited before becoming healthy" >&2
        return 1
      fi
    done
    if curl --silent --fail --max-time 2 "$url" >/dev/null; then
      echo "$name is healthy"
      return 0
    fi
    sleep 1
  done
  echo "$name failed its health check: $url" >&2
  return 1
}

run_migration() {
  local database="$1" url="$2" attempt log_file
  # migrate's MySQL URL parser passes the DSN to mysql.ParseDSN, which treats
  # the last literal slash as the database separator even inside query values.
  # Encode this CA path slash so it remains a query value until ParseQuery.
  url="${url//\/tmp\/trackline-mysql-ca.pem/%2Ftmp%2Ftrackline-mysql-ca.pem}"
  log_file="/tmp/migrate-${database}.log"
  for attempt in {1..30}; do
    if migrate -path "/app/migrations/${database}" -database "$url" up >"$log_file" 2>&1; then
      # Migration drivers can include a DSN in error output. Never write URL
      # credentials to the Render log stream.
      sed -E 's#(mysql://[^:/@]+:)[^@]*@#\1[REDACTED]@#g' "$log_file"
      return 0
    fi
    sleep 2
  done
  echo "Migration failed for ${database} after ${attempt} attempts. Last migrate output (database credentials redacted):" >&2
  sed -E 's#(mysql://[^:/@]+:)[^@]*@#\1[REDACTED]@#g' "$log_file" >&2
  return 1
}

# Bind Render's assigned port before potentially slow NATS startup or remote
# database migrations. /ready continues to report unavailable until every
# internal service is healthy (checked below).
export GATEWAY_ADDR=":${PORT}"
export AUTH_SERVICE_URL=http://127.0.0.1:8081
export PROJECT_SERVICE_URL=http://127.0.0.1:8082
export ISSUE_SERVICE_URL=http://127.0.0.1:8083
export ACTIVITY_SERVICE_URL=http://127.0.0.1:8084
export CORS_ALLOWED_ORIGINS
start_child gateway

# The broker and all internal HTTP services listen only on loopback. Render's
# public ingress reaches only the gateway on its assigned PORT.
start_child nats-server -a 127.0.0.1 -p 4222 -js -sd /tmp/trackline-nats
for attempt in {1..30}; do
  if nc -z 127.0.0.1 4222; then break; fi
  sleep 1
done
if ! nc -z 127.0.0.1 4222; then
  echo "NATS did not start" >&2
  exit 1
fi

run_migration auth_db "$AUTH_MIGRATE_DATABASE_URL"
run_migration project_db "$PROJECT_MIGRATE_DATABASE_URL"
run_migration issue_db "$ISSUE_MIGRATE_DATABASE_URL"
run_migration activity_db "$ACTIVITY_MIGRATE_DATABASE_URL"
if [[ -n "${OPENROUTER_API_KEY:-}" ]]; then
  run_migration ai_db "$AI_MIGRATE_DATABASE_URL"
fi

export AUTH_ADDR=127.0.0.1:8081
export AUTH_SERVICE_URL=http://127.0.0.1:8081
export AUTH_DATABASE_URL
export JWT_SECRET
start_child auth
wait_http auth http://127.0.0.1:8081/ready

export PROJECT_ADDR=127.0.0.1:8082
export PROJECT_SERVICE_URL=http://127.0.0.1:8082
export PROJECT_DATABASE_URL
start_child project
wait_http project http://127.0.0.1:8082/ready

export ISSUE_ADDR=127.0.0.1:8083
export ISSUE_DATABASE_URL
export NATS_URL=nats://127.0.0.1:4222
start_child issue
wait_http issue http://127.0.0.1:8083/ready

export ACTIVITY_ADDR=127.0.0.1:8084
export ACTIVITY_DATABASE_URL
export PROJECT_SERVICE_URL
start_child activity
wait_http activity http://127.0.0.1:8084/ready

if [[ -n "${OPENROUTER_API_KEY:-}" ]]; then
  export AI_ADDR=127.0.0.1:8087
  export AI_DATABASE_URL OPENROUTER_API_KEY
  start_child ai
  wait_http ai http://127.0.0.1:8087/ready
fi

wait_http "gateway" "http://127.0.0.1:${PORT}/ready"

echo "Trackline backend started on port ${PORT}"
wait -n "${children[@]}"
