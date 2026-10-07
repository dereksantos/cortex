package tools

import (
	"context"
	"strings"
	"testing"

	agent "github.com/dereksantos/cortex/internal/agent"
)

// refuserGate embeds headlessDeps for every ToolDeps method the bash tool
// path touches (attribution, workdir, …) and overrides the gate seam with a
// fixed (msg, ok) answer — the deps the bash tool's GateShell answers with.
type refuserGate struct {
	headlessDeps
	gateReply string
	gateOk    bool
}

func (d refuserGate) GateShell(_ context.Context, _ string) (string, bool) {
	return d.gateReply, d.gateOk
}

func (refuserGate) Workdir() string { return "." }

// TestExecuteBashRefusal covers the bash tool's refusal contract (issue
// #219): a bash call the gate refuses (or the user declines — the same
// (msg, false) shape) never ran: the process is never spawned (the gate is
// the sole spawn point in the bash tool), so the result is the refusal
// text, not a run, and no "[exit error: …]" marker is appended; a bash call
// the gate lets through runs and reports its output. The structured "it
// ran" signal is the session's BashOutcomes capability (the gate answer it
// records), not the absence of a marker in the message — a test stub like
// this one that answers (msg, false) with no session records nothing at
// all, and a consumer must not read a refusal's message as a pass.
func TestExecuteBashRefusal(t *testing.T) {
	cases := []struct {
		name      string
		deps      ToolDeps
		command   string
		wantOut   string
		wantNoRun string // substring the result must NOT carry (a refusal is not a run)
	}{
		{
			name:      "gate refusal is a refusal text, never a run",
			deps:      refuserGate{gateReply: "blocked (rm -rf /): destructive command", gateOk: false},
			command:   `{"command":"rm -rf /"}`,
			wantOut:   "blocked (rm -rf /): destructive command",
			wantNoRun: "[exit error:",
		},
		{
			name:      "declined risky command is the decline, never a run",
			deps:      refuserGate{gateReply: "declined by the user; not run.", gateOk: false},
			command:   `{"command":"git push"}`,
			wantOut:   "declined by the user; not run.",
			wantNoRun: "[exit error:",
		},
		{
			name:    "a clean gate runs the command and reports its output",
			deps:    refuserGate{gateReply: "", gateOk: true},
			command: `{"command":"echo ok"}`,
			wantOut: "ok",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call := agent.ToolCall{Function: agent.FunctionCall{Name: FunctionBash, Arguments: tc.command}}
			out, _, err := Execute(context.Background(), call, tc.deps)
			if err != nil {
				t.Fatalf("Execute: %v (out %q)", err, out)
			}
			if !contains(out, tc.wantOut) {
				t.Errorf("Execute result = %q, want it to contain %q", out, tc.wantOut)
			}
			if tc.wantNoRun != "" && contains(out, tc.wantNoRun) {
				t.Errorf("Execute result = %q, must not carry %q — a refused or declined command never ran", out, tc.wantNoRun)
			}
		})
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
