# Debug Claude MCP locally through HTTPS

This reproduces the connector flow against **local Compose data**, not Render.
It does not prove the production environment has identical configuration.
Requires Docker Compose, Python 3, and a separately installed `cloudflared`
(or another trusted HTTPS tunnel). No OpenRouter key is required for an external
Claude connector. Revoke the previously exposed Claude sessions and change the
exposed Trackline password before testing.

## 1. Prepare local configuration

If `.env` does not exist, copy `.env.example` to `.env`; do not overwrite an
existing file. Set strong, local-only `JWT_SECRET` and `MYSQL_ROOT_PASSWORD`
values. Review the configuration and do not use production database credentials
or real customer data. Stop any process already using the local app ports.
The command below starts/builds the existing complete Compose app, including
its database migrations. It preserves existing local database volumes.

## 2. Start a temporary tunnel (terminal A)

With the default Gateway port:

```sh
cloudflared tunnel --url http://127.0.0.1:8082
```

Leave it running. Copy the HTTPS origin printed by cloudflared, for example
`https://your-random-name.trycloudflare.com`. If `.env` changes `GATEWAY_PORT`,
use that port instead. Only tunnel the Gateway; never tunnel MySQL, Auth, or
the private MCP port. The tunnel makes Gateway APIs publicly reachable, so use
only a short-lived debug session and keep normal authentication enabled.

## 3. Start the debug backend (terminal B)

From the repository root, substitute your actual tunnel origin:

```sh
make debug-mcp PUBLIC_ORIGIN=https://your-random-name.trycloudflare.com
```

This runs Compose in the foreground with stdout logs. The helper sets matching
OAuth issuer/resource URLs and generates separate, temporary shared internal
secrets for the containers. It does not print or save secrets or edit `.env`.
Restarting this command changes internal secrets; use it consistently rather
than mixing normal Compose commands into a running debug session.

Wait for the app to become healthy. Open the local frontend (default
`http://localhost:3000`; see `FRONTEND_PORT` in Compose), register a test account
if needed, and create a sample project. Your Render login is not automatically
available in the local database.

## 4. Add a separate connector in Claude

Use the name **Trackline Local Debug**, URL
`https://your-random-name.trycloudflare.com/mcp`, **Sign in now**, and
**Register automatically**. No custom headers or manually supplied client ID.
Sign in using your **local test account** and allow the browser to return to
Claude automatically. Never reuse an old login tab when the tunnel URL changes.

## 5. Read terminal B, not browser payloads

Look for `connector request completed` events for `/oauth/token`,
`/internal/oauth/introspect`, and `/mcp`. These include status, service,
request ID, and an allowlisted OAuth error category. A token failure narrows the
problem to OAuth exchange; token success followed by MCP failure narrows it to
MCP authorization/protocol handling. An initial MCP 401 is normal discovery.
An open streaming request may log completion only when closed.

Share only these sanitized events. Do not share full terminal dumps, browser
headers, passwords, cookies, authorization URLs, token responses, or HAR files.

## 6. Shut down

Press Ctrl+C in terminal A to remove public access, then Ctrl+C in terminal B
to stop the foreground Compose app. Remove the temporary Claude connector.
Do not use `down -v`: local data should remain intact. A new quick tunnel gets
a new URL; restart debugging and add a fresh connector with that exact origin.
