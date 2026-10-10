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
	disabled  bool // IsToolEnabled reports every tool disabled
}

func (d refuserGate) IsToolEnabled(string) bool { return !d.disabled }

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
// ran" signal is the BashOutcome Execute returns, not the absence of a
// marker in the message — a consumer must not read a refusal's message as
// a pass.
func TestExecuteBashRefusal(t *testing.T) {
	cases := []struct {
		name      string
		deps      ToolDeps
		command   string
		wantOut   string
		wantNoRun string // substring the result must NOT carry (a refusal is not a run)
		wantRan   bool   // the returned BashOutcome.Ran
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
			// A disabled tool returns its config notice cleanly (msg, nil):
			// the gate is never consulted and nothing ran — the returned
			// outcome must say so, or a consumer reads the clean result as
			// a pass.
			name:      "a disabled bash tool never runs",
			deps:      refuserGate{gateOk: true, disabled: true},
			command:   `{"command":"echo ok"}`,
			wantOut:   "bash is disabled",
			wantNoRun: "ok\n",
		},
		{
			name:    "a clean gate runs the command and reports its output",
			deps:    refuserGate{gateReply: "", gateOk: true},
			command: `{"command":"echo ok"}`,
			wantOut: "ok",
			wantRan: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call := agent.ToolCall{Function: agent.FunctionCall{Name: FunctionBash, Arguments: tc.command}}
			out, outcome, err := Execute(context.Background(), call, tc.deps)
			if err != nil {
				t.Fatalf("Execute: %v (out %q)", err, out)
			}
			if !contains(out, tc.wantOut) {
				t.Errorf("Execute result = %q, want it to contain %q", out, tc.wantOut)
			}
			if tc.wantNoRun != "" && contains(out, tc.wantNoRun) {
				t.Errorf("Execute result = %q, must not carry %q — a refused or declined command never ran", out, tc.wantNoRun)
			}
			if outcome.Ran != tc.wantRan || outcome.ExitCode != 0 {
				t.Errorf("Execute outcome = %+v, want {Ran:%v ExitCode:0}", outcome, tc.wantRan)
			}
		})
	}
}

// TestExecuteBashExitCodes pins the exit code the returned BashOutcome
// carries for a run (issue #219): the process's own non-zero code, and 1
// for a signal-killed run (exec reports -1 for it — a receipt must never
// render a killed check as `exit -1`, and never as a pass).
func TestExecuteBashExitCodes(t *testing.T) {
	cases := []struct {
		name     string
		command  string
		wantExit int
	}{
		{name: "clean exit", command: `{"command":"true"}`, wantExit: 0},
		{name: "non-zero exit keeps its code", command: `{"command":"exit 3"}`, wantExit: 3},
		{name: "signal-killed run reports 1", command: `{"command":"kill -KILL $$"}`, wantExit: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call := agent.ToolCall{Function: agent.FunctionCall{Name: FunctionBash, Arguments: tc.command}}
			out, outcome, err := Execute(context.Background(), call, refuserGate{gateOk: true})
			if err != nil {
				t.Fatalf("Execute: %v (out %q)", err, out)
			}
			if !outcome.Ran || outcome.ExitCode != tc.wantExit {
				t.Errorf("Execute outcome = %+v, want {Ran:true ExitCode:%d} (out %q)", outcome, tc.wantExit, out)
			}
		})
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
