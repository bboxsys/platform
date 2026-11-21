#!/usr/bin/env bash
# Register a host agent with the local test admin server (./run.sh) and run
# it, using a TEST profile separate from any real agent install
# (/etc/blackbox or ~/.config/blackbox).
#
# This script uses:
#   config:  ~/.config/agent-test/blackbox-agent.json (written by `register`)
#   admin:   https://localhost:8901 (PLATFORM_TEST_PORT to change)
#   login:   admin@localhost / changeme, the test profile's default super
#            admin (ADMIN_EMAIL / ADMIN_PASSWORD to change)
#
# The first run registers: it logs in as the super admin, takes that account's
# ID and session token, and calls `agent register`, which saves the returned
# credentials and certificate to the config above. Later runs skip straight to
# `agent run`, which follows the file and restarts on change.
#
# Usage:
#   ./run.sh                        # in another terminal, first
#   ./run-agent.sh                  # register if needed, then run
#   REGISTER=1 ./run-agent.sh       # force a fresh registration
#   ./run-agent.sh --help           # extra args pass through to `agent run`

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT_DIR"

AGENT_CONFIG_DIR="$HOME/.config/agent-test"
AGENT_CONFIG="$AGENT_CONFIG_DIR/blackbox-agent.json"
TEST_PORT="${PLATFORM_TEST_PORT:-8901}"
ADMIN_URL="${ADMIN_URL:-https://localhost:$TEST_PORT}"
ADMIN_EMAIL="${ADMIN_EMAIL:-admin@localhost}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-changeme}"

for tool in curl jq; do
  command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 1; }
done

# The test admin serves a self-signed localhost certificate, hence -k. Any
# HTTP answer (401 included) means the server is up; only a connection
# failure makes curl exit non-zero here.
if ! curl -ks -o /dev/null "$ADMIN_URL/api/v1/login"; then
  echo "admin server not reachable at $ADMIN_URL; start it with ./run.sh" >&2
  exit 1
fi

# A configuration saved before the admin profile had a controller has no
# controller_url; the agent would run but never ship its logs. Re-register
# so it picks up the controller URL and the trust bundle.
needs_register=0
if [[ "${REGISTER:-0}" == "1" || ! -f "$AGENT_CONFIG" ]]; then
  needs_register=1
elif [[ -z "$(jq -r '.controller_url // ""' "$AGENT_CONFIG")" ]]; then
  echo "==> $AGENT_CONFIG has no controller_url; re-registering"
  needs_register=1
fi
if [[ "$needs_register" == "1" ]]; then
  echo "==> Logging in to $ADMIN_URL as $ADMIN_EMAIL"
  session="$(curl -ksf -u "$ADMIN_EMAIL:$ADMIN_PASSWORD" "$ADMIN_URL/api/v1/login")" || {
    echo "login failed; check ADMIN_EMAIL / ADMIN_PASSWORD" >&2
    exit 1
  }
  account_id="$(jq -r '.user.account_id' <<<"$session")"
  token="$(jq -r '.token' <<<"$session")"
  if [[ -z "$account_id" || "$account_id" == "null" || -z "$token" || "$token" == "null" ]]; then
    echo "login response did not include an account id and token" >&2
    exit 1
  fi

  echo "==> Registering this host under account $account_id"
  mkdir -p "$AGENT_CONFIG_DIR"
  go run ./cmd/agent register \
    --config "$AGENT_CONFIG" \
    --account-id "$account_id" \
    --admin-server-url "$ADMIN_URL" \
    --token "$token"
  echo "==> Agent configuration saved to $AGENT_CONFIG"
fi

echo "==> Running agent with $AGENT_CONFIG"
exec go run ./cmd/agent run --config "$AGENT_CONFIG" "$@"
