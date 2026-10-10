// mentions.go — @path mention attachment (issue #108) and image mentions
// (issue #218, part 3 of image input #134).
//
// A submitted line may contain one or more @path mentions. On submit, each
// mentioned file is attached to the turn through the SAME size rules as
// read_file (internal/tools): a small file is inlined verbatim, a large file
// is reduced to a structural outline with a pointer to study so the model can
// drill in. A mention that names a non-existent or unreadable path is
// reported inline (the model can course-correct) and the line is NOT
// silently dropped.
//
// Only an "@" starting a whitespace-delimited word is a mention (an email
// address or a `@types/node`-style scoped name is prose), and trailing
// sentence punctuation is stripped before resolving ("@main.go," means
// main.go). A mention that does not resolve to a readable file leaves the
// input UNCHANGED: the user's prose is exactly what goes into history and to
// the model, and no attachment note is produced for it.
//
// An IMAGE mention is a different kind of attachment (#218): its bytes reach
// the model as an image content part, never as inlined text — a base64 blob
// pasted into prose is useless to a model and poison for the context window.
// So a mention whose target is an image — a workspace path or an http(s)
// address — goes through the turn-attachment loader (tools.AttachImage, which
// applies the same detection, size cap, and vision verdict read_file uses,
// #217) and comes back in the []TurnImage this file returns, for
// the session to attach to the turn's user message. Text and image
// attachments are returned SEPARATELY because they reach the model through
// different doors: one is prepended text, the other is wire Parts.
//
// What an image mention deliberately does NOT change is the prose contract
// every caller already relies on: a mention that does not resolve to an image
// — a missing file, a directory, an out-of-workspace or absolute path, a
// non-image file — behaves exactly as it did before this issue (left as
// typed, or attached as text). `@main.go` and `@missing.txt` are byte for
// byte what they were.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/dereksantos/cortex/internal/lineedit"
	"github.com/dereksantos/cortex/internal/outline"
	"github.com/dereksantos/cortex/internal/style"
	"github.com/dereksantos/cortex/internal/tools"
)

// mentionRe matches an @path mention: an "@" at the start of a whitespace-
// delimited word (start of line or after whitespace — so "user@example.com",
// "@types/node" inside "bump @types/node", or a Java "@Override" mid-identifier
// is prose, never a mention), followed by a run of non-whitespace.
var mentionRe = regexp.MustCompile(`(^|\s)@\S+`)

// mentionPunct is the sentence punctuation stripped from a mention before
// resolving it: "look at @main.go," attaches main.go, not "main.go,".
const mentionPunct = ",.;:)!?"

// mentionPunctURL is the punctuation stripped from an ADDRESS mention. A URL
// uses ':' and ')' as part of its own grammar (a port; a signed or sized CDN
// path whose last segment ends in a closing paren), so trimming those would
// corrupt a real address; only characters that cannot legitimately end one
// are cut here.
const mentionPunctURL = ",.;!?"

// A resolved image mention becomes a TurnImage (image_input.go) — the one
// shape a turn carries an image in — so there is no second struct to keep in
// step with the seam that consumes it.

// MentionImageFetcher loads the bytes behind an image ADDRESS mention
// (#218). A workspace path is a local read; an http(s) URL is not, and
// mentions.go does not own fetching — it calls the same guarded, bounded,
// SSRF-safe fetch internal/tools gives fetch_url, wrapped here so the seam is
// one function a test can stub. A stub lets the URL branch be exercised
// offline, which is what keeps this file's tests in the default set.
//
// The gate above the seam: fetchImageWebEnabled. The seam itself is only
// reached when that gate says web tools are on.
//
// Returning an error is what makes a failed download a reportable refusal
// rather than a silently missing image.
type MentionImageFetcher func(ctx context.Context, url string) ([]byte, error)

// fetchMentionImage is the seam, swappable in tests exactly as the modelIDs
// var is — so a test drives the REAL resolve loop built here instead of
// hand-copying its shape, and a wiring regression fails the test rather than
// going unnoticed.
var fetchMentionImage MentionImageFetcher = func(ctx context.Context, u string) ([]byte, error) {
	// One extra base64 round-trip of headroom over the image cap: the cap is
	// a verdict on the image bytes, which tools.AttachImage applies to
	// whatever comes back, so the download itself must not fail a file the
	// cap would still have refused with a clearer message.
	return tools.FetchPublicImageBytes(ctx, u, tools.ImageMaxBytes())
}

// fetchImageWebEnabled reports whether the web kill-switch (tools.enable_web,
// the same gate fetch_url and web_search obey through IsToolEnabled) allows
// an image ADDRESS to be downloaded (#218 review). Checked BEFORE any fetch:
// an `@https://…` mention or a serve `{url}` attachment is network egress the
// coder did not ask a tool for, and an operator who set enable_web false to
// stay offline must not get a download from typing a link in prose. A nil
// deps is ungated exactly as IsToolEnabled treats a nil Config (all tools on).
func fetchImageWebEnabled(deps tools.ToolDeps) bool {
	if deps == nil {
		return true
	}
	return deps.IsToolEnabled(tools.FunctionFetchURL)
}

// MentionRefusal is an image mention that resolved to nothing attachable, and
// the reason the human is told (#218). It is returned as data rather than
// printed here because mentions.go is a parser with no output channel — and
// because the reason must be REPORTED and not silently dropped: a screenshot
// that failed to attach would otherwise be indistinguishable from a typo, and
// the human would believe it was sent.
type MentionRefusal struct {
	// Ref is the reference as typed.
	Ref string
	// Reason is the leaf's one-sentence explanation (not an image, over the
	// size cap, unreadable, a failed download, a text-only model).
	Reason string
}

// processMentions scans input for @path mentions and returns:
//   - the input with every RESOLVED mention replaced by a short
//     "[@path attached]" marker (so the model's prompt stays clean) —
//     unresolved mentions and all other text are left byte-for-byte intact;
//   - the text attachment block: for each resolved non-image mention, either
//     the file content (small) or a skeleton (large), prefixed with the path
//     and a size note;
//   - the image attachments (#218), in the order the mentions appear, for the
//     turn to carry as content parts on its user message;
//   - the image mentions that did NOT attach, with their reasons (#218), for
//     the caller to report to the human. A mention that merely failed to
//     resolve as a path at all (a typo, an escape, a directory) is NOT here —
//     that case has always been silent, and the text left in the line is its
//     own report. Only mentions that looked like images get a reason.
//
// A mention that names a non-existent, unreadable, or out-of-workspace path
// is left as the user typed it — the model sees the original text and can
// course-correct on its own (the file simply isn't attached).
//
// deps answers the vision verdict for image mentions (the #217 ImageGate
// seam): a text-only model gets no image attachment, and the refusal is
// reported to the HUMAN (see MentionAttachmentLines) rather than pasted into
// the prompt — bytes the #216 wire gate would reject never get that far. A nil
// deps is ungated, leaving the wire gate as the backstop.
func processMentions(ctx context.Context, workspaceRoot, input string, deps tools.ToolDeps) (cleanInput, textAttachment string, images []TurnImage, refused []MentionRefusal) {
	matches := mentionRe.FindAllStringIndex(input, -1)
	if len(matches) == 0 {
		return input, "", nil, nil
	}
	var b strings.Builder
	var attach strings.Builder
	last := 0
	// resolve closes a mention: the text before it is kept, the mention itself
	// becomes "[@ref attached]", and the user's own trailing punctuation is
	// re-emitted after the marker so it is never silently deleted.
	resolve := func(mentionStart, mentionEnd int, ref, punct string) {
		b.WriteString(input[last:mentionStart])
		b.WriteString("[@" + ref + " attached]" + punct)
		last = mentionEnd
	}
	for _, m := range matches {
		// The match may start with a leading whitespace run; the mention
		// proper is the part from the "@" on.
		mentionStart := m[0]
		for mentionStart < m[1] && input[mentionStart] != '@' {
			mentionStart++
		}
		mention := input[mentionStart:m[1]]
		// The target is the mention minus the "@" marker; trailing sentence
		// punctuation is stripped for RESOLUTION ("@main.go," means main.go)
		// but re-emitted after the marker. An address keeps its own, narrower
		// set (see mentionPunctURL).
		raw := mention[1:]
		punctSet := mentionPunct
		if isMentionURL(raw) {
			punctSet = mentionPunctURL
		}
		punct := raw[len(strings.TrimRight(raw, punctSet)):]
		ref := strings.TrimRight(raw, punctSet)
		if ref == "" {
			// "@." or "@," — no path at all: prose, leave it.
			continue
		}

		// --- image ADDRESS mention (#218): fetch, then the shared loader.
		if addr, ok := tools.ImageURL(ref); ok {
			// The web kill-switch gates the download BEFORE it happens: with
			// tools.enable_web false, a URL mention is refused and left exactly
			// as typed — the bytes are never fetched, so an offline posture
			// survives prose that happens to contain a link.
			if !fetchImageWebEnabled(deps) {
				refused = append(refused, MentionRefusal{Ref: addr, Reason: addr + " was not fetched — web tools are disabled (tools.enable_web: false), so image addresses cannot be downloaded"})
				continue
			}
			data, fetchErr := fetchMentionImageBytes(ctx, addr)
			att := tools.AttachImageWithErr(deps, addr, "", data, fetchErr)
			if att.Refused {
				// An address that yields no attachable image stays in the line
				// exactly as typed — the same rule a path that did not resolve
				// follows — so the human's prose is never rewritten and the
				// model is never shown bytes it cannot see. The reason is
				// reported, because "it didn't attach" is information.
				refused = append(refused, MentionRefusal{Ref: addr, Reason: att.Reason})
				continue
			}
			images = append(images, mentionedAttachment(att))
			resolve(mentionStart, m[1], att.Ref, punct)
			continue
		}

		// Confine to the workspace with the same rules read_file's paths use
		// (tools.ConfinePath: lexical + symlink-resolved). A refusal is
		// treated like any other non-resolution: the text is kept as typed.
		call := tools.ToolCall{Function: tools.FunctionCall{Name: "path", Arguments: fmt.Sprintf(`{"path":%q}`, ref)}}
		if _, err := tools.ConfinePath(call, workspaceRoot); err != nil {
			continue
		}
		abs := filepath.Join(workspaceRoot, filepath.Clean(ref))

		// --- image FILE mention (#218): the leaf's own probe, so the
		// extension table stays in one place (read_file's #217 rules decide
		// what an image is, here and there).
		if tools.IsImagePath(ref, abs) {
			data, loadErr := tools.LoadImageBytes(abs, tools.ImageMaxBytes())
			att := tools.AttachImageWithErr(deps, ref, abs, data, loadErr)
			if att.Refused {
				// Over the cap, unreadable, or a text-only model: the mention is
				// left as typed and the reason is reported to the human.
				refused = append(refused, MentionRefusal{Ref: ref, Reason: att.Reason})
				continue
			}
			images = append(images, mentionedAttachment(att))
			resolve(mentionStart, m[1], ref, punct)
			continue
		}

		info, err := os.Stat(abs)
		if err != nil {
			continue
		}
		if info.IsDir() {
			// A directory isn't attachable; the path is left as typed.
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		// Same size rule as read_file (internal/tools.readFile): a whole file
		// above the curation budget is NOT inlined — its structural outline
		// (the same skeleton read_file hands back) goes in its place, with a
		// pointer to study for the detail.
		estTokens := len(data) / 4
		if estTokens > tools.DefaultLimits().CurationBudgetTokens {
			skel, skelErr := outlineFile(abs)
			if skelErr != nil || strings.TrimSpace(skel) == "" {
				skel = "(outline unavailable)"
			}
			fmt.Fprintf(&attach, "=== @%s (~%d tokens — too large to inline; outline below, study for detail) ===\n%s\n", ref, estTokens, skel)
		} else {
			fmt.Fprintf(&attach, "=== @%s (%d bytes, inlined) ===\n%s\n", ref, len(data), string(data))
		}
		resolve(mentionStart, m[1], ref, punct)
	}
	b.WriteString(input[last:])
	return b.String(), attach.String(), images, refused
}

// mentionedAttachment narrows a loaded tools.ImageAttachment to what the turn
// carries. The leaf has already refused anything unusable, so this is a plain
// field copy; Bytes/Tokens are dropped because the session re-derives the
// image's cost from the part itself (estTurnTokens over its data URI), and a
// second figure here would be one more thing that could disagree.
func mentionedAttachment(att tools.ImageAttachment) TurnImage {
	return TurnImage{Ref: att.Ref, Part: att.Part}
}

// printMentionImages prints what the image mentions did after the turn was
// submitted (#218): one line per image that reached the model, then the
// reasons for the ones that did not. It takes the turn's own notes too
// (TurnResult.ImageNotes) because the session — not the parser — is what
// knows the model's vision verdict, so "no parts were sent" is only known
// after the turn ran.
//
// Lines go to stdout through the same dim style as every other turn-boundary
// receipt, so the anchored (pinned-prompt) mode funnels them into the
// scrollback instead of fighting the live display.
func printMentionImages(images []TurnImage, refusals []MentionRefusal, turnNotes []string) {
	if len(images) == 0 && len(refusals) == 0 && len(turnNotes) == 0 {
		return
	}
	for _, line := range MentionAttachmentLines(images, refusals) {
		fmt.Println(style.Paint("  "+line, style.Dim))
	}
	for _, note := range turnNotes {
		fmt.Println(style.Paint("  "+note, style.Dim))
	}
}

// MentionAttachmentLines renders what the image mentions did, one line each,
// for the HUMAN (issue #218): "shown as an attachment line" in the issue's
// words. It lives beside the parser so the REPL, the headless turn driver, and
// any later adapter print the same thing from the same data.
//
// It reports both outcomes because a mention the model does NOT see must not
// pass for silence: an image that could not be attached would otherwise look
// identical to a typo'd mention, and the human would have no idea the
// screenshot they meant to send never arrived.
func MentionAttachmentLines(images []TurnImage, refused []MentionRefusal) []string {
	var lines []string
	for _, img := range images {
		bytes := imageDataURIRawBytes(img.Part.DataURI)
		lines = append(lines, fmt.Sprintf("attached image: %s (%s, %d bytes, ~%d tokens)",
			img.Ref, img.Part.MediaType, bytes, tools.ImageTokensOf(bytes)))
	}
	for _, r := range refused {
		lines = append(lines, "image not attached: "+r.Reason)
	}
	return lines
}

// isMentionURL reports whether a mention's target is written as a remote
// address (http/https). The scheme is matched as a prefix rather than parsed,
// because url.Parse reads a Windows path like `C:\shots\a.png` as scheme "c"
// — a path must never be mistaken for an address here.
func isMentionURL(raw string) bool {
	low := strings.ToLower(raw)
	return strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://")
}

// fetchMentionImageBytes runs the seam defensively: a missing seam is an
// error, not a nil-map panic, and the address never appears twice in the
// message (the wrapped error names it once).
func fetchMentionImageBytes(ctx context.Context, addr string) ([]byte, error) {
	if fetchMentionImage == nil {
		return nil, errors.New("no image fetcher is configured for URL mentions")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := fetchMentionImage(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", addr, err)
	}
	return data, nil
}

// outlineFile returns the structural outline of path — the same helper
// read_file's too-large path uses (internal/tools.fileSkeleton), so the
// mention attachment and read_file can't drift apart in what a large file
// looks like. Returns an error only when outline.Render itself fails.
func outlineFile(path string) (string, error) {
	return outline.Render(path, 8000)
}

// mentionCompleter builds the completer sources the REPL wires into
// lineedit's SetCompletion. It is kept here (rather than in main.go) so the
// REPL's loop stays focused on the turn pipeline.
//
// Model ids come from ONE source: the slash completer's Sub hook. (A separate
// "model" entry here would double every id in the candidate row and break the
// first-Tab fill, since the engine fills against the word at the cursor — and
// "/model <id>" is never a prefix of the typed argument word.)
//
// modelIDs is a var (not a func) so a test can inject its own id list and
// drive the REAL map mentionCompleter builds instead of hand-copying its
// shape — a regression in the wiring (e.g. a separate "model" entry
// re-appearing) fails the test instead of going unnoticed.
var modelIDs = modelIDsImpl

func mentionCompleter(session *CortexSession) map[string]lineedit.Completer {
	// The fixed slash-command set (the REPL's vocabulary, passed in so
	// lineedit stays free of cmd/cortex).
	commands := []string{
		"/clear", "/compact", "/context", "/help", "/hook",
		"/model", "/plan", "/quit", "/sessions",
	}
	return map[string]lineedit.Completer{
		"slash": lineedit.SlashCompleter{
			Commands: commands,
			Sub: func(line string, cursor int) []string {
				// Only /model has a continuation today.
				if !strings.HasPrefix(line, "/model ") {
					return nil
				}
				return lineedit.ModelCompleter{Names: func() []string {
					return modelIDs(session)
				}}.Candidates(line, cursor)
			},
		},
		"path": lineedit.PathCompleter{
			Root:          session.root(),
			MaxCandidates: 50,
		},
	}
}

// modelIDsImpl returns the set of model ids the /model command can switch to:
// the currently bound code/study models plus every id the fleet knows.
func modelIDsImpl(session *CortexSession) []string {
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	add(session.Request.Model)
	add(session.Study.Model)
	for id := range session.Fleet {
		add(id)
	}
	return out
}
