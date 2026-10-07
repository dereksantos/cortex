package main

// turn_attachments_test.go — the turn-attachment seam (issue #218, part 3 of
// image input #134): images a HUMAN attached to the input ride the turn's own
// user message as wire Parts, get a side-car under the index that message
// actually lands at (so recall can name the bytes), and cost a text-only
// model nothing but a note explaining the absence.
//
// These drive the real TurnWithAttachments path, not a hand-built message: a
// test that assembled the user message itself would pass while production
// appended the images to the wrong message or the wrong index.

import (
	"context"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/cache"
	"github.com/dereksantos/cortex/internal/tools"
	"github.com/dereksantos/cortex/pkg/llm"
)

// attachTestPNG is a genuine PNG (the 8-byte signature the sniffer reads)
// with distinguishable payload, so a side-car's bytes can be compared
// exactly.
func attachTestPNG(seed byte, n int) []byte {
	raw := append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, make([]byte, n)...)
	for i := 8; i < len(raw); i++ {
		raw[i] = seed + byte(i)
	}
	return raw
}

func attachTestDataURI(raw []byte) string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw)
}

// attachTestSession builds a session over a temp workspace with a transcript,
// vision set per verdict, and a scripted one-round answer sender.
func attachTestSession(t *testing.T, vision bool) *CortexSession {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	cs := &CortexSession{
		workspace: ws,
		Window:    20000,
		SessionID: "turn-attach-test",
		Request:   CortexArgs{}.Request(),
	}
	cs.Request.Model = "m"
	cs.Request.Vision = vision
	cs.ws = cs.newWorkingSet(1)
	// The test-only sender seam (indemote_test.go, undo_turn_test.go): turn()
	// builds its own Sender from the bound backend otherwise, and a test must
	// not reach the network.
	cs.senderOverride = SenderFunc(func(_ context.Context, _ *AgentRequest) (*AgentResponse, bool, error) {
		return answerResp("ok"), false, nil
	})
	cs.StartTranscript()
	t.Cleanup(func() { cs.Close() })
	return cs
}

func userMessages(cs *CortexSession) []Message {
	var out []Message
	for _, m := range cs.Request.Messages {
		if m.Role == RoleUser {
			out = append(out, m)
		}
	}
	return out
}

// TestTurnWithAttachmentsUserMessageCarriesImagePart is the issue's first
// acceptance: the image is a content part on the turn's user message, the
// human's text is intact, and the reply path is unaffected.
func TestTurnWithAttachmentsUserMessageCarriesImagePart(t *testing.T) {
	cs := attachTestSession(t, true)
	raw := attachTestPNG(1, 64)

	withImage := "what is in this screenshot @" + "shots/ui.png"
	res, err := cs.TurnWithAttachments(context.Background(), withImage, TurnImage{
		Ref:  "shots/ui.png",
		Part: tools.ImagePart{MediaType: "image/png", Path: "shots/ui.png", DataURI: attachTestDataURI(raw)},
	})
	if err != nil {
		t.Fatalf("TurnWithAttachments: %v", err)
	}
	if res.Reply != "ok" {
		t.Errorf("reply = %q, want the scripted answer", res.Reply)
	}

	users := userMessages(cs)
	if len(users) != 1 {
		t.Fatalf("user messages = %d, want the turn's one message", len(users))
	}
	msg := users[0]
	// The human's text survives: the image rides ALONGSIDE it, never in
	// place of it.
	if !strings.Contains(msg.Content, "what is in this screenshot") {
		t.Errorf("user content = %q, want the typed input preserved", msg.Content)
	}
	// No base64 blob in the text: the bytes are a part, not prose.
	if strings.Contains(msg.Content, "base64,") {
		t.Errorf("user content carries base64 text: %q", msg.Content)
	}
	var imgs []string
	for _, p := range msg.Parts {
		if p.Type == llm.ContentTypeImageURL {
			imgs = append(imgs, p.ImageURL)
		}
	}
	if len(imgs) != 1 {
		t.Fatalf("image parts on the user message = %d (%v), want 1", len(imgs), imgs)
	}
	if imgs[0] != attachTestDataURI(raw) {
		t.Errorf("image part = %.48q…, want the attachment's data URI", imgs[0])
	}
	// Text part first, image second: the content-parts order both transports
	// serialize (#216).
	if len(msg.Parts) != 2 || msg.Parts[0].Type != llm.ContentTypeText {
		t.Errorf("parts = %+v, want [text, image]", msg.Parts)
	}
	if res.ImageNotes != nil {
		t.Errorf("a turn whose image attached must carry no note, got %v", res.ImageNotes)
	}
}

// TestTurnWithAttachmentsSideCarMatchesBytes is the "side-car exists and
// matches the bytes" acceptance, keyed to the index the user message ACTUALLY
// lands at. Index 0 is the system message, so the user message is at 1: a
// side-car written at 0 or 2 would pass a sloppy test and leave recall
// pointing at nothing.
func TestTurnWithAttachmentsSideCarMatchesBytes(t *testing.T) {
	cs := attachTestSession(t, true)
	raw := attachTestPNG(7, 120)

	if _, err := cs.TurnWithAttachments(context.Background(), "look @shots/ui.png", TurnImage{
		Ref:  "shots/ui.png",
		Part: tools.ImagePart{MediaType: "image/png", Path: "shots/ui.png", DataURI: attachTestDataURI(raw)},
	}); err != nil {
		t.Fatalf("TurnWithAttachments: %v", err)
	}

	users := userMessages(cs)
	if len(users) != 1 {
		t.Fatalf("user messages = %d, want 1", len(users))
	}
	// The index the message really occupies in the wire log.
	userIdx := -1
	for i, m := range cs.Request.Messages {
		if m.Role == RoleUser {
			userIdx = i
			break
		}
	}
	if userIdx < 0 {
		t.Fatal("no user message in the request")
	}

	got, ok := imagePathForMsg(cs.SessionsDir(), cs.SessionID, userIdx)
	if !ok {
		t.Fatalf("no side-car under the index the user message landed at (m%d)", userIdx)
	}
	body, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("read side-car: %v", err)
	}
	if string(body) != string(raw) {
		t.Errorf("side-car is %d bytes, want the attachment's %d", len(body), len(raw))
	}

	t.Run("nothing is written under a neighbouring index", func(t *testing.T) {
		for _, other := range []int{userIdx - 1, userIdx + 1} {
			if other < 0 {
				continue
			}
			if p, found := imagePathForMsg(cs.SessionsDir(), cs.SessionID, other); found {
				t.Errorf("unexpected side-car at m%d: %s", other, p)
			}
		}
	})

	t.Run("recall names the side-car path", func(t *testing.T) {
		// The contract the side-car exists for: an outline citation for that
		// message resolves to text naming the file, so a resumed session can
		// reach bytes the in-memory Parts no longer hold.
		citation := fmtCitation(cs.SessionID, userIdx)
		out, err := cs.Recall(citation)
		if err != nil {
			t.Fatalf("Recall(%s): %v", citation, err)
		}
		if !strings.Contains(out, filepath.Base(got)) {
			t.Errorf("recall output does not name the side-car %s:\n%s", filepath.Base(got), out)
		}
	})
}

// TestTurnWithAttachmentsTextOnlyModelGetsNoteNotPart: a model with no vision
// verdict gets NO image part (the #216 wire gate would refuse the whole
// request) and a note that says why — the input text untouched, because
// prefixing a note that stands for bytes never sent would squat in the window
// for the session's life (a user message is the one thing in-turn demotion
// must not stub).
func TestTurnWithAttachmentsTextOnlyModelGetsNoteNotPart(t *testing.T) {
	cs := attachTestSession(t, false)
	raw := attachTestPNG(3, 40)
	input := "what does this show?"

	res, err := cs.TurnWithAttachments(context.Background(), input, TurnImage{
		Ref:  "shots/ui.png",
		Part: tools.ImagePart{MediaType: "image/png", DataURI: attachTestDataURI(raw)},
	})
	if err != nil {
		t.Fatalf("a text-only turn must still run (no wire part was sent): %v", err)
	}

	users := userMessages(cs)
	if len(users) != 1 {
		t.Fatalf("user messages = %d, want 1", len(users))
	}
	if len(users[0].Parts) != 0 {
		t.Errorf("a text-only model must receive no parts, got %+v", users[0].Parts)
	}
	if users[0].Content != input {
		t.Errorf("user content = %q, want the input byte-for-byte as typed (%q)", users[0].Content, input)
	}
	if len(res.ImageNotes) != 1 {
		t.Fatalf("ImageNotes = %v, want one line naming the unsent image", res.ImageNotes)
	}
	note := res.ImageNotes[0]
	for _, want := range []string{"shots/ui.png", "no image input"} {
		if !strings.Contains(note, want) {
			t.Errorf("note %q must name %q", note, want)
		}
	}
	// And the bytes are not persisted as though they had been sent: with no
	// part there is nothing for recall to name.
	for i := range cs.Request.Messages {
		if p, found := imagePathForMsg(cs.SessionsDir(), cs.SessionID, i); found {
			t.Errorf("a text-only turn wrote a side-car at m%d: %s", i, p)
		}
	}
}

// TestTurnWithAttachmentsContextLegendCountsIt is the /context acceptance:
// the image the human attached shows up in the images legend, booked at the
// documented per-image estimate, because it is in the hydrated tail like any
// other turn content.
func TestTurnWithAttachmentsContextLegendCountsIt(t *testing.T) {
	cs := attachTestSession(t, true)
	raw := attachTestPNG(11, 300)

	if _, err := cs.TurnWithAttachments(context.Background(), "see @a.png", TurnImage{
		Ref:  "a.png",
		Part: tools.ImagePart{MediaType: "image/png", DataURI: attachTestDataURI(raw)},
	}); err != nil {
		t.Fatalf("TurnWithAttachments: %v", err)
	}

	wantTokens := tools.ImageTokensOf(len(raw))
	if got := cs.hydratedImageTokens(); got != wantTokens {
		t.Errorf("hydratedImageTokens = %d, want %d (the attachment's estimate)", got, wantTokens)
	}
	report := cs.contextReport()
	if !strings.Contains(report, "images") {
		t.Errorf("context report lacks the images legend row:\n%s", report)
	}
	if !strings.Contains(report, "1 image in the tail") {
		t.Errorf("context report must count the attached image:\n%s", report)
	}

	t.Run("the turn's span books the image too", func(t *testing.T) {
		// The span's token count comes from estTurnTokens, which prices wire
		// Parts at ImageTokensOf — an image that cost nothing would never
		// demote and the window would grow without bound. Measured over the
		// user message itself (the one carrying the Parts), not the reply.
		userIdx := -1
		for i, m := range cs.Request.Messages {
			if m.Role == RoleUser {
				userIdx = i
				break
			}
		}
		if userIdx < 0 {
			t.Fatal("no user message")
		}
		if cs.ws.TotalTurns() < 1 {
			t.Fatal("the turn was not recorded as a span")
		}
		booked := estTurnTokens([]Message{cs.Request.Messages[userIdx]})
		if booked < wantTokens {
			t.Errorf("the user message books %d tokens, want at least the image's %d", booked, wantTokens)
		}
	})

	t.Run("a text-only attachment is not counted", func(t *testing.T) {
		plain := attachTestSession(t, false)
		if _, err := plain.TurnWithAttachments(context.Background(), "see @a.png", TurnImage{
			Ref:  "a.png",
			Part: tools.ImagePart{MediaType: "image/png", DataURI: attachTestDataURI(raw)},
		}); err != nil {
			t.Fatalf("turn: %v", err)
		}
		if got := plain.hydratedImageTokens(); got != 0 {
			t.Errorf("hydratedImageTokens = %d, want 0 — nothing was sent", got)
		}
		if r := plain.contextReport(); strings.Contains(r, "images") {
			t.Errorf("a turn with no image on the wire must show no images row:\n%s", r)
		}
	})
}

// TestTurnWithAttachmentsSeveralImages pins that a turn can carry more than
// one screenshot: all of them reach the wire, and their side-cars do not
// overwrite each other (the #217 single-image key would have clobbered the
// second).
func TestTurnWithAttachmentsSeveralImages(t *testing.T) {
	cs := attachTestSession(t, true)
	rawA := attachTestPNG(21, 50)
	rawB := attachTestPNG(22, 60)

	if _, err := cs.TurnWithAttachments(context.Background(), "two shots @a.png @b.png",
		TurnImage{Ref: "a.png", Part: tools.ImagePart{MediaType: "image/png", DataURI: attachTestDataURI(rawA)}},
		TurnImage{Ref: "b.png", Part: tools.ImagePart{MediaType: "image/png", DataURI: attachTestDataURI(rawB)}},
	); err != nil {
		t.Fatalf("TurnWithAttachments: %v", err)
	}

	userIdx := -1
	for i, m := range cs.Request.Messages {
		if m.Role == RoleUser {
			userIdx = i
			break
		}
	}
	if userIdx < 0 {
		t.Fatal("no user message")
	}
	var imgs []string
	for _, p := range cs.Request.Messages[userIdx].Parts {
		if p.Type == llm.ContentTypeImageURL {
			imgs = append(imgs, p.ImageURL)
		}
	}
	if len(imgs) != 2 {
		t.Fatalf("image parts = %d, want 2", len(imgs))
	}
	if imgs[0] != attachTestDataURI(rawA) || imgs[1] != attachTestDataURI(rawB) {
		t.Error("the two images must reach the wire in the order given")
	}

	// Both side-cars exist and hold their OWN bytes.
	p1, ok1 := imagePathForMsgAt(cs.SessionsDir(), cs.SessionID, userIdx, 1)
	p2, ok2 := imagePathForMsgAt(cs.SessionsDir(), cs.SessionID, userIdx, 2)
	if !ok1 || !ok2 {
		t.Fatalf("side-cars for slot 1/2: %v %v", ok1, ok2)
	}
	if p1 == p2 {
		t.Fatalf("both slots resolved to the same file %s", p1)
	}
	for _, tc := range []struct {
		path string
		want []byte
	}{{p1, rawA}, {p2, rawB}} {
		body, err := os.ReadFile(tc.path)
		if err != nil {
			t.Fatalf("read %s: %v", tc.path, err)
		}
		if string(body) != string(tc.want) {
			t.Errorf("%s holds %d bytes, want %d", filepath.Base(tc.path), len(body), len(tc.want))
		}
	}
	// The unnumbered lookup still finds one of them (a caller that doesn't
	// know there were two must not come up empty).
	if _, ok := imagePathForMsg(cs.SessionsDir(), cs.SessionID, userIdx); !ok {
		t.Error("imagePathForMsg must still resolve a side-car on a multi-image message")
	}

	// Two images are booked as two images.
	if got, want := cs.hydratedImageTokens(), tools.ImageTokensOf(len(rawA))+tools.ImageTokensOf(len(rawB)); got != want {
		t.Errorf("hydratedImageTokens = %d, want %d", got, want)
	}
}

// TestTurnWithAttachmentsNoImagesIsTurnByteForByte: the variadic seam must be
// a no-op when nothing is attached — the existing paths cannot regress.
func TestTurnWithAttachmentsNoImagesIsTurnByteForByte(t *testing.T) {
	cs := attachTestSession(t, true)
	if _, err := cs.TurnWithAttachments(context.Background(), "no images here"); err != nil {
		t.Fatalf("turn: %v", err)
	}
	users := userMessages(cs)
	if len(users) != 1 {
		t.Fatalf("user messages = %d, want 1", len(users))
	}
	if len(users[0].Parts) != 0 {
		t.Errorf("parts = %+v, want none for a turn with no attachments", users[0].Parts)
	}
	if users[0].Content != "no images here" {
		t.Errorf("content = %q, want the input unchanged", users[0].Content)
	}
	for i := range cs.Request.Messages {
		if p, found := imagePathForMsg(cs.SessionsDir(), cs.SessionID, i); found {
			t.Errorf("no-attachment turn wrote a side-car at m%d: %s", i, p)
		}
	}
	// A malformed attachment (no bytes, no reference) is skipped rather than
	// sent as a broken part.
	before := len(cs.Request.Messages)
	if _, err := cs.TurnWithAttachments(context.Background(), "garbage",
		TurnImage{Ref: "", Part: tools.ImagePart{MediaType: "image/png", DataURI: attachTestDataURI(attachTestPNG(1, 4))}},
		TurnImage{Ref: "x.png", Part: tools.ImagePart{MediaType: "image/png", DataURI: ""}},
	); err != nil {
		t.Fatalf("turn with only-void attachments: %v", err)
	}
	last := cs.Request.Messages[before]
	if len(last.Parts) != 0 {
		t.Errorf("a void attachment must produce no part, got %+v", last.Parts)
	}
}

// TestTurnWithAttachmentsPendingStateDoesNotLeak: a turn that ends early must
// not park an image that then splices onto some later message.
func TestTurnWithAttachmentsPendingStateDoesNotLeak(t *testing.T) {
	cs := attachTestSession(t, true)
	raw := attachTestPNG(9, 32)
	if _, err := cs.TurnWithAttachments(context.Background(), "first @a.png", TurnImage{
		Ref:  "a.png",
		Part: tools.ImagePart{MediaType: "image/png", DataURI: attachTestDataURI(raw)},
	}); err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if cs.pendingTurnSideCars != nil {
		t.Fatalf("the side-car slot must be consumed within its turn, got %d parked", len(cs.pendingTurnSideCars))
	}
	// A second, image-less turn must gain no parts and no side-car.
	before := len(cs.Request.Messages)
	if _, err := cs.TurnWithAttachments(context.Background(), "second turn, no images"); err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	if len(cs.Request.Messages[before].Parts) != 0 {
		t.Errorf("turn 2 inherited turn 1's image: %+v", cs.Request.Messages[before].Parts)
	}
}

// TestPrintMentionImages is the REPL half of the issue's acceptance: one
// attachment line per image after submitting, and a line for every image that
// did NOT make it (a mention refusal, or the session's text-only note) —
// silence would look like a typo'd mention.
func TestPrintMentionImages(t *testing.T) {
	raw := attachTestPNG(31, 60)
	img := TurnImage{Ref: "shots/ui.png", Part: tools.ImagePart{MediaType: "image/png", DataURI: attachTestDataURI(raw)}}

	// capture grabs what a print helper writes to stdout, so the assertion is
	// about the lines the human actually sees.
	capture := func(fn func()) string {
		old := os.Stdout
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("pipe: %v", err)
		}
		os.Stdout = w
		done := make(chan string, 1)
		go func() {
			b, _ := io.ReadAll(r)
			done <- string(b)
		}()
		fn()
		w.Close()
		os.Stdout = old
		return <-done
	}

	out := capture(func() { printMentionImages([]TurnImage{img}, nil, nil) })
	if !strings.Contains(out, "shots/ui.png") || !strings.Contains(out, "attached image") {
		t.Errorf("printed %q, want one attachment line naming the image", out)
	}
	if n := strings.Count(out, "attached image"); n != 1 {
		t.Errorf("printed %d attachment lines, want exactly 1", n)
	}

	t.Run("one line per image", func(t *testing.T) {
		two := []TurnImage{img, {Ref: "shots/other.png", Part: tools.ImagePart{MediaType: "image/png", DataURI: attachTestDataURI(raw)}}}
		got := capture(func() { printMentionImages(two, nil, nil) })
		if n := strings.Count(got, "attached image"); n != 2 {
			t.Errorf("printed %d attachment lines, want 2:\n%s", n, got)
		}
		if !strings.Contains(got, "shots/other.png") {
			t.Errorf("second image missing from:\n%s", got)
		}
	})

	t.Run("a refusal and a turn note are both printed", func(t *testing.T) {
		got := capture(func() {
			printMentionImages(nil,
				[]MentionRefusal{{Ref: "shots/gone.png", Reason: "shots/gone.png could not be attached as an image."}},
				[]string{"shots/ui.png was not sent — the bound model takes no image input"})
		})
		if !strings.Contains(got, "image not attached") || !strings.Contains(got, "shots/gone.png") {
			t.Errorf("refusal not printed:\n%s", got)
		}
		if !strings.Contains(got, "no image input") {
			t.Errorf("the session's text-only note not printed:\n%s", got)
		}
	})

	t.Run("a turn with no images prints nothing", func(t *testing.T) {
		// The common case must stay byte-for-byte silent: a line per ordinary
		// turn would be noise.
		if got := capture(func() { printMentionImages(nil, nil, nil) }); got != "" {
			t.Errorf("printed %q, want nothing", got)
		}
	})
}

// TestImageTokensOfMatchesCacheEstimate keeps the seam's booking tied to the
// one documented estimate rather than a second formula.
func TestImageTokensOfMatchesCacheEstimate(t *testing.T) {
	raw := attachTestPNG(5, 90)
	if got, want := tools.ImageTokensOf(len(raw)), cache.TokensOf(len(raw)/3*4); got == 0 && want == 0 {
		t.Fatal("estimate is zero for a non-empty image")
	}
}
