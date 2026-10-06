package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Minimal valid-enough fixture bodies: real magic bytes for each format so
// the sniff and the extension agree, plus filler so sizes are controllable.
func pngBytes(n int) []byte {
	head := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	return append(head, bytes.Repeat([]byte("x"), n)...)
}

func gifBytes() []byte { return append([]byte("GIF89a"), []byte("filler")...) }

func webpBytes() []byte {
	b := append([]byte("RIFF"), 0, 0, 0, 0)
	return append(b, []byte("WEBPVP8 ")...)
}

func jpegBytes() []byte { return append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, []byte("filler")...) }

func TestDetectImage(t *testing.T) {
	tests := []struct {
		name         string
		path         string
		data         []byte
		wantType     string
		wantDetected bool
	}{
		{"png by extension and magic", "shot.png", pngBytes(8), "image/png", true},
		{"jpeg upper-case extension", "DSC.JPG", jpegBytes(), "image/jpeg", true},
		{"gif magic", "anim.gif", gifBytes(), "image/gif", true},
		{"webp magic", "still.webp", webpBytes(), "image/webp", true},
		{"extension only (no magic) still an image", "lying.png", []byte("not really png"), "image/png", true},
		{"magic wins over a lying extension", "mislabeled.txt", pngBytes(4), "image/png", true},
		{"text file is not an image", "main.go", []byte("package main"), "", false},
		{"empty file", "empty.png", nil, "image/png", true}, // extension signal alone
		{"unknown extension, no magic", "notes.md", []byte("# hi"), "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mt, ok := detectImage(tt.path, tt.data)
			if ok != tt.wantDetected {
				t.Fatalf("detectImage(%q) detected=%v, want %v", tt.path, ok, tt.wantDetected)
			}
			if mt != tt.wantType {
				t.Errorf("media type = %q, want %q", mt, tt.wantType)
			}
		})
	}
}

// writeImage drops an image fixture in a temp dir and returns its path.
func writeImage(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", name, err)
	}
	return p
}

func readImageCall(t *testing.T, path string, start, end int) ToolCall {
	t.Helper()
	_ = start
	_ = end
	return ToolCall{ID: "c1", Type: "function", Function: FunctionCall{
		Name:      FunctionReadFile,
		Arguments: `{"path":"` + path + `"}`,
	}}
}

// textOnlyDeps answers the ImageGate with a fixed verdict.
type textOnlyDeps struct {
	headlessDeps
	accept bool
}

func (d textOnlyDeps) ImageInputEnabled() bool { return d.accept }

func TestReadFileImageVisionAttachesPart(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(ResetLimits)
	ResetLimits()
	p := writeImage(t, dir, "shot.png", pngBytes(300))
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	out, err := readFile(readImageCall(t, p, 0, 0), textOnlyDeps{accept: true})
	if err != nil {
		t.Fatalf("read_file image: %v", err)
	}
	if !IsImageObservation(out) {
		t.Fatalf("observation %q is not the image marker", out)
	}
	if !strings.Contains(out, "shot.png") || !strings.Contains(out, "image/png") {
		t.Errorf("observation must name the path and type: %q", out)
	}
	part, ok := TakeImageObservation(out)
	if !ok {
		t.Fatal("TakeImageObservation found no attachment after an image read")
	}
	if part.MediaType != "image/png" || part.Path != p {
		t.Errorf("part = %+v", part)
	}
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
	if part.DataURI != want {
		t.Errorf("data URI mismatch: got %d bytes, want %d", len(part.DataURI), len(want))
	}

	t.Run("attachment is consumed once", func(t *testing.T) {
		if _, ok := TakeImageObservation(out); ok {
			t.Error("a second Take must not re-splice the same image")
		}
	})
}

func TestReadFileImageTextOnlyRefusal(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(ResetLimits)
	ResetLimits()
	tests := []struct {
		name string
		file string
		data []byte
		want string // the file type the refusal must name
	}{
		{"png", "shot.png", pngBytes(64), "image/png"},
		{"jpeg", "photo.jpeg", jpegBytes(), "image/jpeg"},
		{"gif", "anim.gif", gifBytes(), "image/gif"},
		{"webp", "still.webp", webpBytes(), "image/webp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := writeImage(t, dir, tt.file, tt.data)
			out, err := readFile(readImageCall(t, p, 0, 0), textOnlyDeps{accept: false})
			if err != nil {
				t.Fatalf("refusal must be an observation, not an error: %v", err)
			}
			if IsImageObservation(out) {
				t.Errorf("a text-only refusal must not carry the image marker: %q", out)
			}
			if !strings.Contains(out, tt.want) {
				t.Errorf("refusal must name the file type %q: %q", tt.want, out)
			}
			if _, ok := TakeImageObservation(out); ok {
				t.Error("a refusal attaches nothing")
			}
		})
	}

	// A deps that doesn't answer the gate (subagents, headless) takes the
	// image: their requests carry no image parts, and the wire gate
	// (pkg/llm.GateImages) backstops anything that ever would.
	t.Run("deps without the gate attaches", func(t *testing.T) {
		p := writeImage(t, dir, "sub.png", pngBytes(32))
		out, err := readFile(readImageCall(t, p, 0, 0), headlessDeps{})
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if !IsImageObservation(out) {
			t.Errorf("ungated deps should get the image marker, got %q", out)
		}
	})
}

func TestReadFileImageSizeCapRefusal(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(ResetLimits)
	Configure(Limits{ImageMaxBytes: 128})
	p := writeImage(t, dir, "big.png", pngBytes(4096))

	out, err := readFile(readImageCall(t, p, 0, 0), textOnlyDeps{accept: true})
	if err != nil {
		t.Fatalf("cap refusal must be an observation, not an error: %v", err)
	}
	if IsImageObservation(out) {
		t.Errorf("over-cap must attach nothing: %q", out)
	}
	if !strings.Contains(out, "image/png") || !strings.Contains(out, "128") {
		t.Errorf("cap refusal must name the type and the cap: %q", out)
	}
	if _, ok := TakeImageObservation(out); ok {
		t.Error("over-cap leaves no attachment")
	}
}

func TestReadFileImageRangedRefused(t *testing.T) {
	dir := t.TempDir()
	p := writeImage(t, dir, "shot.png", pngBytes(256))
	tc := ToolCall{ID: "c1", Type: "function", Function: FunctionCall{
		Name:      FunctionReadFile,
		Arguments: `{"path":"` + p + `","start":1,"end":5}`,
	}}
	out, err := readFile(tc, textOnlyDeps{accept: true})
	if err != nil {
		t.Fatalf("ranged refusal must be an observation: %v", err)
	}
	if IsImageObservation(out) {
		t.Errorf("a ranged image read attaches nothing: %q", out)
	}
	if !strings.Contains(out, "image/png") || !strings.Contains(out, "range") {
		t.Errorf("ranged refusal must name the type and why: %q", out)
	}
}

func TestTakeImageObservationStaleCleared(t *testing.T) {
	t.Cleanup(ResetLimits)
	ResetLimits()
	dir := t.TempDir()
	p := writeImage(t, dir, "x.png", pngBytes(16))
	ctx := context.Background()

	// Produce an attachment via the real dispatch path...
	out, err := Execute(ctx, readImageCall(t, p, 0, 0), textOnlyDeps{accept: true})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, ok := TakeImageObservation(out); !ok {
		t.Fatal("attachment expected after image read")
	}

	// ...then leave a second one and consume it only via a NON-image
	// tool result: the follow-up Execute must clear it, so the stale
	// image can never be spliced onto the wrong message.
	if _, err := Execute(ctx, readImageCall(t, p, 0, 0), textOnlyDeps{accept: true}); err != nil {
		t.Fatalf("execute 2: %v", err)
	}
	other, err := Execute(ctx,
		ToolCall{ID: "c2", Type: "function", Function: FunctionCall{Name: FunctionOutline, Arguments: `{"path":"` + dir + `"}`}},
		textOnlyDeps{accept: true})
	if err != nil {
		t.Fatalf("outline: %v", err)
	}
	if IsImageObservation(other) {
		t.Fatal("outline output must not be an image observation")
	}
	if _, ok := TakeImageObservation(out); ok {
		t.Error("a later non-image Execute must have cleared the pending attachment")
	}
}

func TestImageTokensOf(t *testing.T) {
	tests := []struct {
		bytes int
		want  int
	}{
		{0, 0},
		{-5, 0},
		{3, 1},
		{1500, 500},
		{1_500_000, 500_000}, // the default cap books ~500k tokens
	}
	for _, tt := range tests {
		if got := ImageTokensOf(tt.bytes); got != tt.want {
			t.Errorf("ImageTokensOf(%d) = %d, want %d", tt.bytes, got, tt.want)
		}
	}
}
