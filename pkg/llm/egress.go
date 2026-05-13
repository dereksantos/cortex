package llm

import (
	"fmt"
	"net/http"
	"time"
)

// egressAllowlistTransport rejects HTTP requests whose Host is not in
// the allowlist. Used to wrap the http.Client given to LLM providers
// so a compromised dep can't exfiltrate via cortex's authenticated
// connection.
//
// Host comparison is exact match against `r.URL.Host`, which includes
// the port. That intentionally blocks lookalike subdomains
// (`api.anthropic.com.attacker.example`) and arbitrary ports — a port
// is part of the trust decision.
type egressAllowlistTransport struct {
	allowed map[string]struct{}
	next    http.RoundTripper
}

// RoundTrip implements http.RoundTripper.
func (t *egressAllowlistTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	host := r.URL.Host
	if _, ok := t.allowed[host]; !ok {
		return nil, fmt.Errorf("egress blocked: host %q not in allowlist", host)
	}
	return t.next.RoundTrip(r)
}

// NewRestrictedHTTPClient returns an *http.Client that only allows
// requests to the given hosts. Use for every outbound LLM call so a
// poisoned URL can't reach a non-allowlisted endpoint.
//
// `allowedHosts` is host:port for non-default-port endpoints (e.g.,
// "127.0.0.1:11434" for Ollama) or just host for HTTPS-default-port
// services (e.g., "api.anthropic.com"). Match is exact.
func NewRestrictedHTTPClient(timeout time.Duration, allowedHosts ...string) *http.Client {
	set := make(map[string]struct{}, len(allowedHosts))
	for _, h := range allowedHosts {
		set[h] = struct{}{}
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &egressAllowlistTransport{
			allowed: set,
			next:    http.DefaultTransport,
		},
	}
}
