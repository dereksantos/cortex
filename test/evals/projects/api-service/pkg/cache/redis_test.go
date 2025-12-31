package cache

import (
	"testing"
	"time"
)

func TestMemoryCache_SetGet(t *testing.T) {
	cache := NewMemoryCache()

	// Set a value
	err := cache.Set("key1", []byte("value1"), time.Minute)
	if err != nil {
		t.Fatalf("unexpected error on Set: %v", err)
	}

	// Get the value
	value, err := cache.Get("key1")
	if err != nil {
		t.Fatalf("unexpected error on Get: %v", err)
	}
	if string(value) != "value1" {
		t.Errorf("expected 'value1', got '%s'", string(value))
	}
}

func TestMemoryCache_CacheMiss(t *testing.T) {
	cache := NewMemoryCache()

	_, err := cache.Get("nonexistent")
	if err != ErrCacheMiss {
		t.Errorf("expected ErrCacheMiss, got %v", err)
	}
}

func TestMemoryCache_Expiration(t *testing.T) {
	cache := NewMemoryCache()

	// Set with very short TTL
	err := cache.Set("expiring", []byte("value"), 10*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should exist immediately
	_, err = cache.Get("expiring")
	if err != nil {
		t.Errorf("expected value to exist, got error: %v", err)
	}

	// Wait for expiration
	time.Sleep(20 * time.Millisecond)

	// Should be expired
	_, err = cache.Get("expiring")
	if err != ErrCacheMiss {
		t.Errorf("expected ErrCacheMiss after expiration, got %v", err)
	}
}

func TestMemoryCache_NoExpiry(t *testing.T) {
	cache := NewMemoryCache()

	// Set with TTL=0 (no expiry)
	err := cache.Set("forever", []byte("value"), 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should still exist
	value, err := cache.Get("forever")
	if err != nil {
		t.Errorf("expected value to exist, got error: %v", err)
	}
	if string(value) != "value" {
		t.Errorf("expected 'value', got '%s'", string(value))
	}
}

func TestMemoryCache_Delete(t *testing.T) {
	cache := NewMemoryCache()

	cache.Set("key1", []byte("value1"), time.Minute)

	err := cache.Delete("key1")
	if err != nil {
		t.Fatalf("unexpected error on Delete: %v", err)
	}

	_, err = cache.Get("key1")
	if err != ErrCacheMiss {
		t.Errorf("expected ErrCacheMiss after delete, got %v", err)
	}
}

func TestMemoryCache_Clear(t *testing.T) {
	cache := NewMemoryCache()

	cache.Set("key1", []byte("value1"), time.Minute)
	cache.Set("key2", []byte("value2"), time.Minute)

	err := cache.Clear()
	if err != nil {
		t.Fatalf("unexpected error on Clear: %v", err)
	}

	_, err = cache.Get("key1")
	if err != ErrCacheMiss {
		t.Errorf("expected ErrCacheMiss for key1 after clear, got %v", err)
	}

	_, err = cache.Get("key2")
	if err != ErrCacheMiss {
		t.Errorf("expected ErrCacheMiss for key2 after clear, got %v", err)
	}
}

func TestMemoryCache_EmptyKey(t *testing.T) {
	cache := NewMemoryCache()

	err := cache.Set("", []byte("value"), time.Minute)
	if err != ErrKeyEmpty {
		t.Errorf("expected ErrKeyEmpty on Set, got %v", err)
	}

	_, err = cache.Get("")
	if err != ErrKeyEmpty {
		t.Errorf("expected ErrKeyEmpty on Get, got %v", err)
	}
}

func TestRedisCache_Interface(t *testing.T) {
	// Verify RedisCache implements Cache interface
	var _ Cache = NewRedisCache("localhost:6379")
}

func TestRedisCache_Ping(t *testing.T) {
	cache := NewRedisCache("localhost:6379").(*RedisCache)

	// Should not error (mock implementation)
	err := cache.Ping()
	if err != nil {
		t.Errorf("unexpected error on Ping: %v", err)
	}
}
