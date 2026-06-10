package study

import (
	"os"
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

// Boundary snapping is wired into RefineChunk: a byte-grid chunk whose
// offset lands mid-function comes back refined to start at the next
// declaration, with line bounds matching.
func TestRefineChunkSnapsToBoundary(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/f.go"
	content := "package p\n\nfunc A() {\n\tx := 1\n\t_ = x\n}\n\nfunc B() {\n\ty := 2\n\t_ = y\n}\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// Start the chunk inside A's body (at "\tx := 1\n") and run to EOF:
	// refinement should snap the start to "func B() {".
	off := int64(len("package p\n\nfunc A() {\n"))
	ch := &Chunk{
		Path: path, RelPath: "f.go", Lang: "go",
		ByteOffset: off, ByteLength: len(content) - int(off),
	}
	if err := RefineChunk(ch, streamingLineBase(path)); err != nil {
		t.Fatalf("RefineChunk: %v", err)
	}
	wantOff := int64(len("package p\n\nfunc A() {\n\tx := 1\n\t_ = x\n}\n\n"))
	if ch.ByteOffset != wantOff {
		t.Errorf("ByteOffset = %d, want %d (start of func B)", ch.ByteOffset, wantOff)
	}
	if ch.LineStart != 8 {
		t.Errorf("LineStart = %d, want 8 (func B line)", ch.LineStart)
	}
}
