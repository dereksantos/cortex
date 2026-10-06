package tools

import (
	"context"
	"strings"
	"testing"

	agent "github.com/dereksantos/cortex/internal/agent"
)

// refuserGate embeds headlessDeps for every ToolDeps method the bash tool
// path touches (attribution, workdir, …) and overrides the gate seam with a
// fixed (msg, ok, outcome) answer — the deps the bash tool's GateShell
// answers with. The outcome is the gate's own structured verdict: when the
// gate refuses (ok=false) the caller supplies the ShellGateOutcome it would
// report; when it lets through (ok=true) the outcome is ShellGateClean.
type refuserGate struct {
	headlessDeps
	gateReply string
	gateOk    bool
	gateOut   ShellGateOutcome
}

func (d refuserGate) GateShell(_ context.Context, _ string) (string, bool, ShellGateOutcome) {
	if d.gateOk {
		return d.gateReply, true, ShellGateClean
	}
	out := d.gateOut
	if out == ShellGateClean {
		// A gate that refuses but did not report a specific outcome: derive
		// from the reply text, mirroring the bash tool's own derivation.
		if strings.HasPrefix(d.gateReply, "blocked") {
			out = ShellGateRefused
		} else {
			out = ShellGateBlocked
		}
	}
	return d.gateReply, false, out
}

func (refuserGate) Workdir() string { return "." }

// TestExecuteWithOutcomeBashRefusal covers the structured-outcome contract
// of ExecuteWithOutcome (issue #219): a bash call the gate refuses (or the
// user declines — the same (msg, false) shape) never ran, so its outcome is
// the refusal (never "clean"); a bash call the gate lets through runs and
// records clean; and a non-bash call is clean regardless.
func TestExecuteWithOutcomeBashRefusal(t *testing.T) {
	cases := []struct {
		name    string
		deps    ToolDeps
		command string
		wantOut ShellGateOutcome
	}{
		{
			name:    "gate refusal records refused, never clean",
			deps:    refuserGate{gateReply: "blocked (rm -rf /): destructive command", gateOk: false},
			command: `{"command":"rm -rf /"}`,
			wantOut: ShellGateRefused,
		},
		{
			name:    "declined risky command records blocked, never clean",
			deps:    refuserGate{gateReply: "declined by the user; not run.", gateOk: false},
			command: `{"command":"git push"}`,
			wantOut: ShellGateBlocked,
		},
		{
			name:    "a clean gate runs the command and records clean",
			deps:    refuserGate{gateReply: "", gateOk: true},
			command: `{"command":"echo ok"}`,
			wantOut: ShellGateClean,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call := agent.ToolCall{Function: agent.FunctionCall{Name: FunctionBash, Arguments: tc.command}}
			out, outcome, err := ExecuteWithOutcome(context.Background(), call, tc.deps)
			if err != nil {
				t.Fatalf("ExecuteWithOutcome: %v (out %q)", err, out)
			}
			if outcome != tc.wantOut {
				t.Errorf("outcome = %v, want %v (out %q)", outcome, tc.wantOut, out)
			}
		})
	}
}

// TestExecuteWithOutcomeNonBashClean pins the non-bash arm: any tool other
// than bash reports ShellGateClean — the outcome is bash-specific, and a
// consumer of a call's fate must not read it for other tools.
func TestExecuteWithOutcomeNonBashClean(t *testing.T) {
	call := agent.ToolCall{Function: agent.FunctionCall{Name: "definitely_not_a_tool", Arguments: "{}"}}
	_, outcome, _ := ExecuteWithOutcome(context.Background(), call, refuserGate{gateOk: true})
	if outcome != ShellGateClean {
		t.Errorf("non-bash outcome = %v, want ShellGateClean", outcome)
	}
}
