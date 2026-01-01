package middleware

import (
	"net/http"
)

// AuthMiddleware wraps handlers to require authentication.
// TODO: Implement JWT token validation
func AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// TODO: Implement authentication check
		next.ServeHTTP(w, r)
	})
}
