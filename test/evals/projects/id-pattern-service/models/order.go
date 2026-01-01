package models

import "time"

// Order represents a customer order.
// TODO: Add a unique ID field.
type Order struct {
	// ID field needed here
	CustomerID string
	Items      []string
	Total      float64
	CreatedAt  time.Time
}

// NewOrder creates a new order.
// TODO: Generate unique ID.
func NewOrder(customerID string, items []string, total float64) *Order {
	return &Order{
		CustomerID: customerID,
		Items:      items,
		Total:      total,
		CreatedAt:  time.Now(),
	}
}
