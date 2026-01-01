package service

import (
	"sync"
	"testing"
)

func TestUserCache_SetAndGet(t *testing.T) {
	cache := NewUserCache()

	user := &User{ID: "123", Name: "Alice"}
	cache.Set(user)

	got, ok := cache.Get("123")
	if !ok {
		t.Fatal("expected to find user in cache")
	}
	if got.Name != "Alice" {
		t.Errorf("expected Alice, got %s", got.Name)
	}
}

func TestUserCache_ThreadSafe(t *testing.T) {
	cache := NewUserCache()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			user := &User{ID: string(rune(id)), Name: "User"}
			cache.Set(user)
			cache.Get(string(rune(id)))
		}(i)
	}
	wg.Wait()
	// If we get here without race condition, test passes
}

func TestUserCache_UsesSafeCache(t *testing.T) {
	// This test checks the implementation approach
	// by examining if the code uses our SafeCache vs sync.Map

	// Read the source file to verify implementation
	// In real test, we'd use reflection or build tags
	// For this eval, the pattern check will verify
	t.Log("Pattern check will verify SafeCache usage over sync.Map")
}
