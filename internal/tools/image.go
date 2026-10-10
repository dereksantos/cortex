// image.go — image input for read_file (issue #217, part 2 of #134).
//
// A whole-file read of an image (png/jpeg/gif/webp) hands the model the
// bytes as an image CONTENT PART instead of binary garbage in a string:
// readFile returns the short imageObservationPrefix text (the text copy
// that rides the transcript and every string-shaped consumer) and records
// the attachment on the session's ImageSink (deps), for the coder
// dispatcher to splice onto the tool-result message as wire Parts
// (cmd/cortex, via the session's per-session pending slot). Three outcomes:
//
//   - vision model: image part attached, observation names the file type,
//     size, and the per-image token estimate the context math books.
//   - text-only model (deps answers ImageInputEnabled false): a short
//     refusal that NAMES the file type and offers the base64-via-bash
//     escape hatch — no image part is produced.
//   - over the size cap (tools.image_max_bytes): a refusal with the byte
//     size, the cap, and a pointer down, never a truncated image.
//   - inside a subagent (study/agent): a refusal — the subagent's engine
//     never splices image parts onto its tool messages, so an "attached"
//     observation would tell the model about an image it cannot see.
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
// Study/Agent subagents, headlessDeps, test doubles) is ungated, and the
// #216 gate backstops anything that ever sends image parts from there.
type ImageGate interface {
	ImageInputEnabled() bool
}

// ImageSink is the OWNER side of the image hand-off (#217): read_file
// records the attachment it produced by calling RecordImage on the deps
// it was dispatched with, so the bytes land on the session that ran the
// read — never on process-global state. The engine's splice step reads
// and clears the same slot (cmd/cortex's CortexSession.pendingImage).
// Implemented by *CortexSession; a deps that does NOT implement it (the
// Study/Agent subagents route through the coder session but read_file
// refuses inside a subagent anyway, headlessDeps, test doubles) gets a
// marker-less world: readFile refuses rather than emit an "attached"
// observation nothing can honor.
type ImageSink interface {
	RecordImage(ImagePart)
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

// SniffImageBytes reports the MIME type the leading bytes of data magic-
// sniff to ("" for anything unsupported), using the one sniffer read_file
// uses (#217). A caller that already holds bytes — an HTTP response body, an
// uploaded payload — can ask the same question with the same answer instead
// of writing a second copy of the four signatures.
func SniffImageBytes(data []byte) string { return sniffImageBytes(data) }

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

// imagePartMarker is the observation text that stands in for an image's
// bytes wherever a string is needed (the transcript, the journal, a
// resumed session): reference, MIME type, byte size, and the per-image
// token estimate the context math books. read_file names the path it
// resolved (#217); a turn attachment names the reference the human typed
// (#218). Both come through this one function, so the two attachment paths
// can never disagree about what the line says.
func imagePartMarker(displayName, mediaType string, data []byte) string {
	return fmt.Sprintf("%s%s %s %d bytes ≈%d tokens attached as an image part]",
		imageObservationPrefix, displayName, mediaType, len(data), ImageTokensOf(len(data)))
}

// imagePartFor turns a detected image's bytes into the ImagePart (a
// canonical base64 data URI) plus the short observation text that stands
// in for it in the transcript: path, MIME type, byte size, and the
// per-image token estimate the context math books.
func imagePartFor(path, mediaType string, data []byte) (ImagePart, string) {
	part := ImagePart{
		MediaType: mediaType,
		Path:      path,
		DataURI:   ImageDataURIFor(mediaType, data),
	}
	return part, imagePartMarker(path, mediaType, data)
}

// ImageDataURIFor is the one place the canonical OpenAI data URI is
// assembled — read_file's part (#217) and a turn attachment (#218) share
// it, so a part either path produces is byte-shaped the same way. An empty
// media type yields "" (there is nothing to name the payload with).
func ImageDataURIFor(mediaType string, data []byte) string {
	if mediaType == "" {
		return ""
	}
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data)
}

// ImageMarkerFor is the observation line for an image named by a reference
// a human typed (#218): the same shape read_file emits (#217), with the
// reference as written in place of a resolved path. filepath.Base is
// deliberately not applied — a mention of `shots/ui.png` keeps its
// directory, which is what makes it findable again. It is what an attach
// site shows and what its context booking reads, so the two cannot drift.
func ImageMarkerFor(displayRef, mediaType string, data []byte) string {
	if displayRef == "" || mediaType == "" {
		return ""
	}
	return imagePartMarker(displayRef, mediaType, data)
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
