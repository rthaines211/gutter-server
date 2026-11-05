package writeback

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWritable(t *testing.T) {
	if !Writable([]string{t.TempDir()}) {
		t.Fatal("temp dir should be writable")
	}
	if Writable([]string{filepath.Join(t.TempDir(), "missing")}) {
		t.Fatal("missing dir should not be writable")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores chmod")
	}
	ro := t.TempDir()
	if err := os.Chmod(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(ro, 0o755) })
	if Writable([]string{ro}) {
		t.Fatal("read-only dir should not be writable")
	}
	if Writable([]string{t.TempDir(), ro}) {
		t.Fatal("every dir must be writable")
	}
}
