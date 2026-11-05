package archive

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gen2brain/go-unarr"
)

type CBRReader struct {
	pages    []cbrPage
	comicXML []byte
}

type cbrPage struct {
	name string
	data []byte
}

func openCBR(path string) (*CBRReader, error) {
	a, err := unarr.NewArchive(path)
	if err != nil {
		return nil, fmt.Errorf("opening cbr: %w", err)
	}
	defer a.Close()

	r := &CBRReader{}

	for {
		err := a.Entry()
		if err != nil {
			break
		}

		name := a.Name()
		lower := strings.ToLower(filepath.Base(name))
		ext := filepath.Ext(lower)

		data, err := a.ReadAll()
		if err != nil {
			continue
		}

		if lower == "comicinfo.xml" {
			r.comicXML = data
			continue
		}

		if _, ok := imageExts[ext]; ok {
			r.pages = append(r.pages, cbrPage{name: name, data: data})
		}
	}

	sort.Slice(r.pages, func(i, j int) bool {
		return r.pages[i].name < r.pages[j].name
	})

	return r, nil
}

func (r *CBRReader) PageCount() int { return len(r.pages) }

func (r *CBRReader) PageNames() []string {
	out := make([]string, len(r.pages))
	for i, p := range r.pages {
		out[i] = p.name
	}
	return out
}

func (r *CBRReader) PageAt(index int) (io.ReadCloser, string, error) {
	if index < 0 || index >= len(r.pages) {
		return nil, "", fmt.Errorf("page index %d out of range (0-%d)", index, len(r.pages)-1)
	}
	page := r.pages[index]
	ext := strings.ToLower(filepath.Ext(page.name))
	contentType := imageExts[ext]
	return io.NopCloser(bytes.NewReader(page.data)), contentType, nil
}

func (r *CBRReader) ComicInfoXML() []byte { return r.comicXML }
func (r *CBRReader) Close() error         { return nil }
