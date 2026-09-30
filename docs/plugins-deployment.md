# Native plugin deployment (CGO build)

CLIProxyAPI's native plugin system loads C-ABI dynamic libraries (`.so`) with
`dlopen` (`internal/pluginhost/loader_unix.go`, build tag `cgo && linux`).
A `CGO_ENABLED=0` build reports `plugins_enabled: false` from
`GET /v0/management/plugins` and cannot load any plugin.

## Key facts

- Plugins are **C-ABI** libraries (Go `-buildmode=c-shared`, C, Rust), loaded
  with raw `dlopen` — not Go `plugin.Open`. They therefore do NOT need the
  exact Go toolchain of the host, but the host itself must be a CGO-enabled
  build and every binary must be glibc-compatible with the target machine.
- Cross-building the host for another glibc (e.g. building on Ubuntu 24.04
  WSL for Debian 12) produces a binary that crashes with
  `GLIBC_2.38 not found`. **Build on the deployment host** (or a matching
  glibc container) instead.
- The plugin store index is
  `https://raw.githubusercontent.com/router-for-me/CLIProxyAPI-Plugins-Store/main/registry.json`
  (GitHub release artifacts installed via `POST /v8/management/plugins/store/:id/install`
  or the management panel's Plugins page).
- Plugins land in the `plugins` directory resolved against the service
  WorkingDirectory. The systemd unit uses `WorkingDirectory=/root/cliproxy`,
  so the store installs into `/root/cliproxy/plugins/`.

## CGO build on the Debian 12 target (what this server runs)

```bash
apt-get install -y gcc libc6-dev
curl -fsSL https://golang.google.cn/dl/go1.26.5.linux-amd64.tar.gz | tar -C /usr/local -xz
git clone --depth 1 https://github.com/haotemans/CLIProxyAPI.git /root/build/CLIProxyAPI
cd /root/build/CLIProxyAPI
CGO_ENABLED=1 GOFLAGS=-p=1 /usr/local/go/bin/go build \
  -ldflags="-s -w -X main.Version=$(git rev-parse --short HEAD) -X main.Commit=$(git rev-parse HEAD) -X main.BuildDate=$(date -u +%FT%TZ)" \
  -o cli-proxy-api ./cmd/server
```

`-p=1` keeps memory in check on a 1 vCPU / 512 MB box (build takes ~10–15 min
there). Verify with `ldd cli-proxy-api` (should list libc) and then
`GET /v0/management/plugins` must report `"plugins_enabled": true`.

## Updating after provider merges

Any future rebuild for the server should reuse the same server-side build so
plugin support stays on. The Windows→Linux CGO_ENABLED=0 cross-builds used
earlier are fine for quick iteration but always leave plugins disabled (and,
in the unified application, also leave `/keeper/` unembedded — cpa-usage-keeper
requires the same CGO build; use `unified.*` only with CGO_ENABLED=1).
