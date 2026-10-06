package main

// image_input_test.go — #217's engine-side acceptance: image parts count
// toward the context window and the /context legend; an old turn's image
// demotes to an outline line naming the image with a citation recall can
// resolve (with the on-disk side-car named).

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
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
			// A resumed image result (marker text, no wire Parts) books NO
			// image tokens: after resume no image goes on the wire, only the
			// ~30-token marker — whose bytes the len(Content) term already
			// counted. (Booking it at the cap added a phantom 500k tokens,
			// more than a whole 131k window; the expectation below is what
			// the marker-only shape actually costs.)
			"resumed marker-only image books only its marker text",
			[]Message{{Role: "tool", ToolCallID: "c1",
				Content: "[image:shot.png image/png 900 bytes ≈300 tokens attached as an image part]"}},
			cache.TokensOf(len("[image:shot.png image/png 900 bytes ≈300 tokens attached as an image part]") + len("c1")),
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

// imageCallResp returns an AgentResponse carrying one read_file tool call.
func imageCallResp(id, path string) *AgentResponse {
	args, _ := json.Marshal(map[string]any{"path": path})
	return &AgentResponse{
		Choices: []Choice{{FinishReason: "tool_calls", Message: Message{
			Role: "assistant",
			ToolCalls: []ToolCall{{
				ID:       id,
				Type:     "function",
				Function: FunctionCall{Name: "read_file", Arguments: string(args)},
			}},
		}}},
	}
}

// TestImageDemotesToRecallableOutline is the end-to-end acceptance for
// the side-car contract (issue #217): it drives the REAL runLoop with the
// coder's Toolset (SpliceImages/WriteImageSideCar wired exactly as
// turn.go wires them, appendMsg = cs.Append, a scripted Sender that
// issues one read_file on a PNG then finalizes), demotes the turn through
// the real turnOutlineEntry, and recalls the citation. The side-car must
// be written under the index the message actually landed at — the one
// Recall looks up — so recall names the file and its bytes must equal the
// fixture's. Driving production ordering matters: a test that calls
// writeImageSideCarAt before the append measures the wrong index and
// passes while production writes the side-car one index past the message.
func TestImageDemotesToRecallableOutline(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	pngRaw := append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, []byte("PNGDATA")...)
	path := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(path, pngRaw, 0o644); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	cs := &CortexSession{
		workspace: ws,
		Window:    20000,
		SessionID: "image-input-test",
		Request:   CortexArgs{}.Request(),
	}
	cs.Request.Model = "m"
	cs.Request.Vision = true
	cs.ws = cs.newWorkingSet(1)
	cs.StartTranscript()
	defer cs.Close()
	id := cs.SessionID

	// The scripted coder: round 1 asks for read_file on the PNG, round 2
	// answers. The real coderDispatcher runs the read through the session
	// (ImageSink records on cs), and the Toolset below is the coder's,
	// spliced + side-car wired as in turn.go.
	var i int
	script := []*AgentResponse{
		imageCallResp("c1", "shot.png"),
		answerResp("saw it"),
	}
	send := SenderFunc(func(_ context.Context, _ *AgentRequest) (*AgentResponse, bool, error) {
		r := script[i]
		if i < len(script)-1 {
			i++
		}
		return r, false, nil
	})
	ts := Toolset{
		Dispatch:          cs.coderDispatcher(),
		SpliceImages:      cs.spliceImageResult,
		WriteImageSideCar: cs.writeImageSideCarAt,
	}
	turnStart := len(cs.Request.Messages)
	if _, _, err := runLoop(t.Context(), send, cs.Request, ts, Bounds{MaxIter: 5}, nil, cs.Append, nil); err != nil {
		t.Fatalf("runLoop: %v", err)
	}

	// The turn ran and the tool result carries the wire Parts.
	toolIdx := -1
	for j := turnStart; j < len(cs.Request.Messages); j++ {
		m := cs.Request.Messages[j]
		if m.Role == RoleTool && tools.IsImageObservation(m.Content) {
			toolIdx = j
			if len(m.Parts) == 0 || !m.Parts[len(m.Parts)-1].HasImage() {
				t.Fatalf("the appended tool message carries no image parts: %+v", m)
			}
		}
	}
	if toolIdx < 0 {
		t.Fatal("no image tool result was appended")
	}

	// Demotion: the outline entry names the image, never the bytes.
	span := cache.TurnSpan{Start: turnStart, End: len(cs.Request.Messages)}
	entry := turnOutlineEntry(1, span, cs.Request.Messages[turnStart:], cs.SessionID)
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
	// On the current-code index bug the side-car sits one past the
	// message and recall prints "image bytes no longer on disk" instead.
	got, err := cs.Recall(entry.Citation)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if !strings.Contains(got, "image bytes on disk:") {
		t.Errorf("recall must name the side-car path: %q", got)
	}
	wantSideCar := filepath.Join(cs.SessionsDir(), fmt.Sprintf("%s.m%d.png", id, toolIdx))
	raw, readErr := os.ReadFile(wantSideCar)
	if readErr != nil {
		matches, _ := filepath.Glob(filepath.Join(cs.SessionsDir(), id+".m*"))
		t.Fatalf("side-car at %s: %v (found %v)", wantSideCar, readErr, matches)
	}
	if !bytes.Equal(raw, pngRaw) {
		t.Errorf("side-car bytes = %q, want the fixture's %q", raw, pngRaw)
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
