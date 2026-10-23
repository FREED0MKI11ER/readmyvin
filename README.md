# ReadMyVIN

A free VIN decoder. Enter a 17-character VIN to get vehicle specs (from the
NHTSA vPIC API), safety ratings, recalls, complaints, and service bulletins
(TSBs), plus maintenance/fluid data.

Live: https://readmyvin.com

## Architecture

```
Browser ──► Caddy (readmyvin.com, TLS)
              ├─ /                static files  (this repo: *.html, *.js, *.css, sw.js, icons)
              ├─ /counter         reverse_proxy 127.0.0.1:8097 ┐
              └─ /api/*           reverse_proxy 127.0.0.1:8097 ┘─► Go API (counter binary)
                                                                   ├─ NHTSA vPIC (VIN decode)
                                                                   ├─ local TSB database (tsb.db)
                                                                   └─ counters (counter.txt, counted_vins.db)
```

- **Frontend:** static HTML/CSS/JS served by Caddy from `/var/www/vindecoder`.
  `sw.js` is a service worker (network-first with cache fallback).
- **Backend:** a single Go binary (`counter`) built from `counter.go`. It serves
  the counter, TSB search, and a vPIC proxy/cache, and imports NHTSA TSB ZIPs
  into `tsb.db` (daily at 3AM; on startup only when the DB is empty).

## Endpoints

| Method | Path | Purpose |
| --- | --- | --- |
| GET/POST | `/counter` | Global decode counter (POST body `{"vinHash":"..."}`) |
| GET | `/api/tsbs` | TSB search: `?make=&model=&year=&max=&offset=&search=` |
| GET | `/api/decode-vin` | vPIC proxy with local cache: `?vin=` |
| GET | `/api/version` | App version (from `VERSION`) |

## Repo layout

```
counter.go                  Go backend (single file, module `counter`)
go.mod / go.sum
index.html dtc.html         pages
scanner.js scanner.css sw.js
manifest.json robots.txt sitemap.xml og-image.svg icon-*.png
maintenance-data-v3.json    fluid/maintenance dataset served to the frontend
deploy/                     server units + auto-updater (see deploy/README.md)
.github/workflows/release.yml
```

## Versioning & releasing

The version is the single line in [`VERSION`](VERSION) (semver, e.g. `1.2.1`).
It is stamped into the footer badge, the service-worker cache name, and the Go
binary (`/api/version`).

**To release:** bump `VERSION`, commit, push to `main`. That's it — deploys are
driven by the version, so a push without a bump will not roll out.

## Deployment (automated)

Bump `VERSION` and push to `main` → GitHub Actions builds the Linux `counter`
binary, bundles it with the static assets + the updater, and publishes a release
tagged `v<VERSION>` (e.g. `v1.2.1`) with `vindecoder-linux-amd64.tar.gz` +
`SHA256SUMS.txt`. The server polls the latest release every 10 minutes
(`readmyvin-update.timer`), verifies the SHA-256, installs the binary + static
assets, restarts `vindecoder.service`, rolls back if the health check fails, and
**self-updates the updater** (script + units) when they change.

Runtime data (`tsb.db`, `counted_vins.db`, `vpic_cache.db`, `counter.txt`,
`tsb_data/`, logs) lives in `/var/www/vindecoder` and is never part of the
release or overwritten by deploys.

See [`deploy/README.md`](deploy/README.md) for one-time server setup.

## Local run

```bash
go build -o counter counter.go
./counter            # serves on :8097; run from a dir where it can write its DBs
./counter import     # force a TSB re-import
./counter reindex    # build search indexes (case-insensitive + FTS5); run once after upgrading
./counter cleanup    # prune cached TSB zips older than 30 days
```

Paths are absolute (`/var/www/vindecoder/...`); for local testing either create
that directory or adjust the constants at the top of `counter.go`.
