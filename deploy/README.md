# Deployment

The site is served by Caddy from `/var/www/vindecoder` (static files) and proxied
to the Go backend on `127.0.0.1:8097` (`/counter`, `/api/*`).

Two things are managed here:

1. the **backend service** (`vindecoder.service`) that runs the Go binary
2. the **auto-updater** (`readmyvin-update.timer`) that polls the GitHub release
   every 10 minutes and deploys changes

## One-time backend setup

Kill any manually started process first, then install the unit:

```bash
sudo install -m 0644 vindecoder.service /etc/systemd/system/vindecoder.service
sudo systemctl daemon-reload
sudo systemctl enable --now vindecoder.service
# retire any old units
sudo systemctl disable --now go-vindecoder.service vin-counter.service
```

## Auto-updater

```bash
sudo bash install.sh
```

This installs `readmyvin-update.sh` to `/usr/local/bin`, the `.service`/`.timer`
units, writes `/etc/default/readmyvin-update` (edit if your names/paths differ),
enables the timer, and runs one update immediately.

> **Self-updating.** The release bundle ships the updater script + units, so once
> this version is installed, future changes to them roll out automatically (the
> running script replaces itself; the new logic applies on the next tick). The
> very first switch to a self-updating updater requires one manual `install.sh`
> run, since the previously installed script has no self-replace code.

Config lives in `/etc/default/readmyvin-update`:

| Variable | Meaning | Default |
| --- | --- | --- |
| `READMYVIN_REPO` | GitHub repo hosting the release | `FREED0MKI11ER/readmyvin` |
| `READMYVIN_WEBROOT` | Directory Caddy serves | `/var/www/vindecoder` |
| `READMYVIN_SERVICE` | systemd unit to restart | `vindecoder` |
| `READMYVIN_BINARY` | Binary filename in the webroot | `counter` |
| `READMYVIN_HEALTH_URL` | Post-deploy health check | `http://127.0.0.1:8097/counter` |
| `READMYVIN_STATE` | File recording the deployed tag | `/var/lib/readmyvin/deployed.version` |

## How updates flow

```
bump VERSION + push to main
   -> GitHub Actions builds `counter` + publishes release tagged v<VERSION> (e.g. v1.2.1)
   -> readmyvin-update.timer polls every 10 min
   -> compares the release tag to /var/lib/readmyvin/deployed.version
   -> downloads bundle, verifies SHA-256
   -> backs up current binary + static files
   -> installs files + swaps binary
   -> restarts $READMYVIN_SERVICE, health-checks, rolls back on failure
   -> optionally self-updates the updater script + units
```

Deploys are **version-driven**: bumping `VERSION` is what triggers a rollout.

Runtime data (`tsb.db`, `counted_vins.db`, `vpic_cache.db`, `counter.txt`,
`tsb_data/`, logs) is never touched. Backups are kept in
`/var/lib/readmyvin/backups` (last 5).

## Verify

```bash
journalctl -u readmyvin-update.service -n 50 --no-pager
journalctl -u vindecoder.service -n 50 --no-pager
curl -s localhost:8097/api/version
systemctl list-timers readmyvin-update.timer
```

## One-time reindex (v1.2.2+)

TSB search uses a case-insensitive vehicle index plus an FTS5 full-text index,
built lazily. After the first deploy of v1.2.2+, run this **once** (it takes a
few minutes and adds disk usage to `tsb.db`):

```bash
cd /var/www/vindecoder
./counter reindex
```

Until it finishes, search falls back to the old `LIKE` scan. New TSB rows are
kept in the FTS index automatically afterwards (via a trigger).
