# Prompt log

- Initial prompt: Build Phase 0 foundation (monorepo structure, Docker Compose
  and MySQL setup, migrations, reusable health-check template, and CI), then
  proceed through the phases in the build plan in order.
- Completion prompt: finish the remaining tracker roadmap in order, add service
  and integration tests, safe issue sorting/query construction, dependency
  ordering, DSA benchmarks/autocomplete, frontend flows, seed data, and final
  documentation/verification. Work continued through the Issue API, tests,
  trie-backed API endpoints, migration hardening, benchmarks, and seed script.
- AI and OAuth prompts: Preserve existing UI/OAuth work; resolve activity actor
  IDs to names; store user-owned AI chat history for 90 days with deletion;
  return factual project/user issue-count charts without rankings or performance
  scores; and configure OAuth callback URLs. The AI implementation adds its own
  `ai_db`, hourly expiry cleanup, deterministic charts for issues marked done,
  and name enrichment before activity evidence reaches the model.
- Follow-up prompts: Bootstrap missing service databases on existing Compose
  MySQL volumes without deleting data; hide the floating Copilot launcher on
  the full AI page; clarify local Google OAuth redirect configuration; and keep
  this prompt log and `docs/CONTEXT.md` current. Add a separately runnable,
  bearer-authenticated Streamable HTTP MCP service behind the Gateway while
  leaving AI Copilot's in-process adapter unchanged.
- Roadmap note: `Build_Plan_Bug_Issue_Tracker.md` is referenced by the root
  guidance but is absent; `Trackline.md` is the available phased roadmap.
- MCP documentation follow-up: document the standalone MCP architecture,
  protocol tool discovery/calls, setup, prompt/context records, and known
  challenges without claiming that an external host or deployed service was
  verified. The accessible-project listing error reported for the deployed
  Render/Aiven environment remains undiagnosed; its cause is unknown.
- Claude hosted connector follow-up: add Auth-owned OAuth authorization code
  + PKCE, DCR, refresh rotation/replay revocation, Gateway introspection and
  scope-limited read-only MCP access; keep legacy bearer-token clients and the
  AI MCP integration working. Deploy configuration and live Claude/Render
  connection still require environment-specific verification.
