// diagnose.go — the user-facing half of model self-healing
// (docs/model-self-healing.md §4): turn a classified model-call failure into
// one plain line saying what failed and the one thing to change, and let
// `cortex model` show the recent substitution/failure receipts so "what
// churned and why" is answerable after the fact.
package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/dereksantos/cortex/internal/journal"
)

// diagnoseModelError returns a one-line explanation + fix hint for a failed
// model call, or "" when classification has nothing useful to add (unknown
// class, user cancel, context overflow — the latter has its own handler).
// Printed AFTER the raw error, so the raw text stays available and greppable.
func diagnoseModelError(err error) string {
	class := classifyModelError(err)
	model := ""
	var mce *modelCallError
	if errors.As(err, &mce) {
		model = mce.Model
	}
	name := "the model"
	if model != "" {
		name = fmt.Sprintf("model %q", model)
	}
	switch class {
	case classAuth:
		return "the backend rejected the API key — check the key behind backend.key_env (or key_service) in .cortex/config.json"
	case classModelMissing:
		return fmt.Sprintf("%s was rejected as unknown by the backend — update models.* in .cortex/config.json, or run 'cortex model' to see what is served and what recently substituted", name)
	case classRateLimited:
		return fmt.Sprintf("%s is rate-limiting after retries — wait a moment, switch models with /model, or run 'cortex model' for alternatives", name)
	case classServer:
		return fmt.Sprintf("the backend kept failing (5xx) on %s — the provider is having trouble; retry, or switch models with /model", name)
	case classTimeout:
		return "the request hit its deadline — raise models.<role>.request_timeout_sec in .cortex/config.json, or check that the endpoint is healthy"
	case classUnreachable:
		return "could not reach the backend at all — check backend.endpoint in .cortex/config.json and that the server is running"
	}
	return ""
}

// recentModelEventsShown caps `cortex model`'s receipts section.
const recentModelEventsShown = 5

// renderRecentModelEvents renders the last few model.substitution /
// model.failure receipts from the project's model journal class, or ""
// when there are none (or no journal exists) — the section simply doesn't
// print. Best-effort by design: a malformed entry is skipped, never fatal.
func renderRecentModelEvents(journalDir string) string {
	r, err := journal.NewReader(journalDir)
	if err != nil {
		return ""
	}
	defer r.Close()

	var lines []string
	for {
		e, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		switch e.Type {
		case journal.TypeModelSubstitution:
			p, perr := journal.ParseModelSubstitution(e)
			if perr != nil {
				continue
			}
			lines = append(lines, fmt.Sprintf("  %s  %s: %s → %s (%s)",
				e.TS.Format("2006-01-02 15:04"), p.Role, p.Old, p.New, p.Reason))
		case journal.TypeModelFailure:
			p, perr := journal.ParseModelFailure(e)
			if perr != nil {
				continue
			}
			status := ""
			if p.Status > 0 {
				status = fmt.Sprintf(", %d", p.Status)
			}
			lines = append(lines, fmt.Sprintf("  %s  %s: %s FAILED unrecovered (%s%s)",
				e.TS.Format("2006-01-02 15:04"), p.Role, p.Model, p.Class, status))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	if len(lines) > recentModelEventsShown {
		lines = lines[len(lines)-recentModelEventsShown:]
	}
	return "Recent model events (substitutions and unrecovered failures):\n" +
		strings.Join(lines, "\n") + "\n"
}
