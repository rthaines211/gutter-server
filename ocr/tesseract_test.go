// ocr/tesseract_test.go
package ocr

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"io"
	"strings"
	"testing"
)

func TestOCRComicCollectsAllPages(t *testing.T) {
	pageAt := func(i int) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(fmt.Sprintf("page%d", i))), nil
	}
	fake := func(i int, img io.Reader) (string, error) {
		b, _ := io.ReadAll(img)
		return "OCR:" + string(b), nil
	}
	got, err := OCRComic(context.Background(), 3, pageAt, fake)
	if err != nil { t.Fatal(err) }
	if got[0] != "OCR:page0" || got[2] != "OCR:page2" || len(got) != 3 {
		t.Fatalf("got %+v", got)
	}
}

// Runs many pages through the worker pool; with -race this also guards the
// concurrent writes to the result map. Results must be correct and keyed by
// page index regardless of completion order.
func TestOCRComicCollectsManyPagesConcurrently(t *testing.T) {
	pageAt := func(i int) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(fmt.Sprintf("%d", i))), nil
	}
	fake := func(i int, img io.Reader) (string, error) {
		b, _ := io.ReadAll(img)
		return "v" + string(b), nil
	}
	got, err := OCRComic(context.Background(), 64, pageAt, fake)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 64 {
		t.Fatalf("want 64 pages, got %d", len(got))
	}
	for i := 0; i < 64; i++ {
		if got[i] != fmt.Sprintf("v%d", i) {
			t.Fatalf("page %d = %q", i, got[i])
		}
	}
}

func TestOCRComicPageReadErrorBecomesEmpty(t *testing.T) {
	pageAt := func(i int) (io.ReadCloser, error) {
		if i == 1 {
			return nil, fmt.Errorf("boom")
		}
		return io.NopCloser(strings.NewReader(fmt.Sprintf("p%d", i))), nil
	}
	fake := func(i int, img io.Reader) (string, error) {
		b, _ := io.ReadAll(img)
		return string(b), nil
	}
	got, _ := OCRComic(context.Background(), 3, pageAt, fake)
	if got[1] != "" {
		t.Fatalf("failed page should be empty, got %q", got[1])
	}
	if got[0] != "p0" || got[2] != "p2" || len(got) != 3 {
		t.Fatalf("got %+v", got)
	}
}

func TestDownscaleForOCRShrinksWidePage(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 3200, 400))); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := image.DecodeConfig(downscaleForOCR(bytes.NewReader(buf.Bytes())))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != ocrMaxWidth {
		t.Fatalf("width %d, want %d", cfg.Width, ocrMaxWidth)
	}
	if cfg.Height != 200 { // 400 * 1600/3200
		t.Fatalf("height %d, want 200 (aspect preserved)", cfg.Height)
	}
}

func TestDownscaleForOCRPassesThroughSmallPage(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 800, 600))); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	got, _ := io.ReadAll(downscaleForOCR(bytes.NewReader(raw)))
	if !bytes.Equal(got, raw) {
		t.Fatal("a page already <= ocrMaxWidth should pass through unchanged")
	}
}

func TestDownscaleForOCRPassesThroughNonImage(t *testing.T) {
	raw := []byte("definitely not an image")
	got, _ := io.ReadAll(downscaleForOCR(bytes.NewReader(raw)))
	if !bytes.Equal(got, raw) {
		t.Fatal("undecodable input should pass through so tesseract still runs")
	}
}
