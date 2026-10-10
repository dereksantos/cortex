package main

import "testing"

// TestFleetDefaults pins the bench's default model bindings to the live
// chatterbox fleet: the code role points at the current default coder and
// the study role at the fleet's study alias. When the fleet changes, update
// these — a stale default sends runs at models that no longer exist.
func TestFleetDefaults(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"code role default", defaultModel, "qwen3.8-27b"},
		{"study role default", defaultStudyModel, "study"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("%s: got %q, want %q", tc.name, tc.got, tc.want)
			}
		})
	}
}
