package main

import (
	"strings"
	"testing"
	"time"

	"github.com/dereksantos/cortex/internal/memory"
	"github.com/dereksantos/cortex/internal/style"
)

func TestListPicker(t *testing.T) {
	defer style.ForceColor(false)()
	items := []pickItem{{"a", "alpha model"}, {"b", "beta model"}, {"c", "gamma"}}
	p := newListPicker("pick", items, 0)

	if got := p.Lines(80); strings.Join(got, "|") != "> alpha model|  beta model|  gamma" {
		t.Errorf("rows = %q", got)
	}
	p.SetCursor(2)
	if p.SelectedID() != "c" {
		t.Errorf("SelectedID = %q, want c", p.SelectedID())
	}

	p.SetFilter("model")
	if p.Title() != "pick — filter: model" {
		t.Errorf("title = %q", p.Title())
	}
	if n := len(p.Lines(80)); n != 2 {
		t.Errorf("filter left %d rows, want 2", n)
	}
	if p.SelectedID() != "b" {
		t.Errorf("cursor past the narrowed list should land on its last row, got %q", p.SelectedID())
	}

	p.SetFilter("nothing")
	if p.Selected() != -1 || p.SelectedID() != "" {
		t.Errorf("empty match should select nothing, got %d %q", p.Selected(), p.SelectedID())
	}

	if p.Accepted() {
		t.Error("not accepted until Accept")
	}
	p.Accept()
	if !p.Accepted() {
		t.Error("Accept should stick")
	}
}

func TestListPickerClipsRows(t *testing.T) {
	defer style.ForceColor(false)()
	p := newListPicker("pick", []pickItem{{"x", strings.Repeat("long ", 20)}}, 0)
	if w := style.Width(p.Lines(30)[0]); w > 30 {
		t.Errorf("row is %d wide, want ≤ 30", w)
	}
}

func TestModelPickerMarksTheCurrentModel(t *testing.T) {
	defer style.ForceColor(false)()
	cs := &CortexSession{Request: &AgentRequest{Model: "qwen3.8-27b"}}
	cs.Study.Model = "qwen3.8-27b"
	p := newModelPicker(cs)
	rows := p.Lines(80)
	if len(rows) == 0 || rows[0] != "> qwen3.8-27b  (code, current; study)" {
		t.Errorf("rows = %q", rows)
	}
}

func TestMemoryPickerListsBothTiers(t *testing.T) {
	defer style.ForceColor(false)()
	now := time.Now()
	proj, err := memory.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	user, err := memory.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proj.Write("deploy-steps", "---\nhook: how staging deploys\n---\nrun make deploy", now); err != nil {
		t.Fatal(err)
	}
	if _, err := user.Write("prefers-tabs", "uses tabs", now); err != nil {
		t.Fatal(err)
	}
	cs := &CortexSession{memory: proj, userMemory: user}
	p := newMemoryPicker(cs)
	rows := strings.Join(p.Lines(120), "\n")
	for _, want := range []string{"deploy-steps", "(project · ", "prefers-tabs", "(user · "} {
		if !strings.Contains(rows, want) {
			t.Errorf("rows missing %q:\n%s", want, rows)
		}
	}
	body, err := readMemoryNote(cs, "user/prefers-tabs")
	if err != nil || !strings.Contains(body, "uses tabs") {
		t.Errorf("readMemoryNote = %q, %v", body, err)
	}
	if _, err := readMemoryNote(&CortexSession{}, "user/x"); err == nil {
		t.Error("a missing store should be an error, not a panic")
	}
}

func TestListPickerSelectIDAndTexts(t *testing.T) {
	defer style.ForceColor(true)()
	p := newListPicker("pick", []pickItem{{"x/a", "alpha"}, {"x/b", "beta"}, {"x/g", "gamma"}}, 0)
	p.SetFilter("a") // alpha, beta, gamma all contain "a"
	p.SelectID("x/g")
	if p.SelectedID() != "x/g" {
		t.Errorf("SelectID: got %q", p.SelectedID())
	}
	for _, l := range p.Texts() {
		if style.Strip(l) != l || strings.HasPrefix(l, ">") {
			t.Errorf("plain listing must carry no styling or cursor mark: %q", l)
		}
	}
}

func TestModelPickerTitleShowsBindings(t *testing.T) {
	cs := &CortexSession{Request: &AgentRequest{Model: "qwen3.8-27b", BaseURL: "http://chatterbox:4000"}}
	cs.Study.Model, cs.Study.Endpoint = "qwen3.8-27b", "http://chatterbox:4000"
	if title := newModelPicker(cs).Title(); !strings.Contains(title, "code: qwen3.8-27b @ http://chatterbox:4000") {
		t.Errorf("title should keep the bindings bare /model used to print: %q", title)
	}
}
