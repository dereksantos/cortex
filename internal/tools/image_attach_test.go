package tools

// image_attach_test.go — the turn-attachment primitive (issue #218, part 3
// of image input #134): an image a HUMAN named (a mention, a serve
// attachment, a URL in a task) goes through the same detection, size cap,
// vision gate, part shape, and marker text as a read_file image (#217).
// These tests pin that shared shape directly: if AttachImage and
// imageReadResult ever drift, the "agrees with read_file" subtest fails.

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// attachLimits pins the image cap so the over-cap case is exercisable
// without writing 1.5 MB of fixture, and restores the defaults after.
func attachLimits(t *testing.T, imageMaxBytes int) {
	t.Helper()
	t.Cleanup(ResetLimits)
	Configure(Limits{ImageMaxBytes: imageMaxBytes})
}

// attachGate is the ImageGate seam for these tests: a fixed verdict, no
// sink (an attachment has a different owner than a read_file result — the
// turn's user message, not a tool result).
type attachGate struct {
	headlessDeps
	accept bool
}

func (d attachGate) ImageInputEnabled() bool { return d.accept }

func TestAttachImage(t *testing.T) {
	dir := t.TempDir()
	// A file whose NAME has no image extension but whose bytes are a PNG:
	// the fsPath extension signal plus magic must still read as an image.
	mystery := filepath.Join(dir, "upload.bin")
	if err := os.WriteFile(mystery, pngBytes(64), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	pngRaw, err := os.ReadFile(mystery)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	tests := []struct {
		name        string
		deps        ToolDeps
		displayRef  string
		fsPath      string
		data        []byte
		wantRefused bool
		wantReason  string // substring of Reason when refused
		wantType    string // expected MIME when attached
	}{
		{
			name:       "workspace png by extension and magic",
			deps:       attachGate{accept: true},
			displayRef: "shots/alpha.png",
			fsPath:     filepath.Join(dir, "shots", "alpha.png"),
			data:       pngBytes(120),
			wantType:   "image/png",
		},
		{
			name:       "jpeg with an upper-case extension",
			deps:       attachGate{accept: true},
			displayRef: "DSC.JPG",
			fsPath:     filepath.Join(dir, "DSC.JPG"),
			data:       jpegBytes(),
			wantType:   "image/jpeg",
		},
		{
			name:       "gif by magic",
			deps:       attachGate{accept: true},
			displayRef: "anim",
			data:       gifBytes(),
			wantType:   "image/gif",
		},
		{
			name:       "webp by magic",
			deps:       attachGate{accept: true},
			displayRef: "still.webp",
			data:       webpBytes(),
			wantType:   "image/webp",
		},
		{
			name:       "magic wins over a lying extension",
			deps:       attachGate{accept: true},
			displayRef: "notes.txt",
			data:       pngBytes(8),
			wantType:   "image/png",
		},
		{
			name:       "remote https url is an image by magic",
			deps:       attachGate{accept: true},
			displayRef: "https://example.com/issue/screenshot.png",
			data:       pngBytes(48),
			wantType:   "image/png",
		},
		{
			name:       "extensionless upload resolves through fsPath",
			deps:       attachGate{accept: true},
			displayRef: "upload.bin",
			fsPath:     mystery,
			data:       pngRaw,
			wantType:   "image/png",
		},
		{
			name:        "a non-image file is refused",
			deps:        attachGate{accept: true},
			displayRef:  "main.go",
			fsPath:      filepath.Join(dir, "main.go"),
			data:        []byte("package main\n"),
			wantRefused: true,
			wantReason:  "not an image file",
		},
		{
			name:        "an empty payload is refused",
			deps:        attachGate{accept: true},
			displayRef:  "empty.png",
			fsPath:      filepath.Join(dir, "empty.png"),
			data:        nil,
			wantRefused: true,
			wantReason:  "no image bytes",
		},
		{
			name:        "a text-only model is refused with the vision pointer",
			deps:        attachGate{accept: false},
			displayRef:  "shot.png",
			fsPath:      filepath.Join(dir, "shot.png"),
			data:        pngBytes(64),
			wantRefused: true,
			wantReason:  "models.code.vision",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			att := AttachImage(tt.deps, tt.displayRef, tt.fsPath, tt.data)
			if att.Refused != tt.wantRefused {
				t.Fatalf("Refused = %v, want %v (reason %q)", att.Refused, tt.wantRefused, att.Reason)
			}
			if att.Ref != tt.displayRef {
				t.Errorf("Ref = %q, want the reference as named (%q)", att.Ref, tt.displayRef)
			}
			if tt.wantRefused {
				if !strings.Contains(att.Reason, tt.wantReason) {
					t.Errorf("Reason = %q, want it to contain %q", att.Reason, tt.wantReason)
				}
				// A refusal must not leave a half-attachment behind.
				if att.Part.DataURI != "" || att.Marker != "" {
					t.Errorf("refusal must carry no part or marker: %+v", att)
				}
				if att.Bytes != 0 || att.Tokens != 0 {
					t.Errorf("refusal must book nothing: bytes=%d tokens=%d", att.Bytes, att.Tokens)
				}
				return
			}
			if att.Reason != "" {
				t.Errorf("an attachment must carry no reason: %q", att.Reason)
			}
			if want := "data:" + tt.wantType + ";base64,"; !strings.HasPrefix(att.Part.DataURI, want) {
				t.Errorf("DataURI = %.40q, want prefix %q", att.Part.DataURI, want)
			}
			if att.Part.MediaType != tt.wantType {
				t.Errorf("MediaType = %q, want %q", att.Part.MediaType, tt.wantType)
			}
			if !IsImageObservation(att.Marker) {
				t.Errorf("Marker %q is not the image observation the engine recognizes", att.Marker)
			}
			if !strings.Contains(att.Marker, tt.wantType) {
				t.Errorf("Marker %q must name the file type", att.Marker)
			}
			if att.Bytes != len(tt.data) {
				t.Errorf("Bytes = %d, want %d", att.Bytes, len(tt.data))
			}
			if att.Tokens != ImageTokensOf(len(tt.data)) {
				t.Errorf("Tokens = %d, want the documented estimate %d", att.Tokens, ImageTokensOf(len(tt.data)))
			}
		})
	}
}

// TestAttachImageMissingFile is the issue's acceptance case for an image
// that isn't there: the loader hands in os.ReadFile's error and the
// refusal names the reference, reports the miss, and wraps the cause so
// errors.Is(err, os.ErrNotExist) still holds — no part, no marker, nothing
// booked.
func TestAttachImageMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone.png")
	data, err := os.ReadFile(missing)
	if err == nil {
		t.Fatal("fixture: the file must not exist")
	}
	att := AttachImageWithErr(attachGate{accept: true}, "gone.png", missing, data, err)
	if !att.Refused {
		t.Fatalf("a missing image must be refused, got %+v", att)
	}
	if !strings.Contains(att.Reason, "gone.png") {
		t.Errorf("Reason %q must name the reference the human typed", att.Reason)
	}
	if !strings.Contains(att.Error(), "no such file") {
		t.Errorf("Error() %q must carry the underlying cause", att.Error())
	}
	if !errors.Is(att, os.ErrNotExist) {
		t.Errorf("the refusal must wrap os.ErrNotExist for errors.Is: %v", att.Err)
	}
	// Unwrap is the programmatic half of the same contract: the caller
	// tells "not there" from "not a png" without parsing prose.
	if !errors.Is(errors.Unwrap(att), os.ErrNotExist) {
		t.Errorf("Unwrap(att) = %v, want the os.ReadFile error", errors.Unwrap(att))
	}
	if att.Unwrap() == nil {
		t.Error("Unwrap must expose the load cause")
	}
	// A non-load refusal ("that isn't a png") has no cause to expose.
	if plain := AttachImage(attachGate{accept: true}, "main.go", "", []byte("package main")); plain.Unwrap() != nil {
		t.Errorf("a not-an-image refusal has no underlying error, got %v", plain.Unwrap())
	}
	if att.Part.DataURI != "" || att.Marker != "" || att.Bytes != 0 || att.Tokens != 0 {
		t.Errorf("a missing image must attach and book nothing: %+v", att)
	}
}

// TestAttachImageWithErrNilBehavesLikeAttachImage pins that passing a nil
// cause is byte-for-byte the plain AttachImage result.
func TestAttachImageWithErrNilBehavesLikeAttachImage(t *testing.T) {
	raw := gifBytes()
	plain := AttachImage(attachGate{accept: true}, "anim.gif", "", raw)
	withNil := AttachImageWithErr(attachGate{accept: true}, "anim.gif", "", raw, nil)
	if withNil != plain {
		t.Errorf("a nil load error must not change the outcome:\n %+v\n %+v", withNil, plain)
	}
}

// TestAttachImageLoadErrorPrecedesDetection: a refused download (SSRF
// guard, non-200) is reported as a load failure with its cause even though
// the payload is empty — the caller's reason beats the "it's empty" shape.
func TestAttachImageLoadErrorPrecedesDetection(t *testing.T) {
	refused := fmt.Errorf("fetch refused: %w", io.EOF)
	att := AttachImageWithErr(attachGate{accept: true}, "https://127.0.0.1/x.png", "", nil, refused)
	if !att.Refused {
		t.Fatalf("a refused load must be refused, got %+v", att)
	}
	if !errors.Is(att, io.EOF) {
		t.Errorf("Reason/Error must keep the fetch cause: %v", att.Err)
	}
	if strings.Contains(att.Reason, "is empty") {
		t.Errorf("a load failure must not be reported as an empty file: %q", att.Reason)
	}
}

// TestAttachImageURLWithQueryString: an image URL with query parameters
// (`?width=100`, a CDN signature) is the shape a screenshot link in a task
// actually has. Its extension signal comes from the URL's PATH, so the
// query cannot poison the media type — an attached image part with
// `image/png?width=100` in its data URI is something no provider accepts.
// Bytes with recognizable magic already work through detectImage; this
// pins the extension-only case, which is all a text-typed CDN body gives.
func TestAttachImageURLWithQueryString(t *testing.T) {
	for _, tt := range []struct {
		name string
		url  string
		want string
	}{
		{"png with a query", "https://cdn.example.com/shot.png?width=100", "image/png"},
		{"jpeg with a signature query", "https://cdn.example.com/a.jpeg?sig=abc&exp=1", "image/jpeg"},
		{"query before the extension is irrelevant", "https://cdn.example.com/x?v=2#frag", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			att := AttachImage(attachGate{accept: true}, tt.url, "", []byte("not-magic-bytes"))
			if tt.want == "" {
				if !att.Refused {
					t.Fatalf("a URL with no image extension and no magic must be refused, got %+v", att)
				}
				return
			}
			if att.Refused {
				t.Fatalf("refused: %q", att.Reason)
			}
			if att.Part.MediaType != tt.want {
				t.Errorf("MediaType = %q, want %q", att.Part.MediaType, tt.want)
			}
			if want := "data:" + tt.want + ";base64,"; !strings.HasPrefix(att.Part.DataURI, want) {
				t.Errorf("DataURI = %.48q, want prefix %q", att.Part.DataURI, want)
			}
		})
	}
}

// TestAttachImagePartPathPrefersFilesystemPath pins which path the part
// records: the real file for a local image (what the #217 side-car's
// extension hint falls back to), the reference for a URL or upload with no
// local file behind it.
func TestAttachImagePartPathPrefersFilesystemPath(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(local, pngBytes(16), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	att := AttachImage(attachGate{accept: true}, "shot.png", local, pngBytes(16))
	if att.Part.Path != local {
		t.Errorf("Part.Path = %q, want the resolved filesystem path %q", att.Part.Path, local)
	}

	remote := AttachImage(attachGate{accept: true}, "https://example.com/a.png", "", pngBytes(16))
	if remote.Part.Path != "https://example.com/a.png" {
		t.Errorf("Part.Path = %q, want the URL reference when there is no local file", remote.Part.Path)
	}
}

// TestAttachImageSizeCap exercises the tools.read.image_max_bytes ceiling
// on a turn attachment: over the cap is a refusal naming the size, the
// cap, and the config key (the read_file refusal's contract, #217) — never
// a truncated image.
func TestAttachImageSizeCap(t *testing.T) {
	attachLimits(t, 64)
	t.Run("over the cap is refused", func(t *testing.T) {
		att := AttachImage(attachGate{accept: true}, "big.png", "/tmp/big.png", pngBytes(200))
		if !att.Refused {
			t.Fatalf("a %d-byte image under a 64-byte cap must be refused, got %+v", 208, att)
		}
		for _, want := range []string{"image/png", "208 bytes", "64", "tools.image_max_bytes"} {
			if !strings.Contains(att.Reason, want) {
				t.Errorf("Reason %q must name %q", att.Reason, want)
			}
		}
		if att.Part.DataURI != "" {
			t.Error("an over-cap image must produce no part")
		}
	})
	t.Run("exactly at the cap attaches", func(t *testing.T) {
		att := AttachImage(attachGate{accept: true}, "edge.png", "/tmp/edge.png", pngBytes(56))
		if att.Refused {
			t.Fatalf("a 64-byte image under a 64-byte cap must attach, got %q", att.Reason)
		}
		if att.Bytes != 64 {
			t.Errorf("Bytes = %d, want 64", att.Bytes)
		}
	})
}

// TestAttachImageUngatedWithoutImageGate mirrors read_file's rule: a deps
// that answers no ImageGate (a subagent's deps, a bare test double) is
// ungated here, with pkg/llm's wire gate (#216) as the backstop.
func TestAttachImageUngatedWithoutImageGate(t *testing.T) {
	att := AttachImage(headlessDeps{}, "shot.png", "", pngBytes(32))
	if att.Refused {
		t.Fatalf("a deps with no ImageGate must be ungated, got %q", att.Reason)
	}
	if !strings.HasPrefix(att.Part.DataURI, "data:image/png;base64,") {
		t.Errorf("DataURI = %.40q, want a png data URI", att.Part.DataURI)
	}
}

// TestLoadImageBytes covers the loading half a mention of a LOCAL image uses:
// the real bytes, the wrapped cause for a missing file, a directory reported
// as such rather than through os.ReadFile's bare error, and the size cap
// applied before the file is read (so a mention of a huge file costs one
// Stat, not a 200 MB slurp).
func TestLoadImageBytes(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "a.png")
	raw := pngBytes(40)
	if err := os.WriteFile(good, raw, 0o644); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	t.Run("reads the bytes", func(t *testing.T) {
		got, err := LoadImageBytes(good, 1<<20)
		if err != nil {
			t.Fatalf("LoadImageBytes: %v", err)
		}
		if string(got) != string(raw) {
			t.Errorf("got %d bytes, want the fixture's %d", len(got), len(raw))
		}
	})

	t.Run("missing file keeps os.ErrNotExist", func(t *testing.T) {
		_, err := LoadImageBytes(filepath.Join(dir, "gone.png"), 1<<20)
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("err = %v, want it to wrap os.ErrNotExist", err)
		}
	})

	t.Run("a directory is named as one", func(t *testing.T) {
		_, err := LoadImageBytes(dir, 1<<20)
		if err == nil || !strings.Contains(err.Error(), "directory") {
			t.Errorf("err = %v, want a 'is a directory' cause", err)
		}
	})

	t.Run("over the cap fails before reading", func(t *testing.T) {
		_, err := LoadImageBytes(good, 8)
		var sizeErr *ImageSizeError
		if !errors.As(err, &sizeErr) {
			t.Fatalf("err = %v, want an *ImageSizeError", err)
		}
		if sizeErr.Bytes != int64(len(raw)) || sizeErr.Cap != 8 {
			t.Errorf("size error = %+v, want bytes=%d cap=8", sizeErr, len(raw))
		}
	})

	t.Run("an empty path is an error not a panic", func(t *testing.T) {
		if _, err := LoadImageBytes("", 1<<20); err == nil {
			t.Error("an empty path must error")
		}
	})
}

// TestImageURL covers the address form a human types: only http(s) counts,
// the scheme is normalized, and neither a Windows path nor a non-web scheme
// is quietly promoted into something the harness will fetch.
func TestImageURL(t *testing.T) {
	tests := []struct {
		name   string
		ref    string
		want   string
		wantOK bool
	}{
		{"plain https", "https://x.test/a.png", "https://x.test/a.png", true},
		{"upper-case scheme is normalized", "HTTPS://X.TEST/a.png", "https://X.TEST/a.png", true},
		{"query is preserved", "https://x.test/a.png?w=1&s=2", "https://x.test/a.png?w=1&s=2", true},
		{"surrounding space is trimmed", "  https://x.test/a.png  ", "https://x.test/a.png", true},
		{"a workspace path is not an address", "shots/ui.png", "", false},
		{"a windows path is not an address", `C:\shots\a.png`, "", false},
		{"file:// is not fetched as an address", "file:///etc/passwd.png", "", false},
		{"a protocol-relative ref is not an address", "//cdn.test/a.png", "", false},
		{"ftp is not an address", "ftp://x.test/a.png", "", false},
		{"http with no host is not an address", "http:///a.png", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ImageURL(tt.ref)
			if ok != tt.wantOK {
				t.Fatalf("ImageURL(%q) ok = %v, want %v (got %q)", tt.ref, ok, tt.wantOK, got)
			}
			if ok && got != tt.want {
				t.Errorf("ImageURL(%q) = %q, want %q", tt.ref, got, tt.want)
			}
			if !ok && got != "" {
				t.Errorf("ImageURL(%q) = %q, want empty when not an address", tt.ref, got)
			}
		})
	}
}

// TestFetchPublicImageBytes pins the guard a URL mention goes through: the
// SSRF posture is the one fetch_url already has (a loopback address is
// refused), and an over-cap body is refused rather than buffered.
func TestFetchPublicImageBytes(t *testing.T) {
	t.Cleanup(ResetLimits)
	ResetLimits()

	t.Run("a private address is refused before any request", func(t *testing.T) {
		// A httptest server is loopback by construction, which is exactly what
		// the guard refuses — so this asserts the refusal without a socket.
		if _, err := FetchPublicImageBytes(context.Background(), "http://127.0.0.1:1/a.png", 1<<20); err == nil {
			t.Fatal("a loopback image url must be refused")
		}
		if _, err := FetchPublicImageBytes(context.Background(), "http://localhost:1/a.png", 1<<20); err == nil {
			t.Fatal("localhost must be refused")
		}
	})

	t.Run("a non-http scheme is refused", func(t *testing.T) {
		if _, err := FetchPublicImageBytes(context.Background(), "file:///tmp/a.png", 1<<20); err == nil {
			t.Fatal("file:// must be refused")
		}
	})
}

// TestImageMarkerForAndDataURIFor pins the two exported helpers a turn-
// attachment site (the REPL mention loader, the serve handler) renders an
// attachment line from: the marker names the reference exactly as typed —
// a nested mention path keeps its directory, so it stays findable — and
// both agree with what AttachImage produced for the same image.
func TestImageMarkerForAndDataURIFor(t *testing.T) {
	raw := pngBytes(90)
	marker := ImageMarkerFor("shots/ui.png", "image/png", raw)
	if !IsImageObservation(marker) {
		t.Errorf("marker %q is not the shape the engine recognizes", marker)
	}
	if !strings.Contains(marker, "shots/ui.png") {
		t.Errorf("marker %q must name the reference as typed, directory included", marker)
	}
	if want := fmt.Sprintf("%d bytes", len(raw)); !strings.Contains(marker, want) {
		t.Errorf("marker %q must carry the byte size (%s)", marker, want)
	}
	if want := fmt.Sprintf("≈%d tokens", ImageTokensOf(len(raw))); !strings.Contains(marker, want) {
		t.Errorf("marker %q must carry the billed estimate (%s)", marker, want)
	}
	if got := ImageDataURIFor("image/png", raw); got != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(raw) {
		t.Errorf("ImageDataURIFor mismatch: %q", got)
	}

	t.Run("empty inputs yield no line", func(t *testing.T) {
		if got := ImageMarkerFor("", "image/png", raw); got != "" {
			t.Errorf("ImageMarkerFor with no reference = %q, want empty", got)
		}
		if got := ImageMarkerFor("a.png", "", raw); got != "" {
			t.Errorf("ImageMarkerFor with no media type = %q, want empty", got)
		}
		if got := ImageDataURIFor("", raw); got != "" {
			t.Errorf("ImageDataURIFor with no media type = %q, want empty", got)
		}
	})

	t.Run("agrees with AttachImage for the same reference", func(t *testing.T) {
		att := AttachImage(attachGate{accept: true}, "shots/ui.png", "", raw)
		if att.Refused {
			t.Fatalf("refused: %q", att.Reason)
		}
		if att.Marker != marker {
			t.Errorf("AttachImage's marker %q must be ImageMarkerFor's %q", att.Marker, marker)
		}
		if att.Part.DataURI != ImageDataURIFor(att.Part.MediaType, raw) {
			t.Error("AttachImage's data URI must be ImageDataURIFor's")
		}
	})
}

// TestAttachImageAgreesWithReadFile is the no-drift guard the issue asks
// for: the exact same bytes attached to a turn and read by read_file must
// yield the identical data URI, media type, and marker observation — one
// shape on the wire and in the transcript, whichever path brought it.
func TestAttachImageAgreesWithReadFile(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(ResetLimits)
	ResetLimits()
	raw := pngBytes(300)
	path := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	fromTool := &textOnlyDeps{accept: true}
	toolObs, err := readFile(readImageCall(t, path, 0, 0), fromTool)
	if err != nil {
		t.Fatalf("read_file image: %v", err)
	}
	toolPart, ok := fromTool.takePart()
	if !ok {
		t.Fatal("read_file recorded no attachment")
	}

	turnAtt := AttachImage(attachGate{accept: true}, "shot.png", path, raw)
	if turnAtt.Refused {
		t.Fatalf("turn attachment refused: %q", turnAtt.Reason)
	}
	if turnAtt.Part.DataURI != toolPart.DataURI {
		t.Errorf("data URI drifted: turn=%d bytes, read_file=%d bytes",
			len(turnAtt.Part.DataURI), len(toolPart.DataURI))
	}
	if turnAtt.Part.MediaType != toolPart.MediaType {
		t.Errorf("media type drifted: %q vs %q", turnAtt.Part.MediaType, toolPart.MediaType)
	}
	// The marker texts agree on everything the context math reads: the
	// type, the byte size, and the billed estimate. (read_file's marker
	// names its resolved path; a turn marker names it too when the caller
	// passes the same path, which is what the mention loader does.)
	turnMarker := AttachImage(attachGate{accept: true}, path, path, raw).Marker
	if turnMarker != toolObs {
		t.Errorf("marker drifted:\n turn: %s\n read: %s", turnMarker, toolObs)
	}
	// And the base64 payload decodes back to the fixture bytes exactly.
	i := strings.Index(turnAtt.Part.DataURI, ";base64,")
	decoded, err := base64.StdEncoding.DecodeString(turnAtt.Part.DataURI[i+len(";base64,"):])
	if err != nil {
		t.Fatalf("data uri does not decode: %v", err)
	}
	if string(decoded) != string(raw) {
		t.Errorf("decoded payload is %d bytes, want the fixture's %d", len(decoded), len(raw))
	}
}
