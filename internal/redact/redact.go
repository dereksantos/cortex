// Package redact masks known secret patterns in strings before they are
// persisted to disk (session transcripts, journal entries, memory notes).
// The live context sent to the model in the current turn is NOT redacted —
// only what survives on disk (docs/journal.md, "Operational invariants").
//
// Every match is replaced with a marker of the form [REDACTED:<kind>] so a
// reader of the persisted form can tell something was removed without
// learning what it was. Redact reports the number of matches it masked so
// callers can record a redaction count per turn (issue #103).
package redact

import (
	"regexp"
)

// Kind is the classification a redaction marker carries, so a persisted
// [REDACTED:...] tells the reader WHICH class of secret was removed.
type Kind string

const (
	KindProviderKey Kind = "provider-key" // OpenAI/Anthropic sk-…, OpenRouter sk-or-…, GitHub ghp_/…/github_pat_
	KindAWSKey      Kind = "aws-key"      // AWS access key id (AKIA…) or a named secret-access-key value
	KindAssignment  Kind = "assignment"   // KEY=/TOKEN=/SECRET=/… value in a shell-style assignment
	KindPEM         Kind = "pem"          // a -----BEGIN … PRIVATE KEY----- block
)

// marker is the replacement text a whole-match pattern produces.
func marker(k Kind) string { return "[REDACTED:" + string(k) + "]" }

// RedactMarker is the common prefix of every marker, exposed for callers that
// want to detect "something was redacted here" without re-running the patterns.
const RedactMarker = "[REDACTED:"

// Re2 has no backreferences, so each pattern enumerates its three quote forms
// (unquoted / single / double) explicitly instead of capturing a quote and
// referring to it. The value is always the match's final substring, so an
// apply that masks "in place" only needs the match length.
//
// awsSecretRe finds a 40-char base64 value ONLY where an assignment names it
// as a secret (AWS_SECRET_ACCESS_KEY=…, aws_secret_access_key: …). A bare
// 40-char run of ordinary text never matches — that naming requirement is the
// false-positive guard (TestRedact_AWSKey).
var awsSecretRe = regexp.MustCompile(
	`(?i)\b(\w*(?:secret|access)\w*(?:\s*[=:]\s*|\s*is\s*))([A-Za-z0-9/+=]{40}|'[A-Za-z0-9/+=]{40}'|"[A-Za-z0-9/+=]{40}")`)

// assignmentRe finds a shell-style assignment whose VARIABLE names a secret
// (key/token/secret/passw(or)d/passphrase/api/credential/authorization). The
// value is either a quoted run of any length (quotedValueBranch), or a BARE
// run of at least 8 non-quote/non-whitespace chars whose FIRST char is in
// valueStartClasses — that length + first-char guard is what keeps
// API_MODE=fast and the like from being masked (TestRedact_Assignment_FalsePositives).
//
// The first-char class deliberately EXCLUDES `[` (the start of an already-
// applied [REDACTED:…] marker) and letters (so a value that is itself a
// prefixed key, like sk-…, is left for the more specific key patterns to
// kind). Excluding `[` is what makes Redact idempotent: a value an earlier,
// more specific pattern already masked to a bare marker is not re-matched
// (and re-kinded) by this broad class (TestRedact_Idempotent).

// valueStartClasses is the first-char class for a BARE (unquoted) assignment
// value in assignmentRe — see the comment above assignmentRe for why it
// excludes `[` (idempotency) and letters (letting prefixed keys through to
// their own patterns).
const valueStartClasses = `@#%^&*!~+./:;,-0-9_`
const quotedValueBranch = `('[^'\n]+'|"[^"\n]+")`

var assignmentRe = regexp.MustCompile(
	`(?i)\b(\w*(?:key|token|secret|passw(?:or)?d|passphrase|api|credential|authorization)\w*(?:\s*[=:]\s*|\s*is\s*))(` + quotedValueBranch + `|[` + valueStartClasses + `][^'\" \t\r\n]{7,})`)

// keepName returns an apply that masks only the VALUE of the match, in place,
// as marker(kind) — preserving the match's name prefix (variable name +
// delimiter, the pattern's first capture, always the match's leading
// substring) and DROPPING the value's quotes. So a `FOO_KEY="…"` line
// redacts to `FOO_KEY=[REDACTED:assignment]`. Dropping the quotes (rather
// than preserving them) is deliberate: a bare marker starts with `[`, which
// valueStartClasses excludes, so the broad assignment class cannot re-match
// the already-masked value on a second pass — that is what makes Redact
// idempotent (TestRedact_Idempotent) and prevents a quoted value consumed by
// an earlier, more specific pattern (e.g. awsSecretRe) from being re-kinded
// by this class (TestRedact_AWSKey).
func keepName(kind Kind, re *regexp.Regexp) func(string) string {
	mk := marker(kind)
	return func(m string) string {
		sub := re.FindStringSubmatch(m)
		if sub == nil { // defensive: the apply runs on this pattern's own matches
			return mk
		}
		return sub[1] + mk
	}
}

// pattern is one secret class: the regex to find it, and the transform to
// apply to each match.
type pattern struct {
	re    *regexp.Regexp
	apply func(m string) string
}

// patterns is the redaction vocabulary, in APPLICATION ORDER: specific key
// shapes FIRST, then the general assignment class LAST. Ordering is what
// makes each secret masked exactly once with the RIGHT kind — a provider key
// like `FOO=sk-…` is consumed by the sk- pattern before the assignment class
// could see its value (so it's masked as provider-key, not assignment), and
// the general KEY=/TOKEN=/SECRET= class only ever sees values no earlier,
// more specific pattern claimed. The assignment class is the broadest, so it
// runs last and can never re-match a span an earlier pattern already masked.
var patterns = []pattern{
	// PEM private keys (all common headers): one match per block, so the
	// whole key is removed as a single [REDACTED:pem].
	{
		re:    regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`),
		apply: func(string) string { return marker(KindPEM) },
	},
	// Provider API keys with a distinctive prefix. The longer sk-or- form is
	// listed before the generic sk- form so its intent is explicit (the
	// generic sk- regex would also match the head of an sk-or- key).
	{
		re:    regexp.MustCompile(`\bsk-or-[A-Za-z0-9_\-]{16,}\b`),
		apply: func(string) string { return marker(KindProviderKey) },
	},
	{
		re:    regexp.MustCompile(`\bsk-[A-Za-z0-9_\-]{16,}\b`),
		apply: func(string) string { return marker(KindProviderKey) },
	},
	{
		re:    regexp.MustCompile(`\b(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{16,}\b`),
		apply: func(string) string { return marker(KindProviderKey) },
	},
	{
		re:    regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{22,}\b`),
		apply: func(string) string { return marker(KindProviderKey) },
	},
	// AWS access key id (AKIA + 16 alnum).
	{
		re:    regexp.MustCompile(`\bA(KIA|SIA|ISA|AGPA|AMCA)[A-Z0-9]{16}\b`),
		apply: func(string) string { return marker(KindAWSKey) },
	},
	// AWS secret access key: a 40-char base64 value named by a secret/access
	// assignment (see awsSecretRe). The name is kept, the value masked. Runs
	// BEFORE the general assignment class so a `AWS_SECRET_ACCESS_KEY=…`
	// line is kinded aws-key, not assignment.
	{
		re:    awsSecretRe,
		apply: keepName(KindAWSKey, awsSecretRe),
	},
	// General KEY=/TOKEN=/SECRET=… assignment values (see assignmentRe).
	// Runs LAST: it is the broadest class, so it only ever claims values no
	// earlier, more specific pattern consumed.
	{
		re:    assignmentRe,
		apply: keepName(KindAssignment, assignmentRe),
	},
}

// Redact masks every known secret pattern in s, replacing each match with a
// [REDACTED:<kind>] marker, and returns the redacted string plus the number
// of matches masked. Patterns are applied once each, in `patterns` order, to
// the evolving text, so a narrower pattern cannot re-match a span an earlier,
// wider one already masked.
//
// Empty or secret-free input is returned unchanged with count 0.
func Redact(s string) (string, int) {
	if s == "" {
		return s, 0
	}
	count := 0
	for _, p := range patterns {
		next := p.re.ReplaceAllStringFunc(s, func(m string) string {
			count++
			return p.apply(m)
		})
		if next != s {
			s = next
		}
	}
	return s, count
}
