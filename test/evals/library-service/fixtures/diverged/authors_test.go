//go:build ignore

package handlers

import "testing"

// No setup helper. Table-driven but with t.Fatalf instead of t.Errorf.
func TestHandleAuthorIndex(t *testing.T) {
	cases := []struct {
		name string
		want int
	}{
		{"ok", 200},
		{"err", 500},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.want == 0 {
				t.Fatalf("want non-zero")
			}
		})
	}
}
