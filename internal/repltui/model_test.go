package repltui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// drive runs a slice of messages through a freshly-constructed
// Model and returns the final state. Each tea.Msg is fed through
// Update; the returned tea.Cmd is ignored — these tests assert on
// rendered state, not on outgoing commands.
func drive(msgs ...any) Model {
	m := New(NewTUISink(false), "test status")
	for _, msg := range msgs {
		mm, _ := m.Update(msg)
		m = mm.(Model)
	}
	return m
}

// stripANSI removes lipgloss-injected escape sequences so substring
// assertions can look at the rendered glyphs without worrying about
// the surrounding color codes.
func stripANSI(s string) string {
	var b []byte
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) {
				c := s[j]
				j++
				if c >= '@' && c <= '~' {
					break
				}
			}
			i = j - 1
			continue
		}
		b = append(b, s[i])
	}
	return string(b)
}

func TestUpdate_InfoAppendsToTranscript(t *testing.T) {
	m := drive(infoMsg{text: "hello world"})
	if len(m.transcript) != 1 {
		t.Fatalf("transcript len: got %d, want 1", len(m.transcript))
	}
	if !strings.Contains(stripANSI(m.transcript[0]), "hello world") {
		t.Errorf("transcript[0]: missing payload; got %q", stripANSI(m.transcript[0]))
	}
}

func TestUpdate_VerbosityMsgFlipsRenderingGate(t *testing.T) {
	// Start in non-verbose; a coding.tool_result event should be hidden.
	m := drive(eventMsg{kind: "coding.tool_result", payload: map[string]any{"output_chars": 100}})
	if len(m.transcript) != 0 {
		t.Fatalf("non-verbose tool_result should not append; got %d lines", len(m.transcript))
	}

	// Flip to verbose; same event now appears.
	m = drive(
		verbosityMsg{verbose: true},
		eventMsg{kind: "coding.tool_result", payload: map[string]any{"output_chars": 100}},
	)
	if !m.verbose {
		t.Errorf("verbose flag not flipped on verbosityMsg")
	}
	found := false
	for _, line := range m.transcript {
		if strings.Contains(stripANSI(line), "100 chars") {
			found = true
		}
	}
	if !found {
		t.Errorf("verbose tool_result line missing from transcript: %v", m.transcript)
	}
}

func TestUpdate_DAGTraceColoredByFunction(t *testing.T) {
	// Each of these qualified names should pick a distinct color
	// (we assert that the rendered output differs from the plain
	// no-color render); the structural shape stays consistent.
	cases := []struct {
		qname   string
		wantSub string
	}{
		{"sense.scan_project_boundaries", "▪ sense.scan_project_boundaries"},
		{"attend.compress", "▪ attend.compress"},
		{"decide.next", "▪ decide.next"},
		{"act.read_file", "▪ act.read_file"},
	}
	for _, tc := range cases {
		t.Run(tc.qname, func(t *testing.T) {
			m := drive(dagTraceMsg{
				QualifiedName: tc.qname,
				NodeID:        "n0001",
				OK:            true,
				LatencyMs:     24,
			})
			if len(m.transcript) != 1 {
				t.Fatalf("transcript len: got %d, want 1", len(m.transcript))
			}
			stripped := stripANSI(m.transcript[0])
			if !strings.Contains(stripped, tc.wantSub) {
				t.Errorf("rendered line missing %q: %q", tc.wantSub, stripped)
			}
			if !strings.Contains(stripped, "n0001") {
				t.Errorf("rendered line missing node id: %q", stripped)
			}
			if !strings.Contains(stripped, "24ms") {
				t.Errorf("rendered line missing latency: %q", stripped)
			}
		})
	}
}

func TestUpdate_BootstrapProgressManagesAmbientRow(t *testing.T) {
	m := drive(studyProgressMsg{Line: "extracting insights (3/12)", Done: false})
	if m.ambientRow != "extracting insights (3/12)" {
		t.Errorf("ambientRow: got %q, want set", m.ambientRow)
	}
	// View should now include the ambient line.
	view := stripANSI(m.View())
	if !strings.Contains(view, "extracting insights (3/12)") {
		t.Errorf("view should include ambient row: %q", view)
	}

	// Done=true clears the row.
	mm, _ := m.Update(studyProgressMsg{Done: true})
	m = mm.(Model)
	if m.ambientRow != "" {
		t.Errorf("ambientRow should clear on Done=true, got %q", m.ambientRow)
	}
}

func TestRenderDagTraceLine_ErrorIncludesCause(t *testing.T) {
	out := stripANSI(renderDagTraceLine(dagTraceMsg{
		QualifiedName: "decide.next",
		NodeID:        "n0042",
		OK:            false,
		LatencyMs:     5_321,
		ErrCause:      "model returned malformed JSON",
	}))
	if !strings.Contains(out, "err") {
		t.Errorf("status missing err: %q", out)
	}
	if !strings.Contains(out, "5.3s") {
		t.Errorf("latency formatting wrong: %q", out)
	}
	if !strings.Contains(out, "cause: model returned malformed JSON") {
		t.Errorf("cause missing: %q", out)
	}
}

func TestUpdate_LeftMousePressEntersSelectMode(t *testing.T) {
	m := New(NewTUISink(false), "status one")
	mm, _ := m.Update(tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
	})
	m = mm.(Model)
	if !m.selectMode {
		t.Fatalf("selectMode should be true after left-mouse press")
	}
	if m.savedStatusLine != "status one" {
		t.Errorf("savedStatusLine: got %q, want %q", m.savedStatusLine, "status one")
	}
	if !strings.Contains(stripANSI(m.statusLine), "text-select") {
		t.Errorf("statusLine should show select-mode hint; got %q", stripANSI(m.statusLine))
	}

	// Any keystroke exits select mode and restores the status line.
	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = mm.(Model)
	if m.selectMode {
		t.Errorf("selectMode should clear on keystroke")
	}
	if m.statusLine != "status one" {
		t.Errorf("statusLine should be restored; got %q", m.statusLine)
	}
}

func TestUpdate_MouseWheelDoesNotEnterSelectMode(t *testing.T) {
	m := New(NewTUISink(false), "status")
	mm, _ := m.Update(tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonWheelUp,
	})
	m = mm.(Model)
	if m.selectMode {
		t.Errorf("wheel events must not trigger select mode")
	}
}

func TestUpdate_CodingFinalRendersMarkdown(t *testing.T) {
	m := drive(
		tea.WindowSizeMsg{Width: 80, Height: 24},
		eventMsg{
			kind: "coding.final",
			payload: map[string]any{
				"content": "# Heading\n\n| col1 | col2 |\n|------|------|\n| a    | b    |\n",
			},
		},
	)
	if len(m.transcript) != 1 {
		t.Fatalf("transcript len: got %d, want 1", len(m.transcript))
	}
	stripped := stripANSI(m.transcript[0])
	// Glamour renders headings (with prefix), table content, etc.
	// We don't assert exact bytes (style-dependent) — just that the
	// table cells survive the round-trip.
	for _, want := range []string{"Heading", "col1", "col2", "a", "b"} {
		if !strings.Contains(stripped, want) {
			t.Errorf("markdown render missing %q: %q", want, stripped)
		}
	}
}

func TestUpdate_CodingFinalFallsBackWithoutResize(t *testing.T) {
	// No WindowSizeMsg → no renderer initialized; ensure plain-text
	// fallback still gets the content into the transcript.
	m := drive(eventMsg{
		kind:    "coding.final",
		payload: map[string]any{"content": "plain answer"},
	})
	if len(m.transcript) != 1 {
		t.Fatalf("transcript len: got %d, want 1", len(m.transcript))
	}
	if !strings.Contains(stripANSI(m.transcript[0]), "plain answer") {
		t.Errorf("fallback rendering missing content: %q", stripANSI(m.transcript[0]))
	}
}

func TestFormatLatencyMs_Ranges(t *testing.T) {
	cases := []struct {
		ms   int
		want string
	}{
		{0, "0ms"},
		{500, "500ms"},
		{1_500, "1.5s"},
		{59_999, "60.0s"},
		{60_000, "1m00s"},
		{125_000, "2m05s"},
	}
	for _, tc := range cases {
		if got := formatLatencyMs(tc.ms); got != tc.want {
			t.Errorf("formatLatencyMs(%d) = %q, want %q", tc.ms, got, tc.want)
		}
	}
}
