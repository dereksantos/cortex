// webui_transcript.go — M5.2b: the session transcript view-model (GOAL.md §6
// M5.2, split into M5.2a/b/c/d per STATE.md; GOAL.md §2 names
// cmd/cortex/webui*.go as home for the web UI's Go view-model builders).
// Per GOAL.md §3 P5, rendering logic lives in golden-tested Go view-models
// — this file is pure data assembly, no HTML/JS.
//
// The transcript is "transcript render" (docs/cortex-web.md Phase 5): every
// message of a real session JSONL file (session.go's loadSession, the same
// reader ResumeTranscript/listSessions already use), in order, with
// tool-call/tool-result turns represented alongside plain role/content ones.
package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/dereksantos/cortex/internal/tools"
)

// transcriptToolCall is one tool invocation carried by an assistant entry.
type transcriptToolCall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Args string `json:"args"`
}

// transcriptEntry is one rendered turn in the session transcript.
// transcriptImage is one image a message carries (#218), described rather
// than embedded: the transcript's JSON never holds image bytes (the wire-only
// Parts field is skipped by its codec), so the view-model reports what the
// image IS — a remote address it can show, or a name for a local attachment
// whose bytes stay in the session — and the web UI decides how to present it.
type transcriptImage struct {
	// URL is a loadable address when the image part carries one (http(s) or an
	// inline data: URI); empty for a local file attachment.
	URL string `json:"url,omitempty"`
	// Name is the reference the attachment carries (a workspace path, an
	// address, an uploaded filename) — display text only.
	Name string `json:"name,omitempty"`
	// MediaType is the image's declared type.
	MediaType string `json:"media_type,omitempty"`
}

type transcriptEntry struct {
	Turn       int                  `json:"turn"`
	Role       string               `json:"role"`
	Content    string               `json:"content"`
	ToolCalls  []transcriptToolCall `json:"tool_calls,omitempty"`
	ToolCallID string               `json:"tool_call_id,omitempty"`
	// Images lists any images attached to this message (#218) so the session
	// screen can show that a turn carried a screenshot instead of dropping it
	// to a marker string the reader cannot see past.
	Images []transcriptImage `json:"images,omitempty"`
}

// transcriptViewModel is the full session transcript screen.
type transcriptViewModel struct {
	SessionID string            `json:"session_id"`
	Entries   []transcriptEntry `json:"entries"`
}

// buildTranscriptViewModel reads a real session transcript file and renders
// it into entries in transcript order. The seed system-prompt message
// (always index 0 of a freshly created session, per session.go's
// write-on-create loop) is intentionally omitted: it is harness
// instructions, never a displayed conversation turn — the same convention
// display.go's REPL renderer already follows (gutter() has no RoleSystem
// case, so the seed system message never prints there either).
func buildTranscriptViewModel(path string) (transcriptViewModel, error) {
	msgs, turns, _, err := loadSession(path)
	if err != nil {
		return transcriptViewModel{}, fmt.Errorf("failed to load session %s: %w", path, err)
	}

	vm := transcriptViewModel{
		SessionID: sessionIDFromPath(path),
		Entries:   []transcriptEntry{},
	}
	for i, m := range msgs {
		if m.Role == RoleSystem {
			continue
		}
		turn := 0
		if i < len(turns) {
			turn = turns[i]
		}
		e := transcriptEntry{
			Turn:       turn,
			Role:       m.Role,
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
		}
		for _, tc := range m.ToolCalls {
			e.ToolCalls = append(e.ToolCalls, transcriptToolCall{
				ID:   tc.ID,
				Name: tc.Function.Name,
				Args: tc.Function.Arguments,
			})
		}
		e.Images = transcriptImagesFor(m)
		// A HUMAN-attached image (#218) leaves no marker in the message text at
		// all — turn.go keeps the user's input byte-for-byte as typed, so the
		// marker scan above can never find it. Its durable record is the
		// side-car manifest written beside the transcript at the index this
		// message landed at; read it so a turn that carried a screenshot shows
		// its images here instead of nothing.
		e.Images = append(e.Images, transcriptImagesFromManifest(filepath.Dir(path), sessionIDFromPath(path), i)...)
		vm.Entries = append(vm.Entries, e)
	}
	return vm, nil
}

// transcriptImagesFor describes the images attached to one transcript message
// (#218).
//
// It reads the message's MARKER TEXT, not its wire Parts, because the marker
// is the part that actually survives into the transcript: Parts is `json:"-"`
// on the wire type (transport.go), so a reloaded transcript holds only
// read_file's / an attachment's short `[image:…]` observation. Deriving the
// list from anything else would make the session screen's image display work
// in-process and come up empty after a restart — the exact split this
// view-model exists to avoid.
//
// Markers are found by scanning rather than by tools.IsImageObservation,
// deliberately: that predicate answers "is this WHOLE observation an image
// result", which the splice and side-car paths in image_input.go depend on,
// and loosening it to a substring test would change what those attach. Here a
// marker may sit inside surrounding prose, so the scan is the right question,
// and a message can carry more than one.
//
// A marker names a reference and a media type, never bytes, so a local
// attachment gets no loadable URL and the web UI shows it as a labelled
// attachment (session.js) — which is what the record honestly supports.
func transcriptImagesFor(m Message) []transcriptImage {
	const open = "[image:"
	rest := m.Content
	var out []transcriptImage
	for {
		i := strings.Index(rest, open)
		if i < 0 {
			return out
		}
		body := rest[i+len(open):]
		end := strings.Index(body, "]")
		if end < 0 {
			return out
		}
		if img, ok := parseTranscriptImageMarker(body[:end]); ok {
			out = append(out, img)
		}
		rest = body[end+1:]
	}
}

// transcriptImagesFromManifest describes the images a HUMAN attached to the
// transcript message at absolute index abs (#218), read from the side-car
// manifest written beside the transcript (image_input.go's
// writeTurnImageManifest). It returns nil when the message carried no
// human-attached image: a turn with an image is recorded ONLY in the manifest
// (the user's text is persisted byte-for-byte as typed), so without this the
// session screen shows nothing for a turn that really carried a screenshot.
// Each entry gets the same URL rule the marker scan applies: a remote
// reference is loadable in the browser directly; a local one gets name and
// type only, its bytes staying in the session directory.
func transcriptImagesFromManifest(sessionsDir, sessionID string, abs int) []transcriptImage {
	entries := readTurnImageManifest(sessionsDir, sessionID, abs)
	if len(entries) == 0 {
		return nil
	}
	out := make([]transcriptImage, 0, len(entries))
	for _, e := range entries {
		img := transcriptImage{Name: e.Ref, MediaType: e.MediaType}
		if addr, ok := tools.ImageURL(e.Ref); ok {
			img.URL = addr
		}
		out = append(out, img)
	}
	return out
}

// parseTranscriptImageMarker reads one marker's body — "ref mediaType N bytes
// ≈M tokens attached as an image part" — into a transcriptImage. ok is false
// when there is no reference at all, so an empty or truncated marker yields no
// entry rather than an image named "".
func parseTranscriptImageMarker(body string) (transcriptImage, bool) {
	fields := strings.Fields(body)
	if len(fields) == 0 {
		return transcriptImage{}, false
	}
	img := transcriptImage{Name: fields[0]}
	// The media type is the only field containing "/", so a marker that omits
	// it (or an older shape) still resolves to its reference.
	if len(fields) > 1 && strings.Contains(fields[1], "/") {
		img.MediaType = fields[1]
	}
	if addr, ok := tools.ImageURL(img.Name); ok {
		// A remote address is loadable in the browser directly, so the UI can
		// show the image itself rather than only its name.
		img.URL = addr
	}
	return img, true
}

// sessionIDFromPath derives a session id from its transcript file path
// (the basename with the .jsonl extension stripped) — the same convention
// latestSessionID/listSessions already use.
func sessionIDFromPath(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".jsonl")
}
