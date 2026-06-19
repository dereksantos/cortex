// Package shellrisk is the risk gate for the loop harness's run_shell tool.
//
// It replaces the static binary allowlist with a three-tier assessment so
// the agent can run anything that isn't risky without a hand-maintained
// whitelist:
//
//	Tier 2 — deny-floor   catastrophic, irreversible-system commands. ALWAYS
//	                      Blocked; the classifier can never override this. The
//	                      non-negotiable backstop: a fallible classifier is
//	                      never the only thing between the model and `rm -rf /`.
//	Tier 1 — safe path    read-only inspection commands (ls, cat, grep, git
//	                      status, go build …) run immediately, with NO model
//	                      call — keeps latency low and the gate un-annoying.
//	Tier 3 — gray zone    everything else goes to a small-model classifier
//	                      (see ProviderClassifier). It returns Safe or Risky,
//	                      and FAILS CLOSED to Risky on any error or unparseable
//	                      response.
//
// The order matters: the deny-floor is consulted FIRST, so a catastrophic
// command can never slip through the safe path or be waved on by the
// classifier. The classifier's verdict is only ever consulted for commands
// that already cleared the deny-floor and missed the safe path.
package shellrisk

import (
	"context"
	"regexp"
	"strings"
)

// Level is the gate's verdict for a command.
type Level int

const (
	// Safe — run without prompting.
	Safe Level = iota
	// Risky — gate per the caller's policy (prompt interactively, block when
	// headless). Reversible-but-consequential: deletes, pushes, installs,
	// network calls, edits outside the working tree.
	Risky
	// Blocked — never run, never prompt. Catastrophic / irreversible-system.
	Blocked
)

func (l Level) String() string {
	switch l {
	case Safe:
		return "safe"
	case Risky:
		return "risky"
	case Blocked:
		return "blocked"
	default:
		return "unknown"
	}
}

// Verdict is a classification result: the level, a human-readable reason
// (surfaced to the user on a prompt and to the model on a block), and the
// tier that decided it (for diagnostics + capture).
type Verdict struct {
	Level  Level
	Reason string
	Tier   string // "deny-floor" | "safe-path" | "classified" | "fail-closed"
}

// ClassifyFn is the tier-3 gray-zone classifier. It returns Safe or Risky
// (never Blocked — the deny-floor owns catastrophe) plus a reason. An error
// makes Classify fail closed to Risky. ProviderClassifier builds the
// LLM-backed implementation; tests inject their own.
type ClassifyFn func(ctx context.Context, command string) (Level, string, error)

// Classify runs the three tiers in safety order and returns the verdict.
// fn may be nil (no classifier wired) — gray-zone commands then fail closed
// to Risky so they are gated rather than silently run.
func Classify(ctx context.Context, command string, fn ClassifyFn) Verdict {
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return Verdict{Level: Blocked, Reason: "empty command", Tier: "deny-floor"}
	}

	// Tier 2 — deny-floor first: catastrophe can never be reached via the
	// safe path or the classifier.
	if reason := matchDenyFloor(cmd); reason != "" {
		return Verdict{Level: Blocked, Reason: reason, Tier: "deny-floor"}
	}

	// Tier 1 — safe path: read-only inspection, no model call.
	if reason, ok := matchSafePath(cmd); ok {
		return Verdict{Level: Safe, Reason: reason, Tier: "safe-path"}
	}

	// Tier 3 — gray zone: the small-model classifier, failing closed.
	if fn == nil {
		return Verdict{Level: Risky, Reason: "no classifier available; gated for safety", Tier: "fail-closed"}
	}
	lvl, reason, err := fn(ctx, cmd)
	if err != nil {
		return Verdict{Level: Risky, Reason: "classifier unavailable (" + err.Error() + "); gated for safety", Tier: "fail-closed"}
	}
	// The classifier may only choose between Safe and Risky. Defensive: a
	// model that emits Blocked is clamped to Risky — only the deny-floor blocks.
	if lvl != Safe {
		lvl = Risky
	}
	if strings.TrimSpace(reason) == "" {
		reason = "classified " + lvl.String()
	}
	return Verdict{Level: lvl, Reason: reason, Tier: "classified"}
}

// --- Tier 2: deny-floor ----------------------------------------------------

// denyPattern is one catastrophic-command matcher.
type denyPattern struct {
	re     *regexp.Regexp
	reason string
}

// denyFloor is the catastrophic-command set. These are irreversible or
// system-level destructive forms that must NEVER auto-run, regardless of the
// classifier. It is intentionally a backstop, not an exhaustive blocklist:
// exotic obfuscations fall through to the classifier, which fails closed to
// Risky. Patterns target the CATASTROPHIC FORM (e.g. rm -rf of a root), not
// the routine one (rm -rf ./build is gated by the classifier, not blocked).
var denyFloor = []denyPattern{
	// rm -rf targeting a filesystem root, home, or a top-level glob. Flag
	// order (-rf/-fr/-r -f/--recursive --force) and an optional
	// --no-preserve-root are all covered.
	{regexp.MustCompile(`(?i)\brm\b\s+(?:-[a-z]*r[a-z]*f[a-z]*|-[a-z]*f[a-z]*r[a-z]*|-r\s+-f|-f\s+-r|--recursive\s+--force|--force\s+--recursive)\b(?:\s+--no-preserve-root)?\s+(?:--no-preserve-root\s+)?(?:/|~|\$HOME|/\*|\.|\./)(?:\s|$)`),
		"rm -rf of a filesystem root / home / cwd"},
	// --no-preserve-root is never used legitimately by an agent.
	{regexp.MustCompile(`(?i)--no-preserve-root`), "rm --no-preserve-root"},
	// Privilege escalation.
	{regexp.MustCompile(`(?i)(^|[|&;]|\s)(sudo|doas)\s`), "privilege escalation (sudo/doas)"},
	{regexp.MustCompile(`(?i)(^|[|&;]|\s)su\s+-`), "privilege escalation (su)"},
	// Fork bomb.
	{regexp.MustCompile(`:\s*\(\s*\)\s*\{`), "fork bomb"},
	// Raw disk / device writes + filesystem creation.
	{regexp.MustCompile(`(?i)\bdd\b[^|&;]*\bof=/dev/`), "dd writing to a device"},
	{regexp.MustCompile(`(?i)\b(mkfs\S*|wipefs|fdisk|parted|sgdisk)\b`), "filesystem/partition tool"},
	{regexp.MustCompile(`(?i)>\s*/dev/(sd|nvme|disk|hd|mmcblk)`), "redirect to a block device"},
	// Download piped straight into an interpreter.
	{regexp.MustCompile(`(?i)\b(curl|wget|fetch)\b[^|]*\|\s*(?:sudo\s+)?(?:ba|z|c|k|tc|da)?sh\b`), "remote script piped to a shell"},
	{regexp.MustCompile(`(?i)\b(curl|wget|fetch)\b[^|]*\|\s*(?:sudo\s+)?(python[0-9.]*|ruby|perl|node)\b`), "remote script piped to an interpreter"},
	// Power state.
	{regexp.MustCompile(`(?i)(^|[|&;]|\s)(shutdown|reboot|halt|poweroff)\b`), "system power-state change"},
	{regexp.MustCompile(`(?i)(^|[|&;]|\s)init\s+0\b`), "system power-state change"},
	// Recursive permission/ownership changes at a root.
	{regexp.MustCompile(`(?i)\b(chmod|chown)\b\s+(-[a-z]*R[a-z]*\s+)?\S*\s*(/|~|\$HOME)(?:\s|$)`), "recursive chmod/chown at a root"},
	// Clobbering a system directory.
	{regexp.MustCompile(`(?i)>\s*/(etc|usr|bin|sbin|boot|lib|var|sys|proc)\b`), "redirect overwriting a system path"},
}

// matchDenyFloor returns the reason a command is catastrophically blocked, or
// "" if it clears the floor.
func matchDenyFloor(cmd string) string {
	for _, p := range denyFloor {
		if p.re.MatchString(cmd) {
			return p.reason
		}
	}
	return ""
}

// --- Tier 1: safe path -----------------------------------------------------

// safeBinaries are tools safe to run without a model call: read-only
// inspection tools (which neither mutate the filesystem nor reach the network)
// plus a few create-only filesystem scaffolds that cannot lose data. A command
// whose sole binary is one of these (with no shell-control characters) runs
// immediately.
//
// mv/cp/rm are deliberately ABSENT: they overwrite, relocate outside the tree,
// or delete, so they go to the classifier. mkdir/touch/rmdir stay here —
// mkdir/touch only create, and rmdir removes only an EMPTY directory (it fails
// on a non-empty one), so none can destroy existing content.
var safeBinaries = map[string]bool{
	// read-only inspection
	"ls": true, "cat": true, "head": true, "tail": true, "wc": true,
	"grep": true, "egrep": true, "fgrep": true, "rg": true, "tree": true,
	"stat": true, "file": true, "which": true, "type": true, "echo": true,
	"pwd": true, "basename": true, "dirname": true, "realpath": true,
	"readlink": true, "date": true, "whoami": true, "hostname": true,
	"uname": true, "sort": true, "uniq": true, "cut": true, "nl": true,
	"column": true, "diff": true, "cmp": true, "env": true,
	"printenv": true, "id": true, "df": true, "du": true, "true": true,
	// create-only filesystem scaffolds (no data loss possible)
	"mkdir": true, "touch": true, "rmdir": true,
}

// safeGitSub are git subcommands that only read repository state.
var safeGitSub = map[string]bool{
	"status": true, "log": true, "diff": true, "show": true, "describe": true,
	"rev-parse": true, "blame": true, "shortlog": true, "ls-files": true,
	"cat-file": true, "rev-list": true, "reflog": true, "config": false, // config can write
}

// safeGoSub are go subcommands routine for a coding agent and free of network
// or install side effects. `run`/`get`/`install`/`generate`/`mod` are absent —
// they execute arbitrary programs or touch the network, so they go to the
// classifier.
var safeGoSub = map[string]bool{
	"build": true, "test": true, "vet": true, "list": true, "doc": true,
	"fmt": true, "version": true, "env": true,
}

// findWriteFlags disqualify `find` from the safe path — they execute or delete.
var findWriteFlags = map[string]bool{
	"-exec": true, "-execdir": true, "-delete": true, "-ok": true,
	"-okdir": true, "-fprint": true, "-fprintf": true,
}

// matchSafePath reports whether cmd is a read-only inspection command that may
// run without a model call. It is conservative: any shell-control character
// (pipe, redirect, chaining, command/var substitution) disqualifies it so
// pipelines are always classified.
func matchSafePath(cmd string) (string, bool) {
	if hasShellControl(cmd) {
		return "", false
	}
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return "", false
	}
	// A leading VAR=val assignment means an environment-mutated invocation —
	// not a simple safe command.
	if strings.Contains(fields[0], "=") {
		return "", false
	}
	bin := lastPathElement(fields[0])
	switch bin {
	case "git":
		if sub, ok := firstSubcommand(fields[1:]); ok && safeGitSub[sub] {
			return "read-only git inspection", true
		}
		return "", false
	case "go":
		if sub, ok := firstSubcommand(fields[1:]); ok && safeGoSub[sub] {
			return "read-only/build go command", true
		}
		return "", false
	case "find":
		for _, a := range fields[1:] {
			if findWriteFlags[a] {
				return "", false
			}
		}
		return "read-only filesystem search", true
	default:
		if safeBinaries[bin] {
			return "read-only inspection command", true
		}
		return "", false
	}
}

// shellControl are the metacharacters that make a command more than a single
// inert invocation: pipes, redirects, chaining, command/var substitution, and
// subshells/grouping. Glob characters (* ? [ ]) and ~ are deliberately NOT
// here — they are safe for read-only tools and excluding them would push
// common commands like `ls *.go` needlessly into the classifier.
const shellControl = "|&;<>`$(){}\n"

func hasShellControl(cmd string) bool {
	return strings.ContainsAny(cmd, shellControl)
}

// firstSubcommand returns the first non-flag token (the subcommand) from a
// binary's args, skipping leading global flags like `git -C path status`.
func firstSubcommand(args []string) (string, bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "" {
			continue
		}
		if strings.HasPrefix(a, "-") {
			// `git -C <dir>` / `go -C <dir>`: the flag takes a value we must skip.
			if a == "-C" {
				i++
			}
			continue
		}
		return a, true
	}
	return "", false
}

// lastPathElement returns the binary name from a path-qualified invocation
// (/usr/bin/git → git, ./scripts/foo → foo) so the safe-path lookup matches.
func lastPathElement(s string) string {
	if i := strings.LastIndexAny(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}
