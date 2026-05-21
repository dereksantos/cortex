package repltui

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
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
	statusRows   = 1
	dividerRows  = 1
	inputRows    = 1
	bottomChrome = statusRows + dividerRows + inputRows
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

	return Model{
		sink:       sink,
		viewport:   vp,
		input:      ti,
		statusLine: initialStatus,
		promptText: "~ ",
		width:      80,
		height:     24,
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
		m.viewport.Height = max(1, msg.Height-bottomChrome)
		m.input.Width = max(1, msg.Width-len(m.input.Prompt)-1)
		m.viewport.SetContent(strings.Join(m.transcript, "\n"))
		m.viewport.GotoBottom()
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlD:
			// Ctrl-D quits — deliver EOF so the REPL's ReadLine
			// returns and its loop exits.
			if m.sink != nil {
				m.sink.deliverInput("", io.EOF)
			}
			m.quitting = true
			return m, tea.Quit

		case tea.KeyCtrlC:
			// v1: Ctrl-C just clears the current input. v2 will hook
			// into a context cancellation for in-flight turns.
			m.input.SetValue("")
			return m, nil

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

		case tea.KeyPgUp, tea.KeyPgDown, tea.KeyUp, tea.KeyDown,
			tea.KeyHome, tea.KeyEnd:
			// Scroll keys go to the viewport.
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
		// Anything else (printable, backspace, …) goes to the input.
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
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
		line := renderEventLine(msg.kind, msg.payload, m.sink != nil && m.sink.verbose)
		if line != "" {
			m.append(line)
		}
		return m, nil

	case quitMsg:
		m.quitting = true
		return m, tea.Quit
	}
	return m, nil
}

// View implements tea.Model.
func (m Model) View() string {
	if m.quitting {
		return "session saved.\n"
	}
	divider := dividerStyle.Render(strings.Repeat("─", max(1, m.width)))
	status := statusStyle.Render(m.statusLine)
	return strings.Join([]string{
		m.viewport.View(),
		divider,
		status,
		m.input.View(),
	}, "\n")
}

// append adds a rendered line to the transcript and re-anchors the
// viewport at the bottom (unless the user has scrolled up). Lines
// arrive pre-styled.
func (m *Model) append(line string) {
	m.transcript = append(m.transcript, line)
	m.viewport.SetContent(strings.Join(m.transcript, "\n"))
	m.viewport.GotoBottom()
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
