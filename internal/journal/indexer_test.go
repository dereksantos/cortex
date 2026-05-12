package journal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"testing"
)

func writeNEntries(t *testing.T, dir string, n int, typ string) {
	t.Helper()
	w, err := NewWriter(WriterOpts{ClassDir: dir, Fsync: FsyncPerBatch})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	for i := 0; i < n; i++ {
		payload, _ := json.Marshal(map[string]int{"i": i})
		if _, err := w.Append(&Entry{Type: typ, Payload: payload}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestIndexer_ProjectsAllEntries(t *testing.T) {
	dir := newClassDir(t)
	writeNEntries(t, dir, 5, "capture.event")

	reg := NewRegistry()
	var seen []Offset
	reg.Register("capture.event", 1, func(e *Entry) error {
		seen = append(seen, e.Offset)
		return nil
	})

	ix := NewIndexer(IndexerOpts{ClassDir: dir, Registry: reg})
	n, err := ix.RunOnce()
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if n != 5 {
		t.Errorf("projected = %d, want 5", n)
	}
	wantOffsets := []Offset{1, 2, 3, 4, 5}
	if len(seen) != len(wantOffsets) {
		t.Fatalf("seen %v, want %v", seen, wantOffsets)
	}
	for i, o := range wantOffsets {
		if seen[i] != o {
			t.Errorf("seen[%d] = %d, want %d", i, seen[i], o)
		}
	}
	got, _ := ix.Cursor().Get()
	if got != 5 {
		t.Errorf("cursor after run = %d, want 5", got)
	}
}

func TestIndexer_ResumesFromCursor(t *testing.T) {
	dir := newClassDir(t)
	writeNEntries(t, dir, 3, "capture.event")

	reg := NewRegistry()
	calls := 0
	reg.Register("capture.event", 1, func(e *Entry) error {
		calls++
		return nil
	})
	ix := NewIndexer(IndexerOpts{ClassDir: dir, Registry: reg})
	if _, err := ix.RunOnce(); err != nil {
		t.Fatalf("first RunOnce: %v", err)
	}
	if calls != 3 {
		t.Errorf("first run calls = %d, want 3", calls)
	}

	// Append more, run again — should only project new entries.
	w, err := NewWriter(WriterOpts{ClassDir: dir, Fsync: FsyncPerBatch})
	if err != nil {
		t.Fatalf("NewWriter 2: %v", err)
	}
	for i := 0; i < 4; i++ {
		if _, err := w.Append(&Entry{Type: "capture.event", Payload: json.RawMessage("{}")}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	w.Close()

	calls = 0
	n, err := ix.RunOnce()
	if err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}
	if n != 4 {
		t.Errorf("second run projected = %d, want 4", n)
	}
	if calls != 4 {
		t.Errorf("second run calls = %d, want 4", calls)
	}
	got, _ := ix.Cursor().Get()
	if got != 7 {
		t.Errorf("cursor = %d, want 7", got)
	}
}

func TestIndexer_UnknownSkipPolicy(t *testing.T) {
	dir := newClassDir(t)
	writeNEntries(t, dir, 2, "future.event")

	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)
	ix := NewIndexer(IndexerOpts{
		ClassDir:  dir,
		Registry:  NewRegistry(), // empty — no projector knows future.event
		OnUnknown: UnknownLogAndSkip,
		Logger:    logger,
	})
	n, err := ix.RunOnce()
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if n != 2 {
		t.Errorf("acknowledged = %d, want 2", n)
	}
	got, _ := ix.Cursor().Get()
	if got != 2 {
		t.Errorf("cursor = %d, want 2 (skipped entries still advance)", got)
	}
	if !strings.Contains(buf.String(), "future.event") {
		t.Errorf("log missing skip message: %q", buf.String())
	}
}

func TestIndexer_UnknownErrorPolicy(t *testing.T) {
	dir := newClassDir(t)
	writeNEntries(t, dir, 2, "future.event")

	ix := NewIndexer(IndexerOpts{
		ClassDir:  dir,
		Registry:  NewRegistry(),
		OnUnknown: UnknownError,
	})
	_, err := ix.RunOnce()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown type") {
		t.Errorf("error message = %q, want contains 'unknown type'", err.Error())
	}
	got, _ := ix.Cursor().Get()
	if got != 0 {
		t.Errorf("cursor = %d, want 0 (no advance on error)", got)
	}
}

func TestIndexer_ProjectorErrorStopsRun(t *testing.T) {
	dir := newClassDir(t)
	writeNEntries(t, dir, 5, "capture.event")

	reg := NewRegistry()
	called := 0
	reg.Register("capture.event", 1, func(e *Entry) error {
		called++
		if called == 3 {
			return fmt.Errorf("intentional failure")
		}
		return nil
	})
	ix := NewIndexer(IndexerOpts{ClassDir: dir, Registry: reg})
	_, err := ix.RunOnce()
	if err == nil {
		t.Fatal("expected projector error")
	}
	got, _ := ix.Cursor().Get()
	if got != 2 {
		t.Errorf("cursor = %d, want 2 (advanced past 2 successful, stopped before 3rd)", got)
	}
}

func TestRegistry_RegisterLookup(t *testing.T) {
	r := NewRegistry()
	called := false
	r.Register("foo.bar", 1, func(*Entry) error { called = true; return nil })

	p, ok := r.Lookup("foo.bar", 1)
	if !ok {
		t.Fatal("Lookup failed for registered type")
	}
	if err := p(&Entry{}); err != nil {
		t.Fatalf("projector: %v", err)
	}
	if !called {
		t.Error("projector not invoked")
	}

	if _, ok := r.Lookup("foo.bar", 2); ok {
		t.Error("Lookup found wrong version")
	}
	if _, ok := r.Lookup("foo.baz", 1); ok {
		t.Error("Lookup found wrong type")
	}
	if !r.Known("foo.bar", 1) {
		t.Error("Known returned false for registered")
	}
	if r.Known("nope", 1) {
		t.Error("Known returned true for unregistered")
	}
}

func TestRegistry_HasType(t *testing.T) {
	r := NewRegistry()
	if r.HasType("x.y") {
		t.Error("HasType true for empty registry")
	}
	r.Register("x.y", 1, func(*Entry) error { return nil })
	if !r.HasType("x.y") {
		t.Error("HasType false after register")
	}
	if r.HasType("x.z") {
		t.Error("HasType true for different type")
	}
	// Prefix-collision guard: "x.yy" must not match "x.y".
	if r.HasType("x.yy") {
		t.Error("HasType matched on prefix only")
	}
}

func TestRegistry_Versions(t *testing.T) {
	r := NewRegistry()
	if v := r.Versions("x.y"); v != nil {
		t.Errorf("Versions on empty = %v, want nil", v)
	}
	r.Register("x.y", 3, func(*Entry) error { return nil })
	r.Register("x.y", 1, func(*Entry) error { return nil })
	r.Register("x.y", 2, func(*Entry) error { return nil })
	r.Register("other", 1, func(*Entry) error { return nil })
	got := r.Versions("x.y")
	want := []int{1, 2, 3}
	if len(got) != len(want) {
		t.Fatalf("Versions(x.y) = %v, want %v", got, want)
	}
	for i, v := range want {
		if got[i] != v {
			t.Errorf("Versions[%d] = %d, want %d", i, got[i], v)
		}
	}
}

func TestIndexer_DistinguishesUnknownTypeVsVersion(t *testing.T) {
	dir := newClassDir(t)
	w, err := NewWriter(WriterOpts{ClassDir: dir, Fsync: FsyncPerBatch})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	// Entry 1: completely unknown type.
	if _, err := w.Append(&Entry{Type: "alien.thing", Payload: []byte(`{}`)}); err != nil {
		t.Fatalf("Append alien: %v", err)
	}
	// Entry 2: known type, unsupported version.
	e2 := &Entry{Type: "capture.event", V: 99, Payload: []byte(`{}`)}
	if _, err := w.Append(e2); err != nil {
		t.Fatalf("Append v99: %v", err)
	}
	w.Close()

	reg := NewRegistry()
	reg.Register("capture.event", 1, func(*Entry) error { return nil })

	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)
	ix := NewIndexer(IndexerOpts{
		ClassDir:  dir,
		Registry:  reg,
		OnUnknown: UnknownLogAndSkip,
		Logger:    logger,
	})
	if _, err := ix.RunOnce(); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "alien.thing") || !strings.Contains(out, "unknown type") {
		t.Errorf("expected 'unknown type' log for alien.thing, got: %q", out)
	}
	if !strings.Contains(out, "v99") || !strings.Contains(out, "unsupported version") {
		t.Errorf("expected 'unsupported version' log for v99, got: %q", out)
	}
	if !strings.Contains(out, "[1]") {
		t.Errorf("expected registered-versions list in v99 log, got: %q", out)
	}
}

func TestRegistry_VersionsAreDistinct(t *testing.T) {
	r := NewRegistry()
	v1, v2 := false, false
	r.Register("x.y", 1, func(*Entry) error { v1 = true; return nil })
	r.Register("x.y", 2, func(*Entry) error { v2 = true; return nil })

	p1, _ := r.Lookup("x.y", 1)
	p2, _ := r.Lookup("x.y", 2)
	_ = p1(&Entry{})
	_ = p2(&Entry{})
	if !v1 || !v2 {
		t.Errorf("versions not distinct: v1=%v v2=%v", v1, v2)
	}
}
