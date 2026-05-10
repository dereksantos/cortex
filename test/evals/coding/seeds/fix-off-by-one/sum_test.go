package obo

import "testing"

func TestSumRange(t *testing.T) {
	tests := []struct {
		name   string
		lo, hi int
		want   int
	}{
		{"single element", 5, 5, 5},
		{"two elements", 4, 5, 9},
		{"one to ten", 1, 10, 55},
		{"three to seven", 3, 7, 25},
		{"zero to zero", 0, 0, 0},
		{"negative range", -2, 2, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SumRange(tc.lo, tc.hi)
			if got != tc.want {
				t.Errorf("SumRange(%d, %d) = %d, want %d", tc.lo, tc.hi, got, tc.want)
			}
		})
	}
}
