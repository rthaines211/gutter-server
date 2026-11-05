# Configuration

Gutter works with just `GUTTER_PASSWORD` set. Everything else is optional.

The config file `/data/config.yaml` is optional. If you save settings in the
admin console (`/_admin/`) it creates the file (mode 0600). Environment
variables override the file.

## Auth

Every endpoint needs an `Authorization` header: Basic (any username plus the
password) or `Bearer <GUTTER_API_KEY>`. Only `GET /healthz` is open.

## Environment variables

| Variable | Default | Meaning |
|---|---|---|
| `GUTTER_PASSWORD` | none | Password for every endpoint. Required unless `GUTTER_AUTH=off`. `change-me` (the compose placeholder) is refused. |
| `GUTTER_API_KEY` | none | Optional bearer token for scripts. |
| `GUTTER_AUTH` | unset | `off` disables authentication. Only for a trusted private network. |
| `GUTTER_COMICVINE_KEY` | none | ComicVine API key. Adds live lookups. |
| `GUTTER_METRON_USER`, `GUTTER_METRON_PASS` | none | Metron account. Adds a fallback metadata source. |
| `GUTTER_WRITEBACK` | on if the library is writable | `true` or `false` overrides `writeback_disabled`. |
| `GUTTER_SCAN_INTERVAL` | `30m` | How often to rescan. `off` means manual scans only. |

## Config file keys

| Key | Default | Meaning |
|---|---|---|
| `port` | `8081` | HTTP port. |
| `comic_dirs` | `[/comics]` | Library folders. |
| `cache_max_items` | `500` | Page cache size. |
| `db_path` | `gutter.db` | SQLite database (`/data/gutter.db` in the container). |
| `cover_dir` | `covers/` next to the DB | Cover thumbnails. |
| `scan_interval` | `30m` | Same as `GUTTER_SCAN_INTERVAL`. |
| `cbr_cache_max_gb` | `2` | Disk cache for unpacked CBR files. |
| `scan_file_delay_ms` | `0` | Pause between files during a scan, to go easy on slow drives. |
| `localcv_db_path` | unset | Your ComicVine dump. Matching is off without it. |
| `gcd_db_path` | unset | Optional Grand Comics Database dump. |
| `comicvine_api_key` | unset | Same as `GUTTER_COMICVINE_KEY`. |
| `metron_user`, `metron_pass` | unset | Same as the `GUTTER_METRON_*` variables. |
| `writeback_disabled` | `false` | `true` turns off metadata writeback. |
| `auto_pipeline` | `false` | After each scheduled scan, match, enrich and write back the new or changed series. |
| `password`, `api_key` | unset | Same as `GUTTER_PASSWORD` and `GUTTER_API_KEY`. Prefer the env vars. |

See [`config.example.yaml`](../config.example.yaml).

## Scanning

An empty library scans on first start. `http://<host>:8081/` shows a status
page with progress until the scan finishes.

## Metadata

Gutter reads ComicInfo.xml from your files. Matching and enrichment need your
own ComicVine dump (`localcv_db_path`); Metron, GCD and a ComicVine API key
only add to it. Gutter does not ship a dump.

## Letting Gutter write to your files

The library is mounted read-only (`:ro` in `compose.yaml`) and a read-only
mount keeps writeback off. Removing `:ro` is the opt-in: on a writable mount,
writeback is on. `GUTTER_WRITEBACK=false` keeps it off even on a writable
mount. Gutter then writes matched metadata into each file's ComicInfo.xml, and
may convert CBR files to CBZ when it does. It only writes for series matched
against your own `localcv` dump. Back up your library first.

## Upgrading

```sh
docker compose pull && docker compose up -d
```

## Backups

Everything Gutter keeps is in the `/data` directory (`./data` in the compose
file). Stop the container before copying it (`docker compose stop`), and never
open the live database from the host: SQLite in WAL mode can be corrupted that
way.

To snapshot a running server, run from the compose directory:

```sh
VOLUME=$PWD/data scripts/gutter-db-backup.sh
```

Snapshots land in `data/db-backups/`. The script fails if it can't find the
database. See its header for the other settings (`DB_FILE`, `OUT_DIR`, `KEEP`).
