package secret

import (
	"errors"
	"testing"
)

var errTestSentinel = errors.New("test sentinel error")

// TestFakeStoreRoundTrip exercises the Store interface entirely through
// the in-memory Fake — no test in this repo may invoke the real
// `security` binary (see meta_test.go's TestNoRealSecurityBinaryInvokedByTests).
func TestFakeStoreRoundTrip(t *testing.T) {
	f := NewFake()

	if _, err := f.Get("cortex-openrouter"); err != ErrNotFound {
		t.Fatalf("Get on empty fake: err=%v, want ErrNotFound", err)
	}

	if err := f.Set("cortex-openrouter", "openrouter", "sk-or-v1-abc"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := f.Get("cortex-openrouter")
	if err != nil {
		t.Fatalf("Get after Set: %v", err)
	}
	if got != "sk-or-v1-abc" {
		t.Errorf("Get after Set = %q, want %q", got, "sk-or-v1-abc")
	}
}

func TestFakeStoreSetOverwrites(t *testing.T) {
	f := NewFake()

	if err := f.Set("svc", "acct", "first"); err != nil {
		t.Fatalf("first Set: %v", err)
	}
	if err := f.Set("svc", "acct", "second"); err != nil {
		t.Fatalf("second Set: %v", err)
	}

	got, err := f.Get("svc")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "second" {
		t.Errorf("Get after overwrite = %q, want %q", got, "second")
	}
}

func TestFakeStoreInjectedErrors(t *testing.T) {
	f := NewFake()
	f.GetErr = errTestSentinel
	f.SetErr = errTestSentinel

	if _, err := f.Get("svc"); err != errTestSentinel {
		t.Errorf("Get with GetErr set: err=%v, want %v", err, errTestSentinel)
	}
	if err := f.Set("svc", "acct", "val"); err != errTestSentinel {
		t.Errorf("Set with SetErr set: err=%v, want %v", err, errTestSentinel)
	}
}

// compile-time assertion that Fake satisfies Store.
var _ Store = (*Fake)(nil)
