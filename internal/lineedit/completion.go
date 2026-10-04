// completion.go — Tab completion for the REPL prompt: `/`-commands,
// `/model <name>`, and `@path` file mentions (issue #108).
//
// Everything here is pure and TTY-free: the engine works on the current line
// plus a candidate list and returns the filled line plus the candidate row to
// show. The driver (Terminal.ReadLinePrefilled) decides when to call it and how
// to paint the row; the sources (SlashCompleter, ModelCompleter, PathCompleter)
// decide WHERE the candidates come from. Keeping the engine free of terminal
// state is what makes the cycling and common-prefix logic unit-testable.
package lineedit

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Completer answers "what can the word ending at the cursor position in line
// complete to?" The driver calls it on every Tab and on every change to the
// completion-relevant part of the line; a source may return nil (nothing to
// offer).
//
// Contract: every candidate is the replacement for the WORD ending at the
// cursor, not the whole line — a path source offers the `@path` mention text,
// a model source offers the bare model id. The engine splices that replacement
// into the line, keeping the text before and after the word intact.
type Completer interface {
	Candidates(line string, cursor int) []string
}

// commonPrefix returns the longest prefix shared by every element of c.
// An empty list yields ""; a single-element list is its own prefix.
func commonPrefix(c []string) string {
	if len(c) == 0 {
		return ""
	}
	p := c[0]
	for _, s := range c[1:] {
		for len(p) > 0 && !strings.HasPrefix(s, p) {
			// s diverges from p (or runs out): shorten p and re-check.
			p = p[:len(p)-1]
		}
	}
	return p
}

// replaceWord swaps the whitespace-delimited word ending at the cursor in
// line for repl, keeping everything before and after the word. This is the
// single acceptance rule the engine applies to every candidate: sources
// return the replacement for the word (see the Completer contract), and the
// engine splices it in — never overwriting the rest of the line. Returns
// ok=false when there is no word to replace (cursor at line start or right
// after whitespace) so the caller leaves the line untouched.
func replaceWord(line string, cursor int, repl string) (string, int, bool) {
	runes := []rune(line)
	start := cursor
	for start > 0 && !isWordSep(runes[start-1]) {
		start--
	}
	if start == cursor {
		return line, cursor, false
	}
	out := string(runes[:start]) + repl + string(runes[cursor:])
	return out, start + len([]rune(repl)), true
}

// fillPrefix extends the word ending at the cursor in line with the common
// prefix of c (the "completion fill"), and parks the cursor at the end of the
// filled word. Word boundaries are whitespace (the REPL line is a prose line,
// not a code line, so there is no richer boundary set).
//
// The acceptance-target behavior: typing `/mo`+Tab against {/model, /model-x}
// fills to `/model` (the common prefix) and offers both — it must NOT
// silently pick one.
func fillPrefix(line string, cursor int, c []string) (filled string, pos int, ok bool) {
	if len(c) == 0 {
		return line, cursor, false
	}
	prefix := commonPrefix(c)
	if prefix == "" {
		return line, cursor, false
	}
	runes := []rune(line)
	start := cursor
	for start > 0 && !isWordSep(runes[start-1]) {
		start--
	}
	word := string(runes[start:cursor])
	if word == "" {
		return line, cursor, false // cursor at start of line or after whitespace
	}
	// The prefix must be an extension of the typed word (the word is a prefix
	// of the prefix): anything else would clobber finished text.
	if !strings.HasPrefix(prefix, word) {
		return line, cursor, false
	}
	if len(prefix) <= len(word) {
		// Nothing new to fill (prefix equals or is shorter than the word).
		return line, cursor, false
	}
	return replaceWord(line, cursor, prefix)
}

// Completions is one live Tab-completion state: the candidate list last offered
// and the index the next Tab should select. Tab fills the common prefix on the
// first press and then CYCLES the index through the candidates (wraparound),
// so a user with several matches can step to the one they want without
// re-typing.
type Completions struct {
	src     []string
	idx     int
	offered bool // has Tab been pressed since the line last changed?
}

// NewCompletions returns an empty completion state.
func NewCompletions() *Completions { return &Completions{} }

// Tab applies one Tab press. It returns the line and cursor to restore (the
// filled line on the first Tab, the cycled candidate on subsequent Tabs) plus
// the candidate row to render below the input line ("" when the completer
// offered nothing).
//
// The contract the driver relies on:
//   - first Tab: fill the common prefix (no cycle yet) and show all candidates;
//   - later Tabs: cycle to the next candidate and show just that one, so the
//     row always reflects what the buffer holds now;
//   - a line change (any non-Tab key) resets the state via Change.
func (cm *Completions) Tab(line string, cursor int, get func(line string, cursor int) []string) (filled string, pos int, row string) {
	cands := get(line, cursor)
	if len(cands) == 0 {
		*cm = *NewCompletions()
		return line, cursor, ""
	}
	if !cm.offered {
		// First Tab: fill the common prefix (when it extends the typed word);
		// the row shows every candidate so the user sees what is reachable.
		// The index parks at -1, so the NEXT Tab selects the FIRST candidate —
		// no candidate is skipped on the first cycle.
		var ok bool
		filled, pos, ok = fillPrefix(line, cursor, cands)
		if !ok {
			filled, pos = line, cursor // nothing new to fill — just show the list
		}
		cm.src = cands
		cm.idx = -1
		cm.offered = true
		row = renderCandidateRow(cands)
		return filled, pos, row
	}
	// Subsequent Tabs: cycle. If the candidate list changed (the user edited
	// after a Tab without going through Change — defensive), reset and re-fill.
	if !sameSlice(cm.src, cands) {
		*cm = *NewCompletions()
		return cm.Tab(line, cursor, get)
	}
	cm.idx = (cm.idx + 1) % len(cands)
	// A candidate is the replacement for the word ending at the cursor (see
	// the Completer contract): splice it in place of that word, keeping the
	// text before and after. When there is no word (cursor at line start or
	// after whitespace) there is nothing to splice into — leave the line as
	// the user left it; the row still shows the candidate the next Tab will
	// fill.
	row = renderCandidateRow([]string{cands[cm.idx]})
	if filled, pos, ok := replaceWord(line, cursor, cands[cm.idx]); ok {
		return filled, pos, row
	}
	return line, cursor, row
}

// Change is called when the line changes for any reason other than a Tab. It
// drops the candidate state so the next Tab re-fills from the current line.
func (cm *Completions) Change() { *cm = *NewCompletions() }

// renderCandidateRow joins up to 8 candidates with a two-space separator for
// the single row the driver renders below the input line (no popup box, per
// the issue). A longer list shows the first 8 plus a "… +N" marker so the
// user knows more are reachable via further Tabs.
func renderCandidateRow(cands []string) string {
	if len(cands) == 0 {
		return ""
	}
	const cap = 8
	extra := 0
	if len(cands) > cap {
		extra = len(cands) - cap
		cands = cands[:cap]
	}
	row := strings.Join(cands, "  ")
	if extra > 0 {
		row += "  … +" + itoa(extra)
	}
	return row
}

// itoa is a tiny int→string helper so renderCandidateRow can avoid importing
// strconv for one use (the rest of this file is string-only).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// sameSlice reports whether a and b are element-equal.
func sameSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- Sources ----------------------------------------------------------------

// SlashCompleter completes `/`-commands: the fixed REPL command set plus a
// prefix-specific continuation (today: `/model <name>`). The fixed set is
// passed in so the editor (and tests) own the vocabulary — lineedit must not
// import cmd/cortex.
type SlashCompleter struct {
	// Commands is the set of complete command lines (e.g. "/model"), sorted
	// so Tab cycling is deterministic.
	Commands []string
	// Sub, if non-nil, returns continuation candidates for the current line
	// (e.g. the bare model id after "/model "). It is consulted ONLY when
	// the command before the cursor is a known command followed by a space —
	// the command itself is complete and the user is typing its argument.
	// Candidates follow the Completer contract: they replace the word ending
	// at the cursor (the argument), not the whole line — the "/model "
	// prefix stays untouched by acceptance.
	Sub func(line string, cursor int) []string
}

func (s SlashCompleter) Candidates(line string, cursor int) []string {
	if cursor == 0 {
		return nil
	}
	runes := []rune(line)
	// A slash command is only offered when the line STARTS with "/" and there
	// is no whitespace before the cursor — a "/" mid-sentence is prose.
	if runes[0] != '/' {
		return nil
	}
	for i := 0; i < cursor; i++ {
		if isWordSep(runes[i]) {
			return s.subCandidates(line, cursor)
		}
	}
	var out []string
	for _, c := range s.Commands {
		if strings.HasPrefix(c, line[:cursor]) {
			out = append(out, c)
		}
	}
	return out
}

// subCandidates handles the "command already complete, typing its argument"
// position: the line has whitespace before the cursor and the first token is a
// known command.
func (s SlashCompleter) subCandidates(line string, cursor int) []string {
	if s.Sub == nil {
		return nil
	}
	// First token: up to the first space.
	i := strings.IndexByte(line, ' ')
	if i < 0 || i >= cursor {
		return nil
	}
	cmd := line[:i]
	for _, c := range s.Commands {
		if c == cmd {
			return s.Sub(line, cursor)
		}
	}
	return nil
}

// ModelCompleter answers the `/model <name>` continuation with model ids. The
// caller supplies the id list lazily (cmd/cortex wires the curated table plus
// the currently bound code/study models) so a /model switch mid-session is
// picked up on the next Tab.
type ModelCompleter struct {
	Names func() []string
}

func (m ModelCompleter) Candidates(line string, cursor int) []string {
	if m.Names == nil {
		return nil
	}
	names := m.Names()
	if len(names) == 0 {
		return nil
	}
	const prefix = "/model "
	if !strings.HasPrefix(line, prefix) || cursor < len(prefix) {
		return nil
	}
	// Argument word: from after the last space before the cursor to the cursor.
	lastSpace := strings.LastIndex(line[:cursor], " ")
	if lastSpace < 0 {
		return nil
	}
	arg := line[lastSpace+1 : cursor]
	// Candidates are the bare model ids: per the Completer contract they
	// replace the ARGUMENT word ending at the cursor, leaving "/model " in
	// the line intact. Spliced acceptance then fills "/model q" → "/model
	// qwen/..." — and cycling a later Tab only ever rewrites the id.
	var out []string
	for _, n := range names {
		if strings.HasPrefix(n, arg) {
			out = append(out, n)
		}
	}
	return out
}

// PathCompleter answers `@`-mentions with workspace-relative paths. It walks
// the workspace (honoring .gitignore) and confines every candidate the same
// way tools.ConfinePath confines a tool's path argument: absolute paths and
// `..` escapes are refused outright, and a resolved path that leaves the
// workspace via a symlink is refused too. Directories are offered with a
// trailing "/" so a later Tab descends into them; the mention marker "@" stays
// on the left of every candidate, so acceptance fills the buffer with the
// complete mention.
type PathCompleter struct {
	// Root is the workspace root (usually the CWD). Empty means os.Getwd().
	Root string
	// MaxCandidates caps the offered list (a large workspace can have
	// thousands of files); the first N in sorted order win.
	MaxCandidates int
}

// RootAbs returns the resolved workspace root.
func (p PathCompleter) RootAbs() string {
	root := p.Root
	if root == "" {
		root, _ = os.Getwd()
	}
	root, _ = filepath.Abs(root)
	return root
}

// Candidates returns the completions offered for the given line.
func (p PathCompleter) Candidates(line string, cursor int) []string {
	runes := []rune(line)
	start := cursor
	for start > 0 && !isWordSep(runes[start-1]) {
		start--
	}
	word := string(runes[start:cursor])
	if len(word) == 0 || word[0] != '@' {
		return nil
	}
	// The path being typed is the word minus the "@" marker. A mention word
	// with whitespace after "@" is prose ("email me at a@b"), not a mention.
	rel := word[1:]
	if strings.ContainsAny(rel, " \t") {
		return nil
	}
	root := p.RootAbs()
	// An absolute or `..`-escaping mention is REFUSED outright — this is the
	// confinement the acceptance test checks.
	if filepath.IsAbs(rel) || containsDotDot(rel) {
		return nil
	}
	// If the mention names an existing directory, list its children.
	base := filepath.Join(root, filepath.Clean(rel))
	if info, err := os.Stat(base); err == nil && info.IsDir() {
		return p.listDir(base, rel)
	}
	// Otherwise walk up to the nearest existing ancestor and complete under
	// it. nearestDir returns the ancestor directory plus the typed tail
	// RELATIVE to that directory ("src/nonexist" → dir="src", tail="nonexist").
	dir, tail := p.nearestDir(base)
	if dir == "" {
		return nil
	}
	// relAt is the mention path (minus @) as seen FROM dir: "" when dir is
	// the workspace root, otherwise the rel prefix up to and including dir —
	// without the "@", because a candidate is the replacement for the word
	// ending at the cursor: the user has already typed the "@", and the
	// engine splices the candidate in place of the whole word, so every
	// candidate carries the marker exactly once.
	relAt := strings.TrimSuffix(rel, tail)
	if relAt != "" && !strings.HasSuffix(relAt, string(filepath.Separator)) {
		relAt += string(filepath.Separator)
	}
	return p.completeUnder(dir, tail, relAt)
}

// nearestDir walks up from abs to the deepest existing directory, returning it
// plus the typed path tail RELATIVE to it ("src/nonexist" → dir="src",
// tail="nonexist" — NO trailing separator, so the caller can prefix-match
// names against the first segment). Returns ("", "") when the walk reaches
// (or passes) the workspace root without finding a directory.
func (p PathCompleter) nearestDir(abs string) (dir, tail string) {
	root := p.RootAbs()
	for {
		if info, err := os.Stat(abs); err == nil && info.IsDir() {
			return abs, tail
		}
		parent := filepath.Dir(abs)
		if parent == abs || !withinRoot(parent, root) {
			return "", ""
		}
		tail = filepath.Base(abs)
		abs = parent
	}
}

// containsDotDot reports whether rel contains a `..` path segment. A bare
// filename like "a..b" is fine; only an actual segment is an escape attempt.
func containsDotDot(rel string) bool {
	for _, seg := range strings.Split(rel, string(filepath.Separator)) {
		if seg == ".." {
			return true
		}
	}
	return false
}

// withinRoot reports whether dir is root or inside it (lexical).
func withinRoot(dir, root string) bool {
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// listDir offers the immediate children of dir (a known-existing directory the
// user typed a complete mention for). Files as-is, directories with "/". rel
// is the mention path already typed WITHOUT the @ marker; it ends with "/" for
// a directory mention ("src/") or is the bare directory name ("src").
func (p PathCompleter) listDir(dir, rel string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.Name() == ".git" {
			continue // the VCS plumbing is never a completion target
		}
		full := filepath.Join(dir, e.Name())
		if gitignored(p.RootAbs(), full) {
			continue
		}
		sep := ""
		if rel != "" && !strings.HasSuffix(rel, string(filepath.Separator)) {
			sep = string(filepath.Separator) // the typed dir has no trailing "/" yet
		}
		cand := rel + sep + e.Name()
		if e.IsDir() {
			cand += string(filepath.Separator)
		}
		if !p.confined(cand) {
			continue
		}
		out = append(out, cand)
		if p.MaxCandidates > 0 && len(out) >= p.MaxCandidates {
			break
		}
	}
	return out
}

// completeUnder offers children of dir whose names begin with the typed tail.
// relAt is the mention path (minus @) as seen from dir: "" for the workspace
// root, "src/" for a nested directory. The typed tail may span several
// segments ("src/in": nearest existing ancestor "src", tail "in"): the FIRST
// segment is prefix-matched against names, and a directory match with a
// remaining tail is descended into.
func (p PathCompleter) completeUnder(dir, tail, relAt string) []string {
	first, rest := tail, ""
	if i := strings.IndexByte(tail, '/'); i >= 0 {
		first, rest = tail[:i], tail[i+1:]
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var out []string
	for _, e := range entries {
		if e.Name() == ".git" || !strings.HasPrefix(e.Name(), first) {
			continue
		}
		full := filepath.Join(dir, e.Name())
		if gitignored(p.RootAbs(), full) {
			continue
		}
		candRel := relAt + e.Name()
		if rest == "" {
			if e.IsDir() {
				candRel += string(filepath.Separator)
			}
		} else if !e.IsDir() {
			continue // more of the tail remains — only a directory can continue it
		}
		if !p.confined(candRel) {
			continue
		}
		out = append(out, candRel)
		if p.MaxCandidates > 0 && len(out) >= p.MaxCandidates {
			break
		}
	}
	// A directory whose name matched the first segment continues the rest of
	// the tail.
	if rest != "" {
		for _, e := range entries {
			if e.Name() == ".git" || !e.IsDir() || !strings.HasPrefix(e.Name(), first) {
				continue
			}
			full := filepath.Join(dir, e.Name())
			if gitignored(p.RootAbs(), full) {
				continue
			}
			sub := p.completeUnder(full, rest, relAt+e.Name()+string(filepath.Separator))
			out = append(out, sub...)
			if p.MaxCandidates > 0 && len(out) >= p.MaxCandidates {
				return out[:p.MaxCandidates]
			}
			break
		}
	}
	return out
}

// confined re-checks a candidate path against the workspace root using the
// same two-part check tools.ConfinePath uses: lexical containment plus
// symlink-resolved containment. Refusing a candidate here is what makes
// "@../../etc/passwd" (or a symlinked escape) simply never appear.
func (p PathCompleter) confined(rel string) bool {
	root := p.RootAbs()
	abs := filepath.Join(root, filepath.Clean(rel))
	if !withinRoot(abs, root) {
		return false
	}
	resolvedRoot, err := resolveSymlinks(root)
	if err != nil {
		return false
	}
	resolved, err := resolveSymlinks(abs)
	if err != nil {
		return false
	}
	relResolved, err := filepath.Rel(resolvedRoot, resolved)
	return err == nil && relResolved != ".." && !strings.HasPrefix(relResolved, ".."+string(filepath.Separator))
}

// resolveSymlinks mirrors internal/tools' (confine.go): it resolves every
// symlink along path, walking up to the deepest existing ancestor when the
// full path does not exist yet — a candidate's tail often does not (that is
// what the user is still typing).
func resolveSymlinks(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	dir := filepath.Dir(path)
	if dir == path {
		return path, nil
	}
	resolvedDir, err := resolveSymlinks(dir)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolvedDir, filepath.Base(path)), nil
}

// gitignored reports whether path (inside root) matches a .gitignore rule.
// The practical subset is implemented: negation (!), directory anchoring
// (trailing "/"), and the glob chars filepath.Match supports (*, ?,
// character classes). An unanchored rule (no "/") matches any path segment
// (the basename); an anchored one (with "/") matches the full relative path.
func gitignored(root, path string) bool {
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	isDir := false
	if info, err := os.Stat(path); err == nil {
		isDir = info.IsDir()
	}
	// A path inside an ignored directory is ignored too, regardless of its
	// own name ("build/" ignores every child) — so a directory rule matches
	// any path under that directory as well as the directory itself.
	segs := strings.Split(rel, string(filepath.Separator))
	ignored := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		negate := false
		if strings.HasPrefix(line, "!") {
			negate = true
			line = line[1:]
		}
		dirsOnly := false
		if strings.HasSuffix(line, "/") {
			dirsOnly = true
			line = strings.TrimSuffix(line, "/")
		}
		// A rule matches this path when its pattern does, OR — for a
		// directory rule — the path lives under a directory named by the
		// rule (everything below an ignored directory is ignored). The
		// dirsOnly half of that requires the directory: "build/" never
		// matches a file NAMED "build", and a non-directory rule like
		// "*.log" matches only by its pattern, never by position — so a
		// .gitignore containing "*.py[cod]" hides the .pyc files, not the
		// whole tree.
		var matched bool
		if dirsOnly {
			// "build/" ignores a directory NAMED build (the name match, incl.
			// globs) and everything under a directory named by the rule
			// (pathUnder) — but never a plain file that happens to be named
			// "build". pathUnder is false for the directory itself, so the
			// isDir name-match covers that case; the OR keeps the two halves
			// separate so a file named like the dir is never caught.
			matched = (isDir && gitignoreMatch(line, rel, true)) ||
				pathUnder(segs, line)
		} else {
			matched = gitignoreMatch(line, rel, isDir)
		}
		if !matched {
			continue
		}
		ignored = !negate
	}
	return ignored
}

// pathUnder reports whether segs (a path's segments) has dirName as one of its
// directory segments — i.e. the path lives under a directory named dirName.
func pathUnder(segs []string, dirName string) bool {
	for _, s := range segs[:len(segs)-1] {
		if s == dirName {
			return true
		}
	}
	return false
}

// gitignoreMatch reports whether rule matches rel (a workspace-relative path).
// A rule with a trailing "/" is a directory rule (dirsOnly): it matches the
// directory itself and, via pathUnder (checked by the caller), everything
// under it — so isDir here only decides the "is this the directory itself"
// half of that test.
func gitignoreMatch(rule, rel string, isDir bool) bool {
	if strings.Contains(rule, "/") {
		m, err := filepath.Match(rule, rel)
		return err == nil && m
	}
	if isDir && strings.HasSuffix(rule, "/") {
		return rule == filepath.Base(rel)
	}
	m, err := filepath.Match(rule, filepath.Base(rel))
	return err == nil && m
}
