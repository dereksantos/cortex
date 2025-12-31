package cache

import (
	"errors"
	"sync"
	"time"
)

var (
	ErrCacheMiss = errors.New("cache miss")
	ErrKeyEmpty  = errors.New("key cannot be empty")
)

// Cache defines the interface for cache operations.
// Implementations should be safe for concurrent use.
type Cache interface {
	// Get retrieves a value from the cache.
	// Returns ErrCacheMiss if the key doesn't exist or has expired.
	Get(key string) ([]byte, error)

	// Set stores a value in the cache with the given TTL.
	// If ttl is 0, the item never expires.
	Set(key string, value []byte, ttl time.Duration) error

	// Delete removes a key from the cache.
	Delete(key string) error

	// Clear removes all keys from the cache.
	Clear() error
}

// cacheEntry represents a cached item with expiration.
type cacheEntry struct {
	value     []byte
	expiresAt time.Time
	noExpiry  bool
}

// MemoryCache is an in-memory cache implementation.
// This is provided for testing and development.
// In production, use RedisCache for distributed caching.
type MemoryCache struct {
	mu    sync.RWMutex
	items map[string]*cacheEntry
}

// NewMemoryCache creates a new in-memory cache.
func NewMemoryCache() *MemoryCache {
	return &MemoryCache{
		items: make(map[string]*cacheEntry),
	}
}

// Get retrieves a value from the cache.
func (c *MemoryCache) Get(key string) ([]byte, error) {
	if key == "" {
		return nil, ErrKeyEmpty
	}

	c.mu.RLock()
	entry, ok := c.items[key]
	c.mu.RUnlock()

	if !ok {
		return nil, ErrCacheMiss
	}

	if !entry.noExpiry && time.Now().After(entry.expiresAt) {
		// Entry has expired, delete it
		c.Delete(key)
		return nil, ErrCacheMiss
	}

	return entry.value, nil
}

// Set stores a value in the cache.
func (c *MemoryCache) Set(key string, value []byte, ttl time.Duration) error {
	if key == "" {
		return ErrKeyEmpty
	}

	entry := &cacheEntry{
		value: value,
	}

	if ttl == 0 {
		entry.noExpiry = true
	} else {
		entry.expiresAt = time.Now().Add(ttl)
	}

	c.mu.Lock()
	c.items[key] = entry
	c.mu.Unlock()

	return nil
}

// Delete removes a key from the cache.
func (c *MemoryCache) Delete(key string) error {
	c.mu.Lock()
	delete(c.items, key)
	c.mu.Unlock()
	return nil
}

// Clear removes all keys from the cache.
func (c *MemoryCache) Clear() error {
	c.mu.Lock()
	c.items = make(map[string]*cacheEntry)
	c.mu.Unlock()
	return nil
}

// RedisCache implements Cache using Redis.
// This is a stub that would connect to a real Redis instance.
// For this scaffold, it wraps MemoryCache for testing.
type RedisCache struct {
	addr string
	*MemoryCache
}

// NewRedisCache creates a new Redis cache client.
// In production, this would establish a connection to Redis.
// For testing purposes, it uses an in-memory implementation.
func NewRedisCache(addr string) Cache {
	return &RedisCache{
		addr:        addr,
		MemoryCache: NewMemoryCache(),
	}
}

// Ping checks if the Redis connection is alive.
func (c *RedisCache) Ping() error {
	// In production, this would ping the Redis server
	return nil
}
