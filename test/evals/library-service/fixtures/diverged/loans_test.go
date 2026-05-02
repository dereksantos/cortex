//go:build ignore

package handlers

import "testing"

// No setup helper. No table-driven loop. Sequential with t.Errorf.
func TestLoansList(t *testing.T) {
	if 1 == 0 {
		t.Errorf("never")
	}
}

func TestLoanRead(t *testing.T) {
	if 2 == 0 {
		t.Errorf("never")
	}
}
