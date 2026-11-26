package archive

import (
	"archive/zip"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gen2brain/go-unarr"
)

// ImageEntryNames lists the archive's page entries (same filter as the
// readers) in archive order, without reading or extracting page data.
func ImageEntryNames(path string) ([]string, error) {
	var all []string
	switch strings.ToLower(filepath.Ext(path)) {
	case ".cbz":
		zr, err := zip.OpenReader(path)
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		for _, f := range zr.File {
			all = append(all, f.Name)
		}
	case ".cbr":
		a, err := unarr.NewArchive(path)
		if err != nil {
			return nil, err
		}
		defer a.Close()
		if all, err = a.List(); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported format: %s", path)
	}
	var names []string
	for _, n := range all {
		if _, ok := imageExts[filepath.Ext(strings.ToLower(filepath.Base(n)))]; ok {
			names = append(names, n)
		}
	}
	return names, nil
}

// PageOrderMap returns m with m[old] = new: old is a page's index in the
// former byte-order sort, new its index in PageLess order. Entries are
// tracked by position, so duplicate names still map one-to-one. Nil when the
// two orders agree.
func PageOrderMap(names []string) []int {
	byteOrder := sortedIndices(names, func(a, b string) bool { return a < b })
	newOrder := sortedIndices(names, PageLess)
	newPos := make([]int, len(names)) // entry index -> new page index
	for p, e := range newOrder {
		newPos[e] = p
	}
	m := make([]int, len(names))
	same := true
	for old, e := range byteOrder {
		m[old] = newPos[e]
		same = same && m[old] == old
	}
	if same {
		return nil
	}
	return m
}

func sortedIndices(names []string, less func(a, b string) bool) []int {
	idx := make([]int, len(names))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(i, j int) bool { return less(names[idx[i]], names[idx[j]]) })
	return idx
}
