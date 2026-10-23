#!/usr/bin/env bash
#
# readmyvin-update.sh
# Polls the latest GitHub release and deploys it if the release tag changed.
# Intended to be run by readmyvin-update.timer (systemd) every 10 minutes, as root.
#
# It only ever touches the Go binary + the static asset list shipped inside the
# release bundle, plus this script and its systemd units (self-update). Runtime
# data (databases, counter.txt, logs, tsb_data/) is never modified. On a failed
# health check the previous files are restored automatically.
#
set -euo pipefail

REPO="${READMYVIN_REPO:-FREED0MKI11ER/readmyvin}"
WEBROOT="${READMYVIN_WEBROOT:-/var/www/vindecoder}"
SERVICE="${READMYVIN_SERVICE:-vindecoder}"       # systemd unit to restart
BINARY="${READMYVIN_BINARY:-counter}"           # deployed binary filename in $WEBROOT
HEALTH_URL="${READMYVIN_HEALTH_URL:-http://127.0.0.1:8097/counter}"
STATE="${READMYVIN_STATE:-/var/lib/readmyvin/deployed.version}"
BACKUP_ROOT="${READMYVIN_BACKUP_DIR:-/var/lib/readmyvin/backups}"
SELF_PATH="${READMYVIN_SELF:-/usr/local/bin/readmyvin-update.sh}"
UNIT_DIR="/etc/systemd/system"
KEEP_BACKUPS=5

ASSET="vindecoder-linux-amd64.tar.gz"
SUMS="SHA256SUMS.txt"
API="https://api.github.com/repos/${REPO}/releases/latest"
LOCK="/var/lock/readmyvin-update.lock"

log() { echo "[readmyvin-update] $*"; }

exec 9>"$LOCK"
flock -n 9 || { log "another run is in progress; exiting"; exit 0; }

mkdir -p "$(dirname "$STATE")" "$BACKUP_ROOT"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# --- 1) resolve the latest release -----------------------------------------
log "checking ${REPO} for updates..."
release_json="$(curl -fsSL -H 'Accept: application/vnd.github+json' "$API")"
tag="$(printf '%s' "$release_json" | grep -o '"tag_name": *"[^"]*"' | head -1 | cut -d'"' -f4)"
asset_url="$(printf '%s' "$release_json" | grep '"browser_download_url"' | grep "$ASSET" | head -1 | cut -d'"' -f4)"
sums_url="$(printf '%s' "$release_json" | grep '"browser_download_url"' | grep "$SUMS" | head -1 | cut -d'"' -f4)"

if [ -z "$tag" ] || [ -z "$asset_url" ] || [ -z "$sums_url" ]; then
  log "release has no tag / $ASSET / $SUMS yet; nothing to do"
  exit 0
fi

# --- 2) compare release tag against what is deployed -----------------------
current="$(cat "$STATE" 2>/dev/null || true)"
if [ "$tag" = "$current" ]; then
  log "already up to date ($tag)"
  exit 0
fi
log "update available: $tag"

# --- 3) download + verify ---------------------------------------------------
curl -fsSL "$sums_url" -o "$tmp/$SUMS"
expected="$(grep " $ASSET\$" "$tmp/$SUMS" | awk '{print $1}' | head -1)"
[ -n "$expected" ] || { log "no checksum listed for $ASSET"; exit 1; }

curl -fsSL "$asset_url" -o "$tmp/$ASSET"
actual="$(sha256sum "$tmp/$ASSET" | awk '{print $1}')"
if [ "$actual" != "$expected" ]; then
  log "checksum mismatch (expected $expected, got $actual); aborting"
  exit 1
fi
log "checksum verified"

# --- 4) extract + sanity checks --------------------------------------------
mkdir -p "$tmp/pkg"
tar -C "$tmp/pkg" -xzf "$tmp/$ASSET"

[ -f "$tmp/pkg/$BINARY" ] || { log "bundle is missing binary '$BINARY'"; exit 1; }
[ -f "$tmp/pkg/FILES" ] || { log "bundle is missing FILES manifest"; exit 1; }

magic="$(head -c4 "$tmp/pkg/$BINARY" | od -An -tx1 | tr -d ' \n')"
[ "$magic" = "7f454c46" ] || { log "bundle binary is not a Linux ELF; aborting"; exit 1; }

# --- 5) back up the files we are about to replace --------------------------
stamp="$(date +%Y%m%d-%H%M%S)"
backup="$BACKUP_ROOT/$stamp"
mkdir -p "$backup"
[ -f "$WEBROOT/$BINARY" ] && cp -a "$WEBROOT/$BINARY" "$backup/$BINARY"
while IFS= read -r f; do
  [ -n "$f" ] || continue
  [ -f "$WEBROOT/$f" ] && cp -a "$WEBROOT/$f" "$backup/$f"
done < "$tmp/pkg/FILES"
log "backed up current files to $backup"

# --- 6) install static assets ----------------------------------------------
while IFS= read -r f; do
  [ -n "$f" ] || continue
  [ -f "$tmp/pkg/$f" ] || { log "bundle is missing asset '$f'; aborting"; exit 1; }
  install -m 0644 "$tmp/pkg/$f" "$WEBROOT/$f"
done < "$tmp/pkg/FILES"

# --- 7) atomic binary swap --------------------------------------------------
install -m 0755 "$tmp/pkg/$BINARY" "$WEBROOT/$BINARY.new"
mv -f "$WEBROOT/$BINARY.new" "$WEBROOT/$BINARY"

# --- 8) restart + health check (roll back on failure) ----------------------
log "restarting $SERVICE..."
systemctl restart "$SERVICE"
sleep 3
if ! curl -fsS --max-time 10 "$HEALTH_URL" >/dev/null 2>&1; then
  log "health check FAILED; rolling back"
  [ -f "$backup/$BINARY" ] && cp -a "$backup/$BINARY" "$WEBROOT/$BINARY"
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    [ -f "$backup/$f" ] && cp -a "$backup/$f" "$WEBROOT/$f"
  done < "$tmp/pkg/FILES"
  systemctl restart "$SERVICE"
  exit 1
fi

# --- 9) record success ------------------------------------------------------
printf '%s\n' "$tag" > "$STATE"
log "deployed $tag successfully"

# --- 10) self-update the updater script + systemd units --------------------
bundle_updater="$tmp/pkg/updater/readmyvin-update.sh"
if [ -f "$bundle_updater" ] && [ -f "$SELF_PATH" ] && ! cmp -s "$bundle_updater" "$SELF_PATH"; then
  if bash -n "$bundle_updater"; then
    cp -a "$SELF_PATH" "$backup/readmyvin-update.sh"
    install -m 0755 "$bundle_updater" "$SELF_PATH"
    log "updater script updated (takes effect next run)"
  else
    log "new updater failed syntax check; keeping current version"
  fi
fi

units_changed=0
for u in readmyvin-update.service readmyvin-update.timer; do
  src="$tmp/pkg/updater/$u"
  dst="$UNIT_DIR/$u"
  if [ -f "$src" ] && { [ ! -f "$dst" ] || ! cmp -s "$src" "$dst"; }; then
    [ -f "$dst" ] && cp -a "$dst" "$backup/$u"
    install -m 0644 "$src" "$dst"
    log "updated unit $u"
    units_changed=1
  fi
done
if [ "$units_changed" = 1 ]; then
  systemctl daemon-reload
  log "systemd reloaded"
fi

# --- 11) prune old backups --------------------------------------------------
ls -1dt "$BACKUP_ROOT"/*/ 2>/dev/null | tail -n +$((KEEP_BACKUPS + 1)) | xargs -r rm -rf
