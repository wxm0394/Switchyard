# Switchyard

Switchyard is a lightweight model relay for coding agents.

It is the Go rewrite of Railways, hosted on RackNerd as a dedicated relay
(no ladder on that host; the AWS ladder is untouched). The frontend in this
repo is adapted from Magpie UI, which the user liked for its layout and
interaction. The original project is
[wxm0394/magpie](https://github.com/wxm0394/magpie) (a fork of
yetone/magpie, MIT).

## Current status: frontend preview only, not yet a working gateway

- Frontend: React 19 + Vite + Tailwind, `src/App.tsx`, `src/types.ts`.
  Run it with `npm install`, then `npm run dev` (preview server on port
  3000, `npm run build` for a static build, `npm run lint` for `tsc`).
- Preview backend: `server.ts` is an Express + Vite preview server serving
  in-memory seed data adapted from the Magpie UI preview. It is NOT the
  Railways gateway: no Go code, no aliases, no rotation, no circuit
  breaker, no Kaggle timer, no real upstream routing, no encrypted keys.
- Go backend: in progress by the user, per the Railways API contract
  (`POST /v1/chat/completions` + `GET /v1/models` for clients, and
  `/api/gateway/*` for admin). See `RAILWAYS_ADAPTATION.md` for the
  view-by-view mapping from Magpie UI to Railways.
- Branding: `package.json`, `index.html`, `metadata.json`, and the
  visible UI strings now say Switchyard. The remaining "Magpie" mentions
  in code are attribution ("adapted from Magpie UI") and seed data
  (for example `@magpie-community/*` plugin package names), pending the
  data-model adaptation in `RAILWAYS_ADAPTATION.md`.

## Files still from Magpie, pending replacement

- `docs/` — Magpie subsystem docs, kept as reference only. They do not
  describe Switchyard yet.
- `Dockerfile` — Magpie Go Dockerfile. It does NOT build Switchyard
  (there is no `go.mod` / `main.go` yet) and must be replaced when the
  Go backend lands.
- `AGENTS.md`, `CLAUDE.md`, `LICENSE`, `THIRD_PARTY_NOTICES` — Magpie
  originals, kept for reference / licence. The Magpie code in this repo
  remains under its MIT licence.

## Roadmap

1. Rename clean (this PR): Switchyard branding in user-facing files.
2. Adapt the UI data model to Railways (see `RAILWAYS_ADAPTATION.md`).
3. Go backend per the Railways API contract, replacing the preview
   in-memory server.
4. Move to RackNerd as a dedicated relay; AWS ladder untouched.
