// Package decide is the parallel "think" stream: a decision model that runs
// beside the coding model and answers small, closed-choice questions about the
// agent's next move — the little voice that watches the work without doing it.
//
// A Decider is a pure function of a Snapshot (a bounded, language-neutral
// digest of the turn so far) and a list of Asks (each a question with a closed
// set of choices). It never returns an error: a backend that is rate-limited,
// slow, or answers off-menu degrades to a Decision with a non-ok Status and no
// answers, so a caller can always skip it. Today every decision is shadow-only
// — logged and measured, never shown to the coder (docs/decide.md).
package decide

import (
	"context"
	"time"
)

// Question names one closed-choice question the decider can answer.
type Question string

const (
	// NextMove: what the agent should do next (choices: Moves).
	NextMove Question = "next_move"
	// RecallTarget: which session-outline citation to recall, or "none".
	RecallTarget Question = "recall_target"
	// Looping: which recent steps repeat without new information, or "none".
	Looping Question = "looping"
	// ClaimCheck: at finalize, whether the reply's claims are backed by steps.
	ClaimCheck Question = "claim_check"
)

// The next_move choices. A tool call maps onto one of them via MoveOf.
const (
	MoveRead     = "read"
	MoveSearch   = "search"
	MoveEdit     = "edit"
	MoveTest     = "test"
	MoveRun      = "run"
	MoveRecall   = "recall"
	MoveFinalize = "finalize"
	MoveOther    = "other"
)

// Moves is the next_move choice set (MoveOther is an actual-move label only;
// the decider is never offered it).
var Moves = []string{MoveRead, MoveSearch, MoveEdit, MoveTest, MoveRun, MoveRecall, MoveFinalize}

// The claim_check choices.
const (
	ClaimSupported   = "supported"
	ClaimUnsupported = "unsupported"
	ClaimNone        = "no_claims"
)

// ClaimChoices is the claim_check choice set.
var ClaimChoices = []string{ClaimSupported, ClaimUnsupported, ClaimNone}

// None is the "nothing applies" choice for recall_target and looping.
const None = "none"

// Status values for a Decision. Only StatusOK carries answers.
const (
	StatusOK          = "ok"
	StatusTimeout     = "timeout"
	StatusRateLimited = "rate_limited"
	StatusParseError  = "parse_error"
	StatusBudget      = "budget"
	StatusError       = "error"
	StatusDisabled    = "disabled"
)

// Ask is one question with its closed choice set. Multi allows several picks
// (looping names every repeating step); otherwise exactly one is valid.
type Ask struct {
	Q       Question `json:"question"`
	Choices []string `json:"choices"`
	Multi   bool     `json:"multi,omitempty"`
}

// Step is one tool call the agent made, clipped for the decider's prompt.
type Step struct {
	ID     string `json:"id"` // "s1", "s2", … — stable within a Snapshot; looping picks name these
	Tool   string `json:"tool"`
	Args   string `json:"args"`
	Result string `json:"result"`
	Err    bool   `json:"err,omitempty"`
}

// Snapshot is what the decider sees: the task, the recent steps, a ledger of
// the whole turn's effects, and (at finalize) the reply. Builders clip every
// field; a Snapshot never carries the coder's full context.
type Snapshot struct {
	Task      string `json:"task"`
	Steps     []Step `json:"steps"`      // the most recent steps, oldest first
	StepsSeen int    `json:"steps_seen"` // total steps this turn (Steps may be a suffix)
	Ledger    Ledger `json:"ledger"`
	Reply     string `json:"reply,omitempty"` // the final reply (claim_check only)
}

// Ledger is the mechanical record of the whole turn so far — what was edited
// and what was run — so claims can be checked even when the steps that made
// them have scrolled out of Steps.
type Ledger struct {
	ToolCounts  map[string]int `json:"tool_counts"`
	FilesEdited []string       `json:"files_edited,omitempty"`
	Commands    []string       `json:"commands,omitempty"` // shell commands run, clipped, with a refusal marker when blocked
	Errors      int            `json:"errors"`
}

// Answer is the decider's pick(s) for one Ask. Confidence is 0 when the
// backend does not report one (a logprob backend fills it).
type Answer struct {
	Q          Question `json:"question"`
	Picks      []string `json:"picks"`
	Confidence float64  `json:"confidence,omitempty"`
}

// Decision is one decider call's outcome. Answers is empty unless Status is
// StatusOK; Detail says why when it is not.
type Decision struct {
	Answers []Answer      `json:"answers,omitempty"`
	Backend string        `json:"backend"`
	Model   string        `json:"model,omitempty"`
	Latency time.Duration `json:"latency"`
	Status  string        `json:"status"`
	Detail  string        `json:"detail,omitempty"`
	// PromptVersion identifies the prompt text that produced the answers
	// (PromptVersion()), so rows from different prompt revisions never mix.
	PromptVersion string `json:"prompt_version,omitempty"`
}

// Pick returns the answer for q, if the decision has one.
func (d Decision) Pick(q Question) (Answer, bool) {
	for _, a := range d.Answers {
		if a.Q == q {
			return a, true
		}
	}
	return Answer{}, false
}

// Decider answers closed-choice questions about a Snapshot. Decide must never
// return an error and must honor ctx: a caller that cannot wait skips the
// decision, it never blocks on it.
type Decider interface {
	Decide(ctx context.Context, s Snapshot, asks []Ask) Decision
	Name() string
}

// Null is the default Decider: it decides nothing.
type Null struct{}

// Decide implements Decider.
func (Null) Decide(context.Context, Snapshot, []Ask) Decision {
	return Decision{Backend: "null", Status: StatusDisabled}
}

// Name implements Decider.
func (Null) Name() string { return "null" }
