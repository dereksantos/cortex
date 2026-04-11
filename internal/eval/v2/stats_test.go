package eval

import (
	"math"
	"testing"
)

func TestPearsonCorrelation(t *testing.T) {
	tests := []struct {
		name    string
		x, y    []float64
		wantMin float64
		wantMax float64
	}{
		{
			"perfect positive",
			[]float64{1, 2, 3, 4, 5},
			[]float64{2, 4, 6, 8, 10},
			0.99, 1.01,
		},
		{
			"perfect negative",
			[]float64{1, 2, 3, 4, 5},
			[]float64{10, 8, 6, 4, 2},
			-1.01, -0.99,
		},
		{
			"no correlation",
			[]float64{1, 2, 3, 4, 5},
			[]float64{3, 1, 4, 2, 5},
			-0.5, 0.5,
		},
		{
			"strong positive",
			[]float64{0.32, 0.61, 0.84, 0.89},
			[]float64{0.41, 0.67, 0.82, 0.88},
			0.95, 1.01,
		},
		{
			"too few points",
			[]float64{1},
			[]float64{2},
			-0.01, 0.01,
		},
		{
			"zero variance",
			[]float64{5, 5, 5},
			[]float64{1, 2, 3},
			-0.01, 0.01,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PearsonCorrelation(tt.x, tt.y)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("PearsonCorrelation() = %.4f, want [%.2f, %.2f]", got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestSpearmanCorrelation(t *testing.T) {
	tests := []struct {
		name    string
		x, y    []float64
		wantMin float64
		wantMax float64
	}{
		{
			"perfect monotonic",
			[]float64{1, 2, 3, 4, 5},
			[]float64{10, 20, 30, 40, 50},
			0.99, 1.01,
		},
		{
			"monotonic but nonlinear",
			[]float64{1, 2, 3, 4, 5},
			[]float64{1, 4, 9, 16, 25},
			0.99, 1.01, // Spearman should be 1.0 since ranks are preserved
		},
		{
			"with ties",
			[]float64{1, 2, 2, 3},
			[]float64{1, 2, 3, 4},
			0.8, 1.01,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SpearmanCorrelation(tt.x, tt.y)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("SpearmanCorrelation() = %.4f, want [%.2f, %.2f]", got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestRanks(t *testing.T) {
	got := ranks([]float64{30, 10, 20, 10})
	want := []float64{4, 1.5, 3, 1.5} // ties get average rank

	if len(got) != len(want) {
		t.Fatalf("ranks() len = %d, want %d", len(got), len(want))
	}
	for i := range got {
		if math.Abs(got[i]-want[i]) > 0.01 {
			t.Errorf("ranks()[%d] = %.2f, want %.2f", i, got[i], want[i])
		}
	}
}
