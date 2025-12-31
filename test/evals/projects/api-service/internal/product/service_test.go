package product

import (
	"context"
	"testing"
	"time"
)

func TestGetProduct(t *testing.T) {
	svc := NewProductService()

	product, err := svc.GetProduct(context.Background(), "prod-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if product.Name != "Widget" {
		t.Errorf("expected Widget, got %s", product.Name)
	}
	if product.Price != 9.99 {
		t.Errorf("expected price 9.99, got %v", product.Price)
	}
}

func TestGetProduct_NotFound(t *testing.T) {
	svc := NewProductService()

	_, err := svc.GetProduct(context.Background(), "nonexistent")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestGetProductCached(t *testing.T) {
	// This test verifies caching behavior.
	// It should FAIL until caching is implemented.
	svc := NewProductService()
	ctx := context.Background()

	// First call - cache miss, hits "database"
	start := time.Now()
	_, err := svc.GetProduct(ctx, "prod-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	firstDuration := time.Since(start)

	// Second call - should be cached, much faster
	start = time.Now()
	_, err = svc.GetProduct(ctx, "prod-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	secondDuration := time.Since(start)

	// Cached call should be at least 5x faster
	if secondDuration > firstDuration/5 {
		t.Errorf("second call not cached: first=%v, second=%v (expected second < %v)",
			firstDuration, secondDuration, firstDuration/5)
	}
}

func TestListProducts(t *testing.T) {
	svc := NewProductService()

	products, err := svc.ListProducts(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(products) != 3 {
		t.Errorf("expected 3 products, got %d", len(products))
	}
}

func TestUpdateInventory(t *testing.T) {
	svc := NewProductService()
	ctx := context.Background()

	// Get initial inventory
	product, _ := svc.GetProduct(ctx, "prod-1")
	initialInventory := product.Inventory

	// Update inventory
	err := svc.UpdateInventory(ctx, "prod-1", -10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify update
	product, _ = svc.GetProduct(ctx, "prod-1")
	if product.Inventory != initialInventory-10 {
		t.Errorf("expected inventory %d, got %d", initialInventory-10, product.Inventory)
	}
}
