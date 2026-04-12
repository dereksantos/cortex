package storage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// appendLine marshals record as JSON and appends it as a line to the file.
// Uses O_APPEND for POSIX atomic appends (safe for lines under PIPE_BUF).
func appendLine(f *os.File, record any) error {
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("failed to marshal record: %w", err)
	}
	data = append(data, '\n')
	_, err = f.Write(data)
	if err != nil {
		return fmt.Errorf("failed to append line: %w", err)
	}
	return nil
}

// readLines reads all JSON lines from a file and unmarshals each into T.
// Skips lines that fail to unmarshal (graceful crash recovery).
func readLines[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to open %s: %w", path, err)
	}
	defer f.Close()

	var results []T
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var item T
		if err := json.Unmarshal(line, &item); err != nil {
			continue // skip corrupted lines
		}
		results = append(results, item)
	}

	if err := scanner.Err(); err != nil {
		return results, fmt.Errorf("scanner error reading %s: %w", path, err)
	}

	return results, nil
}

// atomicRewrite writes records to a temp file then renames over the original.
// Used for compaction — removes deleted/superseded entries.
func atomicRewrite[T any](path string, records []T) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".compact-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	writer := bufio.NewWriter(tmp)
	for _, rec := range records {
		data, err := json.Marshal(rec)
		if err != nil {
			tmp.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("failed to marshal record: %w", err)
		}
		data = append(data, '\n')
		if _, err := writer.Write(data); err != nil {
			tmp.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("failed to write record: %w", err)
		}
	}

	if err := writer.Flush(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("failed to flush: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("failed to sync: %w", err)
	}
	tmp.Close()

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to rename: %w", err)
	}

	return nil
}

// openAppend opens a file for appending, creating it if it doesn't exist.
func openAppend(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
}
