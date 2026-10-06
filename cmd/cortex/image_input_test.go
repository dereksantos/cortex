package main

// image_input_test.go — #217's engine-side acceptance: image parts count
// toward the context window and the /context legend; an old turn's image
// demotes to an outline line naming the image with a citation recall can
// resolve (with the on-disk side-car named).

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/cache"
	"github.com/dereksantos/cortex/internal/tools"
	"github.com/dereksantos/cortex/pkg/llm"
)

// pngDataURI builds a data URI whose decoded payload is exactly n bytes,
// so the /3 image estimate is predictable.
func pngDataURI(t *testing.T, n int) string {
	t.Helper()
	raw := make([]byte, n)
	for i := range raw {
		raw[i] = byte(i % 251)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw)
}

func TestEstTurnTokensImages(t *testing.T) {
	tests := []struct {
		name string
		msgs []Message
		want int // exact expected total
	}{
		{
			"text-only turn unchanged",
			[]Message{{Role: "user", Content: strings.Repeat("a", 400)}},
			cache.TokensOf(400),
		},
		{
			"image part booked at bytes/3 on top of its marker text",
			[]Message{{Role: "tool", ToolCallID: "c1",
				Content: "[image:shot.png image/png 900 bytes ≈300 tokens attached as an image part]",
				Parts:   []llm.ContentPart{llm.ImageURLPart(pngDataURI(t, 900), "")}}},
			cache.TokensOf(len("[image:shot.png image/png 900 bytes ≈300 tokens attached as an image part]")+len("c1")) + tools.ImageTokensOf(900),
		},
		{
			"resumed marker-only image booked at the cap estimate",
			[]Message{{Role: "tool", ToolCallID: "c1",
				Content: "[image:shot.png image/png 900 bytes ≈300 tokens attached as an image part]"}},
			cache.TokensOf(len("[image:shot.png image/png 900 bytes ≈300 tokens attached as an image part]")+len("c1")) +
				tools.ImageTokensOf(defaultImageTokenBookingBytes),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := estTurnTokens(tt.msgs); got != tt.want {
				t.Errorf("estTurnTokens = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestImageDataURIRawBytes(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want int
	}{
		{"empty data uri", "data:image/png;base64,", 0},
		{"no padding", pngDataURI(t, 3), 3},
		{"one pad byte", pngDataURI(t, 2), 2},
		{"two pad bytes", pngDataURI(t, 1), 1},
		{"http url booked at the cap", "https://x.test/a.png", defaultImageTokenBookingBytes},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := imageDataURIRawBytes(tt.url); got != tt.want {
				t.Errorf("imageDataURIRawBytes(%q) = %d, want %d", tt.url, got, tt.want)
			}
		})
	}
}

func TestContextLegendShowsImages(t *testing.T) {
	cs := &CortexSession{Request: CortexArgs{}.Request()}
	cs.Request.Vision = true
	cs.StartTranscript()
	defer cs.transcript.Close()

	sys := Message{Role: "user", Content: "sys"}
	img := Message{Role: "tool", ToolCallID: "c1",
		Content: "[image:shot.png image/png 1200 bytes ≈400 tokens attached as an image part]",
		Parts:   []llm.ContentPart{llm.ImageURLPart(pngDataURI(t, 1200), "")}}
	cs.Append(sys)
	cs.Append(img)
	cs.ws = cs.newWorkingSet(1)
	cs.ws.AddTurn(cache.TurnSpan{Start: 1, End: 3, Tokens: estTurnTokens(cs.Request.Messages[1:])})

	if got := cs.hydratedImageTokens(); got != tools.ImageTokensOf(1200) {
		t.Errorf("hydratedImageTokens = %d, want %d", got, tools.ImageTokensOf(1200))
	}
	report := cs.contextReport()
	if !strings.Contains(report, "images") || !strings.Contains(report, "1 image in the tail") {
		t.Errorf("context report lacks the images legend row:\n%s", report)
	}

	t.Run("no images no row", func(t *testing.T) {
		plain := &CortexSession{Request: CortexArgs{}.Request()}
		plain.StartTranscript()
		defer plain.transcript.Close()
		plain.Append(Message{Role: "user", Content: "hi"})
		plain.ws = plain.newWorkingSet(1)
		plain.ws.AddTurn(cache.TurnSpan{Start: 1, End: 2, Tokens: 1})
		if got := plain.hydratedImageTokens(); got != 0 {
			t.Errorf("text-only session images = %d, want 0", got)
		}
		if r := plain.contextReport(); strings.Contains(r, "in the tail") {
			t.Errorf("text-only report must not carry an images row:\n%s", r)
		}
	})

	// An image behind the demotion frontier is no longer in the window, so
	// it leaves the legend too — the outline stands in for it (#217: it
	// must not stay in the prompt forever).
	t.Run("demoted images leave the legend", func(t *testing.T) {
		h, l := cs.ws.GetWatermarks()
		if err := cs.ws.RestoreState(1, h, l); err != nil {
			t.Fatalf("RestoreState: %v", err)
		}
		if got := cs.hydratedImageTokens(); got != 0 {
			t.Errorf("after demotion hydratedImageTokens = %d, want 0", got)
		}
		if r := cs.contextReport(); strings.Contains(r, "in the tail") {
			t.Errorf("demoted image must leave the images row:\n%s", r)
		}
	})
}

// TestImageDemotesToRecallableOutline drives the real turn-end path's
// pieces: a turn whose tool result carries an image (spliced + side-car
// written through the real hook), demoted through turnOutlineEntry, must
// produce an outline line naming the image with a citation whose recall
// names the side-car bytes on disk.
func TestImageDemotesToRecallableOutline(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	cs := &CortexSession{Request: CortexArgs{}.Request()}
	cs.Request.Vision = true
	// The transcript mirrors the request 1:1, so the side-car key equals
	// the message's request index — but it must be the SAME file recall
	// reads, so the transcript has to exist before the messages are added.
	cs.StartTranscript()
	defer cs.transcript.Close()
	id := cs.SessionID

	pngRaw := append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, []byte("PNGDATA")...)
	// The real splice hook: record through the production path (tools'
	// Execute seam records the slot inside dispatch; the test records the
	// same way readFile does by reading the fixture back through Execute).
	path := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(path, pngRaw, 0o644); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	obs, err := tools.Execute(t.Context(), tools.ToolCall{ID: "c1", Type: "function",
		Function: tools.FunctionCall{Name: tools.FunctionReadFile, Arguments: `{"path":"` + path + `"}`}}, cs)
	if err != nil {
		t.Fatalf("execute read_file: %v", err)
	}
	msg := Message{Role: RoleTool, ToolCallID: "c1", Content: obs}
	cs.spliceImageResult(&msg)
	// Production ordering (loop.go): the assistant message, the
	// SpliceImages hook, then writeImageSideCarAt with the index appendMsg
	// gives this message, then Append.
	cs.Append(Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Type: "function", Function: FunctionCall{Name: "read_file", Arguments: `{"path":"shot.png"}`}}}})
	cs.spliceImageResult(&msg)
	if len(msg.Parts) == 0 || !msg.Parts[len(msg.Parts)-1].HasImage() {
		t.Fatalf("splice produced no image parts: %+v", msg)
	}
	cs.writeImageSideCarAt(&msg, len(cs.Request.Messages))
	cs.Append(msg)

	// Demotion: the outline entry names the image, never the bytes.
	span := cache.TurnSpan{Start: 1, End: 3}
	entry := turnOutlineEntry(1, span, cs.Request.Messages[1:], cs.SessionID)
	if !strings.Contains(strings.Join(entry.Actions, " "), "image attached") {
		t.Errorf("outline actions must name the image: %v", entry.Actions)
	}
	for _, a := range entry.Actions {
		if strings.Contains(a, "base64,") {
			t.Errorf("outline must carry no base64 payload: %q", a)
		}
	}
	if entry.Citation == "" {
		t.Fatal("citation required")
	}

	// Recall resolves the citation and names the side-car with the bytes.
	got, err := cs.Recall(entry.Citation)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if !strings.Contains(got, "image bytes on disk:") {
		t.Errorf("recall must name the side-car path: %q", got)
	}
	matches, _ := filepath.Glob(filepath.Join(cs.SessionsDir(), id+".m*"))
	if len(matches) != 1 {
		t.Fatalf("side-car files = %v, want exactly one", matches)
	}
	raw, readErr := os.ReadFile(matches[0])
	if readErr != nil || !bytes.Equal(raw, pngRaw) {
		t.Errorf("side-car bytes = %q, err %v", raw, readErr)
	}
}

// TestSpliceImageResultNoVision: with the vision verdict off, nothing
// splices even if a stray attachment is pending.
func TestSpliceImageResultGuards(t *testing.T) {
	cs := &CortexSession{Request: CortexArgs{}.Request()}
	cs.Request.Vision = false
	msg := Message{Role: RoleTool, Content: "[image:x.png image/png 3 bytes ≈1 tokens attached as an image part]"}
	cs.spliceImageResult(&msg)
	if len(msg.Parts) != 0 {
		t.Errorf("no splice without vision: %+v", msg.Parts)
	}
	if cs.ImageInputEnabled() {
		t.Error("ImageInputEnabled must follow Request.Vision")
	}
}
