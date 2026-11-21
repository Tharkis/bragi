# Bragi

Bragi is one app for your media collection. Request a show, and Bragi finds it through your indexers, sends it to your download client, and files it into your library. It replaces a stack of *arr apps with one container, one database and one settings page.

**Status:** early. This is the core skeleton: sign-in, the first-run wizard, and settings for indexers, download clients and libraries, each with a working connection test. Searching, downloading and importing TV arrive in the next step.

| | v1 (TV) | Later |
| --- | --- | --- |
| Indexers | Prowlarr, Jackett, any Newznab / Torznab URL | |
| Download clients | NZBGet, qBittorrent | SABnzbd, Transmission, Deluge, slskd (Soulseek) |
| Media | TV | Movies, music, books, audiobooks |
| Requests | Web UI and REST API | Jellyfin plugin, Jellyseerr-compatible API |

## Run it

### Unraid

Add the template from `unraid/bragi.xml` (Community Applications submission comes once the TV slice works). Map one `/data` share that holds both downloads and media, so imports are instant hardlinks instead of copies.

### Docker Compose

```yaml
services:
  bragi:
    image: ghcr.io/tharkis/bragi:latest
    restart: unless-stopped
    ports: ["8787:8787"]
    environment: [PUID=1000, PGID=1000, TZ=Etc/UTC]
    volumes:
      - ./config:/config
      - /path/to/data:/data
```

Open `http://your-server:8787` and the setup wizard walks you through the admin account, a TV folder, an indexer and a download client.

### Settings

| Variable | Default | Meaning |
| --- | --- | --- |
| `PUID` / `PGID` | `99` / `100` | User and group Bragi runs as |
| `UMASK` | `002` | Permission mask for new files |
| `TZ` | `Etc/UTC` | Time zone |
| `BRAGI_PORT` | `8787` | Web UI and API port |
| `BRAGI_CONFIG_DIR` | `/config` | Database, logs and backups |
| `BRAGI_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |

Bragi snapshots its database to `/config/backups` once a day and keeps seven.

## API

Everything the UI does goes through the REST API under `/api/v1`. Scripts authenticate with the `X-Api-Key` header; find your key under Settings, Account.

```
GET  /api/v1/health
GET  /api/v1/indexers                 POST /api/v1/indexers/test
GET  /api/v1/downloadclients          POST /api/v1/downloadclients/{id}/test
GET  /api/v1/libraries                GET  /api/v1/qualitypresets
```

## Develop

Needs Go 1.26+ and Node 22+.

```sh
make test          # go vet + go test
make run           # builds the UI and binary, serves on :8787 with ./config
cd web && npm run dev   # UI with hot reload, proxying /api to :8787
```

Layout:

```
cmd/bragi/              entry point
internal/api/           HTTP handlers, auth, embedded UI serving
internal/integrations/  indexer and download-client adapters (one file each)
internal/store/         SQLite queries
internal/db/            connection, migrations (migrations/*.sql), backups
web/                    Svelte UI, built into web/dist and embedded in the binary
docker/, unraid/        container entrypoint and Unraid template
```

Adding a client: implement `Indexer` or `DownloadClient` in `internal/integrations`, then add a `Kind` with its form fields. The UI builds its form from that list, so no UI change is needed.

## License

GPL-3.0. See [LICENSE](LICENSE).
