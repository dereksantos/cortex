package repltui

import "github.com/charmbracelet/lipgloss"

// Styles for the per-message-kind line rendering. Colors are chosen
// to read on both light and dark terminals; lipgloss adapts when
// ColorProfile detects a non-truecolor environment.
//
// Cortex-function colors (eventStyleByPrefix below) pin the DAG
// trace lines to a recognizable hue per function family so a long
// session's transcript is scannable at a glance:
//
//	sense / remember : cyan         — pulling input from somewhere
//	attend           : yellow       — focusing / compressing
//	decide           : magenta      — choosing the next move
//	act              : green        — side-effecting tool calls
//	value / model    : blue / white — scoring / classification
//	maintain         : grey         — background bookkeeping
//
// The same color palette is used for the small unicode glyph that
// prefixes each line so colorblind users still see the structural
// distinction (⚙ vs · vs ▪).
var (
	infoStyle    = lipgloss.NewStyle()
	warnStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("214")) // orange
	errorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("196")) // red
	bannerStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	userEchoSty  = lipgloss.NewStyle().Foreground(lipgloss.Color("245")) // grey
	dividerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240")) // dim grey
	statusStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("245")) // grey
	ambientStyle = lipgloss.NewStyle().Italic(true).Foreground(lipgloss.Color("245"))

	// Per-cortex-function event colors. Routed via styleForFunction
	// from the qualified_name's prefix; renderDagTraceLine + the
	// tool_call branch in renderEventLine both consult it.
	senseEvtSty    = lipgloss.NewStyle().Foreground(lipgloss.Color("51"))  // cyan
	rememberEvtSty = lipgloss.NewStyle().Foreground(lipgloss.Color("51"))  // cyan
	attendEvtSty   = lipgloss.NewStyle().Foreground(lipgloss.Color("220")) // yellow
	decideEvtSty   = lipgloss.NewStyle().Foreground(lipgloss.Color("207")) // magenta
	actEvtSty      = lipgloss.NewStyle().Foreground(lipgloss.Color("46"))  // green
	valueEvtSty    = lipgloss.NewStyle().Foreground(lipgloss.Color("75"))  // blue
	modelEvtSty    = lipgloss.NewStyle().Foreground(lipgloss.Color("255")) // white
	maintainEvtSty = lipgloss.NewStyle().Foreground(lipgloss.Color("245")) // grey
	finalEvtSty    = lipgloss.NewStyle().Foreground(lipgloss.Color("255"))
)

// styleForFunction returns the per-cortex-function color for a
// qualified op name like "decide.next" or "act.read_file". The
// prefix before the first dot picks the style; unknown families
// fall back to the plain infoStyle so the line still renders.
func styleForFunction(qualifiedName string) lipgloss.Style {
	prefix := qualifiedName
	for i, r := range qualifiedName {
		if r == '.' {
			prefix = qualifiedName[:i]
			break
		}
	}
	switch prefix {
	case "sense":
		return senseEvtSty
	case "remember":
		return rememberEvtSty
	case "attend":
		return attendEvtSty
	case "decide":
		return decideEvtSty
	case "act":
		return actEvtSty
	case "value":
		return valueEvtSty
	case "model":
		return modelEvtSty
	case "maintain":
		return maintainEvtSty
	}
	return infoStyle
}

// styleForEventKind returns the appropriate style for one event
// payload kind. The kind shape is "coding.<sub>" for harness
// events; we also look at the optional "name" subfield (when the
// kind is coding.tool_call) to color by cortex function.
//
// Unknown kinds fall through to a plain (no-color) style — better
// than guessing.
func styleForEventKind(kind, toolName string) lipgloss.Style {
	// Tool calls carry a name like "read_file" / "write_file" /
	// "run_shell" / "cortex_search". Color by the act/* / remember/*
	// flavor.
	if kind == "coding.tool_call" || kind == "coding.tool_result" {
		switch toolName {
		case "read_file", "list_dir":
			return senseEvtSty
		case "cortex_search":
			return rememberEvtSty
		case "write_file", "run_shell":
			return actEvtSty
		default:
			return actEvtSty
		}
	}
	switch kind {
	case "coding.final":
		return finalEvtSty
	case "coding.budget_exceeded", "coding.turn_limit", "coding.no_progress":
		return warnStyle
	case "coding.error":
		return errorStyle
	}
	return infoStyle
}
