package store

import (
	"path/filepath"
	"strconv"
)

func (s *Store) ListRenameTemplates() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT format, template FROM rename_templates`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var f, tpl string
		if err := rows.Scan(&f, &tpl); err != nil {
			return nil, err
		}
		out[f] = tpl
	}
	return out, rows.Err()
}

func (s *Store) SaveRenameTemplates(m map[string]string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for f, tpl := range m {
		if _, err := tx.Exec(`INSERT INTO rename_templates(format, template) VALUES(?, ?)
			ON CONFLICT(format) DO UPDATE SET template = excluded.template`, f, tpl); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// TemplateRenameIssue holds the per-issue data needed to resolve a rename template.
type TemplateRenameIssue struct {
	ID         string
	Filename   string
	Number     string
	Format     string
	Title      string
	SeriesName string
	FolderName string
	Year       int
	// CoverYear is the year of the issue's enrichment cover date, 0 if none.
	CoverYear int
}

// IssuesForTemplateRename returns the issues of the given series that live in
// its primary folder, with the metadata fields needed to resolve a rename
// template. Issues in a twin folder (InTwinFolder) are left out: every caller
// renames or moves files, and the downloader that owns a twin folder tracks
// its files by path and re-downloads any that go missing.
func (s *Store) IssuesForTemplateRename(seriesID string) ([]TemplateRenameIssue, error) {
	rows, err := s.db.Query(`
		SELECT
			i.id,
			i.filename,
			COALESCE(i.number, ''),
			COALESCE(i.format, ''),
			se.name,
			se.folder_path,
			COALESCE(se.year, 0),
			COALESCE(ie.title, ''),
			COALESCE(substr(ie.cover_date, 1, 4), '')
		FROM issues i
		JOIN series se ON se.id = i.series_id
		LEFT JOIN issue_enrichment ie ON ie.issue_id = i.id
		WHERE i.series_id = ?
		ORDER BY CAST(i.number AS INTEGER), i.filename
	`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TemplateRenameIssue
	for rows.Next() {
		var iss TemplateRenameIssue
		var folderPath, coverYear string
		if err := rows.Scan(
			&iss.ID, &iss.Filename, &iss.Number, &iss.Format,
			&iss.SeriesName, &folderPath, &iss.Year, &iss.Title, &coverYear,
		); err != nil {
			return nil, err
		}
		// FolderName is the basename of folder_path, matching the writeback worker's
		// filepath.Base() call so it is always a single directory component regardless
		// of how folder_path is stored in the DB.
		iss.FolderName = filepath.Base(folderPath)
		if InTwinFolder(folderPath, iss.ID, iss.Filename) {
			continue
		}
		if n, err := strconv.Atoi(coverYear); err == nil {
			iss.CoverYear = n
		}
		out = append(out, iss)
	}
	return out, rows.Err()
}

// HasTwinFolder reports whether any issue of the series lives in a twin
// folder. Renaming such a series' folder could change its id out from under
// the twin's issues, so callers refuse.
func (s *Store) HasTwinFolder(seriesID string) (bool, error) {
	series, err := s.GetSeries(seriesID)
	if err != nil {
		return false, err
	}
	issues, err := s.ListIssues(seriesID)
	if err != nil {
		return false, err
	}
	for _, iss := range issues {
		if InTwinFolder(series.FolderPath, iss.ID, iss.Filename) {
			return true, nil
		}
	}
	return false, nil
}
