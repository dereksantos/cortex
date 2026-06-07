package llm

import "testing"

func TestLocalOnly(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"", false},
		{"0", false},
		{"false", false},
		{"1", true},
		{"true", true},
		{"yes", true},
		{"on", true},
	}
	for _, tc := range cases {
		t.Run(tc.val, func(t *testing.T) {
			t.Setenv(LocalOnlyEnv, tc.val)
			if got := LocalOnly(); got != tc.want {
				t.Errorf("LocalOnly() with %q = %v, want %v", tc.val, got, tc.want)
			}
		})
	}
}
