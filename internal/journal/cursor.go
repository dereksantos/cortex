package journal

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Cursor tracks the last offset successfully consumed by an indexer for one
// writer-class. Stored as plain text at <classDir>/.cursor.
type Cursor struct {
	path string
}

// OpenCursor returns a Cursor handle. The file is not read or created
// until Get or Set is called.
func OpenCursor(classDir string) *Cursor {
	return &Cursor{path: filepath.Join(classDir, ".cursor")}
}

// Get returns the last-indexed offset. Returns 0 if the cursor file does
// not exist (interpreted as "nothing indexed yet").
func (c *Cursor) Get() (Offset, error) {
	data, err := os.ReadFile(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("journal: read cursor %s: %w", c.path, err)
	}
	s := strings.TrimSpace(string(data))
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("journal: parse cursor %s: %w", c.path, err)
	}
	return Offset(n), nil
}

// Set writes the cursor value atomically (write-temp then rename).
func (c *Cursor) Set(off Offset) error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return fmt.Errorf("journal: mkdir cursor parent: %w", err)
	}
	tmp := c.path + ".tmp"
	body := strconv.FormatInt(int64(off), 10) + "\n"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		return fmt.Errorf("journal: write cursor tmp: %w", err)
	}
	if err := os.Rename(tmp, c.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("journal: rename cursor: %w", err)
	}
	return nil
}
