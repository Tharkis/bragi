# Bragi

Bragi is one app for your media collection. Request a show, and Bragi finds it through your indexers, sends it to your download client, and files it into your library. It replaces a stack of *arr apps with one container, one database and one settings page.

**Status:** early. TV, movies, ebooks and audiobooks work end to end: request something, Bragi searches your indexers, sends the best release to your download client, then renames it into your library and tells Jellyfin. Music comes later.

| | Now | Later |
| --- | --- | --- |
| Indexers | Prowlarr, Jackett, any Newznab / Torznab URL | |
| Download clients | NZBGet, SABnzbd, qBittorrent, slskd (Soulseek) | Transmission, Deluge |
| Media | TV, movies, ebooks, audiobooks | Music |
| Requests | Web UI, REST API, [Jellyfin plugin](jellyfin-plugin/README.md), Jellyseerr-compatible API | |

### Download client tips

- **Categories.** Set one category, or a different one per media type under *More options* (e.g. `tv`, `movies`, `books`, `audiobooks`). The Test button warns when NZBGet has no such category.
- **Path mapping.** If the client runs in its own container and sees its downloads as `/downloads` while Bragi sees `/data/usenet`, enter both under *More options*. Mapping the same `/data` share into every container avoids this and lets Bragi hardlink.
- **NZBGet** needs version 16 or newer and its control or restricted login; the add-only login cannot see the queue. Bragi treats damaged, password-protected or unpacking-failed downloads as failed, blocklists the release and grabs another.

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

Open `http://your-server:8787` and the setup wizard walks you through the admin account, a TV folder, a TMDB API key (free, for show information), an indexer and a download client. If your TV folder already has shows in it, the wizard offers to add them; nothing is moved or downloaded.

### Settings

| Variable | Default | Meaning |
| --- | --- | --- |
| `PUID` / `PGID` | `99` / `100` | User and group Bragi runs as |
| `UMASK` | `002` | Permission mask for new files |
| `TZ` | `Etc/UTC` | Time zone |
| `BRAGI_PORT` | `8787` | Web UI and API port |
| `BRAGI_CONFIG_DIR` | `/config` | Database, logs and backups |
| `BRAGI_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |
| `BRAGI_TMDB_API_KEY` | | TMDB key to use when none is saved in Settings |

## How TV works

1. **Request.** Pick a show on Discover and choose all seasons, the latest season, future episodes only, or specific seasons. Admin requests (and users with auto-approve) start at once; others wait on the Requests page.
2. **Search.** Every aired, wanted episode is searched on all enabled indexers. A season that has fully aired is tried as a season pack first. Releases are ranked by the library's quality preset, then proper/repack, then Usenet over torrent, then seeders. Anything over the preset's size cap, of the wrong show or episode, or that failed before, is skipped. Nothing suitable? Bragi tries again every 6 hours.
3. **Download.** The best release goes to the first enabled client for its protocol. Bragi checks progress every 30 seconds.
4. **Import.** Finished files are renamed to `Show (Year)/Season 01/Show (Year) - S01E02 - Title [WEBDL-1080p].mkv`. Torrents are hardlinked so they keep seeding; Usenet downloads are moved. A failed download is blocklisted and the episode is searched again.
5. **Done.** Jellyfin (if set up under Settings, Connections) is asked to scan, and the request turns Available once every wanted episode is in.

Downloads and the library must be visible to Bragi at the same paths the download client reports, so map one shared `/data` folder into Bragi and your clients.

Bragi snapshots its database to `/config/backups` once a day and keeps seven.

## API

Everything the UI does goes through the REST API under `/api/v1`. Scripts authenticate with the `X-Api-Key` header; find your key under Settings, Account.

```
GET  /api/v1/discover/search?type=tv&q=   search TMDB, marked with what you already have
GET  /api/v1/discover/trending?type=tv    this week's trending shows, marked the same way
POST /api/v1/requests                     {"type":"tv","tmdbId":95396,"seasons":"all"|"latest"|"future"|[1,2]}
GET  /api/v1/requests?mine=true&status=   requests with progress
GET  /api/v1/requests/{id}                one request, with every episode's status
POST /api/v1/requests/{id}/approve|deny   admins
GET  /api/v1/events                       server-sent events: request, series, episodes, download
GET  /api/v1/series, /series/{id}         the library
GET  /api/v1/activity/queue|history       downloads
```

The [Jellyfin plugin](jellyfin-plugin/README.md) signs in with one admin API key and sends the Jellyfin user's id in `X-Bragi-User` (and name in `X-Bragi-User-Name`). Bragi maps it to a requester account, created on first use, so requests carry the right name and go through approval.

### Jellyseerr-compatible API

Tools written for Jellyseerr, such as Jellyfin Enhanced, can use Bragi instead: point them at `http://your-server:8787/seerr` with a Bragi admin API key. Bragi answers the request-related endpoints (status, users including `import-from-jellyfin` and `X-Api-User`, search, trending, show details, and requests), and returns 404 for the rest.

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
internal/tv/            the TV pipeline: add shows, search, grab, track, import, adopt a folder
internal/release/       release-name parsing and ranking against quality presets
internal/metadata/      TMDB client
internal/fakes/         fake TMDB, Prowlarr, NZBGet and qBittorrent for tests
internal/store/         SQLite queries
internal/db/            connection, migrations (migrations/*.sql), backups
web/                    Svelte UI, built into web/dist and embedded in the binary
docker/, unraid/        container entrypoint and Unraid template
```

Adding a client: implement `Indexer` or `DownloadClient` in `internal/integrations`, then add a `Kind` with its form fields. The UI builds its form from that list, so no UI change is needed.

## License

GPL-3.0. See [LICENSE](LICENSE).
