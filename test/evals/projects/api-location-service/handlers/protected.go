package handlers

import (
	"net/http"
)

// ProtectedHandler handles requests to protected resources.
// TODO: Add authentication check before processing.
func ProtectedHandler(w http.ResponseWriter, r *http.Request) {
	// TODO: Validate auth token from request header
	// TODO: Return 401 if invalid

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Protected resource"))
}
