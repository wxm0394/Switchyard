# Switchyard Go API contract (Phase 1)

Source of truth for this document: Node Railways, repo `wxm0394/railways`,
files `server.ts`, `server/db.ts`, `server/adminGuard.ts`,
`server/security.ts`, `server/scheduler.ts`, `server/adapters.ts`,
`server/validator.ts`, `server/types.ts`, `server/modelRouting.ts`.
Field names below are the Node field names. The Go backend must keep
them on the wire (snake_case in SQLite, camelCase in JSON) unless a
section explicitly says otherwise. No secret, key, token, password,
encrypted blob, or ciphertext value is ever printed, logged, or
returned by any endpoint in this contract; only column names, formats,
and masks are documented.

Status: Phase 1 contract + Phase 2 skeleton + Phase 3 core gateway
+ Phase 5 (tool-rescue, tool-args, Kaggle timer, privacy routing,
auto-enroll). NOT in this pass: catalog-watch mutation,
probe/onboard wizards beyond a stub, RackNerd deploy, cutover, or
ladder teardown. Those are later PRs. Phase 5 does NOT change the
React frontend in src/; frontend integration to the Go backend is
a later step.

## 0. Global rules

- JSON body limit: 10 MB (Node `express.json({ limit: '10mb' })`).
- Client API CORS: `Access-Control-Allow-Origin: *`, methods
  GET/POST/PATCH/DELETE/OPTIONS, headers Content-Type, Authorization,
  X-Requested-With, HTTP-Referer, X-Title, X-Admin-Password,
  X-Session-Id, X-Router-Task, X-Router-Private,
  X-Portal-Target-Model, X-Gateway-Heartbeat-Ms. OPTIONS returns 204.
- Admin API CORS: only origins listed in `GATEWAY_ADMIN_ORIGINS`
  receive an allow-origin header; OPTIONS returns 204 if the origin is
  allowed, else 403.
- Errors are JSON. Client errors use the OpenAI shape
  `{ error: { message, type, code } }`. Admin errors use
  `{ error, code? }` (see §1).
- Time: timestamps are ISO-8601 strings. `quota_counters.window_date`
  is a UTC `YYYY-MM-DD` date. Audit keeps only the latest 200 rows
  in the Node implementation; the Go implementation must enforce the
  same cap on insert (delete oldest beyond 200) until a later PR
  changes it deliberately.

## 1. Admin guard (server/adminGuard.ts)

Applies to every route under `/api/gateway` (middleware, before any
handler). Environment, all trimmed:

| Variable | Meaning |
| --- | --- |
| `GATEWAY_ADMIN_PASSWORD` | Admin password. Empty = not configured. |
| `NODE_ENV` | `production` enables the no-password refusal below. |
| `GATEWAY_ALLOW_NO_ADMIN_AUTH` | `true` = explicit opt-out of the password requirement in production. |
| `GATEWAY_ADMIN_ORIGINS` | Comma-separated extra browser origins allowed. |
| `GATEWAY_ALLOWED_HOSTS` | Comma-separated Host header values the admin API answers to; empty = not enforced. Entries may be a bare name or `name:port`. IPv6 bracket form `[::1]` is kept whole. |

Decision order, exactly as Node:

1. Host check (only if `GATEWAY_ALLOWED_HOSTS` is non-empty):
   Host missing -> refuse. Host lower-cased; compare the full
   `host` value and the bare name (before `:`) against the list.
   Refusal: 403 `{ error: "Host not allowed for the admin API", code: "admin_host_forbidden" }`.
2. Origin check: no `Origin` header -> allowed (curl/scripts are not
   browser traffic). `Origin: null` -> refuse. Listed origin ->
   allowed. Otherwise allowed only if the origin's host equals the
   request Host header. Refusal: 403
   `{ error: "Cross-origin admin requests are not allowed", code: "admin_origin_forbidden" }`.
3. Password: if no password is configured and production and
   `GATEWAY_ALLOW_NO_ADMIN_AUTH` is not `true` -> 503
   `{ error: "Admin API is disabled: set GATEWAY_ADMIN_PASSWORD (or GATEWAY_ALLOW_NO_ADMIN_AUTH=true to run without one)", code: "admin_password_not_configured" }`
   (deliberately no `requiresAdminPassword` flag in this case).
   If no password is configured and not production (or opt-out) ->
   allow. If a password is configured, the request must carry
   header `X-Admin-Password` equal to it (constant-time comparison
   after SHA-256 hashing both sides, so lengths never leak).
   Missing or wrong -> 401
   `{ error: "Admin password required", requiresAdminPassword: true }`.

The Go guard must reproduce this order and these exact status codes
and bodies. In particular, the route-reset endpoint is NOT exempt:
it is admin-guarded like every other `/api/gateway/*` route.

## 2. Client endpoints

### 2.1 GET /v1/models and GET /api/v1/models

No client-token auth in Node (public list). Response:

```json
{ "object": "list", "data": [ { "id": "<alias>", "object": "model", "created": 1735689600, "owned_by": "portal-alias", "permission": [], "root": "<alias>", "parent": null, "portal_type": "task_pool_alias", "upstream_model": "<target1> -> <target2>", "verified": true, "capabilities": { "vision": true, "tools": true, "json_mode": true, "streaming": true, "max_context": 128000, "task_role": "auto", "description": "<alias description>", "target_routes": ["<target1>"] }, "health_status": "<poolStatus>" } ] }
```

Rules: only aliases are listed, never direct routes. An alias is
skipped when its pool status is `no_routes` (usable count of
verified + untested targets == 0). `verified` is true only when
pool status is `callable`. `capabilities.max_context` is the fixed
128000 in the Node list handler (not per-route max context).
Pool status values: `no_routes | partial_invalid | untested |
callable` (see §5).

### 2.2 POST /v1/chat/completions and POST /api/v1/chat/completions

Auth, in order (Node `handleChatCompletions`):

1. Header `Authorization: Bearer <token>` (case-insensitive
   `Bearer` prefix, trimmed). Token lookup is by token value in
   `client_tokens` (unique). No match -> 401
   `{ error: { message: "Invalid or missing Portal Gateway API token. Use a valid sk-portal-* token from the console.", type: "invalid_request_error", code: "invalid_api_key" } }`.
2. Token `active` false -> 403
   `{ error: { message: "Token \"<name>\" is currently disabled.", type: "permission_error", code: "token_disabled" } }`.
3. Token `quota_limit` non-null and `used_tokens >= quota_limit` ->
   429 `{ error: { message: "Token \"<name>\" has reached its token quota limit (<used> / <limit>).", type: "insufficient_quota", code: "quota_exceeded" } }`.
4. Client rate limit: scheduler config `rate_limit_rpm` (default 120)
   per token, per minute window (Node in-memory window). Exceeded ->
   429 `{ error: { message: "Rate limit exceeded (<rpm> RPM). Gateway circuit breaker paused request to protect upstream quota.", type: "rate_limit_error", code: "client_rate_limited" } }`.

Request body (OpenAI chat completions, subset the gateway uses):
`model` (string), `messages` (array of `{role, content}` where
content is a string or an array of `{type: "text", text}` /
`{type: "image_url", image_url: {url}}` parts), `tools` (array),
`tool_choice`, `response_format` (`{type: "json_object"}` or
`{type: "json_schema"}`), `stream` (bool), `max_tokens`,
`temperature` and other pass-through fields (forwarded to upstream
as-is, except the gateway rewrites `model` to the route's
`upstream_model` and, for Gemini-type routes, converts the body;
the Go Phase 3 port covers OpenAI-compatible upstreams and keeps
the Gemini converter as a later, explicitly flagged gap if not
ported — see §7).

Headers that change behaviour:
`X-Portal-Target-Model` (explicit pin), `X-Session-Id`
(explicit session key), `X-Router-Task` (`vision|reasoning|fast`),
`X-Router-Private` (`true` = private).

Model resolution, in order:

1. `explicitPin` = `X-Portal-Target-Model` if present, else the
   scheduler config pinned model when `dispatch_mode == "pinned"`.
   If the pin is not an enabled route's `model_id` but matches an
   alias name, resolve it through that alias's target list and
   clear the pin (Node "Bug 3 Fix").
2. If no pin: `requestedModel` = `coder` when `model` is empty,
   `default`, or `auto`; otherwise the literal `model` string.
   If `requestedModel` matches an alias, `aliasTargets` = that
   alias's target list. Else if it matches an enabled route's
   `model_id`, `aliasTargets = [requestedModel]` (direct-pin).
3. If candidates are empty and `aliasTargets` was set and the
   model was implicit, retry with auto (no alias targets) and set
   response header `x-gateway-alias-fallback: auto` if that
   yields candidates.
4. No candidates at all -> 503
   `{ error: { message: "没有可用或健康的候选模型。当前任务需求: [<taskType>], 私密需求: [<isPrivate>], 需要图片支持: [是|否]。请在控制台添加对应可用模型或检查 API Key 状态。", type: "no_healthy_route", code: "no_route_available" } }`.
   Circuit-breaker/cooldown-blocked requests return this 503 and
   are NOT written to audit.

Classification (`classifyRequest`, server/scheduler.ts):
- Task type override first: `X-Router-Task` or model
  `auto:vision|auto:reasoning|auto:fast` wins.
- Else image part -> `vision`; tools or JSON mode -> `reasoning`
  (tools/JSON bypass fast directly); else estimated tokens > 3200
  -> `reasoning`, else `fast`.
- `estimatedTokens` = `ceil((CJK*0.68 + ascii*0.36 + toolsJSONLen*0.3) * 1.25)`,
  CJK = U+4E00–U+9FFF or U+3000–U+303F, tools overhead only when
  tools are present. Message text parts only; image parts do not
  add text tokens.
- Language: `chinese` if CJK-heavy, `english` if ASCII-letter-heavy,
  else `neutral` (Node `detectRequestLanguage`; exact thresholds
  are in the Go port's scheduler tests).
- Privacy: when scheduler `privacy_mode` is `off`, nothing is
  private regardless of headers or content. Otherwise private if
  `X-Router-Private: true` or model `auto:private`, or (high tier)
  content matches the full sensitive pattern list (API secret,
  private key, exchange names, trade strategy, auth token/bearer),
  or (low tier) only the key-like patterns. This contract does not
  change the current production mode, which is `off`.
- Session key: `X-Session-Id` if present, else
  `metadata.conversation_id|thread_id|project_id` or body
  `conversation_id|thread_id|user`, else `ephemeral_<requestId>`
  (non-explicit session).

Candidate selection and rotation: see §5. Failover: only before
the first byte. On upstream success the gateway writes one audit
row (see §4) and updates route metrics, quota counters, and (only
on HTTP 200 and only for explicit sessions) session affinity with
1800s TTL. On failure it applies §5 failure handling and tries the
next candidate in the original scored order (rotation only changes
the first pick, never the failover order).

Response: upstream response re-streamed for `stream: true`, else
the upstream JSON body returned as-is (with `model` as upstream
reported it). Validator failures (see §6) are treated as upstream
failures for failover, not as client errors.

## 3. Admin endpoints (all under /api/gateway, all admin-guarded)

Response envelopes are exactly as Node returns them. Fields named
below are the JSON field names. Any field that would carry a secret
is stripped: provider-key responses never include `encryptedKey`;
client-token responses include the token value only on creation
(Node `upsertClientToken` returns the full row; the Go port must
preserve that creation-only behaviour and never return token values
on list/overview responses — see the note on GET overview below).

### 3.1 GET /api/gateway/overview

Query: `logLimit` (default 50, clamped 1–200).
Response:

```json
{
  "config": { "<scheduler_config row>" },
  "providerKeys": [ { "<provider_key row without encryptedKey>", "counters": { "<quota_counters row or null>" } } ],
  "catalogWatch": { "<catalog-watch status object>" },
  "routes": [ { "<model_route row>", "effectiveState": "<EffectiveState>", "verificationStatus": "<RouteVerificationStatus>", "metrics": { "<route_metrics row>" }, "keyMask": "<mask or '未绑定Key'>", "providerName": "<name or 'Unknown Provider'>", "isColdStart": true, "timeoutTier": { "timeoutMs": 120000, "isColdStart": true, "tier": "cold_start" } } ],
  "tokens": [ { "<client_token row>" } ],
  "aliases": [ { "<alias with targetDetails/nextTarget, see below>" } ],
  "logs": [ { "<audit_log row>" } ],
  "stats": { "totalRequests": 0, "totalTokens": 0, "avgLatencyMs": 0, "activeTokensCount": 0, "rssMb": 0, "agentPool": { "<agent pool stats>" } }
}
```

Alias entry in overview: `{ id, alias, targetModels: string[],
description, createdAt, poolStatus, missingTargets, failedTargets,
verifiedTargets, untestedTargets, isActive, callableCount,
targetDetails: [ { modelId, upstreamModel, privateOnly, exists,
routeId?, effectiveState, verificationStatus, reliabilityScore,
successCount, failureCount, latencyP50, latencyP95, providerName,
keyMask, keyState, dailyTokenLimit, billedTokensToday,
quotaRemaining, quotaExhausted, schedulerScore, schedulerBreakdown } ],
nextTarget: "<modelId|null>", nextTargetUpstreamModel: "<string|null>" }`.
Missing targets use `upstreamModel: null, privateOnly: false,
exists: false, effectiveState: "missing",
verificationStatus: "unconfigured", reliabilityScore: null,
successCount: 0, failureCount: 0, latencyP50: 0, latencyP95: 0,
providerName: null, keyMask: null, keyState: null,
dailyTokenLimit: null, billedTokensToday: 0, quotaRemaining: null,
quotaExhausted: false, schedulerScore: null, schedulerBreakdown: null`.
`isActive` = verified + untested targets > 0. `nextTarget` is the
highest alias-score eligible target (exists, not privateOnly, not
quota-exhausted, effectiveState healthy or degraded, score not
null) scored at medium size class (estimated 10000 tokens, neutral
language, no capability bonus in the displayed score).
`stats.totalRequests` / `totalTokens` are sums over client tokens
(`requestCount` / `usedTokens`), not over audit rows.
`stats.avgLatencyMs` is the mean of the returned logs.
`stats.rssMb` is process RSS in MB (Go: runtime memory stats,
documented as approximate, not Node RSS).

NOTE on tokens in overview: Node returns full client-token rows
here, including token values. The Go port keeps this behaviour
for admin callers only (admin-guarded endpoint), but frontend code
must not display token values except in the creation response.
This is documented, not hidden: do not "fix" it silently in a
later PR without a user decision.

### 3.2 GET /api/gateway/logs

Query: `limit` (default 50, clamped 1–200), `offset` (>= 0).
Response: `{ logs: [audit rows], limit, offset }`, newest first.

### 3.3 PATCH /api/gateway/config

Body: partial scheduler_config patch (fields as in §4, e.g.
`privacyMode`, `dispatchMode`, `pinnedModelId`, `weightsJson`,
`rateLimitRpm`). Response: the updated scheduler_config row.
Validation failures in Node are returned by `updateSchedulerConfig`
as-is; the Go port must reject unknown `privacyMode` values
(`off|low|high` only) and unknown `dispatchMode` values
(`auto|pinned` only) with 400 `{ error }`.

### 3.4 Provider keys

- POST `/api/gateway/keys` — body `{ id?, name (required, string),
  providerType?, rawApiKey?, accountNotes?, appealUrl?, baseUrl? }`.
  Missing/invalid name -> 400 `{ error: "Key name is required" }`.
  Success 201: key row without `encryptedKey`, plus
  `counters` (quota counters for the key). `providerType` default
  `openai-compatible`. A new key is created with a generated id
  when `id` is absent. `rawApiKey` empty means "no key configured"
  (key_state `unconfigured`); it is encrypted on write, never stored
  or returned in plaintext.
- PATCH `/api/gateway/keys/:id` — body `{ name?, providerType?,
  rawApiKey?, accountNotes?, appealUrl?, baseUrl?, dailyTokenLimit? }`.
  Unknown id -> 404 `{ error: "Provider key not found" }`. Success:
  key row without `encryptedKey`, plus `counters`.
- POST `/api/gateway/keys/:id/reset` — sets key state to `healthy`
  (clears cooldown/exhausted). Response
  `{ ok: true, message: "Provider key reset to healthy state" }`.
  Unknown id: Node `updateKeyState` is a no-op; Go keeps the same
  no-op + ok response (documented, not a 404).
- DELETE `/api/gateway/keys/:id` — deletes the key row.
  Response `{ ok: true }` (no-op when missing, same as Node).

Key row JSON (without `encryptedKey`): `{ id, name, providerType,
keyMask, keyState, cooldownUntil, consecutive429, accountNotes,
appealUrl, baseUrl, createdAt }`. `keyMask` format: empty key ->
`未配置密钥`; length <= 8 -> `••••••`; `sk-` prefix ->
`sk-••••<last4>`; else `<first4>••••<last4>` (server/security.ts
`maskApiKey`). `keyState` values:
`healthy|cooldown|exhausted|disabled|unconfigured`.

### 3.5 Model routes

- POST `/api/gateway/routes` — body requires `modelId`, else 400
  `{ error: "modelId is required" }`. `providerKeyId` resolution:
  if `rawApiKey` is present and non-empty, create a key named
  `<provider or "Provider"> Key (<modelId>)` and use it; else if
  `providerKeyId` is absent, use the first existing key's id (or
  empty). `baseUrl`: body value first, else the provider key's
  `baseUrl`, else 400 with the Chinese message Node returns
  ("该供应商在第三层还没有填上游接口 URL，请先在第三层编辑该供应商填好 URL，再回第二层选它建路由。").
  Success 201: the created route row (full row, see §4).
- PATCH `/api/gateway/routes/:id` — body is a partial route patch.
  Unknown id -> 404 `{ error: "Model route not found" }`. Success:
  route row plus `effectiveState`, `verificationStatus`, `metrics`,
  `keyMask`, `providerName`, `isColdStart`, `timeoutTier`.
- POST `/api/gateway/routes/:id/reset` — sets route state to
  `healthy` and circuit breaker to `closed` (counters 0,0).
  Response
  `{ ok: true, message: "Model route circuit breaker & status reset to healthy" }`.
- DELETE `/api/gateway/routes/:id` — Response `{ ok: true }`.

Route row JSON: `{ id, name, providerKeyId, modelId, displayName,
baseUrl, providerType, upstreamModel, taskRole, routeState,
circuitState, circuitOpensUntil, consecutiveTimeouts,
consecutive5xx, consecutiveFailures, circuitOpenCount,
stableSuccessCount, circuitClosedAt, unavailableUntil,
supportsVision, supportsTools, supportsJsonMode, supportsStreaming,
maxContext, allowSensitive, privateOnly, privacyTier, costWeight,
reliabilityScore, enabled, createdAt }` (booleans as JSON booleans;
SQLite stores them as 0/1). `taskRole` values: `auto|vision|
reasoning|fast`. `routeState` values:
`healthy|degraded|unavailable_for_key|disabled`. `circuitState`:
`closed|open|half_open`. `providerType` values:
`gemini|openai-compatible|openrouter|groq|cerebras|cloudflare|ollama`.
`privacyTier` values:
`local_private|cloud_zero_retention|cloud_free_training`.

### 3.6 Client tokens

- POST `/api/gateway/tokens` — body `{ name (required),
  quotaLimit?, note? }`. Missing name -> 400
  `{ error: "Token name is required" }`. Success 201: full token row
  INCLUDING the generated token value (creation only).
- PATCH `/api/gateway/tokens/:id` — body `{ name?, quotaLimit?,
  active?, note? }` (`quotaLimit`: undefined = unchanged, null or
  `""` = null, else Number). Success: updated row. Node
  `upsertClientToken` preserves name/note/quotaLimit when only
  `active` is toggled; Go must do the same (partial update, never
  blank out untouched fields).
- DELETE `/api/gateway/tokens/:id` — Response `{ ok: true }`.

Token row JSON: `{ id, name, token, createdAt, lastUsedAt, active,
quotaLimit, usedTokens, requestCount, note }`. `createdAt` is
required and has no DB default in Node; inserts must set it.

### 3.7 Logs, probe, tools verification, catalog watch, onboard

- DELETE `/api/gateway/logs` — clears audit logs. Response
  `{ ok: true }`.
- POST `/api/gateway/probe` — body `{ baseUrl?, apiKey?,
  providerKeyId? }`. If `providerKeyId` is given and unknown -> 404
  `{ success: false, error: "providerKeyId not found" }`. If no
  base URL can be resolved (body, else stored key's base URL) ->
  400 `{ success: false, error: "没有上游接口 URL：请先在第三层给该供应商填好 URL，再回第二层探测。" }`.
  If no pasted key, the stored key is decrypted for the probe and
  never returned. Success/failure shape is the probe result
  `{ success, models?, error? }` from `probeUpstreamModels`
  (SSRF-protected; see server/netguard.ts — the Go port must keep
  the same private/loopback/metadata-address refusals before any
  probe, onboard, or upstream fetch to a user-supplied URL).
  On 401-like failures Node appends a Chinese hint distinguishing
  key-supplier mismatch (Groq `gsk_`, OpenRouter `sk-or-`,
  Kaggle key formats) from a missing key; Go keeps that hint text
  verbatim for the 401 case.
- POST `/api/gateway/verify-model-tools` — body
  `{ baseUrl, apiKey, modelId }`. Missing baseUrl or modelId ->
  400 `{ supported: false, latencyMs: 0, error: "baseUrl and modelId are required" }`.
  Success: `{ supported, latencyMs, error? }` from the tool-support
  test (a real small tool-call request to the upstream model).
- GET `/api/gateway/catalog-watch` — returns catalog-watch status
  `{ lastCheckAt?, changes?, error?, ... }` as stored by the
  catalog watcher. Detection + suggestion only: never auto-delete
  a route, never auto-switch a model.
- POST `/api/gateway/catalog-watch/run` — runs one catalog check.
  On error -> 500 `{ error }`.
- POST `/api/gateway/import-probed` and POST `/api/gateway/onboard`
  (same handler) — body `{ providerKeyId?, providerName?,
  providerType?, baseUrl (required), apiKey?, models: [{ id?,
  upstreamModel?, displayName?, taskRole?, supportsVision?,
  supportsTools?, supportsJsonMode?, supportsStreaming?,
  maxContext?, allowSensitive?, privacyTier?, costWeight?,
  updateExistingByModelId? }], privacyTier?, attachAlias?,
  prependToAlias?, hotSwapRouteId? }`.
  Missing baseUrl -> 400 `{ error: "baseUrl is required" }`.
  Hot-swap fast path: when `hotSwapRouteId` matches an existing
  route, update that route's key (if a new key is given) and the
  key's baseUrl, set the route's baseUrl and `enabled: true`, and
  return 200 `{ success: true, hotSwapped: true, providerKeyId,
  importedCount: 1, routes: [route] }` without requiring `models`.
  Otherwise `models` must be a non-empty array, else 400
  `{ error: "models array is required" }`. Routes are created or
  updated in place (preserving reliability/metrics when updating);
  new routes default `taskRole: "auto"`, `supportsTools: true`
  unless explicitly false, `supportsJsonMode: true` unless
  explicitly false, `supportsStreaming: true` unless explicitly
  false, `maxContext: 32768` unless given, `allowSensitive`
  from the model or from `privacyTier == "local_private"`,
  `privacyTier` from the model or the request fallback
  (`cloud_free_training` when invalid/missing). Optional alias
  attach: prepend or append the new modelIds (deduplicated) to
  the named alias, creating the alias when missing.
  Success 201: `{ success: true, providerKeyId, importedCount,
  routes, attachedAlias: { id, alias, targetModels, description } | null }`.
- POST `/api/gateway/routes/:id/verify-tools` — unknown route ->
  404 `{ error: "Route not found" }`. Runs the tool-support test
  against the route's base URL + decrypted key + upstream model;
  when supported, sets the route's `supportsTools: true`.
  Response is the test result `{ supported, latencyMs, error? }`.

### 3.8 Aliases

- GET `/api/gateway/aliases` — array of `{ id, alias,
  targetModels: string[], description, createdAt, poolStatus,
  missingTargets, failedTargets, verifiedTargets, untestedTargets }`
  (same pool evaluation as §3.1, without targetDetails).
- POST `/api/gateway/aliases` — body `{ id?, alias (required,
  non-empty), targetModels (required, non-empty array),
  description? }`. Missing alias -> 400
  `{ error: "alias 字段不能为空" }`. Missing/empty targetModels ->
  400 `{ error: "targetModels 必须为非空模型名称数组" }`.
  Success: `{ id, alias, targetModels, description, createdAt }`
  (targetModels are trimmed, empties dropped).
- PATCH `/api/gateway/aliases/:id` — body `{ alias?,
  targetModels?, description? }`. `targetModels` when present must
  be a non-empty array, else the same 400 as above. Unknown id ->
  404 `{ error: "Model alias not found" }`. Success: same shape as POST.
- DELETE `/api/gateway/aliases/:id` — Response
  `{ success: true|false }` (false when the alias did not exist).

### 3.9 Diagnostics and Kaggle refresh status

- GET `/api/gateway/diagnostics` — returns the gateway diagnostics
  object from Node `getGatewayDiagnostics()` (routes/keys/aliases
  counts, orphaned routes/keys, stale aliases, duplicate modelIds,
  and a user-confirmed cleanup checklist). The Go port must return
  the same top-level shape; exact checklist item wording is a
  Phase 3+ detail and must be copied from Node before the admin
  diagnostics UI is re-enabled against Go.
- POST `/api/gateway/diagnostics/cleanup` — body
  `{ deleteRouteIds?: string[], deleteKeyIds?: string[],
  cleanAliasIds?: string[] }` (non-arrays treated as empty).
  Response `{ ok: true, ...cleanupResult }`.
- GET `/api/gateway/kaggle/refresh-status` — returns Node's
  `getKaggleRefreshStatus()` shape: `enabled`, `providerName`,
  `modelId`, `providerFound`, `keyState`, `expiresAt`,
  `remainingSeconds`, `lastRefreshAt`, `lastError`,
  `consecutiveFailures`, with nulls where Node returns null (no
  provider found, or no refresh state row yet). Phase 5 implements
  this for real; when the timer is not configured for the instance
  it returns `{enabled: false, note: ...}` honestly.
- POST `/api/gateway/kaggle/refresh` — runs one Kaggle refresh
  (Node's `runKaggleRefreshOnce`) and returns `{action, reason}`.
  Phase 5 implements this for real, including the cached-token
  branch (action `cached`, neither success nor failure, DB not
  overwritten) and failure-keeps-old-key. When the timer is not
  configured for the instance it returns 501 with an honest
  message, not a fake success. In the Go Phase 1–3 pass this
  endpoint returned 501 unconditionally; that placeholder is
  removed in Phase 5.

## 4. Database schema (server/db.ts, SQLite)

Pragmas on every connection: `journal_mode = WAL`,
`synchronous = NORMAL`, `foreign_keys = ON`,
`cache_size = -4000` (4 MB page cache), `temp_store = MEMORY`,
`mmap_size = 0`. SQLite >= 3.35.0 required (Node checks and exits
otherwise; Go must fail startup on a lower version the same way).
Default scheduler config row (id `default`): `dispatch_mode
"auto"`, `pinned_model_id` NULL, `weights_json`
`{"wSticky":0.35,"wRole":0.30,"wRel":0.20,"wCost":0.10,"wLat":0.05}`,
`rate_limit_rpm` 120, `ip_allowlist_json` `[]`, `loopback_only` 0,
`privacy_mode` added by migration with default `high` (production
instances are expected to set it explicitly; see §2.2 — the
current production mode is `off`, which is a deployment setting,
not a schema default).

Tables (column names are snake_case in SQLite; JSON names are
camelCase as in §3). All tables use `CREATE TABLE IF NOT EXISTS`
plus the idempotent `ALTER TABLE … ADD COLUMN` migrations Node
applies (provider_keys.base_url, model_routes consecutive_failures /
circuit_open_count / stable_success_count / circuit_closed_at /
private_only, scheduler_config.privacy_mode, audit_logs.requested_model).
The Go schema must apply the same migrations in the same order so
an existing Node DB opens unchanged.

- `provider_keys (id TEXT PRIMARY KEY, name TEXT NOT NULL,
  provider_type TEXT NOT NULL, encrypted_key TEXT NOT NULL,
  key_mask TEXT NOT NULL, key_state TEXT NOT NULL DEFAULT 'healthy',
  cooldown_until TEXT, consecutive_429 INTEGER NOT NULL DEFAULT 0,
  account_notes TEXT NOT NULL DEFAULT '', appeal_url TEXT NOT NULL
  DEFAULT '', base_url TEXT NOT NULL DEFAULT '', created_at TEXT
  NOT NULL)`.
- `kaggle_refresh_state (provider_key_id TEXT PRIMARY KEY,
  expires_at TEXT, last_refresh_at TEXT, last_error TEXT,
  consecutive_failures INTEGER NOT NULL DEFAULT 0)`.
- `model_routes (id TEXT PRIMARY KEY, name TEXT NOT NULL,
  provider_key_id TEXT NOT NULL, model_id TEXT NOT NULL,
  display_name TEXT NOT NULL, base_url TEXT NOT NULL,
  provider_type TEXT NOT NULL, upstream_model TEXT NOT NULL,
  task_role TEXT NOT NULL DEFAULT 'auto', route_state TEXT NOT NULL
  DEFAULT 'healthy', circuit_state TEXT NOT NULL DEFAULT 'closed',
  circuit_opens_until TEXT, consecutive_timeouts INTEGER NOT NULL
  DEFAULT 0, consecutive_5xx INTEGER NOT NULL DEFAULT 0,
  consecutive_failures INTEGER NOT NULL DEFAULT 0,
  circuit_open_count INTEGER NOT NULL DEFAULT 0,
  stable_success_count INTEGER NOT NULL DEFAULT 0,
  circuit_closed_at TEXT, unavailable_until TEXT, supports_vision
  INTEGER NOT NULL DEFAULT 0, supports_tools INTEGER NOT NULL
  DEFAULT 1, supports_json_mode INTEGER NOT NULL DEFAULT 1,
  supports_streaming INTEGER NOT NULL DEFAULT 1, max_context
  INTEGER NOT NULL DEFAULT 128000, allow_sensitive INTEGER NOT NULL
  DEFAULT 0, private_only INTEGER NOT NULL DEFAULT 0, privacy_tier
  TEXT NOT NULL DEFAULT 'cloud_zero_retention', cost_weight REAL
  NOT NULL DEFAULT 1, reliability_score INTEGER NOT NULL DEFAULT 100,
  enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL)`.
- `quota_counters (key_id TEXT NOT NULL, window_date TEXT NOT NULL,
  rpm_count INTEGER NOT NULL DEFAULT 0, rpm_window_minute INTEGER
  NOT NULL DEFAULT 0, tpm_count INTEGER NOT NULL DEFAULT 0,
  rpd_count INTEGER NOT NULL DEFAULT 0, billed_tokens_today INTEGER
  NOT NULL DEFAULT 0, fallback_tokens_today INTEGER NOT NULL
  DEFAULT 0, daily_token_limit INTEGER, rpm_limit INTEGER,
  rpd_limit INTEGER, PRIMARY KEY (key_id, window_date))`.
  `window_date` is UTC `YYYY-MM-DD`. A UTC day is 08:00 CST to
  next-day 08:00 CST; never present a UTC row as a CST calendar-day total.
- `route_metrics_snapshots (route_id TEXT PRIMARY KEY,
  latency_p50 INTEGER NOT NULL DEFAULT 450, latency_p95 INTEGER
  NOT NULL DEFAULT 1200, recent_latencies_json TEXT NOT NULL
  DEFAULT '[]', success_count INTEGER NOT NULL DEFAULT 0,
  failure_count INTEGER NOT NULL DEFAULT 0, last_error_code TEXT,
  last_error_message TEXT, updated_at TEXT NOT NULL)`.
- `session_affinity (session_key TEXT PRIMARY KEY, route_id TEXT
  NOT NULL, provider_key_id TEXT NOT NULL, hit_count INTEGER NOT
  NULL DEFAULT 1, expires_at TEXT NOT NULL)`.
- `scheduler_config (id TEXT PRIMARY KEY, dispatch_mode TEXT NOT
  NULL DEFAULT 'auto', pinned_model_id TEXT, weights_json TEXT NOT
  NULL, rate_limit_rpm INTEGER NOT NULL DEFAULT 120,
  ip_allowlist_json TEXT NOT NULL DEFAULT '[]', loopback_only
  INTEGER NOT NULL DEFAULT 0, privacy_mode TEXT NOT NULL DEFAULT
  'high', updated_at TEXT NOT NULL)`.
- `client_tokens (id TEXT PRIMARY KEY, name TEXT NOT NULL, token
  TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL, last_used_at
  TEXT, active INTEGER NOT NULL DEFAULT 1, quota_limit INTEGER,
  used_tokens INTEGER NOT NULL DEFAULT 0, request_count INTEGER
  NOT NULL DEFAULT 0, note TEXT NOT NULL DEFAULT '')`.
- `audit_logs (request_id TEXT PRIMARY KEY, timestamp TEXT NOT
  NULL, session_key TEXT NOT NULL, client_token_id TEXT NOT NULL,
  client_token_name TEXT NOT NULL, detected_task_type TEXT NOT NULL,
  is_private INTEGER NOT NULL DEFAULT 0, selected_provider_key_id
  TEXT NOT NULL, selected_route_id TEXT NOT NULL,
  selected_model_id TEXT NOT NULL, failover_chain_json TEXT NOT NULL
  DEFAULT '[]', estimated_tokens INTEGER NOT NULL DEFAULT 0,
  billed_tokens INTEGER NOT NULL DEFAULT 0, usage_source TEXT NOT
  NULL DEFAULT 'upstream', latency_ms INTEGER NOT NULL DEFAULT 0,
  ttft_ms INTEGER, status_code INTEGER NOT NULL, validator_result
  TEXT NOT NULL DEFAULT 'pass', preview_prompt TEXT NOT NULL
  DEFAULT '', requested_model TEXT)`.
  `session_key` is per-request unique unless the client supplied an
  explicit session id; audit therefore cannot be grouped into
  conversation sessions. `billed_tokens` is upstream usage tokens,
  not money. `preview_prompt` is the last user message truncated
  (100 chars string, 80 chars + `[Image Redacted] ` prefix for
  image parts) with data-URL image bodies replaced by
  `[IMAGE_BASE64_REDACTED: ~<KB>KB]`.
- `model_aliases (id TEXT PRIMARY KEY, alias TEXT NOT NULL UNIQUE,
  target_models_json TEXT NOT NULL, description TEXT, created_at
  TEXT NOT NULL DEFAULT (datetime('now')))`. Note: `model_aliases`
  has no `updated_at` column.

## 5. Scheduler (server/scheduler.ts, ported in Phase 3)

Weights (configurable via `weights_json`, defaults in §4):
sticky 0.35, role 0.30, reliability 0.20, cost 0.10, latency 0.05.
Sticky score 1 iff `sessionAffinity.routeId == route.id`, except
fast-role routes lose sticky when the task is `reasoning`.
Role score: task match 1.0, `auto` role 0.8, cross-tier fallback
0.35, minus 0.3 state penalty when degraded. Reliability and
latency are clamped to [0,1] from `reliabilityScore/100` and
`latencyP50/5000` (default p50 400 when missing). Cost is
`(costWeight-1)/9` clamped to [0,1].

Alias scoring (`computeAliasScore`, used for alias pools and for
the overview `nextTarget`): size class small <= 3200 (order gap 50,
latency multiplier 30), medium 3201–32000 (gap 100, reliability
multiplier 10, latency multiplier 5), large > 32000 (gap 100,
reliability multiplier 30, latency multiplier 5, +25 when
`maxContext >= 1000000`, -200 soft penalty when base URL contains
`api.groq.com` — soft only, a sole Groq candidate still runs).
`orderBase = 1000 - targetIdx * orderGap`.
`reliabilityBonus = round(clampedReliability * relMult * 10)/10`.
`latencyPenalty = round(clampedLatency * latMult * 10)/10`.
`quotaBonus` = 10 when no daily limit, else
`max(0, (limit - billedToday)/limit * 10)`, rounded to 1 decimal.
`failedDemotion` = 500 when verification status is `failed`, else 0.
`capabilityBonus` = 50 when the request language is not neutral
and the route's capability tags contain it; the overview display
score excludes this bonus (see §3.1), the real request score
includes it.
Score = `orderBase + reliabilityBonus - latencyPenalty + quotaBonus
- failedDemotion + contextBonus - largeGuardPenalty + capabilityBonus`,
rounded to 1 decimal.

Hard filters (applied before scoring, in `selectCandidateRoutes`):
skip routes that are disabled, unconfigured (no key), exhausted,
`unavailable_for_key`, or cooldown (after auto-heal checks, see
below); skip when `hasImage && !supportsVision`,
`hasTools && !supportsTools`, `hasJsonMode && !supportsJsonMode`,
`isPrivate && !allowSensitive`, `!isPrivate && privateOnly`,
`maxContext < estimatedTokens + 1000`; skip when the key's daily
token limit is set and `billedTokensToday >= dailyTokenLimit`
(and mark the key exhausted). Direct-pin candidates: only routes
whose `model_id` equals the pin, scored `1000 + rel*10 - lat*5`.
Alias candidates: only routes whose `model_id` is in the alias
target list, scored by `computeAliasScore`. Auto candidates (no
pin, no alias targets): scored by the sticky/role/reliability/
cost/latency weighted sum. Auto mode (`resolveRoutingMode`):
model empty, `default`, `auto`, `auto:fast`, `auto:reasoning`,
`auto:vision`, `auto:private` (case-insensitive, trimmed) -> auto;
any other string -> pool mode. In auto mode alias targets are
ignored (`resolveAliasTargets` returns undefined).

Effective state (`getEffectiveState`, with auto-heal side effects
that the Go port must reproduce against the DB, not in memory
only): disabled if route disabled or key disabled; unconfigured
if no key or key unconfigured; cooldown key auto-heals to healthy
when `cooldownUntil` has passed, else `cooldown`; exhausted key ->
`exhausted`; `unavailable_for_key` route auto-heals to healthy
when `unavailableUntil` has passed, else `unavailable_for_key`;
open circuit auto-transitions to `half_open` when
`circuitOpensUntil` has passed, else `cooldown`; half-open route
with a probe already running (30s lock) -> `cooldown`, else
`degraded`; degraded route -> `degraded`; else `healthy`.

Verification status (`getRouteVerificationStatus`): `unconfigured`
when no key or effective state unconfigured; `failed` when
effective state is disabled/exhausted/`unavailable_for_key`/
cooldown, or `consecutiveFailures > 0`, or metrics
`lastErrorCode` non-empty; `verified` when success count > 0 and
none of the above; else `untested`. Note: a `failed` badge is a
display status, not by itself a routing block — routing uses
effective state + hard filters + the 500-point demotion.

Rotation (`reorderCandidatesForRotation`, poolKey + size class,
in-process state, restart resets distribution only): if <= 1
candidate, unchanged. For large requests, first remove Groq
candidates (`baseUrl` contains `api.groq.com`) from the rotation
pool only (failover list keeps them). Quota-aware: if total billed
today across the candidate pool > 0 and the Kaggle model
(`KAGGLE_ROTATION_MODEL_ID`, the Kaggle main route's modelId)
share > 60% (`KAGGLE_ROTATION_SHARE_CAP = 0.60`), remove Kaggle
from the rotation pool only (it stays in failover). If the
rotation pool becomes empty, return the original candidates.
Smooth weighted round-robin over the scored order: weights by
position in the scored list are 4/3/2/1 for positions 0/1/2/rest;
each candidate's current counter += its weight, the highest
counter wins the first pick (ties by original order), and the
winner's counter -= total weight. The returned order is
`[picked, ...original candidates except picked]` — rotation never
reorders failover.

Failure handling (`handleRouteFailure`, by upstream error
category, see §6): `unsupported_param` / `bad_request` /
`context_overflow` -> no penalty, no breaker change (record the
metric only). `rate_limited`: daily-quota keywords -> key
`exhausted` until next UTC 00:00; else key `cooldown` until
Retry-After seconds when present, else `60 * 2^(consecutive429)`
seconds capped at 900s, and `consecutive429` increments.
`quota_exhausted` (402 etc.) -> key `exhausted` (manual reset;
never self-recovers). `auth_invalid` (401) -> key `disabled`.
`model_forbidden` (403) -> route `unavailable_for_key` for 12h.
`timeout` / `server_error` (5xx) -> record infrastructure
failure; at >= 3 consecutive failures the circuit opens for
`min(60 * 2^oldOpenCount, 900)` seconds (400s do not increment
the counter). Success (`handleRouteSuccess`): reset breaker
counters atomically, reset key 429 count, release half-open lock,
record metric + quota counters, set session affinity only on
HTTP 200 and only for explicit sessions (1800s TTL). Auto-enroll
on success is a LATER PR, not Phase 3 (see §7) — Phase 3 must not
silently enrol routes.

## 6. Upstream, timeouts, validator (server/adapters.ts, server/validator.ts)

Timeout tiers (`getRouteTimeoutTier`): Kaggle cold-start routes
(see below) -> 120000 ms always. Else task role `fast` ->
streaming 15000 ms, non-streaming 60000 ms. Else (auto/reasoning/
vision) -> streaming 45000 ms, non-streaming 60000 ms.
Cold-start detection (`isKaggleColdStartRoute`): NOT a Kaggle
Model Proxy URL (`mp-staging.kaggle.net` in base URL -> false);
otherwise true when id/name/modelId/upstreamModel/baseUrl
combined matches `qwen3.6|kaggle|trycloudflare|ngrok|gradio|zrok|
pinggy|llama-server|localhost:8000|127.0.0.1:8000`
(case-insensitive). The Go port must keep this exact predicate;
do not "improve" it to match more hosts.

Upstream error normalisation (`normalizeUpstreamError`, status +
body text + headers, body truncated to 200 chars in the error
message): 401 -> `auth_invalid`; 402, or code
`insufficient_quota`, or body containing `insufficient_quota`,
`1113` (Zhipu insufficient balance), `balance`, `arrears`,
`quota exceeded` -> `quota_exhausted`; 403 -> `model_forbidden`;
429 -> `rate_limited` (daily when body contains `day`, `rpd`,
or `daily limit`); 413 with `tokens per minute`/`tpm`/`rate limit`
-> `rate_limited` (Groq TPM reported as 413); 413 or context
overflow patterns -> `context_overflow`; content-filter patterns
-> `content_filtered`; 400 with `error.param` in
`tools|tool_choice|response_format|functions` or unsupported-param
patterns -> `unsupported_param`, else `bad_request`; >= 500 ->
`server_error`; 408 or body containing `timeout` -> `timeout`.
Retry-After: `Retry-After` header (seconds or HTTP date, clamped
1–3600s), else `x-ratelimit-reset-requests|tokens|reset`
(duration or epoch > 1_700_000_000 converted to a delta, clamped
1–3600s). Failover only before the first byte; a failure after
streaming has started is a stream error, not a failover.

Validator (`validateResponse`, non-streaming path): body must be
an object with a non-empty `choices` array, else
`fail_empty_body` / `fail_no_choices`. When no tool calls, content
must be non-empty after trimming, else `fail_empty_content`.
Content matching a known refusal/error pattern (rate limit
reached, model overloaded, upstream timeout, 502/504, HTML
doctype/html tag, `"error": {"code`) -> `fail_canned_error`.
Tool calls: each must be an object with `function.name`; when
tools were declared, the name must be declared, else
`fail_tool_unknown`; string `arguments` must parse to a JSON
object, else `fail_tool_invalid_json_args` /
`fail_tool_args_not_object`; declared required fields must be
present, else `fail_tool_missing_required_arg`; shape failures
are `fail_tool_shape`. JSON mode (`json_object`/`json_schema`):
content must be non-empty (`fail_json_empty`) and parse as JSON
(`fail_invalid_json_content`). `finish_reason == "length"` with
content empty or < 50 chars -> `fail_truncated_length`. Success
code is `pass`. The Go validator must return the same codes in
the same order of checks. The streaming path uses a chunk
watchdog (timeout = the route tier timeout) that fires on no chunk
within the tier; the Go port must implement the same watchdog
semantics for SSE.

Gemini conversion: Node converts OpenAI messages to Gemini
`contents` for Gemini-type routes and back. The Go Phase 3 port
covers OpenAI-compatible upstreams fully; Gemini conversion is
flagged as a known gap in the PR body until it is ported and
tested against a real Gemini upstream (do not claim Gemini parity
in Phase 3).

## 7. Deliberate divergences and later PRs

- Master key: Node `getMasterKey()` generates a new 32-byte key
  and writes it (0600) when no env key and no key file exist.
  The Go backend MUST NOT do this on migration: when neither
  `GATEWAY_MASTER_KEY` nor the configured master-key file exists,
  Go fails startup loudly (non-zero exit, clear log line naming
  the missing source) and never generates a key, because a
  generated key would orphan every existing encrypted provider
  key. Key resolution order in Go: `GATEWAY_MASTER_KEY` (64-char
  hex -> raw 32 bytes, else SHA-256 of the string), else the
  master-key file (deployment path `/var/lib/railways/master.key`;
  Node code default is `.gateway-data/master.key` relative to
  the server directory — the Go config must make the file path
  explicit via `GATEWAY_MASTER_KEY_FILE`, defaulting to the
  deployment path, and must log which source was used, never the
  key value). Encryption format, verified against Node
  `server/security.ts`: AES-256-GCM, IV 12 random bytes,
  output `iv_hex:authTag_hex:ciphertext_hex`. Decryption of a
  string without exactly 3 colon-separated parts returns the
  input unchanged in Node (legacy unencrypted passthrough); Go
  keeps that passthrough for reads but never writes unencrypted
  keys. A decryption failure in Go returns an error to the
  caller (admin probe/route handlers surface it as a 400/500
  with a generic message), it does not silently return empty
  except where Node does so for display masking.
- Kaggle timer (Phase 5): implemented per Node
  `server/kaggle-refresh.ts`. Issuance POSTs to Kaggle's
  ModelProxy endpoint with body `{}`, Bearer auth, 2h TTL via
  server-returned `expiryTime` (never a fixed interval), check
  every 600s, refresh-before 1200s, env `KAGGLE_REFRESH_ENABLED`
  (exactly `true` to start). Failure keeps the old key. The
  timer loop uses a dynamic delay (not a fixed ticker) and runs
  once immediately at startup, as in Node.
- Auto-enroll (Phase 5): implemented per Node scheduler.ts, with
  BOTH Node triggers (on-success and startup verified scan), not
  just one; see the PR body for the trigger ambiguity note. A
  route appends at the LAST position of every alias except
  `vision` (capability-filtered), skipping aliases that already
  contain its modelId. The exclusion-list decision from Phase 3
  notes is still pending user decision: Phase 5 ports Node
  behaviour exactly and does not invent an exclusion list.
- Tool-rescue / tool-args (Phase 5): implemented per Node
  `server/tool-rescue.ts` and `server/tool-args.ts`, non-streaming
  path only, in Node order (rescue first, then args, then
  validator). Streaming is untouched. Scalar coercion is NOT
  implemented. Go re-marshals repaired arguments with sorted map
  keys (Node preserves insertion order); the JSON value is
  identical, so this is not a semantic change.
- Privacy switch (Phase 5): classification + routing filters for
  all three modes, with Node's regex patterns (not Phase 3's
  substring matching) and string-content-only sampling. Default
  is OFF by user order. DIVERGENCE from Node, deliberate: Node
  falls back to `high` for a missing/invalid stored mode; Go
  maps empty/invalid modes to `off` (fail closed to normal
  routing). allowSensitive/privateOnly are routing switches only,
  NOT encryption or redaction; content is forwarded verbatim.
- Probe / onboard / catalog-watch: contract documented in §3.7;
  implementation in Phase 3 is limited to probe (with SSRF guard)
  and read-only catalog-watch status. Onboard batch import and
  catalog-watch mutation are later PRs and must return 501 with
  a clear message until then, never a fake success.
- Frontend: the React frontend in this repo is a preview. Its
  Step 2 data-model adaptation (RAILWAYS_ADAPTATION.md) is a
  separate PR from the Go backend; the Go backend must not depend
  on frontend seed data.
- Deployment: no deploy in this PR. No RackNerd changes, no Ohio
  changes, no AWS ladder changes, no ladder teardown, no main
  merge without user approval of the PR.
