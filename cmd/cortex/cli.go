package main

import (
	"context"
	"encoding/json"
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
	out, err := tools.Execute(context.Background(), call, session)
	if err != nil {
		fmt.Println("study error:", err)
		return
	}
	fmt.Println("\n--- curated context ---")
	fmt.Println(out)
}

func runTurnCLI(args []string) {
	sessionID, asJSON, project := "", false, ""
	var rest []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--session", "-s":
			if i+1 < len(args) {
				sessionID = args[i+1]
				i++
			}
		case "--json":
			asJSON = true
		case "--project":
			if i+1 < len(args) {
				project = args[i+1]
				i++
			}
		default:
			rest = append(rest, args[i])
		}
	}

	input := strings.TrimSpace(strings.Join(rest, " "))
	if input == "" {
		if b, err := io.ReadAll(os.Stdin); err == nil {
			input = strings.TrimSpace(string(b))
		}
	}
	if input == "" {
		fmt.Fprintln(os.Stderr, "usage: cortex turn [--session <id>] [--project <name>] [--json] <input>")
		os.Exit(2)
	}

	session := NewCortexSession()
	if err := applyProjectFlag(session, project); err != nil {
		fmt.Fprintf(os.Stderr, "project %s: %v\n", project, err)
		os.Exit(1)
	}
	session.quiet = true
	if sessionID != "" {
		if err := session.ResumeTranscript(sessionID); err != nil {
			fmt.Fprintf(os.Stderr, "resume %s: %v - starting fresh\n", sessionID, err)
			session.StartTranscript()
		} else {
			session.showLoadedContext(sessionID)
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
		res, turnErr := session.Turn(ctx, input)

		if session.turns > 0 {
			session.emitSessionMetrics()
		}

		if asJSON {
			out := map[string]any{"session": session.SessionID, "reply": res.Reply}
			if turnErr != nil {
				out["error"] = turnErr.Error()
				out["interrupted"] = res.Interrupted
			}
			b, _ := json.Marshal(out)
			fmt.Println(string(b))
		} else {
			if turnErr != nil {
				fmt.Fprintf(os.Stderr, "turn error: %v\n", turnErr)
			}
			if res.Reply != "" {
				fmt.Println(res.Reply)
			}
			fmt.Fprintf(os.Stderr, "session: %s\n", session.SessionID)
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
