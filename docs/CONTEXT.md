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
  authenticated external MCP clients at the Gateway's `/mcp` route. It has no
  database or public port; clients use Trackline bearer tokens, and its
  read-only tools call owning APIs through the Gateway. AI chat is a Streamable
  HTTP client of this service through private `MCP_INTERNAL_URL`; it forwards
  the caller's bearer token, Gateway-verified user ID, and bounded request ID.
  GitHub App tools remain local to AI and are not part of the standalone service.
- Standalone MCP clients connect to the Gateway URL `/mcp` with a Trackline
  bearer token, not the private MCP listener. The endpoint is documented in
  `docs/MCP_SERVER.md`; local `make run-mcp` configures the private service for
  `127.0.0.1:8088` and its Gateway upstream. The implementation registers seven
  read-only tools and uses caller-authenticated Gateway APIs. The official Go
  SDK test covers protocol tool listing and a `get_project` call with token
  forwarding. This test is not a deployed Render/Aiven check or an external
  host integration test.
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
The database bootstrap and standalone MCP have not yet been exercised against a
running Compose stack, and Render deployment and external OAuth provider credentials/callbacks still need
environment-specific verification. Browser-level OAuth behavior has not been
verified end-to-end.

Automated handler tests use SQL mocks and HTTP test servers; container-backed
database integration tests are not yet available. The Playwright smoke flow
covers registration, login, project and issue creation, issue editing, filtering,
and status changes against the Compose gateway. The repeatable API seed script
is `scripts/seed.py`. Issue sorting, work-order, and label autocomplete and
authenticated username autocomplete are implemented and covered by unit tests.
