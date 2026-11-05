package store

import "testing"

func TestIssueDir(t *testing.T) {
	primary := "/lib/Rook: Exodus (2024)"
	cases := []struct{ id, filename, want string }{
		{PathToID("Rook: Exodus (2024)/a.cbz"), "a.cbz", primary},
		{PathToID("Rook Exodus (2024)/b.cbz"), "b.cbz", "/lib/Rook Exodus (2024)"},
		{"c1", "c.cbz", primary},                       // not a path-derived id
		{PathToID("b.cbz"), "b.cbz", primary},          // no folder component
		{PathToID("X (2024)/b.cbz"), "z.cbz", primary}, // id/filename disagree
	}
	for _, c := range cases {
		if got := IssueDir(primary, c.id, c.filename); got != c.want {
			t.Errorf("IssueDir(%q, %q) = %q, want %q", c.id, c.filename, got, c.want)
		}
	}
}
