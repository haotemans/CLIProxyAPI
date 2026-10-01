# CLIProxyAPI (self-use fork)

English | [中文](README_CN.md) | [日本語](README_JA.md)

Personal fork of [router-for-me/CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) — a proxy server that provides OpenAI / Gemini / Claude / Codex compatible API interfaces in front of CLI tools and their accounts. This fork adds self-hosted relay providers, per-account usage cost accounting, and bundles its management panel and the new-api gateway into a single repository.

## What this fork adds

- **Mirasim provider** — first-class `mirasim` API-key provider for Anthropic-protocol reverse proxies (Bearer key, mandatory `base-url`, models fall back to the Claude catalog, alias mapping, thinking config, full Management API and panel support).
- **Cline provider** — first-class `cline` OAuth provider: WorkOS device-flow login (`--cline-login`, Management API, TUI), rotating token refresh, and **first-import model detection** so each account only advertises the models it can actually use (probed from `api.cline.bot`, persisted into the credential file).
- **Cursor provider** — first-class `cursor` OAuth provider: PKCE URL-poll login (`--cursor-login`, Management API, TUI — no local callback, works headless), automatic token refresh, per-account model discovery via `GetUsableModels`, streaming + tool calling over Cursor's Connect/protobuf agent RPC.
- **Kiro provider** — first-class `kiro` OAuth provider (AWS Kiro / CodeWhisperer): AWS Builder ID device-code login (`--kiro-login`, works headless), authorization-code login, IAM Identity Center login, Kiro IDE token import, dynamic per-account model discovery with `-agentic` variants, token refresh via SSO OIDC, and the full Connect EventStream executor with tool calling and thinking support.
- **Unified application (optional)** — `unified.enabled: true` runs the vendored `cpa-usage-keeper` and `cpa-manager-plus` sidecars embedded inside this one process on loopback listens, reverse-proxied behind the main port: `/keeper/` (keeper's native base path; requires the CGO build) and `/manager/` (backend API only — CPAMP's SPA has no base-path support, so run manager-plus standalone for its UI). Status at `GET /v0/management/sidecars`.
- **Native usage/cost accounting** — `usage-stats` in config (on by default) taps every completed request into a local pure-Go sqlite DB (`<auth-dir>/usage-stats.db`, 90-day retention) and exposes `GET /v0/management/usage-meters/summary` and `/usage-meters/series` (group by provider/model/credential/api-key/day); cost comes from upstream-reported spend when available, else token prices from `usage-stats.pricing` merged onto embedded defaults. Browsed natively in the panel (观测 → 用量统计). Kiro/Mirasim credentials additionally gain management-driven quota probes (`POST /v0/management/quota/fetch`) and quota lanes.
- **Per-credential model probing** — `model-probe` in config (off by default) sends one tiny real request per advertised model per credential through each provider's own executor, persists usable/pruned results into the credential file's `model_probe` section, prunes not-available models out of routing and `/v1/models`, and re-probes periodically so they re-appear when usable again. Anti-pattern scheduling: every cycle start is jittered (`interval × (1 ± 0.5)` by default), each probe sleeps a random 3–12s, `limited` models are probed at most every 2nd cycle, and auth_error-flagged credentials every 4th. `GET /v0/management/model-probe/status`, `POST /v0/management/model-probe/run`, plus a probe line and 立即探测 button on every auth-file card. V1 drivers: cursor, kiro, cline, claude, codex, xai, devin, meta.
- **Usage cost accounting** — upstream-reported request cost (Cline `usage.cost`) is captured into usage details and exposed as `tokens.cost_usd` on the usage queue (`/v0/management/usage-queue`, needs `usage-statistics-enabled: true`). Token counters were already there; money is now there too.
- **In-repo management panel** — the Management Center frontend lives in `management-center/` (Vite single-file build) and gets served from `static/management.html` instead of the downloaded GitHub asset. It ships the Cline OAuth card and the Mirasim key family.
- **In-repo new-api** — [QuantumNous/new-api](https://github.com/QuantumNous/new-api) vendored under `new-api/` for the side-by-side gateway deployment (self-use branch `mellow-rolling-falcon`).
- **In-repo ecosystem sidecars** — [cpa-usage-keeper](https://github.com/Willxup/cpa-usage-keeper) (usage persistence + analytics dashboard) vendored under `cpa-usage-keeper/`, and [CPA-Manager-Plus](https://github.com/seakee/CPA-Manager-Plus) (request-level monitoring, price estimation, Codex pool inspection) vendored under `cpa-manager-plus/`. Optional compose: `docker-compose.ecosystem.yaml`.

Upstream highlights that still apply: Gemini / Claude / Codex / Grok / Kimi OAuth pools with round-robin, OpenAI-compatible upstreams, streaming + tool calling, model registry with remote updates, hot-reload, management API + TUI, embeddable Go SDK.

## Quick start

```bash
# build and run
go build -o cli-proxy-api ./cmd/server
cp config.example.yaml config.yaml   # edit: access.api-keys, management.secret-key
./cli-proxy-api --config config.yaml

# account logins (OAuth)
./cli-proxy-api --cline-login        # Cline device flow
./cli-proxy-api --cursor-login       # Cursor (open URL, then poll)
./cli-proxy-api --kiro-login         # Kiro via AWS Builder ID (device code)
./cli-proxy-api --claude-login       # Claude subscription
```

Mirasim relay in `config.yaml`:

```yaml
api-keys:
  mirasim:
    - name: my-relay
      base-url: "https://relay.mirasim.ai"   # required; mirasim.ai user token as the key
      keys:
        - api-key: "your-key"
      models:
        - name: "claude-opus-4-1-20250805"
          alias: "mira-opus"
```

The HTTP API listens on `server.port` (default 8317) with OpenAI `/v1/chat/completions`, Anthropic `/v1/messages`, and Gemini endpoints; authenticate with any key from `access.api-keys`.

## Management panel

The panel source lives in `management-center/` and follows its own `AGENTS.md` (React 19 + Vite + Bun). Build and stage it for the backend:

```bash
./management-center/build.sh   # bun install + build -> static/management.html
```

The server prefers the local `static/management.html` over the auto-downloaded asset; set `management.disable-auto-update-panel: true` to stop the updater from replacing it. Open `http://<host>:<port>/management.html` and log in with `management.secret-key`.

## new-api gateway

`new-api/` vendors the new-api gateway (own Go module, own `AGENTS.md`; branding and attribution belong to QuantumNous and are kept intact). Build and run it alongside this proxy:

```bash
cd new-api/web && bun install --frozen-lockfile && bun run build   # frontend (embedded)
cd .. && go build -o new-api .
./new-api --port 3000   # first run initializes an admin account; SQLite by default
```

Point its channels at `http://127.0.0.1:8317` with a CLIProxyAPI client key to chain billing/account management through this proxy's upstream pool.

Docker option — run both behind one command and let the bootstrap script wire the channel automatically:

```bash
docker compose -f docker-compose.yml -f docker-compose.newapi.yaml up -d
CLIPROXY_KEY=<one access.api-keys entry> NEW_API_BOOTSTRAP_TOKEN=1 ./scripts/newapi-bootstrap.sh
```

See [docs/new-api-integration.md](docs/new-api-integration.md) for bootstrap variables, manual UI setup, and production notes.

## Docs

- Config template: [config.example.yaml](config.example.yaml)
- SDK: [docs/sdk-usage.md](docs/sdk-usage.md) · [docs/sdk-advanced.md](docs/sdk-advanced.md) · [docs/sdk-access.md](docs/sdk-access.md) · [docs/sdk-watcher.md](docs/sdk-watcher.md)
- Management API v8: [docs/management-api-v8.md](docs/management-api-v8.md)
- Upstream guides: https://help.router-for.me/

## Upstream and ecosystem

This fork tracks [router-for-me/CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI). Ecosystem projects, ports, and the contributor list moved to the upstream README — see there if you're looking for related tools.

## License

MIT — see [LICENSE](LICENSE).
