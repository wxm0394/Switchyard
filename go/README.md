# Switchyard Go backend

Pure-Go gateway (no cgo, single binary) implementing the Railways
API contract in `../GO_API_CONTRACT.md`. The React frontend in
`../src` is a separate preview; this backend does not depend on
its seed data.

## Run

```sh
go run ./cmd/switchyard
# env: SWITCHYARD_PORT (default 3000), SWITCHYARD_HOST (default 0.0.0.0)
#   SWITCHYARD_DB_PATH or GATEWAY_DB_PATH (default switchyard.db)
#   GATEWAY_MASTER_KEY (64-char hex, else SHA-256 of the string)
#   GATEWAY_MASTER_KEY_FILE (default /var/lib/railways/master.key)
#   GATEWAY_ADMIN_PASSWORD, NODE_ENV, GATEWAY_ALLOW_NO_ADMIN_AUTH,
#   GATEWAY_ADMIN_ORIGINS, GATEWAY_ALLOWED_HOSTS
```

Master-key rule: if neither `GATEWAY_MASTER_KEY` nor the
master-key file exists, the server fails startup loudly and never
generates a new key (a generated key would orphan every existing
encrypted provider key). Use `SWITCHYARD_ALLOW_NO_MASTER_KEY=1`
only for a fresh, empty database; it is refused when the DB
already contains provider keys.

## Test

```sh
go test ./...
go vet ./...
```

## Layout

- `cmd/switchyard/main.go` — entrypoint, env config, HTTP mux.
- `internal/crypto` — AES-256-GCM `iv_hex:authTag_hex:ciphertext_hex`
  (Node Railways format), master-key resolution, key masking.
- `internal/scheduler` — classification, token estimation, alias
  scoring, hard filters, weighted rotation, Kaggle 60% cap, Groq
  large-request exclusion.
- `internal/validator` — response validation codes from Node.
- `internal/upstream` — error normalisation, timeout tiers,
  cold-start detection, Retry-After parsing.
- `internal/store` — Node-compatible SQLite schema (modernc.org/sqlite,
  pure Go) plus an in-memory store used by tests.
- `internal/gateway` — `/v1/models`, `/v1/chat/completions`,
  `/api/gateway/*` admin handlers, admin guard, failover, audit,
  auto-enroll triggers.
- `internal/toolrescue` — plain-text tool-call rescue (Phase 5).
- `internal/toolargs` — double-encoding tool-argument repair
  (Phase 5).
- `internal/kaggle` — Model Proxy timer + admin status/refresh
  (Phase 5).

## Not in this pass

Gemini conversion, onboard batch import, catalog-watch mutation,
and any deploy/cutover/ladder change. See `GO_API_CONTRACT.md`
§7 for the deliberate 501s and gaps.
