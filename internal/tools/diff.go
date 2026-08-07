package tools

// diff.go renders WHAT a file edit changed, under the tool-action line that
// announced it. Before this, `edit_file(path)` was the whole story: the model
// said it edited something and the terminal showed a path — you had to open
// the file (or trust the model's prose) to learn what moved.
//
// The rendering is plain text by the same 2026-07-19 decision the rest of the
// REPL follows: no glyphs, no box-drawing, no connectors. A unified diff
// already has an ASCII vocabulary — `+`/`-`/` ` markers and `@@` hunk
// headers — so the only additions are ANSI color (green/red/gray, dropped
// under NO_COLOR by Color) and indentation that puts the body under the
// action line's text column.
//
// Everything here is bounded: context is collapsed to a few lines around each
// hunk, the total body height is capped and the remainder elided, a huge
// before/after pair degrades to a one-line summary rather than a diff, and
// file content is sanitized (tabs expanded, control bytes neutered) so a file
// containing escape sequences can't repaint the terminal.

import (
	"fmt"
	"strconv"
	"strings"
)

// printFileDiff prints the diff of a file change under the tool-action line
// that announced it. Suppressed exactly where the action line itself is:
// deps.Quiet() (headless `cortex turn`, serve, discord) and the
// CORTEX_LOOP_RENDER=0 escape hatch, which falls back to today's flat line.
func printFileDiff(deps Quieter, before, after string) {
	if deps.Quiet() || richRenderDisabled {
		return
	}
	emitLines(renderDiff(before, after, diffOptions{Width: termWidth(), Indent: indentPrefix()}))
}

// emitLines writes already-rendered lines to the terminal — or, inside a
// subagent, buffers them onto the call's held-back announcement so they land
// under the line they belong to (nesting.go).
func emitLines(lines []string) {
	if captureExtra(lines) {
		return
	}
	for _, l := range lines {
		fmt.Println(l)
	}
}

const (
	// diffContextLines is how many unchanged lines are kept on each side of a
	// change. Three is the `diff -u` default and enough to place a hunk.
	diffContextLines = 3
	// diffMaxBodyLines caps the rendered body (hunk headers + rows). A large
	// rewrite must not flood scrollback: past this the rest is elided with a
	// "… N more lines" count. The header note and the elision line sit outside
	// the cap, so a diff block is at most this + 2 lines tall.
	diffMaxBodyLines = 24
	// diffMaxInputBytes is the before+after size past which no diff is
	// computed at all — the change is reported as a line-count delta instead.
	diffMaxInputBytes = 512 * 1024
	// diffMaxCells bounds the LCS table (rows × columns) after the common
	// prefix/suffix are trimmed. Past it the changed middle is rendered as a
	// wholesale replacement, which the height cap then elides anyway — the
	// point is to never let a rendering detail cost real time or memory.
	diffMaxCells = 250_000
	// diffTabWidth is how wide a tab renders. Tabs are expanded so the line
	// numbers stay in a column and width truncation counts what's on screen.
	diffTabWidth = 4
)

// gutterPad is the blank stand-in for TimestampPrefix's "HH:MM:SS  " — diff
// rows carry no timestamp of their own (one per row would be noise), so they
// indent past the gutter and sit under the action line's text.
const gutterPad = "          "

// diffBodyIndent nudges the body two columns further right than the action
// line, so the block reads as belonging to the call above it.
const diffBodyIndent = "  "

// diffOptions are the rendering knobs. The zero value is usable: Context and
// MaxLines fall back to the package defaults, Width 0 means "don't truncate"
// (the non-TTY/raw path), Indent "" means top level.
type diffOptions struct {
	Context  int
	MaxLines int
	Width    int
	Indent   string
}

func (o diffOptions) withDefaults() diffOptions {
	if o.Context <= 0 {
		o.Context = diffContextLines
	}
	if o.MaxLines <= 0 {
		o.MaxLines = diffMaxBodyLines
	}
	return o
}

// prefix is the left margin every rendered row shares.
func (o diffOptions) prefix() string { return gutterPad + o.Indent + diffBodyIndent }

// diffRow is one rendered row: an op (' ' context, '-' removed, '+' added),
// the 1-indexed line numbers it holds in the before/after files (0 where it
// has none), and the line's text.
type diffRow struct {
	op   byte
	old  int
	new  int
	text string
}

// renderDiff renders the change from before to after as complete terminal
// lines (no trailing newlines), color included. It is the whole rendering
// decision in one pure function so the shape can be tested without a
// terminal: callers hand it two strings and print what comes back.
func renderDiff(before, after string, opt diffOptions) []string {
	opt = opt.withDefaults()
	switch {
	case before == after && before == "":
		return []string{opt.note("wrote an empty file")}
	case before == after:
		return []string{opt.note("no change")}
	case isBinary(before) || isBinary(after):
		return []string{opt.note(fmt.Sprintf("binary content, %d bytes → %d bytes", len(before), len(after)))}
	case len(before)+len(after) > diffMaxInputBytes:
		return []string{opt.note(fmt.Sprintf("%s → %s (too large to diff)",
			countNoun(len(splitLines(before)), "line"), countNoun(len(splitLines(after)), "line")))}
	}

	a, b := splitLines(before), splitLines(after)
	var out []string
	switch {
	case before == "":
		out = append(out, opt.note("new file, "+countNoun(len(b), "line")))
	case after == "":
		out = append(out, opt.note("emptied, "+countNoun(len(a), "line")+" removed"))
	}

	rows := diffRows(a, b)
	out = append(out, opt.body(rows)...)
	return out
}

// body renders the hunks of rows within the height cap, eliding the rest.
func (o diffOptions) body(rows []diffRow) []string {
	hunks := hunkize(rows, o.Context)
	numW := numWidth(rows)

	// Flatten to renderable items first so the cap can be applied uniformly and
	// the elided remainder counted in CONTENT rows (what the reader cares about),
	// not in headers.
	type item struct {
		text   string
		header bool
	}
	var items []item
	for _, h := range hunks {
		items = append(items, item{text: o.hunkHeader(h), header: true})
		for _, r := range h.rows {
			items = append(items, item{text: o.row(r, numW)})
		}
	}
	if len(items) <= o.MaxLines {
		out := make([]string, 0, len(items))
		for _, it := range items {
			out = append(out, it.text)
		}
		return out
	}

	keep := o.MaxLines
	// Never end on a hunk header with nothing under it.
	for keep > 0 && items[keep-1].header {
		keep--
	}
	elided := 0
	for _, it := range items[keep:] {
		if !it.header {
			elided++
		}
	}
	out := make([]string, 0, keep+1)
	for _, it := range items[:keep] {
		out = append(out, it.text)
	}
	// The totals ride on the elision line rather than a header of their own:
	// once the body is cut, the visible rows can be all-removals (a wholesale
	// rewrite renders every '-' before any '+'), and the reader has no way left
	// to judge the size of the change. Unelided diffs stay one line tighter.
	adds, dels := 0, 0
	for _, r := range rows {
		switch r.op {
		case '+':
			adds++
		case '-':
			dels++
		}
	}
	return append(out, o.note(fmt.Sprintf("… %d more lines (+%d -%d total)", elided, adds, dels)))
}

// note renders a gray one-line remark (the new-file/no-change header, the
// elision count, a degradation notice).
func (o diffOptions) note(s string) string {
	return Color(truncatePlain(o.prefix()+s, o.Width), Gray)
}

// hunkHeader renders the standard unified-diff position header.
func (o diffOptions) hunkHeader(h hunk) string {
	s := fmt.Sprintf("@@ -%d,%d +%d,%d @@", h.oldStart, h.oldCount, h.newStart, h.newCount)
	return Color(truncatePlain(o.prefix()+s, o.Width), Gray)
}

// row renders one diff row: line number, marker, text. Removed rows carry the
// before-file number, added and context rows the after-file number — so the
// column always reads as "where this line is now", except for what's gone.
func (o diffOptions) row(r diffRow, numW int) string {
	n := r.new
	if r.op == '-' {
		n = r.old
	}
	num := strconv.Itoa(n)
	plain := fmt.Sprintf("%s%*s %c %s", o.prefix(), numW, num, r.op, sanitize(r.text))
	plain = truncatePlain(plain, o.Width)
	switch r.op {
	case '+':
		return Color(plain, Green)
	case '-':
		return Color(plain, Red)
	default:
		return Color(plain, Gray)
	}
}

// numWidth is the width of the line-number column — the widest number any row
// will print.
func numWidth(rows []diffRow) int {
	max := 0
	for _, r := range rows {
		n := r.new
		if r.op == '-' {
			n = r.old
		}
		if n > max {
			max = n
		}
	}
	return len(strconv.Itoa(max))
}

// hunk is a run of rows kept together: the changes plus their context.
type hunk struct {
	rows                                   []diffRow
	oldStart, oldCount, newStart, newCount int
}

// hunkize collapses the full row list to the changed regions plus ctx lines of
// context on each side, grouped into contiguous hunks with their positions.
func hunkize(rows []diffRow, ctx int) []hunk {
	// oldBefore[i]/newBefore[i]: how many before/after lines precede row i.
	oldBefore := make([]int, len(rows))
	newBefore := make([]int, len(rows))
	o, n := 0, 0
	for i, r := range rows {
		oldBefore[i], newBefore[i] = o, n
		if r.op != '+' {
			o++
		}
		if r.op != '-' {
			n++
		}
	}

	keep := make([]bool, len(rows))
	for i, r := range rows {
		if r.op == ' ' {
			continue
		}
		lo, hi := i-ctx, i+ctx
		if lo < 0 {
			lo = 0
		}
		if hi >= len(rows) {
			hi = len(rows) - 1
		}
		for j := lo; j <= hi; j++ {
			keep[j] = true
		}
	}

	var hunks []hunk
	for i := 0; i < len(rows); {
		if !keep[i] {
			i++
			continue
		}
		start := i
		for i < len(rows) && keep[i] {
			i++
		}
		h := hunk{rows: rows[start:i]}
		for _, r := range h.rows {
			if r.op != '+' {
				h.oldCount++
			}
			if r.op != '-' {
				h.newCount++
			}
		}
		// A zero-count side has no line of its own to point at; unified diff
		// convention is to name the line it follows.
		h.oldStart = oldBefore[start]
		if h.oldCount > 0 {
			h.oldStart++
		}
		h.newStart = newBefore[start]
		if h.newCount > 0 {
			h.newStart++
		}
		hunks = append(hunks, h)
	}
	return hunks
}

// diffRows computes the line-level diff. The common prefix and suffix are
// trimmed first (the usual case — one edit in a long file — reduces to a
// handful of lines), and only the changed middle goes through LCS.
func diffRows(a, b []string) []diffRow {
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	s := 0
	for s < len(a)-p && s < len(b)-p && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}

	rows := make([]diffRow, 0, len(a)+len(b))
	for i := 0; i < p; i++ {
		rows = append(rows, diffRow{op: ' ', old: i + 1, new: i + 1, text: a[i]})
	}
	rows = append(rows, midRows(a[p:len(a)-s], b[p:len(b)-s], p)...)
	for i := 0; i < s; i++ {
		oi, ni := len(a)-s+i, len(b)-s+i
		rows = append(rows, diffRow{op: ' ', old: oi + 1, new: ni + 1, text: a[oi]})
	}
	return rows
}

// midRows diffs the changed middle. off is how many lines were trimmed off the
// front, so the line numbers stay absolute. A pure insertion/deletion, or a
// middle too big to be worth an LCS table, renders as a wholesale replacement.
func midRows(ma, mb []string, off int) []diffRow {
	if len(ma) == 0 || len(mb) == 0 || len(ma)*len(mb) > diffMaxCells {
		rows := make([]diffRow, 0, len(ma)+len(mb))
		for i, l := range ma {
			rows = append(rows, diffRow{op: '-', old: off + i + 1, text: l})
		}
		for i, l := range mb {
			rows = append(rows, diffRow{op: '+', new: off + i + 1, text: l})
		}
		return rows
	}

	// lcs[i][j] = length of the longest common subsequence of ma[i:] and mb[j:].
	lcs := make([][]int, len(ma)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(mb)+1)
	}
	for i := len(ma) - 1; i >= 0; i-- {
		for j := len(mb) - 1; j >= 0; j-- {
			if ma[i] == mb[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}

	var rows []diffRow
	i, j := 0, 0
	for i < len(ma) && j < len(mb) {
		switch {
		case ma[i] == mb[j]:
			rows = append(rows, diffRow{op: ' ', old: off + i + 1, new: off + j + 1, text: ma[i]})
			i, j = i+1, j+1
		case lcs[i+1][j] >= lcs[i][j+1]:
			rows = append(rows, diffRow{op: '-', old: off + i + 1, text: ma[i]})
			i++
		default:
			rows = append(rows, diffRow{op: '+', new: off + j + 1, text: mb[j]})
			j++
		}
	}
	for ; i < len(ma); i++ {
		rows = append(rows, diffRow{op: '-', old: off + i + 1, text: ma[i]})
	}
	for ; j < len(mb); j++ {
		rows = append(rows, diffRow{op: '+', new: off + j + 1, text: mb[j]})
	}
	return rows
}

// splitLines splits content into lines, dropping the empty element a trailing
// newline leaves behind so line counts and numbers match the file's.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}

// isBinary reports whether content looks non-textual (a NUL byte in the head
// is the same heuristic grep uses).
func isBinary(s string) bool {
	head := s
	if len(head) > 8000 {
		head = head[:8000]
	}
	return strings.IndexByte(head, 0) >= 0
}

// sanitize makes one line of file content safe and stable to print: tabs
// expand to spaces (so numbers stay in a column and width truncation counts
// screen cells), and control bytes — including the ESC that would let a file's
// contents repaint the terminal — become '?'.
func sanitize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\t':
			b.WriteString(strings.Repeat(" ", diffTabWidth))
		case r < 0x20 || r == 0x7f:
			b.WriteByte('?')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// truncatePlain clips an uncolored line to width, marking the cut. width <= 0
// means no terminal is attached (piped output, CI) — leave the line whole,
// which is the raw behavior every other REPL path degrades to.
func truncatePlain(s string, width int) string {
	if width <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	return string(r[:width-1]) + "…"
}
