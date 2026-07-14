package secret

import "sync"

// Store reads and writes secrets in a system secret store (the macOS
// Keychain on darwin; unsupported elsewhere for now — see
// keychain_other.go). It is the interface production code depends on
// so tests can substitute a Fake instead: no test in this repo may
// invoke the real `security` binary (enforced by
// TestNoRealSecurityBinaryInvokedByTests in meta_test.go) — unlocking
// the keychain would prompt on an operator's machine and always fail
// in CI.
type Store interface {
	// Get returns the secret value stored under service, or
	// ErrNotFound if no entry exists.
	Get(service string) (string, error)
	// Set stores value under service/account, creating the entry if
	// absent or overwriting it if present.
	Set(service, account, value string) error
}

// Fake is an in-memory Store for tests. GetErr/SetErr, when non-nil,
// are returned unconditionally by the corresponding method (set them
// to exercise error paths).
type Fake struct {
	mu   sync.Mutex
	data map[string]string // service -> value

	GetErr error
	SetErr error
}

// NewFake returns an empty Fake ready to use.
func NewFake() *Fake {
	return &Fake{data: make(map[string]string)}
}

// Get implements Store.
func (f *Fake) Get(service string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.GetErr != nil {
		return "", f.GetErr
	}
	v, ok := f.data[service]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

// Set implements Store.
func (f *Fake) Set(service, account, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.SetErr != nil {
		return f.SetErr
	}
	if f.data == nil {
		f.data = make(map[string]string)
	}
	f.data[service] = value
	return nil
}
