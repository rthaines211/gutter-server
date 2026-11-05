package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync/atomic"
	"time"

	"github.com/ryanhaines/comic-reader/metadata"
	"github.com/ryanhaines/comic-reader/ocr"
)

var jobSeq atomic.Int64

type detectChaptersResponse struct {
	Candidates []metadata.Candidate `json:"candidates"`
	OCRPending bool                 `json:"ocrPending"`
	JobID      string               `json:"jobId,omitempty"`
	Note       string               `json:"note,omitempty"`
}

// mergeByPage returns all primary candidates plus any secondary candidates
// whose Page is not already occupied by a primary. Primary (structural /
// ComicInfo) always wins on collision.
func mergeByPage(primary, secondary []metadata.Candidate) []metadata.Candidate {
	taken := make(map[int]struct{}, len(primary))
	for _, c := range primary {
		taken[c.Page] = struct{}{}
	}
	out := append([]metadata.Candidate(nil), primary...)
	for _, c := range secondary {
		if _, exists := taken[c.Page]; !exists {
			out = append(out, c)
		}
	}
	return out
}

// detectCandidates builds the merged candidate list for a comic.
// It reads ComicInfo / structural signals, and if OCR text is already cached
// under "ocrtext:<id>", appends OCR-derived divider candidates (secondary).
// Callers MUST ensure the archive is available before calling if they care
// about errors; this helper never hard-fails — it returns nil on any error.
func (s *Server) detectCandidates(id string, exemplarPage *int) []metadata.Candidate {
	absPath, err := s.resolveComicPath(id)
	if err != nil {
		return nil
	}
	cached, err := getArchive(absPath)
	if err != nil {
		return nil
	}

	cached.mu.Lock()
	info, _ := metadata.ParseComicInfo(cached.reader.ComicInfoXML())
	names := cached.reader.PageNames()
	cached.mu.Unlock()

	var primary []metadata.Candidate
	if info != nil && len(info.Chapters) > 0 {
		for _, ch := range info.Chapters {
			primary = append(primary, metadata.Candidate{
				Page:       ch.Page,
				Title:      ch.Title,
				Source:     "comicinfo",
				Confidence: "high",
			})
		}
	} else {
		primary = append(primary, metadata.HarvestStructural(names)...)
	}

	// Merge OCR candidates if text is cached.
	if v, ok := s.Cache.Get("ocrtext:" + id); ok {
		if textMap, ok := v.(map[int]string); ok {
			ocrCands := metadata.DetectTextDividers(textMap, exemplarPage)
			primary = mergeByPage(primary, ocrCands)
		}
	}

	sort.Slice(primary, func(i, j int) bool {
		return primary[i].Page < primary[j].Page
	})
	return primary
}

// HandleDetectChapters returns candidate chapters from ComicInfo, structural
// signals, and (when cached) OCR text. On first call per comic the handler
// starts a background OCR job and returns ocrPending:true + jobId so the
// caller can poll /detect-chapters/status?job=<jobId>.
func (s *Server) HandleDetectChapters(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Parse optional body: { "exemplarPage": <int> }
	var body struct {
		ExemplarPage *int `json:"exemplarPage"`
	}
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&body) // ignore parse errors — field is optional
	}
	exemplarPage := body.ExemplarPage

	absPath, err := s.resolveComicPath(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}

	resp := detectChaptersResponse{Candidates: []metadata.Candidate{}}

	cached, err := getArchive(absPath)
	if err != nil {
		resp.Note = "archive unavailable"
		writeJSON(w, resp)
		return
	}

	// Build structural/ComicInfo candidates to return immediately.
	cached.mu.Lock()
	info, _ := metadata.ParseComicInfo(cached.reader.ComicInfoXML())
	pageNames := cached.reader.PageNames()
	pageCount := cached.reader.PageCount()
	cached.mu.Unlock()

	var structuralCands []metadata.Candidate
	if info != nil && len(info.Chapters) > 0 {
		for _, ch := range info.Chapters {
			structuralCands = append(structuralCands, metadata.Candidate{
				Page:       ch.Page,
				Title:      ch.Title,
				Source:     "comicinfo",
				Confidence: "high",
			})
		}
	} else {
		structuralCands = append(structuralCands, metadata.HarvestStructural(pageNames)...)
	}

	// If OCR text is already cached, return merged candidates immediately.
	if v, ok := s.Cache.Get("ocrtext:" + id); ok {
		if textMap, ok := v.(map[int]string); ok {
			ocrCands := metadata.DetectTextDividers(textMap, exemplarPage)
			merged := mergeByPage(structuralCands, ocrCands)
			sort.Slice(merged, func(i, j int) bool { return merged[i].Page < merged[j].Page })
			if merged == nil {
				merged = []metadata.Candidate{}
			}
			resp.Candidates = merged
			resp.OCRPending = false
			writeJSON(w, resp)
			return
		}
	}

	if structuralCands == nil {
		structuralCands = []metadata.Candidate{}
	}
	sort.Slice(structuralCands, func(i, j int) bool { return structuralCands[i].Page < structuralCands[j].Page })
	resp.Candidates = structuralCands

	// Reject if a detection scan is already running (regardless of ocrRunner).
	if s.jobs().isBusy() {
		writeJSONError(w, http.StatusConflict, "a detection scan is already running")
		return
	}

	// Only start an OCR job when a runner is configured.
	if s.ocrRunner != nil {
		jobID := fmt.Sprintf("%d-%d", time.Now().UnixNano(), jobSeq.Add(1))
		job, ok := s.jobs().start(jobID, id)
		if !ok {
			writeJSONError(w, http.StatusConflict, "a detection scan is already running")
			return
		}
		job.setTotal(pageCount)
		runner := s.ocrRunner
		capturedCands := structuralCands

		go func() {
			ctx := context.Background()

			// Counting pageAt wrapper drives determinate progress: OCRComic
			// calls this once per page.
			pageAtFunc := func(i int) (io.ReadCloser, error) {
				cached.mu.Lock()
				reader, _, err := cached.reader.PageAt(i)
				cached.mu.Unlock()
				if err == nil {
					job.incScanned()
				}
				return reader, err
			}

			textMap, ocrErr := ocr.OCRComic(ctx, pageCount, pageAtFunc, runner)
			if ocrErr != nil {
				job.finishFailed()
				s.jobs().clearActive(job)
				return
			}

			// Cache the OCR text so a later re-detect is instant.
			s.Cache.Set("ocrtext:"+id, textMap)

			ocrCands := metadata.DetectTextDividers(textMap, exemplarPage)
			merged := mergeByPage(capturedCands, ocrCands)
			sort.Slice(merged, func(i, j int) bool { return merged[i].Page < merged[j].Page })
			if merged == nil {
				merged = []metadata.Candidate{}
			}
			job.finishDone(merged)
			s.jobs().clearActive(job)
		}()

		resp.OCRPending = true
		resp.JobID = jobID
	}

	writeJSON(w, resp)
}

// HandleDetectChaptersStatus polls the result of a background OCR job.
// Query param: ?job=<jobId>. Returns { state, done, scanned, total, candidates }.
func (s *Server) HandleDetectChaptersStatus(w http.ResponseWriter, r *http.Request) {
	jobID := r.URL.Query().Get("job")

	type statusResponse struct {
		State      string               `json:"state"`
		Done       bool                 `json:"done"`
		Scanned    int                  `json:"scanned"`
		Total      int                  `json:"total"`
		Candidates []metadata.Candidate `json:"candidates"`
	}

	job := s.jobs().lookup(jobID)
	if jobID == "" || job == nil {
		writeJSON(w, statusResponse{State: string(detectUnknown), Candidates: []metadata.Candidate{}})
		return
	}

	state, scanned, total, cands := job.snapshot()
	if cands == nil {
		cands = []metadata.Candidate{}
	}
	writeJSON(w, statusResponse{
		State:      string(state),
		Done:       state == detectDone,
		Scanned:    scanned,
		Total:      total,
		Candidates: cands,
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
