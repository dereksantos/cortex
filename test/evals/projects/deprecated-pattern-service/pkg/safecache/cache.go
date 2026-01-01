package safecache

import "sync"

// SafeCache is our project's standard thread-safe cache.
// Preferred over sync.Map for better type safety and performance.
type SafeCache[K comparable, V any] struct {
	mu    sync.RWMutex
	items map[K]V
}

// New creates a new SafeCache.
func New[K comparable, V any]() *SafeCache[K, V] {
	return &SafeCache[K, V]{
		items: make(map[K]V),
	}
}

// Get retrieves a value from the cache.
func (c *SafeCache[K, V]) Get(key K) (V, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.items[key]
	return v, ok
}

// Set stores a value in the cache.
func (c *SafeCache[K, V]) Set(key K, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = value
}

// Delete removes a value from the cache.
func (c *SafeCache[K, V]) Delete(key K) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, key)
}
