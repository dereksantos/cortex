package main

import (
	"fmt"
	"strings"

	"github.com/dereksantos/cortex/internal/memory"
	"github.com/dereksantos/cortex/internal/style"
)

// memoryNoteMeta is the memory store's listing row.
type memoryNoteMeta = memory.NoteMeta

// pickItem is one selectable row of a listPicker: the value the caller gets
// back (ID) and the row text shown and filtered on (Text).
type pickItem struct {
	ID, Text string
}

// listPicker is a filterable one-column picker for the inspector harness
// (docs/tui-polish.md, track 4) — the /model and /memory views. It follows the
// same contract as the /sessions picker (session_picker.go): the body holds
// only selectable rows, the title carries the filter, the harness owns keys
// and scrolling.
type listPicker struct {
	title    string
	items    []pickItem
	filter   string
	cursor   int
	accepted bool
}

// newListPicker builds a picker over items with the cursor on row start
// (clamped; -1 when there are no rows).
func newListPicker(title string, items []pickItem, start int) *listPicker {
	p := &listPicker{title: title, items: items, cursor: -1}
	p.SetCursor(start)
	return p
}

func (p *listPicker) Title() string {
	if p.filter == "" {
		return p.title
	}
	return p.title + " — filter: " + p.filter
}

func (p *listPicker) Filter() string { return p.filter }

func (p *listPicker) SetFilter(s string) {
	p.filter = s
	n := len(p.match())
	switch {
	case n == 0:
		p.cursor = -1
	case p.cursor < 0:
		p.cursor = 0
	case p.cursor > n-1:
		p.cursor = n - 1
	}
}

func (p *listPicker) SetCursor(i int) {
	n := len(p.match())
	switch {
	case n == 0:
		p.cursor = -1
	case i < 0:
		p.cursor = 0
	case i > n-1:
		p.cursor = n - 1
	default:
		p.cursor = i
	}
}

func (p *listPicker) Selected() int { return p.cursor }

func (p *listPicker) Accept()        { p.accepted = true }
func (p *listPicker) Accepted() bool { return p.accepted }

// Texts is every item's row text, unfiltered and unstyled — the plain
// listing a piped session prints.
func (p *listPicker) Texts() []string {
	out := make([]string, len(p.items))
	for i, it := range p.items {
		out[i] = it.Text
	}
	return out
}

// SelectID puts the cursor on the row with id among the current matches (a
// no-op when it isn't one) — returning from a note lands back on that note.
func (p *listPicker) SelectID(id string) {
	for i, it := range p.match() {
		if it.ID == id {
			p.cursor = i
			return
		}
	}
}

// SelectedID is the ID of the row under the cursor, "" when nothing matches.
func (p *listPicker) SelectedID() string {
	m := p.match()
	if p.cursor < 0 || p.cursor > len(m)-1 {
		return ""
	}
	return m[p.cursor].ID
}

// Lines renders one row per matching item, clipped to width, the selected row
// marked and bold (the mark carries it without color).
func (p *listPicker) Lines(width int) []string {
	m := p.match()
	rows := make([]string, 0, len(m))
	for i, it := range m {
		mark := "  "
		if i == p.cursor {
			mark = "> "
		}
		text := mark + it.Text
		if width > 0 {
			text = style.Clip(text, width)
		}
		if i == p.cursor {
			text = style.Paint(text, style.Strong)
		}
		rows = append(rows, text)
	}
	return rows
}

// match is the case-insensitive substring filter over row text, in order.
func (p *listPicker) match() []pickItem {
	if p.filter == "" {
		return p.items
	}
	needle := strings.ToLower(p.filter)
	var out []pickItem
	for _, it := range p.items {
		if strings.Contains(strings.ToLower(it.Text), needle) {
			out = append(out, it)
		}
	}
	return out
}

// newModelPicker lists the models this session knows (the code and study
// bindings plus the fleet — modelIDs, the /model completion source), the
// coder's current one first and marked, so Enter on the opening row is a
// no-op.
func newModelPicker(cs *CortexSession) *listPicker {
	var items []pickItem
	for _, id := range modelIDs(cs) {
		text := id
		var roles []string
		if id == cs.Request.Model {
			roles = append(roles, "code, current")
		}
		if id == cs.Study.Model {
			roles = append(roles, "study")
		}
		if len(roles) > 0 {
			text += "  (" + strings.Join(roles, "; ") + ")"
		}
		items = append(items, pickItem{ID: id, Text: text})
	}
	title := fmt.Sprintf("switch the code model — code: %s @ %s · study: %s @ %s",
		cs.Request.Model, cs.Request.BaseURL, cs.Study.Model, cs.Study.Endpoint)
	return newListPicker(title, items, 0)
}

// newMemoryPicker lists the session's notes — project tier, then user tier —
// as "name  hook  (tier · updated)". The row ID encodes tier and name
// ("project/<name>") so the opener knows which store to read.
func newMemoryPicker(cs *CortexSession) *listPicker {
	var items []pickItem
	add := func(tier string, metas []memoryNoteMeta) {
		for _, m := range metas {
			text := m.Name
			if m.Hook != "" {
				text += "  " + m.Hook
			}
			text += "  (" + tier + " · " + relTime(m.Updated) + ")"
			items = append(items, pickItem{ID: tier + "/" + m.Name, Text: text})
		}
	}
	if cs.memory != nil {
		if metas, err := cs.memory.List(); err == nil {
			add("project", metas)
		}
	}
	if cs.userMemory != nil {
		if metas, err := cs.userMemory.List(); err == nil {
			add("user", metas)
		}
	}
	return newListPicker("memory — enter opens a note", items, 0)
}

// readMemoryNote reads a note by the picker ID newMemoryPicker built.
func readMemoryNote(cs *CortexSession, id string) (string, error) {
	tier, name, _ := strings.Cut(id, "/")
	store := cs.memory
	if tier == "user" {
		store = cs.userMemory
	}
	if store == nil {
		return "", fmt.Errorf("no %s memory store", tier)
	}
	return store.Read(name)
}

// noteView shows one note's text in the inspector.
type noteView struct {
	id, body string
}

func (v noteView) Title() string { return "memory — " + v.id }

func (v noteView) Lines(width int) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimRight(v.body, "\n"), "\n") {
		out = append(out, style.Wrap(l, width)...)
	}
	return out
}
