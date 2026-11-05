package cache

import (
	"container/list"
	"sync"
)

type LRU struct {
	mu       sync.Mutex
	capacity int
	items    map[string]*list.Element
	order    *list.List
}

type entry struct {
	key   string
	value interface{}
}

func NewLRU(capacity int) *LRU {
	return &LRU{
		capacity: capacity,
		items:    make(map[string]*list.Element),
		order:    list.New(),
	}
}

func (c *LRU) Get(key string) (interface{}, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.order.MoveToFront(el)
		return el.Value.(*entry).value, true
	}
	return nil, false
}

func (c *LRU) Set(key string, value interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.order.MoveToFront(el)
		el.Value.(*entry).value = value
		return
	}
	if c.order.Len() >= c.capacity {
		oldest := c.order.Back()
		if oldest != nil {
			c.order.Remove(oldest)
			delete(c.items, oldest.Value.(*entry).key)
		}
	}
	el := c.order.PushFront(&entry{key: key, value: value})
	c.items[key] = el
}

// Stats returns the current number of items and the maximum capacity.
func (c *LRU) Stats() (size, capacity int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items), c.capacity
}
