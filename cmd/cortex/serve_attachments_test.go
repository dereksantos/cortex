// serve_attachments_test.go — the HTTP surface for image attachments
// (issue #218 step 5): a client names an image by workspace path or URL, and
// it arrives on the turn's wire messages, or the request is refused with a 400
// that says why.
//
// The assertions read the REAL request body the backend received rather than
// the session's in-memory state: "reaching the turn's wire messages" is a
// claim about what leaves the process, and an in-memory check would pass even
// if the parts never survived serialization (Parts is `json:"-"` on the
// transcript, so a bug that dropped them at the transport would otherwise be
// invisible).
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dereksantos/cortex/internal/registry"
	"github.com/dereksantos/cortex/internal/tools"
)

// wireBackend is a model endpoint that RECORDS each request body, so a test
// can assert what the model was actually sent.
type wireBackend struct {
	mu     sync.Mutex
	bodies []string
	srv    *httptest.Server
}

func newWireBackend(t *testing.T) *wireBackend {
	b := &wireBackend{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		b.mu.Lock()
		b.bodies = append(b.bodies, string(body))
		b.mu.Unlock()
		fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":3}}`)
	}))
	t.Cleanup(srv.Close)
	b.srv = srv
	return b
}

// captured returns the request bodies the backend has seen.
func (b *wireBackend) captured() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.bodies...)
}

// reset forgets every request so a subtest's "the turn did NOT run" assertion
// measures only its own request. Without this, a refused turn in the second
// subtest of a shared-harness test still sees the first subtest's successful
// request and reports a refusal that never happened.
func (b *wireBackend) reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.bodies = nil
}

// lastRequestImageURLs reports the image parts on the most recent request's
// user message that actually carries any — the turn's own attachment.
//
// Why not "the last user message": a turn can issue several requests (an
// empty reply triggers a finalize request whose prompt is itself a new user
// message), so the newest user message is not necessarily the one holding the
// image — asserting on position reported a correct wire as an empty one.
// Why not "every part in the body": a request resends the whole conversation,
// so a second attached turn legitimately carries the first turn's image too.
// "The newest request's image-bearing user message" is the question the
// assertions actually ask: did THIS turn put these bytes on the wire.
func (b *wireBackend) lastRequestImageURLs() []string {
	bodies := b.captured()
	if len(bodies) == 0 {
		return nil
	}
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(bodies[len(bodies)-1]), &req); err != nil {
		return nil
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role != "user" {
			continue
		}
		if got := imageURLsInContent(req.Messages[i].Content); len(got) > 0 {
			return got
		}
	}
	return nil
}

// imageURLsInContent pulls the image_url parts out of one message's content,
// which is either a plain string (no parts, skipped) or an array of parts.
func imageURLsInContent(content json.RawMessage) []string {
	var parts []struct {
		Type     string `json:"type"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err := json.Unmarshal(content, &parts); err != nil {
		return nil
	}
	var out []string
	for _, p := range parts {
		if p.Type == "image_url" && p.ImageURL.URL != "" {
			out = append(out, p.ImageURL.URL)
		}
	}
	return out
}

// serveAttachFixture writes a genuine PNG into the workspace and returns its
// workspace-relative path plus raw bytes.
func serveAttachFixture(t *testing.T, root, rel string) (string, []byte) {
	t.Helper()
	raw := attachTestPNG(13, 200)
	abs := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, raw, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return rel, raw
}

// serveAttachHarness wires a registry rooted at a temp workspace, a backend
// that records the wire, and a live session; returns the manager, the turn
// endpoint URL, and the workspace root.
func serveAttachHarness(t *testing.T) (*wireBackend, string, string) {
	t.Helper()
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	backend := newWireBackend(t)
	mgr := NewSessionManager(reg, func() *CortexSession {
		cs := &CortexSession{quiet: true, Request: CortexArgs{}.Request()}
		cs.Request.BaseURL = backend.srv.URL
		cs.Request.Vision = true // the verdict a vision-capable binding stamps
		return cs
	})
	ts := newTestServeServer(t, newServeMux(reg, mgr, "", "", testLoopsStore(t), newRunningSet()))
	t.Cleanup(ts.Close)
	created, err := mgr.Create("blog")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return backend, ts.URL + "/api/projects/blog/sessions/" + created.ID() + "/turn", root
}

func postTurnJSON(t *testing.T, url, body string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, string(raw)
}

// TestServeTurnAcceptsWorkspaceImagePath is the first acceptance: a workspace
// image path is accepted and the bytes reach the model's wire messages on the
// turn's user message, alongside the human's text.
func TestServeTurnAcceptsWorkspaceImagePath(t *testing.T) {
	backend, turnURL, root := serveAttachHarness(t)
	rel, raw := serveAttachFixture(t, root, "shots/ui.png")

	body, err := json.Marshal(turnRequest{
		Input:       "what is in this screenshot?",
		Attachments: []TurnAttachment{{Path: rel}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, text := postTurnJSON(t, turnURL, string(body))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", resp.StatusCode, text)
	}

	imgs := backend.lastRequestImageURLs()
	if len(imgs) != 1 {
		t.Fatalf("the model saw %d image parts, want exactly 1", len(imgs))
	}
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw)
	if imgs[0] != want {
		t.Errorf("the wire image is not the fixture's bytes (%d vs %d)", len(imgs[0]), len(want))
	}
}

// TestServeTurnRefusesOutOfWorkspacePath is the security acceptance, and the
// one that matters most: an attachment is a client-directed file read, so the
// boundary is read_file's boundary, and crossing it must be a 400 that reads
// as a refusal — not a 200 whose answer simply ignored the image.
func TestServeTurnRefusesOutOfWorkspacePath(t *testing.T) {
	backend, turnURL, root := serveAttachHarness(t)

	// Files that genuinely exist OUTSIDE the session's workspace, and are
	// genuine IMAGES: an escape attempt aimed at a non-image would be refused
	// by the image sniffer alone, and the test would pass with confinement
	// deleted. These distinguish "the boundary stopped it" from "it happened
	// not to look like a PNG".
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "secret.png")
	raw := attachTestPNG(17, 64)
	if err := os.WriteFile(outside, raw, 0o644); err != nil {
		t.Fatalf("write outside fixture: %v", err)
	}
	// A second outside image reached by a deep .. walk from INSIDE the
	// workspace, so the reference starts inside and leaves: the shape a real
	// escape attempt takes.
	shotsDir := filepath.Join(root, "shots")
	if err := os.MkdirAll(shotsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	deepRel, err := filepath.Rel(shotsDir, outside)
	if err != nil {
		t.Fatalf("rel: %v", err)
	}

	tests := []struct {
		name string
		path string
		// boundary: the refusal must name the workspace, because this case is
		// an ESCAPE attempt and "no such file" would hide the decision. The
		// non-boundary case below is refused for a different true reason (a
		// directory is not an image) and must not be dressed up as one.
		boundary bool
	}{
		{"absolute path outside the workspace", outside, true},
		{"dot-dot escape to a sibling", "../" + filepath.Base(outsideDir) + "/secret.png", true},
		{"deep dot-dot walk from inside the workspace", filepath.Join("shots", deepRel), true},
		{"workspace root itself", ".", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend.reset()
			body, _ := json.Marshal(turnRequest{Input: "read this", Attachments: []TurnAttachment{{Path: tt.path}}})
			resp, text := postTurnJSON(t, turnURL, string(body))
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %q)", resp.StatusCode, text)
			}
			// An escape attempt's message must name a boundary violation, not
			// merely "no such file": a client needs to know it was REFUSED.
			if tt.boundary && !strings.Contains(text, "workspace") {
				t.Errorf("refusal %q must name the workspace boundary", text)
			}
			// The turn must NOT have run: a refused attachment that still
			// answers is the silent-failure mode this whole path exists to
			// avoid.
			if n := len(backend.captured()); n != 0 {
				t.Errorf("the turn ran (%d backend requests) despite the refusal", n)
			}
			// And nothing outside the workspace was written into the session.
			if _, err := os.Stat(filepath.Join(root, "secret.png")); err == nil {
				t.Error("the outside file was copied into the workspace")
			}
		})
	}

	t.Run("a symlink inside the workspace pointing out is refused", func(t *testing.T) {
		// ConfinePath resolves symlinks for exactly this: a purely lexical
		// check would wave it through because the LINK path is inside root.
		link := filepath.Join(root, "link.png")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		body, _ := json.Marshal(turnRequest{Input: "via symlink", Attachments: []TurnAttachment{{Path: "link.png"}}})
		resp, text := postTurnJSON(t, turnURL, string(body))
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body %q)", resp.StatusCode, text)
		}
		if n := len(backend.captured()); n != 0 {
			t.Errorf("the turn ran despite the symlink escape, %d requests", n)
		}
	})
}

// TestServeTurnAcceptsURLAttachment is the URL acceptance: the address is
// fetched by the server and sent as the image's bytes.
func TestServeTurnAcceptsURLAttachment(t *testing.T) {
	backend, turnURL, _ := serveAttachHarness(t)

	// Stand in for the network with the same seam @mentions use, so this stays
	// in the default test set and still exercises the real resolve loop.
	orig := fetchMentionImage
	t.Cleanup(func() { fetchMentionImage = orig })
	var gotURL string
	raw := attachTestPNG(19, 150)
	fetchMentionImage = func(_ context.Context, u string) ([]byte, error) {
		gotURL = u
		return raw, nil
	}

	body, _ := json.Marshal(turnRequest{
		Input:       "and this one?",
		Attachments: []TurnAttachment{{URL: "https://cdn.example.com/a.png"}},
	})
	resp, text := postTurnJSON(t, turnURL, string(body))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", resp.StatusCode, text)
	}
	if gotURL != "https://cdn.example.com/a.png" {
		t.Errorf("fetched %q, want the address as sent", gotURL)
	}
	imgs := backend.lastRequestImageURLs()
	if len(imgs) != 1 {
		t.Fatalf("the model saw %d image parts, want 1", len(imgs))
	}
	if imgs[0] != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(raw) {
		t.Error("the fetched bytes are not what reached the wire")
	}
}

// TestServeTurnRejectsMalformedBody is the 400-on-garbage acceptance, and it
// pins that a malformed body never becomes a turn — including the shapes that
// would decode into a half-formed attachment.
func TestServeTurnRejectsMalformedBody(t *testing.T) {
	backend, turnURL, _ := serveAttachHarness(t)

	tests := []struct {
		name string
		body string
		// Every case here must be refused before the turn, so the backend
		// stays untouched (asserted below).
	}{
		{"not json at all", "this is not json"},
		{"truncated json", `{"input":"half`},
		{"json of the wrong shape", `["input","hello"]`},
		{"attachments not an array", `{"input":"x","attachments":"shots/ui.png"}`},
		{"attachment not an object", `{"input":"x","attachments":["shots/ui.png"]}`},
		{"path not a string", `{"input":"x","attachments":[{"path":42}]}`},
		{"empty attachment object", `{"input":"x","attachments":[{}]}`},
		{"both path and url", `{"input":"x","attachments":[{"path":"a.png","url":"https://x.test/b.png"}]}`},
		{"url is not http(s)", `{"input":"x","attachments":[{"url":"file:///etc/passwd"}]}`},
		{"url has no scheme", `{"input":"x","attachments":[{"url":"cdn.example.com/a.png"}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, text := postTurnJSON(t, turnURL, tt.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %q)", resp.StatusCode, text)
			}
			if n := len(backend.captured()); n != 0 {
				t.Errorf("a refused request still ran the turn (%d backend requests)", n)
			}
		})
	}
}

// TestServeTurnStreamAcceptsAttachments covers the SSE endpoint: same rules,
// and the shared helper means it cannot have drifted — but it is a separate
// handler, so it gets its own acceptance rather than being assumed.
func TestServeTurnStreamAcceptsAttachments(t *testing.T) {
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	backend := newWireBackend(t)
	mgr := NewSessionManager(reg, func() *CortexSession {
		cs := &CortexSession{quiet: true, Request: CortexArgs{}.Request()}
		cs.Request.BaseURL = backend.srv.URL
		cs.Request.Vision = true
		return cs
	})
	ts := newTestServeServer(t, newServeMux(reg, mgr, "", "", testLoopsStore(t), newRunningSet()))
	defer ts.Close()
	created, err := mgr.Create("blog")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	streamURL := ts.URL + "/api/projects/blog/sessions/" + created.ID() + "/turn/stream"
	rel, raw := serveAttachFixture(t, root, "shots/stream.png")

	t.Run("an attachment reaches the streamed turn", func(t *testing.T) {
		body, _ := json.Marshal(turnRequest{Input: "describe", Attachments: []TurnAttachment{{Path: rel}}})
		req, _ := http.NewRequest(http.MethodPost, streamURL, strings.NewReader(string(body)))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		defer resp.Body.Close()
		sse, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if !strings.Contains(string(sse), "result") {
			t.Errorf("no result event in the stream:\n%s", sse)
		}
		imgs := backend.lastRequestImageURLs()
		if len(imgs) != 1 {
			t.Fatalf("the streamed turn carried %d image parts, want 1", len(imgs))
		}
		// The bytes, not merely "an image": a part carrying something else
		// (a stale path, an empty URI) would still count as one.
		if want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw); imgs[0] != want {
			t.Errorf("the streamed image is not the fixture's bytes (%d vs %d)", len(imgs[0]), len(want))
		}
	})

	t.Run("an out-of-workspace path is a 400, not an event stream", func(t *testing.T) {
		// The refusal must be a real HTTP status BEFORE the SSE headers: an
		// event-stream that begins with an error leaves the client parsing
		// failures as progress.
		body, _ := json.Marshal(turnRequest{Input: "escape", Attachments: []TurnAttachment{{Path: "../../etc/passwd"}}})
		req, _ := http.NewRequest(http.MethodPost, streamURL, strings.NewReader(string(body)))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		defer resp.Body.Close()
		if got := resp.Header.Get("Content-Type"); strings.Contains(got, "text/event-stream") {
			t.Error("the refusal came back as an event stream, want a plain HTTP error")
		}
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", resp.StatusCode)
		}
	})

	t.Run("a malformed body is a 400", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, streamURL, strings.NewReader("{oops"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", resp.StatusCode)
		}
	})
}

// TestServeTurnAcceptsPostedBytes covers the browser-picker path (#218 step 6):
// `data` carries base64 image bytes, because a file the coder picked in a
// browser has no workspace path and nothing for the server to fetch.
//
// It is also the regression test for a bug this step introduced and caught:
// the resolver's `default:` branch originally sat BEFORE `case a.Data != ""`,
// which made the data case unreachable — every posted image fell through to the
// URL branch and was refused as "not an http(s) image url". A `{data}` request
// 400ing while the UI happily sends one is exactly what this pins.
func TestServeTurnAcceptsPostedBytes(t *testing.T) {
	backend, turnURL, _ := serveAttachHarness(t)
	raw := attachTestPNG(23, 180)
	uri := "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw)

	t.Run("a posted png reaches the wire", func(t *testing.T) {
		body, _ := json.Marshal(turnRequest{
			Input:       "what is this?",
			Attachments: []TurnAttachment{{Data: uri, Name: "clipboard.png"}},
		})
		resp, text := postTurnJSON(t, turnURL, string(body))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%q)", resp.StatusCode, text)
		}
		imgs := backend.lastRequestImageURLs()
		if len(imgs) != 1 {
			t.Fatalf("the model saw %d image parts, want 1", len(imgs))
		}
		if imgs[0] != uri {
			t.Errorf("the wire image is not the posted bytes (%d vs %d)", len(imgs[0]), len(uri))
		}
	})

	t.Run("bare base64 without the data: header also works", func(t *testing.T) {
		backend.reset()
		bare := base64.StdEncoding.EncodeToString(raw)
		body, _ := json.Marshal(turnRequest{Input: "again", Attachments: []TurnAttachment{{Data: bare}}})
		resp, text := postTurnJSON(t, turnURL, string(body))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%q)", resp.StatusCode, text)
		}
		if imgs := backend.lastRequestImageURLs(); len(imgs) != 1 {
			t.Errorf("saw %d image parts, want 1: requests=%d urls=%v", len(imgs), len(backend.captured()), imgs)
		}
	})

	t.Run("a declared media type outside the four is refused", func(t *testing.T) {
		backend.reset()
		evil := "data:application/x-msdownload;base64," + base64.StdEncoding.EncodeToString(raw)
		body, _ := json.Marshal(turnRequest{Input: "exe", Attachments: []TurnAttachment{{Data: evil, Name: "tool.exe"}}})
		resp, text := postTurnJSON(t, turnURL, string(body))
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (%q)", resp.StatusCode, text)
		}
		if n := len(backend.captured()); n != 0 {
			t.Errorf("the turn ran despite the refused payload (%d requests)", n)
		}
	})

	t.Run("bytes that are not an image are refused", func(t *testing.T) {
		// Declared as a png but the bytes are prose: the sniffer, not the
		// declaration, decides — the same verdict a path or URL attachment gets.
		notImage := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("just some text, not a png at all"))
		body, _ := json.Marshal(turnRequest{Input: "fake", Attachments: []TurnAttachment{{Data: notImage, Name: "fake.png"}}})
		resp, text := postTurnJSON(t, turnURL, string(body))
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (%q)", resp.StatusCode, text)
		}
	})

	t.Run("malformed base64 is refused", func(t *testing.T) {
		body, _ := json.Marshal(turnRequest{Input: "junk", Attachments: []TurnAttachment{{Data: "data:image/png;base64,@@@not base64@@@"}}})
		resp, text := postTurnJSON(t, turnURL, string(body))
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (%q)", resp.StatusCode, text)
		}
	})

	t.Run("more than one source per attachment is refused", func(t *testing.T) {
		// path+data, url+data, and path+url must all be a 400: precedence
		// between two sources is undefined, and silently picking one keeps a
		// malformed request quietly "working".
		cases := []TurnAttachment{
			{Path: "a.png", Data: uri},
			{URL: "https://x.test/a.png", Data: uri},
			{Path: "a.png", URL: "https://x.test/a.png"},
		}
		for i, a := range cases {
			body, _ := json.Marshal(turnRequest{Input: "two sources", Attachments: []TurnAttachment{a}})
			resp, text := postTurnJSON(t, turnURL, string(body))
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("case %d: status = %d, want 400 (%q)", i, resp.StatusCode, text)
			}
			if !strings.Contains(text, "exactly one") {
				t.Errorf("case %d: refusal %q must say exactly one source is allowed", i, text)
			}
		}
	})
}

// TestServeTurnURLAttachmentWebDisabledIsRefused is the enable_web acceptance
// on the serve side (#218 review): a `{url}` attachment is network egress, so
// it must obey the same tools.enable_web kill-switch fetch_url does — refused
// with a 400 that names the switch, before the fetch seam is ever touched.
func TestServeTurnURLAttachmentWebDisabledIsRefused(t *testing.T) {
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	backend := newWireBackend(t)
	off := false
	mgr := NewSessionManager(reg, func() *CortexSession {
		cs := &CortexSession{quiet: true, Request: CortexArgs{}.Request()}
		cs.Request.BaseURL = backend.srv.URL
		cs.Request.Vision = true
		// The REAL gate: IsToolEnabled reads Tools.EnableWeb, the same field
		// fetch_url's dispatch gate reads.
		cs.Config = &Config{Tools: ToolConfig{EnableWeb: &off}}
		return cs
	})
	ts := newTestServeServer(t, newServeMux(reg, mgr, "", "", testLoopsStore(t), newRunningSet()))
	defer ts.Close()
	created, err := mgr.Create("blog")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	orig := fetchMentionImage
	t.Cleanup(func() { fetchMentionImage = orig })
	called := false
	fetchMentionImage = func(context.Context, string) ([]byte, error) {
		called = true
		return attachTestPNG(29, 64), nil
	}

	body, _ := json.Marshal(turnRequest{
		Input:       "fetch this",
		Attachments: []TurnAttachment{{URL: "https://cdn.example.com/a.png"}},
	})
	resp, text := postTurnJSON(t, ts.URL+"/api/projects/blog/sessions/"+created.ID()+"/turn", string(body))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%q)", resp.StatusCode, text)
	}
	if !strings.Contains(text, "enable_web") {
		t.Errorf("refusal %q must name tools.enable_web", text)
	}
	if called {
		t.Error("the fetch seam was called despite web tools being disabled")
	}
	if n := len(backend.captured()); n != 0 {
		t.Errorf("the turn ran (%d backend requests) despite the refusal", n)
	}

	// The resolver unit, same table: web off refuses a url before the seam.
	cs := &CortexSession{workspace: &Workspace{Root: root}, deleteRoot: root, Request: CortexArgs{}.Request(), Config: &Config{Tools: ToolConfig{EnableWeb: &off}}}
	cs.Request.Vision = true
	called = false
	imgs, attErr := resolveTurnAttachments(context.Background(), cs, []TurnAttachment{{URL: "https://cdn.example.com/a.png"}})
	if attErr == nil || !strings.Contains(attErr.Reason, "enable_web") {
		t.Errorf("resolver refusal = %v, want one naming enable_web", attErr)
	}
	if imgs != nil || called {
		t.Errorf("a web-disabled url must resolve nothing and fetch nothing (imgs=%v called=%v)", imgs, called)
	}
}

// TestServeTurnNoAttachmentsUnchanged is the regression floor: the ordinary
// request shape must behave exactly as before — same 200, one request, no
// image parts on the wire.
func TestServeTurnNoAttachmentsUnchanged(t *testing.T) {
	backend, turnURL, _ := serveAttachHarness(t)
	resp, text := postTurnJSON(t, turnURL, `{"input":"hello"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%q)", resp.StatusCode, text)
	}
	if !strings.Contains(text, `"reply":"ok"`) {
		t.Errorf("reply missing: %s", text)
	}
	if got := backend.lastRequestImageURLs(); len(got) != 0 {
		t.Errorf("a turn with no attachments put %d image parts on the wire", len(got))
	}
}

// TestServeTurnTextOnlyModelRefusesAttachment: the serve path must not smuggle
// an image to a model that cannot take one. The #216 wire gate refuses the
// whole request, so the turn would fail — better that the attachment is
// dropped with an explanation than the endpoint 500s.
func TestServeTurnTextOnlyModelRefusesAttachment(t *testing.T) {
	root := t.TempDir()
	reg := &fakeRegistry{projects: map[string]registry.Project{"blog": {Name: "blog", Root: root}}}
	backend := newWireBackend(t)
	mgr := NewSessionManager(reg, func() *CortexSession {
		cs := &CortexSession{quiet: true, Request: CortexArgs{}.Request()}
		cs.Request.BaseURL = backend.srv.URL
		cs.Request.Vision = false // the text-only verdict
		return cs
	})
	ts := newTestServeServer(t, newServeMux(reg, mgr, "", "", testLoopsStore(t), newRunningSet()))
	defer ts.Close()
	created, _ := mgr.Create("blog")
	rel, _ := serveAttachFixture(t, root, "shots/x.png")

	body, _ := json.Marshal(turnRequest{Input: "look", Attachments: []TurnAttachment{{Path: rel}}})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/projects/blog/sessions/"+created.ID()+"/turn", strings.NewReader(string(body)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	// Two acceptable outcomes, both honest: refused at the door (400), or the
	// turn runs WITHOUT the image (200 + no parts). What must never happen is
	// a 200 that claims the image was attached.
	if resp.StatusCode == http.StatusOK {
		if got := backend.lastRequestImageURLs(); len(got) != 0 {
			t.Errorf("a text-only model was sent %d image parts; the wire gate would refuse the request", len(got))
		}
		return
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 200 (image dropped) or 400 (refused); body %q", resp.StatusCode, raw)
	}
}

// TestResolveTurnAttachmentsUnits covers the resolver directly, including the
// ordering rule the handlers depend on: the first bad attachment stops
// resolution, so a partially-attached turn never reaches the model.
func TestResolveTurnAttachmentsUnits(t *testing.T) {
	root := t.TempDir()
	good, _ := serveAttachFixture(t, root, "a.png")
	_, _ = serveAttachFixture(t, root, "b.png")
	notImage := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(notImage, []byte("just prose"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cs := &CortexSession{workspace: &Workspace{Root: root}, deleteRoot: root, Request: CortexArgs{}.Request()}
	cs.Request.Vision = true

	t.Run("empty list is nil not error", func(t *testing.T) {
		imgs, attErr := resolveTurnAttachments(context.Background(), cs, nil)
		if attErr != nil || imgs != nil {
			t.Errorf("got (%v, %v), want (nil, nil)", imgs, attErr)
		}
	})

	t.Run("two good paths both resolve in order", func(t *testing.T) {
		imgs, attErr := resolveTurnAttachments(context.Background(), cs, []TurnAttachment{{Path: good}, {Path: "b.png"}})
		if attErr != nil {
			t.Fatalf("unexpected refusal: %v", attErr)
		}
		if len(imgs) != 2 || imgs[0].Ref != good || imgs[1].Ref != "b.png" {
			t.Errorf("resolved %v, want both in request order", imgs)
		}
	})

	t.Run("a non-image path is refused", func(t *testing.T) {
		_, attErr := resolveTurnAttachments(context.Background(), cs, []TurnAttachment{{Path: "notes.txt"}})
		if attErr == nil {
			t.Fatal("a prose file must not attach as an image")
		}
		if !strings.Contains(attErr.Error(), "notes.txt") {
			t.Errorf("refusal must name the attachment: %v", attErr)
		}
	})

	t.Run("the first bad attachment stops resolution", func(t *testing.T) {
		// Ordering matters: appending the good one and ignoring the bad one
		// would let the model answer about an image it never got.
		imgs, attErr := resolveTurnAttachments(context.Background(), cs, []TurnAttachment{{Path: good}, {Path: "../escape.png"}})
		if attErr == nil {
			t.Fatal("expected a refusal")
		}
		if imgs != nil {
			t.Errorf("a refused request must resolve no images, got %d", len(imgs))
		}
		if attErr.Index != 1 {
			t.Errorf("refusal names attachment %d, want the offending one (1)", attErr.Index)
		}
	})

	t.Run("the leaf's image verdict is the same one read_file uses", func(t *testing.T) {
		// Both entry points ask the same question, so a file cannot be an
		// image to the HTTP endpoint and not to read_file.
		if !tools.IsImagePath(good, filepath.Join(root, good)) {
			t.Errorf("%s must count as an image path", good)
		}
		if tools.IsImagePath("notes.txt", notImage) {
			t.Error("notes.txt must not count as an image path")
		}
	})
}
