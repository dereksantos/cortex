// retry_test.go — issue #132's classifier retry policy: classifyWithRetry
// re-sends a bounded number of times ONLY on transient transport errors
// (net.Error, context alive), never on a cancelled ctx or a non-transport
// error, and hands back the LAST error when the attempts run out. The
// attempt function is injected directly, so no provider is needed.
package shellrisk

import (
	"context"
	"errors"
	"net"
	"testing"
)

// transientErr is the recorded failure shape: a dropped connection off a
// small local backend, wrapped the way a real transport stack wraps it.
func transientErr() error {
	return &net.OpError{Op: "read", Err: errors.New("connection reset")}
}

func TestClassifyWithRetry(t *testing.T) {
	// Skip the backoff sleeps; the policy under test is the attempt counting
	// and the error selection, not the delay curve.
	old := classifyRetryBaseDelay
	classifyRetryBaseDelay = 0
	t.Cleanup(func() { classifyRetryBaseDelay = old })

	ctx := context.Background()
	t.Run("transient net.Error retried, second attempt succeeds", func(t *testing.T) {
		attempts := 0
		raw, err := classifyWithRetry(ctx, func() (string, error) {
			attempts++
			if attempts == 1 {
				return "", transientErr()
			}
			return `{"risk":"safe","reason":"ok"}`, nil
		})
		if err != nil {
			t.Fatalf("err = %v, want nil (recovered on retry)", err)
		}
		if raw == "" {
			t.Error("raw = empty, want the recovered response")
		}
		if attempts != 2 {
			t.Errorf("attempts = %d, want 2 (one transient failure + one success)", attempts)
		}
	})

	t.Run("non-transport error is not retried", func(t *testing.T) {
		attempts := 0
		_, err := classifyWithRetry(ctx, func() (string, error) {
			attempts++
			return "", errors.New("HTTP 400 bad request")
		})
		if err == nil {
			t.Fatal("want the error surfaced")
		}
		if attempts != 1 {
			t.Errorf("attempts = %d, want 1 (a 4xx must not be retried)", attempts)
		}
	})

	t.Run("cancelled context is not retried", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		attempts := 0
		_, err := classifyWithRetry(cctx, func() (string, error) {
			attempts++
			return "", transientErr()
		})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled (a cancelled turn must not retry)", err)
		}
		if attempts != 1 {
			t.Errorf("attempts = %d, want 1 (no retry on a cancelled context)", attempts)
		}
	})

	t.Run("exhausted retries return the last error", func(t *testing.T) {
		attempts := 0
		_, err := classifyWithRetry(ctx, func() (string, error) {
			attempts++
			return "", transientErr()
		})
		if err == nil {
			t.Fatal("want the last attempt's error (fail closed)")
		}
		var ne net.Error
		if !errors.As(err, &ne) {
			t.Errorf("err = %v, want the transient net.Error from the last attempt", err)
		}
		// One initial send + classifyRetryAttempts retries.
		if want := 1 + classifyRetryAttempts; attempts != want {
			t.Errorf("attempts = %d, want %d (1 initial + %d retries)", attempts, want, classifyRetryAttempts)
		}
	})

	t.Run("success on first attempt sends nothing more", func(t *testing.T) {
		attempts := 0
		_, err := classifyWithRetry(ctx, func() (string, error) {
			attempts++
			return `{"risk":"safe","reason":"ok"}`, nil
		})
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if attempts != 1 {
			t.Errorf("attempts = %d, want 1", attempts)
		}
	})
}

// TestIsTransientClassifierError pins the transient predicate itself:
// net.Error directly, through a wrapper chain, a cancelled context, and a
// non-transport error.
func TestIsTransientClassifierError(t *testing.T) {
	ctx := context.Background()
	cctx, cancel := context.WithCancel(ctx)
	cancel()

	cases := []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{"nil error", ctx, nil, false},
		{"net.Error directly", ctx, transientErr(), true},
		{"net.Error wrapped", ctx, errors.Join(errors.New("upstream"), transientErr()), true},
		{"non-transport error", ctx, errors.New("boom"), false},
		{"net.Error but ctx cancelled", cctx, transientErr(), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isTransientClassifierError(c.ctx, c.err); got != c.want {
				t.Errorf("isTransientClassifierError = %v, want %v", got, c.want)
			}
		})
	}
}
