#!/usr/bin/env bash
#
# cleanup.sh - one-time cleanup of orphaned services/files from the old setup.
# Safe to re-run. Run on the server:  sudo bash deploy/cleanup.sh
#
set -euo pipefail

WEBROOT="${READMYVIN_WEBROOT:-/var/www/vindecoder}"

if [ "$(id -u)" -ne 0 ]; then
  echo "please run as root (sudo bash deploy/cleanup.sh)" >&2
  exit 1
fi

echo "== Retiring orphaned units (keeps vindecoder.service) =="
systemctl disable --now go-vindecoder.service vin-counter.service 2>/dev/null || true
rm -f /etc/systemd/system/go-vindecoder.service /etc/systemd/system/vin-counter.service
systemctl daemon-reload
systemctl reset-failed 2>/dev/null || true

echo "== Removing orphaned Python counter =="
rm -rf /opt/vin-counter

echo "== Removing leftover source/old binaries from the webroot =="
# The auto-updater only writes the static assets + `counter`, so these stay gone.
# Runtime data (databases, counter.txt, tsb_data/) is intentionally left alone.
rm -f \
  "$WEBROOT/counter.go" \
  "$WEBROOT/Backupcounter.go" \
  "$WEBROOT/_check.js" \
  "$WEBROOT/new_caddyfile.txt" \
  "$WEBROOT/counter_binary" \
  "$WEBROOT/go-vindecoder" \
  "$WEBROOT/counter.log" \
  "$WEBROOT/vindecoder.log"

echo "== Done. Active backend unit: =="
systemctl --no-pager --type=service | grep -E 'vindecoder|counter' || true
