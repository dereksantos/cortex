package repltui

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Layout, top→bottom:
//
//	┌──────────────────────────────────────┐
//	│  scrollable transcript (viewport)    │  height: window-3
//	├──────────────────────────────────────┤  divider (1 row)
//	│  status: model · turn · cost         │  status (1 row)
//	│  ~ user input here                   │  input prompt (1 row)
//	└──────────────────────────────────────┘
//
// The viewport's content is the concatenation of all transcript
// lines. Autoscroll-to-bottom is on by default; PgUp/PgDn pause
// auto-scroll until the user returns to the bottom.

const (
	// statusRows + dividerRows + inputRows = bottom-chrome reserved
	// rows. Subtract from window height to get viewport height.
	// ambientRow adds one more row when the model has a non-empty
	// ambientRow value (bootstrap in progress, …); see View.
	statusRows       = 1
	dividerRows      = 1
	inputRows        = 1
	bottomChromeBase = statusRows + dividerRows + inputRows
)

// Model is the Bubble Tea Model for the REPL TUI.
type Model struct {
	// sink is how Update delivers submitted input lines back to the
	// REPL goroutine. Stored as a pointer to a concrete type so we
	// can call the unexported deliverInput.
	sink *TUISink

	// viewport renders the scrollable transcript area. Width/height
	// updated on tea.WindowSizeMsg; content swapped on every
	// transcript append via SetContent.
	viewport viewport.Model

	// input is the bottom prompt text field. Width matched to
	// terminal width on resize.
	input textinput.Model

	// transcript is the running list of rendered lines (already
	// styled by lipgloss). Joined with "\n" for viewport.SetContent.
	transcript []string

	// statusLine is the model · turn · cost · whatever caption that
	// sits between the divider and the input. Updated on Banner /
	// status-relevant events.
	statusLine string

	// promptText is the current input prefix ("~ " by default, or
	// whatever ReadLine asked for). Set by promptRequestedMsg.
	promptText string

	// width / height mirror the most recent tea.WindowSizeMsg so
	// View() can place things even if the viewport hasn't computed
	// its size yet.
	width, height int

	// quitting flips true after the user submits a quit signal; the
	// View renders a final "session saving…" line before tea.Quit
	// drops the program.
	quitting bool

	// verbose mirrors the sink's verbose flag. Bumped by
	// verbosityMsg (the /verbose slash command). Used by
	// renderEventLine to decide which kinds to render.
	verbose bool

	// ambientRow is the optional one-line tag that sits between
	// the divider and the main status line. Used today by
	// bootstrap progress; empty hides the row.
	ambientRow string
}

// New constructs a Model with reasonable defaults. The width/height
// are set on the first tea.WindowSizeMsg; until then the layout
// renders into an 80×24 fallback (rarely seen in practice).
func New(sink *TUISink, initialStatus string) Model {
	ti := textinput.New()
	ti.Prompt = "~ "
	ti.Placeholder = "type a message — /help for slash commands"
	ti.CharLimit = 0 // unlimited
	ti.Focus()

	vp := viewport.New(80, 20)
	vp.SetContent("")

	verbose := false
	if sink != nil {
		verbose = sink.verbose
	}
	return Model{
		sink:       sink,
		viewport:   vp,
		input:      ti,
		statusLine: initialStatus,
		promptText: "~ ",
		width:      80,
		height:     24,
		verbose:    verbose,
	}
}

// Init implements tea.Model. We blink the cursor.
func (m Model) Init() tea.Cmd { return textinput.Blink }

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.viewport.Width = msg.Width
		m.viewport.Height = max(1, msg.Height-m.bottomChrome())
		m.input.Width = max(1, msg.Width-len(m.input.Prompt)-1)
		m.viewport.SetContent(strings.Join(m.transcript, "\n"))
		m.viewport.GotoBottom()
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyCtrlD:
			// Both Ctrl-C and Ctrl-D quit. Most terminal programs
			// treat Ctrl-C as "exit now"; honoring that muscle memory
			// matters more than the readline-style "Ctrl-C clears the
			// line, twice quits" pattern. In-flight turn cancellation
			// (the roadmap item) will land as a separate signal once
			// the harness exposes a ctx hook.
			if m.sink != nil {
				m.sink.deliverInput("", io.EOF)
			}
			m.quitting = true
			return m, tea.Quit

		case tea.KeyEnter:
			line := m.input.Value()
			m.input.SetValue("")
			// Echo the user's submission in the transcript so
			// scrollback shows what they typed (the bottom prompt
			// resets to empty on submit).
			m.transcript = append(m.transcript, userEchoSty.Render(m.promptText+line))
			m.viewport.SetContent(strings.Join(m.transcript, "\n"))
			m.viewport.GotoBottom()
			if m.sink != nil {
				m.sink.deliverInput(line, nil)
			}
			return m, nil

		case tea.KeyPgUp, tea.KeyPgDown, tea.KeyHome, tea.KeyEnd:
			// Page / Home / End route to the viewport. Up/Down keys
			// stay with the input so prompt-history navigation (when
			// it lands) doesn't fight scrolling; explicit Ctrl+Up /
			// Ctrl+Down can be added later if scroll-by-line is
			// wanted without leaving the input.
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
		// Anything else (printable, backspace, …) goes to the input.
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd

	case tea.MouseMsg:
		// Wheel events scroll the transcript. tea.WithMouseCellMotion
		// at program construction enables the underlying stream; here
		// we delegate to viewport.Update which knows how to interpret
		// MouseButtonWheelUp / Down.
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd

	case infoMsg:
		m.append(infoStyle.Render(msg.text))
		return m, nil

	case warnMsg:
		m.append(warnStyle.Render("warn: " + msg.text))
		return m, nil

	case errMsg:
		m.append(errorStyle.Render("error: " + msg.text))
		return m, nil

	case bannerMsg:
		m.append(bannerStyle.Render(msg.text))
		// Banner doubles as the status line caption — the layout's
		// status row mirrors the model identity.
		m.statusLine = msg.text
		return m, nil

	case promptRequestedMsg:
		m.promptText = msg.prompt
		m.input.Prompt = msg.prompt
		return m, nil

	case eventMsg:
		line := renderEventLine(msg.kind, msg.payload, m.verbose)
		if line != "" {
			m.append(line)
		}
		return m, nil

	case verbosityMsg:
		m.verbose = msg.verbose
		state := "off"
		if m.verbose {
			state = "on"
		}
		m.append(infoStyle.Render("  verbose → " + state))
		return m, nil

	case dagTraceMsg:
		line := renderDagTraceLine(msg)
		m.append(line)
		return m, nil

	case bootstrapProgressMsg:
		if msg.Done {
			m.ambientRow = ""
		} else {
			m.ambientRow = msg.Line
		}
		return m, nil

	case quitMsg:
		m.quitting = true
		return m, tea.Quit
	}
	return m, nil
}

// View implements tea.Model.
//
// The transcript pane is wrapped in a lipgloss block with a pinned
// height so a short transcript doesn't let the input float up the
// screen. Without this, viewport.View() returns only the consumed
// rows; the input renders right after them and the bottom of the
// terminal is empty space. With it, the input always lives on the
// bottom-most row regardless of content length.
//
// When ambientRow is non-empty (bootstrap in progress, etc.) a
// dedicated row sits between the divider and the status line so
// progress chatter doesn't pollute the transcript scroll.
func (m Model) View() string {
	if m.quitting {
		return "session saved.\n"
	}
	vpHeight := max(1, m.height-m.bottomChrome())
	vpBlock := lipgloss.NewStyle().
		Width(max(1, m.width)).
		Height(vpHeight).
		Render(m.viewport.View())
	divider := dividerStyle.Render(strings.Repeat("─", max(1, m.width)))
	status := statusStyle.Render(m.statusLine)
	parts := []string{vpBlock, divider}
	if m.ambientRow != "" {
		parts = append(parts, ambientStyle.Render(m.ambientRow))
	}
	parts = append(parts, status, m.input.View())
	return strings.Join(parts, "\n")
}

// bottomChrome returns the count of rows reserved below the
// viewport. Includes the ambient row when it's populated so the
// viewport shrinks by one to make room.
func (m Model) bottomChrome() int {
	n := bottomChromeBase
	if m.ambientRow != "" {
		n++
	}
	return n
}

// append adds a rendered line to the transcript and re-anchors the
// viewport at the bottom (unless the user has scrolled up). Lines
// arrive pre-styled.
func (m *Model) append(line string) {
	m.transcript = append(m.transcript, line)
	m.viewport.SetContent(strings.Join(m.transcript, "\n"))
	m.viewport.GotoBottom()
}

// renderDagTraceLine formats one dag.trace event for the
// transcript. Same shape as makeREPLDAGTracer's stdout output but
// colored by cortex function — the "what is the DAG doing" pane
// that makes Cortex's emergent behavior visible.
func renderDagTraceLine(t dagTraceMsg) string {
	status := "ok"
	if !t.OK {
		status = "err"
	}
	tail := ""
	if len(t.Spawned) > 0 {
		tail = " → spawned " + strings.Join(t.Spawned, ", ")
	}
	if !t.OK && t.ErrCause != "" {
		cause := t.ErrCause
		if len(cause) > 120 {
			cause = cause[:120] + "…"
		}
		tail = " · cause: " + cause + tail
	}
	line := fmt.Sprintf("  ▪ %-22s [%s] · %s · %s%s",
		t.QualifiedName, t.NodeID, status, formatLatencyMs(t.LatencyMs), tail)
	return styleForFunction(t.QualifiedName).Render(line)
}

// formatLatencyMs renders a millisecond integer in REPL-friendly
// units. Mirrors the formatter in repl.go's makeREPLDAGTracer so
// the two surfaces print the same shape.
func formatLatencyMs(ms int) string {
	switch {
	case ms < 1:
		return "0ms"
	case ms < 1000:
		return fmt.Sprintf("%dms", ms)
	case ms < 60_000:
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	default:
		m := ms / 60_000
		s := (ms % 60_000) / 1000
		return fmt.Sprintf("%dm%02ds", m, s)
	}
}

// renderEventLine formats one Event message for the transcript.
// Returns "" for events that should be hidden at the current
// verbosity. Mirrors the StdoutSink.renderEvent gating so verbose
// flips bring up the same set of lines, just styled.
func renderEventLine(kind string, payload any, verbose bool) string {
	m, _ := payload.(map[string]any)
	style := styleForEventKind(kind, fmt.Sprintf("%v", m["name"]))
	switch kind {
	case "coding.tool_call":
		return style.Render(fmt.Sprintf("  ⚙ %v", m["name"]))
	case "coding.tool_result":
		if !verbose {
			return ""
		}
		return style.Render(fmt.Sprintf("    (result: %v chars)", m["output_chars"]))
	case "coding.turn":
		if !verbose {
			return ""
		}
		return style.Render(fmt.Sprintf("  · agent turn %v · tokens=%v/%v · calls=%v",
			m["turn"], m["tokens_in"], m["tokens_out"], m["tool_calls"]))
	case "coding.session_start":
		if !verbose {
			return ""
		}
		return style.Render(fmt.Sprintf("  · session_start · max_turns=%v · num_tools=%v",
			m["max_turns"], m["num_tools"]))
	case "coding.final":
		return style.Render(fmt.Sprintf("\n%v\n", m["content"]))
	case "coding.no_progress":
		return style.Render(fmt.Sprintf("  · stopped (no progress in last %v turns)", m["window"]))
	case "coding.budget_exceeded":
		return style.Render(fmt.Sprintf("  · stopped (budget): %v tokens", m["cumulative_tokens"]))
	case "coding.turn_limit":
		return style.Render(fmt.Sprintf("  ⚠ agent turn limit hit (%v turns)", m["turns"]))
	case "coding.error":
		return style.Render(fmt.Sprintf("  ⚠ provider error: %v", m["error"]))
	}
	if verbose {
		return style.Render(fmt.Sprintf("  · %s: %v", kind, payload))
	}
	return ""
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
