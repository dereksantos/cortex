// redact.go — best-effort secret redaction for provider errors that land in
// durable surfaces (the model.failure journal entry and the one-line log the
// issue #117 fix writes). A mid-turn provider error's body is usually benign,
// but the failure class that triggered this work (a 400 rejecting the request)
// can carry whatever the request carried — an Authorization header echoed back
// in an error body is the classic leak. redactSecrets scrubs the two shapes
// that matter: an explicit Bearer/<token> header value, and an OpenAI-style
// "sk-..." key. It is NOT a sandbox: it catches the common cases so the
// journal stays grep-safe, not a guarantee against every conceivable body.
//
// Mirrors the best-effort posture of the model.failure write itself (heal.go's
// journalModelFailure): a redaction miss is a log-quality problem, not an
// engine error, so it never propagates.
package main

import (
	"regexp"
)

// bearerRE matches a "Bearer <token>" header value (case-insensitive on the
// keyword, whitespace-tolerant) and replaces the token with a redaction marker
// that keeps the shape recognizable ("Bearer sk-***") without exposing the key.
var bearerRE = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._\-]{8,}`)

// skKeyRE matches a raw OpenAI-style API key ("sk-..." followed by ≥8
// alphanumerics/dashes/underscores). The leading "sk-" is required so a
// legitimate "task-" or "risk-" token in a tool body isn't scrubbed.
var skKeyRE = regexp.MustCompile(`\bsk-[A-Za-z0-9._\-]{8,}`)

// redactMarker is the placeholder for a redacted secret. It keeps the prefix
// visible ("sk-") so a reader sees WHAT was redacted, not just a hole.
const redactMarker = "sk-REDACTED"

// redactSecrets scrubs bearer tokens and raw sk- keys from s. Both patterns
// are replaced with redactMarker; a no-op returns s unchanged.
func redactSecrets(s string) string {
	if s == "" {
		return s
	}
	if bearerRE.MatchString(s) {
		s = bearerRE.ReplaceAllString(s, "Bearer "+redactMarker)
	}
	if skKeyRE.MatchString(s) {
		s = skKeyRE.ReplaceAllString(s, redactMarker)
	}
	return s
}
