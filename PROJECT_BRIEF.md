# Project Brief

**Magnetor Drive** is a self-hosted private cloud drive. Each owner runs their own instance on their own server; files are stored on that server and never sent to any central Magnetor service.

## Scope of this repository

Only the Drive application. A separate *Magnetor Catalog* project will be built later; Drive neither implements nor depends on it.

## Goals (MVP)

- Authenticated, responsive browser UI for a private file/folder workspace.
- Create folders; upload, download, rename, move, copy, preview and delete.
- Persistent, configurable data directory (Docker volume by default).
- "Create torrent" for a file or folder: produces a `.torrent` and magnet link with a maintained library ([anacrolix/torrent](https://github.com/anacrolix/torrent)) and seeds directly from the stored files (no second copy), with visible state and a stop control.
- Server paths stay private; path traversal is prevented; no browsing of arbitrary server directories.

## Non-goals (for now)

Multi-user accounts, sharing links, Catalog integration, editing files in the browser, resumable uploads, end-to-end encryption, mobile apps. See [ROADMAP.md](ROADMAP.md).

## Principles

1. Owner's data stays on the owner's server.
2. Small, understandable, secure defaults over features.
3. Documented limitations rather than pretend completeness.
4. Do not implement BitTorrent ourselves.
