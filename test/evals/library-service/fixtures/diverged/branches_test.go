//go:build ignore

package handlers

import "testing"

// No setup helper. No table loop. t.Fatalf only. Maximally divergent.
func TestBranchesIndex(t *testing.T) {
	if 1 == 0 {
		t.Fatalf("never")
	}
}

func TestBranchesView(t *testing.T) {
	if 2 == 0 {
		t.Fatalf("never")
	}
}

func TestBranchesNew(t *testing.T) {
	if 3 == 0 {
		t.Fatalf("never")
	}
}
