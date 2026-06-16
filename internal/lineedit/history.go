package lineedit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

const defaultMaxHistory = 1000

// History is the recallable list of submitted prompts, persisted as JSONL (one
// JSON-encoded string per line) so multi-line pastes and special characters
// round-trip. Methods are nil-safe so a Terminal with no history still works.
type History struct {
	path  string
	items []string
	max   int
}

// LoadHistory reads the JSONL history at path (missing file is fine), keeping at
// most the newest defaultMaxHistory entries.
func LoadHistory(path string) *History {
	h := &History{path: path, max: defaultMaxHistory}
	f, err := os.Open(path)
	if err != nil {
		return h
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024) // tolerate long pasted lines
	for sc.Scan() {
		var s string
		if json.Unmarshal(sc.Bytes(), &s) == nil && s != "" {
			h.items = append(h.items, s)
		}
	}
	if len(h.items) > h.max {
		h.items = h.items[len(h.items)-h.max:]
	}
	return h
}

// Len reports how many entries are stored.
func (h *History) Len() int {
	if h == nil {
		return 0
	}
	return len(h.items)
}

func (h *History) at(i int) string {
	if h == nil || i < 0 || i >= len(h.items) {
		return ""
	}
	return h.items[i]
}

// Add records a submitted line: skips blanks, drops a consecutive duplicate, and
// appends it to the file.
func (h *History) Add(entry string) {
	if h == nil || strings.TrimSpace(entry) == "" {
		return
	}
	if n := len(h.items); n > 0 && h.items[n-1] == entry {
		return
	}
	h.items = append(h.items, entry)
	if len(h.items) > h.max {
		h.items = h.items[len(h.items)-h.max:]
	}
	h.appendFile(entry)
}

func (h *History) appendFile(entry string) {
	if h.path == "" {
		return
	}
	if dir := filepath.Dir(h.path); dir != "" {
		os.MkdirAll(dir, 0o755)
	}
	f, err := os.OpenFile(h.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	if b, err := json.Marshal(entry); err == nil {
		f.Write(append(b, '\n'))
	}
}

// searchBackward returns the newest entry at an index < before whose text
// contains q (case-insensitive; q=="" matches anything), with its index.
func (h *History) searchBackward(q string, before int) (int, string, bool) {
	if h == nil {
		return -1, "", false
	}
	q = strings.ToLower(q)
	if before > len(h.items) {
		before = len(h.items)
	}
	for i := before - 1; i >= 0; i-- {
		if q == "" || strings.Contains(strings.ToLower(h.items[i]), q) {
			return i, h.items[i], true
		}
	}
	return -1, "", false
}
