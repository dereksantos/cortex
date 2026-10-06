// image_input.go — the coder-side wiring for image input (issue #217,
// part 2 of #134).
//
// read_file detects an image and hands back its short `[image: …]` marker
// observation, recording the attachment on the session itself (the
// ImageSink seam, internal/tools/image.go — a per-session field, so two
// sessions dispatching concurrently can never see each other's image).
// This file closes the loop at the three places the engine touches:
//
//   - the coder Toolset (turn.go wires, loop.go calls): SpliceImages moves
//     the attachment onto the tool-result message as wire Parts
//     (mapped to llm.ContentPart), and WriteImageSideCar writes the bytes
//     to a per-session side-car file so a resumed session — whose
//     transcript keeps only the marker string — can still point recall at
//     the real bytes. The splice only happens while the request's Vision
//     verdict is true; the text-only refusal already came from the tool
//     itself (ImageInputEnabled).
//   - estTurnTokens (demote.go): images with wire Parts are booked at
//     tools.ImageTokensOf, the documented per-image estimate, so an image
//     counts toward the window and demotes on schedule — never staying in
//     the prompt forever.
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

// RecordImage implements internal/tools' ImageSink (#217): read_file
// records the attachment it produced on the session that dispatched it,
// and every tool result that is NOT an image marker clears the slot
// (fail-closed — a stale attachment can never splice onto a later
// message). The field is the slot — per-session by construction, so a
// serve/discord process running several sessions concurrently never
// records one session's image where another session's splice step reads
// it. A second image recorded before the first is spliced overwrites:
// the most recent read is the one being appended next.
func (cs *CortexSession) RecordImage(part tools.ImagePart) {
	cs.pendingImage = part
	cs.pendingImageSet = true
}

// spliceImageResult is the coder Toolset's SpliceImages hook (#217): if
// read_file just attached an image on THIS session, move it onto the
// tool-result message as wire Parts and park the part for the append-
// time side-car write (loop.go calls writeImageSideCarAt with the index
// appendMsg gave this message — the key recall resolves). A message with
// no pending attachment, a session without vision (the tool refused;
// nothing to splice), or no transcript all no-op — and the pending slot
// is always consumed here, so nothing stale can reach the next result.
func (cs *CortexSession) spliceImageResult(msg *Message) {
	part, ok := cs.takePendingImage(msg.Content)
	if !ok || !cs.ImageInputEnabled() {
		return
	}
	spliceImageParts(msg, part)
	cs.pendingSideCar = part
	cs.pendingSideCarSet = cs.transcript != nil
}

// takePendingImage consumes the session's pending attachment, but only
// for an observation that is actually read_file's image marker: a non-
// image tool result clears the slot, so an attachment can never splice
// onto a message it doesn't belong to.
func (cs *CortexSession) takePendingImage(obs string) (tools.ImagePart, bool) {
	if !cs.pendingImageSet || !tools.IsImageObservation(obs) {
		cs.pendingImage = tools.ImagePart{}
		cs.pendingImageSet = false
		return tools.ImagePart{}, false
	}
	part := cs.pendingImage
	cs.pendingImage = tools.ImagePart{}
	cs.pendingImageSet = false
	return part, true
}

// writeImageSideCarAt persists the image side-car for a message the
// engine appended at transcript index abs (#217), consuming the pending
// part spliceImageResult left for this message. The engine calls it from
// inside its own append path — the one place the final index is known.
// A message with no pending part writes nothing.
//
// Keying note: runLoop's appendMsg is cs.Append, which grows the
// transcript and cs.Request.Messages 1:1, so the message's index is
// len(cs.Request.Messages) measured BEFORE the append (equivalently,
// len-1 after) — which is exactly what loop.go passes, and what recall's
// citations resolve to.
func (cs *CortexSession) writeImageSideCarAt(msg *Message, abs int) {
	if cs.transcript == nil {
		return
	}
	part, ok := cs.takePendingSideCar(msg.Content)
	if !ok {
		return
	}
	writeImageSideCar(cs.SessionsDir(), cs.SessionID, abs, part)
}

// takePendingSideCar consumes the part spliceImageResult parked for the
// append-time write; a message whose observation is not an image marker
// clears the slot (the same fail-closed rule as takePendingImage).
func (cs *CortexSession) takePendingSideCar(obs string) (tools.ImagePart, bool) {
	if !cs.pendingSideCarSet || !tools.IsImageObservation(obs) {
		cs.pendingSideCar = tools.ImagePart{}
		cs.pendingSideCarSet = false
		return tools.ImagePart{}, false
	}
	part := cs.pendingSideCar
	cs.pendingSideCar = tools.ImagePart{}
	cs.pendingSideCarSet = false
	return part, true
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
