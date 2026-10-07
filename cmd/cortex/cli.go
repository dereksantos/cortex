package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/dereksantos/cortex/internal/tools"
)

func compactNow(session *CortexSession, reason string) {
	fmt.Println(withColor(reason+" - compacting via study...", yellow))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := session.Compact(ctx); err != nil {
		fmt.Printf("compact: %v\n", err)
		return
	}
	fmt.Println(withColor("compacted -> session "+session.SessionID, gray))
}

// runStudyCLI runs one-off study. project, when non-empty, resolves via the
// registry and re-targets the session at that project's root before
// studying (M3.5) — otherwise study runs against the CWD-implicit workspace
// exactly as before.
func runStudyCLI(project, path, goal string) {
	session := NewCortexSession()
	if err := applyProjectFlag(session, project); err != nil {
		fmt.Println("study error:", err)
		return
	}
	args, _ := json.Marshal(map[string]any{"path": path, "goal": goal})
	call := ToolCall{Function: FunctionCall{Name: FunctionStudy, Arguments: string(args)}}
	out, _, err := tools.Execute(context.Background(), call, session)
	if err != nil {
		fmt.Println("study error:", err)
		return
	}
	fmt.Println("\n--- curated context ---")
	fmt.Println(out)
}

// turnArgs is the parsed form of a `cortex turn` invocation (runTurnCLI).
// Extracted from the flag loop so the parsing — including the new --plan
// switch (#150) — is testable in isolation without driving a live session.
type turnArgs struct {
	sessionID string // --session / -s
	asJSON    bool   // --json
	plan      bool   // --plan: run the plan-then-execute path instead of one turn
	project   string // --project
	input     string // the joined positional input
}

// parseTurnArgs parses the arguments of `cortex turn` into turnArgs. It is
// pure (no session, no I/O) so the flag handling — especially the new
// --plan flag — is covered by a table-driven test. Unknown flags fall
// through to the positional input, matching the historical behavior that
// only --session/-s, --project, --json and (now) --plan are consumed.
func parseTurnArgs(args []string) turnArgs {
	var a turnArgs
	var rest []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--session", "-s":
			if i+1 < len(args) {
				a.sessionID = args[i+1]
				i++
			}
		case "--json":
			a.asJSON = true
		case "--plan":
			a.plan = true
		case "--project":
			if i+1 < len(args) {
				a.project = args[i+1]
				i++
			}
		default:
			rest = append(rest, args[i])
		}
	}
	a.input = strings.TrimSpace(strings.Join(rest, " "))
	return a
}

func runTurnCLI(args []string) {
	a := parseTurnArgs(args)

	if a.input == "" {
		if b, err := io.ReadAll(os.Stdin); err == nil {
			a.input = strings.TrimSpace(string(b))
		}
	}
	if a.input == "" {
		fmt.Fprintln(os.Stderr, "usage: cortex turn [--session <id>] [--project <name>] [--plan] [--json] <input>")
		os.Exit(2)
	}

	session := NewCortexSession()
	if err := applyProjectFlag(session, a.project); err != nil {
		fmt.Fprintf(os.Stderr, "project %s: %v\n", a.project, err)
		os.Exit(1)
	}
	session.quiet = true
	if a.sessionID != "" {
		if err := session.ResumeTranscript(a.sessionID); err != nil {
			fmt.Fprintf(os.Stderr, "resume %s: %v - starting fresh\n", a.sessionID, err)
			session.StartTranscript()
		} else {
			// Issue #118: the resume banner is a headless diagnostic —
			// stderr, plain (no ANSI), never stdout. The colored REPL
			// form stays showLoadedContext.
			session.headlessLoadedContextBanner(a.sessionID)
		}
	} else {
		session.StartTranscript()
	}
	session.EnableMemory()

	// Run the turn (and its deferred cleanup: session.Close, stop) inside a
	// closure so those defers fire before we exit — os.Exit does not run
	// deferred calls, so it must happen outside their scope.
	exitCode := func() int {
		defer session.Close()

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()

		// --plan (#150): the plan-then-execute path. Each planned step is its
		// own turn with the project's checks in between; the reply is the
		// per-step report. Without --plan the pre-existing single-turn path
		// runs, unchanged.
		var res TurnResult
		var turnErr error
		if a.plan {
			plan, planErr := session.TurnWithPlan(ctx, a.input)
			// TestReceipt (issue #141): the plan's steps' receipts, so the
			// stderr / --json "tests_changed" surfaces below cover --plan too.
			// LintReceipt (issue #129 piece 3) and Receipt (issue #219) ride
			// the same mapping, so the non-JSON stderr and --json surfaces
			// cover --plan exactly like a single turn.
			res = TurnResult{Reply: plan.Reply, Interrupted: errors.Is(planErr, context.Canceled), TestReceipt: plan.TestReceipt, LintReceipt: plan.LintReceipt, Receipt: plan.Receipt}
			turnErr = planErr
		} else {
			res, turnErr = session.Turn(ctx, a.input)
		}

		if session.turns > 0 {
			session.emitSessionMetrics()
		}

		if a.asJSON {
			out := map[string]any{"session": session.SessionID, "reply": res.Reply}
			if res.TestReceipt != "" {
				out["tests_changed"] = res.TestReceipt
			}
			if res.LintReceipt != "" {
				out["lint"] = res.LintReceipt
			}
			// Issue #219: the measurement-only turn receipt rides --json under
			// "receipt" — the driver sees what the turn actually left in the
			// workspace, measured, not claimed.
			if res.Receipt != "" {
				out["receipt"] = res.Receipt
			}
			// Issue #117: a turn that recovered from a mid-turn provider
			// failure succeeded, so err is nil and the failure was otherwise
			// lost behind the finalize answer — carry it under "backend_error"
			// so a driver parsing --json sees what the backend said (sibling
			// of tests_changed above).
			if res.LastError != nil {
				out["backend_error"] = backendErrorLine(res.LastError)
			}
			// Issue #103: the per-turn secret-redaction count rides --json
			// ("redactions") so a driver parsing the output sees how much was
			// masked before the turn's text hit disk, like tests_changed above.
			if res.Redactions > 0 {
				out["redactions"] = res.Redactions
			}
			// Issue #230: a final reply the engine's sanitizer could not
			// repair (empty, tool-call markup, or truncated) is flagged so a
			// driver that feeds the reply into a commit message can check for
			// it before doing so.
			if res.SummaryIssue != "" {
				out["summary_issue"] = res.SummaryIssue
			}
			if turnErr != nil {
				out["error"] = turnErr.Error()
				out["interrupted"] = res.Interrupted
			}
			b, _ := json.Marshal(out)
			fmt.Println(string(b))
		} else {
			reportTurnText(os.Stdout, os.Stderr, turnErr, res, session.SessionID)
		}
		if turnErr != nil {
			return 1
		}
		return 0
	}()
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

// reportTurnText is runTurnCLI's non-JSON reporting: it splits the turn's
// output between the two headless streams so issue #118's answer-only stdout
// contract holds — stdout carries ONLY the reply (or nothing, on an error-
// only turn), while every harness-level fact goes to stderr: the turn error
// (and its diagnosis), the test-removal receipt (issue #141), the turn-end
// lint receipt (issue #129 piece 3), the recovered backend-error notice
// (issue #117), and the secret-redaction notice (issue #103 — dim provenance,
// never an alarm; the --json path carries the same count under
// "redactions"). runTurnCLI calls it with os.Stdout/os.Stderr; the writers are
// parameters so the contract is testable with buffers (the test in
// session_runtime_test.go drives this helper, not a copy of its logic).
func reportTurnText(stdout, stderr io.Writer, turnErr error, res TurnResult, sessionID string) {
	if turnErr != nil {
		fmt.Fprintf(stderr, "turn error: %v\n", turnErr)
		if d := diagnoseModelError(turnErr); d != "" {
			fmt.Fprintln(stderr, d)
		}
	}
	if res.Reply != "" {
		fmt.Fprintln(stdout, res.Reply)
	}
	if res.TestReceipt != "" {
		fmt.Fprintln(stderr, res.TestReceipt)
	}
	if res.LintReceipt != "" {
		fmt.Fprintln(stderr, res.LintReceipt)
	}
	// Issue #219: the measurement-only turn receipt goes to stderr too —
	// headless stdout is the answer only (issue #118's contract), and the
	// receipt is a harness-level fact like the lint receipt above.
	if res.Receipt != "" {
		fmt.Fprintln(stderr, res.Receipt)
	}
	if res.LastError != nil {
		fmt.Fprintln(stderr, backendErrorLine(res.LastError))
	}
	// Issue #230: a final reply the engine's sanitizer could not repair
	// (empty, tool-call markup, or truncated) is flagged on stderr — a
	// driver that feeds the reply into a commit message can check for it.
	if res.SummaryIssue != "" {
		fmt.Fprintf(stderr, "summary issue: %s\n", res.SummaryIssue)
	}
	printRedactions(stderr, res.Redactions)
	fmt.Fprintf(stderr, "session: %s\n", sessionID)
}

// backendErrorLine is the one-line, secrets-redacted human-facing notice for a
// provider error a turn recovered from (issue #117). Shared by the two human
// surfaces so the CLI's stderr line and the REPL's dim notice stay word-for-
// word identical: the same "backend error: <status> <message>" text. The
// status is errStatus's extraction (the typed blocking path or the streaming
// path's message-baked "stream (503)"); the message is redactSecrets'd so a
// 400 rejection that echoed the Authorization header can't leak the key.
//
// When errStatus returns 0 (a transport-level failure with no HTTP status),
// the line is just the message — no redundant "0" prefix.
func backendErrorLine(err error) string {
	if err == nil {
		return ""
	}
	msg := redactSecrets(err.Error())
	if s := errStatus(err); s > 0 {
		return fmt.Sprintf("backend error: %d %s", s, msg)
	}
	return "backend error: " + msg
}
