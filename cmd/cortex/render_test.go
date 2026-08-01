package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSplitBlocks(t *testing.T) {
	tests := []struct {
		name       string
		pending    string
		wantBlocks []string
		wantRest   string
	}{
		{
			name:       "incomplete line held entirely",
			pending:    "Here's the plan",
			wantBlocks: nil,
			wantRest:   "Here's the plan",
		},
		{
			name:       "paragraph held until blank line",
			pending:    "one line\n",
			wantBlocks: nil,
			wantRest:   "one line\n",
		},
		{
			name:       "paragraph flushes at blank line",
			pending:    "para one\n\npara two",
			wantBlocks: []string{"para one"},
			wantRest:   "para two",
		},
		{
			name:       "multi-line list buffers as one block",
			pending:    "- a\n- b\n- c\n",
			wantBlocks: nil,
			wantRest:   "- a\n- b\n- c\n",
		},
		{
			name:       "open fence is never flushed",
			pending:    "```go\nfunc x() {}\n",
			wantBlocks: nil,
			wantRest:   "```go\nfunc x() {}\n",
		},
		{
			name:       "closed fence flushes as one block",
			pending:    "```go\nfunc x() {}\n```\n",
			wantBlocks: []string{"```go\nfunc x() {}\n```"},
			wantRest:   "",
		},
		{
			name:       "prose before a fence flushes separately",
			pending:    "Look:\n```go\nx := 1\n```\n",
			wantBlocks: []string{"Look:", "```go\nx := 1\n```"},
			wantRest:   "",
		},
		{
			name:       "blank line inside fence is not a boundary",
			pending:    "```\nline1\n\nline2\n```\n",
			wantBlocks: []string{"```\nline1\n\nline2\n```"},
			wantRest:   "",
		},
		{
			name:       "tilde fences recognized",
			pending:    "~~~\ncode\n~~~\n",
			wantBlocks: []string{"~~~\ncode\n~~~"},
			wantRest:   "",
		},
		{
			name:       "heading isolated even without a following blank line",
			pending:    "## Header\nbody text\n\n",
			wantBlocks: []string{"## Header", "body text"},
			wantRest:   "",
		},
		{
			name:       "heading isolated from preceding prose without a blank line",
			pending:    "lead in\n### Header\n\n",
			wantBlocks: []string{"lead in", "### Header"},
			wantRest:   "",
		},
		{
			name:       "consecutive headings each isolated",
			pending:    "# One\n## Two\n\n",
			wantBlocks: []string{"# One", "## Two"},
			wantRest:   "",
		},
		{
			name:       "heading inside a fence is not isolated",
			pending:    "```\n## not a heading\n```\n",
			wantBlocks: []string{"```\n## not a heading\n```"},
			wantRest:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocks, rest := splitBlocks(tt.pending)
			if !reflect.DeepEqual(blocks, tt.wantBlocks) {
				t.Errorf("blocks = %#v, want %#v", blocks, tt.wantBlocks)
			}
			if rest != tt.wantRest {
				t.Errorf("rest = %q, want %q", rest, tt.wantRest)
			}
		})
	}
}

// TestStreamPrinterRenderHoldsOpenFence verifies the streaming guarantee: a
// half-written code fence is never shown until it closes, and once closed its
// code appears (glamour-rendered) in the output.
func TestStreamPrinterRenderHoldsOpenFence(t *testing.T) {
	md := newMarkdownRenderer(80)
	if md == nil {
		t.Fatal("renderer build failed")
	}
	var buf strings.Builder
	p := &streamPrinter{out: &buf, md: md} // nil spinner: no terminal control

	// Stream an opening fence and a code line, but not the close.
	p.onContent("```go\n")
	p.onContent("func answer() int { return 42 }\n")
	if strings.Contains(buf.String(), "answer") {
		t.Fatalf("open fence leaked code before close: %q", buf.String())
	}

	// Close the fence: now the block flushes through glamour.
	p.onContent("```\n")
	p.finish()
	out := stripANSI(buf.String())
	if !strings.Contains(out, "func answer()") {
		t.Errorf("closed fence not rendered; output = %q", out)
	}
}

// TestStreamPrinterRawPathUnchanged confirms md=nil still streams bytes
// verbatim (the path the existing stream_test.go relies on).
func TestStreamPrinterRawPathUnchanged(t *testing.T) {
	var buf strings.Builder
	p := &streamPrinter{out: &buf} // md nil → raw
	p.onContent("plain ")
	p.onContent("text")
	p.finish()
	if !strings.Contains(buf.String(), "plain text") {
		t.Errorf("raw path mangled output: %q", buf.String())
	}
}

// TestStreamPrinterPadsHeadings checks that a heading gets a blank line on
// each side (so it reads as a section break) and that two headings placed
// back-to-back in the source (no blank line between them) share that one
// blank rather than stacking two.
func TestStreamPrinterPadsHeadings(t *testing.T) {
	md := newMarkdownRenderer(80)
	if md == nil {
		t.Fatal("renderer build failed")
	}
	var buf strings.Builder
	p := &streamPrinter{out: &buf, md: md}
	p.emit("# Title\n\nlead paragraph\n\n## Section\n### Sub\n\ntrailing paragraph\n")
	p.finish()

	lines := strings.Split(stripANSI(buf.String()), "\n")
	find := func(want string) int {
		for i, l := range lines {
			if strings.Contains(l, want) {
				return i
			}
		}
		t.Fatalf("line containing %q not found in %q", want, lines)
		return -1
	}

	// Every transition here touches a heading on at least one side (Title,
	// Section, and Sub are all headings), so each pair of consecutive content
	// lines should be separated by exactly one blank line — never zero
	// (no padding) and never two (doubled padding, e.g. from Section's
	// after-blank stacking with Sub's own before-blank).
	content := []int{find("Title"), find("lead paragraph"), find("Section"), find("Sub"), find("trailing paragraph")}
	for i := 1; i < len(content); i++ {
		if gap := content[i] - content[i-1]; gap != 2 {
			t.Errorf("expected exactly one blank line between lines %d and %d, got gap %d: %q",
				content[i-1], content[i], gap, lines)
		}
	}
}

func TestTrimBlockPadding(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"literal trailing spaces", "hello     ", "hello"},
		{"ansi-wrapped trailing spaces", "hi\x1b[38;5;252m \x1b[0m\x1b[38;5;252m \x1b[0m", "hi"},
		{"interior spaces kept", "a b  c   ", "a b  c"},
		{"per line", "one   \ntwo \x1b[0m\nthree", "one\ntwo\nthree"},
		{"styled text preserved", "\x1b[1mbold\x1b[0m   ", "\x1b[1mbold\x1b[0m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := trimBlockPadding(tt.in); got != tt.want {
				t.Errorf("trimBlockPadding(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestTrimLeadingIndent(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain indent", "  Hello", "Hello"},
		{"leading newlines", "\n\n  Hello", "Hello"},
		{"empty sgr pairs then indent", "\x1b[38;5;252m\x1b[0m  \x1b[38;5;252mHello", "\x1b[38;5;252mHello"},
		{"keeps color of first glyph", "  \x1b[1mBold", "\x1b[1mBold"},
		{"nothing to trim", "Hello world", "Hello world"},
		{"interior indent preserved", "First\n  second", "First\n  second"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := trimLeadingIndent(tt.in); got != tt.want {
				t.Errorf("trimLeadingIndent(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// stripANSI removes CSI/OSC escape sequences so assertions can match the
// visible text glamour wraps in styling codes.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			// Skip ESC and a following CSI ("[ … letter") or short sequence.
			i++
			if i < len(s) && s[i] == '[' {
				i++
				for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
					i++
				}
				if i < len(s) {
					i++ // the final byte
				}
				continue
			}
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func TestHumanK(t *testing.T) {
	tests := []struct {
		in   int
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1k"},
		{1500, "1.5k"},
		{8200, "8.2k"},
		{65536, "65.5k"},
		{1000000, "1M"},
		{1048576, "1M"},
		{1500000, "1.5M"},
	}
	for _, tt := range tests {
		if got := humanK(tt.in); got != tt.want {
			t.Errorf("humanK(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCtxColor(t *testing.T) {
	win := 131072
	tests := []struct {
		used int
		want string
	}{
		{0, green},
		{win / 4, green},       // 25%
		{win * 6 / 10, yellow}, // 60%
		{win * 9 / 10, red},    // 90%
		{win, red},             // full
	}
	for _, tt := range tests {
		if got := ctxColor(tt.used, win); got != tt.want {
			t.Errorf("ctxColor(%d/%d) = %q, want %q", tt.used, win, got, tt.want)
		}
	}
}

func TestSessionPrompt(t *testing.T) {
	sess := &CortexSession{Request: CortexArgs{}.Request(), LastPromptTokens: 8200}
	got := sess.Prompt()

	for _, want := range []string{"cortex " + Version, ModelCoder, promptGlyph} {
		if !strings.Contains(got, want) {
			t.Errorf("Prompt() = %q, missing %q", got, want)
		}
	}
	// The old exact "8.2k/32.8k" (LastPromptTokens/window) scalar left the
	// prompt row for the default two-zone gauge bar (contextbar.go) — check
	// its structure renders instead.
	if !strings.Contains(got, "[") || !strings.Contains(got, "|") || !strings.Contains(got, "]") {
		t.Errorf("Prompt() = %q, missing the two-zone gauge bar ([head|tail...])", got)
	}

	// repl.gauge = "numeric" still renders that old scalar form, now off the
	// bar's own head(zone A)+tail(zone B) figures rather than
	// LastPromptTokens (renderContextBar is a pure function of
	// head/tail/window/cells/style — see contextbar.go).
	sess.Config = &Config{Repl: ReplConfig{Gauge: "numeric"}}
	wantNumeric := humanK(sess.headTokens()) + "/" + humanK(sess.windowSize())
	if numeric := sess.Prompt(); !strings.Contains(numeric, wantNumeric) {
		t.Errorf("Prompt() with repl.gauge=numeric = %q, want to contain %q", numeric, wantNumeric)
	}

	// The prompt is redrawn on every keystroke with only \r\033[K, which cannot
	// erase an embedded newline — a \n here walks the line down one row per byte
	// typed. The inter-turn blank line is the REPL loop's job, not Prompt()'s.
	if strings.ContainsAny(got, "\n\r") {
		t.Errorf("Prompt() must be a single line, got %q", got)
	}
}

func TestStripToolMarkup(t *testing.T) {
	content := "Let me check.\n<tool_call>\n<function=bash>\n<parameter=command>\nls\n</parameter>\n</function>\n</tool_call>"
	got := stripToolMarkup(content)
	if got != "Let me check." {
		t.Errorf("stripToolMarkup = %q, want %q", got, "Let me check.")
	}
}

// TestMessageRender covers the de-glyphed, uniformly-gray gutter (2026-07-19):
// there is no per-role icon and no per-role timestamp color anymore, so
// render() is checked for the gray timestamp + content only, the same for
// every role.
func TestMessageRender(t *testing.T) {
	ts := time.Date(2026, 6, 8, 14, 23, 1, 0, time.UTC)
	for _, role := range []string{"assistant", RoleSystem, RoleTool, RoleUser} {
		m := Message{Role: role, Content: "hello"}
		got := m.render(ts)
		for _, want := range []string{"14:23:01", "hello", gray} {
			if !strings.Contains(got, want) {
				t.Errorf("render(role=%s) = %q, missing %q", role, got, want)
			}
		}
	}
}

func TestContextRatio(t *testing.T) {
	cs := CortexSession{Window: 1000, LastPromptTokens: 800}
	if got := cs.contextRatio(); got != 0.8 {
		t.Errorf("contextRatio = %v, want 0.8", got)
	}
	// The gauge color and the compact trigger share the same threshold.
	if ctxColor(800, 1000) != red {
		t.Error("gauge should be red exactly at compactThreshold")
	}
	if ctxColor(799, 1000) != yellow {
		t.Error("gauge should be yellow just under compactThreshold")
	}
}

func TestWireMessagesComposesEphemerally(t *testing.T) {
	req := CortexArgs{}.Request() // system message only
	sys := req.Messages[0].Content
	req.Messages = append(req.Messages, Message{Role: RoleUser, Content: "add a field"})
	userOrig := req.Messages[1].Content

	t.Run("no ephemeral → everything unchanged", func(t *testing.T) {
		wire := req.wireMessages()
		if wire[0].Content != sys || wire[1].Content != userOrig {
			t.Error("without ephemeral, no message should change")
		}
	})

	t.Run("ephemeral occupies a separate slot after the stable prefix", func(t *testing.T) {
		req.EphemeralSystem = "# memory\n- [decision] use pgx"
		wire := req.wireMessages()

		if wire[0].Content != sys {
			t.Errorf("system message changed: got %q, want %q", wire[0].Content, sys)
		}
		if len(wire) != len(req.Messages)+1 {
			t.Fatalf("wire length = %d, want %d", len(wire), len(req.Messages)+1)
		}
		if wire[1].Role != RoleUser || wire[1].Content != req.EphemeralSystem {
			t.Errorf("wire[1] = %+v, want ephemeral user slot", wire[1])
		}
		if wire[2].Content != userOrig {
			t.Errorf("original user message moved incorrectly: %q", wire[2].Content)
		}
		if req.Messages[0].Content != sys || req.Messages[1].Content != userOrig {
			t.Error("stored messages must not be mutated by composition")
		}
	})

	t.Run("ephemeral slot remains fixed while the tool loop appends", func(t *testing.T) {
		req.Messages = append(req.Messages, Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "1"}}})
		req.Messages = append(req.Messages, Message{Role: RoleTool, ToolCallID: "1", Content: "tool output"})
		req.EphemeralSystem = "ctx"
		wire := req.wireMessages()
		if wire[0].Content != sys || wire[1].Content != "ctx" || wire[2].Content != userOrig {
			t.Fatalf("wire prefix = %+v, want stable system, ephemeral slot, original user", wire[:3])
		}
		if wire[len(wire)-1].Content != "tool output" {
			t.Error("tool-loop append missing from wire tail")
		}
	})
}

func TestWireMessagesTwoZones(t *testing.T) {
	req := &AgentRequest{Messages: []Message{{Role: RoleSystem, Content: "sys"}, {Role: RoleUser, Content: "u1"}, {Role: "assistant", Content: "a1"}, {Role: RoleUser, Content: "u2"}, {Role: "assistant", Content: "a2"}}}

	t.Run("demoted turns are replaced by the outline", func(t *testing.T) {
		req.OutlineBlock = "OUTLINE"
		req.EphemeralSystem = "INDEX"
		req.TailFrom = 3

		wire := req.wireMessages()
		// Wire: system, outline, index slot, u2, a2.
		if len(wire) != 5 {
			t.Errorf("wire length = %d, want 5", len(wire))
		}
		if wire[0].Content != "sys" {
			t.Errorf("wire[0] = %q, want stable system", wire[0].Content)
		}
		if wire[1].Role != RoleUser || wire[1].Content != "OUTLINE" {
			t.Errorf("wire[1] = {%q,%q}, want {%q,%q}", wire[1].Role, wire[1].Content, RoleUser, "OUTLINE")
		}
		if wire[2].Content != "INDEX" || wire[3].Content != "u2" || wire[4].Content != "a2" {
			t.Errorf("wire suffix = %+v, want index, u2, a2", wire[2:])
		}
		// Stored Messages must not be mutated
		if len(req.Messages) != 5 || req.Messages[1].Content != "u1" {
			t.Errorf("stored Messages[1] = %q, want %q (not mutated)", req.Messages[1].Content, "u1")
		}
	})

	t.Run("tail-only demotion without outline still drops demoted messages", func(t *testing.T) {
		req.OutlineBlock = ""
		req.EphemeralSystem = ""
		req.TailFrom = 3

		wire := req.wireMessages()
		if len(wire) != 3 {
			t.Errorf("wire length = %d, want 3", len(wire))
		}
		if wire[0].Content != "sys" {
			t.Errorf("wire[0] = %q, want %q", wire[0].Content, "sys")
		}
		if wire[1].Content != "u2" {
			t.Errorf("wire[1] = %q, want %q", wire[1].Content, "u2")
		}
		if wire[2].Content != "a2" {
			t.Errorf("wire[2] = %q, want %q", wire[2].Content, "a2")
		}
	})

	t.Run("zero values are a no-op", func(t *testing.T) {
		req.OutlineBlock = ""
		req.EphemeralSystem = ""
		req.TailFrom = 0

		wire := req.wireMessages()
		if len(wire) != len(req.Messages) {
			t.Errorf("wire length = %d, want %d", len(wire), len(req.Messages))
		}
		if wire[1].Content != "u1" {
			t.Errorf("wire[1] = %q, want %q", wire[1].Content, "u1")
		}
	})
}

// applyPromptCache marks Anthropic cache breakpoints on the system message and
// the end of prior history, and only for anthropic/* models. The default
// (no-cache) message must marshal byte-identically so transcripts are untouched.
func TestPromptCache(t *testing.T) {
	mk := func() []Message {
		return []Message{
			{Role: RoleSystem, Content: "SYS"},
			{Role: RoleUser, Content: "first task"},
			{Role: "assistant", Content: "doing it"},
			{Role: RoleUser, Content: "follow up"}, // current turn (last user)
		}
	}
	cached := func(m Message) bool {
		b, _ := json.Marshal(&m) // pointer, as addressable wire-slice elements are
		return strings.Contains(string(b), "cache_control")
	}

	t.Run("default message marshals byte-identically (no cache_control)", func(t *testing.T) {
		b, _ := json.Marshal(Message{Role: RoleUser, Content: "hi"})
		if string(b) != `{"role":"user","content":"hi"}` {
			t.Errorf("default marshal changed: %s", b)
		}
	})

	t.Run("non-anthropic model is a no-op", func(t *testing.T) {
		msgs := mk()
		applyPromptCache(msgs, "z-ai/glm-4.6")
		for i, m := range msgs {
			if cached(m) {
				t.Errorf("message %d should not be cached for a non-anthropic model", i)
			}
		}
	})

	t.Run("anthropic marks system + end-of-prior-history, not the current turn", func(t *testing.T) {
		msgs := mk()
		applyPromptCache(msgs, "anthropic/claude-haiku-4.5")
		want := map[int]bool{0: true, 1: false, 2: true, 3: false} // sys + pre-current-user
		for i, m := range msgs {
			if cached(m) != want[i] {
				t.Errorf("message %d (role %s) cached=%v, want %v", i, m.Role, cached(m), want[i])
			}
		}
		// The cached system message must carry the structured content form.
		b, _ := json.Marshal(&msgs[0])
		if !strings.Contains(string(b), `"type":"ephemeral"`) || !strings.Contains(string(b), `"text":"SYS"`) {
			t.Errorf("cached message not in content-parts form: %s", b)
		}
		// The real wire path marshals the message SLICE inside the payload —
		// addressable elements must invoke the pointer marshaler there too.
		wire, _ := json.Marshal(struct {
			Messages []Message `json:"messages"`
		}{msgs})
		if got := strings.Count(string(wire), "cache_control"); got != 2 {
			t.Errorf("wire payload should carry 2 cache breakpoints, got %d: %s", got, wire)
		}
	})

	t.Run("first turn (no prior history) marks only the system message", func(t *testing.T) {
		msgs := []Message{{Role: RoleSystem, Content: "SYS"}, {Role: RoleUser, Content: "hi"}}
		applyPromptCache(msgs, "anthropic/claude-opus-4.8")
		if !cached(msgs[0]) || cached(msgs[1]) {
			t.Error("first turn should cache only the system message")
		}
	})
}
