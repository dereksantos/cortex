// image.go — image input for read_file (issue #217, part 2 of #134).
//
// A whole-file read of an image (png/jpeg/gif/webp) hands the model the
// bytes as an image CONTENT PART instead of binary garbage in a string:
// readFile returns the short imageObservationPrefix text (the text copy
// that rides the transcript and every string-shaped consumer) and records
// the attachment (tools.go's lastImageObservation) for the coder
// dispatcher to splice onto the tool-result message as wire Parts
// (cmd/cortex, via TakeImageObservation). Three outcomes:
//
//   - vision model: image part attached, observation names the file type,
//     size, and the per-image token estimate the context math books.
//   - text-only model (deps answers ImageInputEnabled false): a short
//     refusal that NAMES the file type and offers the base64-via-bash
//     escape hatch — no image part is produced.
//   - over the size cap (tools.image_max_bytes): a refusal with the byte
//     size, the cap, and a pointer down, never a truncated image.
//
// A ranged read of an image is refused regardless: line ranges are
// meaningless over binary bytes.
//
// internal/tools is a leaf (stdlib only), so the image part's shape is
// mirrored here (ImagePart) and cmd/cortex maps it to
// llm.ContentPart/ImageURLPart at the dispatcher seam — one field-for-
// field conversion, and pkg/llm.GateImages stays the wire-side backstop.

package tools

import (
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
)

// imageMediaTypes maps a lowercased extension to its MIME type — the
// formats #217 names (png, jpeg, gif, webp). Magic bytes (sniffImageBytes)
// are the second signal: a magic-confirmed image with a lying extension is
// still an image, and the sniffed type wins for the data URI.
var imageMediaTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
}

// defaultImageMaxBytes is the size cap on an image handed to the model as a
// content part: ~1.5 MB of raw bytes (~2 MB base64 on the wire). Larger
// files are refused with a pointer down (downscale/crop with bash) rather
// than silently resized or truncated. Config-overridable via
// tools.image_max_bytes — see limits.go's active Limits and
// DefaultLimits().
const defaultImageMaxBytes = 1_500_000

// imageObservationPrefix is the marker read_file's image observation
// starts with — the text copy of an image result. It is NOT an "Error:"
// result: the read succeeded (for a vision model) and the harness splices
// the image part back onto the tool message when it sees this marker.
const imageObservationPrefix = "[image:"

// ImageObservationMarker exposes the marker prefix to the engine (cmd/
// cortex's context accounting and demotion recognize a resumed image
// result by it, #217).
func ImageObservationMarker() string { return imageObservationPrefix }

// ImagePart is the tool-side mirror of one image content part (the
// internal/tools leaf stays stdlib-only; cmd/cortex maps this to
// llm.ContentPart at the dispatcher seam). DataURI is the canonical
// OpenAI-form data URI ("data:image/png;base64,...").
type ImagePart struct {
	MediaType string
	Path      string
	DataURI   string
}

// ImageGate is the narrow capability read readFile consults for its
// text-only refusal: does the resolved CODE-role model accept image input
// (#216's verdict — models.<role>.vision / catalog / capability tags).
// Implemented by *CortexSession; a deps that does NOT implement it (the
// Study/Agent subagents, headlessDeps, test doubles) takes the image
// anyway — their wire requests are text-only strings that carry no image
// parts, and the #216 gate backstops anything that ever does.
type ImageGate interface {
	ImageInputEnabled() bool
}

// detectImage reports whether data is an image read_file should hand to a
// vision model, returning the MIME type to use. Extension and magic bytes
// are OR-ed: either signal counts, and a magic-confirmed type wins over a
// mismatched extension.
func detectImage(displayPath string, data []byte) (string, bool) {
	if mt := sniffImageBytes(data); mt != "" {
		return mt, true
	}
	mt, ok := imageMediaTypes[strings.ToLower(filepath.Ext(displayPath))]
	return mt, ok
}

// sniffImageBytes magic-sniffs the four supported formats: PNG's 8-byte
// signature, JPEG's SOI marker, GIF87a/GIF89a headers, and WEBP's
// "RIFF"???"WEBP" container (RIFF + 4-byte size + FourCC).
func sniffImageBytes(data []byte) string {
	switch {
	case len(data) >= 8 &&
		data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G' &&
		data[4] == 0x0D && data[5] == 0x0A && data[6] == 0x1A && data[7] == 0x0A:
		return "image/png"
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "image/jpeg"
	case len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a"):
		return "image/gif"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp"
	}
	return ""
}

// imagePartFor turns a detected image's bytes into the ImagePart (a
// canonical base64 data URI) plus the short observation text that stands
// in for it in the transcript: path, MIME type, byte size, and the
// per-image token estimate the context math books.
func imagePartFor(path, mediaType string, data []byte) (ImagePart, string) {
	part := ImagePart{
		MediaType: mediaType,
		Path:      path,
		DataURI:   "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data),
	}
	obs := fmt.Sprintf("%s%s %s %d bytes ≈%d tokens attached as an image part]",
		imageObservationPrefix, path, mediaType, len(data), ImageTokensOf(len(data)))
	return part, obs
}

// ImageObservationFor builds the (ImagePart, marker-observation) pair for
// raw image bytes — the same construction readFile performs, exposed for
// the cmd/cortex wiring tests so the engine-side splice/recall tests use
// the real production marker shape instead of a hand-copied one.
func ImageObservationFor(path, mediaType string, data []byte) (ImagePart, string) {
	return imagePartFor(path, mediaType, data)
}

// IsImageObservation reports whether a tool observation is read_file's
// image result marker (the text copy), the signal the cmd/cortex
// dispatcher uses to splice the recorded attachment's Parts onto the
// tool-result message.
func IsImageObservation(obs string) bool {
	return strings.HasPrefix(obs, imageObservationPrefix)
}

// imageRefusal builds the refusal shape both refusals share: the file
// type is NAMED (the issue's acceptance line), followed by why and the
// escape hatch the caller appends.
func imageRefusal(path, mediaType string) string {
	return fmt.Sprintf("%s is %s — an image file; ", path, mediaType)
}

// --- Attachment hand-off -------------------------------------------------
//
// tools.Execute's contract is a string, so the image part can't ride it.
// Instead read_file records the attachment in a goroutine-local slot (the
// same goroutine runs Execute and the dispatcher that appends the
// tool-result message), and TakeImageObservation consumes it for exactly
// the observation it belongs to. Any Execute call whose result is not an
// image marker clears the slot, so a stale attachment can never re-splice
// onto a later tool message, and a subagent's nested read_file (whose
// image belongs to the child's own conversation, never the parent's tool
// result) clears the parent's slot on its way out — fail-closed.

type imageSlotKey struct{}

// imageLocal carries the pending attachment for the goroutine running
// tools.Execute. A plain per-goroutine slot (a goroutine ID would need
// unsafe); its identity is the isolation.
var imageLocal sync.Map // imageSlotKey -> ImagePart

// recordImageObservation stores the attachment for the current Execute.
func recordImageObservation(part ImagePart) {
	imageLocal.Store(imageSlotKey{}, part)
}

// clearImageObservation drops any pending attachment (a non-image tool
// result consumed nothing, so nothing may linger for the next taker).
func clearImageObservation() {
	imageLocal.Delete(imageSlotKey{})
}

// TakeImageObservation consumes the pending image attachment (read_file's,
// recorded the moment it emitted the marker text). ok is false when the
// observation is not an image marker or nothing is pending. The slot is
// consumed on the ok path, so a repeated call for the same observation
// can't re-splice the image onto a second message.
func TakeImageObservation(obs string) (ImagePart, bool) {
	if !IsImageObservation(obs) {
		clearImageObservation()
		return ImagePart{}, false
	}
	v, ok := imageLocal.Load(imageSlotKey{})
	if !ok {
		return ImagePart{}, false
	}
	clearImageObservation()
	return v.(ImagePart), true
}

// ImageTokensOf is the documented per-image context estimate (#217):
// a content part bills roughly one token per 4 base64 characters on
// OpenAI-style backends, so raw bytes → base64 chars (×4/3) → tokens
// (÷4) collapses to bytes×⅓. This is the figure the observation names,
// what estTurnTokens books, and what the /context "images" legend row
// sums — one function so the three can't drift.
func ImageTokensOf(bytes int) int {
	if bytes <= 0 {
		return 0
	}
	return bytes / 3
}
