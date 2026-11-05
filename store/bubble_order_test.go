package store

import (
	"reflect"
	"testing"
)

func TestBubbleOrderRoundTrip(t *testing.T) {
	s := newTestStore(t)
	got, err := s.GetBubbleOrder("abc")
	if err != nil || got != nil {
		t.Fatalf("empty: got %v, %v; want nil, nil", got, err)
	}
	if err := s.SetBubbleOrder("abc", []int{2, 1, 3}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBubbleOrder("abc", []int{3, 1, 2}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetBubbleOrder("abc"); !reflect.DeepEqual(got, []int{3, 1, 2}) {
		t.Fatalf("got %v, want [3 1 2]", got)
	}
	if err := s.DeleteBubbleOrder("abc"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetBubbleOrder("abc"); got != nil {
		t.Fatalf("after delete got %v", got)
	}
}
