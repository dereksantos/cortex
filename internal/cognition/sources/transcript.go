// Package sources provides DreamSource implementations for Dream mode exploration.
package sources

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/dereksantos/cortex/pkg/cognition"
)

// TranscriptQueueSource samples from transcripts queued by Stop hooks.
// Unlike ClaudeHistorySource which samples from all history,
// this specifically processes transcripts that were queued for analysis.
type TranscriptQueueSource struct {
	queueDir string // Path to transcript queue directory
}

// NewTranscriptQueueSource creates a new TranscriptQueueSource.
// queueDir is typically <project>/.context/transcript_queue/
func NewTranscriptQueueSource(queueDir string) *TranscriptQueueSource {
	return &TranscriptQueueSource{
		queueDir: queueDir,
	}
}

// Name returns the source identifier.
func (s *TranscriptQueueSource) Name() string {
	return "transcript-queue"
}

// QueuedTranscript represents a transcript queued for analysis.
type queuedTranscriptEntry struct {
	TranscriptPath string `json:"transcript_path"`
	SessionID      string `json:"session_id"`
}

// Sample returns items from queued transcripts.
func (s *TranscriptQueueSource) Sample(ctx context.Context, n int) ([]cognition.DreamItem, error) {
	// Find queued transcript files
	entries, err := s.readQueue()
	if err != nil || len(entries) == 0 {
		return nil, err
	}

	var items []cognition.DreamItem

	for _, entry := range entries {
		if ctx.Err() != nil {
			break
		}

		// Parse the transcript file
		extracted, err := s.parseTranscript(entry.TranscriptPath, entry.SessionID)
		if err != nil {
			continue
		}

		items = append(items, extracted...)
		if len(items) >= n {
			break
		}
	}

	// Truncate to requested count
	if len(items) > n {
		items = items[:n]
	}

	return items, nil
}

// readQueue reads queued transcript entries from the queue directory.
func (s *TranscriptQueueSource) readQueue() ([]queuedTranscriptEntry, error) {
	files, err := os.ReadDir(s.queueDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var entries []queuedTranscriptEntry

	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}

		path := filepath.Join(s.queueDir, file.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var entry queuedTranscriptEntry
		if err := json.Unmarshal(data, &entry); err != nil {
			continue
		}

		if entry.TranscriptPath != "" {
			entries = append(entries, entry)
		}

		// Remove processed entry
		os.Remove(path)
	}

	return entries, nil
}

// parseTranscript parses a Claude transcript file and extracts key insights.
func (s *TranscriptQueueSource) parseTranscript(path string, sessionID string) ([]cognition.DreamItem, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var items []cognition.DreamItem
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 1MB max line

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Bytes()

		var entry transcriptLine
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}

		// Extract interesting content
		extracted := s.extractItem(&entry, path, sessionID, lineNum)
		if extracted != nil {
			items = append(items, *extracted)
		}

		// Limit items per transcript
		if len(items) > 30 {
			break
		}
	}

	return items, scanner.Err()
}

// transcriptLine represents a line in a Claude transcript.
type transcriptLine struct {
	Type      string          `json:"type"`
	Content   string          `json:"content"`
	ToolName  string          `json:"tool_name,omitempty"`
	ToolInput json.RawMessage `json:"tool_input,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
}

// extractItem extracts a DreamItem from a transcript line.
func (s *TranscriptQueueSource) extractItem(entry *transcriptLine, path string, sessionID string, lineNum int) *cognition.DreamItem {
	content := strings.ToLower(entry.Content)

	switch entry.Type {
	case "user":
		// Look for high-value user messages
		if s.isHighValue(content) {
			return &cognition.DreamItem{
				ID:      makeTranscriptItemID(path, lineNum),
				Source:  "transcript-queue",
				Content: entry.Content,
				Path:    path,
				Metadata: map[string]any{
					"type":       s.classifyContent(content),
					"session_id": sessionID,
					"line_num":   lineNum,
				},
			}
		}

	case "assistant":
		// Look for key decisions or explanations
		if s.isKeyExplanation(content) {
			return &cognition.DreamItem{
				ID:      makeTranscriptItemID(path, lineNum),
				Source:  "transcript-queue",
				Content: entry.Content,
				Path:    path,
				Metadata: map[string]any{
					"type":       "explanation",
					"session_id": sessionID,
					"line_num":   lineNum,
				},
			}
		}

	case "tool_result":
		// Look for errors or important results
		if len(entry.Result) > 0 {
			result := string(entry.Result)
			if s.isImportantResult(result) {
				return &cognition.DreamItem{
					ID:      makeTranscriptItemID(path, lineNum),
					Source:  "transcript-queue",
					Content: result,
					Path:    path,
					Metadata: map[string]any{
						"type":       "tool_result",
						"tool_name":  entry.ToolName,
						"session_id": sessionID,
						"line_num":   lineNum,
					},
				}
			}
		}
	}

	return nil
}

// isHighValue checks if a user message is high-value for extraction.
func (s *TranscriptQueueSource) isHighValue(content string) bool {
	patterns := []string{
		"we use", "we don't use", "we always", "we never",
		"the pattern", "the convention", "our standard",
		"decided to", "we chose", "we're using",
		"don't do", "avoid", "make sure to",
		"remember", "important", "critical",
		"actually", "instead", "no,", "correct way",
	}

	for _, p := range patterns {
		if strings.Contains(content, p) {
			return true
		}
	}
	return false
}

// classifyContent classifies the type of high-value content.
func (s *TranscriptQueueSource) classifyContent(content string) string {
	if strings.Contains(content, "actually") || strings.Contains(content, "instead") || strings.Contains(content, "no,") {
		return "correction"
	}
	if strings.Contains(content, "decided") || strings.Contains(content, "chose") || strings.Contains(content, "we're using") {
		return "decision"
	}
	if strings.Contains(content, "pattern") || strings.Contains(content, "convention") || strings.Contains(content, "standard") {
		return "pattern"
	}
	if strings.Contains(content, "avoid") || strings.Contains(content, "don't") || strings.Contains(content, "never") {
		return "constraint"
	}
	return "insight"
}

// isKeyExplanation checks if assistant content contains key explanations.
func (s *TranscriptQueueSource) isKeyExplanation(content string) bool {
	patterns := []string{
		"because", "the reason", "this is why",
		"this approach", "this pattern", "this ensures",
		"by doing this", "this prevents", "this allows",
	}

	for _, p := range patterns {
		if strings.Contains(content, p) {
			return true
		}
	}
	return false
}

// isImportantResult checks if a tool result is worth capturing.
func (s *TranscriptQueueSource) isImportantResult(result string) bool {
	result = strings.ToLower(result)

	// Look for errors that might indicate learning opportunities
	patterns := []string{
		"error:", "failed", "cannot", "not found",
		"syntax error", "type error", "undefined",
	}

	for _, p := range patterns {
		if strings.Contains(result, p) {
			return true
		}
	}
	return false
}

// makeTranscriptItemID creates a unique item ID.
func makeTranscriptItemID(path string, lineNum int) string {
	cleanPath := regexp.MustCompile(`[^a-zA-Z0-9]+`).ReplaceAllString(path, "_")
	return fmt.Sprintf("transcript:%s:%d:%d", cleanPath, lineNum, time.Now().UnixNano())
}
