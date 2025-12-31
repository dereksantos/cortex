package order

import (
	"context"
	"errors"
	"time"
)

var (
	ErrOrderNotFound     = errors.New("order not found")
	ErrInvalidOrder      = errors.New("invalid order")
	ErrInsufficientStock = errors.New("insufficient stock")
	ErrPaymentFailed     = errors.New("payment failed")
)

// OrderService handles order operations.
// TODO: Implement saga pattern for distributed transactions.
// Order creation should:
// 1. Reserve inventory (compensate: release inventory)
// 2. Process payment (compensate: refund payment)
// 3. Send notification (compensate: send cancellation notice)
// If any step fails, execute compensating transactions in reverse order.
type OrderService struct {
	orders map[string]*Order
	// inventoryService would be injected in production
	// paymentService would be injected in production
	// notificationService would be injected in production
}

// NewOrderService creates a new OrderService.
func NewOrderService() *OrderService {
	return &OrderService{
		orders: make(map[string]*Order),
	}
}

// CreateOrder creates a new order.
// Currently a basic implementation without saga pattern.
// TODO: Implement saga pattern with compensating transactions.
func (s *OrderService) CreateOrder(ctx context.Context, order *Order) error {
	if order.ID == "" {
		return ErrInvalidOrder
	}
	if order.CustomerID == "" {
		return ErrInvalidOrder
	}

	// Calculate total
	var total float64
	for _, item := range order.Items {
		total += item.Price * float64(item.Quantity)
	}
	order.Total = total

	// Set timestamps and status
	now := time.Now()
	order.CreatedAt = now
	order.UpdatedAt = now
	order.Status = StatusPending

	// Store order
	s.orders[order.ID] = order

	// TODO: This is where saga pattern should be implemented:
	// Step 1: Reserve inventory for all items
	// Step 2: Process payment
	// Step 3: Send order confirmation notification
	// If any step fails, compensate previous steps in reverse order

	return nil
}

// GetOrder retrieves an order by ID.
func (s *OrderService) GetOrder(ctx context.Context, id string) (*Order, error) {
	order, ok := s.orders[id]
	if !ok {
		return nil, ErrOrderNotFound
	}
	return order, nil
}

// UpdateOrderStatus updates the status of an order.
func (s *OrderService) UpdateOrderStatus(ctx context.Context, id string, status OrderStatus) error {
	order, ok := s.orders[id]
	if !ok {
		return ErrOrderNotFound
	}
	order.Status = status
	order.UpdatedAt = time.Now()
	return nil
}

// CancelOrder cancels an order.
// TODO: Should trigger compensating transactions if saga pattern is implemented.
func (s *OrderService) CancelOrder(ctx context.Context, id string) error {
	order, ok := s.orders[id]
	if !ok {
		return ErrOrderNotFound
	}
	if order.Status == StatusShipped || order.Status == StatusDelivered {
		return errors.New("cannot cancel shipped or delivered orders")
	}
	order.Status = StatusCancelled
	order.UpdatedAt = time.Now()
	return nil
}
