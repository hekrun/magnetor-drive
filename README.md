# Magnetor Drive

Self-hosted private cloud drive for managing files and folders, with built-in torrent creation and seeding from files stored on your own server. Files never leave your server for any central Magnetor service.

See [PROJECT_BRIEF.md](PROJECT_BRIEF.md), [ARCHITECTURE.md](ARCHITECTURE.md), [ROADMAP.md](ROADMAP.md), [DECISIONS.md](DECISIONS.md).

## Quick start (Docker Compose)

```sh
cp .env.example .env      # set MAGNETOR_ADMIN_PASSWORD (min 12 chars)
docker compose up -d --build
```

Open http://127.0.0.1:8080. Data lives in the `magnetor-data` volume (`/data`). Put an HTTPS reverse proxy in front before exposing it, and set `MAGNETOR_COOKIE_SECURE=true`. Publish/forward TCP+UDP 42069 for BitTorrent peers.

## Run locally

```sh
MAGNETOR_ADMIN_PASSWORD='a-long-passphrase' go run ./cmd/magnetor-drive
```

## Configuration

| Variable | Default | Description |
|---|---|---|
| `MAGNETOR_ADMIN_PASSWORD` | *required* | Owner password, min 12 characters |
| `MAGNETOR_ADMIN_USER` | `admin` | Owner username |
| `MAGNETOR_DATA_DIR` | `./data` (`/data` in Docker) | Persistent data (`files/`, `torrents/`) |
| `MAGNETOR_LISTEN_ADDR` | `:8080` | HTTP listen address |
| `MAGNETOR_COOKIE_SECURE` | `false` | Mark the session cookie `Secure` (use with HTTPS) |
| `MAGNETOR_SESSION_HOURS` | `12` | Session lifetime |
| `MAGNETOR_MAX_UPLOAD_MB` | `10240` | Per-file upload limit; `0` = unlimited |
| `MAGNETOR_TORRENT_ENABLED` | `true` | Enable torrent features |
| `MAGNETOR_TORRENT_PORT` | `42069` | BitTorrent listen port |
| `MAGNETOR_TORRENT_DHT` | `true` | Announce to the public DHT |
| `MAGNETOR_TORRENT_UPNP` | `false` | Try UPnP/NAT-PMP port forwarding |
| `MAGNETOR_TORRENT_TRACKERS` | *(none)* | Comma-separated tracker URLs added to new torrents |

## Torrents

Select a file or folder and choose **Create torrent**. You get a `.torrent` download and a magnet link, and seeding starts from the stored files without copying them. Use *Stop seeding* / *Start seeding* to control it.

**Important:** editing, renaming, moving or deleting a seeded source (or adding files to a seeded folder) invalidates its torrent. It stops seeding and shows `invalid`; select the item again and choose **Create torrent** to regenerate it. Anyone who has the magnet link or `.torrent` can download the content while you seed, and DHT announces the info hash publicly.

## Development

```sh
gofmt -l . && go vet ./... && go test ./... && node --check web/static/app.js && docker compose config
```

## Known limitations

No multi-user, sharing links, resumable uploads or Catalog integration (see the roadmap). Sessions are in memory. Hashing large torrents is synchronous in the create request. Move/copy ask for a typed folder path. Seeding verification re-hashes data on every start.
