package pkg

import (
	"sync"
	"time"
)

type item[V any] struct {
	value     V
	expiresAt time.Time
}

type Cache[K comparable, V any] struct {
	mu    sync.RWMutex
	items map[K]item[V]
	ttl   time.Duration
}

func New[K comparable, V any](ttl time.Duration) *Cache[K, V] {
	return &Cache[K, V]{
		items: make(map[K]item[V]),
		ttl:   ttl,
	}
}

func (c *Cache[K, V]) Get(key K) (V, bool) {
	c.mu.RLock()
	it, ok := c.items[key]
	c.mu.RUnlock()
	if !ok || time.Now().After(it.expiresAt) {
		var zero V
		return zero, false
	}
	return it.value, true
}

func (c *Cache[K, V]) Set(key K, value V) {
	c.mu.Lock()
	c.items[key] = item[V]{value: value, expiresAt: time.Now().Add(c.ttl)}
	c.mu.Unlock()
}

func (c *Cache[K, V]) Delete(key K) {
	c.mu.Lock()
	delete(c.items, key)
	c.mu.Unlock()
}

func (c *Cache[K, V]) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	n := 0
	now := time.Now()
	for _, it := range c.items {
		if now.Before(it.expiresAt) {
			n++
		}
	}
	return n
}

func (c *Cache[K, V]) Cleanup() {
	c.mu.Lock()
	now := time.Now()
	for k, it := range c.items {
		if now.After(it.expiresAt) {
			delete(c.items, k)
		}
	}
	c.mu.Unlock()
}
