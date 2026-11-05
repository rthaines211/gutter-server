package localcv

import "testing"

// An ambiguous hash must resolve to the same issue every time. 491 hashes in
// the real dump map to more than one issue id (variant covers and reprints
// share a first page); without a deterministic ordering the same file could
// match different issues on different runs.
func TestIssueByFirstPageMD5IsDeterministicWhenAmbiguous(t *testing.T) {
	path := newFixture(t)
	seedHashCollision(t, path, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	for i := 0; i < 5; i++ {
		iss, err := d.IssueByFirstPageMD5("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		if err != nil {
			t.Fatal(err)
		}
		if iss.IssueID != 317966 {
			t.Fatalf("run %d: expected the lowest colliding id 317966, got %d", i, iss.IssueID)
		}
	}
}

func TestIssueByFirstPageMD5(t *testing.T) {
	d, err := Open(newFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	iss, err := d.IssueByFirstPageMD5("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	if iss.IssueID != 317966 {
		t.Fatalf("expected issue 317966, got %d", iss.IssueID)
	}
	if iss.VolumeName != "Saga" || iss.IssueNumber != "1" {
		t.Fatalf("unexpected issue %+v", iss)
	}
	if iss.Publisher != "Image" || iss.VolumeStartYear != "2012" {
		t.Fatalf("unexpected volume detail %+v", iss)
	}
	if iss.VolumeIssueCount != 66 {
		t.Fatalf("expected VolumeIssueCount 66, got %d", iss.VolumeIssueCount)
	}
}

func TestIssueByFirstPageMD5NotFound(t *testing.T) {
	d, err := Open(newFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	if _, err := d.IssueByFirstPageMD5("00000000000000000000000000000000"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestOpenIsReadOnly(t *testing.T) {
	d, err := Open(newFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	if _, err := d.db.Exec("INSERT INTO cv_publisher(id, name) VALUES (99, 'Nope')"); err == nil {
		t.Fatal("expected write to fail on a read-only handle")
	}
}
