# Changelog

## v0.2.0

Upgrading from v0.1.0 needs no config changes. See [Upgrading](#upgrading-from-v010) below.

### New

- **OPDS catalog** at `/opds/`, with OPDS-PSE page streaming. KOReader,
  Panels, Chunky and other OPDS readers can browse, search, download and
  stream your library with your Gutter password. (#2)
- **KOReader sync server** at `/kosync`. Point KOReader's progress sync at it
  and your place carries between KOReader and the Gutter app, in both
  directions. Setup: [docs/OPDS-KOREADER.md](docs/OPDS-KOREADER.md). (#2)
- **Get the app** block on the status page (`GET /`): an App Store link and a
  QR code, so people you share the server with can find the reader. (#6)
- **Reading orders you make yourself**: `PUT /arcs/{id}` and
  `DELETE /arcs/{id}` save your own reading orders, which reach the app
  through `/sync` like imported story arcs. Imported arcs can't be changed
  this way (409). (#8)

### Fixed

- **Bookmarks now save.** v0.1.0 rejected the app's bookmark payload, so
  bookmarks were never stored and progress saves failed while a bookmark was
  set on that issue. (#5)
- **Pages show in the right order.** Pages inside a CBZ/CBR were served in
  raw byte order, so `page10.jpg` came before `page2.jpg`. They now follow
  natural, case-insensitive order, the same rule the app uses for files it
  imports. Your reading progress, bookmarks and chapter marks move with the
  pages, so nobody loses their place (see below). (#7)

## Upgrading from v0.1.0

1. Back up `/data` (`./data` next to your `compose.yaml`). Either stop the
   container and copy the folder, or snapshot it while it runs:

   ```sh
   VOLUME=$PWD/data scripts/gutter-db-backup.sh
   ```

2. Pull and restart:

   ```sh
   docker compose pull && docker compose up -d
   ```

   The compose file uses `latest`. To pin this release instead, set the
   image to `ghcr.io/rthaines211/gutter-server:0.2.0`.

3. Check the version on the status page (`http://<host>:8081`) reads
   0.2.0.

### What happens on the first start

- **A one-time page-order pass runs in the background.** It only opens
  comics that have saved progress, bookmarks or chapter marks, reads their
  page names (not the images), and moves each saved page number to that
  page's new position. Comics that were already in the right order are left
  alone, and finished comics stay finished. The log shows
  `page order: remapped N comics` when it's done. If your library folder
  isn't mounted or a file can't be read, it logs that and tries again on the
  next start.
- **CBR files that were already unpacked are unpacked again** the next time
  they're opened, so their pages use the new order. The first open of each
  CBR after the upgrade is a little slower.
- **New tables for KOReader sync** are created the first time KOReader
  connects. Nothing to do if you don't use it.

### Comics downloaded to the app

Only comics whose page files were named out of order are affected (in one
4,700-issue library, 17 were). If you downloaded one of those to your iPad or
iPhone before upgrading, its pages stay in the old order on the device while
your saved page moves to the new order. Delete the download and download it
again.

### Going back to v0.1.0

Set the image to `ghcr.io/rthaines211/gutter-server:0.1.0` and restore the
`/data` backup from step 1. Running v0.1.0 on a database that v0.2.0 has
already opened puts saved pages back on the old order for the affected
comics, so restore the backup rather than just switching the image.

## v0.1.0

First public release: Docker image on GHCR, password auth, compose file,
first-run library scan.
