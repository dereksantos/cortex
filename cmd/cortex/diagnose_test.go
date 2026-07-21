package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/journal"
)

func TestDiagnoseModelError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want []string // substrings the line must contain; empty slice means "no line"
	}{
		{"auth names the key", &modelCallError{Status: 401, Class: classAuth}, []string{"API key", "key_env"}},
		{"model-missing names the model and cortex model", &modelCallError{Status: 404, Class: classModelMissing, Model: "v/gone"},
			[]string{`"v/gone"`, "cortex model"}},
		{"rate-limited suggests alternatives", &modelCallError{Status: 429, Class: classRateLimited, Model: "v/busy"},
			[]string{"rate-limiting", "/model"}},
		{"server names the provider", &modelCallError{Status: 503, Class: classServer, Model: "v/x"}, []string{"5xx"}},
		{"timeout names the config knob", fmt.Errorf("model call failed after 3 attempts: %w", context.DeadlineExceeded),
			[]string{"request_timeout_sec"}},
		{"unreachable names the endpoint", errors.New("error executing agent request: dial tcp: connection refused"),
			[]string{"backend.endpoint"}},
		{"unknown class is silent", errors.New("some other failure"), []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := diagnoseModelError(tt.err)
			if len(tt.want) == 0 {
				if got != "" {
					t.Errorf("expected no diagnosis, got %q", got)
				}
				return
			}
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("diagnosis %q missing %q", got, w)
				}
			}
		})
	}
}

func TestRenderRecentModelEvents(t *testing.T) {
	dir := t.TempDir()

	// No journal at all: section is empty.
	if got := renderRecentModelEvents(dir); got != "" {
		t.Errorf("empty dir should render nothing, got %q", got)
	}

	// A substitution, a failure, then more substitutions than the cap.
	w, err := journal.NewWriter(journal.WriterOpts{ClassDir: dir, Fsync: journal.FsyncPerBatch})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	sub, _ := journal.NewModelSubstitutionEntry(journal.ModelSubstitutionPayload{
		Role: "code", Old: "a/x", New: "b/y", Reason: "mid-session heal after rate-limited: next curated pick still served"})
	if _, err := w.Append(sub); err != nil {
		t.Fatalf("append: %v", err)
	}
	fail, _ := journal.NewModelFailureEntry(journal.ModelFailurePayload{
		Role: "code", Model: "b/y", Class: "server", Status: 503})
	if _, err := w.Append(fail); err != nil {
		t.Fatalf("append: %v", err)
	}
	for i := 0; i < recentModelEventsShown+2; i++ {
		e, _ := journal.NewModelSubstitutionEntry(journal.ModelSubstitutionPayload{
			Role: "study", Old: fmt.Sprintf("old/%d", i), New: "new/z", Reason: "r"})
		if _, err := w.Append(e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	w.Close()

	got := renderRecentModelEvents(dir)
	if got == "" {
		t.Fatal("expected a rendered section")
	}
	if !strings.HasPrefix(got, "Recent model events") {
		t.Errorf("missing header: %q", got)
	}
	// Capped: only the last recentModelEventsShown event lines print.
	lines := strings.Count(strings.TrimSpace(got), "\n")
	if lines != recentModelEventsShown {
		t.Errorf("rendered %d event lines, want %d", lines, recentModelEventsShown)
	}
	// The oldest events (the a/x→b/y substitution and the failure) fell off.
	if strings.Contains(got, "a/x") {
		t.Errorf("oldest event should be capped out: %q", got)
	}
	if !strings.Contains(got, "old/6") {
		t.Errorf("newest events should render: %q", got)
	}
}

func TestRenderRecentModelEventsShowsFailures(t *testing.T) {
	dir := t.TempDir()
	w, err := journal.NewWriter(journal.WriterOpts{ClassDir: dir, Fsync: journal.FsyncPerBatch})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	fail, _ := journal.NewModelFailureEntry(journal.ModelFailurePayload{
		Role: "code", Model: "b/y", Class: "server", Status: 503})
	if _, err := w.Append(fail); err != nil {
		t.Fatalf("append: %v", err)
	}
	w.Close()

	got := renderRecentModelEvents(dir)
	if !strings.Contains(got, "FAILED unrecovered") || !strings.Contains(got, "server, 503") {
		t.Errorf("failure line malformed: %q", got)
	}
}
