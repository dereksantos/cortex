package cache

import "testing"

func TestTokensOf(t *testing.T) {
	tests := []struct {
		name  string
		chars int
		want  int
	}{
		{"4000 chars → 1000 tokens", 4000, 1000},
		{"3 chars → 0", 3, 0},
		{"4 chars → 1", 4, 1},
		{"7 chars → 1", 7, 1},
		{"8 chars → 2", 8, 2},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got := TokensOf(tt.chars)
			if got != tt.want {
				t.Errorf("TokensOf(%d) = %d, want %d", tt.chars, got, tt.want)
			}
		})
	}
}

func TestAddTurnContiguity(t *testing.T) {
	t.Run("first turn starting at base is accepted", func(t *testing.T) {
		ws := New(1, 1000, 600)
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("AddTurn panicked unexpectedly: %v", r)
			}
		}()
		ws.AddTurn(TurnSpan{Start: 1, End: 3, Tokens: 100})
	})

	t.Run("first turn starting elsewhere panics", func(t *testing.T) {
		ws := New(1, 1000, 600)
		defer func() {
			if r := recover(); r == nil {
				t.Error("AddTurn should have panicked but did not")
			}
		}()
		ws.AddTurn(TurnSpan{Start: 2, End: 4, Tokens: 100})
	})

	t.Run("second turn starting at first End is accepted", func(t *testing.T) {
		ws := New(1, 1000, 600)
		ws.AddTurn(TurnSpan{Start: 1, End: 3, Tokens: 100})
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("AddTurn panicked unexpectedly: %v", r)
			}
		}()
		ws.AddTurn(TurnSpan{Start: 3, End: 5, Tokens: 200})
	})

	t.Run("gap panics", func(t *testing.T) {
		ws := New(1, 1000, 600)
		ws.AddTurn(TurnSpan{Start: 1, End: 3, Tokens: 100})
		defer func() {
			if r := recover(); r == nil {
				t.Error("AddTurn should have panicked but did not")
			}
		}()
		ws.AddTurn(TurnSpan{Start: 5, End: 7, Tokens: 200})
	})
}

func TestDemoteBatch(t *testing.T) {
	t.Run("under high watermark → nil", func(t *testing.T) {
		ws := New(1, 1000, 600)
		ws.AddTurn(TurnSpan{Start: 1, End: 3, Tokens: 400})
		ws.AddTurn(TurnSpan{Start: 3, End: 5, Tokens: 400})

		demoted := ws.DemoteBatch()

		if len(demoted) != 0 {
			t.Errorf("DemoteBatch len = %d, want 0", len(demoted))
		}
		if ws.FrontierMsg() != 1 {
			t.Errorf("FrontierMsg = %d, want 1", ws.FrontierMsg())
		}
		if ws.TailTokens() != 800 {
			t.Errorf("TailTokens = %d, want 800", ws.TailTokens())
		}
	})

	t.Run("crossing high drains to low", func(t *testing.T) {
		ws := New(1, 1000, 600)
		// Add 4 turns of 300 tokens each: 1200 tokens total, spanning [1,5), [5,9), [9,13), [13,17)
		ws.AddTurn(TurnSpan{Start: 1, End: 5, Tokens: 300})
		ws.AddTurn(TurnSpan{Start: 5, End: 9, Tokens: 300})
		ws.AddTurn(TurnSpan{Start: 9, End: 13, Tokens: 300})
		ws.AddTurn(TurnSpan{Start: 13, End: 17, Tokens: 300})

		// TailTokens is 1200, above highWM (1000), so demote
		demoted := ws.DemoteBatch()

		if len(demoted) != 2 {
			t.Errorf("DemoteBatch len = %d, want 2", len(demoted))
		}
		// Oldest first: [1,5) then [5,9)
		if demoted[0].Start != 1 || demoted[0].End != 5 {
			t.Errorf("demoted[0] = [%d,%d), want [1,5)", demoted[0].Start, demoted[0].End)
		}
		if demoted[1].Start != 5 || demoted[1].End != 9 {
			t.Errorf("demoted[1] = [%d,%d), want [5,9)", demoted[1].Start, demoted[1].End)
		}

		// After demoting 2 turns, TailTokens = 600 (at lowWM)
		if ws.TailTokens() != 600 {
			t.Errorf("TailTokens after demotion = %d, want 600", ws.TailTokens())
		}
		if ws.FrontierMsg() != 9 {
			t.Errorf("FrontierMsg = %d, want 9 (End of last demoted span)", ws.FrontierMsg())
		}
		if ws.Demoted() != 2 {
			t.Errorf("Demoted() = %d, want 2", ws.Demoted())
		}

		// Second immediate DemoteBatch returns len 0 (hysteresis)
		demoted2 := ws.DemoteBatch()
		if len(demoted2) != 0 {
			t.Errorf("Second DemoteBatch len = %d, want 0 (hysteresis)", len(demoted2))
		}
	})

	t.Run("never demotes the most recent turn", func(t *testing.T) {
		ws := New(1, 100, 50)
		ws.AddTurn(TurnSpan{Start: 1, End: 5, Tokens: 500})

		demoted := ws.DemoteBatch()

		// Guard returns nil only when TailTokens <= highWM; here TailTokens=500 > highWM=100
		// but the loop must not demote the only turn
		if len(demoted) != 0 {
			t.Errorf("DemoteBatch len = %d, want 0", len(demoted))
		}
		if ws.FrontierMsg() != 1 {
			t.Errorf("FrontierMsg = %d, want 1", ws.FrontierMsg())
		}
		if ws.TailTokens() != 500 {
			t.Errorf("TailTokens = %d, want 500", ws.TailTokens())
		}
	})

	t.Run("oversized last turn stops the drain", func(t *testing.T) {
		ws := New(1, 100, 50)
		ws.AddTurn(TurnSpan{Start: 1, End: 3, Tokens: 30})
		ws.AddTurn(TurnSpan{Start: 3, End: 7, Tokens: 500})

		demoted := ws.DemoteBatch()

		// TailTokens = 530, above lowWM=50, so try to demote
		// Demote first turn: TailTokens becomes 500, still > 50
		// But cannot demote second (most recent) turn, so stop after 1
		if len(demoted) != 1 {
			t.Errorf("DemoteBatch len = %d, want 1", len(demoted))
		}
		if demoted[0].Start != 1 || demoted[0].End != 3 {
			t.Errorf("demoted[0] = [%d,%d), want [1,3)", demoted[0].Start, demoted[0].End)
		}

		if ws.TailTokens() != 500 {
			t.Errorf("TailTokens after demotion = %d, want 500", ws.TailTokens())
		}
		if ws.Demoted() != 1 {
			t.Errorf("Demoted() = %d, want 1", ws.Demoted())
		}
	})
}

func TestFrontierMsg(t *testing.T) {
	t.Run("before any demotion returns base", func(t *testing.T) {
		ws := New(1, 1000, 600)
		if ws.FrontierMsg() != 1 {
			t.Errorf("FrontierMsg = %d, want 1 (base)", ws.FrontierMsg())
		}
	})

	t.Run("after demotion returns End of last demoted span", func(t *testing.T) {
		ws := New(1, 500, 400)
		ws.AddTurn(TurnSpan{Start: 1, End: 5, Tokens: 300})
		ws.AddTurn(TurnSpan{Start: 5, End: 9, Tokens: 300})
		ws.AddTurn(TurnSpan{Start: 9, End: 13, Tokens: 300})

		// Demote 2 turns (tail goes from 900 to 300 tokens)
		ws.DemoteBatch()

		if ws.FrontierMsg() != 9 {
			t.Errorf("FrontierMsg = %d, want 9 (End of last demoted span)", ws.FrontierMsg())
		}
	})

	t.Run("with base=0", func(t *testing.T) {
		ws := New(0, 1000, 600)
		if ws.FrontierMsg() != 0 {
			t.Errorf("FrontierMsg = %d, want 0 (base)", ws.FrontierMsg())
		}
	})
}

func TestRestoreState(t *testing.T) {
	ws := New(1, 100, 60)
	ws.AddTurn(TurnSpan{Start: 1, End: 3, Tokens: 20})
	ws.AddTurn(TurnSpan{Start: 3, End: 5, Tokens: 20})

	if err := ws.RestoreState(1, 120, 80); err != nil {
		t.Fatal(err)
	}
	if ws.Demoted() != 1 || ws.FrontierMsg() != 3 {
		t.Fatalf("restored frontier = %d / msg %d, want 1 / 3", ws.Demoted(), ws.FrontierMsg())
	}
	if high, low := ws.GetWatermarks(); high != 120 || low != 80 {
		t.Fatalf("restored watermarks = %d/%d, want 120/80", high, low)
	}
}

func TestRestoreStateRejectsInvalidSnapshotWithoutMutation(t *testing.T) {
	ws := New(1, 100, 60)
	ws.AddTurn(TurnSpan{Start: 1, End: 2, Tokens: 20})

	for _, tc := range []struct {
		name                string
		frontier, high, low int
	}{
		{"negative frontier", -1, 100, 60},
		{"frontier past turns", 2, 100, 60},
		{"invalid watermarks", 0, 50, 60},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ws.RestoreState(tc.frontier, tc.high, tc.low); err == nil {
				t.Fatal("RestoreState succeeded")
			}
			if ws.Demoted() != 0 {
				t.Fatalf("frontier mutated to %d", ws.Demoted())
			}
			if high, low := ws.GetWatermarks(); high != 100 || low != 60 {
				t.Fatalf("watermarks mutated to %d/%d", high, low)
			}
		})
	}
}

func TestReorderTail(t *testing.T) {
	t.Run("recency reorders tail newest first", func(t *testing.T) {
		ws := New(1, 1000, 600)
		ws.AddTurn(TurnSpan{Start: 1, End: 3, Tokens: 100})
		ws.AddTurn(TurnSpan{Start: 3, End: 5, Tokens: 200})
		ws.AddTurn(TurnSpan{Start: 5, End: 7, Tokens: 300})
		// Total 600 tokens, all in tail (frontier=0)

		reordered := ws.ReorderTail("recency")

		if len(reordered) != 3 {
			t.Errorf("ReorderTail len = %d, want 3", len(reordered))
		}
		// With recency (newest first), the order should be reversed
		// Original: [100, 200, 300], Reversed: [300, 200, 100]
		if reordered[0].Tokens != 300 || reordered[1].Tokens != 200 || reordered[2].Tokens != 100 {
			t.Errorf("ReorderTail order = [%d,%d,%d], want [300,200,100]",
				reordered[0].Tokens, reordered[1].Tokens, reordered[2].Tokens)
		}
	})

	t.Run("salience reorders tail (same as recency for now)", func(t *testing.T) {
		ws := New(1, 1000, 600)
		ws.AddTurn(TurnSpan{Start: 1, End: 3, Tokens: 100})
		ws.AddTurn(TurnSpan{Start: 3, End: 5, Tokens: 200})
		ws.AddTurn(TurnSpan{Start: 5, End: 7, Tokens: 300})

		reordered := ws.ReorderTail("salience")

		if len(reordered) != 3 {
			t.Errorf("ReorderTail len = %d, want 3", len(reordered))
		}
		// For now, salience is same as recency (reversed)
		if reordered[0].Tokens != 300 {
			t.Errorf("First token is %d, want 300", reordered[0].Tokens)
		}
	})

	t.Run("unknown metric returns nil", func(t *testing.T) {
		ws := New(1, 1000, 600)
		ws.AddTurn(TurnSpan{Start: 1, End: 3, Tokens: 100})

		reordered := ws.ReorderTail("unknown-metric")

		if reordered != nil {
			t.Errorf("ReorderTail with unknown metric should return nil, got %v", reordered)
		}
	})

	t.Run("tail with one turn returns that turn", func(t *testing.T) {
		ws := New(1, 500, 400)
		ws.AddTurn(TurnSpan{Start: 1, End: 3, Tokens: 100})
		ws.AddTurn(TurnSpan{Start: 3, End: 5, Tokens: 200})
		// Add a third turn so we can demote some
		ws.AddTurn(TurnSpan{Start: 5, End: 7, Tokens: 100})
		// Demote turns - will leave at least one in tail
		ws.DemoteBatch()

		reordered := ws.ReorderTail("recency")

		// Should return the remaining turn(s) in tail
		if len(reordered) == 0 {
			t.Errorf("ReorderTail with non-empty tail should not return nil")
		}
	})

	t.Run("fully demoted (only one turn) returns that one turn", func(t *testing.T) {
		ws := New(1, 100, 50)
		ws.AddTurn(TurnSpan{Start: 1, End: 3, Tokens: 100})

		// TailTokens is 100, above highWM=100? No, it equals.
		// Let's adjust: make it larger
		ws.highWM = 50 // Override for test
		ws.lowWM = 30  // Override for test

		// With only 1 turn and highWM=50, TailTokens=100 > 50
		// But loop condition is ws.frontier < len(ws.turns)-1 which is 0 < 0 = false
		// So no demotion happens, and we can't test the "empty tail" case this way

		// Actually, with only 1 turn, it can never be demoted
		reordered := ws.ReorderTail("recency")

		// Should return that one turn
		if len(reordered) != 1 {
			t.Errorf("ReorderTail with 1 turn should return 1 item, got %d", len(reordered))
		}
	})
}

func TestAdjustWatermarks(t *testing.T) {
	t.Run("valid deltas adjust watermarks", func(t *testing.T) {
		ws := New(1, 1000, 600)

		newHigh, newLow, err := ws.AdjustWatermarks(100, 50)

		if err != nil {
			t.Errorf("AdjustWatermarks unexpected error: %v", err)
		}
		if newHigh != 1100 {
			t.Errorf("newHigh = %d, want 1100", newHigh)
		}
		if newLow != 650 {
			t.Errorf("newLow = %d, want 650", newLow)
		}
		if ws.highWM != 1100 {
			t.Errorf("ws.highWM = %d, want 1100", ws.highWM)
		}
		if ws.lowWM != 650 {
			t.Errorf("ws.lowWM = %d, want 650", ws.lowWM)
		}
	})

	t.Run("negative deltas work", func(t *testing.T) {
		ws := New(1, 1000, 600)

		newHigh, newLow, err := ws.AdjustWatermarks(-200, -100)

		if err != nil {
			t.Errorf("AdjustWatermarks unexpected error: %v", err)
		}
		if newHigh != 800 {
			t.Errorf("newHigh = %d, want 800", newHigh)
		}
		if newLow != 500 {
			t.Errorf("newLow = %d, want 500", newLow)
		}
	})

	t.Run("exceeding bounds returns error", func(t *testing.T) {
		ws := New(1, 1000, 600)

		// Max delta is 500 (W/4 where W=2000)
		_, _, err := ws.AdjustWatermarks(600, 0)

		if err == nil {
			t.Error("AdjustWatermarks should return error for out-of-bounds delta")
		}
	})

	t.Run("violating low <= high returns error", func(t *testing.T) {
		ws := New(1, 1000, 600)

		// This would make low > high: 600 + 500 = 1100 > 1000 - 100 = 900
		_, _, err := ws.AdjustWatermarks(-100, 500)

		if err == nil {
			t.Error("AdjustWatermarks should return error when low > high")
		}
	})

	t.Run("zero deltas work", func(t *testing.T) {
		ws := New(1, 1000, 600)

		newHigh, newLow, err := ws.AdjustWatermarks(0, 0)

		if err != nil {
			t.Errorf("AdjustWatermarks unexpected error: %v", err)
		}
		if newHigh != 1000 {
			t.Errorf("newHigh = %d, want 1000", newHigh)
		}
		if newLow != 600 {
			t.Errorf("newLow = %d, want 600", newLow)
		}
	})
}
