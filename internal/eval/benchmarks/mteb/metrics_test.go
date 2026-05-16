package mteb

import (
	"math"
	"testing"
)

// floatNear is the per-assertion tolerance for metric comparisons.
// 1e-9 is tighter than IEEE-754 float64 noise on the integer-relevance
// arithmetic these tests do, but loose enough to survive log2/discount
// rounding in the NDCG path.
const floatNear = 1e-9

func nearlyEqual(a, b float64) bool {
	return math.Abs(a-b) < floatNear
}

// --- NDCG@k ---

// TestNDCGPerfectRanking — every relevant doc appears at the top in
// descending relevance order, so DCG == IDCG and NDCG == 1.0.
// Hand-computed:
//
//	DCG  = 2/log2(2) + 1/log2(3) + 0 + 0
//	     = 2/1 + 1/1.5849625 = 2 + 0.6309297535714574 = 2.6309297535714574
//	IDCG = same (this IS the ideal ranking)
//	NDCG = 1.0
func TestNDCGPerfectRanking(t *testing.T) {
	retrieved := []string{"a", "b", "c", "d"}
	qrels := map[string]int{"a": 2, "b": 1}
	got := NDCG(retrieved, qrels, 10)
	if !nearlyEqual(got, 1.0) {
		t.Fatalf("NDCG perfect = %v, want 1.0", got)
	}
}

// TestNDCGReversedRanking — relevant docs at the very bottom of the
// list (still inside k). DCG is the sum of relevances over a slow
// discount; IDCG is the perfect ordering.
//
// retrieved = [c, d, b, a] (irrelevant, irrelevant, rel=1 at rank 3, rel=2 at rank 4)
// qrels     = {a:2, b:1}
//
// DCG  = 0 + 0 + 1/log2(4) + 2/log2(5)
//
//	= 0 + 0 + 0.5 + 0.86134... = 1.36134752...
//
// IDCG = 2/log2(2) + 1/log2(3) = 2 + 0.6309297535714574 = 2.6309297535714574
// NDCG = 1.36134752 / 2.6309297535714574 = 0.51745...
func TestNDCGReversedRanking(t *testing.T) {
	retrieved := []string{"c", "d", "b", "a"}
	qrels := map[string]int{"a": 2, "b": 1}
	got := NDCG(retrieved, qrels, 10)
	dcg := 1.0/math.Log2(4) + 2.0/math.Log2(5)
	idcg := 2.0/math.Log2(2) + 1.0/math.Log2(3)
	want := dcg / idcg
	if !nearlyEqual(got, want) {
		t.Fatalf("NDCG reversed = %v, want %v", got, want)
	}
}

// TestNDCGEmptyRetrieved — no retrieval at all is NDCG=0.
func TestNDCGEmptyRetrieved(t *testing.T) {
	got := NDCG(nil, map[string]int{"a": 1}, 10)
	if got != 0 {
		t.Errorf("NDCG nil retrieved = %v, want 0", got)
	}
	got = NDCG([]string{}, map[string]int{"a": 1}, 10)
	if got != 0 {
		t.Errorf("NDCG empty retrieved = %v, want 0", got)
	}
}

// TestNDCGRetrievedShorterThanK — we asked for k=10 but only got 3
// results. NDCG is computed against IDCG also truncated at min(k, #rel).
// retrieved = [a, b, c] with qrels {a:2, b:1, x:1} → x never appears.
//
// DCG  = 2/log2(2) + 1/log2(3) + 0/log2(4) = 2 + 0.6309297535714574 = 2.6309297535714574
// IDCG over qrels sorted: [2, 1, 1] but truncated to min(k=10, len(retrieved)=3) = 3:
//
//	IDCG = 2/log2(2) + 1/log2(3) + 1/log2(4)
//	     = 2 + 0.6309297535714574 + 0.5
//	     = 3.1309297535714574
//
// NDCG = 2.6309297535714574 / 3.1309297535714574 = 0.8402...
func TestNDCGRetrievedShorterThanK(t *testing.T) {
	retrieved := []string{"a", "b", "c"}
	qrels := map[string]int{"a": 2, "b": 1, "x": 1}
	got := NDCG(retrieved, qrels, 10)
	dcg := 2.0/math.Log2(2) + 1.0/math.Log2(3)
	idcg := 2.0/math.Log2(2) + 1.0/math.Log2(3) + 1.0/math.Log2(4)
	want := dcg / idcg
	if !nearlyEqual(got, want) {
		t.Fatalf("NDCG short retrieved = %v, want %v", got, want)
	}
}

// TestNDCGGradedRelevance — NFCorpus uses 0/1/2 grades. Confirm a
// graded mid-quality result outranks an ungraded one.
//
// retrieved = [a, b, c] with qrels {a:2, b:1, c:0 (i.e. not in qrels)}
// qrels actually only has a:2, b:1 (c is "implicitly 0").
func TestNDCGGradedRelevance(t *testing.T) {
	retrieved := []string{"a", "b", "c"}
	qrels := map[string]int{"a": 2, "b": 1}
	got := NDCG(retrieved, qrels, 3)
	dcg := 2.0/math.Log2(2) + 1.0/math.Log2(3) // c contributes 0
	idcg := 2.0/math.Log2(2) + 1.0/math.Log2(3)
	want := dcg / idcg
	if !nearlyEqual(got, want) {
		t.Fatalf("NDCG graded = %v, want %v (DCG=%v IDCG=%v)", got, want, dcg, idcg)
	}
	if got != 1.0 {
		t.Errorf("NDCG@3 perfect-within-3 should be 1.0; got %v", got)
	}
}

// TestNDCGNoRelevantInQrels — empty qrels (the query has no judged
// relevant docs at all). Convention: skip the query (return NDCG=0 so
// the aggregator can choose to drop it). Tested explicitly so callers
// know the contract.
func TestNDCGNoRelevantInQrels(t *testing.T) {
	got := NDCG([]string{"a", "b"}, map[string]int{}, 10)
	if got != 0 {
		t.Errorf("NDCG empty qrels = %v, want 0", got)
	}
}

// --- MRR@k ---

// TestMRRFirstHit — relevant doc at rank 1 → MRR = 1/1 = 1.0
func TestMRRFirstHit(t *testing.T) {
	got := MRR([]string{"a", "b"}, map[string]int{"a": 1}, 10)
	if !nearlyEqual(got, 1.0) {
		t.Errorf("MRR first-hit = %v, want 1.0", got)
	}
}

// TestMRRThirdHit — relevant doc at rank 3 → MRR = 1/3
func TestMRRThirdHit(t *testing.T) {
	got := MRR([]string{"x", "y", "a"}, map[string]int{"a": 1}, 10)
	if !nearlyEqual(got, 1.0/3) {
		t.Errorf("MRR rank-3 = %v, want %v", got, 1.0/3)
	}
}

// TestMRRMissing — no relevant doc in top-k → MRR = 0
func TestMRRMissing(t *testing.T) {
	got := MRR([]string{"x", "y", "z"}, map[string]int{"a": 1}, 10)
	if got != 0 {
		t.Errorf("MRR miss = %v, want 0", got)
	}
}

// TestMRRBeyondK — relevant doc exists in retrieved but beyond k → MRR = 0
func TestMRRBeyondK(t *testing.T) {
	got := MRR([]string{"x", "y", "a"}, map[string]int{"a": 1}, 2)
	if got != 0 {
		t.Errorf("MRR beyond-k = %v, want 0", got)
	}
}

// TestMRREmptyRetrieved — nil/empty retrieved → MRR = 0
func TestMRREmptyRetrieved(t *testing.T) {
	if got := MRR(nil, map[string]int{"a": 1}, 10); got != 0 {
		t.Errorf("MRR nil = %v, want 0", got)
	}
	if got := MRR([]string{}, map[string]int{"a": 1}, 10); got != 0 {
		t.Errorf("MRR empty = %v, want 0", got)
	}
}

// TestMRRGradedRelevance — any positive relevance counts; 0 doesn't.
func TestMRRGradedRelevance(t *testing.T) {
	got := MRR([]string{"zero", "a"}, map[string]int{"zero": 0, "a": 2}, 10)
	if !nearlyEqual(got, 0.5) {
		t.Errorf("MRR graded = %v, want 0.5 (skip rank 0)", got)
	}
}

// --- Recall@k ---

// TestRecallAllHit — all relevant docs retrieved within k → Recall = 1.0
func TestRecallAllHit(t *testing.T) {
	got := Recall([]string{"a", "b", "c"}, map[string]int{"a": 1, "b": 2}, 10)
	if !nearlyEqual(got, 1.0) {
		t.Errorf("Recall all-hit = %v, want 1.0", got)
	}
}

// TestRecallPartial — 1 of 2 relevant docs retrieved → Recall = 0.5
func TestRecallPartial(t *testing.T) {
	got := Recall([]string{"a", "x", "y"}, map[string]int{"a": 1, "b": 1}, 10)
	if !nearlyEqual(got, 0.5) {
		t.Errorf("Recall partial = %v, want 0.5", got)
	}
}

// TestRecallNoneHit — none of the relevant docs in top-k → Recall = 0
func TestRecallNoneHit(t *testing.T) {
	got := Recall([]string{"x", "y"}, map[string]int{"a": 1, "b": 1}, 10)
	if got != 0 {
		t.Errorf("Recall none = %v, want 0", got)
	}
}

// TestRecallEmptyQrels — denominator zero → Recall = 0 (no judged docs;
// can't measure recall).
func TestRecallEmptyQrels(t *testing.T) {
	got := Recall([]string{"a", "b"}, map[string]int{}, 10)
	if got != 0 {
		t.Errorf("Recall empty qrels = %v, want 0", got)
	}
}

// TestRecallKTruncation — relevant doc beyond k is not counted.
//
// retrieved = [x, y, a, b] with k=2, qrels {a:1, b:1}
// Within k=2: no relevant → Recall = 0/2 = 0.
func TestRecallKTruncation(t *testing.T) {
	got := Recall([]string{"x", "y", "a", "b"}, map[string]int{"a": 1, "b": 1}, 2)
	if got != 0 {
		t.Errorf("Recall k-truncation = %v, want 0", got)
	}
}

// TestRecallSkipsZeroGrades — a doc with relevance grade 0 in qrels is
// NOT considered relevant. Confirm Recall doesn't count it.
//
// retrieved = [zero], qrels = {zero: 0, a: 1}
// Recall = 0 / 1 = 0.
func TestRecallSkipsZeroGrades(t *testing.T) {
	got := Recall([]string{"zero"}, map[string]int{"zero": 0, "a": 1}, 10)
	if got != 0 {
		t.Errorf("Recall zero-grade = %v, want 0", got)
	}
}

// TestRecallEmptyRetrieved — nothing retrieved → Recall = 0.
func TestRecallEmptyRetrieved(t *testing.T) {
	got := Recall(nil, map[string]int{"a": 1}, 10)
	if got != 0 {
		t.Errorf("Recall nil = %v, want 0", got)
	}
	got = Recall([]string{}, map[string]int{"a": 1}, 10)
	if got != 0 {
		t.Errorf("Recall empty = %v, want 0", got)
	}
}

// TestKZeroIsTreatedAsLength — a k≤0 argument is interpreted as "use
// the full retrieved length". This matches BEIR convention and avoids
// silently returning 0 when a caller forgets to set k.
func TestKZeroIsTreatedAsLength(t *testing.T) {
	retrieved := []string{"a", "b"}
	qrels := map[string]int{"a": 1, "b": 1}
	if got := Recall(retrieved, qrels, 0); !nearlyEqual(got, 1.0) {
		t.Errorf("Recall k=0 = %v, want 1.0 (treat as len(retrieved))", got)
	}
	if got := Recall(retrieved, qrels, -1); !nearlyEqual(got, 1.0) {
		t.Errorf("Recall k=-1 = %v, want 1.0", got)
	}
}
