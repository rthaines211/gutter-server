// ocr/tesseract.go
package ocr

import (
	"bytes"
	"context"
	"image"
	_ "image/gif"  // register GIF decoder
	_ "image/jpeg" // register JPEG decoder
	"image/png"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // register WebP decoder
)

type PageOCR func(index int, img io.Reader) (string, error)

// ocrWorkers is the number of pages OCR'd concurrently. tesseract is the slow
// step, so running one per core (leaving one for I/O) saturates the box.
func ocrWorkers() int {
	if n := runtime.NumCPU() - 1; n > 1 {
		return n
	}
	return 1
}

// OCRComic runs ocr over every page (0..pageCount-1) and returns index->text.
//
// Pages are OCR'd by a worker pool (the expensive, CPU-bound step), while a
// single producer reads the archive sequentially — so archive access stays
// serialized and safe even though OCR runs in parallel. Cancellation via ctx
// stops queuing further pages. A per-page error is recorded as empty text and
// never aborts the whole comic.
func OCRComic(ctx context.Context, pageCount int, pageAt func(int) (io.ReadCloser, error), ocr PageOCR) (map[int]string, error) {
	out := make(map[int]string, pageCount)
	var mu sync.Mutex // guards out

	type job struct {
		i   int
		buf []byte
	}

	workers := ocrWorkers()
	jobs := make(chan job, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				txt, err := ocr(j.i, bytes.NewReader(j.buf))
				mu.Lock()
				if err != nil {
					out[j.i] = ""
				} else {
					out[j.i] = txt
				}
				mu.Unlock()
			}
		}()
	}

	// Producer: read each page fully (under the archive's own serialization in
	// pageAt) before dispatching its bytes to the pool.
	var retErr error
	for i := 0; i < pageCount; i++ {
		select {
		case <-ctx.Done():
			retErr = ctx.Err()
		default:
		}
		if retErr != nil {
			break
		}
		rc, err := pageAt(i)
		if err != nil {
			mu.Lock()
			out[i] = ""
			mu.Unlock()
			continue
		}
		buf, rerr := io.ReadAll(rc)
		rc.Close()
		if rerr != nil {
			mu.Lock()
			out[i] = ""
			mu.Unlock()
			continue
		}
		jobs <- job{i: i, buf: buf}
	}
	close(jobs)
	wg.Wait()
	return out, retErr
}

// ocrMaxWidth caps the width fed to tesseract. Comic pages are scanned far
// higher than OCR needs; downscaling wide pages cuts tesseract's work
// substantially while leaving stylized title text legible.
const ocrMaxWidth = 1600

// RunTesseract is the production PageOCR body: downscale the page, then pipe it
// to tesseract. --psm 3 (auto page segmentation) reads stylized title art;
// --psm 6 fails. OMP_THREAD_LIMIT=1 keeps each tesseract single-threaded so the
// page-level worker pool (above) owns the parallelism predictably.
func RunTesseract(img io.Reader) (string, error) {
	cmd := exec.Command("tesseract", "stdin", "stdout", "--psm", "3")
	cmd.Env = append(os.Environ(), "OMP_THREAD_LIMIT=1")
	cmd.Stdin = downscaleForOCR(img)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out.String(), nil
}

// downscaleForOCR returns an image reader no wider than ocrMaxWidth. It uses a
// cheap header read to skip already-small pages, and on any decode failure
// returns the original bytes unchanged so tesseract still runs on the raw page.
func downscaleForOCR(r io.Reader) io.Reader {
	raw, err := io.ReadAll(r)
	if err != nil {
		return bytes.NewReader(nil)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width <= ocrMaxWidth {
		return bytes.NewReader(raw) // unknown format or already small → raw
	}
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return bytes.NewReader(raw)
	}
	b := src.Bounds()
	h := b.Dy() * ocrMaxWidth / b.Dx()
	if h < 1 {
		h = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, ocrMaxWidth, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return bytes.NewReader(raw)
	}
	return &buf
}
