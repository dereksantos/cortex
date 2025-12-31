package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestJWTMiddleware_ValidToken(t *testing.T) {
	// Create a valid token
	claims := &Claims{
		UserID:    "user-1",
		Email:     "test@example.com",
		Role:      "user",
		ExpiresAt: time.Now().Add(time.Hour),
		IssuedAt:  time.Now(),
	}
	token, err := CreateJWT(claims)
	if err != nil {
		t.Fatalf("failed to create token: %v", err)
	}

	// Create a test handler
	handler := JWTMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := GetUserFromContext(r.Context())
		if !ok {
			t.Error("user not found in context")
			return
		}
		if user.ID != "user-1" {
			t.Errorf("expected user-1, got %s", user.ID)
		}
		w.WriteHeader(http.StatusOK)
	}))

	// Make request with valid token
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
}

func TestJWTMiddleware_MissingToken(t *testing.T) {
	handler := JWTMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", rec.Code)
	}
}

func TestJWTMiddleware_InvalidToken(t *testing.T) {
	handler := JWTMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer invalid.token.here")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", rec.Code)
	}
}

func TestJWTMiddleware_ExpiredToken(t *testing.T) {
	// Create an expired token
	claims := &Claims{
		UserID:    "user-1",
		Email:     "test@example.com",
		Role:      "user",
		ExpiresAt: time.Now().Add(-time.Hour), // Expired
		IssuedAt:  time.Now().Add(-2 * time.Hour),
	}
	token, _ := CreateJWT(claims)

	handler := JWTMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", rec.Code)
	}
}

func TestJWTMiddleware_HealthEndpointSkipsAuth(t *testing.T) {
	handler := JWTMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Health endpoint should not require auth
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200 for health endpoint, got %d", rec.Code)
	}
}

func TestCreateJWT(t *testing.T) {
	claims := &Claims{
		UserID:    "user-123",
		Email:     "test@example.com",
		Role:      "admin",
		ExpiresAt: time.Now().Add(time.Hour),
		IssuedAt:  time.Now(),
	}

	token, err := CreateJWT(claims)
	if err != nil {
		t.Fatalf("failed to create token: %v", err)
	}

	// Validate the token
	validatedClaims, err := ValidateJWT(token)
	if err != nil {
		t.Fatalf("failed to validate token: %v", err)
	}

	if validatedClaims.UserID != claims.UserID {
		t.Errorf("expected user ID %s, got %s", claims.UserID, validatedClaims.UserID)
	}
	if validatedClaims.Email != claims.Email {
		t.Errorf("expected email %s, got %s", claims.Email, validatedClaims.Email)
	}
	if validatedClaims.Role != claims.Role {
		t.Errorf("expected role %s, got %s", claims.Role, validatedClaims.Role)
	}
}

func TestRequireRole(t *testing.T) {
	tests := []struct {
		name           string
		requiredRole   string
		userRole       string
		expectedStatus int
	}{
		{
			name:           "matching role",
			requiredRole:   "editor",
			userRole:       "editor",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "admin bypasses role check",
			requiredRole:   "editor",
			userRole:       "admin",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "insufficient role",
			requiredRole:   "admin",
			userRole:       "user",
			expectedStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create token with the test role
			claims := &Claims{
				UserID:    "user-1",
				Email:     "test@example.com",
				Role:      tt.userRole,
				ExpiresAt: time.Now().Add(time.Hour),
				IssuedAt:  time.Now(),
			}
			token, _ := CreateJWT(claims)

			// Chain JWTMiddleware -> RequireRole -> handler
			innerHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			roleHandler := RequireRole(tt.requiredRole, innerHandler)
			handler := JWTMiddleware(roleHandler)

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, rec.Code)
			}
		})
	}
}
