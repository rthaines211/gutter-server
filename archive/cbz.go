package archive

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
)

// CBZReader reads pages from a CBZ (ZIP) comic archive.
type CBZReader struct {
	zr       *zip.ReadCloser
	pages    []*zip.File
	comicXML []byte
}

var imageExts = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".webp": "image/webp",
}

// OpenCBZ opens a CBZ file and returns a CBZReader.
func OpenCBZ(path string) (*CBZReader, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("opening cbz: %w", err)
	}
	r := &CBZReader{zr: zr}
	for _, f := range zr.File {
		name := strings.ToLower(filepath.Base(f.Name))
		ext := filepath.Ext(name)
		if name == "comicinfo.xml" {
			rc, err := f.Open()
			if err == nil {
				r.comicXML, _ = io.ReadAll(rc)
				rc.Close()
			}
			continue
		}
		if _, ok := imageExts[ext]; ok {
			r.pages = append(r.pages, f)
		}
	}
	sort.Slice(r.pages, func(i, j int) bool {
		return r.pages[i].Name < r.pages[j].Name
	})
	return r, nil
}

// PageCount returns the number of image pages in the archive.
func (r *CBZReader) PageCount() int { return len(r.pages) }

// PageNames returns the page entry names in served order.
func (r *CBZReader) PageNames() []string {
	out := make([]string, len(r.pages))
	for i, f := range r.pages {
		out[i] = f.Name
	}
	return out
}

// PageAt returns a reader for the page at the given index, along with its content type.
func (r *CBZReader) PageAt(index int) (io.ReadCloser, string, error) {
	if index < 0 || index >= len(r.pages) {
		return nil, "", fmt.Errorf("page index %d out of range (0-%d)", index, len(r.pages)-1)
	}
	f := r.pages[index]
	ext := strings.ToLower(filepath.Ext(f.Name))
	contentType := imageExts[ext]
	rc, err := f.Open()
	if err != nil {
		return nil, "", fmt.Errorf("opening page %d: %w", index, err)
	}
	data, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		return nil, "", fmt.Errorf("reading page %d: %w", index, err)
	}
	return io.NopCloser(bytes.NewReader(data)), contentType, nil
}

// ComicInfoXML returns the raw bytes of ComicInfo.xml if present, or nil.
func (r *CBZReader) ComicInfoXML() []byte { return r.comicXML }

// Close releases resources held by the CBZReader.
func (r *CBZReader) Close() error { return r.zr.Close() }
