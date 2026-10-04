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
PANEL_ASSET="management.html"
HEALTH_URL="${HEALTH_URL:-http://127.0.0.1:8317/}"

BINARY="$APP_DIR/cli-proxy-api"
URL="https://github.com/${REPO}/releases/download/${TAG}/${ASSET}"
PANEL_URL="https://github.com/${REPO}/releases/download/${TAG}/${PANEL_ASSET}"

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

if curl -fsSL -m 30 "$PANEL_URL" -o "${APP_DIR}/management.html.new" 2>/dev/null; then
  if ! cmp -s "${APP_DIR}/management.html.new" "${APP_DIR}/static/management.html" 2>/dev/null; then
    [[ -f "${APP_DIR}/static/management.html" ]] && cp -a "${APP_DIR}/static/management.html" "${APP_DIR}/static/management.html.bak"
    mkdir -p "${APP_DIR}/static"
    mv "${APP_DIR}/management.html.new" "${APP_DIR}/static/management.html"
    echo ">> panel updated"
  else
    rm -f "${APP_DIR}/management.html.new"
    echo ">> panel unchanged"
  fi
else
  echo ">> panel asset not found, skipping"
fi

stamp="$(date +%Y%m%d-%H%M%S)"
PREV_BAK=""
if [[ -f "$BINARY" ]]; then
  PREV_BAK="${BINARY}.bak-${stamp}"
  cp -a "$BINARY" "$PREV_BAK"
fi
mv "${BINARY}.new" "$BINARY"
rm -f "${BINARY}.new.sha256"

# Keep only the two most recent backups.
ls -t "${BINARY}".bak-* 2>/dev/null | tail -n +3 | xargs -r rm -f

echo ">> restarting ${SERVICE}"
systemctl restart "$SERVICE"
sleep 8
if ! systemctl is-active --quiet "$SERVICE" || ! curl -sf -m 10 "$HEALTH_URL" -o /dev/null; then
  echo "!! health check failed (${HEALTH_URL})" >&2
  if [[ -n "$PREV_BAK" && -f "$PREV_BAK" ]]; then
    echo ">> rolling back to ${PREV_BAK}"
    cp -a "$PREV_BAK" "$BINARY"
    systemctl restart "$SERVICE"
    sleep 8
    systemctl is-active --quiet "$SERVICE" || { echo "!! rollback failed, service still down" >&2; exit 1 }
    echo ">> rolled back, service active again"
  fi
  exit 1
fi
echo ">> OK: $(stat -c '%s bytes' "$BINARY"), service active, health check passed"
journalctl -u "$SERVICE" --since '-10s' --no-pager | tail -3
