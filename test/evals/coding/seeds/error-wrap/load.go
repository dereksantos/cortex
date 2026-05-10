// Package wrap demonstrates an error-handling-style fix: bare errors
// from os.ReadFile should be wrapped with fmt.Errorf("...: %w", err)
// to preserve the cause chain.
package wrap

import (
	"os"
)

// LoadConfig reads a config file. It currently returns the bare error
// from os.ReadFile, which loses the "where in the program did this
// happen" context.
//
// Fix: wrap the os.ReadFile error with fmt.Errorf using %w.
func LoadConfig(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return data, nil
}
