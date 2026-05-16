//go:build !windows

package eval

import (
	"context"
	"time"
)

// dreamDrainStub holds the inter-attempt idle window so the next
// attempt sees a "rested" agent. In iteration 1 this is literally a
// time.Sleep — the captured failures (coding_capture.go) are already
// indexed as Insights and reachable via cortex_search, so the next
// attempt benefits from learning even without Dream extraction.
//
// Iteration 2 will wire a real *cognition.Dream invocation here so
// the system can extract higher-order patterns ("the model keeps
// botching boundary cells") from raw failure captures. The signature
// is chosen to stay stable across that change: replace the time.Sleep
// body with `(*intcognition.Cortex).MaybeDream(ctx)` while honoring
// the same idle window.
//
// idleSeconds <= 0 is a no-op. Honors ctx cancellation so a SIGINT
// during the wait shuts the eval down promptly.
func dreamDrainStub(ctx context.Context, storeDir string, idleSeconds int) error {
	_ = storeDir
	if idleSeconds <= 0 {
		return nil
	}
	timer := time.NewTimer(time.Duration(idleSeconds) * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
