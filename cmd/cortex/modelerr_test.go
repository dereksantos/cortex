package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestClassifyStatus(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   modelErrClass
	}{
		{"401 is auth", 401, `{"error":"invalid key"}`, classAuth},
		{"403 is auth", 403, "", classAuth},
		{"404 is model-missing", 404, "", classModelMissing},
		{"429 is rate-limited", 429, `{"error":{"message":"rate limit exceeded"}}`, classRateLimited},
		{"500 is server", 500, "", classServer},
		{"503 is server", 503, "", classServer},
		{"400 naming an invalid model is model-missing", 400,
			`{"error":{"message":"qwen/nope is not a valid model ID"}}`, classModelMissing},
		{"404 with OpenRouter no-endpoints body is model-missing", 404,
			`{"error":{"message":"No endpoints found for qwen/nope"}}`, classModelMissing},
		{"422 unknown model is model-missing", 422,
			`{"error":"Unknown model: whatever"}`, classModelMissing},
		{"400 context overflow is unclassified", 400,
			`{"error":{"message":"This model's maximum context length is 32768 tokens"}}`, classUnknown},
		{"200 never reaches classification but classifies unknown", 200, "", classUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyStatus(tt.status, tt.body); got != tt.want {
				t.Errorf("classifyStatus(%d) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

func TestModelCallErrorMessageShapePreserved(t *testing.T) {
	// parseCtxSize and other text-sniffing callers depend on the historical
	// "agent returned %d: %s" shape.
	err := &modelCallError{Status: 404, Class: classModelMissing, Model: "m", Detail: `{"x":1}`}
	want := `agent returned 404: {"x":1}`
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestClassifyModelError(t *testing.T) {
	typed := &modelCallError{Status: 429, Class: classRateLimited, Model: "m", Detail: "slow down"}
	wrapped := fmt.Errorf("model call failed after 3 attempts: %w", typed)

	tests := []struct {
		name string
		err  error
		want modelErrClass
	}{
		{"nil", nil, classUnknown},
		{"typed error direct", typed, classRateLimited},
		{"typed error survives Send's wrapping", wrapped, classRateLimited},
		{"user cancel is never classified", context.Canceled, classUnknown},
		{"wrapped cancel is never classified", fmt.Errorf("send: %w", context.Canceled), classUnknown},
		{"deadline is timeout", fmt.Errorf("send: %w", context.DeadlineExceeded), classTimeout},
		{"stream 401", errors.New("stream status 401: unauthorized"), classAuth},
		{"stream 404", errors.New("stream status 404: nope"), classModelMissing},
		{"stream 429", errors.New("stream status 429: slow down"), classRateLimited},
		{"stream 5xx", errors.New("stream status 502: bad gateway"), classServer},
		{"stream 400 naming a bad model", errors.New(`stream status 400: {"error":"foo is not a valid model ID"}`), classModelMissing},
		{"stream 400 otherwise unknown", errors.New("stream status 400: bad request"), classUnknown},
		{"stream transport failure", errors.New("stream: request failed: dial tcp: connect: connection refused"), classUnreachable},
		{"blocking transport failure", errors.New(`error executing agent request: Post "http://x": EOF`), classUnreachable},
		{"anything else", errors.New("some other failure"), classUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyModelError(tt.err); got != tt.want {
				t.Errorf("classifyModelError(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

func TestHealableClasses(t *testing.T) {
	healable := []modelErrClass{classModelMissing, classRateLimited, classServer}
	for _, c := range healable {
		if !c.healable() {
			t.Errorf("%q should be healable", c)
		}
	}
	notHealable := []modelErrClass{classAuth, classTimeout, classUnreachable, classUnknown}
	for _, c := range notHealable {
		if c.healable() {
			t.Errorf("%q should NOT be healable", c)
		}
	}
}

func TestErrStatus(t *testing.T) {
	typed := &modelCallError{Status: 503, Class: classServer}
	if got := errStatus(fmt.Errorf("wrap: %w", typed)); got != 503 {
		t.Errorf("errStatus = %d, want 503", got)
	}
	if got := errStatus(errors.New("untyped")); got != 0 {
		t.Errorf("errStatus(untyped) = %d, want 0", got)
	}
}
