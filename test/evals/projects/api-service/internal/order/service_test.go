package order

import (
	"context"
	"testing"
)

func TestCreateOrder(t *testing.T) {
	svc := NewOrderService()
	ctx := context.Background()

	order := &Order{
		ID:         "ord-1",
		CustomerID: "cust-1",
		Items: []OrderItem{
			{ProductID: "prod-1", Quantity: 2, Price: 9.99},
		},
	}

	err := svc.CreateOrder(ctx, order)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify order was created
	retrieved, err := svc.GetOrder(ctx, "ord-1")
	if err != nil {
		t.Fatalf("failed to retrieve order: %v", err)
	}
	if retrieved.Status != StatusPending {
		t.Errorf("expected status pending, got %s", retrieved.Status)
	}
	if retrieved.Total != 19.98 {
		t.Errorf("expected total 19.98, got %v", retrieved.Total)
	}
}

func TestCreateOrder_Invalid(t *testing.T) {
	svc := NewOrderService()
	ctx := context.Background()

	tests := []struct {
		name  string
		order *Order
	}{
		{
			name:  "missing ID",
			order: &Order{CustomerID: "cust-1"},
		},
		{
			name:  "missing customer ID",
			order: &Order{ID: "ord-1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := svc.CreateOrder(ctx, tt.order)
			if err != ErrInvalidOrder {
				t.Errorf("expected ErrInvalidOrder, got %v", err)
			}
		})
	}
}

func TestCreateOrder_SagaPattern(t *testing.T) {
	// This test verifies saga pattern implementation.
	// It should FAIL until the saga pattern is implemented.
	//
	// The saga pattern ensures that a distributed transaction either
	// completes entirely or all partial changes are rolled back.
	// For order creation, this means:
	// 1. Reserve inventory (compensate: release)
	// 2. Process payment (compensate: refund)
	// 3. Send notification (compensate: send cancellation)

	svc := NewOrderService()
	ctx := context.Background()

	// Create an order that requires saga coordination
	order := &Order{
		ID:         "ord-saga-1",
		CustomerID: "cust-1",
		Items: []OrderItem{
			{ProductID: "prod-1", Quantity: 5, Price: 9.99},
		},
	}

	// For this test to pass, OrderService needs to:
	// 1. Accept inventory, payment, and notification service dependencies
	// 2. Implement saga pattern with compensating transactions
	// 3. Return saga state or track it on the order

	err := svc.CreateOrder(ctx, order)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify saga was executed - order should be confirmed (all steps passed)
	// or failed (some step failed and compensation ran)
	retrieved, _ := svc.GetOrder(ctx, "ord-saga-1")

	// A proper saga implementation should:
	// - Complete all steps and set status to Confirmed
	// - Or fail at some step and set status to Failed/Cancelled after compensation
	// The status should NOT remain Pending after saga completion
	if retrieved.Status == StatusPending {
		t.Errorf("saga not implemented: order status is still 'pending' after CreateOrder; "+
			"expected 'confirmed' (success) or 'failed' (compensation ran); got %s", retrieved.Status)
	}
}

func TestCreateOrder_SagaCompensation(t *testing.T) {
	// This test verifies that compensating transactions work correctly.
	// It should FAIL until saga pattern with compensation is implemented.
	//
	// Scenario: Payment fails after inventory is reserved
	// Expected: Inventory reservation should be rolled back

	// For this test to pass, OrderService needs dependency injection:
	// - InventoryService interface
	// - PaymentService interface
	// - NotificationService interface
	//
	// The test would then inject a mock PaymentService that fails,
	// and verify that InventoryService.Release() was called.

	svc := NewOrderService()

	// Check if the service supports dependency injection
	// by verifying it has the required fields/methods
	// This is a compile-time check via interface assertion

	// Define what a saga-enabled service should look like
	type SagaEnabledService interface {
		SetInventoryService(inv interface{})
		SetPaymentService(pay interface{})
		SetNotificationService(notif interface{})
	}

	// This will fail because OrderService doesn't implement dependency injection yet
	_, hasSagaSupport := interface{}(svc).(SagaEnabledService)
	if !hasSagaSupport {
		t.Errorf("saga compensation not implemented: OrderService does not support dependency injection; "+
			"expected methods: SetInventoryService, SetPaymentService, SetNotificationService")
	}
}

func TestGetOrder_NotFound(t *testing.T) {
	svc := NewOrderService()

	_, err := svc.GetOrder(context.Background(), "nonexistent")
	if err != ErrOrderNotFound {
		t.Errorf("expected ErrOrderNotFound, got %v", err)
	}
}

func TestUpdateOrderStatus(t *testing.T) {
	svc := NewOrderService()
	ctx := context.Background()

	// Create an order first
	order := &Order{
		ID:         "ord-1",
		CustomerID: "cust-1",
	}
	svc.CreateOrder(ctx, order)

	// Update status
	err := svc.UpdateOrderStatus(ctx, "ord-1", StatusConfirmed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify update
	retrieved, _ := svc.GetOrder(ctx, "ord-1")
	if retrieved.Status != StatusConfirmed {
		t.Errorf("expected status confirmed, got %s", retrieved.Status)
	}
}

func TestCancelOrder(t *testing.T) {
	svc := NewOrderService()
	ctx := context.Background()

	// Create an order
	order := &Order{
		ID:         "ord-cancel",
		CustomerID: "cust-1",
	}
	svc.CreateOrder(ctx, order)

	// Cancel it
	err := svc.CancelOrder(ctx, "ord-cancel")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify cancellation
	retrieved, _ := svc.GetOrder(ctx, "ord-cancel")
	if retrieved.Status != StatusCancelled {
		t.Errorf("expected status cancelled, got %s", retrieved.Status)
	}
}
