// serve_attachments.go — image attachments on the HTTP turn endpoints
// (issue #218, parts 4–6 of image input #134).
//
// A browser client (or any API caller) attaches an image to a turn by naming
// it: `{path}` for a file in the session's own workspace, `{url}` for a remote
// image, or `{data}` for base64 bytes posted by a file picker. All three are
// resolved into the same `TurnImage` the REPL's @mentions produce, and handed
// to the one seam that puts images on a turn
// (`CortexSession.TurnWithAttachments`) — so a web-attached screenshot and a
// mention-attached one reach the model in exactly the same shape, and the
// serve layer adds no second path for an image to take.
//
// Why a separate file: the resolution rules are security-relevant (a `path` is
// a client-directed file read, `data` is client-supplied bytes) and belong next
// to their tests, not buried in whichever handler grew them.
//
// The confinement rule is the one read_file uses — `tools.ConfinePath`,
// lexical AND symlink-resolved — applied here rather than relying on the tool
// layer, because these bytes are read by the HTTP handler itself, on the
// human's behalf, and never pass through a tool dispatch that would vet them.
// Reusing the same function (not a reimplementation) is what keeps "what the
// agent may read" and "what a client may attach" from drifting into two
// different answers.

package main

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/dereksantos/cortex/internal/tools"
)

// TurnAttachment is one client-supplied image reference in a turn request: a
// workspace-relative `path`, an http(s) `url`, or `data` holding base64 image
// bytes (a `data:` URI or bare base64) posted by a browser picker.
//
// Exactly one of the three, or the entry is a caller error — see
// resolveTurnAttachments. `path` and `url` are REFERENCES, and the server does
// the reading; `data` exists because a file the coder picked in a browser has
// no workspace path and nothing for the server to fetch, so the bytes are the
// only thing that can travel. It is still not a file store: the payload goes
// through the same cap, sniffer, and vision verdict as the other two
// (tools.DecodeInlineDataURI + tools.AttachImage), so posting bytes buys an
// image no more trust than naming one.
type TurnAttachment struct {
	Path string `json:"path,omitempty"`
	URL  string `json:"url,omitempty"`
	Data string `json:"data,omitempty"`
	// Name labels a `data` attachment for the report line; it is never used to
	// decide what the bytes ARE (the sniffer does that) and never touches the
	// filesystem.
	Name string `json:"name,omitempty"`
}

// AttachmentError names which attachment was unacceptable and why, so the
// handler can answer 400 with something the caller can act on instead of a
// bare decode failure.
type AttachmentError struct {
	Index  int
	Ref    string
	Reason string
}

func (e AttachmentError) Error() string {
	return fmt.Sprintf("attachment %d (%s): %s", e.Index, e.Ref, e.Reason)
}

// resolveTurnAttachments turns the request's attachment list into the images
// the turn should carry, resolving each against the session's workspace.
//
// Rules, and the reasoning for each:
//
//   - An EMPTY list is not an error: the vast majority of turns have no
//     attachments, and this must stay the ordinary path (nil, nil).
//   - Exactly one source per entry. Two leaves precedence undefined by
//     accident and none is a client bug; both are a 400, loudly — silently
//     picking one would hide a malformed request that keeps "working".
//   - A path is confined with tools.ConfinePath, the same rule read_file
//     applies (absolute paths refused, `..` escapes refused, and a symlink
//     inside the workspace pointing out of it refused after resolution). The
//     refusal is a 400, not a silent skip: a client that asked for
//     /etc/passwd must be told it was refused, because the alternative —
//     ignoring it and answering as if nothing was attached — makes a
//     permission decision invisible.
//   - A url is validated with tools.ImageURL (so only http(s) reaching an
//     image-looking target, never file:// or a bare host) and fetched through
//     the same seam @mentions use, which is the SSRF-safe public fetch and
//     swappable in tests.
//   - `data` is decoded and then judged by its BYTES. A `path`/`url`
//     attachment's reference is anchored in something the server itself read,
//     so the leaf's extension-OR-magic rule is safe there; here the filename
//     is untrusted text attached to unseen bytes, so letting ".png" vouch for
//     prose would defeat the leaf. The sniff decides, and a declared media
//     type is reconciled against it rather than believed.
//   - A path that exists but is not an image, or an image over the cap, is a
//     refusal too: the leaf (tools.AttachImage) is what says "this is an
//     attachable image", and it is the same verdict read_file reaches, so the
//     two entry points cannot disagree about what an image is.
//
// Anything the leaf refuses becomes an AttachmentError, and resolution stops
// at the FIRST one: a request that partially attached would leave the model
// answering about images it never received, which is worse than the turn not
// running at all.
func resolveTurnAttachments(ctx context.Context, cs *CortexSession, atts []TurnAttachment) ([]TurnImage, *AttachmentError) {
	if len(atts) == 0 {
		return nil, nil
	}
	out := make([]TurnImage, 0, len(atts))
	for i, a := range atts {
		ref := strings.TrimSpace(a.Path)
		if ref == "" {
			ref = strings.TrimSpace(a.URL)
		}
		if ref == "" {
			ref = strings.TrimSpace(a.Name)
		}
		attErr := func(reason string) *AttachmentError {
			return &AttachmentError{Index: i, Ref: ref, Reason: reason}
		}
		provided := 0
		for _, v := range []string{a.Path, a.URL, a.Data} {
			if strings.TrimSpace(v) != "" {
				provided++
			}
		}
		if provided > 1 {
			return nil, attErr("specify exactly one of path, url, or data")
		}
		if provided == 0 {
			return nil, attErr("empty attachment: needs a path, a url, or data")
		}
		switch {
		case a.Path != "":
			// Confinement FIRST, before any read: the check is the guard, so
			// nothing may touch the filesystem past it.
			call := tools.ToolCall{Function: tools.FunctionCall{
				Name:      "path",
				Arguments: fmt.Sprintf(`{"path":%q}`, a.Path),
			}}
			if _, err := tools.ConfinePath(call, cs.root()); err != nil {
				return nil, attErr(err.Error())
			}
			abs := filepath.Join(cs.root(), filepath.Clean(a.Path))
			data, loadErr := tools.LoadImageBytes(abs, tools.ImageMaxBytes())
			att := tools.AttachImageWithErr(cs, a.Path, abs, data, loadErr)
			if att.Refused {
				return nil, attErr(att.Reason)
			}
			out = append(out, TurnImage{Ref: att.Ref, Part: att.Part})
		case a.URL != "":
			addr, ok := tools.ImageURL(strings.TrimSpace(a.URL))
			if !ok {
				return nil, attErr("not an http(s) image url")
			}
			data, fetchErr := fetchMentionImageBytes(ctx, addr)
			// The fsPath stays empty exactly as the mention path leaves it: the
			// bytes came from the network, so there is no local file to name,
			// and the marker (and any report line) names the address instead.
			att := tools.AttachImageWithErr(cs, addr, "", data, fetchErr)
			if att.Refused {
				return nil, attErr(att.Reason)
			}
			out = append(out, TurnImage{Ref: att.Ref, Part: att.Part})
		default:
			// a.Data: browser-picked bytes. Decode, then hand them to the SAME
			// leaf the other two sources use, so the cap and the vision verdict
			// are identical however the image arrived. fsPath stays empty
			// (there is no file).
			raw, declared, decErr := tools.DecodeInlineDataURI(a.Data, tools.ImageMaxBytes())
			if decErr != nil {
				return nil, attErr(decErr.Error())
			}
			name := strings.TrimSpace(a.Name)
			if name == "" {
				name = "attached image"
			}
			// The bytes alone decide what this is: the client's filename is
			// display text and must not vouch for the payload.
			sniffed := tools.SniffImageBytes(raw)
			if sniffed == "" {
				return nil, attErr(name + " is not an image file (png, jpeg, gif, or webp)")
			}
			if declared != "" && declared != "image/jpg" && !strings.EqualFold(declared, sniffed) {
				return nil, attErr(fmt.Sprintf("%s declares %s but its bytes are %s", name, declared, sniffed))
			}
			att := tools.AttachImageWithErr(cs, name, "", raw, nil)
			if att.Refused {
				return nil, attErr(att.Reason)
			}
			// Belt and braces: whatever the leaf concluded, a posted payload
			// must not reach the model as a media type other than what its
			// bytes actually are.
			if att.Part.MediaType != "" && att.Part.MediaType != sniffed {
				return nil, attErr(fmt.Sprintf("%s was classified as %s, not %s", name, att.Part.MediaType, sniffed))
			}
			out = append(out, TurnImage{Ref: att.Ref, Part: att.Part})
		}
	}
	return out, nil
}

// attachmentHTTPStatus maps an attachment refusal to the status a client
// should see. Every refusal here is a malformed or disallowed REQUEST (a bad
// path, an escape, a non-image, an over-cap image, an unfetchable URL), so
// they are all 400 — there is no attachment failure that is the server's
// fault, and reporting one as a 500 would invite the client to retry a
// request that will fail identically forever.
func attachmentHTTPStatus(*AttachmentError) int { return http.StatusBadRequest }
