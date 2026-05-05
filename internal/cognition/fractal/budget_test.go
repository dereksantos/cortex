package fractal

import (
	"math/rand"
	"testing"
)

func TestSourceWeights_AllocateSumsToBudget(t *testing.T) {
	w := NewSourceWeights()
	rng := rand.New(rand.NewSource(7))
	sources := []string{"a", "b", "c", "d"}
	alloc := w.Allocate(20, sources, rng)
	sum := 0
	for _, n := range alloc {
		sum += n
	}
	// With ε-greedy active (10% chance) the sum may be budget+EpsilonBonus.
	// Without ε-greedy it should equal budget.
	if sum != 20 && sum != 20+EpsilonBonus {
		t.Errorf("sum=%d; want %d or %d", sum, 20, 20+EpsilonBonus)
	}
}

func TestSourceWeights_FloorRespected(t *testing.T) {
	w := NewSourceWeights()
	rng := rand.New(rand.NewSource(11))
	sources := []string{"a", "b", "c", "d"}
	alloc := w.Allocate(20, sources, rng)
	for name, n := range alloc {
		if n < 1 {
			t.Errorf("source %s got %d, expected at least 1", name, n)
		}
	}
}

func TestSourceWeights_YieldFavoured(t *testing.T) {
	w := NewSourceWeights()
	// Run many cycles giving "good" much higher yield than "bad".
	for i := 0; i < YieldWindow; i++ {
		w.Update(map[string]CycleStats{
			"good": {Items: 5, Insights: 4},
			"bad":  {Items: 5, Insights: 0},
		})
	}
	rng := rand.New(rand.NewSource(1))
	totalGood := 0
	totalBad := 0
	for i := 0; i < 200; i++ {
		alloc := w.Allocate(20, []string{"good", "bad"}, rng)
		totalGood += alloc["good"]
		totalBad += alloc["bad"]
	}
	if totalGood <= totalBad {
		t.Errorf("good-yield source should win more budget: good=%d bad=%d", totalGood, totalBad)
	}
}

func TestSourceWeights_EpsilonGreedyFires(t *testing.T) {
	w := NewSourceWeights()
	rng := rand.New(rand.NewSource(3))
	bonusCount := 0
	for i := 0; i < 1000; i++ {
		alloc := w.Allocate(4, []string{"a", "b"}, rng)
		sum := alloc["a"] + alloc["b"]
		if sum > 4 {
			bonusCount++
		}
	}
	// Expect ~10% — accept 5%–18%.
	if bonusCount < 50 || bonusCount > 180 {
		t.Errorf("ε-greedy bonus rate out of expected band: %d / 1000", bonusCount)
	}
}

func TestSourceWeights_EmptyInput(t *testing.T) {
	w := NewSourceWeights()
	rng := rand.New(rand.NewSource(0))
	if got := w.Allocate(0, []string{"a"}, rng); len(got) != 0 {
		t.Errorf("budget=0 should yield empty allocation")
	}
	if got := w.Allocate(10, nil, rng); len(got) != 0 {
		t.Errorf("no sources should yield empty allocation")
	}
}
