// modelerr.go — typed classification of model-call failures
// (docs/model-self-healing.md §1). The ContextOverflowError pattern
// (pkg/llm/context_overflow.go) applied to the rest of the failure space:
// sendOnce returns a *modelCallError for every non-200, Send's %w wrapping
// preserves it, and classifyModelError resolves any error that crossed the
// Sender seam — typed or string-shaped (the streaming path) — to one class
// the healing ladder (heal.go) and the diagnosis line (diagnose.go) share.
// All string sniffing is confined to this file.
package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// modelErrClass is the failure class of one model call. Healable classes
// (docs/model-self-healing.md §1) are the ones a model swap can fix;
// auth/timeout/unreachable indicate the key or endpoint is the problem and
// swapping models would just thrash.
type modelErrClass string

const (
	classModelMissing modelErrClass = "model-missing" // 404, or a body naming an unknown model
	classRateLimited  modelErrClass = "rate-limited"  // 429 after transport retries
	classServer       modelErrClass = "server"        // 5xx after transport retries
	classAuth         modelErrClass = "auth"          // 401/403 — key problem, not model problem
	classTimeout      modelErrClass = "timeout"       // request deadline exceeded
	classUnreachable  modelErrClass = "unreachable"   // connection failure before any HTTP status
	classUnknown      modelErrClass = ""              // everything else (incl. context overflow — handled elsewhere)
)

// healable reports whether a model swap can plausibly fix this class.
func (c modelErrClass) healable() bool {
	switch c {
	case classModelMissing, classRateLimited, classServer:
		return true
	}
	return false
}

// modelCallError is a non-200 model-call failure with its HTTP status and
// class carried structurally. Error() reproduces the exact pre-typed message
// shape ("agent returned %d: %s") so parseCtxSize (tool_deps.go) and every
// caller that sniffs error text keeps working unchanged.
type modelCallError struct {
	Status int
	Class  modelErrClass
	Model  string
	Detail string // raw response body — full fidelity for Error(); truncate at use sites
}

func (e *modelCallError) Error() string {
	return fmt.Sprintf("agent returned %d: %s", e.Status, e.Detail)
}

// modelMissingBodyHints mark a 400-family body as "the model id itself was
// rejected" — OpenRouter and OpenAI-compatible proxies disagree on status
// (400 vs 404) but converge on phrasing.
var modelMissingBodyHints = []string{
	"not a valid model",
	"model not found",
	"no endpoints found",
	"unknown model",
	"model does not exist",
	"is not a valid model id",
}

// classifyStatus maps one HTTP status (+ body, for the 400-vs-404 ambiguity)
// to a class. Used by sendOnce at the moment the status is in hand.
func classifyStatus(status int, body string) modelErrClass {
	switch {
	case status == 401 || status == 403:
		return classAuth
	case status == 404:
		return classModelMissing
	case status == 429:
		return classRateLimited
	case status >= 500:
		return classServer
	case status == 400 || status == 422:
		lower := strings.ToLower(body)
		for _, hint := range modelMissingBodyHints {
			if strings.Contains(lower, hint) {
				return classModelMissing
			}
		}
	}
	return classUnknown
}

// classifyModelError resolves any error returned across the Sender seam to a
// class: the typed path first (errors.As through Send's wrapping), then the
// context sentinels, then bounded string sniffing for the streaming path
// (pkg/llm/stream.go's "stream status %d" / "stream: request failed" shapes)
// and the blocking transport-level shapes sendOnce wraps without a status.
// A user interrupt (context.Canceled) is never classified — healing and
// diagnosis both stay out of the way of a deliberate cancel.
func classifyModelError(err error) modelErrClass {
	if err == nil || errors.Is(err, context.Canceled) {
		return classUnknown
	}
	var mce *modelCallError
	if errors.As(err, &mce) {
		return mce.Class
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return classTimeout
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "stream status 401"), strings.Contains(msg, "stream status 403"):
		return classAuth
	case strings.Contains(msg, "stream status 404"):
		return classModelMissing
	case strings.Contains(msg, "stream status 429"):
		return classRateLimited
	case strings.Contains(msg, "stream status 5"):
		return classServer
	case strings.Contains(msg, "stream status 4"):
		// Remaining 4xx on the stream path: sniff the appended body for the
		// unknown-model phrasings, mirroring classifyStatus's 400/422 case.
		lower := strings.ToLower(msg)
		for _, hint := range modelMissingBodyHints {
			if strings.Contains(lower, hint) {
				return classModelMissing
			}
		}
		return classUnknown
	case strings.Contains(msg, "request failed"),
		strings.Contains(msg, "error executing agent request"),
		strings.Contains(msg, "connection refused"):
		return classUnreachable
	}
	return classUnknown
}

// streamStatusRe matches the status the streaming path bakes into its error
// text. pkg/llm/stream.go has two shapes:
//
//   - the JSON-error path: wrapServerError("stream (500)", body) →
//     "…: server error: …" where the prefix is the literal "stream (500)";
//   - the raw-body fallback: "stream status 500: …".
//
// The streaming path returns a PLAIN error (not a typed *modelCallError), so
// the status only lives in the message; this is how errStatus recovers it for
// the model.failure receipt (issue #117's "status and body" requirement on a
// recovered turn). The pattern matches both the paren-wrapped and the bare
// "stream status N" shapes.
var streamStatusRe = regexp.MustCompile(`stream (?:\((\d{3})\)|status (\d{3}))`)

// errStatus extracts the HTTP status carried by a model-call error: the typed
// *modelCallError first (the blocking Send path), then the streaming path's
// message-baked "stream (<status>)" shape (stream.go returns a plain error, so
// its status only lives in the text). 0 when the error never got one
// (transport-level failures, context overflow — the latter's status is its own
// concern, handled by the overflow handler, not the failure receipt).
func errStatus(err error) int {
	if err == nil {
		return 0
	}
	var mce *modelCallError
	if errors.As(err, &mce) {
		return mce.Status
	}
	if m := streamStatusRe.FindStringSubmatch(err.Error()); m != nil {
		// Two alternations, one capture each: "stream (N)" → m[1],
		// "stream status N" → m[2]. The unmatched one is "".
		for _, g := range m[1:3] {
			if g == "" {
				continue
			}
			if n, perr := strconv.Atoi(g); perr == nil {
				return n
			}
		}
	}
	return 0
}
