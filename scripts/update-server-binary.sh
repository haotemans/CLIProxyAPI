#!/usr/bin/env bash
# update-server-binary.sh — run on the VPS to pull the latest CI-built binary
# from the rolling "server-latest" prerelease and restart cliproxy.
#
# Usage: bash /root/cliproxy/update-server-binary.sh
set -euo pipefail

APP_DIR="${APP_DIR:-/root/cliproxy}"
SERVICE="${SERVICE:-cliproxy}"
REPO="${REPO:-haotemans/CLIProxyAPI}"
TAG="${TAG:-server-latest}"
ASSET="cli-proxy-api-linux-amd64"

BINARY="$APP_DIR/cli-proxy-api"
URL="https://github.com/${REPO}/releases/download/${TAG}/${ASSET}"

echo ">> downloading ${URL}"
curl -fsSL "$URL" -o "${BINARY}.new"
curl -fsSL "${URL}.sha256" -o "${BINARY}.new.sha256"

echo ">> verifying checksum"
expected="$(awk '{print $1}' "${BINARY}.new.sha256")"
actual="$(sha256sum "${BINARY}.new" | awk '{print $1}')"
if [[ "$expected" != "$actual" ]]; then
  echo "checksum mismatch: expected $expected got $actual" >&2
  rm -f "${BINARY}.new" "${BINARY}.new.sha256"
  exit 1
fi
chmod +x "${BINARY}.new"

stamp="$(date +%Y%m%d-%H%M%S)"
if [[ -f "$BINARY" ]]; then
  cp -a "$BINARY" "${BINARY}.bak-${stamp}"
fi
mv "${BINARY}.new" "$BINARY"
rm -f "${BINARY}.new.sha256"

# Keep only the two most recent backups.
ls -t "${BINARY}".bak-* 2>/dev/null | tail -n +3 | xargs -r rm -f

echo ">> restarting ${SERVICE}"
systemctl restart "$SERVICE"
sleep 2
systemctl is-active --quiet "$SERVICE"
echo ">> OK: $(stat -c '%s bytes' "$BINARY"), service active"
journalctl -u "$SERVICE" --since '-10s' --no-pager | tail -3
