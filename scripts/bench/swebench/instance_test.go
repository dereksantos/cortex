package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func testInstances() []Instance {
	var out []Instance
	for _, id := range []string{"a__a-1", "b__b-2", "c__c-3", "d__d-4", "e__e-5"} {
		out = append(out, Instance{InstanceID: id, BaseCommit: "abc", Image: "img:" + id, Repo: "r/" + id})
	}
	return out
}

func TestSelectInstances(t *testing.T) {
	all := testInstances()
	tests := []struct {
		name    string
		ids     []string
		n       int
		seed    string
		wantLen int
		wantIDs []string
		wantErr bool
	}{
		{name: "explicit ids keep order", ids: []string{"c__c-3", "a__a-1"}, wantIDs: []string{"c__c-3", "a__a-1"}, wantLen: 2},
		{name: "unknown id errors", ids: []string{"nope"}, wantErr: true},
		{name: "n caps the seeded selection", n: 2, seed: "106", wantLen: 2},
		{name: "n=0 means all", n: 0, seed: "106", wantLen: 5},
		{name: "n larger than set", n: 50, seed: "x", wantLen: 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SelectInstances(all, tt.ids, tt.n, tt.seed)
			if tt.wantErr {
				if err == nil {
					t.Fatal("want error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tt.wantLen {
				t.Fatalf("len=%d want %d", len(got), tt.wantLen)
			}
			if tt.wantIDs != nil && !reflect.DeepEqual(ids(got), tt.wantIDs) {
				t.Errorf("ids=%v want %v", ids(got), tt.wantIDs)
			}
		})
	}
}

// The seeded selection must be a pure function of (seed, id set): same seed
// → same order regardless of input order; a different seed reorders.
func TestSelectInstancesDeterministic(t *testing.T) {
	all := testInstances()
	rev := append([]Instance(nil), all...)
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	a, _ := SelectInstances(all, nil, 0, "106")
	b, _ := SelectInstances(rev, nil, 0, "106")
	if !reflect.DeepEqual(ids(a), ids(b)) {
		t.Errorf("selection depends on input order: %v vs %v", ids(a), ids(b))
	}
	// Ranking is by sha256(seed:id): verify the first pick directly.
	best := ""
	for _, in := range all {
		if best == "" || seedKey("106", in.InstanceID) < seedKey("106", best) {
			best = in.InstanceID
		}
	}
	if a[0].InstanceID != best {
		t.Errorf("first=%s want %s", a[0].InstanceID, best)
	}
}

// The prompt carries the issue text and nothing a submission may not use.
// LoadInstances never decodes the forbidden fields, so a dataset row that
// has them cannot leak them.
func TestLoadInstancesDropsForbiddenFields(t *testing.T) {
	row := `{"instance_id":"x__x-1","repo":"x/x","base_commit":"deadbeef","image":"img","problem_statement":"It breaks.",` +
		`"hints_text":"HINT-SECRET","patch":"GOLD-PATCH","test_patch":"TEST-PATCH","FAIL_TO_PASS":"[\"test_f2p\"]","PASS_TO_PASS":"[]"}`
	path := filepath.Join(t.TempDir(), "ds.jsonl")
	if err := os.WriteFile(path, []byte(row+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ins, err := LoadInstances(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(ins) != 1 {
		t.Fatalf("got %d instances", len(ins))
	}
	prompt := BuildPrompt(ins[0])
	if !strings.Contains(prompt, "It breaks.") {
		t.Error("prompt must contain the problem statement")
	}
	for _, forbidden := range []string{"HINT-SECRET", "GOLD-PATCH", "TEST-PATCH", "test_f2p"} {
		if strings.Contains(prompt, forbidden) {
			t.Errorf("prompt leaks %q", forbidden)
		}
	}
}

func TestLoadInstancesRejectsIncompleteRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ds.jsonl")
	if err := os.WriteFile(path, []byte(`{"instance_id":"x","image":"i"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadInstances(path); err == nil {
		t.Error("row without base_commit must be rejected")
	}
}

func TestContainerName(t *testing.T) {
	got := containerName("20260927-120000", "django__django-11099")
	if !strings.HasPrefix(got, "cortex-swebench-") {
		t.Errorf("name %q lacks the ownership prefix", got)
	}
	if strings.ContainsAny(got, "/: ") {
		t.Errorf("name %q has characters docker rejects", got)
	}
}
