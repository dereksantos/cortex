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
//   - the HUMAN's turn (#218, image_input.go's TurnImage): images the coder
//     was never asked for — @mentioned in the REPL, attached to a serve
//     request — ride the turn's own user message through
//     CortexSession.TurnWithAttachments. attachTurnImages splices them as
//     wire Parts and parks them for writeTurnImageSideCars, which writes one
//     side-car per image plus a manifest naming what the human called each
//     one, under the index the user message lands at. A text-only model gets
//     no part and no marker text (the #216 gate refuses the whole request for
//     one image part, and a user message is the one thing in-turn demotion
//     cannot stub, so an appended note would squat in the window forever); it
//     gets a note in TurnResult.ImageNotes for the human instead.
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
//
// A message that carries SEVERAL images (#218) names them with a slot
// suffix (`<id>.m<abs>.i2.<ext>`), so the lookup tries the bare name first —
// the single-image #217 shape, and what a turn with one image writes — then
// the numbered slots in order. It returns the first that exists, which is
// the right answer for the common case and a defined one for the rest.
func imagePathForMsg(sessionsDir, id string, abs int) (string, bool) {
	for _, ext := range []string{"png", "jpg", "jpeg", "gif", "webp", "img"} {
		p := filepath.Join(sessionsDir, fmt.Sprintf("%s.m%d.%s", id, abs, ext))
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
	}
	// Numbered slots (#218): the write emits .i2 onward only past the first
	// image, so scanning a handful is enough to cover any realistic turn.
	for slot := 2; slot <= maxSideCarSlots; slot++ {
		for _, ext := range []string{"png", "jpg", "jpeg", "gif", "webp", "img"} {
			p := filepath.Join(sessionsDir, fmt.Sprintf("%s.m%d.i%d.%s", id, abs, slot, ext))
			if _, err := os.Stat(p); err == nil {
				return p, true
			}
		}
	}
	return "", false
}

// maxSideCarSlots bounds the numbered-slot scan above: an attachment list
// longer than this would be refused by the per-turn image cap long before it
// reached disk, so the scan is a small constant, not a directory walk.
const maxSideCarSlots = 32

// imagePathForMsgAt names the side-car for a specific 1-based slot on a
// message (#218), so recall can name EVERY image on a turn that carried
// several rather than only the first one the plain lookup happens to find.
func imagePathForMsgAt(sessionsDir, id string, abs, slot int) (string, bool) {
	prefix := fmt.Sprintf("%s.m%d", id, abs)
	if slot > 1 {
		prefix = fmt.Sprintf("%s.i%d", prefix, slot)
	}
	for _, ext := range []string{"png", "jpg", "jpeg", "gif", "webp", "img"} {
		p := filepath.Join(sessionsDir, prefix+"."+ext)
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
	}
	return "", false
}

// TurnImage is one image a HUMAN attached to a turn's input (#218): the wire
// part plus the reference it came from (an @mention path or a URL). The
// caller that resolved it — the REPL's mention loader, a serve handler — is
// the one that knows the reference, so the session never has to guess it.
type TurnImage struct {
	// Ref names the image to the human and in the transcript line.
	Ref string
	// Part is the image bytes as a canonical data URI (internal/tools built
	// it, #217/#218 — the same shape a read_file attachment carries).
	Part tools.ImagePart
}

// attachTurnImages puts human-attached images on the turn's user message
// before it is appended (#218), and reports what happened in lines for the
// human. turn() calls it with the message it is about to append, so the
// parts land on the message that actually carries the turn's input.
//
// The two verdicts differ, deliberately:
//
//   - a vision-capable model gets the bytes as wire Parts through the same
//     mapping a read_file image uses (#217), so both paths put identical
//     shapes on the wire, and the bytes are parked for writeTurnImageSideCars
//     to persist at the index this message lands at — that is what lets
//     `recall` name them after an in-memory Parts list is gone;
//   - a text-only model gets NOTHING on the message: the user's text is
//     left byte-for-byte as typed. An image content part would fail the #216
//     gate for the WHOLE request, and prefixing the input with an
//     "[image: …]" note would be worse than dropping it — a RoleUser message
//     is the one thing in-turn demotion must not stub, so that note would
//     squat in the window for the session's life while standing for bytes
//     that were never sent. The human is told, in plain text, instead.
//
// The transcript keeps the human's input exactly as typed and nothing else:
// the reference is already in it (an @mention left as `@shots/ui.png` by the
// loader would be a second source of truth), and the bytes live in the
// side-car.
func (cs *CortexSession) attachTurnImages(msg *Message, images []TurnImage) []string {
	if len(images) == 0 {
		return nil
	}
	usable := images[:0:0]
	for _, img := range images {
		// An attachment with no reference has nothing to name it in a report,
		// and no bytes has nothing to send: skipping is the only safe reading.
		if img.Ref == "" || img.Part.DataURI == "" {
			continue
		}
		usable = append(usable, img)
	}
	if len(usable) == 0 {
		return nil
	}
	if !cs.ImageInputEnabled() {
		var notes []string
		for _, img := range usable {
			notes = append(notes, fmt.Sprintf("%s was not sent — the bound model takes no image input (set models.code.vision to true to attach images)", img.Ref))
		}
		return notes
	}
	parts := make([]tools.ImagePart, 0, len(usable))
	refs := make([]string, 0, len(usable))
	for _, img := range usable {
		parts = append(parts, img.Part)
		refs = append(refs, img.Ref)
	}
	spliceImagePartsAll(msg, parts)
	// Park the parts — and what the human called each one — for the
	// append-time side-car write, which needs the index the message lands at
	// (the same keying a read_file image uses at writeImageSideCarAt, so
	// recall resolves both the same way).
	cs.pendingTurnSideCars = parts
	cs.pendingTurnSideCarRefs = refs
	return nil
}

// writeTurnImageSideCars persists one side-car per parked image, at
// transcript index abs (#218), plus a small manifest naming what each one is.
// The file names carry the index and, for a second and later image on the
// same message, its position — the #217 single-image key
// `<id>.m<abs>.<ext>` would let a second image clobber the first — and
// imagePathForMsg tries the numbered forms too, so recall still finds them.
// Best-effort like every other side-car write: a failure costs recall its
// pointer, never the turn.
func (cs *CortexSession) writeTurnImageSideCars(abs int) {
	parts := cs.pendingTurnSideCars
	refs := cs.pendingTurnSideCarRefs
	cs.pendingTurnSideCars = nil
	cs.pendingTurnSideCarRefs = nil
	if cs.transcript == nil || len(parts) == 0 {
		return
	}
	var written []turnSideCarEntry
	for i, part := range parts {
		slot := ""
		if i > 0 {
			slot = fmt.Sprintf(".i%d", i+1)
		}
		ref := ""
		if i < len(refs) {
			ref = refs[i]
		}
		if p := writeImageSideCarSlot(cs.SessionsDir(), cs.SessionID, abs, slot, part); p != "" {
			written = append(written, turnSideCarEntry{Path: p, Ref: ref, MediaType: part.MediaType})
		}
	}
	if len(written) > 0 {
		writeTurnImageManifest(cs.SessionsDir(), cs.SessionID, abs, written)
	}
}

// turnSideCarEntry is one image persisted for a turn's user message: where
// its bytes went, what the human called it, and what type it is.
type turnSideCarEntry struct {
	Path      string
	Ref       string
	MediaType string
}

// writeTurnImageManifest records, beside the bytes, which reference each
// side-car came from (#218). The transcript deliberately keeps the human's
// input exactly as typed — no marker text appended to it — so without this
// the reference a byte-file came from would exist nowhere on disk: a resumed
// session could see it holds an image and not say which one. One JSON line
// per image, read by recall and by a resume.
func writeTurnImageManifest(sessionsDir, id string, abs int, entries []turnSideCarEntry) {
	if sessionsDir == "" || id == "" || len(entries) == 0 {
		return
	}
	var b strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&b, "%s\t%s\t%s\n", e.Path, e.MediaType, e.Ref)
	}
	p := filepath.Join(sessionsDir, fmt.Sprintf("%s.m%d.images", id, abs))
	_ = os.WriteFile(p, []byte(b.String()), 0o600)
}

// readTurnImageManifest returns the manifest entries for message index abs,
// or nil when the message carried no human-attached images (or the manifest
// could not be read — an absent list is the right answer for a lookup).
func readTurnImageManifest(sessionsDir, id string, abs int) []turnSideCarEntry {
	p := filepath.Join(sessionsDir, fmt.Sprintf("%s.m%d.images", id, abs))
	body, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var out []turnSideCarEntry
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		out = append(out, turnSideCarEntry{Path: parts[0], MediaType: parts[1], Ref: parts[2]})
	}
	return out
}

// writeImageSideCarSlot is writeImageSideCar with a slot suffix between the
// message index and the extension ("" for the single-image #217 case, which
// keeps writing the exact file name it always did).
func writeImageSideCarSlot(sessionsDir, id string, abs int, slot string, part tools.ImagePart) string {
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
	p := filepath.Join(sessionsDir, fmt.Sprintf("%s.m%d%s%s", id, abs, slot, imageDataURIExt(part.MediaType, part.Path)))
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		return ""
	}
	return p
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
	spliceImagePartsAll(m, []tools.ImagePart{part})
}

// spliceImagePartsAll maps SEVERAL images onto one message's wire Parts
// (#218): the text part first, then one image part per attachment, in
// order — the form both transports serialize (#216). A message with no
// usable part is left untouched, so an empty or malformed attachment can
// never produce a Parts list that the provider would reject outright.
func spliceImagePartsAll(m *Message, parts []tools.ImagePart) {
	usable := parts[:0:0]
	for _, p := range parts {
		if p.DataURI != "" {
			usable = append(usable, p)
		}
	}
	if len(usable) == 0 {
		return
	}
	out := make([]llm.ContentPart, 0, len(usable)+1)
	if m.Content != "" {
		out = append(out, llm.TextPart(m.Content))
	}
	for _, p := range usable {
		out = append(out, llm.ImageURLPart(p.DataURI, llm.ImageDetailAuto))
	}
	m.Parts = out
}
