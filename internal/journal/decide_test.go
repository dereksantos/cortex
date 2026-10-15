package journal

import (
	"slices"
	"strings"
	"testing"
)

func TestDecideShadow_RoundTrip(t *testing.T) {
	p := DecideShadowPayload{
		Session: "20261015-000000", Turn: 2, Round: 7, Point: "after_batch",
		Question: "next_move", Choices: []string{"read", "edit", "test"}, Picks: []string{"test"},
		Backend: "chat", Model: "m:free", Status: "ok", LatencyMs: 2300, PromptVersion: "sha256:abc",
		RoundsLate: 2, Actual: DecideActual{Kind: "tools", Moves: []string{"edit"}, Tools: []string{"edit_file"}},
	}
	e, err := NewDecideShadowEntry(p)
	if err != nil {
		t.Fatalf("NewDecideShadowEntry: %v", err)
	}
	if e.Type != TypeDecideShadow {
		t.Errorf("Type = %s", e.Type)
	}
	got, err := ParseDecideShadow(e)
	if err != nil {
		t.Fatalf("ParseDecideShadow: %v", err)
	}
	if got.Round != 7 || got.RoundsLate != 2 || !slices.Equal(got.Picks, []string{"test"}) || got.Actual.Kind != "tools" {
		t.Errorf("round trip lost fields: %+v", got)
	}
}

func TestDecideShadow_Validation(t *testing.T) {
	ok := DecideShadowPayload{Question: "looping", Point: "finalize", Status: "busy"}
	tests := []struct {
		name    string
		mutate  func(*DecideShadowPayload)
		wantErr string
	}{
		{"valid", func(*DecideShadowPayload) {}, ""},
		{"no question", func(p *DecideShadowPayload) { p.Question = "" }, "Question"},
		{"bad point", func(p *DecideShadowPayload) { p.Point = "midway" }, "Point"},
		{"no status", func(p *DecideShadowPayload) { p.Status = "" }, "Status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := ok
			tt.mutate(&p)
			_, err := NewDecideShadowEntry(p)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
	if _, err := ParseDecideShadow(&Entry{Type: "other"}); err == nil {
		t.Error("ParseDecideShadow accepted a foreign entry type")
	}
}
