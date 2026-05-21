package bootstrap

import "testing"

func TestEffectiveLines_Go(t *testing.T) {
	src := `// header comment
package main

import "fmt"

/* multi-line
   block comment
   continued */
func main() {
	fmt.Println("hi")  // trailing comment is part of the line
}

/* one-line */
var x = 1
`
	got := effectiveLinesOf([]byte(src), "go")
	// Expected effective (non-blank, non-comment) lines:
	//   package main
	//   import "fmt"
	//   func main() {
	//   fmt.Println("hi")
	//   }
	//   var x = 1
	want := 6
	if got != want {
		t.Errorf("Go effective = %d, want %d", got, want)
	}
}

func TestEffectiveLines_Python(t *testing.T) {
	src := `# top comment
import os

def hello():
    # nested comment
    print("hi")

x = 1  # trailing comment counts as a code line (only line-leading comments are excluded)
`
	got := effectiveLinesOf([]byte(src), "py")
	// import os
	// def hello():
	// print("hi")
	// x = 1
	want := 4
	if got != want {
		t.Errorf("Python effective = %d, want %d", got, want)
	}
}

func TestEffectiveLines_Markdown(t *testing.T) {
	src := `# Heading

Some prose here.

- bullet one
- bullet two

> blockquote
`
	got := effectiveLinesOf([]byte(src), "md")
	// Markdown has no comment filter: every non-blank line counts.
	// Lines: "# Heading", "Some prose here.", "- bullet one",
	//        "- bullet two", "> blockquote" → 5
	want := 5
	if got != want {
		t.Errorf("Markdown effective = %d, want %d", got, want)
	}
}

func TestEffectiveLines_Unknown(t *testing.T) {
	src := "alpha\nbravo\n\ncharlie\n"
	got := effectiveLinesOf([]byte(src), "unknown")
	want := 3 // non-blank-only fallback
	if got != want {
		t.Errorf("Unknown effective = %d, want %d", got, want)
	}
}

func TestEffectiveLines_Lua(t *testing.T) {
	src := `-- top comment
local x = 1
-- another
print(x)
`
	got := effectiveLinesOf([]byte(src), "lua")
	want := 2
	if got != want {
		t.Errorf("Lua effective = %d, want %d", got, want)
	}
}

func TestEffectiveLines_Empty(t *testing.T) {
	if got := effectiveLinesOf([]byte{}, "go"); got != 0 {
		t.Errorf("empty go = %d, want 0", got)
	}
	if got := effectiveLinesOf([]byte{}, "md"); got != 0 {
		t.Errorf("empty md = %d, want 0", got)
	}
}

func TestEffectiveLines_BlockCommentSpanning(t *testing.T) {
	src := `package x
/*
all
this
is
comment
*/
var y = 2
`
	got := effectiveLinesOf([]byte(src), "go")
	// package x; var y = 2 → 2
	want := 2
	if got != want {
		t.Errorf("block comment go = %d, want %d", got, want)
	}
}

func TestLangFor(t *testing.T) {
	cases := []struct {
		ext  string
		want string
	}{
		{".go", "go"},
		{".PY", "py"},
		{".tsx", "ts"},
		{".cc", "c"},
		{".md", "md"},
		{".unknown_ext", "unknown"},
		{"", "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.ext, func(t *testing.T) {
			if got := langFor(tc.ext); got != tc.want {
				t.Errorf("langFor(%q) = %q, want %q", tc.ext, got, tc.want)
			}
		})
	}
}
