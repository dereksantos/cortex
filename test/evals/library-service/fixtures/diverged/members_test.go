//go:build ignore

package handlers

import "testing"

// Setup helper present, but no table-driven loop and uses t.Fatalf.
func setupTestMembers(t *testing.T) {
	t.Helper()
}

func TestMemberAll(t *testing.T) {
	setupTestMembers(t)
	if 1 == 0 {
		t.Fatalf("never")
	}
}

func TestMemberOne(t *testing.T) {
	setupTestMembers(t)
	if 2 == 0 {
		t.Fatalf("never")
	}
}
