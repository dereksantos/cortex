package models

import (
	"strings"
	"testing"
)

func TestNewOrder_HasID(t *testing.T) {
	order := NewOrder("cust-1", []string{"item1"}, 99.99)

	if order.ID == "" {
		t.Fatal("order should have an ID")
	}

	// ULID is 26 characters, uppercase alphanumeric
	if len(order.ID) != 26 {
		t.Errorf("expected ULID (26 chars), got %d chars: %s", len(order.ID), order.ID)
	}

	// ULID uses Crockford's base32 (uppercase, no I, L, O, U)
	for _, c := range order.ID {
		if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z')) {
			t.Errorf("ULID should be uppercase alphanumeric, got char: %c", c)
		}
	}
}

func TestNewOrder_UniqueIDs(t *testing.T) {
	order1 := NewOrder("cust-1", []string{"item1"}, 10.00)
	order2 := NewOrder("cust-1", []string{"item1"}, 10.00)

	if order1.ID == order2.ID {
		t.Error("each order should have a unique ID")
	}
}

func TestOrder_IDNotUUID(t *testing.T) {
	order := NewOrder("cust-1", []string{"item1"}, 99.99)

	// UUID has hyphens, ULID does not
	if strings.Contains(order.ID, "-") {
		t.Error("should use ULID (no hyphens), not UUID")
	}
}
