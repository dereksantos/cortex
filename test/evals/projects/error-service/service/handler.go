package service

import (
	"os"
)

// ReadConfig reads configuration from a file.
// TODO: Add proper error handling with context.
func ReadConfig(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err // Needs better error wrapping
	}
	return data, nil
}

// ProcessData processes input data.
// TODO: Add proper error handling.
func ProcessData(data []byte) error {
	if len(data) == 0 {
		return nil // Should return an error
	}
	// Processing logic here
	return nil
}
