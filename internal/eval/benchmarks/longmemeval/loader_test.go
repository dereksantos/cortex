package longmemeval

import (
	"context"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/eval/benchmarks"
)

// withFixture swaps the loadFile hook to the local sample file and
// restores the prior hook on cleanup.
func withFixture(t *testing.T) {
	t.Helper()
	prev := loadFile
	loadFile = func() (string, error) { return "testdata/oracle_sample.json", nil }
	t.Cleanup(func() { loadFile = prev })
}

func TestLoad_DefaultsToOracleAndCortexStrategy(t *testing.T) {
	withFixture(t)
	got, err := Load(context.Background(), benchmarks.LoadOpts{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// 5 questions × 1 strategy.
	if len(got) != 5 {
		t.Fatalf("len=%d want 5", len(got))
	}
	for _, inst := range got {
		pl, ok := inst.Payload.(InstancePayload)
		if !ok {
			t.Fatalf("payload type = %T want InstancePayload", inst.Payload)
		}
		if pl.Strategy != StrategyCortex {
			t.Errorf("default strategy = %q want %q", pl.Strategy, StrategyCortex)
		}
	}
}

func TestLoad_StrategyMultipliesInstances(t *testing.T) {
	withFixture(t)
	got, err := Load(context.Background(), benchmarks.LoadOpts{
		Limit: 3,
		Filter: map[string]string{
			FilterStrategy: "baseline,cortex",
		},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// 3 questions × 2 strategies = 6 instances.
	if len(got) != 6 {
		t.Fatalf("len=%d want 6", len(got))
	}
	// Each question appears once per strategy.
	qStrats := map[string]map[string]int{}
	for _, inst := range got {
		pl := inst.Payload.(InstancePayload)
		if _, ok := qStrats[pl.Q.QuestionID]; !ok {
			qStrats[pl.Q.QuestionID] = map[string]int{}
		}
		qStrats[pl.Q.QuestionID][pl.Strategy]++
	}
	if len(qStrats) != 3 {
		t.Errorf("distinct questions=%d want 3", len(qStrats))
	}
	for qid, strats := range qStrats {
		if strats[StrategyBaseline] != 1 || strats[StrategyCortex] != 1 {
			t.Errorf("%s strategy counts = %v want {baseline:1, cortex:1}", qid, strats)
		}
	}
}

func TestLoad_QuestionTypeFilterByAxis(t *testing.T) {
	withFixture(t)
	got, err := Load(context.Background(), benchmarks.LoadOpts{
		Filter: map[string]string{FilterQuestionType: AxisAbstention},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len=%d want 1", len(got))
	}
	pl := got[0].Payload.(InstancePayload)
	if pl.Q.QuestionType != "abstention" {
		t.Errorf("question_type=%q want abstention", pl.Q.QuestionType)
	}
}

func TestLoad_QuestionTypeFilterByRawString(t *testing.T) {
	withFixture(t)
	// Raw upstream string also accepted.
	got, err := Load(context.Background(), benchmarks.LoadOpts{
		Filter: map[string]string{FilterQuestionType: "temporal-reasoning"},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len=%d want 1", len(got))
	}
	pl := got[0].Payload.(InstancePayload)
	if pl.Q.QuestionType != "temporal-reasoning" {
		t.Errorf("question_type=%q", pl.Q.QuestionType)
	}
}

func TestLoad_QuestionTypeFilterMultipleAxes(t *testing.T) {
	withFixture(t)
	got, err := Load(context.Background(), benchmarks.LoadOpts{
		Filter: map[string]string{FilterQuestionType: "single-hop,multi-hop"},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len=%d want 2 (single-hop + multi-hop)", len(got))
	}
}

func TestLoad_LimitCapsQuestionCountNotCellCount(t *testing.T) {
	withFixture(t)
	got, err := Load(context.Background(), benchmarks.LoadOpts{
		Limit: 2,
		Filter: map[string]string{
			FilterStrategy: "baseline,cortex",
		},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// 2 questions × 2 strategies = 4. Confirms --limit caps questions.
	if len(got) != 4 {
		t.Fatalf("len=%d want 4", len(got))
	}
}

func TestLoad_RejectsSubsetSAndMWithPhaseBMessage(t *testing.T) {
	withFixture(t)
	for _, subset := range []string{SubsetS, SubsetM} {
		_, err := Load(context.Background(), benchmarks.LoadOpts{Subset: subset})
		if err == nil {
			t.Fatalf("subset=%q: want error, got nil", subset)
		}
		if !strings.Contains(err.Error(), "Phase B") {
			t.Errorf("subset=%q error %q should mention 'Phase B'", subset, err)
		}
	}
}

func TestLoad_RejectsUnknownSubset(t *testing.T) {
	withFixture(t)
	_, err := Load(context.Background(), benchmarks.LoadOpts{Subset: "bogus"})
	if err == nil {
		t.Fatal("want error on unknown subset")
	}
	if !strings.Contains(err.Error(), "unknown subset") {
		t.Errorf("err=%q should mention 'unknown subset'", err)
	}
}

func TestLoad_RejectsUnknownStrategy(t *testing.T) {
	withFixture(t)
	_, err := Load(context.Background(), benchmarks.LoadOpts{
		Filter: map[string]string{FilterStrategy: "cortex,frontier"},
	})
	if err == nil {
		t.Fatal("want error on unknown strategy")
	}
	if !strings.Contains(err.Error(), "unknown strategy") {
		t.Errorf("err=%q should mention 'unknown strategy'", err)
	}
}

func TestLoad_PreservesParallelArrayShape(t *testing.T) {
	withFixture(t)
	got, err := Load(context.Background(), benchmarks.LoadOpts{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, inst := range got {
		pl := inst.Payload.(InstancePayload)
		if len(pl.Q.HaystackSessionIDs) != len(pl.Q.HaystackDates) {
			t.Errorf("%s: session_ids=%d, dates=%d (must be parallel)", pl.Q.QuestionID, len(pl.Q.HaystackSessionIDs), len(pl.Q.HaystackDates))
		}
		if len(pl.Q.HaystackSessionIDs) != len(pl.Q.HaystackSessions) {
			t.Errorf("%s: session_ids=%d, sessions=%d (must be parallel)", pl.Q.QuestionID, len(pl.Q.HaystackSessionIDs), len(pl.Q.HaystackSessions))
		}
	}
}

func TestLoad_StableOrderByQuestionID(t *testing.T) {
	withFixture(t)
	got, err := Load(context.Background(), benchmarks.LoadOpts{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var ids []string
	for _, inst := range got {
		ids = append(ids, inst.Payload.(InstancePayload).Q.QuestionID)
	}
	want := []string{"qa_001", "qa_002", "qa_003", "qa_004", "qa_005"}
	for i, id := range want {
		if ids[i] != id {
			t.Errorf("ids[%d]=%q want %q (full=%v)", i, ids[i], id, ids)
		}
	}
}

func TestNormalizeAxis(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"single-session-user", AxisSingleHop},
		{"single-session-assistant", AxisSingleHop},
		{"single-session-preference", AxisSingleHop},
		{"multi-session", AxisMultiHop},
		{"temporal-reasoning", AxisTemporal},
		{"knowledge-update", AxisKnowledgeUpdate},
		{"abstention", AxisAbstention},
		// pass-through when input already normalized:
		{"single-hop", AxisSingleHop},
		{"multi-hop", AxisMultiHop},
		// unknown:
		{"some-future-type", ""},
		{"", ""},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := NormalizeAxis(tc.in); got != tc.want {
				t.Errorf("NormalizeAxis(%q) = %q want %q", tc.in, got, tc.want)
			}
		})
	}
}
