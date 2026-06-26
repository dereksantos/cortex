package storage

import (
	"os"
	"testing"

	"github.com/dereksantos/cortex/pkg/config"
)

// TestRetract_PersistsAcrossReopen verifies a retraction is durable: after
// Retract, IsRetracted is true, and it stays true when the store is reopened
// (rebuilt from feedback.jsonl).
func TestRetract_PersistsAcrossReopen(t *testing.T) {
	dir, err := os.MkdirTemp("", "cortex-retract-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	cfg := &config.Config{ContextDir: dir}

	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if s.IsRetracted("event-x") {
		t.Fatal("event-x retracted before any retraction")
	}
	if err := s.Retract("event-x", "superseded by event-y"); err != nil {
		t.Fatalf("Retract: %v", err)
	}
	if !s.IsRetracted("event-x") {
		t.Fatal("event-x not retracted after Retract")
	}
	s.Close()

	// Reopen: retraction must survive (rebuilt from feedback.jsonl).
	s2, err := New(cfg)
	if err != nil {
		t.Fatalf("reopen New: %v", err)
	}
	defer s2.Close()
	if !s2.IsRetracted("event-x") {
		t.Error("retraction did not persist across reopen")
	}
}
