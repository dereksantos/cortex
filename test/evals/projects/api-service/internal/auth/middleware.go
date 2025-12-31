package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

var (
	ErrMissingToken   = errors.New("missing authorization token")
	ErrInvalidToken   = errors.New("invalid token")
	ErrExpiredToken   = errors.New("token expired")
	ErrInvalidClaims  = errors.New("invalid claims")
)

// SecretKey is used for signing JWTs.
// In production, this should be loaded from environment/config.
var SecretKey = []byte("super-secret-key-change-in-production")

// JWTMiddleware validates JWT tokens in the Authorization header.
// Currently implements basic JWT validation.
// TODO: Consider migrating to OAuth2 for better security and standardization.
func JWTMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip auth for health check endpoints
		if r.URL.Path == "/health" || r.URL.Path == "/ready" {
			next.ServeHTTP(w, r)
			return
		}

		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, ErrMissingToken.Error(), http.StatusUnauthorized)
			return
		}

		// Expect "Bearer <token>"
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
			http.Error(w, ErrInvalidToken.Error(), http.StatusUnauthorized)
			return
		}

		token := parts[1]
		claims, err := ValidateJWT(token)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}

		// Add user to context
		user := &User{
			ID:    claims.UserID,
			Email: claims.Email,
			Role:  claims.Role,
		}
		ctx := context.WithValue(r.Context(), UserContextKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ValidateJWT validates a JWT token and returns the claims.
func ValidateJWT(tokenString string) (*Claims, error) {
	parts := strings.Split(tokenString, ".")
	if len(parts) != 3 {
		return nil, ErrInvalidToken
	}

	// Verify signature
	signatureInput := parts[0] + "." + parts[1]
	expectedSignature := signToken(signatureInput)
	if parts[2] != expectedSignature {
		return nil, ErrInvalidToken
	}

	// Decode payload
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrInvalidToken
	}

	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, ErrInvalidClaims
	}

	// Check expiration
	if time.Now().After(claims.ExpiresAt) {
		return nil, ErrExpiredToken
	}

	return &claims, nil
}

// CreateJWT creates a new JWT token with the given claims.
func CreateJWT(claims *Claims) (string, error) {
	// Header
	header := map[string]string{
		"alg": "HS256",
		"typ": "JWT",
	}
	headerJSON, _ := json.Marshal(header)
	headerEncoded := base64.RawURLEncoding.EncodeToString(headerJSON)

	// Payload
	payloadJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payloadEncoded := base64.RawURLEncoding.EncodeToString(payloadJSON)

	// Signature
	signatureInput := headerEncoded + "." + payloadEncoded
	signature := signToken(signatureInput)

	return headerEncoded + "." + payloadEncoded + "." + signature, nil
}

// signToken creates an HMAC-SHA256 signature for the token.
func signToken(input string) string {
	h := hmac.New(sha256.New, SecretKey)
	h.Write([]byte(input))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// GetUserFromContext retrieves the authenticated user from the context.
func GetUserFromContext(ctx context.Context) (*User, bool) {
	user, ok := ctx.Value(UserContextKey).(*User)
	return user, ok
}

// RequireRole middleware ensures the user has the required role.
func RequireRole(role string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := GetUserFromContext(r.Context())
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if user.Role != role && user.Role != "admin" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
