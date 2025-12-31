package main

import (
	"context"
	"testing"
)

func TestGetUser_WithContext(t *testing.T) {
	svc := NewUserService()
	ctx := context.Background()

	// Test successful retrieval
	user, err := svc.GetUser(ctx, "user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if user.Name != "Alice" {
		t.Errorf("expected Alice, got %s", user.Name)
	}
}

func TestGetUser_NotFound(t *testing.T) {
	svc := NewUserService()
	ctx := context.Background()

	// Test not found returns error
	_, err := svc.GetUser(ctx, "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent user")
	}
}

func TestGetUser_ReturnsError(t *testing.T) {
	svc := NewUserService()
	ctx := context.Background()

	// Verify the function signature returns error
	user, err := svc.GetUser(ctx, "user-1")
	_ = user // use the variable
	_ = err  // verify err is returned (compile check)
}
