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

Config lives in `/etc/default/readmyvin-update`:

| Variable | Meaning | Default |
| --- | --- | --- |
| `READMYVIN_REPO` | GitHub repo hosting the release | `FREED0MKI11ER/readmyvin` |
| `READMYVIN_WEBROOT` | Directory Caddy serves | `/var/www/vindecoder` |
| `READMYVIN_SERVICE` | systemd unit to restart | `vindecoder` |
| `READMYVIN_BINARY` | Binary filename in the webroot | `counter` |
| `READMYVIN_HEALTH_URL` | Post-deploy health check | `http://127.0.0.1:8097/counter` |

## How updates flow

```
push to main -> GitHub Actions builds `counter` + publishes "latest" release
             -> readmyvin-update.timer polls every 10 min
             -> downloads bundle, verifies SHA-256
             -> backs up current binary + static files
             -> installs files + swaps binary
             -> restarts $READMYVIN_SERVICE, health-checks, rolls back on failure
```

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
