package main

import "fmt"

// User represents a user in the system.
type User struct {
	ID    string
	Name  string
	Email string
}

// UserService handles user operations.
type UserService struct {
	users map[string]*User
}

// NewUserService creates a new UserService.
func NewUserService() *UserService {
	return &UserService{
		users: map[string]*User{
			"user-1": {ID: "user-1", Name: "Alice", Email: "alice@example.com"},
			"user-2": {ID: "user-2", Name: "Bob", Email: "bob@example.com"},
		},
	}
}

// GetUser retrieves a user by ID.
// TODO: This function needs to be updated to:
// 1. Accept context.Context as the first parameter
// 2. Return (*User, error) instead of just User
// 3. Return an error if user not found instead of empty User
func (s *UserService) GetUser(id string) User {
	user, ok := s.users[id]
	if !ok {
		return User{} // Bad: returns empty user, should return error
	}
	return *user
}

func main() {
	svc := NewUserService()
	user := svc.GetUser("user-1")
	fmt.Printf("User: %+v\n", user)
}
