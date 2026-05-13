package llm

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

// TestEgressAllowlistTransport asserts that the transport refuses any
// request whose Host is not in the allowlist. This is the backstop
// against a compromised LLM-side dep trying to exfiltrate data through
// the http.Client we hand it: even with a poisoned URL, the request
// never leaves the process.
func TestEgressAllowlistTransport(t *testing.T) {
	// A noop "underlying" transport that records what was passed
	// through. RoundTrip returns an err to short-circuit before
	// network — we only care that the request reached this layer.
	var passedHost string
	underlying := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		passedHost = r.URL.Host
		return nil, errors.New("synthetic-underlying")
	})

	rt := &egressAllowlistTransport{
		allowed: map[string]struct{}{
			"api.anthropic.com": {},
			"127.0.0.1:11434":   {},
		},
		next: underlying,
	}

	t.Run("allowed host passes through", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "https://api.anthropic.com/v1/messages", nil)
		_, err := rt.RoundTrip(req)
		if err == nil || err.Error() != "synthetic-underlying" {
			t.Errorf("expected passthrough to underlying, got: %v", err)
		}
		if passedHost != "api.anthropic.com" {
			t.Errorf("expected request reached underlying with host api.anthropic.com, got %q", passedHost)
		}
	})

	t.Run("disallowed host is blocked", func(t *testing.T) {
		passedHost = ""
		req, _ := http.NewRequest("GET", "https://evil.example.com/exfil", nil)
		_, err := rt.RoundTrip(req)
		if err == nil {
			t.Fatal("expected error for disallowed host")
		}
		if !strings.Contains(err.Error(), "evil.example.com") {
			t.Errorf("error should name the blocked host; got %q", err.Error())
		}
		if passedHost != "" {
			t.Errorf("request reached underlying despite block: %q", passedHost)
		}
	})

	t.Run("ollama on loopback with port is allowed", func(t *testing.T) {
		passedHost = ""
		req, _ := http.NewRequest("GET", "http://127.0.0.1:11434/api/tags", nil)
		_, err := rt.RoundTrip(req)
		if err == nil || err.Error() != "synthetic-underlying" {
			t.Errorf("expected passthrough, got %v", err)
		}
	})

	t.Run("subdomain not in allowlist is blocked", func(t *testing.T) {
		// Sometimes attackers register lookalike subdomains: api.anthropic.com.attacker.example
		req, _ := http.NewRequest("GET", "https://api.anthropic.com.attacker.example/v1/messages", nil)
		_, err := rt.RoundTrip(req)
		if err == nil {
			t.Fatal("expected error for non-exact host match")
		}
	})
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
