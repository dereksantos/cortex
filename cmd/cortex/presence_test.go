package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/lineedit"
	"github.com/dereksantos/cortex/internal/presence"
	"github.com/dereksantos/cortex/internal/shellrisk"
)

type presenceSink struct{ seqs []string }

func (s *presenceSink) open() (io.WriteCloser, error) { return sinkWriter{s}, nil }

type sinkWriter struct{ s *presenceSink }

func (w sinkWriter) Write(p []byte) (int, error) {
	w.s.seqs = append(w.s.seqs, string(p))
	return len(p), nil
}
func (sinkWriter) Close() error { return nil }

// labels reduces each sequence to its event, or "notify" for a notification.
func (s *presenceSink) labels() string {
	var out []string
	for _, seq := range s.seqs {
		switch {
		case strings.Contains(seq, ";kind=notify;"):
			out = append(out, "notify")
		case strings.Contains(seq, ";event="):
			ev := seq[strings.Index(seq, ";event=")+len(";event="):]
			out = append(out, strings.TrimSuffix(ev, "\x1b\\"))
		default:
			out = append(out, fmt.Sprintf("?%q", seq))
		}
	}
	return strings.Join(out, ",")
}

func TestReportTurnEnd(t *testing.T) {
	tests := []struct {
		name        string
		reply       string
		interrupted bool
		err         error
		want        string
	}{
		{"answered", "done", false, nil, "idle,notify"},
		{"empty reply posts nothing", "", false, nil, "idle"},
		{"interrupted flag", "partial", true, nil, "idle"},
		{"cancelled context", "", false, context.Canceled, "idle"},
		{"wrapped cancel", "", false, fmt.Errorf("turn: %w", context.Canceled), "idle"},
		{"failed turn", "", false, errors.New("backend 500"), "error,notify"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sink := &presenceSink{}
			cs := &CortexSession{presence: presence.New(sink.open, 0)}
			cs.reportTurnEnd(tt.reply, tt.interrupted, tt.err)
			if got := sink.labels(); got != tt.want {
				t.Errorf("signals = %s, want %s", got, tt.want)
			}
		})
	}
}

// TestRiskyPromptReportsAwaitingInput: the host badge must say the agent is
// waiting on the human while the Risky-command prompt is up, and busy again
// once it is answered.
func TestRiskyPromptReportsAwaitingInput(t *testing.T) {
	sink := &presenceSink{}
	var during string
	cs := &CortexSession{
		classifyShell: func(context.Context, string, string) (shellrisk.Level, string, error) {
			return shellrisk.Risky, "test: risky", nil
		},
		workspace: &Workspace{Root: t.TempDir()},
		presence:  presence.New(sink.open, 0),
	}
	cs.confirmRisky = func(string) lineedit.ConfirmChoice {
		during = sink.labels()
		return lineedit.ConfirmNo
	}
	cs.gateShell(context.Background(), "make deploy")
	if during != "awaiting_input" {
		t.Errorf("signals while prompting = %q, want awaiting_input", during)
	}
	if got := sink.labels(); got != "awaiting_input,busy" {
		t.Errorf("signals = %s, want awaiting_input,busy", got)
	}
}

// TestSessionsWithoutPresenceStayInert covers every session the REPL and
// `cortex turn` did not attach an emitter to (serve, discord, tests): the
// reports are no-ops on the nil emitter.
func TestSessionsWithoutPresenceStayInert(t *testing.T) {
	cs := &CortexSession{}
	cs.reportTurnEnd("done", false, nil)
	cs.reportTurnEnd("", false, errors.New("x"))
	cs.endPresence()
}
