package writeback

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rthaines211/gutter-server/store"
)

// seedTwinIssue seeds an issue whose file lives in the downloader's twin
// folder while its series' primary folder is the organized spelling.
func seedTwinIssue(t *testing.T, st *store.Store, libRoot, filename string) (string, string) {
	t.Helper()
	twinDir := filepath.Join(libRoot, "Rook Exodus (2024)")
	if err := os.MkdirAll(twinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(libRoot, "Rook: Exodus (2024)"), 0o755)
	path := filepath.Join(twinDir, filename)
	writeTestCbz(t, path)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	id := seedIssue(t, st, libRoot, "Rook Exodus (2024)", filename, store.IssueMetadataPatch{Number: sp("11")})
	if _, err := st.DB().Exec(`UPDATE series SET folder_path=?`, filepath.Join(libRoot, "Rook: Exodus (2024)")); err != nil {
		t.Fatal(err)
	}
	return id, path
}

// Writeback rewrites a twin-folder file in place — its own path, not the
// primary folder's — and keeps the downloader's file mode.
func TestProcessTwinFolderIssueInPlace(t *testing.T) {
	st, libRoot := setupTestStore(t)
	id, path := seedTwinIssue(t, st, libRoot, "Rook Exodus 11 (2026).cbz")

	w := New(st, []string{libRoot}, t.TempDir(), time.Millisecond, nil)
	if !w.ProcessOne() {
		t.Fatal("expected a job to be processed")
	}
	var status string
	st.DB().QueryRow(`SELECT status FROM metadata_writeback WHERE comic_id=?`, id).Scan(&status)
	if status != "done" {
		t.Fatalf("status = %q", status)
	}
	if ci := readComicInfoFromCbz(t, path); ci.Number != "11" {
		t.Fatalf("Number = %q", ci.Number)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v, want 0644", fi.Mode().Perm())
	}
}

// A CBR in a twin folder is not converted: that renames the downloader's
// file, which it would then re-download.
func TestProcessTwinFolderCbrIsNotConverted(t *testing.T) {
	st, libRoot := setupTestStore(t)
	id, path := seedTwinIssue(t, st, libRoot, "Rook Exodus 11 (2026).cbr")

	w := New(st, []string{libRoot}, t.TempDir(), time.Millisecond, nil)
	if !w.ProcessOne() {
		t.Fatal("expected a job to be processed")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("cbr gone: %v", err)
	}
	if !st.ComicExists(id) {
		t.Fatal("issue id changed")
	}
}
