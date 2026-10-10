// checkpoint_turn_test.go drives the REAL turn path (cs.Turn →
// recordCheckpoint at turn start, commitCheckpoint's turn-end diff) with the
// test-only seams: a scripted coder sender (senderOverride) and a scripted
// `agent` subagent sender (subagentSenderOverride) — zero network. These lock
// the
// two cases the per-tool mutation flag (round 1's bug, through bash) and the
// subagent dispatcher's separate path (round 2's bug 3) both get wrong:
//
//   - a turn whose only calls are READ-ONLY bash commands must record NO
//     checkpoint — the tree is unchanged, so commitCheckpoint's turn-end
//     diff drops the ref (depth must not shift), while a mutating turn
//     BEFORE it still lands one;
//   - a turn whose ONLY edit happens inside the `agent` subagent's own
//     dispatcher must land a checkpoint the same /undo reverts — the
//     subagent's writes go through a dispatcher that never set the flag, so
//     the only signal that catches them is the tree diff at the turn's end.
package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dereksantos/cortex/internal/tools"
)

// scriptedSubagentSender returns a Sender that replays the subagent's scripted
// model replies one per send, repeating the last — the subagent's own model
// conversation (its tool-call and final-answer rounds). Wired in through the
// subagentSenderOverride seam (blockingSender), so the REAL runLoop +
// dispatcher still run and only the model round-trip is scripted (zero
// network). The subagent's model REPLIES are produced by the sender, not the
// request — that is why the seam keys on the role and replaces the sender.
func scriptedSubagentSender(resp ...*AgentResponse) Sender {
	i := 0
	return SenderFunc(func(_ context.Context, _ *AgentRequest) (*AgentResponse, bool, error) {
		r := resp[min(i, len(resp)-1)]
		i++
		return r, false, nil
	})
}

// checkpointTurnSession builds a hand-built session rooted at a git repo in
// dir (initGitRepo), with the test-only seams armed: a scripted coder sender
// (send) and, when subagentSend is non-nil, a scripted `agent` subagent sender.
// The REAL coderDispatcher and the real agent subagent dispatcher run — the
// turn's edits really happen on disk.
func checkpointTurnSession(t *testing.T, dir string, send Sender, subagentSend Sender) *CortexSession {
	t.Helper()
	t.Chdir(dir)
	initGitRepo(t, dir)
	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	cs := &CortexSession{
		quiet: true, workspace: ws,
		Request: &AgentRequest{Model: "m", Messages: []Message{{Role: RoleSystem, Content: "sys"}}},
	}
	cs.SessionID = "s-turn-checkpoint"
	cs.Study = ModelSpec{Model: "sub-m", Endpoint: "http://127.0.0.1:9"}
	cs.senderOverride = send
	if subagentSend != nil {
		cs.subagentSenderOverride = map[string]Sender{"agent": subagentSend}
	}
	return cs
}

// stackLen is a nil-safe checkpointStack length (the stack is nil until the
// first committed checkpoint).
func stackLen(s *checkpointStack) int {
	if s == nil {
		return -1
	}
	return s.len()
}

// TestTurnReadonlyBashDoesNotRecordCheckpoint locks finding 2's case (a): a
// turn whose only call is a read-only bash command leaves no checkpoint. The
// real bash tool runs `git status --porcelain` in the workspace; the tree is
// unchanged when the turn ends, so commitCheckpoint's diff drops the ref. A
// mutating turn FIRST still lands a checkpoint, and the read-only turn must
// not shift its depth: before the undo the stack holds exactly 1 entry, and
// /undo reverts the mutating turn.
func TestTurnReadonlyBashDoesNotRecordCheckpoint(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "a.txt", "v1\n")
	initGitRepo(t, dir)
	gitCmd(t, dir, "add", "a.txt")
	gitCmd(t, dir, "commit", "-q", "-m", "base")

	coder := []Sender{
		multiTurnScriptedSender([]*AgentResponse{
			writeCallResp("w1", "a.txt", "v2\n"),
			answerResp("changed a.txt"),
		}),
		multiTurnScriptedSender([]*AgentResponse{
			bashCallResp("git status --porcelain"),
			answerResp("status shown"),
		}),
	}
	cs := checkpointTurnSession(t, dir, coder[0], nil)

	// Turn 1 (mutating): write a.txt v2 through the real write_file path.
	if _, err := cs.Turn(context.Background(), "change a.txt to v2"); err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if got := stackLen(cs.checkpoints); got != 1 {
		t.Fatalf("after the mutating turn the stack has %d entries, want 1", got)
	}

	// Turn 2 (read-only bash only): the scripted coder reply issues ONE
	// `git status` call, then answers. The real dispatcher runs the real
	// bash tool (read-only), so the tree is unchanged at the turn's end.
	cs.senderOverride = coder[1]
	if _, err := cs.Turn(context.Background(), "just show the git status"); err != nil {
		t.Fatalf("turn 2: %v", err)
	}

	if got := stackLen(cs.checkpoints); got != 1 {
		t.Fatalf("after the read-only bash turn the stack has %d entries, want 1 — the read-only turn must not push a no-op checkpoint", got)
	}
	// The on-disk refs agree: exactly one (the mutating turn's), no ref for
	// the read-only turn.
	if n := checkpointRefCount(t, dir); n != 1 {
		t.Errorf("read-only bash turn left %d checkpoint refs, want 1 (the mutating turn's only)", n)
	}
	// /undo reverts the MUTATING turn: a.txt is back to v1.
	cs.undo(1)
	if got := read(t, dir, "a.txt"); got != "v1\n" {
		t.Errorf("after /undo a.txt = %q, want %q (the pre-turn-1 state)", got, "v1\n")
	}
	if got := stackLen(cs.checkpoints); got != 0 {
		t.Errorf("after /undo the stack has %d entries, want 0", got)
	}
}

// TestTurnAgentSubagentWriteRecordsCheckpoint locks finding 2's case (b): a
// turn whose ONLY edit happens inside the `agent` subagent's own dispatcher
// lands a checkpoint the same /undo reverts. The real agent dispatcher
// (dispatcherFor + tools.Execute) runs the scripted subagent's write_file —
// the subagent's own model replies come through the subagentSenderOverride
// seam (no network). The per-tool mutation flag was never set (it is gone
// entirely), so the checkpoint can only exist if commitCheckpoint's turn-end
// tree diff catches the subagent's write.
func TestTurnAgentSubagentWriteRecordsCheckpoint(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "a.txt", "v1\n")
	initGitRepo(t, dir)
	gitCmd(t, dir, "add", "a.txt")
	gitCmd(t, dir, "commit", "-q", "-m", "base")

	writeArgs, _ := json.Marshal(map[string]any{"path": "b.txt", "content": "v2\n"})
	subWrite := &AgentResponse{
		Choices: []Choice{{Message: Message{
			Role: "assistant",
			ToolCalls: []ToolCall{{
				ID: "c1", Type: "function",
				Function: FunctionCall{Name: "write_file", Arguments: string(writeArgs)},
			}},
		}}},
	}
	subAnswer := &AgentResponse{
		Choices: []Choice{{Message: Message{
			Role: "assistant", Content: "wrote b.txt",
		}}},
	}
	cs := checkpointTurnSession(t, dir,
		multiTurnScriptedSender([]*AgentResponse{
			agentCallResp("write the file b.txt with content v2"),
			answerResp("done"),
		}),
		scriptedSubagentSender(subWrite, subAnswer))

	if _, err := cs.Turn(context.Background(), "create the file b.txt with content v2"); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	// The file must actually exist (the subagent's write really ran).
	if _, err := os.ReadFile(filepath.Join(dir, "b.txt")); err != nil {
		t.Fatalf("read b.txt: %v", err)
	}
	// The checkpoint must exist: the turn's only edit was the subagent's, and
	// the turn-end tree diff caught it.
	if got := stackLen(cs.checkpoints); got != 1 {
		t.Fatalf("turn whose only edit was the agent subagent's left %d checkpoints, want 1", got)
	}
	// /undo reverts the subagent's write: b.txt is gone (the turn created
	// it — an untracked path absent from the snapshot's baseline).
	cs.undo(1)
	if _, err := os.Stat(filepath.Join(dir, "b.txt")); !os.IsNotExist(err) {
		t.Errorf("after /undo b.txt still exists (err=%v), want it removed (the turn created it)", err)
	}
	// The pre-existing tracked file is untouched.
	if got := read(t, dir, "a.txt"); got != "v1\n" {
		t.Errorf("after /undo a.txt = %q, want %q (the pre-existing tracked file is untouched)", got, "v1\n")
	}
}

// writeCallResp returns an AgentResponse carrying one write_file tool call
// (id, path, content).
func writeCallResp(id, path, content string) *AgentResponse {
	args, _ := json.Marshal(map[string]any{"path": path, "content": content})
	return &AgentResponse{
		Choices: []Choice{{
			Message: Message{
				Role: "assistant",
				ToolCalls: []ToolCall{{
					ID:       id,
					Function: FunctionCall{Name: "write_file", Arguments: string(args)},
				}},
			},
		}},
	}
}

// bashCallResp returns an AgentResponse carrying one bash tool call
// (command) — the scripted coder reply for a read-only bash turn.
func bashCallResp(command string) *AgentResponse {
	args, _ := json.Marshal(map[string]any{"command": command})
	return &AgentResponse{
		Choices: []Choice{{
			Message: Message{
				Role: "assistant",
				ToolCalls: []ToolCall{{
					ID:       "c1",
					Function: FunctionCall{Name: "bash", Arguments: string(args)},
				}},
			},
		}},
	}
}

// agentCallResp returns an AgentResponse carrying one agent tool call (goal,
// path) — the scripted coder reply for the subagent turn.
func agentCallResp(goal string) *AgentResponse {
	args, _ := json.Marshal(map[string]any{"goal": goal, "path": "."})
	return &AgentResponse{
		Choices: []Choice{{
			Message: Message{
				Role: "assistant",
				ToolCalls: []ToolCall{{
					ID:       "c1",
					Function: FunctionCall{Name: tools.FunctionAgent, Arguments: string(args)},
				}},
			},
		}},
	}
}
