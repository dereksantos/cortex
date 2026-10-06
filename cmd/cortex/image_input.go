// image_input.go — the coder-side wiring for image input (issue #217,
// part 2 of #134).
//
// read_file detects an image and hands back its short `[image: …]` marker
// observation with the attachment recorded in tools' per-dispatch slot
// (internal/tools/image.go). This file closes the loop at the three places
// the engine touches:
//
//   - coderDispatcher (loop.go): after Execute, TakeImageObservation moves
//     the attachment onto the tool-result message as wire Parts (mapped to
//     llm.ContentPart), and writes the bytes to a per-session side-car
//     file so a resumed session — whose transcript keeps only the marker
//     string — can still point recall at the real bytes. The splice only
//     happens while the request's Vision verdict is true; the text-only
//     refusal already came from the tool itself (ImageInputEnabled).
//   - estTurnTokens (demote.go): images are booked at tools.ImageTokensOf,
//     the documented per-image estimate, so an image counts toward the
//     window and demotes on schedule — never staying in the prompt forever.
//   - Recall (tool_deps.go): an image's outline citation resolves to the
//     marker line plus the side-car path, the recoverable-image contract.
//
// The side-car lives beside the transcript in .cortex/sessions/ (already
// gitignored, per-session, same lifetime as the transcript it backs).

package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/dereksantos/cortex/internal/tools"
	"github.com/dereksantos/cortex/pkg/llm"
)

// imageDataURIExt maps a MIME type to a side-car file extension; an
// unknown type keeps the source file's own extension when it's one of
// the four supported image extensions (a .jpeg source stays .jpeg on
// disk), else the generic ".img".
func imageDataURIExt(mediaType, sourcePath string) string {
	switch mediaType {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	}
	if e := strings.ToLower(filepath.Ext(sourcePath)); e != "" {
		return e
	}
	return ".img"
}

// imagePathForMsg names (and stats) the side-car file for the image bytes
// of transcript message index abs of session id: every supported
// extension plus the generic ones, since the lookup side doesn't know
// which one the write chose. ok is false when no side-car exists (an
// image read before #217, or bytes cleaned up).
func imagePathForMsg(sessionsDir, id string, abs int) (string, bool) {
	for _, ext := range []string{"png", "jpg", "jpeg", "gif", "webp", "img"} {
		p := filepath.Join(sessionsDir, fmt.Sprintf("%s.m%d.%s", id, abs, ext))
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
	}
	return "", false
}

// writeImageSideCar persists an attachment's decoded bytes beside the
// transcript so recall (and a resumed session) can reach them after the
// in-memory Parts are gone. Best-effort like the journal notes it mirrors:
// a failed write costs recall its pointer, never the turn. Returns the
// path written ("" when nothing was written).
func writeImageSideCar(sessionsDir, id string, abs int, part tools.ImagePart) string {
	if sessionsDir == "" || id == "" {
		return ""
	}
	i := strings.Index(part.DataURI, ";base64,")
	if !strings.HasPrefix(part.DataURI, "data:") || i < 0 {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(part.DataURI[i+len(";base64,"):])
	if err != nil {
		return ""
	}
	p := filepath.Join(sessionsDir, fmt.Sprintf("%s.m%d%s", id, abs, imageDataURIExt(part.MediaType, part.Path)))
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		return ""
	}
	return p
}

// ImageInputEnabled answers internal/tools' ImageGate (#217): whether the
// resolved code-role model accepts image input — the same #216 verdict the
// wire gate reads (AgentRequest.Vision, stamped at binding and re-derived
// on /model switches), so the tool's text-only refusal and the send-side
// gate can never disagree. A session without a request (bare test fixture)
// reads true: nothing here has decided against vision, and the wire gate
// backstops the send.
func (cs *CortexSession) ImageInputEnabled() bool {
	if cs == nil || cs.Request == nil {
		return true
	}
	return cs.Request.Vision
}

// spliceImageResult is the coder Toolset's SpliceImages hook (#217): if
// read_file just attached an image, move it onto the tool-result message
// as wire Parts. The bytes' side-car copy is written at append time by
// writeImageSideCarAt (loop.go calls it with the message's final
// transcript index — the key recall resolves). A message with no pending
// attachment, a session without vision (the tool refused; nothing to
// splice), or no transcript all no-op — and the attachment slot is always
// consumed, so nothing stale can reach the next result.

// imagePendingCarries the part plus the index the engine will append the
// spliced message at, set by spliceImageResult and consumed by
// writeImageSideCarAt (the write must happen at append time, when the
// message's citation-space index is final).
type imagePending struct {
	part tools.ImagePart
	abs  int
}

var imageSideCarKeys sync.Map // marker text -> imagePending

// pendingSideCar returns the recorded (part, index) for obs, consuming it.
func pendingSideCar(obs string) (tools.ImagePart, int, bool) {
	v, ok := imageSideCarKeys.LoadAndDelete(obs)
	if !ok {
		return tools.ImagePart{}, 0, false
	}
	p := v.(imagePending)
	return p.part, p.abs, true
}

func (cs *CortexSession) spliceImageResult(msg *Message) {
	part, ok := tools.TakeImageObservation(msg.Content)
	if !ok || !cs.ImageInputEnabled() {
		return
	}
	spliceImageParts(msg, part)
	// Remember the part for the append-time write. The transcript index
	// is NOT reliably known here (the hook runs before the engine appends
	// this message, and possibly before this turn's leading messages are
	// written), so the entry carries abs = -1 ("unknown") and the engine
	// supplies the final index when it calls writeImageSideCarAt — the
	// key recall's citations resolve.
	if cs.transcript != nil {
		imageSideCarKeys.Store(msg.Content, imagePending{part: part, abs: -1})
	}
}

// writeImageSideCarAt persists the image side-car for a message the
// engine just appended at transcript index abs (#217), consuming the
// pending entry spliceImageResult left for this marker. The engine calls
// it from inside its own append path — the one place the final index is
// known. A message with no pending entry writes nothing.
//
// Keying note: runLoop's appendMsg is cs.Append, whose transcript entry
// position equals the message's index in cs.Request.Messages AFTER the
// append, so the correct abs is len(cs.Request.Messages) measured after
// appendMsg ran — which is exactly what loop.go passes.
func (cs *CortexSession) writeImageSideCarAt(msg *Message, abs int) {
	if cs.transcript == nil {
		return
	}
	part, _, ok := pendingSideCar(msg.Content)
	if !ok {
		return
	}
	writeImageSideCar(cs.SessionsDir(), cs.SessionID, abs, part)
}

// llmImageParts returns the image parts of a Parts slice (the cheap
// filter recall and the accounting helpers share).
func llmImageParts(parts []llm.ContentPart) []llm.ContentPart {
	var out []llm.ContentPart
	for _, p := range parts {
		if p.HasImage() {
			out = append(out, p)
		}
	}
	return out
}

// spliceImageParts maps a tools.ImagePart onto a message's wire Parts
// (#217). The text part carries the marker observation the model already
// sees in the transcript, so the wire message is [text, image] — the
// OpenAI content-parts form the #216 transport serializes. Reached only
// through spliceImageResult (the coder Toolset's SpliceImages hook),
// which checks the vision verdict first; the text-only case never
// produced an attachment in the first place.
func spliceImageParts(m *Message, part tools.ImagePart) {
	parts := make([]llm.ContentPart, 0, 2)
	if m.Content != "" {
		parts = append(parts, llm.TextPart(m.Content))
	}
	parts = append(parts, llm.ImageURLPart(part.DataURI, llm.ImageDetailAuto))
	m.Parts = parts
}
