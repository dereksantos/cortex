package study

import "testing"

func TestResolveDensity_Mapping(t *testing.T) {
	cases := []struct {
		name string
		in   Density
		want int
	}{
		{"sparse", "sparse", 4},
		{"normal", "normal", 8},
		{"dense", "dense", 16},
		{"mixed-case", "SPARSE", 4},
		{"trimmed", "  dense  ", 16},
		{"empty-string", "", 8},
		{"unknown-string", "bogus", 8},
		{"nil", nil, 8},
		{"raw-int", 12, 12},
		{"zero-int", 0, 8},
		{"negative-int", -3, 8},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ResolveDensity(c.in); got != c.want {
				t.Errorf("ResolveDensity(%v) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}
