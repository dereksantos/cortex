package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/lineedit"
	"github.com/dereksantos/cortex/internal/tools"
)

// visionDeps answers the #217 ImageGate seam for the mention tests: an image
// mention is only attached when the bound model accepts image input, and
// these tests decide that verdict explicitly rather than inherit a default.
//
// It embeds *CortexSession rather than hand-implementing tools.ToolDeps: the
// leaf reads the gate with `deps.(ImageGate)` alone (an unimplementing deps is
// ungated, exactly as for read_file), so a test only needs that one method to
// be real — and embedding the production owner keeps the double honest about
// what it stands in for, with no 40-method stub to drift.
type visionDeps struct {
	*CortexSession
	accept bool
}

func (d visionDeps) ImageInputEnabled() bool { return d.accept }

// mentionTestVision is the deps the mention tests pass: a vision-capable
// model, so an image mention attaches.
func mentionTestVision() tools.ToolDeps {
	return visionDeps{CortexSession: &CortexSession{}, accept: true}
}

// mentionTestTextOnly is the same seam answering "this model takes no images".
func mentionTestTextOnly() tools.ToolDeps {
	return visionDeps{CortexSession: &CortexSession{}, accept: false}
}

// mentions runs the parser with the background context the REPL path uses.
func mentions(root, input string, deps tools.ToolDeps) (string, string, []TurnImage, []MentionRefusal) {
	return processMentions(context.Background(), root, input, deps)
}

// mentionsAndText is the two-value shape the pre-#218 tests asserted: those
// cases are about TEXT attachments and prose, so they keep reading exactly as
// they did, with the image returns dropped. The image cases below call
// mentions directly and assert all four values.
func mentionsAndText(input, root string) (string, string) {
	clean, attach, _, _ := mentions(root, input, mentionTestVision())
	return clean, attach
}

// pngFixtureBytes is a minimal but genuine PNG: the 8-byte signature the
// sniffer reads, plus payload so sizes are predictable.
func pngFixtureBytes(n int) []byte {
	head := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	for i := 0; i < n; i++ {
		head = append(head, byte('a'+i%26))
	}
	return head
}

// setupMentionWorkspace creates a small workspace with a few files for the
// mention tests.
func setupMentionWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"src/alpha.go": "package main\n",
		"main.go":      "package main\n",
		"notes.txt":    "hello world\n",
	}
	for path, content := range files {
		p := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	// A real PNG (issue #218) in a nested dir, so a mention keeps its
	// directory and is still findable.
	if err := os.MkdirAll(filepath.Join(root, "shots"), 0o755); err != nil {
		t.Fatalf("mkdir shots: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "shots", "ui.png"), pngFixtureBytes(64), 0o644); err != nil {
		t.Fatalf("write shot: %v", err)
	}
	// A DIRECTORY whose name ends in .png: the image branch must not swallow
	// it, and it must still be left as typed (its pre-#218 behaviour).
	if err := os.MkdirAll(filepath.Join(root, "shots.png"), 0o755); err != nil {
		t.Fatalf("mkdir shots.png: %v", err)
	}
	return root
}

func TestProcessMentionsNoMentions(t *testing.T) {
	root := setupMentionWorkspace(t)
	clean, attach := mentionsAndText("just a normal line", root)
	if clean != "just a normal line" {
		t.Errorf("clean = %q, want unchanged", clean)
	}
	if attach != "" {
		t.Errorf("attach = %q, want empty", attach)
	}
}

func TestProcessMentionsInlinesSmallFile(t *testing.T) {
	root := setupMentionWorkspace(t)
	clean, attach := mentionsAndText("look at @notes.txt please", root)
	if !strings.Contains(clean, "[@notes.txt attached]") {
		t.Errorf("clean = %q, want the mention replaced by a marker", clean)
	}
	if !strings.Contains(attach, "inlined") {
		t.Errorf("attach = %q, want the inlined marker", attach)
	}
	if !strings.Contains(attach, "hello world") {
		t.Errorf("attach = %q, want the file content inlined", attach)
	}
}

// TestProcessMentionsProseLeavesInputUnchanged covers the prose cases:
// only an @ starting a whitespace-delimited word is a mention, and a
// mention that does not resolve to a readable file is left in the input
// EXACTLY as the user typed it — the user's prose is what goes into history
// and to the model.
func TestProcessMentionsProseLeavesInputUnchanged(t *testing.T) {
	root := setupMentionWorkspace(t)
	tests := []struct {
		name  string
		input string
	}{
		{"email address", "mail user@example.com"},
		{"scoped npm name mid-identifier context", "bump @types/node"},
		{"Java-style annotation", "annotate @Override on the method"},
		{"trailing punctuation after a real mention resolves", "look at @main.go, please"},
		{"missing file stays as typed", "see @missing.txt"},
		{"out-of-workspace path stays as typed", "see @../../etc/passwd"},
		{"absolute path stays as typed", "see @/etc/passwd"},
		{"a directory stays as typed", "see @src"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clean, attach := mentionsAndText(tt.input, root)
			// For every case above, the input must come back unchanged:
			// email/scoped/annotation are prose (not mentions at all);
			// missing/escape/absolute/directory are mentions that did not
			// resolve — left as typed, with NO attachment note.
			// Exception: the punctuation case resolves (main.go exists), so
			// it IS rewritten — handled below with its own expectations.
			if tt.name == "trailing punctuation after a real mention resolves" {
				// The mention resolves (main.go exists): the comma is stripped
				// before resolution and the marker names the resolved path,
				// while the user's trailing punctuation stays in the line.
				if clean != "look at [@main.go attached], please" {
					t.Errorf("clean = %q, want \"look at [@main.go attached], please\" (comma stripped, path resolved, punctuation kept)", clean)
				}
				if !strings.Contains(attach, "inlined") || !strings.Contains(attach, "package main") {
					t.Errorf("attach = %q, want main.go inlined", attach)
				}
				return
			}
			if clean != tt.input {
				t.Errorf("clean = %q, want the input unchanged (%q)", clean, tt.input)
			}
			if attach != "" {
				t.Errorf("attach = %q, want empty (unresolved mention: no note)", attach)
			}
		})
	}
}

func TestProcessMentionsMultiple(t *testing.T) {
	root := setupMentionWorkspace(t)
	clean, attach := mentionsAndText("see @notes.txt and @main.go", root)
	if !strings.Contains(clean, "[@notes.txt attached]") || !strings.Contains(clean, "[@main.go attached]") {
		t.Errorf("clean = %q, want both mentions replaced", clean)
	}
	if strings.Count(attach, "inlined") < 2 {
		t.Errorf("attach = %q, want both files inlined", attach)
	}
}

// TestProcessMentionsLargeFileOutlines verifies the large-file branch uses
// the same size rule as read_file (CurationBudgetTokens, ~16k tokens) and
// hands back a structural outline with a pointer to study, instead of a
// 64k-byte inline.
func TestProcessMentionsLargeFileOutlines(t *testing.T) {
	root := t.TempDir()
	// ~70k bytes: above 16k tokens (the curation budget) by a wide margin.
	big := strings.Repeat("lorem ipsum dolor sit amet\n", 5000)
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(big), 0o644); err != nil {
		t.Fatalf("write big: %v", err)
	}
	clean, attach := mentionsAndText("review @big.txt", root)
	if !strings.Contains(clean, "[@big.txt attached]") {
		t.Errorf("clean = %q, want the mention marked", clean)
	}
	if strings.Contains(attach, big) {
		t.Errorf("attach inlined the whole 70k file — it should be reduced to an outline")
	}
	if !strings.Contains(attach, "too large to inline") {
		t.Errorf("attach = %q, want the too-large-to-inline note", attach)
	}
	if !strings.Contains(attach, "study") {
		t.Errorf("attach = %q, want a pointer to study", attach)
	}
}

// TestMentionsImages is issue #218's parsing acceptance: an @mention naming
// an image — a workspace path or an http(s) address — comes back as an image
// attachment (wire part + marker), NOT as inlined text, while a non-image
// file and a missing file keep their pre-#218 behaviour exactly.
func TestMentionsImages(t *testing.T) {
	root := setupMentionWorkspace(t)
	remotePNG := "https://cdn.example.com/issues/screenshot.png"

	// The URL branch is driven through the fetch seam so the real resolve
	// loop runs offline; t.Cleanup restores the production fetcher.
	orig := fetchMentionImage
	t.Cleanup(func() { fetchMentionImage = orig })
	fetchMentionImage = func(_ context.Context, u string) ([]byte, error) {
		if strings.Contains(u, "broken") {
			return nil, errors.New("404 not found")
		}
		if strings.Contains(u, ".txt") {
			return []byte("text served at an image-looking url"), nil
		}
		return pngFixtureBytes(48), nil
	}

	tests := []struct {
		name string
		// wantImages is how many image attachments the mention must produce.
		wantImages int
		// wantRef is the reference the attachment must carry, as typed.
		wantRef string
		// wantClean is the exact expected line when set.
		wantClean string
		// wantTextAttach is a substring the TEXT block must contain (a non-
		// image mention still attaches as text), when set.
		wantTextAttach string
		// wantUnchanged asserts the input comes back byte-for-byte unchanged.
		wantUnchanged bool
		// wantRefusals is how many "looked like an image, did not attach"
		// reports the call must return.
		wantRefusals int
		input        string
	}{
		{
			name:       "image path mention attaches as an image",
			input:      "look at @shots/ui.png",
			wantImages: 1,
			wantRef:    "shots/ui.png",
			wantClean:  "look at [@shots/ui.png attached]",
		},
		{
			name:       "image url mention attaches as an image",
			input:      "repro in @" + remotePNG,
			wantImages: 1,
			wantRef:    remotePNG,
			wantClean:  "repro in [@" + remotePNG + " attached]",
		},
		{
			name:          "missing image is left as typed",
			input:         "see @shots/gone.png",
			wantImages:    0,
			wantRefusals:  1,
			wantUnchanged: true,
		},
		{
			name:           "non-image file still attaches as text",
			input:          "see @notes.txt",
			wantImages:     0,
			wantTextAttach: "hello world",
			wantClean:      "see [@notes.txt attached]",
		},
		{
			name:           "a .go file is untouched text behaviour",
			input:          "see @main.go",
			wantImages:     0,
			wantTextAttach: "package main",
		},
		{
			name:          "a url that serves non-image bytes is left as typed",
			input:         "see @https://example.com/notes.txt",
			wantImages:    0,
			wantRefusals:  1,
			wantUnchanged: true,
		},
		{
			name:          "a url whose download fails is left as typed",
			input:         "see @https://example.com/broken.png",
			wantImages:    0,
			wantRefusals:  1,
			wantUnchanged: true,
		},
		{
			name:          "an out-of-workspace image path is left as typed",
			input:         "see @../../etc/shadow.png",
			wantImages:    0,
			wantUnchanged: true,
		},
		{
			// A directory whose NAME says image: it looked like one, so the
			// human gets a reason ("that's a folder") rather than silence —
			// the line itself is still left exactly as typed.
			name:          "a directory named like an image is left as typed, with a reason",
			input:         "see @shots.png",
			wantImages:    0,
			wantRefusals:  1,
			wantUnchanged: true,
		},
		{
			name:       "trailing punctuation survives an image mention",
			input:      "see @shots/ui.png, please",
			wantImages: 1,
			wantRef:    "shots/ui.png",
			wantClean:  "see [@shots/ui.png attached], please",
		},
		{
			name:       "an image url keeps its query and its period is trimmed",
			input:      "see @https://cdn.example.com/a.png?width=100.",
			wantImages: 1,
			wantRef:    "https://cdn.example.com/a.png?width=100",
		},
		{
			name:       "mixed text and image mentions split by kind",
			input:      "the bug is in @main.go and @shots/ui.png",
			wantImages: 1,
			wantRef:    "shots/ui.png",
			wantClean:  "the bug is in [@main.go attached] and [@shots/ui.png attached]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clean, textAttach, images, refused := mentions(root, tt.input, mentionTestVision())
			if len(images) != tt.wantImages {
				t.Fatalf("images = %d (%v), want %d", len(images), refsOf(images), tt.wantImages)
			}
			// A mention that LOOKED like an image but could not attach owes the
			// human a reason; a plain unresolved path stays silent, as it always
			// was.
			if len(refused) != tt.wantRefusals {
				t.Errorf("refusals = %d (%v), want %d", len(refused), refused, tt.wantRefusals)
			}
			if tt.wantUnchanged {
				if clean != tt.input {
					t.Errorf("clean = %q, want the input unchanged (%q)", clean, tt.input)
				}
				if textAttach != "" {
					t.Errorf("text attach = %q, want empty for an unresolved mention", textAttach)
				}
				return
			}
			if tt.wantClean != "" && clean != tt.wantClean {
				t.Errorf("clean = %q, want %q", clean, tt.wantClean)
			}
			if tt.wantTextAttach != "" && !strings.Contains(textAttach, tt.wantTextAttach) {
				t.Errorf("text attach = %q, want it to contain %q", textAttach, tt.wantTextAttach)
			}
			if tt.wantImages == 0 {
				return
			}
			img := images[0]
			if img.Ref != tt.wantRef {
				t.Errorf("Ref = %q, want %q", img.Ref, tt.wantRef)
			}
			// The bytes are a content part, never text in the attachment block.
			if strings.Contains(textAttach, "base64") || strings.Contains(textAttach, "attached as an image part") {
				t.Errorf("an image must not appear in the TEXT attachment block: %q", textAttach)
			}
			if img.Part.MediaType != "image/png" {
				t.Errorf("MediaType = %q, want image/png", img.Part.MediaType)
			}
			if !strings.HasPrefix(img.Part.DataURI, "data:image/png;base64,") {
				t.Errorf("DataURI = %.40q, want a png data URI", img.Part.DataURI)
			}
			// A resolved mention must carry REAL image bytes, not a placeholder:
			// the data URI has to decode back to something the sniffer still
			// calls a PNG (the fixture the workspace was built from).
			i := strings.Index(img.Part.DataURI, ";base64,")
			raw, decErr := base64.StdEncoding.DecodeString(img.Part.DataURI[i+len(";base64,"):])
			if decErr != nil {
				t.Fatalf("attachment data URI does not decode: %v", decErr)
			}
			if tools.SniffImageBytes(raw) != "image/png" {
				t.Errorf("attachment decodes to %d bytes that are not a png", len(raw))
			}
			// The attachment line the human is shown names the reference, the
			// type, the size, and what it costs the window.
			lines := MentionAttachmentLines(images, nil)
			if len(lines) != 1 {
				t.Fatalf("attachment lines = %v, want one", lines)
			}
			for _, want := range []string{tt.wantRef, "image/png", fmt.Sprintf("%d bytes", len(raw))} {
				if !strings.Contains(lines[0], want) {
					t.Errorf("attachment line %q must name %q", lines[0], want)
				}
			}
			if want := fmt.Sprintf("~%d tokens", tools.ImageTokensOf(len(raw))); !strings.Contains(lines[0], want) {
				t.Errorf("attachment line %q must carry the billed estimate %q", lines[0], want)
			}
		})
	}
}

func refsOf(images []TurnImage) []string {
	out := make([]string, len(images))
	for i, im := range images {
		out[i] = im.Ref
	}
	return out
}

// TestMentionsImageTextOnlyModel: with the vision verdict off, an image
// mention attaches nothing — the bytes would fail the #216 wire gate anyway —
// and the mention is left in the line as typed so the human sees it was not
// taken.
func TestMentionsImageTextOnlyModel(t *testing.T) {
	root := setupMentionWorkspace(t)
	input := "look at @shots/ui.png"
	clean, textAttach, images, refused := mentions(root, input, mentionTestTextOnly())
	if len(images) != 0 {
		t.Fatalf("a text-only model must get no image attachment, got %v", refsOf(images))
	}
	// The human still learns why nothing arrived.
	if len(refused) != 1 || !strings.Contains(refused[0].Reason, "vision") {
		t.Errorf("refusals = %v, want one naming the vision verdict", refused)
	}
	if clean != input {
		t.Errorf("clean = %q, want the input unchanged (%q)", clean, input)
	}
	if textAttach != "" {
		t.Errorf("text attach = %q, want no base64 blob pasted into the prompt", textAttach)
	}
}

// TestMentionsImageDataURLIsNotInlined guards the specific failure this
// issue is about: an image must never reach the model as inlined text, not
// even a small one, and not even when the same line mentions a text file.
func TestMentionsImageDataURLIsNotInlined(t *testing.T) {
	root := setupMentionWorkspace(t)
	_, textAttach, images, _ := mentions(root, "@notes.txt and @shots/ui.png", mentionTestVision())
	if len(images) != 1 {
		t.Fatalf("images = %d, want 1", len(images))
	}
	if strings.Contains(textAttach, base64.StdEncoding.EncodeToString(pngFixtureBytes(8))) {
		t.Errorf("image bytes leaked into the text attachment block")
	}
	if !strings.Contains(textAttach, "hello world") {
		t.Errorf("the text mention must still inline: %q", textAttach)
	}
}

// TestMentionAttachmentLines is the "shown as an attachment line" half of
// #218: what the parser resolved, and what it refused, each get a line — a
// screenshot that silently failed to attach must not look like a typo.
func TestMentionAttachmentLines(t *testing.T) {
	root := setupMentionWorkspace(t)
	_, _, images, refused := mentions(root, "see @shots/ui.png", mentionTestVision())
	if len(images) != 1 {
		t.Fatalf("fixture: want one image, got %v", refsOf(images))
	}
	if len(refused) != 0 {
		t.Fatalf("fixture: want no refusals, got %v", refused)
	}
	lines := MentionAttachmentLines(images, nil)
	if len(lines) != 1 {
		t.Fatalf("lines = %v, want one attachment line", lines)
	}
	if !strings.Contains(lines[0], "shots/ui.png") || !strings.Contains(lines[0], "image/png") {
		t.Errorf("attachment line %q must name the reference and its type", lines[0])
	}
	if !strings.Contains(lines[0], "attached image") {
		t.Errorf("attachment line %q must say it attached", lines[0])
	}

	t.Run("a refusal is reported too", func(t *testing.T) {
		// Driven from a real parse, not a hand-written string: a missing image
		// mention must produce the line, with the parser's own reason in it.
		_, _, none, missing := mentions(root, "see @shots/gone.png", mentionTestVision())
		if len(none) != 0 || len(missing) != 1 {
			t.Fatalf("fixture: want no image and one refusal, got %v / %v", none, missing)
		}
		out := MentionAttachmentLines(nil, missing)
		if len(out) != 1 || !strings.Contains(out[0], "image not attached") || !strings.Contains(out[0], "shots/gone.png") {
			t.Errorf("refusal line = %v, want one 'image not attached' line naming it", out)
		}
	})

	t.Run("nothing to report is silent", func(t *testing.T) {
		if out := MentionAttachmentLines(nil, nil); out != nil {
			t.Errorf("a text-only turn must print nothing, got %v", out)
		}
	})
}

// TestMentionsWindowsPathIsNotTreatedAsURL: url.Parse reads `C:\shots\a.png`
// as scheme "c". A mention that merely LOOKS like a URL must stay a path
// (and, outside the workspace, be left as typed) rather than be fetched.
func TestMentionsWindowsPathIsNotTreatedAsURL(t *testing.T) {
	fetched := false
	orig := fetchMentionImage
	t.Cleanup(func() { fetchMentionImage = orig })
	fetchMentionImage = func(context.Context, string) ([]byte, error) {
		fetched = true
		return pngFixtureBytes(8), nil
	}
	root := setupMentionWorkspace(t)
	input := `see @C:\shots\ui.png`
	clean, _, images, _ := mentions(root, input, mentionTestVision())
	if fetched {
		t.Error("a Windows-style path must not be fetched as a URL")
	}
	if len(images) != 0 {
		t.Errorf("images = %v, want none", refsOf(images))
	}
	if clean != input {
		t.Errorf("clean = %q, want the input unchanged", clean)
	}
}

// TestMentionsContextCancelStopsFetch: an interrupted line must not start a
// download nobody is waiting for.
func TestMentionsContextCancelStopsFetch(t *testing.T) {
	orig := fetchMentionImage
	t.Cleanup(func() { fetchMentionImage = orig })
	called := false
	fetchMentionImage = func(context.Context, string) ([]byte, error) {
		called = true
		return pngFixtureBytes(8), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, images, _ := processMentions(ctx, setupMentionWorkspace(t), "see @https://example.com/a.png", mentionTestVision())
	if called {
		t.Error("a cancelled context must not reach the fetcher")
	}
	if len(images) != 0 {
		t.Errorf("images = %v, want none after a cancel", refsOf(images))
	}
}

// TestMentionsURLWebDisabledNoFetch is the enable_web acceptance (#218
// review): an `@https://…` mention is network egress, and an operator who set
// tools.enable_web false to stay offline must not get a download from typing
// a link in prose. With web tools off the fetch seam is never called, the
// mention is refused with a reason that NAMES the switch, and the line is
// left exactly as typed.
func TestMentionsURLWebDisabledNoFetch(t *testing.T) {
	orig := fetchMentionImage
	t.Cleanup(func() { fetchMentionImage = orig })
	called := false
	fetchMentionImage = func(context.Context, string) ([]byte, error) {
		called = true
		return pngFixtureBytes(8), nil
	}
	root := setupMentionWorkspace(t)
	no := false
	// The REAL gate: *CortexSession.IsToolEnabled reads Tools.EnableWeb, the
	// same field fetch_url's dispatch gate reads — so this exercises the
	// production check, not a copy of it.
	webOff := visionDeps{CortexSession: &CortexSession{Config: &Config{Tools: ToolConfig{EnableWeb: &no}}}, accept: true}
	webOn := visionDeps{CortexSession: &CortexSession{}, accept: true}

	tests := []struct {
		name      string
		deps      tools.ToolDeps
		wantFetch bool
		wantRef   int
	}{
		{"web disabled: no fetch, refusal names enable_web", webOff, false, 1},
		{"web enabled (absent config = on): fetch happens", webOn, true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called = false
			input := "see @https://cdn.example.com/a.png"
			clean, _, images, refused := mentions(root, input, tt.deps)
			if called != tt.wantFetch {
				t.Errorf("fetch seam called = %v, want %v", called, tt.wantFetch)
			}
			if len(refused) != tt.wantRef {
				t.Fatalf("refusals = %v, want %d", refused, tt.wantRef)
			}
			if tt.wantRef == 1 {
				if !strings.Contains(refused[0].Reason, "enable_web") {
					t.Errorf("refusal %q must name tools.enable_web", refused[0].Reason)
				}
				if len(images) != 0 {
					t.Errorf("images = %v, want none when web is disabled", refsOf(images))
				}
				if clean != input {
					t.Errorf("clean = %q, want the input unchanged", clean)
				}
			}
		})
	}
}

// TestMentionCompleterSingleModelSource drives the REAL wired completers
// (the map mentionCompleter builds, with the id list injected through the
// modelIDs seam) through the REAL lineedit engine for a /model continuation:
// every model id must appear exactly once in the candidate list — the old
// double wiring (SlashCompleter.Sub AND a separate "model" entry) listed each
// id twice and never filled the prefix on the first Tab. The ids are
// word-level (bare model names), so the first Tab fills the common model-id
// prefix in place of the argument.
func TestMentionCompleterSingleModelSource(t *testing.T) {
	root := setupMentionWorkspace(t)
	names := []string{"qwen/qwen3-coder:free", "tencent/hy3:free"}
	orig := modelIDs
	modelIDs = func(*CortexSession) []string { return names }
	defer func() { modelIDs = orig }()

	// A minimal session: root() resolves the PathCompleter's root and
	// Request.Model feeds modelIDs. The map comes from the REAL
	// mentionCompleter — if a regression re-added a separate "model" entry,
	// the merge below (Terminal.completionCandidates' fixed slot order,
	// slash → model → path) would double every id and this test would fail
	// instead of going unnoticed.
	session := &CortexSession{
		Request:   &AgentRequest{Model: names[0]},
		workspace: &Workspace{Root: root},
	}
	completers := mentionCompleter(session)

	// Merged in the engine's fixed slot order (slash, model, path) — the
	// same merge Terminal.completionCandidates runs before handing a Tab to
	// Completions.
	var cands []string
	for _, k := range []string{"slash", "model", "path"} {
		if c, ok := completers[k]; ok {
			cands = append(cands, c.Candidates("/model q", 8)...)
		}
	}
	if len(cands) != 1 {
		t.Fatalf("candidates = %v, want exactly one (the single matching id, no duplicates)", cands)
	}
	if cands[0] != "qwen/qwen3-coder:free" {
		t.Errorf("candidates[0] = %q, want the bare id (word-level: it replaces the argument word)", cands[0])
	}

	// The real engine: the first Tab fills the common model-id prefix into
	// the argument, leaving "/model " intact — the behavior that only works
	// when there is exactly one source for the ids.
	cm := lineedit.NewCompletions()
	line, pos, _ := cm.Tab("/model q", 8, func(string, int) []string { return cands })
	if line != "/model qwen/qwen3-coder:free" || pos != 28 {
		t.Errorf("first Tab = (%q, %d), want the id filled in place of 'q'", line, pos)
	}
}
