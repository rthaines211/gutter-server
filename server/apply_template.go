package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/ryanhaines/comic-reader/rename"
)

// applyTemplateResult is the per-issue result returned by HandleApplyTemplate.
type applyTemplateResult struct {
	IssueID  string `json:"issue_id"`
	OldName  string `json:"old_name"`
	NewName  string `json:"new_name"`
	Applied  bool   `json:"applied"`
	Conflict bool   `json:"conflict"`
}

// HandleApplyTemplate handles POST /series/{id}/apply-template.
// Query param dry_run=1 previews renames without touching disk or DB.
func (s *Server) HandleApplyTemplate(w http.ResponseWriter, r *http.Request) {
	seriesID := r.PathValue("id")
	dryRun := r.URL.Query().Get("dry_run") == "1"

	// Load template map.
	templates, err := s.Store.ListRenameTemplates()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to load templates")
		return
	}

	// Load issues with their metadata.
	issues, err := s.Store.IssuesForTemplateRename(seriesID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "store error")
		return
	}
	if len(issues) == 0 {
		writeJSONError(w, http.StatusNotFound, "series not found")
		return
	}

	// Resolve the series folder (absolute path from the first issue).
	// FolderName stored in TemplateRenameIssue is the basename; we need the full abs path.
	// We derive it from ComicDirs + FolderName of the first issue.
	seriesFolder := ""
	firstIssue := issues[0]
	for _, root := range s.ComicDirs {
		candidate := filepath.Join(root, firstIssue.FolderName)
		if _, err := os.Stat(candidate); err == nil {
			seriesFolder = candidate
			break
		}
	}
	if seriesFolder == "" {
		writeJSONError(w, http.StatusNotFound, "series folder not found under any configured library root")
		return
	}

	// --- Pass 1: resolve all new names and build results slice ---
	// planned counts how many issues map to each target name (intra-batch duplicates).
	planned := make(map[string]int)
	results := make([]applyTemplateResult, 0, len(issues))

	for _, iss := range issues {
		res := applyTemplateResult{
			IssueID: iss.ID,
			OldName: iss.Filename,
		}

		// Pick template: format-specific or default.
		tpl, ok := templates[iss.Format]
		if !ok || tpl == "" {
			tpl = templates["default"]
		}

		// Build fields map for Resolve.
		yearStr := ""
		if iss.Year != 0 {
			yearStr = fmt.Sprintf("%d", iss.Year)
		}
		fields := map[string]string{
			"series": iss.SeriesName,
			"number": iss.Number,
			"year":   yearStr,
			"title":  iss.Title,
			"format": iss.Format,
			// {volume} is the matched upstream volume's name, which this
			// path has no access to — it renames from local metadata only.
			// Empty collapses the placeholder and its lead (rename.Resolve)
			// rather than emitting the issue number, which is {number}.
			"volume": "",
		}

		// Resolve new base name (without extension).
		resolved := rename.Resolve(tpl, fields)
		sanitized, err := rename.SanitizeFilename(resolved)
		if err != nil || sanitized == "" {
			// Leave NewName empty; the second pass will see no planned entry.
			results = append(results, res)
			continue
		}

		ext := filepath.Ext(iss.Filename)
		newName := sanitized + ext
		res.NewName = newName

		results = append(results, res)

		// Only count non-trivial renames toward planned duplicates.
		if newName != iss.Filename {
			planned[newName]++
		}
	}

	// --- Pass 2: apply or flag conflicts ---
	anyApplied := false

	for i, iss := range issues {
		res := &results[i]

		// No new name means sanitize failed — skip silently.
		if res.NewName == "" {
			continue
		}

		// Skip if name is unchanged.
		if res.NewName == iss.Filename {
			continue
		}

		// Intra-batch duplicate: BOTH colliders are conflicts.
		if planned[res.NewName] > 1 {
			res.Conflict = true
			continue
		}

		// Disk conflict (existing file with that name that is not one we are renaming).
		newAbsPath := filepath.Join(seriesFolder, res.NewName)
		if _, err := os.Stat(newAbsPath); err == nil {
			res.Conflict = true
			continue
		}

		if dryRun {
			continue
		}

		// Apply: rename on disk.
		oldAbsPath := filepath.Join(seriesFolder, iss.Filename)
		if err := os.Rename(oldAbsPath, newAbsPath); err != nil {
			log.Printf("apply-template rename %s -> %s: %v", oldAbsPath, newAbsPath, err)
			res.Conflict = true
			continue
		}

		// Update DB: compute new ID and migrate.
		// New relative path = FolderName/newName (relative to library root).
		newRelPath := filepath.Join(firstIssue.FolderName, res.NewName)
		newID := pathToID(newRelPath)

		if err := s.Store.MigrateComicIDFull(iss.ID, newID, res.NewName); err != nil {
			log.Printf("apply-template MigrateComicIDFull %s -> %s: %v", iss.ID, newID, err)
			// Rollback disk rename.
			if rerr := os.Rename(newAbsPath, oldAbsPath); rerr != nil {
				log.Printf("apply-template rollback rename failed: %v", rerr)
			}
			res.Conflict = true
			continue
		}

		// Delete old thumbnail.
		if s.CoverDir != "" {
			thumbPath := filepath.Join(s.CoverDir, "comic_"+iss.ID+".jpg")
			os.Remove(thumbPath)
		}

		// If this issue was the series cover, update cover_path to the new filename
		// so the next sync/listing returns the correct path.
		if series, serr := s.Store.GetSeries(seriesID); serr == nil && series != nil {
			if filepath.Base(series.CoverPath) == iss.Filename {
				newCoverPath := filepath.Join(firstIssue.FolderName, res.NewName)
				if err := s.Store.UpdateSeriesCoverPath(seriesID, newCoverPath); err != nil {
					log.Printf("apply-template UpdateSeriesCoverPath %s: %v", seriesID, err)
				}
			}
		}

		res.Applied = true
		anyApplied = true
	}

	// TouchSeries on every real (non-dry) run so delta sync picks up the series
	// even if only metadata changed (e.g. all names were already correct).
	if !dryRun {
		if err := s.Store.TouchSeries(seriesID); err != nil {
			log.Printf("apply-template TouchSeries %s: %v", seriesID, err)
		}
	}

	_ = anyApplied // kept for potential future use

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"results": results})
}
