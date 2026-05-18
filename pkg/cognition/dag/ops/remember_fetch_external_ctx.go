package ops

import (
	"context"
	"time"
)

// contextWithTimeoutImpl is the actual context.WithTimeout call,
// separated so the wrapper can be replaced in tests without
// depending on go-time-mocking gymnastics.
func contextWithTimeoutImpl(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, d)
}
