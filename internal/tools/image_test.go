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

// textOnlyDeps answers the ImageGate with a fixed verdict and the
// ImageSink with a per-instance slot (the real production owner shape: a
// session, not process-global state).
type textOnlyDeps struct {
	headlessDeps
	accept   bool
	part     ImagePart
	recorded bool
}

func (d *textOnlyDeps) ImageInputEnabled() bool { return d.accept }
func (d *textOnlyDeps) RecordImage(p ImagePart) {
	d.part = p
	d.recorded = true
}

// takePart consumes the recorded attachment (what the cmd/cortex splice
// step does to its session's slot).
func (d *textOnlyDeps) takePart() (ImagePart, bool) {
	if !d.recorded {
		return ImagePart{}, false
	}
	p := d.part
	d.part = ImagePart{}
	d.recorded = false
	return p, true
}

func TestReadFileImageVisionAttachesPart(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(ResetLimits)
	ResetLimits()
	p := writeImage(t, dir, "shot.png", pngBytes(300))
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	deps := &textOnlyDeps{accept: true}
	out, err := readFile(readImageCall(t, p, 0, 0), deps)
	if err != nil {
		t.Fatalf("read_file image: %v", err)
	}
	if !IsImageObservation(out) {
		t.Fatalf("observation %q is not the image marker", out)
	}
	if !strings.Contains(out, "shot.png") || !strings.Contains(out, "image/png") {
		t.Errorf("observation must name the path and type: %q", out)
	}
	part, ok := deps.takePart()
	if !ok {
		t.Fatal("the session's ImageSink slot holds no attachment after an image read")
	}
	if part.MediaType != "image/png" || part.Path != p {
		t.Errorf("part = %+v", part)
	}
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
	if part.DataURI != want {
		t.Errorf("data URI mismatch: got %d bytes, want %d", len(part.DataURI), len(want))
	}

	t.Run("attachment is consumed once", func(t *testing.T) {
		if _, ok := deps.takePart(); ok {
			t.Error("a second take must not re-splice the same image")
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
			deps := &textOnlyDeps{accept: false}
			out, err := readFile(readImageCall(t, p, 0, 0), deps)
			if err != nil {
				t.Fatalf("refusal must be an observation, not an error: %v", err)
			}
			if IsImageObservation(out) {
				t.Errorf("a text-only refusal must not carry the image marker: %q", out)
			}
			if !strings.Contains(out, tt.want) {
				t.Errorf("refusal must name the file type %q: %q", tt.want, out)
			}
			if _, ok := deps.takePart(); ok {
				t.Error("a refusal attaches nothing")
			}
		})
	}

	// A deps that answers the gate yes but records nothing (no ImageSink
	// — the headless stub, a test double): a marker with no attachment
	// behind it would tell the model about an image nothing can deliver,
	// so the read is refused instead (#217: no phantom "attached").
	t.Run("deps without a sink refuses", func(t *testing.T) {
		p := writeImage(t, dir, "sub.png", pngBytes(32))
		out, err := readFile(readImageCall(t, p, 0, 0), headlessDeps{})
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if IsImageObservation(out) {
			t.Errorf("a no-sink dispatch must not claim an attachment, got %q", out)
		}
		if !strings.Contains(out, "image/png") {
			t.Errorf("the refusal must name the file type: %q", out)
		}
	})
}

func TestReadFileImageSizeCapRefusal(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(ResetLimits)
	Configure(Limits{ImageMaxBytes: 128})
	p := writeImage(t, dir, "big.png", pngBytes(4096))

	deps := &textOnlyDeps{accept: true}
	out, err := readFile(readImageCall(t, p, 0, 0), deps)
	if err != nil {
		t.Fatalf("cap refusal must be an observation, not an error: %v", err)
	}
	if IsImageObservation(out) {
		t.Errorf("over-cap must attach nothing: %q", out)
	}
	if !strings.Contains(out, "image/png") || !strings.Contains(out, "128") {
		t.Errorf("cap refusal must name the type and the cap: %q", out)
	}
	if _, ok := deps.takePart(); ok {
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
	deps := &textOnlyDeps{accept: true}
	out, err := readFile(tc, deps)
	if err != nil {
		t.Fatalf("ranged refusal must be an observation: %v", err)
	}
	if IsImageObservation(out) {
		t.Errorf("a ranged image read attaches nothing: %q", out)
	}
	if !strings.Contains(out, "image/png") || !strings.Contains(out, "range") {
		t.Errorf("ranged refusal must name the type and why: %q", out)
	}
	if _, ok := deps.takePart(); ok {
		t.Error("a ranged refusal records no attachment")
	}
}

// TestImageSinkIsPerSession: the attachment slot is the DEPS' own, not
// process-global state — with two sessions recording images, neither
// picks up the other's (the shape serve/discord hit when two sessions
// dispatch concurrently, #217).
func TestImageSinkIsPerSession(t *testing.T) {
	t.Cleanup(ResetLimits)
	ResetLimits()
	dir := t.TempDir()
	ctx := context.Background()

	depsA := &textOnlyDeps{accept: true}
	depsB := &textOnlyDeps{accept: true}
	pA := writeImage(t, dir, "a.png", pngBytes(16))
	pB := writeImage(t, dir, "b.png", append(append([]byte{}, pngBytes(8)...), 'B', 'B', 'B'))

	if _, err := Execute(ctx, readImageCall(t, pA, 0, 0), depsA); err != nil {
		t.Fatalf("execute A: %v", err)
	}
	if _, err := Execute(ctx, readImageCall(t, pB, 0, 0), depsB); err != nil {
		t.Fatalf("execute B: %v", err)
	}

	partA, ok := depsA.takePart()
	if !ok || partA.Path != pA {
		t.Errorf("session A's slot = %+v ok=%v, want the A.png attachment", partA, ok)
	}
	partB, ok := depsB.takePart()
	if !ok || partB.Path != pB {
		t.Errorf("session B's slot = %+v ok=%v, want the B.png attachment", partB, ok)
	}
}

// TestReadFileImageSubagentRefused: a read_file inside a subagent (study
// or agent) must not return an "attached as an image part" observation —
// the subagent's engine never splices Parts onto its tool messages, so
// the model would be told about an image it cannot see (#217 review).
func TestReadFileImageSubagentRefused(t *testing.T) {
	t.Cleanup(ResetLimits)
	ResetLimits()
	dir := t.TempDir()
	p := writeImage(t, dir, "x.png", pngBytes(16))

	frame := pushNest("study")
	defer func() {
		popNest()
		_ = frame
	}()

	deps := &textOnlyDeps{accept: true}
	out, err := Execute(context.Background(), readImageCall(t, p, 0, 0), deps)
	if err != nil {
		t.Fatalf("subagent image read must be an observation, not an error: %v", err)
	}
	if IsImageObservation(out) {
		t.Errorf("a subagent read must not claim an attached image part: %q", out)
	}
	if !strings.Contains(out, "image/png") || !strings.Contains(out, "subagent") {
		t.Errorf("the refusal must name the file type and why: %q", out)
	}
	if _, ok := deps.takePart(); ok {
		t.Error("a subagent refusal records no attachment")
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
