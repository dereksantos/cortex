package order

import (
	"bytes"
	"log/slog"
	"os"
	"strings"
	"testing"
)

func TestProcessOrder_Success(t *testing.T) {
	// Capture log output
	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, nil)
	logger := slog.New(handler)
	slog.SetDefault(logger)

	order := &Order{
		ID:       "ORD-123",
		Customer: "John",
		Items:    []string{"Widget"},
		Total:    99.99,
	}

	err := ProcessOrder(order)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := buf.String()
	// Should log success with Info level
	if !strings.Contains(output, "INFO") && !strings.Contains(output, "info") {
		t.Errorf("expected Info level log, got: %s", output)
	}
	// Should include operation name
	if !strings.Contains(output, "ProcessOrder") && !strings.Contains(output, "process") {
		t.Errorf("expected operation name in log, got: %s", output)
	}
}

func TestProcessOrder_Error(t *testing.T) {
	// Capture log output
	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, nil)
	logger := slog.New(handler)
	slog.SetDefault(logger)

	err := ProcessOrder(nil)
	if err == nil {
		t.Fatal("expected error for nil order")
	}

	output := buf.String()
	// Should log error with Error level
	if !strings.Contains(output, "ERROR") && !strings.Contains(output, "error") {
		t.Errorf("expected Error level log, got: %s", output)
	}
}

func TestMain(m *testing.M) {
	// Run tests
	os.Exit(m.Run())
}
