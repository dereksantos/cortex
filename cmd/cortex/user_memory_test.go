// user_memory_test.go is the Δ (deterministic) layer for the user-tier
// memory store (docs/cross-source-learning.md piece 1): scope routing on
// each memory_* tool, project-shadows-user read resolution, both-tier search
// tagging, turn-start injection order + independently-capped tiers, and
// forget's never-cross-tier-delete contract. No model — the tool dispatcher
// and the real on-disk internal/memory.Store are exercised directly, mirroring
// memory_tools_test.go / memory_e2e_test.go's style for the pre-existing
// single (project) tier.
package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/memory"
	"github.com/dereksantos/cortex/internal/tools"
)

// newUserTierSession builds a session with BOTH memory tiers wired to their
// own isolated temp roots: the project tier via t.Chdir (EnableMemory reads
// ContextDir() relative to cwd) and the user tier via CORTEX_HOME
// (internal/userhome), so no test in this file can collide with another test
// or with the real machine's ~/.cortex.
func newUserTierSession(t *testing.T) *CortexSession {
	t.Helper()
	t.Chdir(t.TempDir())
	t.Setenv("CORTEX_HOME", t.TempDir())
	cs := &CortexSession{Request: CortexArgs{}.Request()}
	cs.EnableMemory()
	if cs.memory == nil {
		t.Fatal("EnableMemory should wire the project memory store")
	}
	if cs.userMemory == nil {
		t.Fatal("EnableMemory should wire the user memory store")
	}
	return cs
}

// --- scope routing ----------------------------------------------------

// TestMemoryScopeRoutingPerTool is table-driven over the four memory tools:
// an explicit scope arg ("project" or "user") on the tool call must land the
// operation on that tier's store ONLY — never the other one.
func TestMemoryScopeRoutingPerTool(t *testing.T) {
	ctx := context.Background()

	t.Run("write", func(t *testing.T) {
		for _, scope := range []string{"project", "user"} {
			t.Run(scope, func(t *testing.T) {
				cs := newUserTierSession(t)
				out, err := tools.Execute(ctx, memCall(tools.FunctionMemoryWrite, map[string]any{
					"name": "note", "content": "body", "scope": scope,
				}), cs)
				if err != nil {
					t.Fatalf("memory_write scope=%s: %v", scope, err)
				}
				if !strings.Contains(out, scope) {
					t.Errorf("write confirmation should name its tier %q, got %q", scope, out)
				}
				projBody, projErr := cs.memory.Read("note")
				userBody, userErr := cs.userMemory.Read("note")
				if scope == "project" {
					if projErr != nil || projBody != "body" {
						t.Errorf("project store should hold the note: body=%q err=%v", projBody, projErr)
					}
					if userErr == nil {
						t.Errorf("user store should NOT hold the note, got body=%q", userBody)
					}
				} else {
					if userErr != nil || userBody != "body" {
						t.Errorf("user store should hold the note: body=%q err=%v", userBody, userErr)
					}
					if projErr == nil {
						t.Errorf("project store should NOT hold the note, got body=%q", projBody)
					}
				}
			})
		}
	})

	t.Run("write default is project", func(t *testing.T) {
		cs := newUserTierSession(t)
		if _, err := tools.Execute(ctx, memCall(tools.FunctionMemoryWrite, map[string]any{
			"name": "note", "content": "body",
		}), cs); err != nil {
			t.Fatal(err)
		}
		if _, err := cs.memory.Read("note"); err != nil {
			t.Errorf("unscoped write should default to the project tier: %v", err)
		}
		if _, err := cs.userMemory.Read("note"); err == nil {
			t.Error("unscoped write must not also land on the user tier")
		}
	})

	t.Run("read explicit scope does not fall back", func(t *testing.T) {
		cs := newUserTierSession(t)
		if _, err := cs.userMemory.Write("only-user", "user body", time.Now()); err != nil {
			t.Fatal(err)
		}
		// Explicit scope=project must NOT fall back to the user tier even
		// though the note exists there.
		out, err := tools.Execute(ctx, memCall(tools.FunctionMemoryRead, map[string]any{
			"name": "only-user", "scope": "project",
		}), cs)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "user body") {
			t.Errorf("scope=project read must not fall back to the user tier, got %q", out)
		}
		if !strings.Contains(out, "no note named") {
			t.Errorf("scope=project miss should be a friendly observation, got %q", out)
		}

		out, err = tools.Execute(ctx, memCall(tools.FunctionMemoryRead, map[string]any{
			"name": "only-user", "scope": "user",
		}), cs)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "user body") {
			t.Errorf("scope=user read should return the user-tier note, got %q", out)
		}
	})

	t.Run("search explicit scope only queries that tier", func(t *testing.T) {
		cs := newUserTierSession(t)
		if _, err := cs.memory.Write("proj-fact", "keyword-zzz here", time.Now()); err != nil {
			t.Fatal(err)
		}
		if _, err := cs.userMemory.Write("user-fact", "keyword-zzz here too", time.Now()); err != nil {
			t.Fatal(err)
		}
		out, err := tools.Execute(ctx, memCall(tools.FunctionMemorySearch, map[string]any{
			"query": "keyword-zzz", "scope": "project",
		}), cs)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "proj-fact") || strings.Contains(out, "user-fact") {
			t.Errorf("scope=project search should return only the project hit, got %q", out)
		}
	})

	t.Run("forget explicit scope only deletes that tier", func(t *testing.T) {
		cs := newUserTierSession(t)
		if _, err := cs.memory.Write("dup", "project version", time.Now()); err != nil {
			t.Fatal(err)
		}
		if _, err := cs.userMemory.Write("dup", "user version", time.Now()); err != nil {
			t.Fatal(err)
		}
		if _, err := tools.Execute(ctx, memCall(tools.FunctionMemoryForget, map[string]any{
			"name": "dup", "scope": "user",
		}), cs); err != nil {
			t.Fatal(err)
		}
		if _, err := cs.userMemory.Read("dup"); err == nil {
			t.Error("scope=user forget should remove the user-tier note")
		}
		if body, err := cs.memory.Read("dup"); err != nil || body != "project version" {
			t.Errorf("scope=user forget must NOT touch the project tier: body=%q err=%v", body, err)
		}
	})
}

// --- read resolution: project shadows user -----------------------------

// TestMemoryReadShadowsProjectOverUser: an unscoped memory_read with a
// same-named note in both tiers must return the PROJECT body, silently — a
// project can locally override a cross-project fact without deleting the
// promoted one (docs/cross-source-learning.md piece 1).
func TestMemoryReadShadowsProjectOverUser(t *testing.T) {
	ctx := context.Background()
	cs := newUserTierSession(t)
	if _, err := cs.userMemory.Write("shared-name", "USER-TIER fact", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.memory.Write("shared-name", "PROJECT-TIER fact", time.Now()); err != nil {
		t.Fatal(err)
	}

	out, err := tools.Execute(ctx, memCall(tools.FunctionMemoryRead, map[string]any{"name": "shared-name"}), cs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "PROJECT-TIER") {
		t.Errorf("unscoped read should shadow to the project tier, got %q", out)
	}
	if strings.Contains(out, "USER-TIER") {
		t.Errorf("unscoped read must not leak the shadowed user-tier body, got %q", out)
	}
}

// TestMemoryReadFallsBackToUserOnProjectMiss: unscoped read with no project
// note but a user note of the same name still resolves — the fallback half
// of shadowing.
func TestMemoryReadFallsBackToUserOnProjectMiss(t *testing.T) {
	ctx := context.Background()
	cs := newUserTierSession(t)
	if _, err := cs.userMemory.Write("user-only", "cross-project fact", time.Now()); err != nil {
		t.Fatal(err)
	}
	out, err := tools.Execute(ctx, memCall(tools.FunctionMemoryRead, map[string]any{"name": "user-only"}), cs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "cross-project fact") {
		t.Errorf("unscoped read should fall back to the user tier on a project miss, got %q", out)
	}
}

// --- search: both-tier tagging ------------------------------------------

// TestMemorySearchBothTierTagging: an unscoped memory_search returns hits
// from BOTH tiers, each tagged with its tier — the collision visibility
// memory_read's shadowing hides.
func TestMemorySearchBothTierTagging(t *testing.T) {
	ctx := context.Background()
	cs := newUserTierSession(t)
	if _, err := cs.memory.Write("proj-note", "widget-factory details", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.userMemory.Write("user-note", "widget-factory pattern", time.Now()); err != nil {
		t.Fatal(err)
	}

	out, err := tools.Execute(ctx, memCall(tools.FunctionMemorySearch, map[string]any{"query": "widget-factory"}), cs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[project] proj-note") {
		t.Errorf("search should tag the project hit [project], got %q", out)
	}
	if !strings.Contains(out, "[user] user-note") {
		t.Errorf("search should tag the user hit [user], got %q", out)
	}
}

// --- turn-start injection: order + independent caps ---------------------

// TestMemoryIndexNoteInjectionOrderAndIndependentCaps: the user tier renders
// BEFORE the project tier, and each tier's truncation is governed by its OWN
// cap (limits.user_memory_index_cap_chars vs. limits.memory_index_cap_chars)
// — a tiny user cap truncates the user section without touching an
// untruncated project section, and vice versa.
func TestMemoryIndexNoteInjectionOrderAndIndependentCaps(t *testing.T) {
	cs := newUserTierSession(t)
	if _, err := cs.userMemory.Write("user-note", "a cross-project fact, long enough to matter", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.memory.Write("project-note", "a project-local fact", time.Now()); err != nil {
		t.Fatal(err)
	}

	t.Run("order", func(t *testing.T) {
		note := cs.memoryIndexNote()
		userPos := strings.Index(note, "user-note")
		projPos := strings.Index(note, "project-note")
		if userPos < 0 || projPos < 0 {
			t.Fatalf("both notes should appear in the index:\n%s", note)
		}
		if userPos > projPos {
			t.Errorf("user tier should render BEFORE project tier; got user@%d project@%d:\n%s", userPos, projPos, note)
		}
		if !strings.Contains(note, "User memory") || !strings.Contains(note, "Project memory") {
			t.Errorf("index should clearly label each tier:\n%s", note)
		}
	})

	t.Run("independent caps: tiny user cap truncates only the user section", func(t *testing.T) {
		cs.Config = &Config{Limits: LimitsConfig{UserMemoryIndexCapChars: 1}}
		note := cs.memoryIndexNote()
		if !strings.Contains(note, "truncated") {
			t.Errorf("user section should show as truncated at cap=1:\n%s", note)
		}
		if !strings.Contains(note, "project-note") {
			t.Errorf("project section must render in full, untouched by the user cap:\n%s", note)
		}
	})

	t.Run("independent caps: tiny project cap truncates only the project section", func(t *testing.T) {
		cs.Config = &Config{Limits: LimitsConfig{MemoryIndexCapChars: 1}}
		note := cs.memoryIndexNote()
		userIdx := strings.Index(note, "User memory")
		projIdx := strings.Index(note, "Project memory")
		if userIdx < 0 || projIdx < 0 {
			t.Fatalf("both section headers should be present:\n%s", note)
		}
		userSection := note[userIdx:projIdx]
		projSection := note[projIdx:]
		if strings.Contains(userSection, "truncated") {
			t.Errorf("user section must NOT be truncated by the project cap:\n%s", userSection)
		}
		if !strings.Contains(projSection, "truncated") {
			t.Errorf("project section should be truncated at cap=1:\n%s", projSection)
		}
	})
}

// TestMemoryIndexNoteOmitsEmptyTier: a tier with no notes (or no store) is
// simply omitted, not rendered as a hollow header — matching the
// pre-existing "no injection at all when there's nothing to say" contract.
func TestMemoryIndexNoteOmitsEmptyTier(t *testing.T) {
	cs := newUserTierSession(t)
	if _, err := cs.memory.Write("only-project", "fact", time.Now()); err != nil {
		t.Fatal(err)
	}
	note := cs.memoryIndexNote()
	if strings.Contains(note, "User memory") {
		t.Errorf("empty user tier should not render a section header:\n%s", note)
	}
	if !strings.Contains(note, "Project memory") || !strings.Contains(note, "only-project") {
		t.Errorf("project section should still render:\n%s", note)
	}
}

// --- forget: never cross-tier deletes ------------------------------------

// TestMemoryForgetNeverCrossesTiers: a same-named note in both tiers,
// forgotten with the DEFAULT (unscoped) call, removes only the project
// note — forget's default matches write's ("project" unless told
// otherwise), so an unscoped forget can never silently reach across into
// the user tier.
func TestMemoryForgetNeverCrossesTiers(t *testing.T) {
	ctx := context.Background()
	cs := newUserTierSession(t)
	if _, err := cs.memory.Write("dup", "project version", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.userMemory.Write("dup", "user version", time.Now()); err != nil {
		t.Fatal(err)
	}

	out, err := tools.Execute(ctx, memCall(tools.FunctionMemoryForget, map[string]any{"name": "dup"}), cs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "project") {
		t.Errorf("forget confirmation should name the project tier, got %q", out)
	}
	if _, err := cs.memory.Read("dup"); err == nil {
		t.Error("unscoped forget should remove the project-tier note")
	}
	if body, err := cs.userMemory.Read("dup"); err != nil || body != "user version" {
		t.Errorf("unscoped forget must NOT touch the user tier: body=%q err=%v", body, err)
	}
}

// TestMemoryUnavailableReportsWhichTier: with only the project store wired
// (no user store — the pre-EnableMemory / no-workspace shape), a scope=user
// call reports THAT tier unavailable rather than a generic message, and
// leaves the project tier fully working.
func TestMemoryUnavailableReportsWhichTier(t *testing.T) {
	ctx := context.Background()
	t.Chdir(t.TempDir())
	cs := &CortexSession{Request: CortexArgs{}.Request()}
	// Wire ONLY the project tier by hand — the shape EnableMemory produces
	// when userhome.Path fails (e.g. no resolvable home), without needing to
	// actually break os.UserHomeDir for this test.
	dir := cs.ContextDir()
	memStore, err := memory.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	cs.memory = memStore

	out, err := tools.Execute(ctx, memCall(tools.FunctionMemoryWrite, map[string]any{
		"name": "x", "content": "y", "scope": "user",
	}), cs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "user") || !strings.Contains(out, "unavailable") {
		t.Errorf("scope=user write with no user store should report the USER tier unavailable, got %q", out)
	}

	// The project tier is unaffected.
	if _, err := tools.Execute(ctx, memCall(tools.FunctionMemoryWrite, map[string]any{
		"name": "x", "content": "y",
	}), cs); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.memory.Read("x"); err != nil {
		t.Errorf("project tier should still work: %v", err)
	}
}
