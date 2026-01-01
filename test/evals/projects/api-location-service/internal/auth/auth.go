package auth

import "context"

// User represents an authenticated user.
type User struct {
	ID   string
	Role string
}

// Validate checks if the token is valid and returns the user.
func Validate(ctx context.Context, token string) (*User, error) {
	// Mock validation - in real code this would check JWT/session
	if token == "valid-token" {
		return &User{ID: "user-123", Role: "admin"}, nil
	}
	return nil, ErrInvalidToken
}

// ErrInvalidToken is returned when token validation fails.
var ErrInvalidToken = errorString("invalid token")

type errorString string

func (e errorString) Error() string { return string(e) }
