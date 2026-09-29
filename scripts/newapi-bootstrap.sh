#!/usr/bin/env bash
# Bootstrap new-api next to CLIProxyAPI:
#   1. Initialize the new-api system (first-run setup wizard) if needed.
#   2. Log in as the admin account.
#   3. Create (or skip, if present) an OpenAI-compatible channel pointing at CLIProxyAPI.
#
# Run from the host after:
#   docker compose -f docker-compose.yml -f docker-compose.newapi.yaml up -d
#
# Environment variables:
#   NEW_API_URL              Base URL of new-api as seen from this host. Default http://localhost:3000
#   NEW_API_ADMIN_USER       Admin username. Default root
#   NEW_API_ADMIN_PASSWORD   Admin password. Required if the system is already initialized.
#                            If the system is NOT initialized this password is used to create
#                            the admin account; when unset a random one is generated and printed.
#   CLIPROXY_KEY             One key from CLIProxyAPI access.api-keys (client auth). Required.
#   CLIPROXY_MODELS_URL      Where to fetch the model list from. Default http://localhost:8317/v1/models
#   NEW_API_MODELS           Comma-separated model list override (skips fetching CLIProxyAPI).
#   NEW_API_CHANNEL_NAME     Channel name in new-api. Default cliproxy
#   NEW_API_CHANNEL_BASE_URL Upstream URL as seen from inside the new-api container.
#                            Default http://cli-proxy-api:8317
#   NEW_API_BOOTSTRAP_TOKEN  When set to 1, also create an unlimited user token and print its key.
set -euo pipefail

NEW_API_URL="${NEW_API_URL:-http://localhost:3000}"
ADMIN_USER="${NEW_API_ADMIN_USER:-root}"
ADMIN_PASSWORD="${NEW_API_ADMIN_PASSWORD:-}"
CLIPROXY_KEY="${CLIPROXY_KEY:-}"
MODELS_URL="${CLIPROXY_MODELS_URL:-http://localhost:8317/v1/models}"
MODELS_OVERRIDE="${NEW_API_MODELS:-}"
CHANNEL_NAME="${NEW_API_CHANNEL_NAME:-cliproxy}"
CHANNEL_BASE_URL="${NEW_API_CHANNEL_BASE_URL:-http://cli-proxy-api:8317}"
BOOTSTRAP_TOKEN="${NEW_API_BOOTSTRAP_TOKEN:-0}"

if [[ -z "$CLIPROXY_KEY" ]]; then
  echo "ERROR: CLIPROXY_KEY is required (a key from CLIProxyAPI access.api-keys)." >&2
  exit 1
fi

json_field() { # json_field <name> : extract first "name":"value" (string values only) from stdin
  sed -n 's/.*"'"$1"'":"\([^"]*\)".*/\1/p' | head -n1
}

has_true() { # has_true <name> <json> : true when the JSON contains "name":true
  [[ "$2" == *"\"$1\":true"* ]]
}

wait_ready() {
  echo "Waiting for new-api at ${NEW_API_URL} ..."
  for _ in $(seq 1 60); do
    if curl -fsS -o /dev/null "${NEW_API_URL}/api/status" 2>/dev/null; then
      echo "new-api is up."
      return 0
    fi
    sleep 2
  done
  echo "ERROR: new-api did not become ready." >&2
  return 1
}

setup_system() {
  local status_json
  status_json="$(curl -fsS "${NEW_API_URL}/api/setup")"
  if has_true status "$status_json"; then
    echo "new-api already initialized."
    return 0
  fi
  if [[ -z "$ADMIN_PASSWORD" ]]; then
    ADMIN_PASSWORD="$(LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom | head -c 16 || true)"
    if [[ ${#ADMIN_PASSWORD} -lt 12 ]]; then
      ADMIN_PASSWORD="Na$(date +%s | sha256sum | head -c 14)"
    fi
    echo "Generated admin password: ${ADMIN_PASSWORD}  (user: ${ADMIN_USER}) — save it, it is only shown once."
  fi
  if [[ ${#ADMIN_PASSWORD} -lt 8 ]]; then
    echo "ERROR: NEW_API_ADMIN_PASSWORD must be at least 8 characters." >&2
    exit 1
  fi
  echo "Initializing new-api with admin account '${ADMIN_USER}' ..."
  local resp
  resp="$(curl -fsS -X POST "${NEW_API_URL}/api/setup" \
    -H 'Content-Type: application/json' \
    -d "{\"username\":\"${ADMIN_USER}\",\"password\":\"${ADMIN_PASSWORD}\",\"confirmPassword\":\"${ADMIN_PASSWORD}\",\"SelfUseModeEnabled\":false,\"DemoSiteEnabled\":false}")"
  if ! has_true success "$resp"; then
    echo "ERROR: setup failed: $resp" >&2
    exit 1
  fi
}

login() {
  if [[ -z "$ADMIN_PASSWORD" ]]; then
    echo "ERROR: NEW_API_ADMIN_PASSWORD is required to log in to an already-initialized system." >&2
    exit 1
  fi
  local resp
  resp="$(curl -fsS -X POST "${NEW_API_URL}/api/user/login" \
    -H 'Content-Type: application/json' \
    -d "{\"username\":\"${ADMIN_USER}\",\"password\":\"${ADMIN_PASSWORD}\"}")"
  ACCESS_TOKEN="$(echo "$resp" | json_field access_token)"
  if [[ -z "$ACCESS_TOKEN" ]]; then
    echo "ERROR: login failed: $resp" >&2
    exit 1
  fi
  echo "Logged in as ${ADMIN_USER}."
}

fetch_models() {
  if [[ -n "$MODELS_OVERRIDE" ]]; then
    MODELS="$MODELS_OVERRIDE"
  else
    MODELS="$(curl -fsS "$MODELS_URL" -H "Authorization: Bearer ${CLIPROXY_KEY}" \
      | grep -o '"id":"[^"]*"' | cut -d'"' -f4 | paste -sd, -)"
  fi
  if [[ -z "$MODELS" ]]; then
    echo "ERROR: model list is empty; pass NEW_API_MODELS explicitly." >&2
    exit 1
  fi
  echo "Models: ${MODELS}"
}

create_channel() {
  local existing
  existing="$(curl -fsS "${NEW_API_URL}/api/channel/search?keyword=${CHANNEL_NAME}" \
    -H "Authorization: Bearer ${ACCESS_TOKEN}")"
  if echo "$existing" | grep -q "\"name\":\"${CHANNEL_NAME}\""; then
    echo "Channel '${CHANNEL_NAME}' already exists, skipping creation."
    return 0
  fi
  local payload resp
  payload="{\"mode\":\"single\",\"channel\":{\"name\":\"${CHANNEL_NAME}\",\"type\":1,\"key\":\"${CLIPROXY_KEY}\",\"base_url\":\"${CHANNEL_BASE_URL}\",\"models\":\"${MODELS}\",\"group\":\"default\"}}"
  resp="$(curl -fsS -X POST "${NEW_API_URL}/api/channel/" \
    -H "Authorization: Bearer ${ACCESS_TOKEN}" \
    -H 'Content-Type: application/json' \
    -d "$payload")"
  if ! has_true success "$resp"; then
    echo "ERROR: channel creation failed: $resp" >&2
    exit 1
  fi
  echo "Channel '${CHANNEL_NAME}' created -> ${CHANNEL_BASE_URL}"
}

create_token() {
  [[ "$BOOTSTRAP_TOKEN" != "1" ]] && return 0
  local resp search id key_resp key
  resp="$(curl -fsS -X POST "${NEW_API_URL}/api/token/" \
    -H "Authorization: Bearer ${ACCESS_TOKEN}" \
    -H 'Content-Type: application/json' \
    -d '{"name":"bootstrap","remain_quota":0,"unlimited_quota":true,"expired_time":-1}')"
  if ! has_true success "$resp"; then
    # Token list endpoints include created ones; a duplicate name is fine to reuse.
    if [[ "$resp" != *"already exists"* && "$resp" != *"已存在"* ]]; then
      echo "ERROR: token creation failed: $resp" >&2
      exit 1
    fi
  fi
  search="$(curl -fsS "${NEW_API_URL}/api/token/search?keyword=bootstrap" \
    -H "Authorization: Bearer ${ACCESS_TOKEN}")"
  id="$(echo "$search" | grep -o '"id":[0-9]*' | head -n1 | cut -d: -f2)"
  if [[ -z "$id" ]]; then
    echo "ERROR: could not find the bootstrap token after creation: $search" >&2
    exit 1
  fi
  key_resp="$(curl -fsS -X POST "${NEW_API_URL}/api/token/${id}/key" \
    -H "Authorization: Bearer ${ACCESS_TOKEN}")"
  key="$(echo "$key_resp" | json_field key)"
  if [[ -z "$key" ]]; then
    echo "ERROR: could not read token key: $key_resp" >&2
    exit 1
  fi
  echo "Bootstrap token created, key: ${key}"
}

wait_ready
setup_system
login
fetch_models
create_channel
create_token
echo "Done. Point users at ${NEW_API_URL} (OpenAI-compatible API at ${NEW_API_URL}/v1)."
