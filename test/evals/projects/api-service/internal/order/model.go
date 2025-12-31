package order

import "time"

// OrderStatus represents the status of an order.
type OrderStatus string

const (
	StatusPending    OrderStatus = "pending"
	StatusConfirmed  OrderStatus = "confirmed"
	StatusProcessing OrderStatus = "processing"
	StatusShipped    OrderStatus = "shipped"
	StatusDelivered  OrderStatus = "delivered"
	StatusCancelled  OrderStatus = "cancelled"
	StatusFailed     OrderStatus = "failed"
)

// Order represents a customer order.
type Order struct {
	ID         string      `json:"id"`
	CustomerID string      `json:"customer_id"`
	Items      []OrderItem `json:"items"`
	Total      float64     `json:"total"`
	Status     OrderStatus `json:"status"`
	CreatedAt  time.Time   `json:"created_at"`
	UpdatedAt  time.Time   `json:"updated_at"`
}

// OrderItem represents an item in an order.
type OrderItem struct {
	ProductID string  `json:"product_id"`
	Quantity  int     `json:"quantity"`
	Price     float64 `json:"price"`
}

// SagaState tracks the state of a distributed transaction.
// Used for implementing the saga pattern in order processing.
type SagaState struct {
	OrderID          string
	InventoryReserved bool
	PaymentProcessed  bool
	NotificationSent  bool
}
