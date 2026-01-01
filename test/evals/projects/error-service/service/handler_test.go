package service

import (
	"strings"
	"testing"
)

func TestReadConfig_ErrorWrapping(t *testing.T) {
	_, err := ReadConfig("/nonexistent/path")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}

	errStr := err.Error()
	// Should use pkg/errors.Wrap which includes context
	if !strings.Contains(errStr, "ReadConfig") && !strings.Contains(errStr, "reading config") {
		t.Errorf("error should include context about ReadConfig, got: %v", err)
	}
}

func TestProcessData_EmptyError(t *testing.T) {
	err := ProcessData(nil)
	if err == nil {
		t.Fatal("expected error for nil data")
	}

	errStr := err.Error()
	if !strings.Contains(errStr, "process") && !strings.Contains(errStr, "empty") {
		t.Errorf("error should describe the problem, got: %v", err)
	}
}
