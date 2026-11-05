// Package pagehash computes the MD5 of a comic archive's first page, matching
// the definition used by the offline ComicVine dump's comic_files index.
package pagehash

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"io"

	"github.com/rthaines211/gutter-server/archive"
)

var ErrNoPages = errors.New("pagehash: archive has no pages")

// FirstPageMD5 returns the lowercase hex MD5 of the first page's bytes.
//
// "First page" is the first image entry in lexicographic order by full archive
// entry name, with ComicInfo.xml excluded — the ordering archive.Reader already
// applies. This matches how the dump's first_page_md5 column was computed, and
// it survives CBR-to-CBZ repacking because repacking preserves image bytes.
func FirstPageMD5(archivePath string) (string, error) {
	r, err := archive.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer r.Close()

	if r.PageCount() == 0 {
		return "", ErrNoPages
	}

	rc, _, err := r.PageAt(0)
	if err != nil {
		return "", err
	}
	defer rc.Close()

	h := md5.New()
	if _, err := io.Copy(h, rc); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
