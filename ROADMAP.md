# Roadmap

## Phase 1 – MVP (this repository state)
Auth, file/folder operations, preview, Docker Compose, torrent creation and in-place seeding, tests.

## Phase 2 – Hardening
- Persistent sessions and optional password-hash configuration (`argon2`/`bcrypt`).
- Resumable/chunked uploads; folder upload; drag-and-drop move.
- Folder picker for move/copy (currently a typed path), trash/undo.
- Quotas and disk-space checks.
- Torrent: watch source changes automatically (fsnotify), progress while hashing, per-torrent rate limits, private-torrent option.

## Phase 3 – Sharing and multi-user
- Multiple users with isolated workspaces; per-user quotas.
- Expiring share links; 2FA.

## Phase 4 – Catalog integration
Optional, user-initiated publishing of *metadata only* (magnet link) to a separate Magnetor Catalog. Not started; Drive must continue to work without it.
