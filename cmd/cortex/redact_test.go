// redact_test.go — the unit half of the issue #117 redaction: redactSecrets
// scrubs bearer tokens and raw sk- keys from provider-error text (the surface
// a model.failure journal entry and the "backend error" log line both pass
// through), and leaves everything else byte-for-byte. Table-driven per the
// project's test conventions (stdlib only, t.Run subtests).
package main

import (
	"fmt"
	"strings"
	"testing"
)

// testKey is a realistic OpenAI-style key the redaction must never let survive.
const testKey = "sk-or-v1-1234567890abcdef"

func TestRedactSecrets(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // exact expected output ("" = check only the substring guards)
	}{
		{
			name: "bare bearer token",
			in:   `agent returned 400: {"error":{"message":"invalid token Bearer ` + testKey + `"}}`,
			want: `agent returned 400: {"error":{"message":"invalid token Bearer sk-REDACTED"}}`,
		},
		{
			name: "bearer with non-sk token",
			in:   `auth header Bearer mysecrettoken123 leaked in body`,
			want: `auth header Bearer sk-REDACTED leaked in body`,
		},
		{
			name: "raw sk- key in body",
			in:   `request failed: key ` + testKey + ` was rejected`,
			want: `request failed: key sk-REDACTED was rejected`,
		},
		{
			name: "both bearer and raw sk- in one body",
			in:   `Bearer ` + testKey + ` and sk-abcdef1234567890`,
			want: `Bearer sk-REDACTED and sk-REDACTED`,
		},
		{
			name: "short bearer value is not a token",
			in:   `Bearer abc`, // < 8 chars after Bearer → not scrubbed
			want: `Bearer abc`,
		},
		{
			name: "no secrets unchanged",
			in:   `server error: internal error 500`,
			want: `server error: internal error 500`,
		},
		{
			name: "empty string unchanged",
			in:   "",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactSecrets(tc.in)
			if got != tc.want {
				t.Errorf("redactSecrets(%q) = %q, want %q", tc.in, got, tc.want)
			}
			// The invariant that matters for #117: a real sk- key must NEVER
			// survive, regardless of the shape it was embedded in.
			if strings.Contains(got, testKey) {
				t.Errorf("redactSecrets(%q) = %q leaked the raw key", tc.in, got)
			}
		})
	}
}

// TestBackendErrorLine locks the one-line human-facing notice (issue #117):
// "backend error: <status> <message>", message redacted, and — when the error
// has no HTTP status (transport-level) — no redundant "0" prefix.
func TestBackendErrorLine(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "streaming 503 with bearer key",
			err:  fmt.Errorf("stream (503): server error: auth Bearer " + testKey + " leaked"),
			want: "backend error: 503 stream (503): server error: auth Bearer sk-REDACTED leaked",
		},
		{
			name: "blocking 400 typed error (the real Send path)",
			err:  &modelCallError{Status: 400, Class: classUnknown, Model: "m", Detail: "bad request"},
			want: "backend error: 400 agent returned 400: bad request",
		},
		{
			name: "no status (transport-level) — no 0 prefix",
			err:  fmt.Errorf("connection refused"),
			want: "backend error: connection refused",
		},
		{
			name: "nil — empty",
			err:  nil,
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := backendErrorLine(tc.err)
			if got != tc.want {
				t.Errorf("backendErrorLine = %q, want %q", got, tc.want)
			}
			// The key must never survive to the human-facing line.
			if strings.Contains(got, testKey) {
				t.Errorf("backendErrorLine(%v) = %q leaked the key", tc.err, got)
			}
		})
	}
}
