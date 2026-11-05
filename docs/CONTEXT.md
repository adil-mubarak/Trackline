# Trackline project context

Trackline is a Go microservices project with a React/Vite frontend. `Trackline.md`
is the available phased roadmap; root guidance refers to
`Build_Plan_Bug_Issue_Tracker.md`, which is absent. Services communicate through
the gateway and service APIs, and each service owns its database. Auth, Project,
Issue, Activity, and AI use `auth_db`, `project_db`, `issue_db`, `activity_db`,
and `ai_db`, respectively. No service should query another service's tables.

The gateway validates bearer JWTs and sets `X-User-ID` / `X-User-Role` on
internal requests. Database schemas are managed with parameterized SQL and
golang-migrate migrations. Compose has an idempotent database bootstrap before
migrations so a pre-existing MySQL volume gains newly introduced databases
without losing its data. Run `make docker-up` before starting local services;
`make run-ai` requires the migrated `ai_db` and an OpenRouter API key for AI
readiness.

## AI and OAuth

- AI chat history is owned by `ai_db`, scoped to the authenticated user, kept
  for 90 days, and can be deleted by its owner. Expired chats are purged hourly.
  Prior turns are saved for display but are not sent as model context.
- Activity evidence sent to the model resolves actor IDs to usernames first;
  unresolved actors are labeled “Unknown user.” Do not expose raw actor IDs in
  activity answers.
- A separate `services/mcp` process provides stateless Streamable HTTP for
  external clients at the Gateway's `/mcp` route. It has no database or public
  port. Claude hosted connectors use Auth-owned OAuth authorization code + PKCE
  and DCR; compatible developer clients may use Trackline bearer tokens. OAuth
  grants are checked through Auth and restricted by Gateway to the MCP route and
  specific read-only tool APIs. OAuth state is in `auth_db` migration
  `000006_mcp_oauth`; MCP remains DB-free. AI chat uses private
  `MCP_INTERNAL_URL`, forwards caller identity and credentials, and remains
  unchanged. GitHub App tools remain local to AI.
- Standalone MCP clients connect to Gateway `/mcp`, not the private MCP
  listener. Claude hosted connectors require a public HTTPS issuer configured
  as `MCP_OAUTH_ISSUER_URL`; `MCP_RESOURCE_URL` defaults to issuer + `/mcp`.
  `MCP_OAUTH_INTERNAL_SECRET` is shared by Auth/Gateway for introspection and
  `MCP_USERNAME_LOOKUP_SECRET` by Gateway/MCP for private OAuth name enrichment.
  Render generates both secrets; set the issuer in Render environment settings.
  See `docs/MCP_SERVER.md` before configuring Claude. Local MCP remains on
  loopback `127.0.0.1:8088`; the service exposes seven read-only tools and calls
  caller-authenticated Gateway APIs.
- Known unresolved challenge: accessible-project listing has reportedly failed
  in the deployed environment. `list_projects` maps to `GET /projects`, but no
  reproducer or root cause has been established. Treat the cause as unknown;
  compare sanitized request IDs and status codes across Gateway/MCP/API logs
  during a deployment-specific investigation. No deployed fix or successful
  listing result is asserted.
- AI charts are deterministic tracker issue-count summaries for issues marked
  done. User charts are alphabetical, unranked counts and explicitly not
  performance scores. Do not infer employee productivity or work quality.
- Google local OAuth uses origin `http://localhost:5173` and redirect URI
  `http://localhost:8082/api/auth/oauth/google/callback`. Configure
  `OAUTH_CALLBACK_BASE_URL=http://localhost:8082`, and ensure the OAuth client ID
  in `.env` is the client whose redirect URI is registered in Google Cloud.
  Restart the auth process after changing environment variables.
- The floating Ask Copilot launcher is hidden while the full AI Copilot page is
  open; it remains available elsewhere in the workspace.

## Verification status

Go race tests, `go vet ./...`, frontend tests, and frontend production build have
passed for the AI/history/chart work. The frontend build emits a non-blocking
large-bundle warning. The focused Copilot launcher regression test and complete
frontend suite/build also passed. `docker compose config --quiet`, Render startup
shell syntax, and `git diff --check` passed after the database bootstrap change.
The full Compose stack and Render deployment have not been run for standalone
MCP OAuth, so live Claude connector behavior still needs environment-specific
verification. OAuth handler tests cover DCR, PKCE code exchange, refresh
rotation/replay revocation, introspection, and Gateway scope enforcement.
`go test -race ./...`, `go vet ./...`, `golangci-lint run`, Compose/YAML/shell
checks, and Auth/Gateway/MCP Docker builds passed. The Auth OAuth migration was
applied and rolled back on an isolated MySQL 8.4 container.

Automated handler tests use SQL mocks and HTTP test servers; container-backed
database integration tests are not yet available. The Playwright smoke flow
covers registration, login, project and issue creation, issue editing, filtering,
and status changes against the Compose gateway. The repeatable API seed script
is `scripts/seed.py`. Issue sorting, work-order, and label autocomplete and
authenticated username autocomplete are implemented and covered by unit tests.
