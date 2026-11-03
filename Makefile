.DEFAULT_GOAL := help

COMPOSE ?= docker compose
COMPOSE_FILE ?= docker-compose.yml
MIGRATE_SERVICE ?= migrate
MIGRATE_DATABASE ?= all
MIGRATE_STEPS ?= 1
MIGRATE_TARGET_DB = $(if $(filter all,$(MIGRATE_DATABASE)),auth_db,$(MIGRATE_DATABASE))

DC = $(COMPOSE) -f $(COMPOSE_FILE)
DATABASES = auth_db project_db issue_db activity_db

.PHONY: help run-trackline run-bug-issue-tracker run-frontend run-backend run-auth run-project run-issue run-activity run-ai run-gateway docker-up docker-app-up docker-status docker-down migrate-up migrate-down migrate-status fmt vet test lint build

help: ## List available targets (default)
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z0-9_-]+:.*## / {printf "  %-25s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

run-trackline: ## Run Trackline's Go backend and React frontend locally (start docker-up first)
	$(MAKE) -j7 run-auth run-project run-issue run-activity run-ai run-gateway run-frontend

run-bug-issue-tracker: run-trackline ## Legacy alias for run-trackline

run-backend: ## Run all Go services and the gateway locally (start docker-up first)
	$(MAKE) -j6 run-auth run-project run-issue run-activity run-ai run-gateway

run-frontend: ## Run the Vite development server locally
	VITE_API_TARGET=http://127.0.0.1:8082 npm --prefix frontend run dev

run-auth:
	@set -a; . ./.env; set +a; AUTH_ADDR=:8083 AUTH_DATABASE_URL="root:$${MYSQL_ROOT_PASSWORD}@tcp(127.0.0.1:$${MYSQL_PORT:-3306})/auth_db?parseTime=true" go run ./services/auth

run-project:
	@set -a; . ./.env; set +a; PROJECT_ADDR=:8084 PROJECT_DATABASE_URL="root:$${MYSQL_ROOT_PASSWORD}@tcp(127.0.0.1:$${MYSQL_PORT:-3306})/project_db?parseTime=true" AUTH_SERVICE_URL=http://127.0.0.1:8083 go run ./services/project

run-issue:
	@set -a; . ./.env; set +a; ISSUE_ADDR=:8085 ISSUE_DATABASE_URL="root:$${MYSQL_ROOT_PASSWORD}@tcp(127.0.0.1:$${MYSQL_PORT:-3306})/issue_db?parseTime=true" PROJECT_SERVICE_URL=http://127.0.0.1:8084 NATS_URL="nats://127.0.0.1:$${NATS_PORT:-4222}" go run ./services/issue

run-activity:
	@set -a; . ./.env; set +a; ACTIVITY_ADDR=:8086 ACTIVITY_DATABASE_URL="root:$${MYSQL_ROOT_PASSWORD}@tcp(127.0.0.1:$${MYSQL_PORT:-3306})/activity_db?parseTime=true" PROJECT_SERVICE_URL=http://127.0.0.1:8084 NATS_URL="nats://127.0.0.1:$${NATS_PORT:-4222}" go run ./services/activity

run-ai:
	@set -a; . ./.env; set +a; AI_ADDR=127.0.0.1:8087 GATEWAY_INTERNAL_URL=http://127.0.0.1:8082 go run ./services/ai

run-gateway:
	@set -a; . ./.env; set +a; GATEWAY_ADDR=:8082 AUTH_SERVICE_URL=http://127.0.0.1:8083 PROJECT_SERVICE_URL=http://127.0.0.1:8084 ISSUE_SERVICE_URL=http://127.0.0.1:8085 ACTIVITY_SERVICE_URL=http://127.0.0.1:8086 AI_SERVICE_URL=http://127.0.0.1:8087 go run ./gateway

docker-up: ## Start only MySQL and NATS, then apply migrations
	$(DC) --profile app stop auth project issue activity gateway frontend
	$(DC) up -d mysql nats
	$(MAKE) migrate-up MIGRATE_DATABASE=$(MIGRATE_DATABASE)

docker-app-up: ## Build and start the complete containerized application
	$(DC) --profile app up --build -d

docker-status: ## Show Compose container status
	$(DC) --profile app ps -a

docker-down: ## Stop Compose containers without removing database volumes
	$(DC) down

migrate-up: ## Apply pending migrations (default: all databases; override MIGRATE_DATABASE)
	$(DC) run --rm -e MIGRATE_DATABASE=$(MIGRATE_DATABASE) $(MIGRATE_SERVICE)

migrate-down: ## Roll back MIGRATE_STEPS (default 1) in selected MIGRATE_DATABASE (default auth_db)
	$(DC) run --rm --entrypoint /bin/sh $(MIGRATE_SERVICE) -c \
		'case "$(MIGRATE_STEPS)" in ""|*[!0-9]*|0) echo "MIGRATE_STEPS must be a positive integer" >&2; exit 2;; esac; migrate -path /migrations/$(MIGRATE_TARGET_DB) -database "mysql://root:$$MYSQL_ROOT_PASSWORD@tcp(mysql:3306)/$(MIGRATE_TARGET_DB)?multiStatements=true" down $(MIGRATE_STEPS)'

migrate-status: ## Show applied version, pending migrations, or dirty/error state (select MIGRATE_DATABASE)
	$(DC) run --rm --entrypoint /bin/sh $(MIGRATE_SERVICE) -c \
		'set -eu; path=/migrations/$(MIGRATE_TARGET_DB); url="mysql://root:$$MYSQL_ROOT_PASSWORD@tcp(mysql:3306)/$(MIGRATE_TARGET_DB)?multiStatements=true"; \
		if ! applied=$$(migrate -path "$$path" -database "$$url" version 2>&1); then echo "ERROR: $$applied"; exit 1; fi; \
		case "$$applied" in *[!0-9]*|"") echo "ERROR: unexpected migration version: $$applied"; exit 1;; esac; \
		printf "Applied version: %s\n" "$$applied"; \
		pending=$$(for file in "$$path"/*.up.sql; do version=$${file##*/}; version=$${version%%_*}; if [ "$$version" -gt "$$applied" ]; then printf "%s\n" "$$version"; fi; done); \
		if [ -n "$$pending" ]; then printf "Pending versions:\n%s\n" "$$pending"; else echo "Status: up to date"; fi'

fmt: ## Format Go source files
	find gateway pkg services -type f -name '*.go' -print0 | xargs -0 gofmt -w

vet: ## Run go vet across all Go packages
	go vet ./...

test: ## Run Go tests with the race detector
	go test -race ./...

lint: ## Run golangci-lint
	golangci-lint run

build: ## Build Go packages and the frontend
	go build ./...
	npm --prefix frontend run build
