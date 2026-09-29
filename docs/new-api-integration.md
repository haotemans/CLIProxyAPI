# new-api Integration

This repository can run [new-api](https://github.com/QuantumNous/new-api) as the
user-facing API distribution gateway in front of CLIProxyAPI:

- **new-api** handles user accounts, sign-up, top-up/billing, quotas, per-model pricing,
  API token issuance, channel health and analytics.
- **CLIProxyAPI** handles what it is good at: pooling CLI-derived OAuth credentials and
  exposing them as OpenAI/Gemini/Claude/Codex-compatible endpoints.

Traffic flows: `users -> new-api (:3000) -> CLIProxyAPI (:8317) -> upstream providers`.

Billing works out of the box because CLIProxyAPI returns standard `usage` fields that
new-api reads for quota settlement.

## Layout

| Path | Purpose |
| --- | --- |
| `new-api/` | new-api source, vendored in-repo as an independent Go module (reference and custom image builds) |
| `docker-compose.newapi.yaml` | Compose merge file adding the `new-api` service |
| `scripts/newapi-bootstrap.sh` | First-run bootstrap: init wizard, admin login, channel creation |

## Quick start

1. Make sure CLIProxyAPI is configured and has at least one client key under
   `access.api-keys` in `config.yaml` (see `config.example.yaml`).

2. Start both services:

   ```bash
   docker compose -f docker-compose.yml -f docker-compose.newapi.yaml up -d
   ```

   new-api listens on `http://localhost:3000` (override with `NEW_API_PORT`) and stores
   its SQLite database under `./data/new-api` (override with `NEW_API_DATA_PATH`).
   Set `NEW_API_SQL_DSN` to use MySQL/PostgreSQL instead, e.g.
   `new-api passes SQL_DSN through: user:pass@tcp(host:3306)/new-api`.

3. Bootstrap the channel:

   ```bash
   CLIPROXY_KEY=<one access.api-keys entry> NEW_API_BOOTSTRAP_TOKEN=1 \
     ./scripts/newapi-bootstrap.sh
   ```

   The script waits for new-api, runs the first-run setup wizard (printing a generated
   admin password if you did not set `NEW_API_ADMIN_PASSWORD`), fetches the model list
   from CLIProxyAPI, creates an OpenAI-type channel named `cliproxy` pointing at
   `http://cli-proxy-api:8317`, and optionally prints a ready-to-use `sk-` token.
   It is idempotent — re-running skips existing resources.

Clients then use `http://localhost:3000/v1` with their new-api token keys. The
CLIProxyAPI port (8317) no longer needs to be public; only new-api faces users.

## Manual configuration (alternative to the script)

In the new-api admin UI (`http://localhost:3000`):

1. Complete the setup wizard on first visit and log in.
2. Channels -> Add: type **OpenAI**, name `cliproxy`,
   Base URL `http://cli-proxy-api:8317`, key = one of CLIProxyAPI's `access.api-keys`,
   models = the CLIProxyAPI model list (copy from `http://localhost:8317/v1/models`
   with your CLIProxyAPI key), group `default`.
3. Create user tokens under Tokens and hand them to your users.

## Bootstrap script variables

| Variable | Default | Purpose |
| --- | --- | --- |
| `NEW_API_URL` | `http://localhost:3000` | new-api base URL from the host |
| `NEW_API_ADMIN_USER` / `NEW_API_ADMIN_PASSWORD` | `root` / generated | Admin credentials (password required when already initialized) |
| `CLIPROXY_KEY` | — (required) | A CLIProxyAPI `access.api-keys` entry used by the channel |
| `CLIPROXY_MODELS_URL` | `http://localhost:8317/v1/models` | Model list source |
| `NEW_API_MODELS` | auto | Comma-separated model list override |
| `NEW_API_CHANNEL_NAME` | `cliproxy` | Channel name |
| `NEW_API_CHANNEL_BASE_URL` | `http://cli-proxy-api:8317` | Upstream URL from inside the new-api container |
| `NEW_API_BOOTSTRAP_TOKEN` | `0` | Set `1` to also create an unlimited demo token |

## Updating new-api

new-api is vendored as plain source under `new-api/` (currently QuantumNous/new-api @ 789c97019).
To update, re-vendor from a fresh upstream clone and commit the result:

```bash
rm -rf new-api && git clone --depth 1 https://github.com/QuantumNous/new-api.git new-api \
  && rm -rf new-api/.git && git add new-api && git status --short | head
```

To deploy the update, bump the image tag via `NEW_API_IMAGE` or build from `new-api/`
by uncommenting the `build:` block in `docker-compose.newapi.yaml`.

## Production notes

- Use strong values for `access.api-keys`; new-api's channel key is a full credential
  for your OAuth pool.
- Keep 8317 behind the internal network (default compose already only needs 3000 public;
  remove the 8317 port mapping from `docker-compose.yml` if you want it fully internal).
- Set `NEW_API_SESSION_SECRET` so sessions survive container recreation.
- Keep the vendored `new-api/` pristine; carry local tweaks as small reviewed commits
  on top of the vendored snapshot so re-vendoring stays a clean copy.
