# AI to standalone MCP connection

The AI chat service connects to the private Streamable HTTP MCP endpoint at
`MCP_INTERNAL_URL` (default `http://127.0.0.1:8088/mcp`). Compose overrides it
with `http://mcp:8088/mcp` for service-to-service networking. Render's
single-container startup runs MCP on loopback, so the default URL applies
there. In both deployments the MCP process is not exposed to the public
network. The authenticated Gateway's public `/mcp` route remains available for
external MCP clients; it is not the endpoint AI should use.

For local Compose, copy `.env.example` to `.env` and start the app with
`make docker-app-up`; Compose supplies the private MCP URL and waits for MCP's
health check before starting AI. For a local process-based run, `make
run-trackline` starts both AI and MCP, and their loopback defaults match. For
Render, `MCP_INTERNAL_URL` is optional because the AI service has the loopback
default; set it only when intentionally using a different trusted private
endpoint. See [`DEPLOYMENT.md`](DEPLOYMENT.md).

For each authenticated, tool-enabled chat, AI supplies the caller's Bearer token, Gateway-verified
`X-User-ID`, and bounded `X-Request-ID` on MCP requests. The MCP service
forwards the caller token to Gateway APIs, which continue enforcing the
caller-scoped permissions. The model provider receives neither identity nor
credentials. Remote tool schemas are discovered from MCP and offered to the
model; calls, rounds, result sizes, and deadlines remain bounded.

Set `MCP_INTERNAL_URL` only to a trusted private HTTP(S) endpoint. Do not point
it at an untrusted server: the AI sends the caller's bearer credential there.
The AI client does not follow redirects, and user-facing MCP failures are
sanitized rather than returning server error details. GitHub tools remain
local to AI and are independently feature-gated; they are not discovered from
the standalone MCP server.

## Troubleshooting

- If AI chat reports that tracker tools are unavailable, check that the private
  MCP process is healthy and that `MCP_INTERNAL_URL` resolves from the AI
  process. Compose uses `http://mcp:8088/mcp`; loopback deployments use
  `http://127.0.0.1:8088/mcp`.
- Verify that the private endpoint includes the `/mcp` path and is reachable
  over HTTP(S). The MCP server also has a `/health` endpoint for process health;
  successful health does not prove that MCP tool calls or downstream tracker
  APIs succeed.
- Confirm the request reaches the authenticated Gateway as expected and inspect
  sanitized logs/request IDs across AI, MCP, and Gateway. Do not log or share
  bearer tokens.
- To connect an external MCP host, use the public Gateway URL ending in `/mcp`
  with a Trackline bearer token; do not substitute that URL for AI's private
  `MCP_INTERNAL_URL`.

Local automated tests cover the AI-to-MCP caller identity and tool path. No
external deployment test was performed, and this change does not claim that a
reported deployed Copilot issue is fixed.
