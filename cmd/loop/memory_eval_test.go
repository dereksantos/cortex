package main

import (
	"regexp"
	"testing"
	"time"

	"github.com/dereksantos/cortex/pkg/cognition"
)

// freshnessSignal matches any relative-age / staleness cue a reader could use to
// tell a stale memory from a current one.
var freshnessSignal = regexp.MustCompile(`(?i)\b(ago|just now|today|yesterday|stale|fresh|distilled through|newer|older|\d+\s*(d|h|m|s|day|days|hour|hours|min|week|weeks|month|months))\b`)

// TestRetrievalConveysFreshness is the motivating eval for temporal grounding in
// memory. The harness must let the model tell a stale fact from a current one:
// when two memories about the same subject are retrieved — an old "936 docs"
// (captured a month ago) and a fresh "64 docs" (captured now) — the rendered
// context must carry enough recency information that the stale one is
// distinguishable.
//
// It FAILS today by design: formatRetrieved renders only "- [category] content",
// dropping every timestamp, so the two facts are indistinguishable and the model
// has no basis to discount the stale one (the "still 936 docs?" failure). It will
// pass once retrieval injects per-item relative age (and a distill watermark).
func TestRetrievalConveysFreshness(t *testing.T) {
	now := time.Now()
	stale := cognition.Result{
		Category:  "event",
		Content:   "the project has 936 documentation files",
		Timestamp: now.Add(-30 * 24 * time.Hour), // a month ago — no longer true
	}
	fresh := cognition.Result{
		Category:  "event",
		Content:   "the project has 64 documentation files",
		Timestamp: now, // current
	}

	out := formatRetrievedAt([]cognition.Result{stale, fresh}, now)

	if !freshnessSignal.MatchString(out) {
		t.Fatalf("FAILURE MODE: retrieved memory carries no freshness grounding — a "+
			"stale fact (30d old) and a current one render identically, so the model "+
			"cannot tell which to trust. Rendered context:\n%s", out)
	}

	// The stale month-old item must read as older than the just-now one (not
	// merely "some timestamp is present").
	staleOnly := formatRetrievedAt([]cognition.Result{stale}, now)
	freshOnly := formatRetrievedAt([]cognition.Result{fresh}, now)
	if staleOnly == freshOnly {
		t.Errorf("stale and fresh items render identically (%q) — recency is not distinguished", staleOnly)
	}
}

// TestRetrievalFreshnessIsFast enforces the "stay fast" constraint: freshness
// grounding is mechanical, so formatting a full result set must stay far under a
// millisecond — it can never become a reason to keep agents off the hot path.
func TestRetrievalFreshnessIsFast(t *testing.T) {
	now := time.Now()
	results := make([]cognition.Result, 50)
	for i := range results {
		results[i] = cognition.Result{
			Category:  "event",
			Content:   "some retrieved memory line with a bit of content to format",
			Timestamp: now.Add(-time.Duration(i) * time.Hour),
		}
	}
	start := time.Now()
	for i := 0; i < 200; i++ {
		_ = formatRetrievedAt(results, now)
	}
	per := time.Since(start) / 200
	if per > time.Millisecond {
		t.Errorf("formatRetrieved took %v for 50 hits; must stay sub-ms (mechanical, no model)", per)
	}
}
