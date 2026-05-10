package mathx

import "testing"

func TestAbs(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{"positive", 5, 5},
		// TODO: add cases for negative input, zero, and a large negative
		// number. Match the existing struct shape and t.Run pattern.
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Abs(tc.in); got != tc.want {
				t.Errorf("Abs(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}
