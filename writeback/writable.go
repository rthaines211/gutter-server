package writeback

import "os"

// Writable reports whether a temp file can be created and removed in every dir.
func Writable(dirs []string) bool {
	for _, d := range dirs {
		f, err := os.CreateTemp(d, ".gutter-write-test-*")
		if err != nil {
			return false
		}
		f.Close()
		if os.Remove(f.Name()) != nil {
			return false
		}
	}
	return true
}
