# Architecture

The application is a Go monorepo with a gateway and independently owned
services. An AI service provides authenticated chat and calls OpenRouter; it
owns its transcript database (`ai_db`) and does not query tracker databases.
Auth, Project, Issue, and Activity each use a separate MySQL database
(`auth_db`, `project_db`, `issue_db`, and `activity_db`). The Compose setup
starts the MySQL and NATS dependencies, applies migrations, and starts all
services plus the static React frontend.

Services communicate through APIs rather than querying another service's
tables. The AI service stores successful chat turns scoped to authenticated
user IDs, supports owner-checked deletion, and purges transcripts after 90 days.
The Issue service calls Project for membership checks and caches the
result briefly in a bounded LRU. Issue-created and status-changed events are
published to NATS for Activity to consume. The gateway is the frontend's single
API entry point: it validates HS256 JWTs and forwards the authenticated user ID
and role to internal services. The frontend proxies `/api/*` to the gateway.
The gateway readiness endpoint probes the four required data services, and the
gateway drains requests on SIGINT/SIGTERM with a bounded shutdown period. AI is
optional and a provider outage does not make core tracker routes unready.

The AI service is routed through the Gateway at `/ai/chat`; the Gateway
validates the caller's JWT before forwarding. OpenRouter credentials are configured
only on the AI service. For tool-enabled chat, AI connects as a Streamable HTTP
MCP client to the separately running `services/mcp` process at
`MCP_INTERNAL_URL`. The default is `http://127.0.0.1:8088/mcp`; Compose sets
`http://mcp:8088/mcp`. Render's single-container startup starts MCP on
`127.0.0.1:8088`, so the AI default applies there as well. The MCP listener is
private and is not the public Gateway `/mcp` endpoint.

AI discovers the remote MCP tool schemas and supplies them to OpenRouter. On
MCP calls it forwards the caller's bearer token, Gateway-verified `X-User-ID`,
and bounded `X-Request-ID`; the MCP service then calls Gateway APIs with the
caller token so owning APIs enforce permissions. The caller token and identity
are not included in OpenRouter requests. AI rejects unapproved remote tools,
does not follow redirects on the MCP connection, and returns sanitized errors
for MCP tool failures. Calls remain capped at six across three rounds, each
tool result at 48 KiB, and aggregate tool evidence at 128 KiB. GitHub evidence
and code tools remain local to AI and independently gated by the configured App
and explicit opt-in flags; they are not advertised by the standalone MCP
service. The standalone service has no database and also serves external MCP
clients through the authenticated Gateway `/mcp` route.

Issue list filters are assembled in `services/issue/query.go`: filter values are
SQL parameters, while sort columns and directions come from fixed allowlists.
`after_id` keyset pagination is supported with the default `id` ordering only.
`GET /issues/work-order?project_id=…` returns issue IDs with dependencies before
the issues they block. The Issue and Auth services use their own trie indexes
for project label and authenticated username suggestions; Auth returns local
parts and user IDs rather than email addresses. Project membership calls carry
the gateway request ID and have a three-second client timeout.

The frontend enriches project membership IDs with usernames through the
authenticated, bounded `POST /users/names` Auth endpoint. Project continues to
own membership and stores only user IDs; it never queries `auth_db`. The UI
falls back to `User #<id>` when a name cannot be resolved.

Global admins can create, deactivate, and reactivate accounts in Auth. The
default Project collection is membership-scoped for regular users, while global
admins receive all projects because they have effective access to every project.
The explicit `GET /projects?scope=all` remains a compatible admin-only alias.
This effective membership is not persisted: Project reports the requesting admin
as a synthetic member in the member list, and Project's membership-check endpoint
grants the admin access only to an existing project. Issue forwards the verified
gateway role to Project and includes it in its bounded membership-cache key.
Assignee checks allow a global admin to assign themselves through effective
membership; any other assignee must have their own active, stored membership.
Account
deactivation revokes existing JWTs through the gateway's live session check but
preserves project memberships and issue history. Project validates target
account activity through Auth's internal status API before adding membership
and when checking an assignee; its Auth call is bounded to three seconds and
fails closed. Global admins may access all project boards and issues and manage
memberships using the Project API, while project managers remain limited to
projects they manage.
Project creation can include up to 50 selected active user IDs; Project validates
their account status before opening its database transaction, then creates the
project, creator-as-manager membership, and selected member memberships atomically.
The creation form obtains its selectable, paginated username-only directory from
Auth; the current user and inactive accounts are excluded, and email addresses
are never exposed by this endpoint.

Project deletion is a soft delete owned by Project: only the project creator
(`owner_id`) or a global admin can set `deleted_at`. Soft-deleted projects are
excluded from project listings and membership checks, but project-member rows and
Issue-service history are retained; Project never reads the Issue database.
Issue deletion is enforced by Issue and allowed to the issue reporter, current
assignee, or a global admin. Project card actions are shown only to project
creators and global admins; issue-card actions are shown only to reporters,
assignees, and global admins. Both user-facing actions require confirmation.

Issue events contain random event IDs and request IDs. Activity stores each
event under a unique event ID using an upsert, so duplicate delivery does not
create duplicate activity rows. The Activity service still uses NATS core
subscriptions (not a durable JetStream consumer); prolonged consumer downtime
can therefore lose events.

Compose defaults expose the frontend on port 3000, gateway on 8082, MySQL on
3306, and NATS on 4222. Override published ports in `.env` when those are
already in use. Development passwords are intentionally not production-safe.

## Workspace sections

The React frontend's **My issues** view fans out to the Issue service once per
accessible project, using the caller's `assignee_id` filter and keyset
pagination. Requests are limited to three projects concurrently. **Activity**
fetches up to 100 recent events per accessible project from Activity and merges
the results by event time; per-project failures are shown without hiding other
project results. These aggregations happen in the frontend, so neither Activity
nor Issue reads another service's data store.

**Analytics** is a current-state snapshot for the selected project. The Issue
service provides a membership-checked endpoint that calculates totals by status
and priority plus open high/critical counts directly from `issue_db`. It is not
a historical trend, velocity, or burndown report; the current event pipeline
does not guarantee a complete historical record.

Global administrators can list accounts and initiate password resets through
the Auth service. Auth stores only bcrypt password hashes; a reset sets a
temporary password and a reset-required flag in `auth_db`. The user must change
that password on the next login. The resulting JWT marks the session as
password-change-only, and the gateway rejects protected routes other than the
password-change endpoint until a refreshed token is issued. The admin UI never
shows current or temporary passwords after submission.
