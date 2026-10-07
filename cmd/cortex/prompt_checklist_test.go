// prompt_checklist_test.go — issue #220: per-item checklist accounting in
// the final answer (part 2 of #128).
//
// taskChecklistItems is pure (no session, no config), so it is tested
// table-driven directly — the no-checklist, checklist, and fenced-code
// cases the step calls for. TestTaskPromptCarriesChecklistPrinciple pins
// the intent of the prompt wording: the model-facing task prompt carries
// the per-item accounting principle (checklistAccountingPrinciple) ONLY
// when the task actually has a checklist, and an aggregate "all met" is
// never the account it asks for.
package main

import (
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// taskChecklistItems
// ---------------------------------------------------------------------------

func TestTaskChecklistItems(t *testing.T) {
	tests := []struct {
		name string
		task string
		want []string // nil when the task has no checklist items
	}{
		{
			name: "no checklist: plain prose",
			task: "Add a status line to the REPL that shows the current model.\n",
			want: nil,
		},
		{
			name: "no checklist: prose mentions the words but has no checkbox lines",
			task: "The issue asks that every checklist item be accounted for.\nMake it so.\n",
			want: nil,
		},
		{
			name: "items: two unchecked",
			task: "- [ ] add the helper\n- [ ] add the tests\n",
			want: []string{"add the helper", "add the tests"},
		},
		{
			name: "items: mixed checked and unchecked states, either case, kept in order",
			task: "- [x] scaffold\n- [X] wire\n- [ ] verify\n",
			want: []string{"scaffold", "wire", "verify"},
		},
		{
			name: "items: asterisk and plus bullets are checkboxes too",
			task: "* [ ] alpha\n+ [ ] beta\n",
			want: []string{"alpha", "beta"},
		},
		{
			name: "items: item text may itself contain brackets and dashes",
			task: "- [x] handle - [ ] markers in prose\n",
			want: []string{"handle - [ ] markers in prose"},
		},
		{
			name: "fenced code ignored: a task that SHOWS checkbox syntax has no checklist",
			task: "Add a status line that renders a task like:\n```\n- [ ] example item\n```\n",
			want: nil,
		},
		{
			name: "fenced code ignored: ~~~ fences delimit the skipped span too",
			task: "```\n- [ ] only in the block\n~~~\n- [x] after the block\n~~~\n- [ ] this is outside again\n",
			want: nil,
		},
		{
			name: "fenced block closed: a real item AFTER a code block still counts",
			task: "Context:\n```\n- [ ] not an item\n```\n- [ ] a real item\n",
			want: []string{"a real item"},
		},
		{
			name: "items interleaved with prose lines, in order",
			task: "Overall goal first.\n- [ ] first item\nSome prose.\n- [x] second item\n",
			want: []string{"first item", "second item"},
		},
		{
			name: "indented checkbox lines count (CommonMark allows up to three spaces)",
			task: "  - [ ] indented item\n",
			want: []string{"indented item"},
		},
		{
			name: "crlf line endings",
			task: "- [ ] first\r\n- [ ] second\r\n",
			want: []string{"first", "second"},
		},
		{
			name: "empty item text (checkbox with nothing after) is skipped",
			task: "- [ ]\n- [ ] a real item\n",
			want: []string{"a real item"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := taskChecklistItems(tt.task)
			if len(got) != len(tt.want) {
				t.Fatalf("taskChecklistItems(%q) = %v (len %d), want %v (len %d)", tt.task, got, len(got), tt.want, len(tt.want))
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("taskChecklistItems(%q)[%d] = %q, want %q", tt.task, i, got[i], tt.want[i])
				}
			}
			// nil-vs-empty: a task with no checklist must yield nil (the
			// caller's "principle does not apply" signal), not an empty slice.
			if tt.want == nil && got != nil {
				t.Errorf("taskChecklistItems(%q) = non-nil empty %v, want nil", tt.task, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// taskPrompt — the model-facing prompt carries the principle iff there is a
// checklist to account for.
// ---------------------------------------------------------------------------

func TestTaskPromptCarriesChecklistPrinciple(t *testing.T) {
	t.Run("no checklist: the prompt is the task text unchanged", func(t *testing.T) {
		task := "Add a status line to the REPL."
		if got := taskPrompt(task); got != task {
			t.Errorf("taskPrompt(no checklist) = %q, want the task unchanged", got)
		}
	})

	t.Run("checklist: the prompt carries each item and the per-item principle", func(t *testing.T) {
		task := "Do the work.\n- [ ] add the helper\n- [x] add the tests\n"
		got := taskPrompt(task)
		if !strings.HasPrefix(got, task) {
			t.Fatalf("taskPrompt did not start with the task text:\n%s", got)
		}
		for _, want := range []string{
			"Task checklist",
			"- [ ] add the helper",
			"- [ ] add the tests",
			checklistAccountingPrinciple,
			"item by item",
			"with the evidence",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("taskPrompt lacks %q:\n%s", want, got)
			}
		}
	})
}

// TestTaskPromptPrincipleIsPerItemNotAggregate pins the intent of the
// principle wording (issue #220's "never an aggregate 'all met'"): the
// principle asks for each item named and evidenced — done with the evidence
// (a file and line, or a command and its result) or not done — and says an
// aggregate summary is not an account. It checks the PRINCIPLE the text
// states, not a recipe: which words, in what order, the final answer takes.
func TestTaskPromptPrincipleIsPerItemNotAggregate(t *testing.T) {
	p := checklistAccountingPrinciple
	for _, want := range []string{
		"item by item",
		"done",
		"with the evidence",
		"not done",
		"not an account",
	} {
		if !strings.Contains(strings.ToLower(p), want) {
			t.Errorf("checklistAccountingPrinciple lacks %q:\n%s", want, p)
		}
	}
	// The aggregate answer the issue rules out must never be sanctioned by
	// the wording: "all met" must not appear in the principle at all —
	// naming it would make the banned phrasing the one the model imitates.
	if strings.Contains(strings.ToLower(p), "all met") {
		t.Errorf("checklistAccountingPrinciple must not name the aggregate answer it rules out:\n%s", p)
	}
}

// ---------------------------------------------------------------------------
// checklistItemPresent / checklistMissingItems — the reply-vs-checklist
// check (issue #220 step 2). The reply's account of an item is per item
// through checklistItemPresent: every significant word of the item (lower-
// cased, punctuation-stripped, stop-words dropped, a leading "add" dropped)
// must appear in the reply — case-insensitive, punctuation-stripped, with a
// word-prefix match ("add" matches "added") — so the model accounts for an
// item in its own words, including an explicit "not done". A missing item
// is one the reply never names.
// ---------------------------------------------------------------------------

func TestChecklistItemPresent(t *testing.T) {
	cases := []struct {
		name  string
		reply string
		item  string
		want  bool
	}{
		{
			name:  "a rephrased reply that names the item in its own words",
			reply: "I added the helper.",
			item:  "add the helper",
			want:  true, // "add" matches "added" (word-prefix); "helper" is named
		},
		{
			name:  "whole phrase present verbatim",
			reply: "Done: add the helper, and add the tests.",
			item:  "add the helper",
			want:  true,
		},
		{
			name:  "case-insensitive",
			reply: "I will ADD THE HELPER now.",
			item:  "add the helper",
			want:  true,
		},
		{
			name:  "an explicit not-done account names the item too",
			reply: "The tests are deferred.",
			item:  "add the tests",
			want:  true, // "tests" is named — an account is an account, done or not
		},
		{
			name:  "item missing from the reply",
			reply: "I added the helper.",
			item:  "add the tests",
			want:  false,
		},
		{
			name:  "item named by an unrelated reply is missing",
			reply: "I wired the handler instead.",
			item:  "add the tests",
			want:  false,
		},
		{
			name:  "a reply word that only STARTS LIKE the item word does not match",
			reply: "The helpers are in place.",
			item:  "add the handle",
			want:  false, // the prefix rule is item-word → reply-word, never the other way
		},
		{
			name:  "shorter item named by a longer phrase in the reply",
			reply: "I wired the handler.",
			item:  "wire",
			want:  true,
		},
		{
			name:  "punctuation around the reply's word does not hide it",
			reply: "helper.go:12, done; tests deferred.",
			item:  "add the helper",
			want:  true,
		},
		{
			name:  "a leading 'add' prefix is dropped (form-verb), mid-item stays",
			reply: "The flag is back.",
			item:  "re-add the flag",
			want:  false, // "re" (from "re-add") is significant and never named
		},
		{
			name:  "empty item is always present (never dangles)",
			reply: "anything",
			item:  "",
			want:  true,
		},
		{
			name:  "stop-word-only item is always present",
			reply: "anything",
			item:  "the and",
			want:  true,
		},
		{
			name:  "empty reply, non-empty item is missing",
			reply: "",
			item:  "add the helper",
			want:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := checklistItemPresent(tc.reply, tc.item); got != tc.want {
				t.Errorf("checklistItemPresent(%q, %q) = %v, want %v", tc.reply, tc.item, got, tc.want)
			}
		})
	}
}

func TestChecklistMissingItems(t *testing.T) {
	tests := []struct {
		name  string
		task  string
		reply string
		want  []string // nil when nothing is missing (no checklist, or all met)
	}{
		{
			name:  "all items accounted for (each named, in the reply's own words)",
			task:  "- [ ] add the helper\n- [ ] add the tests\n",
			reply: "I added the helper (helper.go:12) — done; the tests are in helper_test.go:3, and they pass.",
			want:  nil,
		},
		{
			name:  "some not done: an item the reply explicitly names as deferred is NOT missing",
			task:  "- [ ] add the helper\n- [ ] add the tests\n",
			reply: "I added the helper (helper.go:12). The tests are deferred to the next task.",
			want:  nil, // the reply accounts for BOTH items — done or not done
		},
		{
			name:  "one item missing from the reply (never named)",
			task:  "- [ ] add the helper\n- [ ] add the tests\n",
			reply: "I added the helper (helper.go:12).",
			want:  []string{"add the tests"},
		},
		{
			name:  "several items missing, kept in task order",
			task:  "- [ ] scaffold\n- [ ] wire the handler\n- [ ] verify\n",
			reply: "scaffold (main.go) is done.",
			want:  []string{"wire the handler", "verify"},
		},
		{
			name:  "case-insensitive matching",
			task:  "- [ ] Add the Helper\n",
			reply: "done: add the helper (helper.go:12).",
			want:  nil,
		},
		{
			name:  "a task with no checklist yields nil",
			task:  "Add a status line to the REPL.\n",
			reply: "Done.",
			want:  nil,
		},
		{
			name:  "an empty reply misses every item",
			task:  "- [ ] add the helper\n- [ ] add the tests\n",
			reply: "",
			want:  []string{"add the helper", "add the tests"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checklistMissingItems(tt.task, tt.reply)
			if len(got) != len(tt.want) {
				t.Fatalf("checklistMissingItems(%q, %q) = %v (len %d), want %v (len %d)", tt.task, tt.reply, got, len(got), tt.want, len(tt.want))
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("checklistMissingItems[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
			if tt.want == nil && got != nil {
				t.Errorf("checklistMissingItems = non-nil empty %v, want nil", got)
			}
		})
	}
}

// TestReceiptRendersChecklistSection pins the measurement receipt's
// "checklist:" section (issue #220 step 2): it renders the task's checklist
// items the reply did not account for, after the verification section and
// before the unformatted line, and is omitted when there is no checklist or
// the reply met every item. A receipt with no facts at all still renders "".
func TestReceiptRendersChecklistSection(t *testing.T) {
	// The missing-item shape: verification plus a checklist the reply left
	// an item out of. The section sits after verification.
	got := turnReceipt{
		gitWorkspace: true,
		verification: []receiptVerification{
			{role: "test", command: "go test ./...", exitCode: 1, elapsed: 420 * time.Millisecond},
		},
		checklistMissing: []string{"add the tests"},
	}.render()
	for _, want := range []string{
		"files changed:",
		"verification:",
		"checklist (not accounted for in the reply):",
		"  - add the tests",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("render() =\n%s\nwant it to contain %q", got, want)
		}
	}
	// Order: the checklist section comes after verification.
	if iV, iC := strings.Index(got, "verification:"), strings.Index(got, "checklist"); iV < 0 || iC < 0 || iV > iC {
		t.Errorf("render() =\n%s\nwant the checklist section AFTER verification", got)
	}
	// No checklist measured (reply met every item, or no checklist): the
	// section is absent, and a fact-less receipt renders "".
	if got := (turnReceipt{gitWorkspace: true}).render(); strings.Contains(got, "checklist") {
		t.Errorf("render() with no checklist fact =\n%s\nwant no checklist section", got)
	}
	if got := (turnReceipt{checklistMissing: []string{}}).render(); got != "" {
		t.Errorf("render() of an empty checklist fact = %q, want \"\" (no facts measured)", got)
	}
}
