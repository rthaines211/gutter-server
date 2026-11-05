package store

import (
	"encoding/json"
	"strings"
	"time"
)

// SeriesMetadataPatch holds optional edits to a series. Nil fields are not updated.
type SeriesMetadataPatch struct {
	Name, Publisher, Writer, Penciller, Description *string
	Year                                            *int
	Genres                                          *[]string
}

// IssueMetadataPatch holds optional edits to an issue. Nil fields are not updated.
type IssueMetadataPatch struct {
	Title, Summary, Number, Format, Writer, Penciller *string
}

// UpdateSeriesMetadata applies a partial patch to a series row and keeps the
// series_enrichment overlay consistent so that read-time merges reflect edits.
func (s *Store) UpdateSeriesMetadata(seriesID string, p SeriesMetadataPatch) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Build dynamic UPDATE for the series row.
	set := []string{"updated_at = ?"}
	args := []any{time.Now().UnixMilli()}
	add := func(col string, v any) {
		set = append(set, col+" = ?")
		args = append(args, v)
	}
	if p.Name != nil {
		add("name", *p.Name)
	}
	if p.Publisher != nil {
		add("publisher", *p.Publisher)
	}
	if p.Writer != nil {
		add("writer", *p.Writer)
	}
	if p.Penciller != nil {
		add("penciller", *p.Penciller)
	}
	if p.Year != nil {
		add("year", *p.Year)
	}
	args = append(args, seriesID)
	if _, err := tx.Exec(`UPDATE series SET `+strings.Join(set, ", ")+` WHERE id = ?`, args...); err != nil {
		return err
	}

	// Replace genres if provided.
	if p.Genres != nil {
		if _, err := tx.Exec(`DELETE FROM series_genres WHERE series_id = ?`, seriesID); err != nil {
			return err
		}
		for _, g := range *p.Genres {
			if _, err := tx.Exec(`INSERT INTO series_genres(series_id, genre) VALUES(?, ?)`, seriesID, g); err != nil {
				return err
			}
		}
	}

	// Keep the read-time enrichment overlay in sync with any overlapping fields
	// (description, publisher, start_year, creators). If no enrichment row exists,
	// create a minimal 'manual' one with the required NOT NULL columns.
	needsEnrichmentUpdate := p.Description != nil || p.Publisher != nil || p.Year != nil || p.Writer != nil || p.Penciller != nil
	if needsEnrichmentUpdate {
		now := time.Now().UnixMilli()
		if _, err := tx.Exec(`
			INSERT INTO series_enrichment(series_id, comicvine_volume_id, status, enriched_at)
			SELECT ?, 0, 'manual', ?
			WHERE NOT EXISTS (SELECT 1 FROM series_enrichment WHERE series_id = ?)`,
			seriesID, now, seriesID); err != nil {
			return err
		}
		eset := []string{}
		eargs := []any{}
		if p.Description != nil {
			eset = append(eset, "description = ?")
			eargs = append(eargs, *p.Description)
		}
		if p.Publisher != nil {
			eset = append(eset, "publisher = ?")
			eargs = append(eargs, *p.Publisher)
		}
		if p.Year != nil {
			eset = append(eset, "start_year = ?")
			eargs = append(eargs, *p.Year)
		}

		// When writer or penciller is patched, rewrite the creators JSON array so the
		// read-time enrichment merge (server/sync.go) picks up the manual values.
		// Roles "writer" and "penciller/inker/artist" are identified by substring per
		// server/enrichment_merge.go. We replace existing entries for the affected role
		// and keep other roles untouched.
		if p.Writer != nil || p.Penciller != nil {
			// Load existing creators JSON.
			var existingJSON string
			_ = tx.QueryRow(`SELECT COALESCE(creators,'[]') FROM series_enrichment WHERE series_id = ?`, seriesID).Scan(&existingJSON)
			var creators []Creator
			if err := json.Unmarshal([]byte(existingJSON), &creators); err != nil {
				creators = nil
			}

			// Filter out roles being replaced.
			filtered := creators[:0]
			for _, c := range creators {
				role := strings.ToLower(c.Role)
				if p.Writer != nil && strings.Contains(role, "writer") {
					continue
				}
				if p.Penciller != nil && (strings.Contains(role, "pencil") || strings.Contains(role, "artist")) {
					continue
				}
				filtered = append(filtered, c)
			}
			// Append new entries for non-empty values.
			if p.Writer != nil && *p.Writer != "" {
				filtered = append(filtered, Creator{Name: *p.Writer, Role: "writer"})
			}
			if p.Penciller != nil && *p.Penciller != "" {
				filtered = append(filtered, Creator{Name: *p.Penciller, Role: "penciller"})
			}
			creatorsJSON, err := json.Marshal(filtered)
			if err != nil {
				return err
			}
			eset = append(eset, "creators = ?")
			eargs = append(eargs, string(creatorsJSON))
		}

		if len(eset) > 0 {
			eargs = append(eargs, seriesID)
			if _, err := tx.Exec(`UPDATE series_enrichment SET `+strings.Join(eset, ", ")+` WHERE series_id = ?`, eargs...); err != nil {
				return err
			}
		}
		var manual []string
		if p.Description != nil {
			manual = append(manual, "description")
		}
		if p.Publisher != nil {
			manual = append(manual, "publisher")
		}
		if p.Year != nil {
			manual = append(manual, "start_year")
		}
		if p.Writer != nil || p.Penciller != nil {
			manual = append(manual, "creators")
		}
		if err := markManual(tx, "series_enrichment", "series_id", seriesID, manual...); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// SeriesIDForComic returns the series_id for the given comic (issue) ID.
// Returns sql.ErrNoRows (wrapped) if the comic does not exist.
func (s *Store) SeriesIDForComic(comicID string) (string, error) {
	var id string
	err := s.db.QueryRow(`SELECT series_id FROM issues WHERE id = ?`, comicID).Scan(&id)
	return id, err
}

// IssueIDsForSeries returns the IDs of all issues belonging to a series,
// ordered by position. Returns an empty slice (no error) if the series exists
// but has no issues; callers distinguish "not found" by checking len == 0.
func (s *Store) IssueIDsForSeries(seriesID string) ([]string, error) {
	rows, err := s.db.Query(`SELECT id FROM issues WHERE series_id = ? ORDER BY position`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// UpdateIssueMetadata applies a partial patch to an issue row and keeps the
// issue_enrichment overlay consistent for title/summary.
func (s *Store) UpdateIssueMetadata(comicID string, p IssueMetadataPatch) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Build dynamic UPDATE for the issues row (number, format, writer, penciller).
	set := []string{}
	args := []any{}
	add := func(col string, v any) {
		set = append(set, col+" = ?")
		args = append(args, v)
	}
	if p.Number != nil {
		add("number", *p.Number)
	}
	if p.Format != nil {
		add("format", strings.ToLower(*p.Format))
	}
	if p.Writer != nil {
		add("writer", *p.Writer)
	}
	if p.Penciller != nil {
		add("penciller", *p.Penciller)
	}
	if len(set) > 0 {
		args = append(args, comicID)
		if _, err := tx.Exec(`UPDATE issues SET `+strings.Join(set, ", ")+` WHERE id = ?`, args...); err != nil {
			return err
		}
	}

	// Keep issue_enrichment overlay in sync for title/summary.
	// If no enrichment row exists, create a minimal one with required NOT NULL columns.
	if p.Title != nil || p.Summary != nil {
		now := time.Now().UnixMilli()
		if _, err := tx.Exec(`
			INSERT INTO issue_enrichment(issue_id, comicvine_issue_id, enriched_at)
			SELECT ?, 0, ?
			WHERE NOT EXISTS (SELECT 1 FROM issue_enrichment WHERE issue_id = ?)`,
			comicID, now, comicID); err != nil {
			return err
		}
		eset := []string{}
		eargs := []any{}
		if p.Title != nil {
			eset = append(eset, "title = ?")
			eargs = append(eargs, *p.Title)
		}
		if p.Summary != nil {
			eset = append(eset, "summary = ?")
			eargs = append(eargs, *p.Summary)
		}
		eargs = append(eargs, comicID)
		if _, err := tx.Exec(`UPDATE issue_enrichment SET `+strings.Join(eset, ", ")+` WHERE issue_id = ?`, eargs...); err != nil {
			return err
		}
		var manual []string
		if p.Title != nil {
			manual = append(manual, "title")
		}
		if p.Summary != nil {
			manual = append(manual, "summary")
		}
		if err := markManual(tx, "issue_enrichment", "issue_id", comicID, manual...); err != nil {
			return err
		}
	}

	return tx.Commit()
}
