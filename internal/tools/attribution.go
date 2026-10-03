package tools

import (
	"os/exec"
	"regexp"
	"strings"
	"sync"

	"github.com/dereksantos/cortex/internal/journal"
)

// backstopOutcome is what the git-commit attribution backstop decided about
// one command, reported verbatim on the attribution.commit journal event
// (internal/journal/attribution.go) so commit-attribution compliance is
// measured rather than assumed (issue #146). The values are the journal's
// vocabulary (journal.AttributionOutcome*), restated here because
// internal/tools must not depend on a caller's labels.
const (
	outcomeDisabled        = "disabled"
	outcomeAdded           = "added"
	outcomeAlreadyPresent  = "already_present"
	outcomeSkippedUnparsed = "skipped_unparseable"
	outcomeSkippedAmend    = "skipped_amend"
	outcomeSkippedStdin    = "skipped_stdin"
	// outcomeNotACommit marks a command the backstop does not treat as a git
	// commit at all. It is the one outcome that is never journaled: there is
	// no commit for it to make a claim about.
	outcomeNotACommit = ""
)

// classifyAttribution is the bash tool's git-commit attribution backstop: it
// decides what to do about the trailer AND says which decision it took, so
// the decision is reportable as well as applied. When attribution is on
// (deps.AttributionCommit() non-empty) and command is exactly one simple
// `git commit …` invocation that doesn't already carry the trailer text, it
// returns the command with --trailer='<trailer>' spliced in right after the
// `commit` word — so it always precedes a bare `--` and any pathspecs —
// with outcome added. The trailer is POSIX single-quoted, so no $, backtick
// or quote in a template is ever evaluated. bash() calls it BEFORE the
// shell-risk gate, so the gate classifies (and a confirm prompt shows) the
// command that actually runs.
//
// It refuses to rewrite — returning command unchanged — anything it can't
// parse as one simple command: pipelines, &&/||/; chains, redirections,
// subshells or command substitution, newlines, comments, unbalanced quotes,
// or a command whose first word isn't `git commit`. It also refuses
// `--amend` (the amended message may already be attributed, or not be the
// agent's) and a message read from stdin (-F - / --file=-). Each refusal
// names itself in outcome, so the journal can tell "skipped_amend" from
// "skipped_unparseable" instead of seeing one opaque "not rewritten";
// attribution disabled reports itself as outcomeDisabled, and a command the
// backstop doesn't treat as a commit (echo "git commit", git -C x commit)
// reports outcomeNotACommit.
//
// note is non-empty when the rewrite was refused for a command that still
// appears to run `git commit` — an unparseable one containing it, or a
// stdin-message commit — and doesn't mention the trailer: bash() appends it
// to the tool result so the model can add the trailer itself.
func classifyAttribution(command string, deps ToolDeps) (rewritten, note, outcome string) {
	trailer := deps.AttributionCommit()
	if trailer == "" {
		return command, "", outcomeDisabled
	}
	if strings.Contains(command, trailer) {
		return command, "", outcomeAlreadyPresent
	}
	words, ok := splitSimpleCommand(command)
	if !ok {
		switch {
		case !gitCommitRe.MatchString(command):
			return command, "", outcomeNotACommit
		case mentionsAmend(command):
			return command, "", outcomeSkippedAmend
		default:
			return command, missingTrailerNote(trailer), outcomeSkippedUnparsed
		}
	}
	if len(words) < 2 || words[0].val != "git" || words[1].val != "commit" {
		return command, "", outcomeNotACommit
	}
	for _, w := range words[2:] {
		if w.val == "--" {
			break
		}
		if strings.Contains(w.val, trailer) {
			return command, "", outcomeAlreadyPresent
		}
		if isAmendFlag(w.val) {
			return command, "", outcomeSkippedAmend
		}
		if readsMessageFromStdin(w.val) {
			return command, missingTrailerNote(trailer), outcomeSkippedStdin
		}
	}
	at := words[1].end
	return command[:at] + " --trailer=" + quoteShellArg(trailer) + command[at:], "", outcomeAdded
}

// mentionsAmend loosely spots an --amend flag (or a git-legal abbreviation of
// it) in a command classifyAttribution could not split into words, so the
// unparseable-amend case reports skipped_amend — the journal says *why* the
// command was left alone rather than lumping it in with every other
// unparseable form. Deliberately loose: a false positive only chooses a
// different skip reason, never a rewrite.
func mentionsAmend(command string) bool {
	for _, w := range strings.Fields(command) {
		if strings.HasPrefix(w, "--") && len(w) >= len("--am") && strings.HasPrefix("--amend", w) {
			return true
		}
	}
	return false
}

// gitCommitRe loosely spots a `git commit` invocation anywhere in a command
// the backstop refused to rewrite — used to decide whether to attach the
// missing-trailer note and whether the command is worth a journal event at
// all, never to rewrite.
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

// --- Journaling the outcome (issue #146) ------------------------------------
//
// The backstop's decision is worth a durable record only if the record says
// what actually happened, so bash() writes two events around a commit it
// recognized:
//
//   - before the shell-risk gate: the INTENT (added / already_present /
//     skipped_* / disabled) with the command as observed; and
//   - after a successful run: the FACT, read back out of the repository with
//     `git log -1 --format=%H` and `--format=%B` — the resulting commit's SHA
//     and whether its message really carries the trailer.
//
// The fact is what the issue asks to measure; the intent is kept because a
// command that never ran (gate refusal, non-zero exit) leaves only an intent.
// Every write is best-effort like scan_landscape's landscape.scan: a failed
// receipt costs a data point, never the commit.
//
// AttributionJournaler is an OPTIONAL ToolDeps capability, asserted
// dynamically the way Workdirer is (workdir.go), so ToolDeps implementations
// that don't journal (headlessDeps, the test doubles) stay untouched. It is
// the seam *because* internal/tools cannot reach a session's turn ordinal or
// project root — the coordinate pair the event carries — and cmd/cortex
// cannot import internal/tools for the write path.
type AttributionJournaler interface {
	// AttributionSession identifies the agent session for an attribution
	// receipt ("" when there is none to name).
	AttributionSession() (sessionID string, turn int)
	// AttributionProject is the workspace root the receipt is scoped to
	// ("" when unknown, e.g. a commit in a scratch temp repo).
	AttributionProject() string
}

// attributionJournalerOf extracts the optional journaler from deps (nil when
// absent, which turns every attribution receipt into a no-op).
// No defensive probing here: a ToolDeps that embeds this interface to satisfy
// it without implementing it panics, which is the Go convention for an
// interface assertion and points straight at the offending type.
func attributionJournalerOf(deps ToolDeps) AttributionJournaler {
	j, ok := deps.(AttributionJournaler)
	if !ok {
		return nil
	}
	return j
}

// journalAttributionIntent appends the intent event for one classified commit
// command. Nothing is written when the command isn't a commit at all (no
// claim to make) or when deps carry no journaler.
func journalAttributionIntent(deps ToolDeps, command, outcome string) {
	j := attributionJournalerOf(deps)
	if j == nil || outcome == outcomeNotACommit {
		return
	}
	sessionID, turn := j.AttributionSession()
	_ = journal.AppendAttributionCommit(journal.AttributionCommitPayload{
		SessionID: sessionID,
		Turn:      turn,
		Project:   j.AttributionProject(),
		Outcome:   outcome,
		Command:   command,
	})
}

// journalAttributionVerified re-reads HEAD after a command that ran
// successfully and appends the fact event: the commit's SHA plus whether its
// message actually contains the trailer. It writes nothing when there is no
// journaler, no repo, and no commit (the usual case for a non-git command),
// and nothing when the command already got its verified receipt from another
// path — its SHA is in the per-deps dedupe set (attributionDedupe).
//
// intent is what the backstop decided for this same command, carried through
// so a receipt confirming an UNATTRIBUTED commit (the backstop left a pipeline
// alone and git committed anyway) doesn't claim the trailer was added.
//
// dir is where the command ran ("" = the process CWD), matching bash's own
// working-directory choice, so the verification reads the repository the
// commit actually landed in.
func journalAttributionVerified(dir, trailer, command, intent string, deps ToolDeps) {
	j := attributionJournalerOf(deps)
	if j == nil || trailer == "" {
		return
	}
	sha, body, ok := attributionHead(dir)
	if !ok {
		return // no repository, or no commit: nothing to verify against
	}
	if st := attributionDedupe(deps); st != nil {
		if st.claims(sha) {
			return // this exact commit was already reported verified
		}
		st.claim(sha)
	}
	sessionID, turn := j.AttributionSession()
	_ = journal.AppendAttributionCommit(journal.AttributionCommitPayload{
		SessionID:      sessionID,
		Turn:           turn,
		Project:        j.AttributionProject(),
		Outcome:        intent,
		Command:        command,
		SHA:            sha,
		TrailerPresent: strings.Contains(body, trailer),
		Verified:       true,
	})
}

// attributionHead returns HEAD's full commit hash and its message in dir, or
// ok=false when dir isn't a repository or has no commit yet — the usual case
// for a non-git command, which is why the verification stays silent there.
// One `git log` call carries both facts a receipt needs; a failure comes back
// as ok=false because a receipt is never worth an error the model must read.
func attributionHead(dir string) (sha, body string, ok bool) {
	cmd := exec.Command("git", "log", "-1", "--format=%H%x00%B")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", "", false
	}
	hash, message, found := strings.Cut(strings.TrimSpace(string(out)), "\x00")
	if !found || hash == "" {
		return "", "", false
	}
	return hash, message, true
}

// attributionSkipState is the per-deps dedupe set behind
// journalAttributionVerified: the commits whose fact has already been
// reported. Without it, a command that commits through a path that journals
// for itself (`cortex change commit`, run through the bash tool) would get a
// second verified receipt from bash()'s own check — the same SHA twice, which
// reads as two commits. Keyed on the deps value so each session (or test
// double) carries its own set, and lazily created: a session that never
// commits allocates nothing. See attributionSkipRegistry.
type attributionSkipState struct {
	mu    sync.Mutex
	shas  map[string]bool
	order []string
}

const attributionSkipMax = 64

// claims reports whether sha's verified receipt was already written.
func (s *attributionSkipState) claims(sha string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shas[sha]
}

// claim records sha as reported, dropping the oldest beyond attributionSkipMax
// so a long-lived session's set stays bounded (a re-seen old SHA can only
// ever mean a duplicate receipt, never a wrong one).
func (s *attributionSkipState) claim(sha string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shas == nil {
		s.shas = make(map[string]bool)
	}
	if s.shas[sha] {
		return
	}
	s.shas[sha] = true
	s.order = append(s.order, sha)
	if len(s.order) > attributionSkipMax {
		drop := s.order[0]
		s.order = s.order[1:]
		delete(s.shas, drop)
	}
}

// attributionSkipRegistry holds one skip state per ToolDeps value. ToolDeps is
// an interface, so it can't carry a field; a WeakMap is what's wanted and Go
// has none, hence this bounded map — bounded so a long-lived process that
// builds sessions per turn can't accumulate one entry per session forever.
var attributionSkipRegistry = &boundedDepsMap{cap: attributionSkipMax}

type boundedDepsMap struct {
	mu    sync.Mutex
	byDep map[ToolDeps]*attributionSkipState
	order []ToolDeps
	cap   int
}

// attributionDedupe returns deps' dedupe set, or nil when deps can't hold
// state (a nil interface value) — in which case no dedupe happens and a
// double-committed SHA may be reported twice, which is a data-quality wrinkle,
// not a wrong fact.
func attributionDedupe(deps ToolDeps) *attributionSkipState {
	if deps == nil {
		return nil
	}
	return attributionSkipRegistry.getOrCreate(deps)
}

func (m *boundedDepsMap) getOrCreate(deps ToolDeps) *attributionSkipState {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.byDep == nil {
		m.byDep = make(map[ToolDeps]*attributionSkipState)
	}
	if st, ok := m.byDep[deps]; ok {
		return st
	}
	st := &attributionSkipState{}
	m.byDep[deps] = st
	m.order = append(m.order, deps)
	if len(m.order) > m.cap {
		drop := m.order[0]
		m.order = m.order[1:]
		delete(m.byDep, drop)
	}
	return st
}
