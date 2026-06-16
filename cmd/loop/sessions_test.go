package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTestSession(t *testing.T, dir, id string, lines ...string) {
	t.Helper()
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(body), 0644); err != nil {
		t.Fatalf("write session %s: %v", id, err)
	}
}

func TestListSessions(t *testing.T) {
	dir := t.TempDir()
	// Older session first (ids are timestamps; lexicographic order = chronological).
	// Transcript lines embed Message (role/content promoted to top level).
	writeTestSession(t, dir, "20260101-000000",
		`{"kind":"message","role":"system","content":"sys"}`,
		`{"kind":"message","role":"user","content":"first prompt here"}`,
		`{"kind":"message","role":"assistant","content":"reply"}`,
		`{"kind":"retrieval","query":"x"}`, // not a core message
	)
	writeTestSession(t, dir, "20260202-000000",
		`{"kind":"message","role":"system","content":"sys"}`,
		`{"kind":"message","role":"user","content":"newer\nsecond line"}`,
	)
	// A non-session file must be ignored.
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore me"), 0644)

	infos, err := listSessions(dir, 0)
	if err != nil {
		t.Fatalf("listSessions: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("got %d sessions, want 2", len(infos))
	}
	// Newest first.
	if infos[0].ID != "20260202-000000" || infos[1].ID != "20260101-000000" {
		t.Errorf("order = [%s, %s], want newest first", infos[0].ID, infos[1].ID)
	}
	// Core message count excludes system + retrieval entries.
	if infos[1].Messages != 2 {
		t.Errorf("older session msgs = %d, want 2 (1 user + 1 assistant)", infos[1].Messages)
	}
	if infos[1].First != "first prompt here" {
		t.Errorf("older first prompt = %q", infos[1].First)
	}
	// First prompt is the first line only.
	if infos[0].First != "newer" {
		t.Errorf("newer first prompt = %q, want first line only", infos[0].First)
	}
}

func TestListSessionsLimit(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"20260101-000001", "20260101-000002", "20260101-000003"} {
		writeTestSession(t, dir, id, `{"kind":"message","role":"user","content":"hi"}`)
	}
	infos, err := listSessions(dir, 2)
	if err != nil {
		t.Fatalf("listSessions: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("got %d, want 2 (limit)", len(infos))
	}
	if infos[0].ID != "20260101-000003" {
		t.Errorf("first = %s, want newest", infos[0].ID)
	}
}

func TestListSessionsNoDir(t *testing.T) {
	if _, err := listSessions(filepath.Join(t.TempDir(), "missing"), 0); err == nil {
		t.Fatal("expected error for missing sessions dir")
	}
}

func TestRelTime(t *testing.T) {
	now := time.Now()
	tests := []struct {
		t    time.Time
		want string
	}{
		{time.Time{}, "?"},
		{now.Add(-30 * time.Second), "just now"},
		{now.Add(-5 * time.Minute), "5m ago"},
		{now.Add(-3 * time.Hour), "3h ago"},
		{now.Add(-2 * 24 * time.Hour), "2d ago"},
	}
	for _, tt := range tests {
		if got := relTime(tt.t); got != tt.want {
			t.Errorf("relTime(%v) = %q, want %q", tt.t, got, tt.want)
		}
	}
}

func TestFirstLine(t *testing.T) {
	if got := firstLine("  hello world  "); got != "hello world" {
		t.Errorf("got %q", got)
	}
	if got := firstLine("line one\nline two"); got != "line one" {
		t.Errorf("got %q, want first line", got)
	}
}
