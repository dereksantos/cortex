package measure

import "testing"

func TestScoreForInjection(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		wantWorth  float64 // minimum expected worth
		worthBelow float64 // maximum expected worth
	}{
		{
			"specific project decision",
			"Use JWT with RS256 signing for auth in pkg/auth/handler.go. Tokens stored in httpOnly cookies with 1h expiry.",
			0.3, 1.0,
		},
		{
			"generic advice",
			"Follow best practices for error handling. Write tests and use clean code.",
			0.0, 0.15,
		},
		{
			"concrete constraint",
			"Never use `localStorage` for tokens. Must use httpOnly cookies. See auth.go for implementation.",
			0.3, 1.0,
		},
		{
			"vague insight",
			"The code could be improved in various ways.",
			0.0, 0.2,
		},
		{
			"empty",
			"",
			0.0, 0.01,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := ScoreForInjection(tt.content)
			if score.Worth < tt.wantWorth || score.Worth > tt.worthBelow {
				t.Errorf("Worth = %.2f, want [%.2f, %.2f]\n  Clarity=%.2f Redundancy=%.2f TokenCost=%d",
					score.Worth, tt.wantWorth, tt.worthBelow,
					score.Clarity, score.Redundancy, score.TokenCost)
			}
		})
	}
}

func TestScoreRedundancy(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantMin float64
		wantMax float64
	}{
		{"generic", "Follow best practices and handle errors properly", 0.5, 1.0},
		{"project specific", "Use pgx not database/sql in pkg/storage/db.go", 0.0, 0.2},
		{"mixed", "Use best practices when implementing handleAuth() in auth.go", 0.0, 0.4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scoreRedundancy(tt.content)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("scoreRedundancy() = %.2f, want [%.2f, %.2f]", got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestScoreContextClarity(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantMin float64
		wantMax float64
	}{
		{"clear with paths and values", "Set timeout to 30s in pkg/config/defaults.go. Use `context.WithTimeout`.", 0.5, 1.0},
		{"vague", "Make the thing work better somehow", 0.0, 0.3},
		{"has numbers and identifiers", "Rate limit: 100 req/s per user, use `TokenBucket` struct", 0.3, 1.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scoreContextClarity(tt.content)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("scoreContextClarity() = %.2f, want [%.2f, %.2f]", got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestCalculateWorth(t *testing.T) {
	tests := []struct {
		name       string
		clarity    float64
		redundancy float64
		tokenCost  int
		wantMin    float64
		wantMax    float64
	}{
		{"high value", 0.8, 0.0, 50, 0.7, 1.0},
		{"generic", 0.5, 0.8, 50, 0.0, 0.15},
		{"expensive", 0.8, 0.0, 500, 0.3, 0.6},
		{"zero clarity", 0.0, 0.0, 10, 0.0, 0.01},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateWorth(tt.clarity, tt.redundancy, tt.tokenCost)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("calculateWorth(%.1f, %.1f, %d) = %.2f, want [%.2f, %.2f]",
					tt.clarity, tt.redundancy, tt.tokenCost, got, tt.wantMin, tt.wantMax)
			}
		})
	}
}
