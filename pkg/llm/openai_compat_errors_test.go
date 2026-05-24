package llm

import (
	"strings"
	"testing"
)

// TestWrapServerError_BackendUnreachable pins the cleanup of the
// triple-nested "<endpoint>: server error: Network error: CURL error:
// Could not connect to server" message lemonade emits when its
// underlying llama-server is down. The user-facing string should name
// the actual cause (backend down) and suggest a remediation.
func TestWrapServerError_BackendUnreachable(t *testing.T) {
	cases := []string{
		"Network error: CURL error: Could not connect to server",
		"network error: curl error: could not connect to server",
		"connection refused",
		"Connection refused on port 8080",
	}
	for _, msg := range cases {
		err := wrapServerError("chatterbox", msg)
		got := err.Error()
		if strings.Contains(got, "server error:") {
			t.Errorf("backend-unreachable case should NOT use the generic 'server error:' wrapper; got %q", got)
		}
		if !strings.Contains(got, "backend unreachable") {
			t.Errorf("expected 'backend unreachable' phrasing for %q; got %q", msg, got)
		}
		if !strings.Contains(got, "chatterbox") {
			t.Errorf("expected endpoint name in error for %q; got %q", msg, got)
		}
	}
}

// TestWrapServerError_GenericPathUnchanged pins that non-backend-down
// messages still flow through the generic "server error: <msg>" path.
func TestWrapServerError_GenericPathUnchanged(t *testing.T) {
	err := wrapServerError("chatterbox", "model not found")
	got := err.Error()
	if !strings.Contains(got, "server error: model not found") {
		t.Errorf("expected generic wrapper to preserve message; got %q", got)
	}
}
