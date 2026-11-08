// Package kosync holds the pure parts of the KOReader sync ("kosync")
// protocol: request/response shapes, KOReader's document digests, and the
// mapping between KOReader progress and Gutter's page index.
//
// # Document identity
//
// KOReader identifies a book by a digest it computes on the device, in one
// of two ways (the "Document matching method" setting):
//
//   - Binary (default): util.partialMD5 — MD5 over 1 KiB samples taken at
//     offsets 0, 1 KiB, 4 KiB, 16 KiB, ... 1024<<(2*i) for i = 0..10,
//     stopping at the first offset past end of file. See PartialMD5.
//   - Filename: MD5 of the file's base name, extension included.
//
// Gutter serves the original archive bytes over OPDS, so a CBZ that KOReader
// downloaded from Gutter has the same binary digest as the library file.
//
// # Page ↔ position mapping (CBZ)
//
// KOReader opens CBZ as a paged document (MuPDF). For paged documents the
// kosync "progress" field is the 1-based page number as a decimal string and
// "percentage" is page/total. Gutter's reading_progress.current_page is a
// 0-based page index, with current_page == total_pages meaning "finished".
//
//	KOReader page p (1..T)  →  Gutter current_page p-1, or T when p == T
//	Gutter current_page c   →  KOReader page c+1, capped at T
//
// Reaching the last page in KOReader marks the issue finished in Gutter,
// which matches what the Gutter reader does on the last page.
//
// Limits: the mapping assumes KOReader and Gutter count and order the same
// image entries. Archives with non-image files mixed in, or page names whose
// sort order differs between MuPDF and Gutter, can be off by a page or more.
// Reflowable progress (EPUB xpointers) is not a page number; for it we fall
// back to percentage × total pages.
package kosync

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"path/filepath"
	"strconv"
	"strings"
)

// Error codes from the reference koreader-sync-server.
const (
	CodeUnauthorized    = 2001
	CodeUserExists      = 2002
	CodeInvalidFields   = 2003
	CodeDocumentMissing = 2004
)

// ProgressRequest is the body of PUT /syncs/progress.
type ProgressRequest struct {
	Document   string  `json:"document"`
	Progress   string  `json:"progress"`
	Percentage float64 `json:"percentage"`
	Device     string  `json:"device"`
	DeviceID   string  `json:"device_id"`
}

// ProgressResponse is the body of GET /syncs/progress/{document}. Timestamp
// is unix seconds.
type ProgressResponse struct {
	Document   string  `json:"document"`
	Progress   string  `json:"progress"`
	Percentage float64 `json:"percentage"`
	Device     string  `json:"device"`
	DeviceID   string  `json:"device_id"`
	Timestamp  int64   `json:"timestamp"`
}

// UpdateResponse is the body returned by PUT /syncs/progress.
type UpdateResponse struct {
	Document  string `json:"document"`
	Timestamp int64  `json:"timestamp"`
}

// UserRequest is the body of POST /users/create. Password is what KOReader
// sends: the MD5 hex of the user's password.
type UserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// ErrorResponse is the error body shape KOReader understands.
type ErrorResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Validate reports whether a progress update has the fields KOReader always
// sends.
func (r ProgressRequest) Validate() error {
	if r.Document == "" || strings.ContainsAny(r.Document, ":/") {
		return errors.New("invalid document")
	}
	if r.Progress == "" || r.Device == "" {
		return errors.New("missing progress or device")
	}
	if r.Percentage < 0 || r.Percentage > 1 || math.IsNaN(r.Percentage) {
		return errors.New("percentage out of range")
	}
	return nil
}

// KeyFor returns the x-auth-key KOReader sends for a password: MD5 hex.
func KeyFor(password string) string {
	sum := md5.Sum([]byte(password))
	return hex.EncodeToString(sum[:])
}

// FilenameDigest is KOReader's "filename" document digest: MD5 hex of the
// base name of path.
func FilenameDigest(path string) string {
	sum := md5.Sum([]byte(filepath.Base(path)))
	return hex.EncodeToString(sum[:])
}

// partialMD5 sample geometry, from KOReader's util.partialMD5.
const (
	sampleStep = 1024
	sampleSize = 1024
)

// sampleOffsets lists where PartialMD5 reads. KOReader loops i = -1..10 over
// lshift(1024, 2*i); LuaJIT masks the shift count to 5 bits, so i = -1 is a
// shift by 30, which overflows 32 bits to 0.
func sampleOffsets() []int64 {
	offs := []int64{0}
	for i := 0; i <= 10; i++ {
		offs = append(offs, int64(sampleStep)<<(2*i))
	}
	return offs
}

// PartialMD5 computes KOReader's binary document digest for a file of the
// given size. It reads at most 12 KiB.
func PartialMD5(r io.ReaderAt, size int64) (string, error) {
	h := md5.New()
	buf := make([]byte, sampleSize)
	for _, off := range sampleOffsets() {
		if off >= size {
			break // KOReader: file:read returns nil at EOF → stop
		}
		n, err := r.ReadAt(buf, off)
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		h.Write(buf[:n])
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ToGutterPage maps a KOReader progress update to a Gutter 0-based
// current_page, given the comic's page count. ok is false when the position
// can't be mapped (no page count and no usable percentage).
func ToGutterPage(progress string, percentage float64, totalPages int) (page int, ok bool) {
	if totalPages <= 0 {
		return 0, false
	}
	p, err := strconv.Atoi(strings.TrimSpace(progress))
	if err != nil || p < 1 {
		// Not a page number (e.g. an EPUB xpointer): use the percentage.
		if percentage <= 0 || math.IsNaN(percentage) {
			return 0, err == nil // "0" or negative → start of book
		}
		p = int(math.Round(percentage * float64(totalPages)))
		if p < 1 {
			p = 1
		}
	}
	if p >= totalPages {
		return totalPages, true // last page → finished
	}
	return p - 1, true
}

// FromGutterPage maps a Gutter 0-based current_page to KOReader's 1-based
// page and percentage.
func FromGutterPage(currentPage, totalPages int) (progress string, percentage float64) {
	if totalPages <= 0 {
		return "1", 0
	}
	p := currentPage + 1
	if p > totalPages {
		p = totalPages
	}
	if p < 1 {
		p = 1
	}
	pct := float64(p) / float64(totalPages)
	// Round to 4 places like KOReader does when it reports percentages.
	pct = math.Round(pct*10000) / 10000
	return strconv.Itoa(p), pct
}
