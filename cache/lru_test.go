package cache

import "testing"

func TestLRUGetSet(t *testing.T) {
	c := NewLRU(3)
	c.Set("a", "alpha")
	c.Set("b", "beta")
	val, ok := c.Get("a")
	if !ok || val.(string) != "alpha" {
		t.Errorf("expected alpha, got %v (ok=%v)", val, ok)
	}
	_, ok = c.Get("missing")
	if ok {
		t.Error("expected miss for 'missing'")
	}
}

func TestLRUEviction(t *testing.T) {
	c := NewLRU(2)
	c.Set("a", 1)
	c.Set("b", 2)
	c.Set("c", 3) // evicts "a"
	if _, ok := c.Get("a"); ok {
		t.Error("'a' should have been evicted")
	}
	if _, ok := c.Get("b"); !ok {
		t.Error("'b' should still exist")
	}
	if _, ok := c.Get("c"); !ok {
		t.Error("'c' should still exist")
	}
}

func TestLRUAccessRefreshes(t *testing.T) {
	c := NewLRU(2)
	c.Set("a", 1)
	c.Set("b", 2)
	c.Get("a")    // refresh "a"
	c.Set("c", 3) // evicts "b" (least recently used)
	if _, ok := c.Get("a"); !ok {
		t.Error("'a' should still exist after refresh")
	}
	if _, ok := c.Get("b"); ok {
		t.Error("'b' should have been evicted")
	}
}

func TestLRUStats(t *testing.T) {
	c := NewLRU(10)
	size, capacity := c.Stats()
	if size != 0 {
		t.Errorf("empty cache size = %d, want 0", size)
	}
	if capacity != 10 {
		t.Errorf("capacity = %d, want 10", capacity)
	}

	c.Set("a", 1)
	c.Set("b", 2)
	c.Set("c", 3)
	size, capacity = c.Stats()
	if size != 3 {
		t.Errorf("cache size = %d, want 3", size)
	}
	if capacity != 10 {
		t.Errorf("capacity = %d, want 10", capacity)
	}
}
