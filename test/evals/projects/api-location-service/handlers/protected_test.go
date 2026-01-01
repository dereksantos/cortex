package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProtectedHandler_NoToken(t *testing.T) {
	req := httptest.NewRequest("GET", "/protected", nil)
	rec := httptest.NewRecorder()

	ProtectedHandler(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without token, got %d", rec.Code)
	}
}

func TestProtectedHandler_ValidToken(t *testing.T) {
	req := httptest.NewRequest("GET", "/protected", nil)
	req.Header.Set("Authorization", "Bearer valid-token")
	rec := httptest.NewRecorder()

	ProtectedHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 with valid token, got %d", rec.Code)
	}
}
