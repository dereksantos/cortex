package main

import (
	"testing"

	"github.com/dereksantos/cortex/internal/decide"
)

func TestSamplePoints(t *testing.T) {
	mk := func(n int, final bool) []decide.Point {
		var pts []decide.Point
		for i := 1; i <= n; i++ {
			pts = append(pts, decide.Point{Round: i})
		}
		if final {
			pts = append(pts, decide.Point{Round: n + 1, Final: true})
		}
		return pts
	}
	tests := []struct {
		name      string
		pts       []decide.Point
		k         int
		wantLen   int
		wantLast  int // round of the last mid-turn point kept
		wantFinal bool
	}{
		{name: "under cap keeps all", pts: mk(5, true), k: 12, wantLen: 6, wantLast: 5, wantFinal: true},
		{name: "over cap thins, keeps last", pts: mk(40, true), k: 12, wantLen: 13, wantLast: 40, wantFinal: true},
		{name: "no final", pts: mk(30, false), k: 10, wantLen: 10, wantLast: 30},
		{name: "zero cap keeps all", pts: mk(30, false), k: 0, wantLen: 30, wantLast: 30},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := samplePoints(tt.pts, tt.k)
			if len(got) != tt.wantLen {
				t.Fatalf("len = %d, want %d", len(got), tt.wantLen)
			}
			last := -1
			hasFinal := false
			for _, p := range got {
				if p.Final {
					hasFinal = true
				} else {
					last = p.Round
				}
			}
			if last != tt.wantLast || hasFinal != tt.wantFinal {
				t.Errorf("last mid = %d final = %v, want %d %v", last, hasFinal, tt.wantLast, tt.wantFinal)
			}
		})
	}
}

func TestInstanceOf(t *testing.T) {
	tests := []struct {
		base    string
		id      string
		attempt int
	}{
		{"cortex-django__django-11133", "django__django-11133", 1},
		{"cortex-pytest-dev__pytest-5787.r2", "pytest-dev__pytest-5787", 2},
		{"cortex-sympy__sympy-13852.r10", "sympy__sympy-13852", 10},
	}
	for _, tt := range tests {
		t.Run(tt.base, func(t *testing.T) {
			id, n := instanceOf(tt.base)
			if id != tt.id || n != tt.attempt {
				t.Errorf("instanceOf(%q) = %q, %d; want %q, %d", tt.base, id, n, tt.id, tt.attempt)
			}
		})
	}
}

func TestAUROC(t *testing.T) {
	tests := []struct {
		name             string
		failed, resolved []float64
		want             float64
	}{
		{"perfect", []float64{3, 4}, []float64{1, 2}, 1},
		{"inverted", []float64{1, 2}, []float64{3, 4}, 0},
		{"all ties", []float64{1, 1}, []float64{1, 1}, 0.5},
		{"binary half", []float64{1, 0}, []float64{0, 0}, 0.75},
		{"empty side", nil, []float64{1}, 0.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := auroc(tt.failed, tt.resolved); got != tt.want {
				t.Errorf("auroc = %v, want %v", got, tt.want)
			}
		})
	}
}
