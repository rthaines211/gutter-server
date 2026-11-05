package store

import "testing"

func TestSmartFilterCRUD(t *testing.T) {
	s := newTestStore(t)

	// Create
	id, err := s.CreateSmartFilter("Unread Horror", "filter_list", "#64B5F6",
		`[{"field":"status","op":"eq","value":"unread"},{"field":"genre","op":"eq","value":"Horror"}]`)
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("expected non-zero id")
	}

	// Get
	sf, err := s.GetSmartFilter(int(id))
	if err != nil {
		t.Fatal(err)
	}
	if sf.Name != "Unread Horror" {
		t.Errorf("name: got %q, want %q", sf.Name, "Unread Horror")
	}
	if sf.Conditions != `[{"field":"status","op":"eq","value":"unread"},{"field":"genre","op":"eq","value":"Horror"}]` {
		t.Errorf("conditions: got %q", sf.Conditions)
	}

	// List
	list, err := s.ListSmartFilters()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("list count: got %d, want 1", len(list))
	}

	// Update
	if err := s.UpdateSmartFilter(int(id), "Big Unread", "star", "#FF0000",
		`[{"field":"issue_count","op":"gte","value":"5"}]`); err != nil {
		t.Fatal(err)
	}
	sf, _ = s.GetSmartFilter(int(id))
	if sf.Name != "Big Unread" {
		t.Errorf("updated name: got %q, want %q", sf.Name, "Big Unread")
	}

	// Delete
	if err := s.DeleteSmartFilter(int(id)); err != nil {
		t.Fatal(err)
	}
	list, _ = s.ListSmartFilters()
	if len(list) != 0 {
		t.Errorf("list after delete: got %d, want 0", len(list))
	}
}

func TestSmartFilterPositionAutoIncrement(t *testing.T) {
	s := newTestStore(t)
	id1, _ := s.CreateSmartFilter("First", "filter_list", "#64B5F6", "[]")
	id2, _ := s.CreateSmartFilter("Second", "filter_list", "#64B5F6", "[]")
	sf1, _ := s.GetSmartFilter(int(id1))
	sf2, _ := s.GetSmartFilter(int(id2))
	if sf1.Position >= sf2.Position {
		t.Errorf("position: first=%d should be < second=%d", sf1.Position, sf2.Position)
	}
}
