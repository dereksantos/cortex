package order

import (
	"errors"
	"time"
)

// Order represents a customer order
type Order struct {
	ID       string
	Customer string
	Items    []string
	Total    float64
}

// ProcessOrder validates and processes an order.
// It should have logging but currently has none.
func ProcessOrder(o *Order) error {
	if o == nil {
		return errors.New("order cannot be nil")
	}

	if o.ID == "" {
		return errors.New("order ID required")
	}

	if len(o.Items) == 0 {
		return errors.New("order must have items")
	}

	// Simulate processing time
	time.Sleep(10 * time.Millisecond)

	return nil
}
