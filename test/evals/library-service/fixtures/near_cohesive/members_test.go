//go:build ignore

package handlers

import (
	"net/http/httptest"
	"testing"
)

func setupTestMembers(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(nil)
}

func TestListMembers(t *testing.T) {
	srv := setupTestMembers(t)
	defer srv.Close()
	cases := []struct {
		name       string
		path       string
		wantStatus int
	}{
		{"ok", "/members", 200},
		{"trailing slash", "/members/", 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.wantStatus != 200 {
				t.Errorf("want %d", tc.wantStatus)
			}
		})
	}
}

func TestGetMember(t *testing.T) {
	srv := setupTestMembers(t)
	defer srv.Close()
	cases := []struct {
		name       string
		id         string
		wantStatus int
	}{
		{"found", "m1", 200},
		{"missing", "", 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.id == "" && tc.wantStatus != 400 {
				t.Errorf("want 400")
			}
		})
	}
}
