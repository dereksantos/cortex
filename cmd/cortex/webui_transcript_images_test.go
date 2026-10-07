// webui_transcript_images_test.go — the web UI showing a turn's attached
// image (issue #218 step 6).
//
// Two halves, tested the way each half can actually be tested:
//
//   - the view-model producer, in Go, end to end through
//     buildTranscriptViewModel over a real transcript file. This is the half
//     with logic worth breaking: which messages get an images list, what the
//     reference and media type come out as, and whether a remote image is
//     given a loadable URL while a local one is not.
//   - the renderer, by source shape (session.js), since this stdlib-only suite
//     has no JS engine — the same convention webui_session_input_test.go
//     established.
//
// The producer half matters more than it looks. An image's bytes never reach
// the transcript JSON (Parts is `json:"-"` on the wire type), so the ONLY
// durable trace of an attachment is the short `[image: …]` marker string in
// the message content. If the view-model does not read that, the session
// screen shows a coder their own screenshot as literal bracketed text.
package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/tools"
)

func TestTranscriptViewModelReportsAttachedImages(t *testing.T) {
	const marker = "[image:shots/ui.png image/png 20480 bytes ≈6826 tokens attached as an image part]"
	const remote = "[image:https://cdn.example.com/a.png image/png 4096 bytes ≈1365 tokens attached as an image part]"
	dir := t.TempDir()
	// The same fixture shape and helper TestBuildTranscriptViewModelGolden
	// uses: role/content at the top level of the entry, not nested. Markers
	// ride tool-role messages (read_file's image result) and a text-only model
	// turn's user content stays as typed (#218 step 3), so that is what the
	// renderer is shown in practice.
	writeTestSession(t, dir, "20261007-120000",
		`{"kind":"message","turn":0,"role":"system","content":"seed"}`,
		`{"kind":"message","turn":1,"role":"user","content":"what is in this?"}`,
		`{"kind":"message","turn":1,"role":"tool","tool_call_id":"c1","content":"`+marker+`"}`,
		`{"kind":"message","turn":2,"role":"tool","tool_call_id":"c2","content":"`+remote+`"}`,
		`{"kind":"message","turn":3,"role":"assistant","content":"a login form"}`,
	)

	vm, err := buildTranscriptViewModel(filepath.Join(dir, "20261007-120000.jsonl"))
	if err != nil {
		t.Fatalf("buildTranscriptViewModel: %v", err)
	}
	if len(vm.Entries) != 4 {
		t.Fatalf("entries = %d, want 4 (the seed system message is omitted)", len(vm.Entries))
	}

	t.Run("a local attachment gets a name and no loadable URL", func(t *testing.T) {
		imgs := vm.Entries[1].Images
		if len(imgs) != 1 {
			t.Fatalf("read_file entry images = %+v, want 1", imgs)
		}
		if imgs[0].Name != "shots/ui.png" {
			t.Errorf("name = %q, want the reference the marker named", imgs[0].Name)
		}
		if imgs[0].MediaType != "image/png" {
			t.Errorf("media_type = %q, want image/png", imgs[0].MediaType)
		}
		// No URL: the transcript holds no bytes and a workspace path is not a
		// URL the browser can load. Emitting one anyway would render a broken
		// <img>, which the renderer is written to avoid.
		if imgs[0].URL != "" {
			t.Errorf("url = %q, want empty for a local attachment", imgs[0].URL)
		}
	})

	t.Run("a remote image gets a loadable URL", func(t *testing.T) {
		imgs := vm.Entries[2].Images
		if len(imgs) != 1 {
			t.Fatalf("remote entry images = %+v, want 1", imgs)
		}
		if imgs[0].URL != "https://cdn.example.com/a.png" {
			t.Errorf("url = %q, want the address so the browser can show it", imgs[0].URL)
		}
	})

	t.Run("a text entry has no images", func(t *testing.T) {
		// The human's own turn in THIS fixture: its content is kept exactly as
		// typed, with no marker in it (#218 step 3), and no side-car manifest
		// sits beside the transcript either, so it shows no attachment. (A real
		// human-attached turn IS covered — by the manifest, through a real
		// TurnWithAttachments run — in
		// TestTranscriptViewModelReportsManifestAttachment below.)
		if vm.Entries[0].Images != nil {
			t.Errorf("user entry images = %+v, want none", vm.Entries[0].Images)
		}
		if vm.Entries[3].Images != nil {
			t.Errorf("assistant entry images = %+v, want none", vm.Entries[3].Images)
		}
	})

	t.Run("images are omitted from the JSON when absent", func(t *testing.T) {
		// `omitempty` is what keeps existing transcript clients' payloads
		// unchanged: an image-less entry must serialize exactly as before.
		blob, err := json.Marshal(vm.Entries[3])
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(blob), "images") {
			t.Errorf("an entry with no images still serializes the field: %s", blob)
		}
	})

	t.Run("an attached-image entry does serialize the field", func(t *testing.T) {
		blob, err := json.Marshal(vm.Entries[1])
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if !strings.Contains(string(blob), `"images"`) || !strings.Contains(string(blob), "shots/ui.png") {
			t.Errorf("the attached image is missing from the JSON: %s", blob)
		}
	})
}

// TestTranscriptViewModelReportsManifestAttachment is the real-turn
// acceptance the reviewer asked for (#218): a human-attached image writes NO
// marker into the message text — turn.go keeps the user's input byte-for-byte
// — so the ONLY durable record is the side-car manifest beside the
// transcript. This drives a real TurnWithAttachments on a vision-enabled
// session, then builds the view-model from the transcript that run actually
// wrote, and asserts the user entry's Images is non-empty. A fixture of
// hand-written marker strings cannot catch this regression, because
// production never writes one on a human-attached turn.
func TestTranscriptViewModelReportsManifestAttachment(t *testing.T) {
	cs := attachTestSession(t, true) // vision-capable, temp workspace, real transcript
	raw := attachTestPNG(41, 80)
	input := "what is in this screenshot @shots/ui.png"
	if _, err := cs.TurnWithAttachments(context.Background(), input, TurnImage{
		Ref:  "shots/ui.png",
		Part: tools.ImagePart{MediaType: "image/png", Path: "shots/ui.png", DataURI: attachTestDataURI(raw)},
	}); err != nil {
		t.Fatalf("TurnWithAttachments: %v", err)
	}
	transcriptPath := filepath.Join(cs.SessionsDir(), cs.SessionID+".jsonl")

	vm, err := buildTranscriptViewModel(transcriptPath)
	if err != nil {
		t.Fatalf("buildTranscriptViewModel: %v", err)
	}
	var userEntry *transcriptEntry
	for i := range vm.Entries {
		if vm.Entries[i].Role == RoleUser {
			userEntry = &vm.Entries[i]
			break
		}
	}
	if userEntry == nil {
		t.Fatal("no user entry in the built view-model")
	}
	// The human's text survived exactly as typed (no marker was appended)...
	if userEntry.Content != input {
		t.Errorf("user content = %q, want the input as typed", userEntry.Content)
	}
	// ...and the attachment is still shown, from the manifest.
	if len(userEntry.Images) == 0 {
		t.Fatalf("user entry images = %+v, want the manifest's attachment", userEntry.Images)
	}
	img := userEntry.Images[0]
	if img.Name != "shots/ui.png" {
		t.Errorf("name = %q, want the reference the human attached", img.Name)
	}
	if img.MediaType != "image/png" {
		t.Errorf("media_type = %q, want image/png", img.MediaType)
	}
	// A local attachment has no loadable URL, same rule as the marker path.
	if img.URL != "" {
		t.Errorf("url = %q, want empty for a local attachment", img.URL)
	}
}

func TestTranscriptImagesForEdgeShapes(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantLen int
		wantRef string
	}{
		{"plain text", "just a sentence", 0, ""},
		{"empty", "", 0, ""},
		{"a marker naming a path", "[image:a.png image/png 10 bytes ≈3 tokens attached as an image part]", 1, "a.png"},
		{"a marker with no fields", "[image:]", 0, ""},
		{"a bare marker prefix", "[image:", 0, ""},
		{"a marker whose second field is not a media type", "[image:a.png 12 bytes attached as an image part]", 1, "a.png"},
		// A near-miss must not be read as an image: prose that merely mentions
		// the word would otherwise gain an attachment the turn never carried.
		{"prose mentioning image", "the image looks fine", 0, ""},
		// A marker embedded in surrounding prose is found (the scan, not a
		// whole-string prefix test, is what makes this work).
		{"marker inside prose", `read it: [image:a.png image/png 10 bytes ≈3 tokens attached as an image part] done`, 1, "a.png"},
		{"two markers, both found", `[image:a.png image/png 10 bytes ≈3 tokens attached as an image part] and [image:b.jpg image/jpeg 20 bytes ≈6 tokens attached as an image part]`, 2, "a.png"},
		{"an unterminated marker is ignored", "[image:a.png image/png 10 bytes", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := transcriptImagesFor(Message{Role: RoleUser, Content: tt.content})
			if len(got) != tt.wantLen {
				t.Fatalf("images = %+v, want %d", got, tt.wantLen)
			}
			if tt.wantLen > 0 && got[0].Name != tt.wantRef {
				t.Errorf("name = %q, want %q", got[0].Name, tt.wantRef)
			}
			// Two-marker case: the second one must be found too.
			if tt.wantLen == 2 && got[1].Name != "b.jpg" {
				t.Errorf("second name = %q, want b.jpg", got[1].Name)
			}
		})
	}
}

// TestSessionScreenRendersAttachedImage is the renderer half: session.js must
// render an entry's images, and must NOT claim to show one it cannot load.
func TestSessionScreenRendersAttachedImage(t *testing.T) {
	src := readWebUIJS(t, "session.js")

	if !strings.Contains(src, "renderAttachedImages") {
		t.Fatal("session.js defines no attached-image renderer")
	}
	// It must be CALLED from the per-entry renderer, or it is dead code: an
	// image-bearing entry would render exactly as it did before this feature.
	callIdx := strings.LastIndex(src, "renderAttachedImages(container, e)")
	defIdx := strings.Index(src, "function renderAttachedImages")
	if callIdx < 0 {
		t.Fatal("session.js never calls renderAttachedImages for an entry")
	}
	if strings.Index(src, "function renderEntry") > callIdx {
		t.Error("renderAttachedImages is called outside renderEntry, so entries never show images")
	}
	if defIdx < 0 || callIdx < defIdx {
		t.Error("renderAttachedImages is called before it is defined")
	}

	t.Run("a loadable image renders as an img with alt text", func(t *testing.T) {
		for _, want := range []string{`el("img"`, "alt:", "attach-img"} {
			if !strings.Contains(src, want) {
				t.Errorf("session.js is missing %s for the loadable-image case", want)
			}
		}
	})

	t.Run("an unloadable image is labelled, not rendered broken", func(t *testing.T) {
		// The local-attachment case: no URL exists, so the transcript shows a
		// caption instead. Asserting the label exists is what pins that
		// behavior rather than a silent omission.
		if !strings.Contains(src, "attach-cap") {
			t.Error("session.js renders no caption for an attached image")
		}
		if !strings.Contains(src, "bytes held in the session") {
			t.Error("session.js does not say where an unloadable image's bytes are")
		}
	})

	t.Run("nothing renders for an entry with no images", func(t *testing.T) {
		// The guard is what keeps every existing text-only transcript
		// byte-for-byte: no container, no empty box.
		idx := strings.Index(src, "imgs.length === 0")
		if idx < 0 {
			t.Fatal("session.js has no empty-images guard")
		}
		if !strings.Contains(src[idx:idx+80], "return") {
			t.Error("the empty-images guard does not return early")
		}
	})
}
