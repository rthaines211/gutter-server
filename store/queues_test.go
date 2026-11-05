package store

import "testing"

func TestUpsertAndListQueues(t *testing.T) {
	s := newTestStore(t)

	if err := s.UpsertQueue(Queue{ID: "q1", Name: "Weekend", Icon: "star", Color: "#FFD700", Position: 0}, []string{"batman", "saga"}); err != nil {
		t.Fatalf("UpsertQueue: %v", err)
	}
	if err := s.UpsertQueue(Queue{ID: "q2", Name: "Marvel", Icon: "bookmark", Color: "#FFB74D", Position: 1}, nil); err != nil {
		t.Fatalf("UpsertQueue: %v", err)
	}

	list, err := s.ListQueues()
	if err != nil {
		t.Fatalf("ListQueues: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 queues, got %d", len(list))
	}
	if list[0].ID != "q1" || list[1].ID != "q2" {
		t.Errorf("unexpected order: %s, %s", list[0].ID, list[1].ID)
	}

	ids, err := s.ListQueueSeriesIDs("q1")
	if err != nil {
		t.Fatalf("ListQueueSeriesIDs: %v", err)
	}
	if len(ids) != 2 || ids[0] != "batman" || ids[1] != "saga" {
		t.Errorf("unexpected series order: %v", ids)
	}
}

func TestUpsertQueueReplacesMembershipAndPreservesOrder(t *testing.T) {
	s := newTestStore(t)
	if err := s.UpsertQueue(Queue{ID: "q1", Name: "A", Position: 0}, []string{"a", "b", "c"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertQueue(Queue{ID: "q1", Name: "A", Position: 0}, []string{"c", "a"}); err != nil {
		t.Fatal(err)
	}
	ids, err := s.ListQueueSeriesIDs("q1")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != "c" || ids[1] != "a" {
		t.Errorf("expected [c a], got %v", ids)
	}
}

func TestDeleteQueueCascades(t *testing.T) {
	s := newTestStore(t)
	if err := s.UpsertQueue(Queue{ID: "q1", Name: "A"}, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteQueue("q1"); err != nil {
		t.Fatalf("DeleteQueue: %v", err)
	}
	list, err := s.ListQueues()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("expected 0 queues, got %d", len(list))
	}
	ids, err := s.ListQueueSeriesIDs("q1")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Errorf("expected membership cascaded away, got %v", ids)
	}
}
