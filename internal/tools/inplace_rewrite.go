package tools

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
)

// inplace_rewrite.go: best-effort detection of file rewrites made THROUGH the
// bash tool (issue #201). Cortex damaged Go files with sed/awk/inline-python
// scripts and then "repaired" them with destructive git commands; the fix is
// to see such rewrites at the harness level — report the touched targets on
// the bash result (the model and the turn record both see which files were
// touched outside the edit tools) and, when the command names a workdir path,
// run the post-edit hook on it (step 4).
//
// Detection is deliberately conservative: it names only targets it is sure a
// rewrite reached. A missed command degrades to today's behavior (no note);
// a false positive only costs a steering line the model can ignore. The
// command is scanned the way the shell would run it — one simple command at
// a time, quote-aware — and a target that is a shell variable ($VAR) is
// skipped, because its value is unknowable.
//
// Recognized rewrite forms:
//
//	sed -i [OPTS] 'SCRIPT' FILE...        -i, --in-place, and its [=SUFFIX]
//	                                       form; the script and its quotes
//	                                       never end the target list
//	ed -s                                  the whole stdin program edits
//	                                       the file on its argument list
//	perl -pi [OPTS] FILE...                -p/-i (any order, any cluster)
//	awk -i inplace 'prog' FILE...         gawk's in-place form: the file list
//	                                       IS the output; plain awk only READS
//	                                       its file list (a `> out` on a plain
//	                                       awk is caught by the redirect scan)
//	  (python/ruby/node/perl) -c 'SCRIPT' [ARG...]   the script string opens
//	                                       files in-process (open(...,'w'),
//	                                       Path.write_text); ARG is only a
//	                                       GUESS at the target (the script may
//	                                       open a path the argument does not
//	                                       name) — the steering note applies,
//	                                       the post-edit hook does not
//	  ... > TARGET   /   >> TARGET         an unquoted redirection: the shell
//	                                       rewrites TARGET before the
//	                                       command starts — the same
//	                                       clobber the issue names
//
// The tool-specific scanners (sed/ed/perl/awk/python) are quote-aware via
// splitSimpleCommand: a quoted string — the sed script, the -c program, the
// awk program — is consumed whole, so a program that merely MENTIONS a path
// (`python3 -c 'print(open("f.go").read())'`) is not read as editing that
// path; only the program's ARGUMENTS are targets. The redirect scan is
// separate, because splitSimpleCommand treats `>` as a shell operator it
// refuses to tokenize (it exists to rewrite `git commit`, not to parse
// arbitrary shell): scanRedirects walks the raw bytes, skipping quoted
// regions, and takes the word after each unquoted `>`/`>>`. A quoted
// `> "out.txt"` target comes through with its quotes stripped.
//
// Redirects are the exception to the in-script rule: `awk 'prog' FILE... >
// out` is the usual idiom (the program's print goes to stdout), so the
// redirect target is the rewrite target and the file list is its input, not
// its output. Only gawk's `-i inplace` flag makes the file list itself the
// output — plain awk has no in-place mode at all.

// rewriteForm names one detected rewrite in the model-facing note (step 3);
// step 4 folds it into the hook line for a workdir target.
type rewriteForm string

const (
	rewriteFormRedirect rewriteForm = "redirect"
	rewriteFormInPlace  rewriteForm = "in-place"
	rewriteFormScript   rewriteForm = "script"
	rewriteFormRead     rewriteForm = "read"
)

type rewriteTarget struct {
	// raw is the target as the model spelled it (quote-removed where it was
	// quoted); it is what the note shows.
	raw  string
	form rewriteForm
}

// scriptBins are interpreter invocations whose -c string is a whole program:
// open(..., 'w') inside it rewrites files in-process.
var scriptBins = map[string]bool{
	"python": true, "python2": true, "python3": true, "ruby": true,
	"node": true,
}

// scriptBinPlaceholder is the sentinel value stripQuotedRegions puts where a
// quoted region used to be; it is a valid word (so splitSimpleCommand keeps
// it as a token) but isBareFile refuses it (it is not a path), so it never
// leaks into the rewrite-target list.
const scriptBinPlaceholder = "\x00"

var (
	sedInPlaceRe = regexp.MustCompile(`^-i(\.[\w.-]+)?$`)
	perlFlagRe   = regexp.MustCompile(`^-[a-zA-Z]+$`)
	redirVarRe   = regexp.MustCompile(`^\$`)
	cFlagRe      = regexp.MustCompile(`^-c$|^-c=`)
)

// detectInPlaceRewrites scans command (already rewritten by the attribution
// backstop — the command the gate classified is the command that runs) and
// returns the rewrite targets it can see, in first-occurrence order,
// de-duplicated by raw path. A command whose only form is a READ (plain cat
// / head / tail / tac, sed/grep without a write flag, a piped head/tail) has
// NO rewrite targets — its targets carry rewriteFormRead (issue #209).
func detectInPlaceRewrites(command string) []rewriteTarget {
	var out []rewriteTarget
	seen := map[string]bool{}
	add := func(raw string, form rewriteForm) {
		raw = strings.TrimSpace(raw)
		if raw == "" || redirVarRe.MatchString(raw) {
			return // $VAR: the value is unknowable — don't guess
		}
		if seen[raw] {
			return
		}
		seen[raw] = true
		out = append(out, rewriteTarget{raw: raw, form: form})
	}
	// The binary/argument scan is per-simple-command: a `|`, `&`, `;`, or
	// newline outside quotes delimits commands, and splitSimpleCommand itself
	// is NOT quote-aware for those operators (it would split on the `;`
	// inside a quoted script), so we quote-strip the command first and then
	// split on the operators. The redirect scan is quote-aware and runs on
	// the ORIGINAL part (it needs the script bodies to know where a `>` is
	// data, not a redirect), and it runs AFTER the binary/argument scan for
	// the same command, so targets appear in first-occurrence order within
	// the command (the sed -i target before the redirect target in
	// `sed -i 's/x/y/' f.go && cat f.go > copy.go`). The read scan runs in
	// the same per-part pass, after the rewrite scan: a command that REWRITES
	// (sed -i, ed, perl -i, gawk -i, a script -c, a redirect) uses no read
	// binary, so scanReadPart finds nothing to add — and a sed -i's file is
	// its OUTPUT, never a read target. Only a command that reads (plain cat,
	// head, tail, tac, sed/grep without a write flag, a piped head/tail) has
	// a read binary, so its file arguments are named with rewriteFormRead.
	for _, part := range strings.FieldsFunc(stripQuotedRegions(command), func(r rune) bool {
		return r == '|' || r == '&' || r == ';' || r == '\n' || r == '\r'
	}) {
		scanRewritePart(part, add)
		scanReadPart(part, add)
	}
	// Redirects are scanned on the original (un-stripped) command, split on
	// the same operators but quote-aware so a `;` inside a quoted script
	// does not fool the splitter.
	for _, part := range splitQuoteAware(command) {
		scanRedirects(part, add)
	}
	return out
}

// splitQuoteAware splits command on the simple-command operators (`|`, `&`,
// `;`, newline, CR) but treats quoted regions as atomic: a `;` inside a
// single- or double-quoted string is data, not a delimiter. The returned
// parts are the ORIGINAL substrings (not the stripped form), so the
// redirect scan sees the exact bytes of each target (including quotes).
func splitQuoteAware(s string) []string {
	var parts []string
	i := 0
	start := 0
	for i < len(s) {
		c := s[i]
		switch c {
		case '\'':
			i = skipQuoted(s, i, '\'')
		case '"':
			i = skipQuoted(s, i, '"')
		case '\\':
			i += 2
			if i > len(s) {
				i = len(s)
			}
		case '|', '&', ';', '\n', '\r':
			parts = append(parts, s[start:i])
			i++
			for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
				i++
			}
			start = i
		default:
			i++
		}
	}
	parts = append(parts, s[start:])
	return parts
}

// stripQuotedRegions replaces every quoted region (single, double, or a
// backslash escape) with the scriptBinPlaceholder (a NUL byte), preserving
// word boundaries so the -c scanner can still see "the program string is the
// NEXT word" (a bare space would make the program and its -c flag one word),
// while removing the script bodies that would otherwise contain `;`/`|`/`&`/
// newline and fool the simple-command splitter. The returned string is used
// ONLY for the binary/argument scan; the redirect scan uses the original
// command (it is quote-aware and needs the script bodies to know where a `>`
// is data, not a redirect).
func stripQuotedRegions(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		c := s[i]
		switch c {
		case '\'':
			i = skipQuoted(s, i, '\'')
			b.WriteByte('\x00')
		case '"':
			i = skipQuoted(s, i, '"')
			b.WriteByte('\x00')
		case '\\':
			i += 2
			if i > len(s) {
				i = len(s)
			}
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// scanRewritePart scans one simple command's binary + arguments (no shell
// metacharacters remain — the caller split on them). Quoted arguments are
// NOT shell-aware: a quoted "git commit" in a string literal is not a
// command, and a quote that would not parse is skipped, not guessed at.
func scanRewritePart(part string, add func(string, rewriteForm)) {
	words, ok := splitSimpleCommand(part)
	if !ok {
		return // unparseable (subshell, substitution, unbalanced quote): skip
	}
	if len(words) < 2 {
		return
	}
	// A leading VAR=val is an env-mutated invocation (possibly several:
	// `LC_ALL=C LANG=C sed ...`); the binary is the first word without an
	// `=`. Words with an `=` are never the binary.
	offset := 0
	for offset < len(words) && strings.Contains(words[offset].val, "=") {
		offset++
	}
	if offset >= len(words) {
		return
	}
	bin := lastPathElem(words[offset].val)
	args := words[offset+1:]
	switch bin {
	case "sed":
		scanSed(args, add)
	case "ed":
		scanEd(args, add)
	case "perl":
		// `perl -c FILE` compile-checks a file (it READS, never writes), so a
		// perl invocation is only an in-place edit via its -i/-pi forms
		// (scanPerl); a perl -c is never a rewrite.
		scanPerl(args, add)
	case "awk":
		scanAwk(args, add)
	default:
		if scriptBins[bin] {
			scanScriptC(args, add)
		}
	}
}

// isBareFile reports whether a word is a file argument (not a flag and not
// the placeholder that stripQuotedRegions leaves in place of a quoted region).
func isBareFile(v string) bool {
	return v != scriptBinPlaceholder && (!strings.HasPrefix(v, "-") || v == "-")
}

// scanSed: `sed -i [OPTS] 'SCRIPT' FILE...` — -i (with or without an
// =SUFFIX form), --in-place, or -i[SUFFIX]. The script (quoted or bare) is
// consumed, so only the FILE arguments after it are targets. A sed without
// -i only reads. Scripts may be inline (`sed -i 's/a/b/' f`) or via -e
// (`sed -i -e 's/a/b/' -e 's/c/d/' f`); in both forms the files come after
// the last script word.
func scanSed(args []shellWord, add func(string, rewriteForm)) {
	inPlace := false
	lastScript := -1
	// Consume leading flags; -e consumes its script argument.
	i := 0
	for i < len(args) {
		v := args[i].val
		if !strings.HasPrefix(v, "-") || v == "-" {
			break
		}
		if v == "-i" || sedInPlaceRe.MatchString(v) || v == "--in-place" || strings.HasPrefix(v, "--in-place=") {
			inPlace = true
		}
		if v == "-e" && i+1 < len(args) {
			i++ // the script argument
			lastScript = i
		}
		i++
	}
	if !inPlace {
		return
	}
	// Skip the script: ONE word after the flags, whether quoted or bare
	// (the file list is everything after it). With -e, the LAST -e script
	// is the one to skip past; the files follow it.
	if lastScript >= 0 {
		i = lastScript + 1
	} else if i < len(args) {
		i++ // the inline script
	}
	for ; i < len(args); i++ {
		if isBareFile(args[i].val) {
			add(args[i].val, rewriteFormInPlace)
		}
	}
}

// scanEd: `ed -s 'cmds' FILE...` — ed's whole stdin program edits its file
// arguments in place. Any ed invocation with file arguments is an in-place
// edit by definition (ed opens, edits, and writes back).
func scanEd(args []shellWord, add func(string, rewriteForm)) {
	for _, a := range args {
		if isBareFile(a.val) {
			add(a.val, rewriteFormInPlace)
		}
	}
}

// scanPerl: `perl -pi [OPTS] FILE...` — -p and -i in any order, any cluster.
// -i alone (no -p) also rewrites a plain `perl -i 'x' file` loop.
func scanPerl(args []shellWord, add func(string, rewriteForm)) {
	sawI := false
	i := 0
	for i < len(args) {
		v := args[i].val
		if !strings.HasPrefix(v, "-") || v == "-" {
			break
		}
		if perlFlagRe.MatchString(v) && strings.Contains(v[1:], "i") {
			sawI = true
		}
		if v == "-e" && i+1 < len(args) {
			i++
		}
		i++
	}
	if !sawI {
		return
	}
	for ; i < len(args); i++ {
		if isBareFile(args[i].val) {
			add(args[i].val, rewriteFormInPlace)
		}
	}
}

// scanAwk: `awk -i inplace 'prog' FILE...` (gawk's in-place form) rewrites
// its file list. Plain awk has NO in-place mode — its file list is its
// INPUT, and its own print goes to stdout — so a plain `awk 'prog' f` is
// never named a rewrite target here; if the invocation also redirects
// stdout to a file, the redirect scan reports THAT target. Only the
// `-i`/`--in-place` flag (gawk) makes the file list the output.
func scanAwk(args []shellWord, add func(string, rewriteForm)) {
	inPlace := false
	i := 0
	for i < len(args) {
		v := args[i].val
		if !strings.HasPrefix(v, "-") || v == "-" {
			break // the program word
		}
		if v == "-i" || v == "-i inplace" || v == "--in-place" || v == "--in-place=" {
			inPlace = true
		}
		if (v == "-v" || v == "-F") && i+1 < len(args) {
			i++
		}
		i++
	}
	if !inPlace {
		return
	}
	// The program is the next word (quoted or bare); skip it.
	// `awk -f progfile FILE...` names the files after the program file.
	i++
	if i < len(args) && args[i].val == "-f" {
		i++
		if i < len(args) {
			i++ // the program file
		}
	}
	for ; i < len(args); i++ {
		if isBareFile(args[i].val) {
			add(args[i].val, rewriteFormInPlace)
		}
	}
}

// scanScriptC: `(python|ruby|node) -c 'SCRIPT' [ARG...]` — the program
// string opens files in-process. The targets are the program's ARGUMENTS (a
// `python3 -c 'open(sys.argv[1],"w")' file.go` rewrites file.go); without
// arguments there is no knowable target, so nothing is reported. `perl -c`
// is the OPPOSITE (it compile-checks a FILE argument, reading it), so a
// perl -c invocation is never a rewrite.
func scanScriptC(args []shellWord, add func(string, rewriteForm)) {
	sawC := false
	i := 0
	for i < len(args) {
		v := args[i].val
		if !strings.HasPrefix(v, "-") || v == "-" {
			break
		}
		if cFlagRe.MatchString(v) {
			sawC = true
			if v == "-c" && i+1 < len(args) {
				i++ // the program string is the NEXT word
			}
		}
		i++
	}
	if !sawC {
		return
	}
	for ; i < len(args); i++ {
		if isBareFile(args[i].val) {
			add(args[i].val, rewriteFormScript)
		}
	}
}

// readBins are the binaries whose FILE arguments are READ (not written): a
// plain cat / head / tail / tac, and sed / grep without their write forms
// (sed -i / ed / perl -i / gawk -i are REWRITES, handled by the rewrite
// scan, so they never reach the read scan as readBins). The read scan names
// their file arguments with rewriteFormRead (issue #209). `grep` is always a
// read (it has no in-place mode), and `head`/`tail`/`tac` read their file
// list (tail -f is excluded by scanReadPart's -f check).
var readBins = map[string]bool{
	"cat": true, "head": true, "tail": true, "tac": true,
	"sed": true, "grep": true,
}

// scanReadPart scans one simple command's binary + arguments (no shell
// metacharacters remain — the caller split on them) and names its file
// arguments as READ targets (rewriteFormRead, issue #209) when the binary is
// a reader. A command that REWRITES (sed -i, ed, perl -i, gawk -i, a script
// -c, a redirect) never reaches the read scan: its binary is not a readBin,
// so nothing is added. The file arguments are the same bare-file words the
// rewrite scan names (isBareFile), so a sed -i's file (its OUTPUT) is never
// a read target.
func scanReadPart(part string, add func(string, rewriteForm)) {
	words, ok := splitSimpleCommand(part)
	if !ok {
		return
	}
	if len(words) < 2 {
		return
	}
	// A leading VAR=val is an env-mutated invocation (possibly several);
	// the binary is the first word without an `=`. Words with an `=` are
	// never the binary. (The rewrite scan does the same offset.)
	offset := 0
	for offset < len(words) && strings.Contains(words[offset].val, "=") {
		offset++
	}
	if offset >= len(words) {
		return
	}
	bin := lastPathElem(words[offset].val)
	args := words[offset+1:]
	if !readBins[bin] {
		return // not a reader (a rewrite, an interpreter, or an unknown bin)
	}
	// `head`/`tail`/`tac` consume their leading flags (so `-n 40`'s argument
	// is not a file); `-f` (tail's follow mode) names no file to read.
	// `sed`/`grep` consume their leading flags + scripts (so `sed -n '2,4p'`'s
	// script is not a file). `cat` has no flags that consume a file argument,
	// so its file list is everything after the binary.
	i := 0
	switch bin {
	case "head", "tail", "tac":
		for i < len(args) {
			v := args[i].val
			if !strings.HasPrefix(v, "-") || v == "-" {
				break
			}
			if v == "-f" {
				return // tail -f: follow mode, no file to read
			}
			// Flags that take a value (-n, -c, -k) consume their argument.
			if v == "-n" || v == "-c" || v == "-k" || v == "--lines" ||
				v == "--bytes" || v == "--sleep-interval" || v == "--quiet" ||
				v == "--silent" || v == "--verbose" {
				i++
			}
			i++
		}
	case "sed":
		for i < len(args) {
			v := args[i].val
			if !strings.HasPrefix(v, "-") || v == "-" {
				break
			}
			if v == "-i" || sedInPlaceRe.MatchString(v) || v == "--in-place" || strings.HasPrefix(v, "--in-place=") {
				return // sed -i: a REWRITE (handled by the rewrite scan)
			}
			if (v == "-e" || v == "-f") && i+1 < len(args) {
				i++ // the script argument
			}
			i++
		}
	case "grep":
		hasPatternFlag := false
		for i < len(args) {
			v := args[i].val
			if !strings.HasPrefix(v, "-") || v == "-" {
				break
			}
			// -e/-f take a pattern argument; -A/-B/-C take a line count.
			if v == "-e" || v == "-f" {
				hasPatternFlag = true
				i++ // the pattern argument
			}
			if (v == "-A" || v == "-B" || v == "-C") && i+1 < len(args) {
				i++ // the line-count argument
			}
			i++
		}
		// The first non-flag word is the PATTERN (unless it came via -e/-f),
		// so skip it; the rest are the files. `grep -A 5 TODO inspect.go`:
		// -A eats 5, TODO is the pattern, inspect.go is the file.
		if !hasPatternFlag && i < len(args) && isBareFile(args[i].val) {
			i++
		}
	}
	for ; i < len(args); i++ {
		if isBareFile(args[i].val) {
			add(args[i].val, rewriteFormRead)
		}
	}
}

// scanRedirects walks one simple command's raw bytes and names the target of
// every unquoted `> TARGET` / `>> TARGET`. It is separate from
// splitSimpleCommand, which treats `>` as a shell operator it refuses to
// tokenize. Quoted regions (single, double, or a backslash escape) are
// skipped — a `>` inside a quoted string is data, not a redirect — and the
// target's own quotes are kept (the note shows the raw spelling). `2>&1`
// (file descriptor dup) is naturally skipped: its `&1` is not a bare `>`, and
// the fd number sits before the operator, never after. `> /dev/null` is
// output being thrown away, not a file rewrite, and is skipped.
func scanRedirects(part string, add func(string, rewriteForm)) {
	i := 0
	for i < len(part) {
		c := part[i]
		switch c {
		case '\'':
			i = skipQuoted(part, i, '\'')
		case '"':
			i = skipQuoted(part, i, '"')
		case '\\':
			i += 2
			if i > len(part) {
				i = len(part)
			}
		case '>':
			j := i + 1
			if j < len(part) && part[j] == '>' {
				j++
			}
			for j < len(part) && (part[j] == ' ' || part[j] == '\t') {
				j++
			}
			// A `>` followed by `<`, `|`, `&`, or end-of-command is not a
			// file redirect (it is a fd dup, a pipe, or a dangling operator).
			if j >= len(part) || part[j] == '<' || part[j] == '|' || part[j] == '&' {
				i = j
				continue
			}
			end := skipTarget(part, j)
			raw := strings.TrimSpace(part[j:end])
			if raw != "" && raw != "/dev/null" {
				add(raw, rewriteFormRedirect)
			}
			i = end
		default:
			i++
		}
	}
}

// skipQuoted returns the index just past the quoted region that starts at i
// (part[i] is the opening quote). For double quotes, backslash escapes are
// honored; for single quotes, everything to the next `'` is data.
func skipQuoted(part string, i int, quote byte) int {
	i++ // the opening quote
	for i < len(part) && part[i] != quote {
		if quote == '"' && part[i] == '\\' {
			i++
		}
		i++
	}
	if i < len(part) {
		i++ // the closing quote
	}
	return i
}

// skipTarget returns the index just past the redirect target that starts at
// i. The target ends at the next unquoted space/tab or end of command.
// Quoted regions within the target are consumed whole (so `> "out file.txt"`
// yields the index past the closing quote, and the raw bytes keep the quotes
// for the note).
func skipTarget(part string, i int) int {
	for i < len(part) {
		c := part[i]
		switch c {
		case ' ', '\t':
			return i
		case '\'':
			i = skipQuoted(part, i, '\'')
		case '"':
			i = skipQuoted(part, i, '"')
		case '\\':
			i += 2
		default:
			i++
		}
	}
	return i
}

// lastPathElem returns the binary name from a path-qualified invocation
// (/usr/bin/sed → sed, ./bin/python3 → python3) so the form lookup matches.
func lastPathElem(s string) string {
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// rewriteTargetDisplay renders one rewrite target for the model-facing note:
// a workdir path made workdir-relative (the form the edit tools use) when a
// workdir is anchored, the raw spelling otherwise.
func rewriteTargetDisplay(raw, workdir string) string {
	if workdir != "" {
		if abs := absOf(raw, workdir); abs != "" {
			if rel, err := relInsideWorkdir(abs, workdir); err == nil {
				return rel
			}
		}
	}
	return raw
}

// hasRewriteTarget reports whether command names at least one target a
// REWRITE reached (in-place, redirect, or script form) — as opposed to a
// read-only target (rewriteFormRead). The bash tool's grep no-match
// special-case ("no matches") is suppressed when a command changed
// something, but a pure read (cat / head / sed -n / grep -A) changes
// nothing, so a read-only note must NOT suppress it (issue #209 step 1).
func hasRewriteTarget(command string) bool {
	for _, t := range detectInPlaceRewrites(command) {
		if t.form != rewriteFormRead {
			return true
		}
	}
	return false
}

// heredocDataBins are binaries for which a heredoc (`<<`) is PROGRAM DATA, not
// a file being created: the interpreter/script binaries (python/ruby/node/
// perl/awk/sed/ed — the heredoc is the source they execute) and tee/cp/mv
// (whose file arguments are written by their own machinery, not by a heredoc
// probe). A heredoc on any OTHER binary — a `cat`, a bare shell, a probe
// (`cat <<'EOF'` to test whether a file exists) — names a file being created
// and is steered to write_file (issue #209 step 3). A `<<<` here-string is
// inline input data, never a file, and is never a create target.
var heredocDataBins = map[string]bool{
	"python": true, "python2": true, "python3": true,
	"ruby": true, "node": true, "perl": true,
	"awk": true, "sed": true, "ed": true,
	"tee": true, "cp": true, "mv": true,
}

// hasHeredocCreate reports whether command has a `<<` heredoc in a simple
// command whose binary is not one of heredocDataBins (issue #209 step 3). It
// returns true for a `cat <<'EOF'` probe (the model writing a file via the
// shell) and false for `python3 <<EOF` (the heredoc is the program),
// `tee file <<EOF`, and a `<<` inside a quoted argument (`git commit -m 'x
// << y'`). `<<<` here-strings are ignored (inline data, never a file).
func hasHeredocCreate(command string) bool {
	// The command scan is per-simple-command, split on the same operators the
	// rewrite scan uses, but quote-aware (splitQuoteAware) so a `<<` inside a
	// quoted string — the `git commit -m 'x << y'` case — is data, not a
	// heredoc. A real heredoc is always unquoted shell syntax, so scanning the
	// original (un-stripped) parts finds it; the heredoc BODY may itself
	// contain operators and quoted text, but the `<<` operator precedes the
	// body, so the binary + `<<` are seen before any body byte.
	for _, part := range splitQuoteAware(command) {
		if hasHeredocCreatePart(part) {
			return true
		}
	}
	return false
}

// hasHeredocCreatePart reports whether one simple command (already split on
// the simple-command operators) has an unquoted `<<` heredoc whose binary is
// not a heredocDataBin. The binary is extracted from the raw first word
// (past any VAR=val env prefix); the part is then scanned forward from the
// binary for an unquoted `<<` that is not part of a `<<<` here-string (the
// third `<` disqualifies it). Quoted regions are skipped (a `<<` inside quotes
// is data). `splitSimpleCommand` is not used here: it refuses `<` as a shell
// operator, but a heredoc's `<<` is exactly that.
func hasHeredocCreatePart(part string) bool {
	// Extract the binary: the first word without an `=` (a leading VAR=val
	// env prefix is skipped). We walk the raw bytes — splitSimpleCommand
	// refuses `<`, which a heredoc's `<<` is.
	bin := ""
	i := 0
	for i < len(part) {
		for i < len(part) && (part[i] == ' ' || part[i] == '\t') {
			i++
		}
		wstart := i
		for i < len(part) && part[i] != ' ' && part[i] != '\t' && part[i] != '\n' {
			i++
		}
		word := part[wstart:i]
		if word == "" {
			break
		}
		if !strings.Contains(word, "=") {
			// A word containing a heredoc operator (`<<`) is not a binary —
			// it is the heredoc itself (a bare `<<EOF` with no command). We
			// cannot name a create without a binary, so return false.
			if strings.Contains(word, "<<") {
				return false
			}
			bin = word
			break
		}
	}
	if bin == "" {
		return false
	}
	bin = lastPathElem(bin)
	if heredocDataBins[bin] {
		return false // the heredoc is program data, not a file create
	}
	// Scan the part for an unquoted `<<` heredoc. The heredoc body may start
	// on the NEXT line (after a newline), so we walk through newlines and
	// backslash-continuations until we find a `<<` (not `<<<`) or reach the
	// end. Quoted regions are skipped.
	heredoc := false
	j := 0
	for j < len(part) {
		c := part[j]
		switch c {
		case '\'':
			j = skipQuoted(part, j, '\'')
		case '"':
			j = skipQuoted(part, j, '"')
		case '\\':
			j += 2
			if j > len(part) {
				j = len(part)
			}
		case '\n', '\r':
			j++
		case '<':
			k := j + 1
			if k < len(part) && part[k] == '<' {
				if k+1 < len(part) && part[k+1] == '<' {
					j = k + 1 // `<<<` here-string: inline data, not a heredoc
					continue
				}
				heredoc = true
				j = k
			}
		}
		if heredoc {
			break
		}
		j++
	}
	return heredoc
}

// inPlaceRewriteNote renders the model-facing note for one bash command that
// touches files (issue #201 rewrites, issue #209 reads). It names the touched
// targets — workdir-relative when a workdir is anchored, so the model can act
// on the same path it uses everywhere — and steers to the right tool for each
// form: rewrite targets (in-place, redirect, script) steer to
// edit_file/write_file (diff display + post-edit hook), read targets (plain
// cat/head/tail/tac/sed/grep) steer to read_file/outline/grep (the dedicated
// readers a bash shell command skips). One combined note when the command has
// both kinds. "" when the command names no target at all; the caller (bash)
// appends it to the result on both the run and the refused paths.
func inPlaceRewriteNote(deps ToolDeps, command string) string {
	targets := detectInPlaceRewrites(command)
	// A heredoc create (issue #209 step 3) may fire even when the rewrite
	// scan names no targets (a `cat <<'EOF'` probe names no read or rewrite
	// target — the heredoc is the create, not a file argument).
	heredocCreate := hasHeredocCreate(command)
	if len(targets) == 0 && !heredocCreate {
		return ""
	}
	var rewriteTargets, readTargets []rewriteTarget
	for _, t := range targets {
		if t.form == rewriteFormRead {
			readTargets = append(readTargets, t)
		} else {
			rewriteTargets = append(rewriteTargets, t)
		}
	}
	wd := workdirOf(deps)
	var b strings.Builder
	if len(rewriteTargets) > 0 {
		b.WriteString("note: this command rewrites file(s) in place: ")
		for i, t := range rewriteTargets {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(rewriteTargetDisplay(t.raw, wd))
		}
		b.WriteString(" — use edit_file (or write_file for a whole-file rewrite) for file edits: they show the diff and run the post-edit hook, which scripted edits skip.")
	}
	if len(readTargets) > 0 {
		if len(rewriteTargets) > 0 {
			b.WriteString(" ")
		}
		b.WriteString("note: this command reads file(s) via the shell: ")
		for i, t := range readTargets {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(rewriteTargetDisplay(t.raw, wd))
		}
		b.WriteString(" — use read_file (or outline/grep) for file reads: they are the dedicated readers, sized and quote-aware, that a bash shell command skips.")
	}
	if heredocCreate {
		if len(rewriteTargets) > 0 || len(readTargets) > 0 {
			b.WriteString(" ")
		}
		b.WriteString("note: this command creates a file via a heredoc — use write_file for new files: it shows the diff and runs the post-edit hook, which a heredoc skips.")
	}
	return b.String()
}

// inPlaceRewriteHookNote closes the hook gap (issue #201, step 4): when a
// detected in-place rewrite targets a workdir path, it runs the SAME
// format-only post-edit hook write_file/edit_file run on that file
// (runProjectCommandHook), so a scripted edit gets the same format/
// verification coverage as a tool edit. It returns the combined hook notes,
// one per workdir target in first-occurrence order, de-duplicated by
// resolved path. "" when there is nothing to report — no workdir anchor, no
// target inside the workdir, an untrusted workspace (the hook's own
// once-per-session "inactive" note is the only output in that case), a hook
// mode of off, or no format command that applies to the file's extension.
//
// The hook runs the command's argv against the file on disk, so a target
// that does not exist yet (a create) simply yields no note — the file is
// left exactly as the command left it, and the steering note above still
// tells the model how to redo the change with the edit tools.
func inPlaceRewriteHookNote(ctx context.Context, deps ToolDeps, command string) string {
	targets := detectInPlaceRewrites(command)
	if len(targets) == 0 {
		return ""
	}
	wd := workdirOf(deps)
	if wd == "" {
		return "" // no workdir anchor: there is no workspace-relative target to hook
	}
	seen := map[string]bool{}
	var b strings.Builder
	for _, t := range targets {
		if t.form == rewriteFormScript {
			continue // a script-form target is a GUESS (the script string's args
			// may not be the file it opens); the steering note names it, but
			// the hook must not format a file the command never touched
		}
		abs := absOf(t.raw, wd)
		if abs == "" || !filepath.IsAbs(abs) {
			continue
		}
		rel, err := relInsideWorkdir(abs, wd)
		if err != nil {
			continue // outside the workspace: the hook is scoped to the workdir
		}
		if seen[rel] {
			continue
		}
		seen[rel] = true
		note := runProjectCommandHook(ctx, projectCommandsOf(deps), wd, abs, workspaceTrusted(deps), hookStateOf(deps), effectiveHookMode(hookStateOf(deps), false))
		if note != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(note)
		}
	}
	return b.String()
}

var (
	errNotInPlaceAbs     = errors.New("not an absolute path")
	errNotInPlaceWorkdir = errors.New("outside the workdir")
)

// absOf anchors raw to workdir (relative) or takes it as-is (absolute); ""
// when the path is unresolvable.
func absOf(raw, workdir string) string {
	if raw == "" {
		return ""
	}
	if filepath.IsAbs(raw) {
		return raw
	}
	return filepath.Join(workdir, raw)
}

// relInsideWorkdir makes abs relative to workdir, refusing to escape it.
func relInsideWorkdir(abs, workdir string) (string, error) {
	if !filepath.IsAbs(abs) {
		return "", errNotInPlaceAbs
	}
	rel, err := filepath.Rel(workdir, abs)
	if err != nil || rel == "" || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errNotInPlaceWorkdir
	}
	return filepath.ToSlash(rel), nil
}
