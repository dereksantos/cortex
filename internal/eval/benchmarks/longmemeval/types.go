// Package longmemeval implements the LongMemEval Oracle-split benchmark
// against Cortex's in-process coding harness with cortex_search enabled.
//
// LongMemEval is a published benchmark (Wu et al., MIT-licensed) that
// probes long-horizon conversational memory across five ability axes:
// single-hop extraction, multi-session reasoning, temporal reasoning,
// knowledge-update tracking, and abstention. The Oracle split contains
// only evidence sessions per question, making it the cheapest entry
// point for cross-comparable memory numbers.
//
// Phase A (this package): Oracle split, single-attempt scoring,
// per-axis pass-rate breakdown reported via the CellResult.Notes field.
// Phase B (deferred): S/M splits, parity GPT-4o judge runs.
package longmemeval

import (
	"bytes"
	"encoding/json"
)

// Question is the parsed shape of one LongMemEval Oracle instance.
// Field names mirror the upstream JSON (snake_case) so unmarshaling is
// trivial. haystack_dates is parallel to haystack_session_ids and
// haystack_sessions — all three slices share an index.
//
// Answer is normalized to string by UnmarshalJSON because upstream
// serializes counting-question answers as bare JSON numbers
// (e.g. `"answer": 3`) while everything else is a string. Downstream
// code (judge prompt) always sees a string.
type Question struct {
	QuestionID         string   `json:"question_id"`
	QuestionType       string   `json:"question_type"`
	Question           string   `json:"question"`
	Answer             string   `json:"-"`
	QuestionDate       string   `json:"question_date"`
	HaystackSessionIDs []string `json:"haystack_session_ids"`
	HaystackDates      []string `json:"haystack_dates"`
	HaystackSessions   [][]Turn `json:"haystack_sessions"`
	AnswerSessionIDs   []string `json:"answer_session_ids"`
}

// UnmarshalJSON parses a Question accommodating the polymorphic
// `answer` field. The shadow type prevents recursive UnmarshalJSON
// calls on the embedded Question.
func (q *Question) UnmarshalJSON(data []byte) error {
	type shadow struct {
		QuestionID         string             `json:"question_id"`
		QuestionType       string             `json:"question_type"`
		Question           string             `json:"question"`
		Answer             jsonStringOrNumber `json:"answer"`
		QuestionDate       string             `json:"question_date"`
		HaystackSessionIDs []string           `json:"haystack_session_ids"`
		HaystackDates      []string           `json:"haystack_dates"`
		HaystackSessions   [][]Turn           `json:"haystack_sessions"`
		AnswerSessionIDs   []string           `json:"answer_session_ids"`
	}
	var s shadow
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	q.QuestionID = s.QuestionID
	q.QuestionType = s.QuestionType
	q.Question = s.Question
	q.Answer = string(s.Answer)
	q.QuestionDate = s.QuestionDate
	q.HaystackSessionIDs = s.HaystackSessionIDs
	q.HaystackDates = s.HaystackDates
	q.HaystackSessions = s.HaystackSessions
	q.AnswerSessionIDs = s.AnswerSessionIDs
	return nil
}

// jsonStringOrNumber accepts either a JSON string or a JSON number
// (or null) and stores the textual form. Used for the polymorphic
// `answer` field described on Question.
type jsonStringOrNumber string

func (j *jsonStringOrNumber) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		*j = ""
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*j = jsonStringOrNumber(s)
		return nil
	}
	*j = jsonStringOrNumber(string(data))
	return nil
}

// Turn is one user/assistant exchange in a haystack session.
// HasAnswer is set upstream only on evidence turns; the JSON omits
// the field entirely on non-evidence turns (zero-value false).
type Turn struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	HasAnswer bool   `json:"has_answer,omitempty"`
}

// Strategy values for --strategy. Maps 1:1 to evalv2.StrategyBaseline
// and evalv2.StrategyCortex, but kept as package-local constants so
// the loader can validate without importing the evalv2 package.
const (
	StrategyBaseline = "baseline"
	StrategyCortex   = "cortex"
)

// InstancePayload is what Load packs into benchmarks.Instance.Payload.
// One payload per (Question × Strategy) — the runner reads Strategy to
// decide whether to hydrate the haystack and register cortex_search.
type InstancePayload struct {
	Q        Question
	Strategy string
}

// Ability axes — the five buckets the paper defines. These are the
// normalized labels reported via CellResult.Notes; the upstream
// question_type strings are mapped through NormalizeAxis.
const (
	AxisSingleHop       = "single-hop"
	AxisMultiHop        = "multi-hop"
	AxisTemporal        = "temporal"
	AxisKnowledgeUpdate = "knowledge-update"
	AxisAbstention      = "abstention"
)

// NormalizeAxis maps an upstream question_type string into one of the
// five Ability constants. Unknown types fall through to "" so callers
// can detect drift in the dataset rather than silently bucket them.
//
// Mappings (paper-aligned):
//
//	single-session-{user,assistant,preference} → single-hop
//	multi-session                              → multi-hop
//	temporal-reasoning                         → temporal
//	knowledge-update                           → knowledge-update
//	abstention                                 → abstention
func NormalizeAxis(questionType string) string {
	switch questionType {
	case "single-session-user", "single-session-assistant", "single-session-preference":
		return AxisSingleHop
	case "multi-session":
		return AxisMultiHop
	case "temporal-reasoning":
		return AxisTemporal
	case "knowledge-update":
		return AxisKnowledgeUpdate
	case "abstention":
		return AxisAbstention
	}
	// Permissive: if the upstream string already matches a normalized
	// axis (older snapshots, custom datasets) accept it as-is.
	switch questionType {
	case AxisSingleHop, AxisMultiHop, AxisTemporal, AxisKnowledgeUpdate, AxisAbstention:
		return questionType
	}
	return ""
}

// AllAxes returns the five normalized axis labels in a stable order.
// Used by reporting code that iterates over per-axis rollups.
func AllAxes() []string {
	return []string{
		AxisSingleHop,
		AxisMultiHop,
		AxisTemporal,
		AxisKnowledgeUpdate,
		AxisAbstention,
	}
}
