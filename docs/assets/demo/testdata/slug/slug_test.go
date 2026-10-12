package slug

import "testing"

func TestMake(t *testing.T) {
	tests := []struct{ in, want string }{
		{"hello world", "hello-world"},
		{"Hello, World!", "hello-world"},
		{"  Go 1.26 is out  ", "go-1-26-is-out"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := Make(tt.in); got != tt.want {
				t.Errorf("Make(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
