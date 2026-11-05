package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/ryanhaines/comic-reader/metadata"
)

func TestDetectChaptersStructural(t *testing.T) {
	srv, comicFile := setupComicTest(t)
	id := pathToID(comicFile)
	req := httptest.NewRequest("POST", "/comics/"+id+"/detect-chapters", nil)
	req.SetPathValue("id", id)
	w := httptest.NewRecorder()
	srv.HandleDetectChapters(w, req)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Candidates []map[string]any `json:"candidates"`
		OCRPending bool             `json:"ocrPending"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.OCRPending {
		t.Error("phase 1 must not report ocrPending")
	}
	// candidates may be empty for a plain single issue — assert shape only.
}

// TestDetectChaptersOCRMergesDividers verifies that detectCandidates merges
// OCR text-divider candidates when OCR text is already cached.
func TestDetectChaptersOCRMergesDividers(t *testing.T) {
	srv, comicFile := setupComicTest(t)
	id := pathToID(comicFile)

	// Seed OCR text into the cache so detectCandidates picks it up.
	// Page 0 text matches the divider pattern: ALLCAPS title + issue number.
	ocrText := map[int]string{
		0: `AMAZING SPIDER-MAN #1 "ORIGIN"`,
		1: "story words and art and panels and things",
		2: "more story words here on this page",
	}
	srv.Cache.Set("ocrtext:"+id, ocrText)

	got := srv.detectCandidates(id, nil)

	found := false
	for _, c := range got {
		if c.Source == "ocr" && c.Page == 0 {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected an ocr candidate at page 0 in %+v", got)
	}
}

// Replaces TestDetectChaptersStatusDone.
func TestDetectChaptersStatusDone(t *testing.T) {
	srv, comicFile := setupComicTest(t)
	id := pathToID(comicFile)

	job, ok := srv.jobs().start("job-done", id)
	if !ok {
		t.Fatal("start failed")
	}
	job.setTotal(3)
	job.incScanned()
	job.finishDone([]metadata.Candidate{
		{Page: 0, Title: "Issue 1", Source: "ocr", Confidence: "medium"},
	})

	req := httptest.NewRequest("GET", "/comics/"+id+"/detect-chapters/status?job=job-done", nil)
	req.SetPathValue("id", id)
	w := httptest.NewRecorder()
	srv.HandleDetectChaptersStatus(w, req)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		State      string           `json:"state"`
		Done       bool             `json:"done"`
		Total      int              `json:"total"`
		Candidates []map[string]any `json:"candidates"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.State != "done" || !resp.Done {
		t.Errorf("expected done state, got state=%q done=%v", resp.State, resp.Done)
	}
	if resp.Total != 3 {
		t.Errorf("expected total 3, got %d", resp.Total)
	}
	if len(resp.Candidates) == 0 {
		t.Error("expected candidates when done")
	}
}

// Replaces TestDetectChaptersStatusPending: an unrecognized id reports "unknown"
// (the post-restart case) instead of hanging on done:false forever.
func TestDetectChaptersStatusUnknown(t *testing.T) {
	srv, comicFile := setupComicTest(t)
	id := pathToID(comicFile)

	req := httptest.NewRequest("GET", "/comics/"+id+"/detect-chapters/status?job=notexist", nil)
	req.SetPathValue("id", id)
	w := httptest.NewRecorder()
	srv.HandleDetectChaptersStatus(w, req)

	var resp struct {
		State string `json:"state"`
		Done  bool   `json:"done"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.State != "unknown" {
		t.Errorf("expected state unknown, got %q", resp.State)
	}
	if resp.Done {
		t.Error("unknown job must not report done")
	}
}

// A second detect while one runs is rejected with 409.
func TestDetectChaptersBusy(t *testing.T) {
	srv, comicFile := setupComicTest(t)
	id := pathToID(comicFile)

	if _, ok := srv.jobs().start("running-job", id); !ok {
		t.Fatal("seed start failed")
	}

	req := httptest.NewRequest("POST", "/comics/"+id+"/detect-chapters", nil)
	req.SetPathValue("id", id)
	w := httptest.NewRecorder()
	srv.HandleDetectChapters(w, req)
	if w.Code != 409 {
		t.Fatalf("expected 409 while a job runs, got %d: %s", w.Code, w.Body.String())
	}
}
