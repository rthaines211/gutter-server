package store

import "testing"

func TestCreateAndListCollections(t *testing.T) {
	s := newTestStore(t)
	id1, err := s.CreateCollection("Favorites", "star", "#FFD700")
	if err != nil {
		t.Fatalf("CreateCollection failed: %v", err)
	}
	id2, err := s.CreateCollection("To Read", "bookmark", "#FFB74D")
	if err != nil {
		t.Fatalf("CreateCollection failed: %v", err)
	}
	if id1 == id2 {
		t.Error("expected different IDs")
	}
	list, err := s.ListCollections()
	if err != nil {
		t.Fatalf("ListCollections failed: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 collections, got %d", len(list))
	}
	if list[0].Name != "Favorites" || list[1].Name != "To Read" {
		t.Errorf("unexpected order: %s, %s", list[0].Name, list[1].Name)
	}
	if list[0].Position != 0 || list[1].Position != 1 {
		t.Errorf("expected positions 0,1 got %d,%d", list[0].Position, list[1].Position)
	}
}

func TestGetCollection(t *testing.T) {
	s := newTestStore(t)
	id, _ := s.CreateCollection("Test", "star", "#FFF")
	c, err := s.GetCollection(int(id))
	if err != nil {
		t.Fatalf("GetCollection failed: %v", err)
	}
	if c.Name != "Test" {
		t.Errorf("expected 'Test', got %q", c.Name)
	}
}

func TestUpdateCollection(t *testing.T) {
	s := newTestStore(t)
	id, _ := s.CreateCollection("Old Name", "star", "#FFF")
	if err := s.UpdateCollection(int(id), "New Name", "heart", "#F00"); err != nil {
		t.Fatalf("UpdateCollection failed: %v", err)
	}
	c, _ := s.GetCollection(int(id))
	if c.Name != "New Name" || c.Icon != "heart" || c.Color != "#F00" {
		t.Errorf("update did not apply: %+v", c)
	}
}

func TestDeleteCollection(t *testing.T) {
	s := newTestStore(t)
	id, _ := s.CreateCollection("To Delete", "star", "#FFF")
	if err := s.DeleteCollection(int(id)); err != nil {
		t.Fatalf("DeleteCollection failed: %v", err)
	}
	list, _ := s.ListCollections()
	if len(list) != 0 {
		t.Errorf("expected 0 collections after delete, got %d", len(list))
	}
}

func TestCollectionSeriesCRUD(t *testing.T) {
	s := newTestStore(t)
	colID, _ := s.CreateCollection("Faves", "star", "#FFF")
	if err := s.AddSeriesToCollection(int(colID), "spider-man"); err != nil {
		t.Fatalf("AddSeriesToCollection failed: %v", err)
	}
	if err := s.AddSeriesToCollection(int(colID), "batman"); err != nil {
		t.Fatalf("AddSeriesToCollection failed: %v", err)
	}
	if err := s.AddSeriesToCollection(int(colID), "spider-man"); err != nil {
		t.Fatalf("duplicate AddSeriesToCollection should not error: %v", err)
	}
	ids, err := s.ListCollectionSeriesIDs(int(colID))
	if err != nil {
		t.Fatalf("ListCollectionSeriesIDs failed: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 series, got %d", len(ids))
	}
	if ids[0] != "spider-man" || ids[1] != "batman" {
		t.Errorf("unexpected order: %v", ids)
	}
	if err := s.RemoveSeriesFromCollection(int(colID), "spider-man"); err != nil {
		t.Fatalf("RemoveSeriesFromCollection failed: %v", err)
	}
	ids, _ = s.ListCollectionSeriesIDs(int(colID))
	if len(ids) != 1 || ids[0] != "batman" {
		t.Errorf("after remove expected [batman], got %v", ids)
	}
}

func TestReorderCollectionSeries(t *testing.T) {
	s := newTestStore(t)
	colID, _ := s.CreateCollection("Ordered", "star", "#FFF")
	s.AddSeriesToCollection(int(colID), "a")
	s.AddSeriesToCollection(int(colID), "b")
	s.AddSeriesToCollection(int(colID), "c")
	if err := s.ReorderCollectionSeries(int(colID), []string{"c", "a", "b"}); err != nil {
		t.Fatalf("ReorderCollectionSeries failed: %v", err)
	}
	ids, _ := s.ListCollectionSeriesIDs(int(colID))
	if len(ids) != 3 || ids[0] != "c" || ids[1] != "a" || ids[2] != "b" {
		t.Errorf("expected [c a b], got %v", ids)
	}
}

func TestDeleteCollectionCascadesSeriesEntries(t *testing.T) {
	s := newTestStore(t)
	colID, _ := s.CreateCollection("Temp", "star", "#FFF")
	s.AddSeriesToCollection(int(colID), "x")
	s.DeleteCollection(int(colID))
	ids, _ := s.ListCollectionSeriesIDs(int(colID))
	if len(ids) != 0 {
		t.Errorf("expected cascade delete, got %d series entries", len(ids))
	}
}
