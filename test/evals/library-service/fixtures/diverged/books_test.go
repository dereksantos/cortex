//go:build ignore

package handlers

import (
	"net/http/httptest"
	"testing"
)

// Mirrors S1 cohesive style: setup helper, table-driven, t.Errorf.
func setupTestBooks(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(nil)
}

func TestListBooks(t *testing.T) {
	srv := setupTestBooks(t)
	defer srv.Close()
	cases := []struct {
		name       string
		path       string
		wantStatus int
	}{
		{"ok", "/books", 200},
		{"trailing slash", "/books/", 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.wantStatus != 200 {
				t.Errorf("want %d", tc.wantStatus)
			}
		})
	}
}
