package fizzbuzz

import "testing"

func TestFizzBuzz(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want string
	}{
		{"one", 1, "1"},
		{"two", 2, "2"},
		{"three is fizz", 3, "Fizz"},
		{"four", 4, "4"},
		{"five is buzz", 5, "Buzz"},
		{"six is fizz", 6, "Fizz"},
		{"ten is buzz", 10, "Buzz"},
		{"fifteen is fizzbuzz", 15, "FizzBuzz"},
		{"thirty is fizzbuzz", 30, "FizzBuzz"},
		{"large coprime", 31, "31"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := FizzBuzz(tc.in)
			if got != tc.want {
				t.Errorf("FizzBuzz(%d) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
