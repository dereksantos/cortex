package tools

import (
	"regexp"
	"strings"
)

// maybeAddAttributionTrailer is the bash tool's git-commit attribution
// backstop. When attribution is on (deps.AttributionCommit() non-empty) and
// command is exactly one simple `git commit …` invocation that doesn't
// already carry the trailer text, it returns the command with
// --trailer='<trailer>' spliced in right after the `commit` word — so it
// always precedes a bare `--` and any pathspecs. The trailer is POSIX
// single-quoted, so no $, backtick or quote in a template is ever evaluated.
// bash() calls it BEFORE the shell-risk gate, so the gate classifies (and a
// confirm prompt shows) the command that actually runs.
//
// It refuses to rewrite — returning command unchanged — anything it can't
// parse as one simple command: pipelines, &&/||/; chains, redirections,
// subshells or command substitution, newlines, comments, unbalanced quotes,
// or a command whose first word isn't `git commit`. It also refuses
// `--amend` (the amended message may already be attributed, or not be the
// agent's) and a message read from stdin (-F - / --file=-).
//
// note is non-empty when the rewrite was refused for a command that still
// appears to run `git commit` — an unparseable one containing it, or a
// stdin-message commit — and doesn't mention the trailer: bash() appends it
// to the tool result so the model can add the trailer itself. A parseable
// simple command that isn't `git commit …` (echo "git commit", git -C x
// commit) gets neither a rewrite nor a note.
func maybeAddAttributionTrailer(command string, deps ToolDeps) (rewritten, note string) {
	trailer := deps.AttributionCommit()
	if trailer == "" || strings.Contains(command, trailer) {
		return command, ""
	}
	words, ok := splitSimpleCommand(command)
	if !ok {
		if gitCommitRe.MatchString(command) && !strings.Contains(command, "--amend") {
			return command, missingTrailerNote(trailer)
		}
		return command, ""
	}
	if len(words) < 2 || words[0].val != "git" || words[1].val != "commit" {
		return command, ""
	}
	for _, w := range words[2:] {
		if w.val == "--" {
			break
		}
		if strings.Contains(w.val, trailer) || isAmendFlag(w.val) {
			return command, ""
		}
		if readsMessageFromStdin(w.val) {
			return command, missingTrailerNote(trailer)
		}
	}
	at := words[1].end
	return command[:at] + " --trailer=" + quoteShellArg(trailer) + command[at:], ""
}

// gitCommitRe loosely spots a `git commit` invocation anywhere in a command
// the backstop refused to rewrite — used only to decide whether to attach
// the missing-trailer note, never to rewrite.
var gitCommitRe = regexp.MustCompile("(?:^|[\\s;&|(`])git\\s+commit(?:\\s|$)")

func missingTrailerNote(trailer string) string {
	return "[attribution: this command was not rewritten to add the attribution trailer; if it created a commit, its message should end with: " + trailer + "]"
}

// isAmendFlag reports whether a word is `--amend` or an unambiguous
// abbreviation of it that git would accept (--am, --ame, --amen).
func isAmendFlag(w string) bool {
	return len(w) >= len("--am") && strings.HasPrefix("--amend", w)
}

// readsMessageFromStdin reports whether a word makes git commit read its
// message from stdin: a bare "-" (as the argument to -F/--file), -F- in a
// short-option cluster, or --file=-. Deliberately over-broad — a false
// positive only means the command is left unrewritten.
func readsMessageFromStdin(w string) bool {
	if w == "-" || w == "--file=-" {
		return true
	}
	return strings.HasPrefix(w, "-") && !strings.HasPrefix(w, "--") && strings.HasSuffix(w, "F-")
}

// quoteShellArg quotes a string for safe use in a bash -c command using
// POSIX single-quote escaping: the value is wrapped in '...' and each ' is
// replaced with '\” (end the quote, an escaped quote, restart the quote).
// No expansion happens inside single quotes, so a template containing $,
// backticks or & can never be evaluated by the shell.
func quoteShellArg(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellWord is one word of a simple command: its value after quote removal
// and the byte span [start, end) it occupies in the raw command.
type shellWord struct {
	val        string
	start, end int
}

// splitSimpleCommand splits command into the words of ONE simple command,
// applying POSIX quote removal ('…', "…", backslash escapes). ok is false —
// the command is not safe to rewrite — when it contains anything beyond
// plain words: an unquoted operator or redirection (| & ; < > ( )), a
// newline, a comment, command substitution ($( or a backtick, outside single
// quotes), or an unterminated quote.
func splitSimpleCommand(command string) (words []shellWord, ok bool) {
	var (
		cur     strings.Builder
		inWord  bool
		start   int
		sq, dq  bool // inside single / double quotes
		escaped bool // previous byte was an unquoted or in-dq backslash
	)
	flush := func(end int) {
		if inWord {
			words = append(words, shellWord{val: cur.String(), start: start, end: end})
			cur.Reset()
			inWord = false
		}
	}
	begin := func(i int) {
		if !inWord {
			inWord = true
			start = i
		}
	}
	for i := 0; i < len(command); i++ {
		c := command[i]
		if c == '\n' || c == '\r' {
			return nil, false
		}
		switch {
		case escaped:
			escaped = false
			cur.WriteByte(c)
		case sq:
			if c == '\'' {
				sq = false
			} else {
				cur.WriteByte(c)
			}
		case dq:
			switch c {
			case '"':
				dq = false
			case '`':
				return nil, false
			case '$':
				if i+1 < len(command) && command[i+1] == '(' {
					return nil, false
				}
				cur.WriteByte(c)
			case '\\':
				if i+1 < len(command) && strings.IndexByte("$`\"\\", command[i+1]) >= 0 {
					escaped = true
				} else {
					cur.WriteByte(c)
				}
			default:
				cur.WriteByte(c)
			}
		default:
			switch c {
			case ' ', '\t':
				flush(i)
			case '\'':
				begin(i)
				sq = true
			case '"':
				begin(i)
				dq = true
			case '\\':
				begin(i)
				escaped = true
			case '|', '&', ';', '<', '>', '(', ')', '`':
				return nil, false
			case '$':
				if i+1 < len(command) && command[i+1] == '(' {
					return nil, false
				}
				begin(i)
				cur.WriteByte(c)
			case '#':
				if !inWord {
					return nil, false
				}
				cur.WriteByte(c)
			default:
				begin(i)
				cur.WriteByte(c)
			}
		}
	}
	if sq || dq || escaped {
		return nil, false
	}
	flush(len(command))
	return words, true
}
