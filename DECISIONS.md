# Decisions

1. **Go backend, vanilla JS UI embedded with `go:embed`.** One static binary, no front-end build chain.
2. **anacrolix/torrent** for metainfo and seeding; no custom protocol code.
3. **Seed in place.** Per-torrent file storage rooted at the source's parent directory; no copy. Trade-off: editing/moving the source breaks the torrent, so we invalidate and require regeneration (fingerprint check).
4. **Seed-only.** `DisallowDataDownload` so peers can never write into user files.
5. **Virtual paths + no symlinks.** Simpler and safer than trying to sandbox arbitrary symlink targets; the app never creates symlinks. `os.Root` was considered but Go's `Root` lacks rename/remove-all.
6. **Single owner, credentials from environment, in-memory sessions.** Smallest secure auth for an MVP; no secrets in the repo, plaintext password not retained after startup hashing.
7. **PBKDF2 from the standard library** to avoid extra dependencies.
8. **Hard-link upload finalisation** guarantees no overwrite race; requires a filesystem with hard-link support (standard Linux filesystems/Docker volumes).
9. **Web port bound to 127.0.0.1 in Compose**; HTTPS is expected to come from a reverse proxy (`MAGNETOR_COOKIE_SECURE=true`).
10. **Go 1.26** is required by the current dependency set (see `go.mod`).
