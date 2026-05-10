// Package renametag exposes a User struct whose JSON tag for the
// display name needs to migrate from old_name → display_name.
package renametag

// User represents an account in the system. The display_name field is
// shown in the UI; legacy clients still send it as old_name in some
// payloads, but new clients have moved on.
type User struct {
	ID          int    `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"old_name"`
}
