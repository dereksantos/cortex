package cognition

import (
	"context"
	"log"

	"github.com/dereksantos/cortex/pkg/events"
)

// Router routes events to appropriate cognitive modes based on event type.
//
// Event routing logic:
//   - user_prompt → Think.IngestPrompt() for pattern learning
//   - tool_use → Reflex.Index() for fast storage
//   - stop → Dream.Queue() for deeper transcript analysis
type Router struct {
	reflex *Reflex
	think  *Think
	dream  *Dream
}

// NewRouter creates a new event router.
func NewRouter(reflex *Reflex, think *Think, dream *Dream) *Router {
	return &Router{
		reflex: reflex,
		think:  think,
		dream:  dream,
	}
}

// RouteResult describes what happened when routing an event.
type RouteResult struct {
	Routed    bool   // Whether the event was successfully routed
	Target    string // Which cognitive mode handled it ("think", "reflex", "dream")
	Immediate bool   // Whether processing was synchronous
}

// Route sends an event to the appropriate cognitive mode.
// Returns information about how the event was handled.
func (r *Router) Route(ctx context.Context, event *events.Event) *RouteResult {
	if event == nil {
		return &RouteResult{Routed: false}
	}

	switch event.EventType {
	case events.EventUserPrompt:
		return r.routeUserPrompt(ctx, event)
	case events.EventToolUse:
		return r.routeToolUse(ctx, event)
	case events.EventStop:
		return r.routeStop(ctx, event)
	default:
		log.Printf("Router: unhandled event type %s", event.EventType)
		return &RouteResult{Routed: false}
	}
}

// routeUserPrompt handles user prompt events.
// First prompt in session → immediate Think (sync)
// Subsequent prompts → queue for background Think
func (r *Router) routeUserPrompt(ctx context.Context, event *events.Event) *RouteResult {
	if r.think == nil {
		return &RouteResult{Routed: false}
	}

	prompt := event.Prompt
	if prompt == "" {
		return &RouteResult{Routed: false}
	}

	// IngestPrompt handles both sync and async based on session state
	r.think.IngestPrompt(ctx, prompt, event.Context.SessionID)

	return &RouteResult{
		Routed:    true,
		Target:    "think",
		Immediate: false, // IngestPrompt decides sync vs async internally
	}
}

// routeToolUse handles tool use events.
// These go to Reflex for fast indexing.
func (r *Router) routeToolUse(ctx context.Context, event *events.Event) *RouteResult {
	if r.reflex == nil {
		return &RouteResult{Routed: false}
	}

	// For now, tool use events are just stored via normal capture path
	// Reflex will pick them up on next search
	// Future: could add explicit Reflex.Index() method for immediate embedding

	return &RouteResult{
		Routed:    true,
		Target:    "reflex",
		Immediate: false,
	}
}

// routeStop handles stop events (session end).
// These queue transcript analysis for Dream.
func (r *Router) routeStop(ctx context.Context, event *events.Event) *RouteResult {
	if r.dream == nil {
		return &RouteResult{Routed: false}
	}

	// Queue transcript for Dream exploration
	if event.TranscriptPath != "" {
		r.dream.QueueTranscript(event.TranscriptPath, event.Context.SessionID)
	}

	return &RouteResult{
		Routed:    true,
		Target:    "dream",
		Immediate: false, // Dream processes during idle
	}
}
