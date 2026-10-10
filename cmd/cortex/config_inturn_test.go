// config_inturn_test.go — issue #171, step 4: the context.in_turn_demotion
// and context.in_turn_keep_recent config surface. Table-driven tests lock the
// four behaviors the step calls for: default-on (absent key → demotion enabled,
// keepRecent 6), explicit off (in_turn_demotion:false → disabled), project
// override (a project-level context.* threads over the user layer, the rest
// inherited), and invalid keep_recent ≤ 0 rejected by validateContextConfig.
// Kept in its own new file rather than editing config_test.go (the
// pre-existing file the standing regression guard treats as frozen — see
// loop_budget_test.go's header).
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestInTurnDemotionConfig(t *testing.T) {
	t.Run("default-on: absent key → enabled, keepRecent 6", func(t *testing.T) {
		// A config with NO context section at all (the zero value) must leave
		// in-turn demotion enabled and keep-recent at the default.
		c := &Config{}
		if !c.inTurnDemotionEnabled() {
			t.Error("inTurnDemotionEnabled() = false for an absent context.in_turn_demotion, want true (default on)")
		}
		if got := c.inTurnKeepRecent(); got != inTurnKeepRecentDefault {
			t.Errorf("inTurnKeepRecent() = %d for an absent context.in_turn_keep_recent, want %d (default)", got, inTurnKeepRecentDefault)
		}
	})

	t.Run("explicit off: in_turn_demotion:false disables", func(t *testing.T) {
		c := &Config{Context: ContextConfig{InTurnDemotion: boolPtr(false)}}
		if c.inTurnDemotionEnabled() {
			t.Error("inTurnDemotionEnabled() = true for in_turn_demotion:false, want false")
		}
	})

	t.Run("explicit on: in_turn_demotion:true enables", func(t *testing.T) {
		c := &Config{Context: ContextConfig{InTurnDemotion: boolPtr(true)}}
		if !c.inTurnDemotionEnabled() {
			t.Error("inTurnDemotionEnabled() = false for in_turn_demotion:true, want true")
		}
	})

	t.Run("explicit keep_recent wins over the default", func(t *testing.T) {
		c := &Config{Context: ContextConfig{InTurnKeepRecent: intPtr(3)}}
		if got := c.inTurnKeepRecent(); got != 3 {
			t.Errorf("inTurnKeepRecent() = %d for in_turn_keep_recent:3, want 3", got)
		}
	})
}

// TestInTurnKeepRecentValidation locks validateContextConfig's handling of
// context.in_turn_keep_recent: any explicit value ≤ 0 is rejected (clear
// error), positive values are accepted, and an ABSENT key (nil) is never
// rejected — it just means "use the default". The invalid check runs even when
// no fractions are set (a config that names ONLY in_turn_keep_recent:0 must
// still be caught), so the rejection is not skipped by the fraction group's
// "all unset → done" early return.
func TestInTurnKeepRecentValidation(t *testing.T) {
	cases := []struct {
		name    string
		cfg     ContextConfig
		wantErr bool
	}{
		{"absent (nil) → accepted (means default)", ContextConfig{}, false},
		{"absent demotion + keep_recent 6 → accepted", ContextConfig{InTurnKeepRecent: intPtr(6)}, false},
		{"explicit positive 1 → accepted", ContextConfig{InTurnKeepRecent: intPtr(1)}, false},
		{"zero (≤0) → rejected", ContextConfig{InTurnKeepRecent: intPtr(0)}, true},
		{"negative (≤0) → rejected", ContextConfig{InTurnKeepRecent: intPtr(-4)}, true},
		{"zero with a fraction set → still rejected (not masked)", ContextConfig{TailHighFraction: floatPtr(0.4), InTurnKeepRecent: intPtr(0)}, true},
		{"positive with a fraction set → accepted", ContextConfig{TailHighFraction: floatPtr(0.4), InTurnKeepRecent: intPtr(5)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateContextConfig(tc.cfg)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateContextConfig(%+v) error = %v, wantErr %v", tc.cfg, err, tc.wantErr)
			}
			if tc.wantErr && err != nil {
				// The error must name the field so an operator knows what to fix.
				if !contains(err.Error(), "in_turn_keep_recent") {
					t.Errorf("error %q does not name context.in_turn_keep_recent", err)
				}
			}
		})
	}
}

// TestInTurnKeepRecentProjectOverride locks mergeContext's threading: a
// project-level context.in_turn_keep_recent (and in_turn_demotion) overrides
// the user layer, while a context fraction set ONLY at the user level
// (inherited) is preserved. Exercises loadMergedConfig the same way the
// config_test.go project-override tests do, but for the in-turn fields.
func TestInTurnKeepRecentProjectOverride(t *testing.T) {
	t.Run("project keep_recent overrides user, user fraction inherited", func(t *testing.T) {
		dir := t.TempDir()
		write := func(name, body string) string {
			p := filepath.Join(dir, name)
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			return p
		}
		// User layer: keep_recent 8, a tail_high_fraction 0.4 (inherited).
		userPath := write("user.json", `{
			"backend": {"type": "openrouter", "endpoint": "https://openrouter.ai/api/v1", "key_env": "OPENROUTER_API_KEY"},
			"context": {"in_turn_keep_recent": 8, "tail_high_fraction": 0.4}
		}`)
		// Project layer: keep_recent 3 (overrides 8). No fractions.
		projPath := write("proj.json", `{
			"context": {"in_turn_keep_recent": 3}
		}`)
		cfg := loadMergedConfig(userPath, projPath)
		if cfg == nil {
			t.Fatal("merged config is nil")
		}
		if got := cfg.inTurnKeepRecent(); got != 3 {
			t.Errorf("inTurnKeepRecent() = %d, want the project override 3", got)
		}
		// The user's tail_high_fraction (0.4) must be inherited (not clobbered
		// by the project's keep_recent-only context block).
		if cfg.Context.TailHighFraction == nil || *cfg.Context.TailHighFraction != 0.4 {
			t.Errorf("tail_high_fraction = %v, want the inherited user 0.4", cfg.Context.TailHighFraction)
		}
		// in_turn_demotion unset at both layers → enabled by default.
		if !cfg.inTurnDemotionEnabled() {
			t.Error("inTurnDemotionEnabled() = false, want true (absent at both layers → default on)")
		}
	})

	t.Run("project in_turn_demotion:false overrides user on", func(t *testing.T) {
		dir := t.TempDir()
		write := func(name, body string) string {
			p := filepath.Join(dir, name)
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			return p
		}
		userPath := write("user.json", `{
			"backend": {"type": "openrouter", "endpoint": "https://openrouter.ai/api/v1", "key_env": "OPENROUTER_API_KEY"},
			"context": {"in_turn_demotion": true}
		}`)
		projPath := write("proj.json", `{
			"context": {"in_turn_demotion": false}
		}`)
		cfg := loadMergedConfig(userPath, projPath)
		if cfg == nil {
			t.Fatal("merged config is nil")
		}
		if cfg.inTurnDemotionEnabled() {
			t.Error("inTurnDemotionEnabled() = true, want false (project in_turn_demotion:false overrides user true)")
		}
	})

	t.Run("user-only: project layer absent inherits user", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "user.json")
		if err := os.WriteFile(p, []byte(`{
			"backend": {"type": "openrouter", "endpoint": "https://openrouter.ai/api/v1", "key_env": "OPENROUTER_API_KEY"},
			"context": {"in_turn_keep_recent": 9}
		}`), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg := loadMergedConfig(p, filepath.Join(dir, "missing.json"))
		if cfg == nil {
			t.Fatal("merged config is nil")
		}
		if got := cfg.inTurnKeepRecent(); got != 9 {
			t.Errorf("inTurnKeepRecent() = %d, want the inherited user 9", got)
		}
	})
}

// --- helpers ---

// intPtr is the int→*int test helper (mirrors boolPtr in session_core_test.go
// and floatPtr in config_full_configurability_test.go).
func intPtr(i int) *int { return &i }

// contains is a small strings.Contains shim kept local so this test file's
// imports stay to the stdlib the step calls for (os, path/filepath, testing)
// plus fmt.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// silence unused fmt import in the override tests (it is imported for
// consistency with the other config test files and is intentionally unused).
var _ = fmt.Sprintf
