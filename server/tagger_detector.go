package server

import (
	"context"
	"io"
	"time"

	"github.com/ryanhaines/comic-reader/ocr"
	"github.com/ryanhaines/comic-reader/tagger"
)

// notePageServed records that the reader just served a page; the tagger worker
// yields while this is recent. Called from the page handler.
func (s *Server) notePageServed() { s.lastPageServed.Store(time.Now().UnixNano()) }

func (s *Server) RecentlyActive() bool {
	last := s.lastPageServed.Load()
	return last != 0 && time.Since(time.Unix(0, last)) < 30*time.Second
}

func (s *Server) PageMeta(comicID string) (int, []string, error) {
	absPath, err := s.resolveComicPath(comicID)
	if err != nil {
		return 0, nil, err
	}
	cached, err := getArchive(absPath)
	if err != nil {
		return 0, nil, err
	}
	cached.mu.Lock()
	defer cached.mu.Unlock()
	return cached.reader.PageCount(), cached.reader.PageNames(), nil
}

// Detect runs OCR synchronously (filling the ocrtext cache) then merges
// structural + OCR candidates — the same logic the on-demand handler uses.
func (s *Server) Detect(comicID string) ([]tagger.DetectedChapter, error) {
	absPath, err := s.resolveComicPath(comicID)
	if err != nil {
		return nil, err
	}
	cached, err := getArchive(absPath)
	if err != nil {
		return nil, err
	}
	cached.mu.Lock()
	pageCount := cached.reader.PageCount()
	cached.mu.Unlock()

	if s.ocrRunner != nil {
		pageAt := func(i int) (io.ReadCloser, error) {
			cached.mu.Lock()
			rc, _, err := cached.reader.PageAt(i)
			cached.mu.Unlock()
			return rc, err
		}
		if textMap, err := ocr.OCRComic(context.Background(), pageCount, pageAt, s.ocrRunner); err == nil {
			s.Cache.Set("ocrtext:"+comicID, textMap)
		}
	}
	out := []tagger.DetectedChapter{}
	for _, c := range s.detectCandidates(comicID, nil) {
		out = append(out, tagger.DetectedChapter{Page: c.Page, Title: c.Title, Confidence: c.Confidence})
	}
	return out, nil
}
