package eval

import (
	"testing"
)

func TestTestParity_FullMatch(t *testing.T) {
	s1 := `package h

import "testing"

func setupTest(t *testing.T) {}

func TestList(t *testing.T) {
	cases := []struct{ name string }{{"a"}, {"b"}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "" {
				t.Errorf("empty")
			}
		})
	}
}
`
	s2 := `package h

import "testing"

func setupTest(t *testing.T) {}

func TestList(t *testing.T) {
	cases := []struct{ name string }{{"x"}, {"y"}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "" {
				t.Errorf("empty")
			}
		})
	}
}
`
	_, paths := writeFixtureFiles(t,
		[]string{"books_test.go", "authors_test.go"},
		[]string{s1, s2},
	)
	got, err := testParity(paths[0], paths[1:])
	if err != nil {
		t.Fatalf("testParity: %v", err)
	}
	if got != 1.0 {
		t.Errorf("matching shapes should score 1.0, got %.3f", got)
	}
}

func TestTestParity_DivergentShape(t *testing.T) {
	s1 := `package h

import "testing"

func setupTest(t *testing.T) {}

func TestList(t *testing.T) {
	cases := []struct{ name string }{{"a"}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "" {
				t.Errorf("empty")
			}
		})
	}
}
`
	// No setup helper, no table loop, uses t.Fatalf instead of t.Errorf.
	s2 := `package h

import "testing"

func TestList(t *testing.T) {
	if true {
		t.Fatalf("oops")
	}
}
`
	_, paths := writeFixtureFiles(t,
		[]string{"books_test.go", "authors_test.go"},
		[]string{s1, s2},
	)
	got, err := testParity(paths[0], paths[1:])
	if err != nil {
		t.Fatalf("testParity: %v", err)
	}
	if got > 0.34 {
		t.Errorf("fully divergent shape should score ≤ 0.34, got %.3f", got)
	}
}

func TestTestParity_PartialMatch(t *testing.T) {
	s1 := `package h

import "testing"

func setupTest(t *testing.T) {}

func TestList(t *testing.T) {
	cases := []struct{ name string }{{"a"}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Errorf("x")
		})
	}
}
`
	// Same setup + same dominant assertion, but no table-driven.
	s2 := `package h

import "testing"

func setupTest(t *testing.T) {}

func TestList(t *testing.T) {
	t.Errorf("x")
}
`
	_, paths := writeFixtureFiles(t,
		[]string{"books_test.go", "authors_test.go"},
		[]string{s1, s2},
	)
	got, err := testParity(paths[0], paths[1:])
	if err != nil {
		t.Fatalf("testParity: %v", err)
	}
	want := 2.0 / 3.0
	if diff := got - want; diff > 0.01 || diff < -0.01 {
		t.Errorf("partial match should score %.3f, got %.3f", want, got)
	}
}

func TestDominantAssertion(t *testing.T) {
	tests := []struct {
		name  string
		calls map[string]int
		want  string
	}{
		{"errorf wins", map[string]int{"t.Errorf": 5, "t.Fatalf": 1}, "t.Errorf"},
		{"fatal wins", map[string]int{"t.Errorf": 1, "t.Fatalf": 3}, "t.Fatalf"},
		{"empty", map[string]int{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dominantAssertion(tt.calls)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
