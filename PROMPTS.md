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
  this prompt log and `docs/CONTEXT.md` current.
- Roadmap note: `Build_Plan_Bug_Issue_Tracker.md` is referenced by the root
  guidance but is absent; `Trackline.md` is the available phased roadmap.
