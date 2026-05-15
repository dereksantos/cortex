//go:build !darwin

package secret

// LookupOpenRouterKey is a non-darwin stub that always reports the
// keychain backend is unavailable. Callers fall back to the env-var
// path; the Linux/Windows secret-store equivalents are out of scope
// for iteration 1.
func LookupOpenRouterKey() (string, error) {
	return "", errKeychainUnsupported
}
