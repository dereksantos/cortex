package journal

import (
	"encoding/json"
	"fmt"
)

// TypeShellApproval is the entry type for a session-scoped bash approval
// (issue #107): the user chose "always this session" for a Risky command at
// the risky-command prompt. Kind is "exact" (the user approved that exact
// command, stored as itself) or "prefix" (the user approved a command
// prefix, stored as the command with "*" appended, e.g. "make test*").
// Command is the exact command that earned the approval and Reason is the
// classifier's reason for it — together they make the journal self-
// explaining for a later review of what a session auto-allowed.
const TypeShellApproval = "shell.approval"

// ShellApprovalPayload is one session approval event.
type ShellApprovalPayload struct {
	// Kind is "exact" or "prefix" (see TypeShellApproval).
	Kind string `json:"kind"`
	// Pattern is the stored approval pattern: the exact command for "exact",
	// the command with "*" appended for "prefix".
	Pattern string `json:"pattern"`
	// Command is the exact command that earned the approval.
	Command string `json:"command"`
	// Reason is the classifier's reason for that command (the same text the
	// prompt showed above the question).
	Reason string `json:"reason"`
	// Turn is the session's turn ordinal at the time of the approval (0
	// outside a turn — approvals only happen mid-turn, but hand-built
	// callers may not be inside one).
	Turn int `json:"turn"`
}

// NewShellApprovalEntry builds a journal entry for one session approval.
func NewShellApprovalEntry(p ShellApprovalPayload) (*Entry, error) {
	switch p.Kind {
	case "exact", "prefix":
	default:
		return nil, fmt.Errorf("journal: shell.approval requires Kind %q or %q, got %q", "exact", "prefix", p.Kind)
	}
	if p.Pattern == "" {
		return nil, fmt.Errorf("journal: shell.approval requires Pattern")
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("journal: marshal shell.approval: %w", err)
	}
	return &Entry{Type: TypeShellApproval, V: 1, Payload: data}, nil
}

// ParseShellApproval decodes a shell.approval entry's payload.
func ParseShellApproval(e *Entry) (*ShellApprovalPayload, error) {
	if e.Type != TypeShellApproval {
		return nil, fmt.Errorf("journal: entry type %q is not %s", e.Type, TypeShellApproval)
	}
	var p ShellApprovalPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil, fmt.Errorf("journal: parse shell.approval: %w", err)
	}
	return &p, nil
}
