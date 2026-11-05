package archive

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// Reader provides access to pages in a comic archive.
type Reader interface {
	PageCount() int
	PageAt(index int) (io.ReadCloser, string, error) // reader, content-type, error
	PageNames() []string                             // sorted page entry names; index matches PageAt
	ComicInfoXML() []byte
	Close() error
}

// Open returns the appropriate Reader for the given comic file path.
func Open(path string) (Reader, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".cbz":
		return OpenCBZ(path)
	case ".cbr":
		return OpenCBR(path)
	default:
		return nil, fmt.Errorf("unsupported format: %s", ext)
	}
}

func OpenCBR(path string) (Reader, error) {
	if cbrCacheDir != "" {
		return openExtractedCBR(path)
	}
	return openCBR(path)
}
