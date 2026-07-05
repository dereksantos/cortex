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
