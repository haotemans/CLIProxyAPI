# CLIProxyAPI (self-use fork)

English | [中文](README_CN.md) | [日本語](README_JA.md)

Personal fork of [router-for-me/CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) — a proxy server that provides OpenAI / Gemini / Claude / Codex compatible API interfaces in front of CLI tools and their accounts. This fork adds self-hosted relay providers, per-account usage cost accounting, and bundles its management panel and the new-api gateway into a single repository.

## What this fork adds

- **Mirasim provider** — first-class `mirasim` provider in two credential flavors sharing one provider key: `api-keys.mirasim` for Anthropic-protocol reverse proxies (Bearer key, mandatory `base-url`, models fall back to the Claude catalog, alias mapping, thinking config, full Management API and panel support), and native **Mirasim OAuth** (ported from the MIT cpa-plugin-mirasim): `--mirasim-login` (loopback callback with paste fallback; `--mirasim-login-provider` / `--mirasim-login-email` / `--mirasim-login-code` variants), panel/TUI login via `/v0/management/mirasim-auth-url` with start/authorize/callback/email pages served on CPA's own port, signed relay protocol (mrs-sig-v2 + sealed metadata + device tickets), token refresh with plan change detection, signed `/v1/limits` quota lane, and per-account model probing.
- **Commandcode provider** — first-class `commandcode` API-key provider for the Commandcode relay (OpenAI chat-completions at `https://api.commandcode.ai/provider/v1`, Bearer key, default base-url, built-in catalog `deepseek/deepseek-v4.1-flash` + `z-ai/glm-5.3-flash`, reasoning backfill from `reasoning`/`reasoning_details`, full Management API and panel support).
- **OpenCode Go provider** — first-class `opencode-go` API-key provider for the OpenCode Zen Go relay (OpenAI chat-completions at `https://opencode.ai/zen/go/v1`, Bearer key, default base-url, built-in catalog `kimi-k3` / `glm-5.3` / `big-pickle` / `grok-code-fast-1`, full Management API and panel support) plus the **anonymous Zen free tier**: the literal key `public` binds `https://opencode.ai/zen/v1`, syncs the `*-free` catalog every model refresh (ids priced 0, vanished ids dropped, last-good cache on fetch failure), classifies the per-model `FreeTierError` gate as not_available (prunable) and `Model is unavailable` capacity denials as limited (retryable), and surfaces a hint in the provider form and a `free` badge in the models dialog.
- **Cline provider** — first-class `cline` provider with two credential flavors sharing one executor: `api-keys.cline` for the account API keys Cline now requires for chat completions (`app.cline.bot` → Settings → API Keys; Bearer at `https://api.cline.bot/api/v1/chat/completions`, full Management API and panel support), and the native OAuth login (`--cline-login`, WorkOS device flow, rotating refresh, first-import model detection) which keeps serving discovery and extension flows — OAuth session tokens no longer answer chat (upstream 401), so put an API key in config for inference. Per-account catalogs are **curated** via `GET .../ai/cline/recommended-models` (the official picker's feed): `recommended + free` tiers always; `clinePass + clineCloud` for subscription API keys or via `cline.include-paid-tiers` / per-credential `include_paid_tiers`; the full `…/ai/cline/models` catalog only as fallback when the feed fails. Provider block phases (the periodic third-party 401) classify as `provider_blocked` in the probe engine — nothing prunes, the whole set hides from routing until the next probe succeeds, and pool-inspection suggests `wait-provider` instead of relogin.
- **Cursor provider** — first-class `cursor` OAuth provider: PKCE URL-poll login (`--cursor-login`, Management API, TUI — no local callback, works headless), automatic token refresh, per-account model discovery via `GetUsableModels`, streaming + tool calling over Cursor's Connect/protobuf agent RPC.
- **Kiro provider** — first-class `kiro` OAuth provider (AWS Kiro / CodeWhisperer): AWS Builder ID device-code login (`--kiro-login`, works headless), authorization-code login, IAM Identity Center login, Kiro IDE token import, dynamic per-account model discovery with `-agentic` variants, token refresh via SSO OIDC, and the full Connect EventStream executor with tool calling and thinking support.
- **Native usage/cost accounting** — `usage-stats` in config (on by default) taps every completed request into a local pure-Go sqlite DB (`<auth-dir>/usage-stats.db`, 90-day retention) and exposes `GET /v0/management/usage-meters/summary`, `/usage-meters/series` (group by provider/model/credential/api-key/day) and `/usage-meters/events` (paginated per-request archive with filters); cost comes from upstream-reported spend when available, else token prices from `usage-stats.pricing` merged onto embedded defaults. Pricing is runtime-managed via `GET`/`PUT`/`DELETE /v0/management/usage-meters/pricing` plus `POST .../pricing/sync-litellm` (never clobbers user-set prices). `GET /v0/management/pool-inspection?provider=codex` grades every OAuth credential (disabled/quota/auth-error/probe signals → delete/relogin/rotate suggestions, read-only). All three are browsed natively in the panel (观测 → 用量统计 → 请求记录/定价 tabs; 认证文件 → 巡检). Kiro/Mirasim credentials additionally gain management-driven quota probes (`POST /v0/management/quota/fetch`) and quota lanes.
- **Per-credential model probing** — `model-probe` in config (off by default) sends one tiny real request per advertised model per credential through each provider's own executor, persists usable/pruned results into the credential file's `model_probe` section, prunes not-available models out of routing and `/v1/models`, and re-probes periodically so they re-appear when usable again. Anti-pattern scheduling: every cycle start is jittered (`interval × (1 ± 0.5)` by default), each probe sleeps a random 3–12s, `limited` models are probed at most every 2nd cycle, and auth_error-flagged credentials every 4th. `GET /v0/management/model-probe/status`, `POST /v0/management/model-probe/run`, plus a probe line and 立即探测 button on every auth-file card. V1 drivers: cursor, kiro, cline, claude, codex, xai, devin, meta.
- **Usage cost accounting** — upstream-reported request cost (Cline `usage.cost`) is captured into usage details and exposed as `tokens.cost_usd` on the usage queue (`/v0/management/usage-queue`, needs `usage-statistics-enabled: true`). Token counters were already there; money is now there too.
- **In-repo management panel** — the Management Center frontend lives in `management-center/` (Vite single-file build) and gets served from `static/management.html` instead of the downloaded GitHub asset. It ships the Cline OAuth card and the Mirasim key family.
- **In-repo new-api** — [QuantumNous/new-api](https://github.com/QuantumNous/new-api) vendored under `new-api/` for the side-by-side gateway deployment (self-use branch `mellow-rolling-falcon`).

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

Commandcode / OpenCode Go relays in `config.yaml` (base-url optional, defaults shown):

```yaml
api-keys:
  commandcode:
    - name: commandcode-1
      base-url: "https://api.commandcode.ai/provider/v1"
      keys:
        - api-key: "your-commandcode-key"
  opencode-go:
    - name: opencode-go-1
      base-url: "https://opencode.ai/zen/go/v1"
      keys:
        - api-key: "your-opencode-go-key"
    - name: opencode-go-free  # optional: anonymous Zen free tier (defaults to https://opencode.ai/zen/v1)
      keys:
        - api-key: "public"
```

Cline chat completions in `config.yaml` (API key from the Cline app; required since chat no longer answers OAuth session tokens):

```yaml
api-keys:
  cline:
    - name: cline-1
      base-url: "https://api.cline.bot/api/v1"   # default; /chat/completions and /ai/cline/models join under it
      keys:
        - api-key: "cline-sk-..."                # app.cline.bot → Settings → API Keys
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
