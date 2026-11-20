# Natural Page Order Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Serve archive pages in natural, case-insensitive order and move stored page numbers once so nobody loses their place.

**Architecture:** One shared `archive.NaturalLess` drives every reader's page sort. A one-time background pass at start-up computes, for each comic that has stored page numbers, the old (byte) → new (natural) index map from the raw entry names and rewrites the four page-number tables in one transaction per comic. The CBR extract marker gets a version so old extractions redo.

**Tech Stack:** Go 1.26, SQLite (`modernc`/`mattn` as the repo uses), `github.com/gen2brain/go-unarr`.

**Spec:** `docs/superpowers/specs/2026-11-20-natural-page-order-design.md`

## Global Constraints

- Sort: natural, case-insensitive; names equal ignoring case fall back to byte order (total, stable).
- Tables remapped: `reading_progress.current_page`, `bookmarks.page_num`, `chapters.page_num`, `page_flags.page`.
- Progress at or beyond the old last page (`>= count-1`) is left unchanged; any other value outside `0..count-1` is left unchanged.
- Remapped progress gets `updated_at = CURRENT_TIMESTAMP` so `/sync` sends it.
- The pass runs once per DB (flag), again only if it did not finish; it never opens archives without stored page numbers.
- Do not `gofmt` unrelated code; CI runs `go test ./...`.

## Review Focus

1. **App writes progress during the pass** (server already serves new order): a progress row with `updated_at` after the pass started must not be remapped again. Test in Task 3.
2. **Permutation hits UNIQUE(comic_id, page_num)** on bookmarks/chapters/page_flags mid-update: remap must not fail or drop rows. Test in Task 3 (chapters on pages 0..n swapped).
3. **Comic id whose file is gone or unreadable**: skip, log, keep going, still mark done. Test in Task 4.
4. **Duplicate entry names in one zip**: map by entry index, not by name. Test in Task 2.
5. **Old CBR cache with marker "ok"**: re-extract into a clean dir (no leftover `page_` files). Test in Task 1.

---

### Task 1: Natural sort in every reader + CBR cache version

**Files:**
- Create: `archive/natural.go` (move `naturalLess`, `isDigit`, `trimLeadingZeros` from `server/scanner.go`)
- Modify: `server/scanner.go` (delete the moved funcs; call `archive.NaturalLess`), `archive/cbz.go:50`, `archive/cbr.go:58`, `archive/cbr_extracted.go:58-66,154,163,204`
- Test: `archive/natural_test.go`, `archive/pagenames_test.go`, `archive/cbr_extracted_test.go`

**Interfaces:**
- Produces: `func NaturalLess(a, b string) bool` in package `archive` (case-insensitive, digit runs numeric, ties → `a < b`); `func PageLess(a, b string) bool` = `NaturalLess(a,b) || (!NaturalLess(b,a) && a < b)` used by all page sorts.

- [ ] **Step 1: Write failing tests**
  - `TestPageLessOrder`, pairwise: `PageLess("page2.jpg","page10.jpg")`, `PageLess("xombi_00_fc.jpg","Xombi_01.jpg")`, `PageLess("p6.jpg","p07.jpg")`, `!PageLess("a.jpg","a.jpg")`, and exactly one of `PageLess("A.jpg","a.jpg")`/`PageLess("a.jpg","A.jpg")` is true.
  - `TestCBZNaturalPageOrder`: build a zip in `t.TempDir()` with entries `page10.jpg, page2.jpg, page1.jpg` (any bytes); `OpenCBZ(...).PageNames()` == `[page1.jpg page2.jpg page10.jpg]`.
  - Update `TestCBZPageNamesMatchOrder` to check `!PageLess(names[i], names[i-1])` instead of byte order.
  - `TestExtractedCBRStaleMarkerReextracts` (in `cbr_extracted_test.go`, reuse its existing fixture setup): pre-create the cache dir for the fixture with marker contents `ok` and a stray `page_9999.jpg`; open; stray file is gone and marker content is the new version.
- [ ] **Step 2: Run** `go test ./archive/ -run 'PageLess|NaturalPageOrder|PageNamesMatchOrder|StaleMarker' -v` → FAIL (undefined `PageLess`, wrong order, stale file present).
- [ ] **Step 3: Implement.** Move funcs to `archive/natural.go` (export `NaturalLess`, add `PageLess`); replace the four `name < name` sorts with `PageLess`. In `cbr_extracted.go` add `const extractMarkerVersion = "v2-natural"`; write it as the marker content; in `openExtractedCBR` treat the marker as valid only if its content equals the version, else `os.RemoveAll(extractDir)` before extracting.
- [ ] **Step 4: Run** `go test ./archive/ ./server/ -v -run 'PageLess|NaturalPageOrder|PageNamesMatchOrder|StaleMarker|Natural'` then `go test ./...` → PASS.
- [ ] **Step 5: Commit** `fix(archive): serve pages in natural order`.

### Task 2: Old→new page index map

**Files:**
- Create: `archive/pageorder.go`
- Test: `archive/pageorder_test.go`

**Interfaces:**
- Consumes: `PageLess` (Task 1).
- Produces: `func ImageEntryNames(path string) ([]string, error)` — raw image entry names in archive order (CBZ via `zip.OpenReader`, CBR via `unarr.List()`), same image filter as the readers (`imageExts` on lowercased base name). `func PageOrderMap(names []string) []int` — `m[old] = new`, where old = stable sort of entry indices by byte name, new = stable sort by `PageLess`; returns nil when identical.

- [ ] **Step 1: Write failing tests**
  - `TestPageOrderMapIdentityNil`: `PageOrderMap([]string{"001.jpg","002.jpg"}) == nil`.
  - `TestPageOrderMapUnpadded`: names `["page1.jpg","page10.jpg","page2.jpg"]` (byte order already) → `[0,2,1]`.
  - `TestPageOrderMapDuplicates`: names `["b/1.jpg","a/10.jpg","a/2.jpg","a/2.jpg"]` → result is a permutation of `0..3` (no index repeated).
  - `TestImageEntryNamesCBZ`: zip with `page2.jpg, ComicInfo.xml, page10.jpg` → `[page2.jpg page10.jpg]`.
- [ ] **Step 2: Run** `go test ./archive/ -run 'PageOrderMap|ImageEntryNames' -v` → FAIL (undefined).
- [ ] **Step 3: Implement** both funcs in `archive/pageorder.go`; unsupported extension → error.
- [ ] **Step 4: Run** same command → PASS.
- [ ] **Step 5: Commit** `feat(archive): map old byte page order to natural order`.

### Task 3: Store remap + migration flag

**Files:**
- Create: `store/page_order.go`
- Modify: `store/db.go` schema (add `CREATE TABLE IF NOT EXISTS migrations (name TEXT PRIMARY KEY, done_at INTEGER NOT NULL)`)
- Test: `store/page_order_test.go`

**Interfaces:**
- Produces:
  - `func (s *Store) PageDataComicIDs() ([]string, error)` — distinct `comic_id` across the four tables.
  - `func (s *Store) RemapPages(comicID string, m []int, before time.Time) error` — one transaction; for each table, in-range values `v` (`0 <= v < len(m)`) become `m[v]` via a two-phase update (set `-1 - m[v]`, then `col = -1 - col` where `col < 0`) so UNIQUE never trips; progress rows with `current_page >= len(m)-1` or `updated_at >= before` are skipped; remapped progress gets `updated_at = CURRENT_TIMESTAMP`.
  - `func (s *Store) MigrationDone(name string) (bool, error)`, `func (s *Store) MarkMigrationDone(name string) error`.

- [ ] **Step 1: Write failing tests** (use `newTestStore`)
  - `TestRemapPagesProgress`: progress `{c1, 1, 3}` with `m=[0,2,1]` → `current_page == 2`.
  - `TestRemapPagesLastPageKept`: progress `{c1, 2, 3}` with `m=[2,1,0]` → stays `2` (finished stays finished).
  - `TestRemapPagesSkipsNewerProgress`: upsert progress, call with `before = time.Now().Add(-time.Hour)` → unchanged.
  - `TestRemapPagesUniqueSwap`: chapters on pages 0,1,2 with titles A,B,C and `m=[1,2,0]` → page 1=A, 2=B, 0=C, 3 rows; same shape for a bookmark pair and page_flags.
  - `TestRemapPagesOutOfRangeKept`: chapter on page 7 with `len(m)=3` → still 7.
  - `TestMigrationFlag`: `MigrationDone("x")` false → `MarkMigrationDone("x")` → true.
- [ ] **Step 2: Run** `go test ./store/ -run 'RemapPages|MigrationFlag' -v` → FAIL (undefined).
- [ ] **Step 3: Implement** in `store/page_order.go`; per-row map lookups (read `rowid, col` first, then update by rowid). `reading_progress.updated_at` is DATETIME text: compare with `before.UTC().Format("2006-01-02 15:04:05")`.
- [ ] **Step 4: Run** same command, then `go test ./store/` → PASS.
- [ ] **Step 5: Commit** `feat(store): remap stored page numbers`.

### Task 4: Start-up pass

**Files:**
- Create: `server/page_order_migration.go`
- Modify: `main.go` (start it after the store and `ComicDirs` are set, before `ListenAndServe`, as `go srv.MigratePageOrder()`)
- Test: `server/page_order_migration_test.go`

**Interfaces:**
- Consumes: `Store.PageDataComicIDs`, `Store.RemapPages`, `Store.MigrationDone/MarkMigrationDone` (Task 3); `archive.ImageEntryNames`, `archive.PageOrderMap` (Task 2); `(*Server).resolveComicPath`.
- Produces: `func (s *Server) MigratePageOrder()` with migration name `"natural_page_order"`; logs `page order: remapped N comics (M skipped)`.

- [ ] **Step 1: Write failing test** `TestMigratePageOrder` (`newTestServer(t)` in `server/reading_test.go`, then set `srv.ComicDirs` to a temp dir): write `S/a.cbz` with `page1, page10, page2` and `S/b.cbz` with `001, 002`; progress on a at page 1, progress on b at page 1, progress on a missing id `S/gone.cbz`; run `MigratePageOrder()`; assert a → 2, b → 1, flag set; run again after setting a back to 1 → stays 1 (ran once).
- [ ] **Step 2: Run** `go test ./server/ -run TestMigratePageOrder -v` → FAIL (undefined).
- [ ] **Step 3: Implement**: return if done; `start := time.Now()`; for each id: resolve path (error → skip, count), `ImageEntryNames` (error → skip, log), `PageOrderMap` nil → continue, else `RemapPages(id, m, start)`; a store error stops without marking done (next start retries); otherwise mark done and log.
- [ ] **Step 4: Run** `go test ./...` and `go vet ./...` → PASS.
- [ ] **Step 5: Commit** `feat(server): one-time remap of page numbers to natural order`.

### Task 5: Proof on real data

- [ ] **Step 1:** Build: `go build ./...`.
- [ ] **Step 2:** Run the server binary against a **copy** of the live DB (`/tmp/pageorder/db`) with `comic_dirs` pointing at the real library read-only, on a spare port, with a temp config; confirm the log line and that Deadly Class (146), Grayson (32) and Flash: Rebirth #1 (1) progress and the Black Casebook chapters moved to the page with the same entry name (check with `archive.ImageEntryNames` old vs new order).
- [ ] **Step 3:** `GET /comics/{TMNT #10 id}/pages/1` and `/pages/2` — page names in order (`page-001`, `page-002` …), via the page served bytes' hash vs the zip entries.
- [ ] **Step 4:** No commit (proof only); record results for the PR.
