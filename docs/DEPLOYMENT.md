# Trackline deployment: Render Free + external MySQL

This setup deploys Trackline as **one Render Free web service** containing the
gateway, the four tracker Go service processes, the standalone MCP service, an
optional AI service, and a local NATS broker. Only the gateway listens on
Render's public port; the other services and NATS bind to loopback. MySQL runs
separately on a MySQL provider such as Aiven Free, and
the static frontend remains on GitHub Pages.

This is a demo deployment, not production. Render Free services spin down after
15 minutes without inbound traffic, can restart, and have no persistent local
disk. Expect a cold start when the service wakes. The local NATS broker is
ephemeral, so events published while the service is down can be lost. Confirm
current Render and database-provider limits before relying on this setup.

## 1. Prepare the external MySQL database

Create a MySQL service with TLS enabled and download its CA certificate. Aiven
offers a free MySQL tier, but eligibility, availability, size limits, and
terms can change. Record its hostname, port, and administrative credentials
privately. Do not use Render's free Postgres database: Trackline requires
MySQL.

Connect to MySQL using the provider's secured connection method, then create
the four required service-owned databases and separate users. If enabling AI,
also create its optional database and user. Replace the example
passwords with random hexadecimal values (for example, from `openssl rand
-hex 32`):

```sql
CREATE DATABASE auth_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE DATABASE project_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE DATABASE issue_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE DATABASE activity_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
-- Optional, only when enabling AI chat:
CREATE DATABASE ai_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE USER 'trackline_auth'@'%' IDENTIFIED BY 'REPLACE_WITH_AUTH_HEX_PASSWORD';
CREATE USER 'trackline_project'@'%' IDENTIFIED BY 'REPLACE_WITH_PROJECT_HEX_PASSWORD';
CREATE USER 'trackline_issue'@'%' IDENTIFIED BY 'REPLACE_WITH_ISSUE_HEX_PASSWORD';
CREATE USER 'trackline_activity'@'%' IDENTIFIED BY 'REPLACE_WITH_ACTIVITY_HEX_PASSWORD';
-- Optional, only when enabling AI chat:
CREATE USER 'trackline_ai'@'%' IDENTIFIED BY 'REPLACE_WITH_AI_HEX_PASSWORD';

GRANT ALL PRIVILEGES ON auth_db.* TO 'trackline_auth'@'%';
GRANT ALL PRIVILEGES ON project_db.* TO 'trackline_project'@'%';
GRANT ALL PRIVILEGES ON issue_db.* TO 'trackline_issue'@'%';
GRANT ALL PRIVILEGES ON activity_db.* TO 'trackline_activity'@'%';
-- Optional, only when enabling AI chat:
GRANT ALL PRIVILEGES ON ai_db.* TO 'trackline_ai'@'%';
```

If the provider restricts user creation or grants, follow its supported method
for creating database-scoped users. Do not give application accounts access to
other services' databases. Add the Render service's outbound IP ranges to the
database provider's allowlist if available; do not disable TLS to work around
connection problems.

## 2. Create the Render service

1. Push this repository to GitHub's `main` branch.
2. In Render, choose **New → Blueprint**, connect the repository, and apply
   `render.yaml`. It creates one Docker-based `free` web service named
   `trackline-backend`.
3. When prompted, set the service environment variables below. Keep them in
   Render's environment settings; never commit their values.

For each service, set both its Go SQL DSN and migration URL. Use the same
service-specific database username/password for that pair, the provider's
hostname and port, and the `trackline` TLS profile. Paste the downloaded CA
certificate into `MYSQL_TLS_CA_PEM`; the startup script writes it to a
temporary file, and both the Go services and migration tool verify the server
certificate. The hex passwords avoid URL-escaping problems.

| Render variable | Value format |
|---|---|
| `AUTH_DATABASE_URL` | `trackline_auth:PASSWORD@tcp(HOST:PORT)/auth_db?parseTime=true&tls=trackline` |
| `AUTH_MIGRATE_DATABASE_URL` | `mysql://trackline_auth:PASSWORD@tcp(HOST:PORT)/auth_db?multiStatements=true&tls=trackline&x-tls-ca=%2Ftmp%2Ftrackline-mysql-ca.pem` |
| `PROJECT_DATABASE_URL` | `trackline_project:PASSWORD@tcp(HOST:PORT)/project_db?parseTime=true&tls=trackline` |
| `PROJECT_MIGRATE_DATABASE_URL` | `mysql://trackline_project:PASSWORD@tcp(HOST:PORT)/project_db?multiStatements=true&tls=trackline&x-tls-ca=%2Ftmp%2Ftrackline-mysql-ca.pem` |
| `ISSUE_DATABASE_URL` | `trackline_issue:PASSWORD@tcp(HOST:PORT)/issue_db?parseTime=true&tls=trackline` |
| `ISSUE_MIGRATE_DATABASE_URL` | `mysql://trackline_issue:PASSWORD@tcp(HOST:PORT)/issue_db?multiStatements=true&tls=trackline&x-tls-ca=%2Ftmp%2Ftrackline-mysql-ca.pem` |
| `ACTIVITY_DATABASE_URL` | `trackline_activity:PASSWORD@tcp(HOST:PORT)/activity_db?parseTime=true&tls=trackline` |
| `ACTIVITY_MIGRATE_DATABASE_URL` | `mysql://trackline_activity:PASSWORD@tcp(HOST:PORT)/activity_db?multiStatements=true&tls=trackline&x-tls-ca=%2Ftmp%2Ftrackline-mysql-ca.pem` |
| `OPENROUTER_API_KEY` | Optional; set to enable the AI service |
| `AI_DATABASE_URL` | Required only with AI: `trackline_ai:PASSWORD@tcp(HOST:PORT)/ai_db?parseTime=true&tls=trackline` |
| `AI_MIGRATE_DATABASE_URL` | Required only with AI: `mysql://trackline_ai:PASSWORD@tcp(HOST:PORT)/ai_db?multiStatements=true&tls=trackline&x-tls-ca=%2Ftmp%2Ftrackline-mysql-ca.pem` |
| `MCP_INTERNAL_URL` | Optional; defaults to `http://127.0.0.1:8088/mcp` for the colocated private MCP process. Set only to a trusted private HTTP(S) MCP endpoint. |
| `CORS_ALLOWED_ORIGINS` | Exact Pages origin, e.g. `https://YOUR_GITHUB_USERNAME.github.io` |
| `MYSQL_TLS_CA_PEM` | Contents of the MySQL provider's PEM CA certificate |
| `JWT_SECRET` | Generated by the Blueprint; keep the generated value |

OAuth sign-in is optional. To enable it, set the corresponding client ID and
secret in Render (`GOOGLE_OAUTH_CLIENT_ID` / `GOOGLE_OAUTH_CLIENT_SECRET` or
`GITHUB_OAUTH_CLIENT_ID` / `GITHUB_OAUTH_CLIENT_SECRET`), then set:

- `OAUTH_CALLBACK_BASE_URL` to the Render service origin, e.g.
  `https://trackline-backend.onrender.com` (no trailing slash).
- `OAUTH_FRONTEND_URL` to the deployed Pages app URL, including its repository
  path if applicable, e.g. `https://YOUR_GITHUB_USERNAME.github.io/Trackline/`.

Register the exact provider callback URL: Google uses
`https://trackline-backend.onrender.com/api/auth/oauth/google/callback`; GitHub
uses `https://trackline-backend.onrender.com/api/auth/oauth/github/callback`.
For Google, the authorized JavaScript origin is the Pages origin only (no path).
Make sure the credentials belong to the OAuth client/application configured in
Render. Do not enable a provider until its secrets and callback URL are set.

Optional GitHub App evidence requires `GITHUB_APP_ID`,
`GITHUB_INSTALLATION_ID`, and `GITHUB_PRIVATE_KEY`. Keep
`GITHUB_EVIDENCE_TO_OPENROUTER` and `GITHUB_CODE_TO_OPENROUTER` false unless
organization policy explicitly approves sending that evidence to the model.

Set the Blueprint's `MCP_OAUTH_ISSUER_URL` to the public HTTPS origin of this
Render service (for example, `https://trackline-backend.onrender.com`). The
OAuth resource URL defaults to that origin plus `/mcp`. Render generates the
private `MCP_OAUTH_INTERNAL_SECRET` shared by Auth and Gateway and
`MCP_USERNAME_LOOKUP_SECRET` shared by Gateway and MCP; if you create the
service manually, set different random 32+ character secrets for each pair.

The Blueprint sets the web service's `PORT` to `10000` and the health check to
`/ready`. The startup script starts the public gateway on the assigned port
first, starts NATS on loopback, starts the private MCP service, applies the four
core service migrations (and the AI migration when AI is enabled), then starts
the tracker services on loopback. The Gateway routes `/mcp` to the private MCP
listener; external MCP clients use the Gateway HTTPS URL. Claude hosted
connectors authenticate through OAuth; compatible developer clients may still
send a Trackline bearer token. The gateway's `/ready` endpoint probes only the
Auth, Project, Issue, and Activity services' `/ready` endpoints. Optional AI and
MCP services do not affect gateway readiness; MCP metadata and internal secrets
are never used as probe targets. A healthy `/ready` therefore confirms core
tracker readiness, not AI or MCP availability. Check the deploy logs
for sanitized migration output and TLS errors; database URL credentials are
redacted.

When AI is enabled, it connects to the separately started loopback MCP process
at `http://127.0.0.1:8088/mcp` (the AI service's `MCP_INTERNAL_URL` default).
Do not set this variable to the public Gateway `/mcp` route: that route is for
external MCP clients. AI forwards the caller's credential and verified
identity to the configured private MCP endpoint, so do not point it at an
untrusted host. GitHub tools remain local to AI. This deployment wiring has not
been tested against a deployed external MCP server, and does not establish that
a reported deployed Copilot issue is fixed; see [`AI_MCP.md`](AI_MCP.md) for
troubleshooting.

Once deployed, copy the service's HTTPS URL from Render, for example
`https://trackline-backend.onrender.com`.

## 3. Point GitHub Pages at the backend

In the GitHub repository, open **Settings → Secrets and variables → Actions →
Variables** and create the repository variable:

- Name: `VITE_API_BASE_URL`
- Value: the Render service's HTTPS origin, e.g. `https://trackline-backend.onrender.com`

Then select **Settings → Pages → Build and deployment → GitHub Actions** and
run the `Deploy frontend to GitHub Pages` workflow (or push to `main`). The
workflow rejects missing or malformed API URLs, builds the frontend using the
repository-specific Pages base path, and publishes only `frontend/dist`.

The Pages origin in `CORS_ALLOWED_ORIGINS` must exactly match the browser's
origin—scheme and hostname only, with no repository path. The backend returns
CORS headers only for that configured origin.

## 4. Verify and understand the limits

Open the Render service's `/health` or `/ready` endpoint, then open the Pages
site and try registering/logging in. The first request after idle may take
about a minute while Render wakes the free service. Confirm the API service URL
in the browser's network tools if calls fail.

- Do not deploy the four internal Go services as separate public free web
  services: they trust identity headers added by the gateway.
- Keep all provider credentials in Render environment variables. Never put
  database URLs or passwords in GitHub Actions variables or source code.
- Back up MySQL independently and test restoring it. Render's local filesystem
  is not persistent; NATS state is disposable.
- Render Free and free database tiers can change, restart, sleep, or hit quotas.
  This arrangement is suitable for learning/demo use, not sensitive data or
  availability guarantees.
