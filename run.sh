#!/usr/bin/env bash
# Run the admin server locally against a TEST profile that does not collide
# with a production install (~/.config/blackbox, admin port 8900).
#
# This script uses:
#   config:  ~/.config/platform-test/platform-admin.toml
#   certs:   ~/.config/platform-test/localhost.{cert,key}.pem (self-signed)
#   pki:     ~/.config/platform-test/{ca,intermediate}.{cert,key}.pem (dev CA
#            that signs agent certificates during registration)
#   badger:  ~/.config/platform-test/{admin,logs,metrics,queue}.db
#   port:    8901 (admin API), 8902 (embedded NATS controller), 8903 (NATS
#            monitoring, proxied by the Services page), 8904 (job queue
#            relay service, proxied by the Queue page)
#   nats:    the admin API is also served over the controller as
#            admin_service in every account (admin_server.nats_api) and the
#            queue API as queue_server (queue_server.nats_api); try it with
#            `agent api GET /api/v1/users` from ./run-agent.sh's profile
#
# Every run passes through `admin -init`, which only writes what is missing:
# the first run seeds the config and certificates, later runs fill in anything
# a newer init knows about (the intermediate CA keys, the embedded controller
# section, the queue server section). Edit the TOML as needed. Then in another terminal:
#
#   cd admin/ui && npm run dev      # http://localhost:4400, proxies to :8901
#   ./run-agent.sh                  # register + run a host agent against it
#
# Usage:
#   ./run.sh                        # API only (SPA served by vite)
#   UI=1 ./run.sh                   # rebuild admin/ui and embed it (-tags adminui)
#   ./run.sh -config /other/path    # extra args pass through to cmd/admin

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT_DIR"

TEST_CONFIG_DIR="$HOME/.config/platform-test"
TEST_CONFIG="$TEST_CONFIG_DIR/platform-admin.toml"
TEST_PORT="${PLATFORM_TEST_PORT:-8901}"

echo "==> Checking test profile in $TEST_CONFIG_DIR (port $TEST_PORT)"
go run ./cmd/admin -init -config "$TEST_CONFIG" -port "$TEST_PORT"

TAGS=()
if [[ "${UI:-0}" == "1" ]]; then
  echo "==> Building admin/ui for embed"
  if [[ ! -d admin/ui/node_modules ]]; then
    (cd admin/ui && npm install --silent)
  fi
  (cd admin/ui && npm run build --silent)
  TAGS=(-tags adminui)
fi

echo "==> Starting admin server (test profile) with $TEST_CONFIG"
go run "${TAGS[@]}" ./cmd/admin -config "$TEST_CONFIG" "$@"
