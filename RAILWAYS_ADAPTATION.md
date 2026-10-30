# Switchyard <- Railways adaptation map

Goal: keep Magpie's UI/UX (layout, navigation, cards, modals, pickers,
dark theme) and replace its data and concepts with Railways, the user's
existing relay. This document is the contract for that adaptation. It is
not a claim that the adaptation is done: the frontend in this repo is
still a Magpie UI preview with in-memory seed data.

## Railways facts the UI must match (not Magpie facts)

- Clients use one base URL + one client token, OpenAI format only.
  Clients use four aliases: `coder`, `fast`, `reasoning`, `vision`.
  A client may also direct-pin by exact modelId, bypassing rotation.
- Routing inside an alias: weighted rotation (user order -> weights
  4/3/2/rest 1, smooth weighted round-robin), quota-aware (healthy and
  non-exhausted only), size split (small = whole pool, large =
  high-capacity only, large first-pick excludes only Groq), Kaggle
  rotation share cap 60%, direct-pin never rotates, failover after
  first pick keeps the original candidate/score order.
- Hard filters, in order: state, capability (vision/tools/JSON),
  private (`allowSensitive`), private-only, `maxContext` vs estimated
  tokens, daily quota. Circuit breaker: opens after 3 consecutive
  failures, backoff capped at 900s, half-open trial, 400s do not count.
  Key side effects: 429 -> cooldown (doubling, cap 900s) or exhausted
  on daily keywords; 402/quota -> exhausted (manual clear); 401 -> key
  disabled; 403 -> route unavailable for 12h; timeout/5xx -> breaker
  only.
- Kaggle: Model Proxy key, 2h TTL, refresh-before 1200s, failure keeps
  the old key, $10/day budget, notebook tunnel, 120s timeout tier.
  Other timeout tiers: fast non-streaming 60s, reasoning
  streaming 45s / non-streaming 60s.
- Privacy: mode is currently OFF. `allowSensitive` / `privateOnly`
  are routing switches only, NOT encryption or redaction; content is
  forwarded verbatim. The secretary route is "fewer providers see it",
  not "nobody sees it". The UI must never present masking previews as
  server-side privacy protection.
- Accounting: `billed_tokens` is upstream usage tokens, NOT money.
  There is no prompt caching (`cached_tokens` is 0). Audit keeps only
  the latest 200 rows. `quota_counters` is per key per UTC day.
- Client endpoints only: `POST /v1/chat/completions` (+ `/api/v1`
  alias) and `GET /v1/models` (+ alias). No `/v1/messages`,
  no `/v1/responses`, no embeddings, no images, no audio, no
  Anthropic endpoint.

## View-by-view mapping

| Magpie UI view | Railways equivalent | Action |
| --- | --- | --- |
| Agents (Claude, Codex, Gemini CLI, OpenCode, Cursor, Zed) | Clients: Cline (main), Roo, other coding agents, chat UIs, scripts. Model picker becomes alias picker (`coder`/`fast`/`reasoning`/`vision`) + direct-pin modelId. | Keep picker interaction, replace agent list and model list. |
| Sessions | Recent requests / audit (latest 200 rows: timestamp, requested model, selected model, status, billed tokens, latency, validator result, failover chain). Railways has no conversation sessions; `session_key` in audit is per-request unique. | Replace data source and columns; do not invent session grouping. |
| Providers | Layer-3 routes + provider keys: route name, upstream model, base URL, masked key only (never plaintext), vision/tools/JSON capability, max context, route state, circuit state, key state / cooldown, daily budget per provider. | Keep provider card interaction, replace fields. |
| Routing (smart/order/rotate/usage/pace groups) | Aliases with ordered target lists, weighted rotation, quota-aware rotation, size split, Kaggle 60% cap, Groq large-request exclusion, direct-pin bypass. | Replace routing modes; do not keep smart/pace as Railways modes. |
| Gateway playground | Railways playground: alias or direct-pin test, small reply, tools test, validator result, failover chain, billed tokens, latency. Only `/v1/chat/completions`. | Drop `/v1/messages` and `/v1/responses`. Drop or clearly label masking preview as local preview only, never as server privacy. |
| Usage | Audit + quota: billed tokens (not USD), latency, status, per-key per-UTC-day totals, per-model table. Cost in USD must be removed; `cache_read` is always 0. | Replace cost columns with billed tokens; keep charts/tables interaction. |
| Library | Provider name -> base-URL presets, and Kaggle route templates only. No other vendor route templates. | Map only if useful; otherwise drop. |
| Plugins | No Railways equivalent. | Drop. Do not keep Magpie plugin packages as Railways features. |
| Settings | Privacy switch (off/low/high), secretary route, Kaggle timer status, provider presets. | Replace Magpie profiles/settings with these. |

## What this PR does and does not do

Does (Step 1, rename clean):
- `package.json`, `metadata.json`, `index.html`: name and description
  now say Switchyard, not magpie.
- Visible UI strings in `src/App.tsx`: logo, "Starting Switchyard
  gateway…", rewrite/sync sentences, console error text.
- `server.ts`: preview version now `switchyard-0.1.0-web`, console log
  says switchyard, `x-switchyard-agent` header, `switchyard_*` record
  ids, comments updated. Type names `MagpieState`/`MagpieSettings`
  renamed to `SwitchyardState`/`SwitchyardSettings` across
  `src/types.ts`, `src/App.tsx`, `server.ts`.
- `README.md` / `README.zh-CN.md`: rewritten as Switchyard READMEs
  (this repo's status, how to run the preview, roadmap). The original
  Magpie README remains in git history (initial commit) and in the
  original repo.

Does not do (pending, not silently dropped):
- Seed data in `server.ts` (`@magpie-community/*` plugin packages,
  agent models, provider lists, subscription windows) is still Magpie
  seed data. It must be replaced by real Railways data via the Go
  backend, not by hand-editing the seed to look like Railways.
- `docs/`, `AGENTS.md`, `CLAUDE.md`, `Dockerfile`, `LICENSE`,
  `THIRD_PARTY_NOTICES`, `bun.lock`: Magpie originals, kept as
  reference. `Dockerfile` does not build Switchyard yet.
- No Go backend, no SQLite, no encrypted-key handling, no real
  `/v1/chat/completions` routing in this PR. The Go backend (Step 3)
  must implement the Railways API contract above, and must be verified
  with real Cline-shaped large requests, not small tests, before
  cutover from the Node Railways on Ohio.

## UI burden rule

Do not add new controls by default. New Railways features must fit
the existing Magpie layout (existing views, cards, modals) unless the
user explicitly orders a new control. The privacy switch was an
explicit exception in Railways; it is not a licence to add more.
