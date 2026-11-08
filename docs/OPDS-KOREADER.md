# OPDS and KOReader sync

Gutter serves an OPDS 1.2 catalog for e-ink and third-party reader apps, and
a KOReader sync ("kosync") server so KOReader and the Gutter app share
reading progress.

Both use the same password as everything else: `GUTTER_PASSWORD` (any
username), or the API key.

## OPDS catalog

Catalog URL: `https://<your-gutter-host>/opds/`

| Path | Feed |
|---|---|
| `/opds/` | Navigation root |
| `/opds/reading` | Issues you've started (acquisition) |
| `/opds/recent` | 50 newest series by add date |
| `/opds/series?page=N` | All series, A to Z, 100 per page |
| `/opds/series/{id}` | One series' issues (acquisition) |
| `/opds/search?q=` | Series whose name, writer or publisher contain every word |
| `/opds/opensearch.xml` | OpenSearch description for search |
| `/opds/download/{id}` | The original CBZ/CBR, byte for byte |

Each issue entry has:

- an acquisition link to the raw archive (`application/vnd.comicbook+zip` or
  `application/vnd.comicbook-rar`);
- cover and thumbnail links (`/comics/{id}/cover`);
- an OPDS-PSE stream link, `/comics/{id}/pages/{pageNumber}`, with
  `pse:count`, plus `pse:lastRead` (0-based) and `pse:lastReadDate` when you
  have progress on it.

### Setting up clients

- **KOReader**: Search → OPDS catalog → `+`. URL
  `https://<host>/opds/`, any username, your Gutter password. Tap an issue to
  download it, or long-press → "Stream" to read pages without downloading.
- **Panels / Chunky** (iPad): add an OPDS server with the same URL and
  credentials. Both use the PSE stream link for page streaming.

## KOReader sync

In KOReader: Tools → Progress sync → Custom sync server →
`https://<host>/kosync`. Then **Login** with any username and your Gutter
password (or API key). **Register** also works with the right password; a
wrong one is refused, since there's only one user.

| Endpoint | |
|---|---|
| `POST /kosync/users/create` | Register; succeeds with the right password (no auth headers needed) |
| `GET /kosync/users/auth` | Login check |
| `PUT /kosync/syncs/progress` | Push progress |
| `GET /kosync/syncs/progress/{document}` | Pull progress |
| `GET /kosync/healthcheck` | Liveness, no auth |

KOReader sends `x-auth-user` and `x-auth-key` headers, where the key is the
MD5 of what you typed as password. Gutter accepts the MD5 of the Gutter
password or of the API key, plus HTTP Basic or Bearer for other clients.

### How documents map to comics

KOReader names a book by a digest it computes on the device:

- **Binary** (the default "document matching method"): an MD5 over 1 KiB
  samples at offsets 0, 1 KiB, 4 KiB, 16 KiB … 1 GiB. OPDS downloads are the
  original bytes, so a comic downloaded from Gutter has the same digest as
  the file in the library.
- **Filename**: the MD5 of the file name. This only matches if the file on
  the device keeps its library file name.

Gutter stores both digests per comic in `kosync_documents`. A digest is
recorded when the comic is downloaded through `/opds/download/{id}`. When
KOReader asks about a digest Gutter doesn't know, Gutter digests every new or
changed library file (at most once every 5 minutes) and tries again. Each
file costs about 12 KiB of reads.

Progress from a book that isn't in the library is still stored and returned,
so several KOReader devices can sync it with each other.

### How pages map to progress (CBZ)

KOReader opens a CBZ as a paged document. Its `progress` is the 1-based page
number as a string, and `percentage` is page ÷ total. Gutter's
`reading_progress.current_page` is a 0-based page index, and
`current_page == total_pages` means finished.

| KOReader | Gutter |
|---|---|
| page `p` of `T` (p < T) | `current_page = p − 1` |
| page `T` (last page) | `current_page = T` (finished) |
| Gutter `current_page = c` | page `min(c + 1, T)`, percentage page ÷ T |

`total_pages` is Gutter's own count of image entries in the archive.

A KOReader push is written to `reading_progress`, so the Gutter app sees it
on its next sync. When KOReader pulls, Gutter returns KOReader's last push.
If the Gutter app has saved progress since then, on a different page, Gutter
returns that instead, with device `Gutter` and device_id `gutter-server`.

### Known limits

- **Page order**: the mapping assumes KOReader (MuPDF) and Gutter count and
  sort the same images. Archives with stray non-image files, or page names
  that sort differently, can be off by a page or more.
- **CBR**: KOReader may not open CBR files at all. In the OPDS feed, a CBR
  gets a PSE stream link only once its page count is known (after someone
  has opened it). Gutter doesn't extract a whole CBR just to build a feed.
  CBRs can always be downloaded.
- **Re-packed files**: if a file changes on disk (for example after
  ComicInfo writeback), its binary digest changes. A copy KOReader
  downloaded earlier then no longer matches. Download it again or use the
  filename method.
- **Reflowable formats**: EPUB-style progress (xpointers) has no page
  number. For those Gutter uses percentage × total pages. This only matters
  if a non-comic ends up matched to a comic.
- **Timestamps** have one-second resolution. On a tie, KOReader's push wins.
- **Sub-path proxies**: feed links are absolute paths (`/opds/...`, `/comics/...`).
  Serving Gutter under a URL prefix such as `https://host/gutter/` is not
  supported.
- **One user**: every username shares the same library and progress.
