package decide

import "context"

// Logprob is the interface-ready stub for a local backend that scores each
// closed choice by its log-probability instead of generating text (the shape
// a decision model like Jev also fits: choices in, a scored pick out, with
// Answer.Confidence filled). Not implemented: every decision reports so.
type Logprob struct{}

// Decide implements Decider.
func (Logprob) Decide(context.Context, Snapshot, []Ask) Decision {
	return Decision{Backend: "logprob", Status: StatusError, Detail: "logprob backend not implemented"}
}

// Name implements Decider.
func (Logprob) Name() string { return "logprob" }
