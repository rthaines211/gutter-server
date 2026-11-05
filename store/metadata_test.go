package store

import (
	"encoding/json"
	"strings"
	"testing"
)

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }

func TestUpdateSeriesMetadata(t *testing.T) {
	s := newTestStore(t)
	seedComic(t, s, "srs", "c1", "a.cbz")
	// Pre-existing enrichment row that would otherwise mask edits at read time.
	if _, err := s.db.Exec(`INSERT INTO series_enrichment(series_id, comicvine_volume_id, description, publisher, start_year, status, enriched_at)
		VALUES('srs', 0, 'old desc', 'Old House', 1990, 'enriched', 0)`); err != nil {
		t.Fatal(err)
	}

	genres := []string{"Action", "Sci-Fi"}
	err := s.UpdateSeriesMetadata("srs", SeriesMetadataPatch{
		Name:        strPtr("Edited Name"),
		Publisher:   strPtr("New House"),
		Year:        intPtr(2001),
		Description: strPtr("new desc"),
		Genres:      &genres,
	})
	if err != nil {
		t.Fatal(err)
	}

	var name, pub string
	var year int
	s.db.QueryRow(`SELECT name, publisher, year FROM series WHERE id='srs'`).Scan(&name, &pub, &year)
	if name != "Edited Name" || pub != "New House" || year != 2001 {
		t.Errorf("series row: %s %s %d", name, pub, year)
	}
	// Enrichment row updated too, so the read-time overlay shows edits.
	var desc, epub string
	var sy int
	s.db.QueryRow(`SELECT description, publisher, start_year FROM series_enrichment WHERE series_id='srs'`).Scan(&desc, &epub, &sy)
	if desc != "new desc" || epub != "New House" || sy != 2001 {
		t.Errorf("enrichment overlay: %s %s %d", desc, epub, sy)
	}
	if countWhere(t, s, "series_genres", "series_id", "srs") != 2 {
		t.Error("genres not replaced")
	}
	// Writer untouched (nil in patch).
	var writer string
	s.db.QueryRow(`SELECT COALESCE(writer,'') FROM series WHERE id='srs'`).Scan(&writer)
	if writer != "" {
		t.Errorf("writer should be untouched, got %q", writer)
	}
}

func TestUpdateSeriesMetadataCreatesEnrichmentRow(t *testing.T) {
	s := newTestStore(t)
	seedComic(t, s, "srs2", "c2", "a.cbz")
	if err := s.UpdateSeriesMetadata("srs2", SeriesMetadataPatch{Description: strPtr("d")}); err != nil {
		t.Fatal(err)
	}
	var status string
	s.db.QueryRow(`SELECT status FROM series_enrichment WHERE series_id='srs2'`).Scan(&status)
	if status != "manual" {
		t.Errorf("want manual enrichment row, got %q", status)
	}
}

func TestUpdateIssueMetadata(t *testing.T) {
	s := newTestStore(t)
	seedComic(t, s, "srs3", "c3", "a.cbz")
	err := s.UpdateIssueMetadata("c3", IssueMetadataPatch{
		Title: strPtr("New Title"), Number: strPtr("4"), Format: strPtr("tpb"), Writer: strPtr("W"),
	})
	if err != nil {
		t.Fatal(err)
	}
	var num, format, writer string
	s.db.QueryRow(`SELECT number, COALESCE(format,''), COALESCE(writer,'') FROM issues WHERE id='c3'`).Scan(&num, &format, &writer)
	if num != "4" || format != "tpb" || writer != "W" {
		t.Errorf("issues row: %s %s %s", num, format, writer)
	}
	var title string
	s.db.QueryRow(`SELECT title FROM issue_enrichment WHERE issue_id='c3'`).Scan(&title)
	if title != "New Title" {
		t.Errorf("enrichment title: %s", title)
	}
}

func TestUpsertSeriesPreservesEditedMetadata(t *testing.T) {
	s := newTestStore(t)
	seedComic(t, s, "srs4", "c4", "a.cbz")
	s.UpdateSeriesMetadata("srs4", SeriesMetadataPatch{Publisher: strPtr("Edited Pub")})

	// Simulate a rescan of the same series (scanner calls UpsertSeries).
	rescanUpsertSeries(t, s, "srs4", "Scanned Pub")

	var pub string
	s.db.QueryRow(`SELECT publisher FROM series WHERE id='srs4'`).Scan(&pub)
	if pub != "Edited Pub" {
		t.Errorf("rescan clobbered edited publisher: %q", pub)
	}
}

// TestUpdateSeriesMetadataCreatorsPatchedOnEnrichedSeries verifies I1:
// patching writer/penciller on a series that has an enrichment row updates the
// creators JSON array so the read-time merge picks up the manual values, while
// other creator roles (e.g. "inker") are left untouched.
func TestUpdateSeriesMetadataCreatorsPatchedOnEnrichedSeries(t *testing.T) {
	s := newTestStore(t)
	seedComic(t, s, "srs-i1", "c-i1", "a.cbz")

	// Seed an enrichment row with multiple creator roles.
	creatorsJSON := `[{"name":"Brian K. Vaughan","role":"writer"},{"name":"Fiona Staples","role":"penciller"},{"name":"Fonografiks","role":"letterer"}]`
	if _, err := s.db.Exec(`INSERT INTO series_enrichment(series_id, comicvine_volume_id, description, publisher, start_year, creators, status, enriched_at)
		VALUES('srs-i1', 42, 'desc', 'Image', 2012, ?, 'confirmed', 0)`, creatorsJSON); err != nil {
		t.Fatal(err)
	}

	// Patch only writer.
	err := s.UpdateSeriesMetadata("srs-i1", SeriesMetadataPatch{Writer: strPtr("New Writer")})
	if err != nil {
		t.Fatalf("UpdateSeriesMetadata: %v", err)
	}

	var gotJSON string
	s.db.QueryRow(`SELECT creators FROM series_enrichment WHERE series_id='srs-i1'`).Scan(&gotJSON)

	// New writer must appear; old writer must be replaced.
	if !containsCreator(gotJSON, "New Writer", "writer") {
		t.Errorf("expected new writer in creators JSON, got: %s", gotJSON)
	}
	if containsCreator(gotJSON, "Brian K. Vaughan", "writer") {
		t.Errorf("old writer should have been replaced, got: %s", gotJSON)
	}
	// Unrelated role (letterer) must be preserved.
	if !containsCreator(gotJSON, "Fonografiks", "letterer") {
		t.Errorf("letterer role should be preserved, got: %s", gotJSON)
	}
	// Penciller (not in this patch) must also be preserved.
	if !containsCreator(gotJSON, "Fiona Staples", "penciller") {
		t.Errorf("penciller should be preserved when not patched, got: %s", gotJSON)
	}

	// series row writer must also be updated.
	var writer string
	s.db.QueryRow(`SELECT COALESCE(writer,'') FROM series WHERE id='srs-i1'`).Scan(&writer)
	if writer != "New Writer" {
		t.Errorf("series.writer: want 'New Writer', got %q", writer)
	}
}

// containsCreator is a test helper that searches a creators JSON string for a
// matching Name+Role pair. Role is matched by substring (case-insensitive),
// matching the logic in server/enrichment_merge.go.
func containsCreator(creatorsJSON, name, role string) bool {
	var list []Creator
	if err := json.Unmarshal([]byte(creatorsJSON), &list); err != nil {
		return false
	}
	for _, c := range list {
		if c.Name == name && strings.Contains(strings.ToLower(c.Role), strings.ToLower(role)) {
			return true
		}
	}
	return false
}

func TestSeriesIDForComic(t *testing.T) {
	s := newTestStore(t)
	seedComic(t, s, "srs5", "c5", "a.cbz")
	got, err := s.SeriesIDForComic("c5")
	if err != nil {
		t.Fatal(err)
	}
	if got != "srs5" {
		t.Errorf("want srs5, got %q", got)
	}
	_, err = s.SeriesIDForComic("nope")
	if err == nil {
		t.Error("expected error for unknown comic")
	}
}

func TestIssueIDsForSeries(t *testing.T) {
	s := newTestStore(t)
	seedComic(t, s, "srs6", "c6a", "a.cbz")
	seedComic(t, s, "srs6", "c6b", "b.cbz")
	ids, err := s.IssueIDsForSeries("srs6")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("want 2 ids, got %d: %v", len(ids), ids)
	}
	empty, err := s.IssueIDsForSeries("unknown")
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Errorf("want empty for unknown series, got %v", empty)
	}
}

// rescanUpsertSeries mirrors how server/scanner.go:192 calls UpsertSeries,
// passing a new publisher value as if a rescan found different metadata.
func rescanUpsertSeries(t *testing.T, s *Store, seriesID, publisher string) {
	t.Helper()
	series := Series{
		ID:         seriesID,
		Name:       seriesID,
		FolderPath: "/lib/" + seriesID,
		IssueCount: 1,
		CoverPath:  "",
		Writer:     "",
		Penciller:  "",
		Publisher:  publisher,
		Year:       0,
	}
	if err := s.UpsertSeries(series); err != nil {
		t.Fatal(err)
	}
}
