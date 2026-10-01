# AGENTS.md

Go 1.26+ proxy server providing OpenAI/Gemini/Claude/Codex compatible APIs with OAuth and round-robin load balancing.

## Repository
- GitHub: https://github.com/router-for-me/CLIProxyAPI

## Commands
```bash
gofmt -w . # Format (required after Go changes)
go build -o cli-proxy-api ./cmd/server # Build
go run ./cmd/server # Run dev server
go test ./... # Run all tests
go test -v -run TestName ./path/to/pkg # Run single test
go build -o test-output ./cmd/server && rm test-output # Verify compile (REQUIRED after changes)
```
- Common flags: `--config <path>`, `--tui`, `--standalone`, `--local-model`, `--no-browser`, `--oauth-callback-port <port>`

## Config
- Default config: `config.yaml` (template: `config.example.yaml`)
- `.env` is auto-loaded from the working directory
- Auth material defaults under `auths/`
- Storage backends: file-based default; optional Postgres/git/object store (`PGSTORE_*`, `GITSTORE_*`, `OBJECTSTORE_*`)

## Architecture
- `cmd/server/` — Server entrypoint
- `internal/api/` — Gin HTTP API (routes, middleware, modules)
- `internal/api/modules/amp/` — Amp integration (Amp-style routes + reverse proxy)
- `internal/thinking/` — Main thinking/reasoning pipeline. `ApplyThinking()` (apply.go) parses suffixes (`suffix.go`, suffix overrides body), normalizes config to canonical `ThinkingConfig` (`types.go`), normalizes and validates centrally (`validate.go`/`convert.go`), then applies provider-specific output via `ProviderApplier`. Do not break this "canonical representation → per-provider translation" architecture.
- `internal/runtime/executor/` — Per-provider runtime executors (incl. Codex WebSocket)
- `internal/translator/` — Provider protocol translators (and shared `common`)
- `internal/registry/` — Model registry + remote updater (`StartModelsUpdater`); `--local-model` disables remote updates
- `internal/store/` — Storage implementations and secret resolution
- `internal/managementasset/` — Config snapshots and management assets
- `internal/cache/` — Request signature caching
- `internal/watcher/` — Config hot-reload and watchers
- `internal/wsrelay/` — WebSocket relay sessions
- `internal/usage/` — Usage and token accounting
- `internal/home/` — CLIProxyAPIHome control plane integration (bootstrap, RESP communication, dispatch coordination)
- `internal/tui/` — Bubbletea terminal UI (`--tui`, `--standalone`)
- `sdk/cliproxy/` — Embeddable SDK entry (service/builder/watchers/pipeline)
- `management-center/` — In-repo management panel source (React + Vite, single-file build). Follow its own `AGENTS.md`; needs Bun. Run `./management-center/build.sh` to build and stage `dist/index.html` into `static/management.html`, which the server serves in preference to the GitHub-downloaded asset
- `internal/usagestats/` — Native per-request usage/cost accounting: taps `sdk/cliproxy/usage` plugin flow into a pure-Go sqlite DB (`modernc.org/sqlite`, CGO-free builds keep working; WAL + single writer + 1s batch flush + bounded 4k channel with drop counter), management endpoints `/v0/management/usage-meters/summary|series|events` and `/v0/management/usage-meters/pricing` (+ `sync-litellm`, LiteLLM-sourced entries refresh on re-sync while user overrides are never clobbered), embedded default price table in `pricing.go` (longest prefix) merged with `usage-stats.pricing`. Started from `internal/cmd/run.go` behind `usage-stats.enabled` (default on; path/enabled/retention require restart, pricing hot-reloads). Pool-inspection grading lives in `internal/api/handlers/management/pool_inspection.go` (read-only, `GET /v0/management/pool-inspection`).
- `internal/modelprobe/` — Per-credential model capability probing: minimal live probes (`max_tokens=1`, "hi") through provider executors, classifies usable/not_available(prune)/limited/auth_error/unreachable, persists a `model_probe` section into the credential metadata + JSON file (pruned → advertised set shrinks in `service_models` and `/v1/models`; re-adds on usable). Scheduler behind `model-probe.enabled` (default off) with per-credential panic containment, jittered cycle starts (`jitter`, default 0.5), per-probe random spacing (`probe-spacing`, default 3–12s), limited backoff (every 2nd cycle) and auth_error backoff (every 4th cycle). Tests inject fake clocks/sleepers/random sources — no wall-clock sleeps. Manual probes via `GET/POST /v0/management/model-probe/*`. V1 executors: cursor, kiro, cline, claude, codex, xai, devin, meta.
- `new-api/` — In-repo copy of the new-api AI gateway (Go backend + React web admin, deployed alongside this proxy). Independent Go module and its own `AGENTS.md`/`web/AGENTS.md` conventions apply; not part of the root Go module. Build web first (`cd new-api/web && bun install --frozen-lockfile && bun run build`, embed requires `web/dist`), then `go build` inside `new-api/`
- `docker-compose.newapi.yaml` — Optional compose merge file adding the new-api service in front of cli-proxy-api (`docker compose -f docker-compose.yml -f docker-compose.newapi.yaml up -d`)
- `scripts/newapi-bootstrap.sh` — First-run bootstrap for the new-api integration (setup wizard, admin login, CLIProxyAPI channel creation); see `docs/new-api-integration.md`
- `test/` — Cross-module integration tests

## Code Conventions
- Keep changes small and simple (KISS)
- Comments in English only
- If editing code that already contains non-English comments, translate them to English (don’t add new non-English comments)
- For user-visible strings, keep the existing language used in that file/area
- New Markdown docs should be in English unless the file is explicitly language-specific (e.g. `README_CN.md`)
- As a rule, do not make standalone changes to `internal/translator/`. You may modify it only as part of broader changes elsewhere.
- If a task requires changing only `internal/translator/`, run `gh repo view --json viewerPermission -q .viewerPermission` to confirm you have `WRITE`, `MAINTAIN`, or `ADMIN`. If you do, you may proceed; otherwise, file a GitHub issue including the goal, rationale, and the intended implementation code, then stop further work.
- `internal/runtime/executor/` should contain executors and their unit tests only. Place any helper/supporting files under `internal/runtime/executor/helps/`.
- Follow `gofmt`; keep imports goimports-style; wrap errors with context where helpful
- Do not use `log.Fatal`/`log.Fatalf` (terminates the process); prefer returning errors and logging via logrus
- Shadowed variables: use method suffix (`errStart := server.Start()`)
- Wrap defer errors: `defer func() { if err := f.Close(); err != nil { log.Errorf(...) } }()`
- Use logrus structured logging; avoid leaking secrets/tokens in logs
- Avoid panics in HTTP handlers; prefer logged errors and meaningful HTTP status codes
- Timeouts are allowed only during credential acquisition; after an upstream connection is established, do not set timeouts for any subsequent network behavior. Intentional exceptions that must remain allowed are the Codex websocket liveness deadlines in `internal/runtime/executor/codex_websockets_executor.go`, the wsrelay session deadlines in `internal/wsrelay/session.go`, the management APICall timeout in `internal/api/handlers/management/api_tools.go`, and the `cmd/fetch_antigravity_models` utility timeouts
- Avoid wall-clock `time.Sleep` in TTL, expiration, ordering, or cache-eviction unit tests due to platform timer granularity (e.g. Windows default timer resolution of ~15.6ms) and CI jitter under load; prefer controllable clocks (`nowFunc` / mock clock), explicit timestamp manipulation, or deterministic synchronization primitives.
- Note: if modifying features that involve CLIProxyAPIHome, check if corresponding updates are needed in the CLIProxyAPIHome repository.
- Endpoints under the `/v0/management` base URL are deprecated and no longer maintained. For any feature changes, do not modify endpoints under `/v0/management` unless necessary to fix compilation errors.
