package product

import (
	"context"
	"errors"
	"time"
)

var ErrNotFound = errors.New("product not found")

// ProductService handles product operations.
// TODO: Add caching using pkg/cache.Cache to improve performance.
// The cache interface is already provided in pkg/cache/redis.go.
type ProductService struct {
	// db would be a real database in production
	products map[string]*Product
	// cache should be added for GetProduct
}

// NewProductService creates a new ProductService with mock data.
func NewProductService() *ProductService {
	return &ProductService{
		products: map[string]*Product{
			"prod-1": {ID: "prod-1", Name: "Widget", Description: "A useful widget", Price: 9.99, Inventory: 100},
			"prod-2": {ID: "prod-2", Name: "Gadget", Description: "A fancy gadget", Price: 19.99, Inventory: 50},
			"prod-3": {ID: "prod-3", Name: "Gizmo", Description: "An essential gizmo", Price: 14.99, Inventory: 75},
		},
	}
}

// GetProduct retrieves a product by ID.
// Performance note: This hits the "database" every time.
// Consider adding caching for frequently accessed products.
func (s *ProductService) GetProduct(ctx context.Context, id string) (*Product, error) {
	// Simulate database latency
	time.Sleep(10 * time.Millisecond)

	product, ok := s.products[id]
	if !ok {
		return nil, ErrNotFound
	}
	return product, nil
}

// ListProducts returns all products.
func (s *ProductService) ListProducts(ctx context.Context) ([]*Product, error) {
	// Simulate database latency
	time.Sleep(20 * time.Millisecond)

	products := make([]*Product, 0, len(s.products))
	for _, p := range s.products {
		products = append(products, p)
	}
	return products, nil
}

// UpdateInventory updates the inventory count for a product.
func (s *ProductService) UpdateInventory(ctx context.Context, id string, delta int) error {
	product, ok := s.products[id]
	if !ok {
		return ErrNotFound
	}
	product.Inventory += delta
	if product.Inventory < 0 {
		product.Inventory = 0
	}
	return nil
}
