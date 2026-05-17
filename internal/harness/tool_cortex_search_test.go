package harness

import (
	"testing"

	"github.com/dereksantos/cortex/pkg/cognition"
)

// TestResolveRetrieveMode_EnvVar covers the ABR adapter's hook: when
// the agent doesn't specify mode, CORTEX_SEARCH_DEFAULT_MODE picks it.
// Explicit args always win — that's how runtime callers can opt out of
// the benchmark default without code changes.
func TestResolveRetrieveMode_EnvVar(t *testing.T) {
	t.Setenv(CortexSearchDefaultModeEnv, "full")

	t.Run("empty falls through to env default", func(t *testing.T) {
		mode, degraded, err := resolveRetrieveMode("", true)
		if err != nil || mode != cognition.Full || degraded {
			t.Fatalf("want Full,nil; got mode=%v degraded=%v err=%v", mode, degraded, err)
		}
	})

	t.Run("explicit fast overrides env", func(t *testing.T) {
		mode, _, err := resolveRetrieveMode("fast", true)
		if err != nil || mode != cognition.Fast {
			t.Fatalf("want Fast,nil; got mode=%v err=%v", mode, err)
		}
	})

	t.Run("env full degrades without provider", func(t *testing.T) {
		mode, degraded, err := resolveRetrieveMode("", false)
		if err != nil || mode != cognition.Fast || !degraded {
			t.Fatalf("want Fast,degraded; got mode=%v degraded=%v err=%v", mode, degraded, err)
		}
	})

	t.Run("garbage env value falls through to fast", func(t *testing.T) {
		t.Setenv(CortexSearchDefaultModeEnv, "deep")
		mode, _, err := resolveRetrieveMode("", true)
		if err != nil || mode != cognition.Fast {
			t.Fatalf("want Fast,nil; got mode=%v err=%v", mode, err)
		}
	})
}

// TestResolveRetrieveMode covers the three live paths through mode
// parsing: empty/fast → Fast, full with provider → Full, full without
// provider → Fast + degraded flag, unknown → error.
//
// The error path matters: callers (especially LLM-generated tool args)
// should see a typo immediately rather than silently get Fast and
// produce a misleading ABR cell.
func TestResolveRetrieveMode(t *testing.T) {
	tests := []struct {
		name         string
		requested    string
		haveProvider bool
		wantMode     cognition.RetrieveMode
		wantDegrade  bool
		wantErr      bool
	}{
		{name: "empty defaults to fast", requested: "", haveProvider: true, wantMode: cognition.Fast},
		{name: "explicit fast", requested: "fast", haveProvider: true, wantMode: cognition.Fast},
		{name: "fast is case insensitive", requested: "FAST", haveProvider: true, wantMode: cognition.Fast},
		{name: "full with provider", requested: "full", haveProvider: true, wantMode: cognition.Full},
		{name: "full without provider degrades", requested: "full", haveProvider: false, wantMode: cognition.Fast, wantDegrade: true},
		{name: "full is case insensitive", requested: "Full", haveProvider: true, wantMode: cognition.Full},
		{name: "whitespace trimmed", requested: "  full  ", haveProvider: true, wantMode: cognition.Full},
		{name: "unknown is rejected", requested: "deep", haveProvider: true, wantErr: true},
		{name: "fast doesnt degrade when no provider", requested: "fast", haveProvider: false, wantMode: cognition.Fast},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode, degraded, err := resolveRetrieveMode(tt.requested, tt.haveProvider)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got mode=%v degraded=%v", mode, degraded)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if mode != tt.wantMode {
				t.Errorf("mode: want %v, got %v", tt.wantMode, mode)
			}
			if degraded != tt.wantDegrade {
				t.Errorf("degraded: want %v, got %v", tt.wantDegrade, degraded)
			}
		})
	}
}

// TestModeString round-trips the enum to the JSON-friendly string so
// the cell's `mode` field is grep/jq-stable.
func TestModeString(t *testing.T) {
	if got := modeString(cognition.Fast); got != "fast" {
		t.Errorf("Fast: want fast, got %q", got)
	}
	if got := modeString(cognition.Full); got != "full" {
		t.Errorf("Full: want full, got %q", got)
	}
}
