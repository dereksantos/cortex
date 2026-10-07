// image_attach.go — the image-attachment primitive for turn input
// (issue #218, part 3 of image input #134).
//
// read_file attaches an image the MODEL asked for (#217): the coder calls
// the tool on a path, the tool detects the image, and the bytes reach the
// model as a content part through the session's ImageSink. Issue #218 is
// the mirror case — the HUMAN names an image (an `@shot.png` mention in
// the REPL, an attachment on a `cortex serve` turn, a URL or local path
// written in a task) and the harness attaches it to the turn, with no
// tool call anywhere in sight.
//
// Both cases must produce the SAME thing, or the two attachment paths
// drift into parts a backend can't parse, markers the context accountant
// doesn't recognize, or a billed size that disagrees with the bytes. So
// the decisions live here, in one leaf function over bytes the caller has
// already fetched:
//
//   - detection: extension OR magic bytes, magic winning on a mismatch —
//     the exact detectImage rule read_file uses, over the same four
//     formats.
//   - the size cap: tools.read.image_max_bytes, refused with the byte
//     size and the cap named, never a truncated image.
//   - the vision verdict: a text-only model gets a refusal that names the
//     file type and points at models.code.vision, and NO part. The bytes
//     would fail the #216 wire gate anyway, and a silently dropped image
//     is precisely the failure mode #216 was written to prevent.
//   - the part and its text: ImagePart, imagePartFor, and the
//     `[image: …]` marker — produced by the very functions read_file
//     calls, so the shapes are shared rather than imitated.
//
// The package stays a leaf (stdlib only) and owns no I/O and no session:
// the CALLER reads the local file or downloads the URL (it is the caller
// that knows how to confine a path and how to fetch), and hands the bytes
// in. The #217 ImageGate seam is the capability read, unchanged — a deps
// that does not implement it is ungated, exactly as for read_file.

package tools

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// ImageAttachment is one image a turn carries, as loaded by AttachImage:
// the wire part (a canonical base64 data URI in DataURI, so both provider
// translations #216 already has — OpenAI's image_url and Anthropic's
// native source block — work from it), the marker observation that stands
// in for the bytes in every string-shaped consumer (the transcript, the
// journal, a resumed session), and the figure the context math books it
// at.
//
// Refused is set when the image could NOT be attached; Reason then says
// why in one sentence and Part/Marker stay empty, so a caller can never
// mistake a refusal for an attachment.
type ImageAttachment struct {
	// Ref is what was asked for, as the human named it: the workspace-
	// relative mention path, the URL, or the uploaded filename. The
	// marker observation and every user-facing line name this.
	Ref string
	// Part is the image content part to splice onto the turn's message.
	Part ImagePart
	// Marker is the short `[image: …]` observation IsImageObservation
	// recognizes — the transcript's copy of the attachment (#217).
	Marker string
	// Bytes is the decoded payload size, the figure ImageTokensOf prices.
	Bytes int
	// Tokens is the documented per-image estimate for this attachment
	// (ImageTokensOf over Bytes), what the turn's context booking
	// charges. Computed here so the attach site and the accounting cannot
	// disagree about what an attached image costs.
	Tokens int
	// Refused is true when no image can be attached; the reason is in
	// Reason and Part/Marker are empty.
	Refused bool
	// Reason is the refusal sentence — the text the human is told.
	Reason string
	// Err, set only for a load failure the caller surfaced as a cause
	// (an unreadable file, a failed or rejected download), is the wrapped
	// underlying error: errors.Is/As keep working on it while Reason stays
	// the sentence the human reads. A plain "not an image" refusal has no
	// Err — nothing went wrong, the target simply isn't an image.
	Err error
}

// Error implements error so a refusal can be reported with its cause
// attached; AttachImage itself never returns one, the value does.
func (a ImageAttachment) Error() string {
	if a.Err != nil {
		return a.Reason + ": " + a.Err.Error()
	}
	return a.Reason
}

// Unwrap exposes the load failure behind a refusal (os.Stat's ErrNotExist,
// a fetch error) so a caller can tell "the file isn't there" from "that
// isn't a png" without parsing prose.
func (a ImageAttachment) Unwrap() error { return a.Err }

// AttachImage turns bytes a caller already fetched into a turn attachment:
// the #217 pipeline (detect → cap → vision gate → part + marker) with the
// loading half lifted out, so a mentioned image, a serve-request
// attachment, and a read_file result all run one set of decisions.
//
//   - displayRef is what the marker and any user-facing line name (a
//     workspace-relative path for a file, the URL for a remote image).
//   - fsPath is the real filesystem path when there is one ("" for a URL
//     or an uploaded payload); it carries the extension signal for bytes
//     whose reference has no usable one, exactly as read_file carries its
//     resolved path.
//   - data is the file's or the response's bytes, verbatim.
//   - deps answers the vision verdict through the #217 ImageGate seam; a
//     deps that does not implement it is ungated (a subagent's deps, a
//     bare test double), leaving the wire gate as the backstop.
//
// Refusal shapes — a refusal comes back as Refused with a one-sentence
// Reason rather than an error, because an attachment that cannot happen is
// a reportable outcome the turn carries on without, not a failed
// operation. Callers that need the underlying cause get it from Err
// (AttachImageWithErr).
func AttachImage(deps ToolDeps, displayRef, fsPath string, data []byte) ImageAttachment {
	return attachImage(deps, displayRef, fsPath, data, nil)
}

// AttachImageWithErr is AttachImage for a caller whose load step already
// produced the bytes AND the reason it has none (os.ReadFile failed, a
// download was refused): pass those bytes and that error together and the
// refusal carries the cause for errors.Is, with Reason still naming the
// reference. A nil err behaves exactly like AttachImage.
func AttachImageWithErr(deps ToolDeps, displayRef, fsPath string, data []byte, loadErr error) ImageAttachment {
	return attachImage(deps, displayRef, fsPath, data, loadErr)
}

// attachImage is the shared body of AttachImage and AttachImageWithErr.
// The part and its text come from imagePartFor/imagePartMarker (#217); the
// media type for an image named by a reference is reduced through
// imageRefName first, so a URL's query cannot poison the extension signal.
func attachImage(deps ToolDeps, displayRef, fsPath string, data []byte, loadErr error) ImageAttachment {
	att := ImageAttachment{Ref: displayRef}
	ref := displayRef
	if ref == "" {
		ref = "(unnamed image)"
	}
	if loadErr != nil {
		att.Refused = true
		att.Reason = ref + " could not be attached as an image."
		att.Err = loadErr
		return att
	}
	if len(data) == 0 {
		att.Refused = true
		att.Reason = ref + " is empty — there are no image bytes to attach."
		return att
	}
	mediaType, ok := detectImage(imageRefName(displayRef), data)
	if !ok && fsPath != "" {
		mediaType, ok = detectImage(fsPath, data)
	}
	if !ok {
		att.Refused = true
		att.Reason = ref + " is not an image file (png, jpeg, gif, or webp), so it cannot be attached as an image."
		return att
	}
	// The part's Path is the real filesystem path when the image is a
	// local file — what read_file records, and what the side-car's
	// extension hint falls back to — else the reference as named.
	partPath := fsPath
	if partPath == "" {
		partPath = displayRef
	}
	if max := active.ImageMaxBytes; len(data) > max {
		att.Refused = true
		att.Reason = fmt.Sprintf("%s is %s — an image file of %d bytes, over the %d-byte image cap (tools.image_max_bytes) — downscale or crop it before attaching.",
			ref, mediaType, len(data), max)
		return att
	}
	if gate, ok := deps.(ImageGate); ok && !gate.ImageInputEnabled() {
		att.Refused = true
		att.Reason = fmt.Sprintf("%s is %s — an image file; the bound model does not accept image input, so it was not attached — set models.code.vision to true or route the turn to a vision-capable model.",
			ref, mediaType)
		return att
	}
	// Only the part is wanted here: its Path records the resolved file, while
	// the marker names what the human typed (below) — so imagePartFor's
	// observation, which is keyed to the part's path, is discarded.
	part, _ := imagePartFor(partPath, mediaType, data)
	att.Part = part
	// The marker names the reference as the human typed it (a URL or a
	// mention path), not the resolved file behind it — while the type, size,
	// and billed estimate stay the same figures read_file's marker reports.
	att.Marker = imagePartMarker(ref, mediaType, data)
	att.Bytes = len(data)
	att.Tokens = ImageTokensOf(len(data))
	return att
}

// ImageURL reports whether a reference a human typed is a remote image
// address rather than a workspace path, and returns it normalized: scheme
// lowercased (providers are strict about `https://`, and a human may type
// `HTTPS://…`), the path escaped if it isn't already, and the query and
// fragment preserved — they carry what the CDN needs to serve the file.
//
// Only http and https count. `file://` is refused rather than quietly
// turned into a local read: a reference that LOOKS like an address should
// not gain filesystem privileges by being typed in a URL shape, and the
// local file belongs in the mention as a workspace path, where the
// workspace confinement rules apply to it.
//
// The scheme is tested as a prefix before parsing, deliberately: url.Parse
// reads `C:\shots\a.png` as scheme "c" with no host (and a path that is
// "//host/x" has no scheme at all but is not a relative path either), so
// parsing alone would either misread a Windows path as an address or
// silently drop a protocol-relative one. A workspace path never starts
// with a scheme prefix, so it is never mistaken for an address here.
// DecodeInlineDataURI accepts base64 image bytes handed over the wire —
// either a complete `data:<media>;base64,<payload>` URI (the shape a browser's
// FileReader produces) or a bare base64 payload — and returns the decoded
// bytes with their declared media type.
//
// It exists for the HTTP attach endpoint (issue #218 step 6): a file the coder
// picked in a browser has NO workspace path, so it cannot be referenced the
// way a `{path}` attachment is, and the server cannot fetch it. Posting the
// bytes is the only shape that works. The decode lives here, beside the cap
// and the sniffer that already judge attachment bytes, so a posted image gets
// exactly the same verdict as a fetched or workspace-read one — a fourth way
// in with weaker rules is the thing to avoid, not a path to add.
//
// Deliberate limits: the base64 input is bounded BEFORE decoding (a payload
// longer than 4/3 of the cap cannot decode to anything the cap accepts), and a
// declared media type outside the four supported images is refused by name
// rather than sniffed into something unexpected.
func DecodeInlineDataURI(s string, imageMaxBytes int) (data []byte, mediaType string, err error) {
	payload := strings.TrimSpace(s)
	if payload == "" {
		return nil, "", fmt.Errorf("inline image is empty")
	}
	if strings.HasPrefix(payload, "data:") {
		i := strings.Index(payload, ";base64,")
		if i < 0 {
			return nil, "", fmt.Errorf("inline image must be a base64 data: URI")
		}
		declared := strings.TrimPrefix(payload[:i], "data:")
		mediaType = strings.TrimSpace(strings.SplitN(declared, ";", 2)[0])
		payload = payload[i+len(";base64,"):]
	}
	if mediaType != "" && !knownImageMediaType(mediaType) {
		return nil, "", fmt.Errorf("inline image media type %q is not a supported image", mediaType)
	}
	if payload == "" {
		return nil, "", fmt.Errorf("inline image is empty")
	}
	if imageMaxBytes > 0 && len(payload) > (imageMaxBytes+2)/3*4+4 {
		return nil, "", fmt.Errorf("inline image is over the %d byte image limit", imageMaxBytes)
	}
	raw, derr := base64.StdEncoding.DecodeString(payload)
	if derr != nil {
		// Tolerate the URL-safe alphabet and missing padding a browser or a
		// client library may emit before giving up.
		raw, derr = decodeBase64Lenient(payload)
		if derr != nil {
			return nil, "", fmt.Errorf("inline image is not valid base64: %w", derr)
		}
	}
	if len(raw) == 0 {
		return nil, "", fmt.Errorf("inline image is empty")
	}
	return raw, mediaType, nil
}

// knownImageMediaType reports whether a declared media type is one of the four
// Cortex accepts, so a posted `data:application/x-msdownload;base64,…` is
// refused by name instead of being sniffed into whatever its bytes happen to
// start with.
func knownImageMediaType(t string) bool {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "image/png", "image/jpeg", "image/jpg", "image/gif", "image/webp":
		return true
	}
	return false
}

// decodeBase64Lenient accepts the URL-safe alphabet and missing padding.
func decodeBase64Lenient(payload string) ([]byte, error) {
	p := strings.NewReplacer("-", "+", "_", "/").Replace(payload)
	for len(p)%4 != 0 {
		p += "="
	}
	return base64.StdEncoding.DecodeString(p)
}

// ImageURL reduces a reference to
func ImageURL(ref string) (string, bool) {
	low := strings.ToLower(strings.TrimSpace(ref))
	if !strings.HasPrefix(low, "http:") && !strings.HasPrefix(low, "https:") {
		return "", false
	}
	u, err := url.Parse(strings.TrimSpace(ref))
	if err != nil {
		return "", false
	}
	if !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return "", false
	}
	if u.Host == "" {
		return "", false
	}
	if u.Opaque != "" {
		// An opaque form (`http:x`) has no path to take an image's
		// extension from and no shape a provider will fetch.
		return "", false
	}
	out := *u
	out.Scheme = strings.ToLower(u.Scheme)
	if unesc, err := url.PathUnescape(u.EscapedPath()); err == nil && unesc == u.Path {
		// Already raw rather than percent-encoded: emit the encoded form so
		// the address handed to the provider is a valid one.
		out.Path = u.EscapedPath()
	}
	return out.String(), true
}

// ImageSizeError says a file is larger than the image cap. It is its own
// type so a caller can tell "too big to attach" from "unreadable" without
// matching prose; the fields are what a refusal line quotes back.
type ImageSizeError struct {
	Path  string
	Bytes int64
	Cap   int
}

func (e *ImageSizeError) Error() string {
	return fmt.Sprintf("%s is %d bytes, over the %d-byte image cap (tools.image_max_bytes)", e.Path, e.Bytes, e.Cap)
}

// LoadImageBytes reads a LOCAL image file for attachment. It exists so the
// loading half is one small function that keeps os.ReadFile's error as the
// cause (which is what lets a refusal answer errors.Is(err, os.ErrNotExist)
// — "the file isn't there" stays distinguishable from "that isn't a png"
// without parsing prose); the caller has already confined the path.
//
// A directory is reported as a refusal cause rather than left to
// os.ReadFile's bare "is a directory", the same dead-end read_file avoids
// (#142); an over-cap file is refused by AttachImage without being read
// whole, so a mention of a huge file costs one Stat.
func LoadImageBytes(fsPath string, imageMaxBytes int) ([]byte, error) {
	if fsPath == "" {
		return nil, fmt.Errorf("no file to read")
	}
	info, err := os.Stat(fsPath)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", fsPath, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory", fsPath)
	}
	if imageMaxBytes > 0 && info.Size() > int64(imageMaxBytes) {
		return nil, &ImageSizeError{Path: fsPath, Bytes: info.Size(), Cap: imageMaxBytes}
	}
	data, err := os.ReadFile(fsPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", fsPath, err)
	}
	return data, nil
}

// ImageMaxBytes is the active tools.read.image_max_bytes ceiling, so a
// caller that pre-sizes a read (LoadImageBytes) quotes the same cap the
// attachment decision applies rather than its own copy of it.
func ImageMaxBytes() int { return active.ImageMaxBytes }

// imageRefName reduces a reference to the name its TYPE is read from:
// for an http(s) URL, the last path segment with any query and fragment
// stripped (a CDN link like `https://cdn/x/shot.png?width=100` is a PNG —
// running filepath.Ext over the whole URL yields ".png?width=100", which
// matches no media type and would refuse a perfectly good image); for a
// workspace path, the path unchanged, so read_file's rule applies to it
// byte for byte.
func imageRefName(ref string) string {
	u, err := url.Parse(ref)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Path == "" {
		return ref
	}
	// A URL path always separates on '/' — filepath's separator does not on
	// Windows, so the segment is cut by hand.
	if i := strings.LastIndexByte(u.Path, '/'); i >= 0 {
		return u.Path[i+1:]
	}
	return u.Path
}
