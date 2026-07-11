//go:build !darwin

package secret

import "errors"

// errKeychainUnsupported is the sentinel returned by the non-darwin
// stub. Declared here (not in keychain.go) so it isn't a dead symbol
// on darwin builds — linters flag cross-build-tag unused vars.
var errKeychainUnsupported = errors.New("keychain not supported on this platform")

// LookupOpenRouterKey is a non-darwin stub that always reports the
// keychain backend is unavailable. Callers fall back to the env-var
// path; the Linux/Windows secret-store equivalents are out of scope
// for iteration 1.
func LookupOpenRouterKey() (string, error) {
	return "", errKeychainUnsupported
}

// unsupportedStore implements Store as an always-error stub on
// platforms without a wired secret-store backend.
type unsupportedStore struct{}

// NewStore returns the non-darwin Store stub.
func NewStore() Store { return unsupportedStore{} }

// Get implements Store.
func (unsupportedStore) Get(service string) (string, error) {
	return "", errKeychainUnsupported
}

// Set implements Store.
func (unsupportedStore) Set(service, account, value string) error {
	return errKeychainUnsupported
}
