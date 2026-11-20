# Natural page order — design

## Intent

Pages inside a CBZ/CBR are served in raw byte order of their entry names, so
`page10.jpg` comes before `page2.jpg` and `Xombi_01.jpg` before
`xombi_00_fc.jpg`. Readers see pages out of order. A scan of the owner's
library found 17 of 4,745 CBZs affected (worst: Deadly Class Compendium and
Uncanny X-Men Omnibus Vol. 03, where pages 1000+ follow page 100). Serve pages
in natural, case-insensitive order, as the Gutter app's own CBZ import already
does, without losing anyone's place.

## Goals

- `PageNames()`/`PageAt()` use natural, case-insensitive order in all readers
  (`cbz.go`, `cbr.go`, `cbr_extracted.go` extract + load).
- Stored page numbers (`reading_progress.current_page`, `bookmarks.page_num`,
  `chapters.page_num`, `page_flags.page`) are moved to the new position of the
  same page, once, on every server, automatically.
- CBRs already unpacked under the old order are unpacked again.

## Non-goals

- App changes. iPad downloads keep the old order inside them; the owner
  deletes and re-downloads the affected comics by hand.
- Bubble data: keyed by page name/SHA, unaffected.

## Anti-goals

- A finished comic must not turn "in progress": progress on the old last page
  (or beyond) stays on the new last page.
- No pass over every archive in the library; no slowdown of page serving.

## Approaches

1. **One-time background pass at start-up — picked.** Fixes every install with
   no action; runs once (DB flag); touches only comics that have stored page
   numbers.
2. On first open — same result, but a check in the page-serving path forever
   and a per-comic marker.
3. One-off command — only servers whose owner runs it get fixed.

## Decided

- Sort key: the existing `naturalLess` (`server/scanner.go`), moved to
  `archive` and shared, so issue and page order use one rule. Ties (names equal
  ignoring case) fall back to byte order so the order is total and stable.
- Remap: background goroutine after start-up. For each distinct comic id with
  rows in the four tables: open the archive, old order = byte sort of
  `PageNames()`, new order = natural sort; if they differ, map each stored
  number old→new in one transaction and set `reading_progress.updated_at` so
  `/sync` delivers it to the app. Numbers outside `0..count-1` and progress at
  the old last page are left as "last page". A row in a small state table
  marks the pass done; it runs again if it did not finish (crash/restart).
  Missing or unreadable archives are skipped and logged.
- CBR cache: bump the `.extracted` marker contents/version so old extractions
  are redone.

## Left to the builder

- Exact name of the state table/flag and where the goroutine starts (next to
  other start-up work in `main.go`).
- Test shape: a unit test that fails on byte order (`page2` vs `page10`,
  case), and a store test for the remap including the last-page rule.

<!-- flow-decisions:start -->
## Decisions

| Who | Stage | Decision | Why | Confirmed |
|---|---|---|---|---|
| You | Brainstorm | Natural, case-insensitive page sort; remap stored page numbers via a one-time background startup pass; auto-invalidate CBR cache; iPad downloads re-downloaded by hand | Server is public, so every install must self-fix; only a few comics locally carry page data | ✓ |
<!-- flow-decisions:end -->
