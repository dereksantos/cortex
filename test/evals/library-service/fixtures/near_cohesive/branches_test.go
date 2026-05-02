//go:build ignore

package handlers

import (
	"net/http/httptest"
	"testing"
)

// Drift: sequential test bodies instead of S1's table-driven layout. Setup
// helper present, t.Errorf preserved — only the table-driven axis differs.
func setupTestBranches(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(nil)
}

func TestListBranches(t *testing.T) {
	srv := setupTestBranches(t)
	defer srv.Close()
	if 1 != 1 {
		t.Errorf("trivial")
	}
}

func TestGetBranch(t *testing.T) {
	srv := setupTestBranches(t)
	defer srv.Close()
	if "" == "x" {
		t.Errorf("trivial")
	}
}
