package study

import (
	"testing"
)

func TestUnitBytesFor(t *testing.T) {
	tests := []struct {
		lang string
		want int
	}{
		{"go", unitBytesCode},
		{"py", unitBytesCode},
		{"rs", unitBytesCode},
		{"md", unitBytesProse},
		{"txt", unitBytesProse},
		{"json", unitBytesData},
		{"yaml", unitBytesData},
		{"csv", unitBytesData},
		{"unknown", 0},
		{"", 0},
	}
	for _, tt := range tests {
		t.Run(tt.lang, func(t *testing.T) {
			if got := unitBytesFor(tt.lang); got != tt.want {
				t.Errorf("unitBytesFor(%q) = %d, want %d", tt.lang, got, tt.want)
			}
		})
	}
}

func TestSnapToBoundary(t *testing.T) {
	tests := []struct {
		name string
		body string
		lang string
		want int
	}{
		{
			name: "go: snaps to func start past a partial tail",
			body: "\treturn x\n}\n\nfunc Next() int {\n\treturn 1\n}\n",
			lang: "go",
			want: len("\treturn x\n}\n\n"),
		},
		{
			name: "go: already at a boundary — no snap",
			body: "func Head() {}\n\nfunc Next() {}\n",
			lang: "go",
			want: 0,
		},
		{
			name: "go: type decl is a boundary too",
			body: "\t}\n}\ntype Thing struct {\n\tA int\n}\n",
			lang: "go",
			want: len("\t}\n}\n"),
		},
		{
			name: "go: no boundary in slack — no snap",
			body: "\ta := 1\n\tb := 2\n\tc := 3\n\td := 4\n\te := 5\n\tf := 6\n",
			lang: "go",
			want: 0,
		},
		{
			name: "go: boundary past the first half is ignored",
			body: "\ta := 1\n\tb := 2\n\tc := 3\n\td := 4\n\te := 5\nfunc Late() {}\n",
			lang: "go",
			want: 0,
		},
		{
			name: "md: snaps to a heading",
			body: "tail of the previous section.\n\n## Next section\n\nBody text here.\n",
			lang: "md",
			want: len("tail of the previous section.\n\n"),
		},
		{
			name: "prose: paragraph rule — non-blank line after a blank line",
			body: "the previous paragraph trails off\n\nA new paragraph begins here and runs on.\nmore of it\n",
			lang: "txt",
			want: len("the previous paragraph trails off\n\n"),
		},
		{
			name: "prose: first line never treated as a paragraph start",
			body: "could be a paragraph start, predecessor unknown\nmore text\nand more text follows here\n",
			lang: "txt",
			want: 0,
		},
		{
			name: "json: record start at low indent",
			body: "      \"deep\": true},\n  {\"id\": 2, \"name\": \"b\"},\n  {\"id\": 3}\n",
			lang: "json",
			want: len("      \"deep\": true},\n"),
		},
		{
			name: "empty body",
			body: "",
			lang: "go",
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := snapToBoundary([]byte(tt.body), tt.lang); got != tt.want {
				t.Errorf("snapToBoundary = %d, want %d", got, tt.want)
			}
		})
	}
}
