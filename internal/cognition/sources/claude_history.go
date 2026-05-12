// Package sources provides DreamSource implementations for Dream mode exploration.
package sources

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/dereksantos/cortex/internal/journal"
	"github.com/dereksantos/cortex/pkg/cognition"
)

// ClaudeHistorySource samples from Claude Code session transcripts.
// It parses JSONL files containing session history and extracts
// interesting items like tool uses, corrections, and decisions.
type ClaudeHistorySource struct {
	transcriptDir string // Path to Claude session transcripts
	rng           *rand.Rand
	observer      *Observer
}

// NewClaudeHistorySource creates a new ClaudeHistorySource.
// transcriptDir is typically ~/.claude/projects/<project-hash>/ or
// can be passed via hook input.
func NewClaudeHistorySource(transcriptDir string) *ClaudeHistorySource {
	return &ClaudeHistorySource{
		transcriptDir: transcriptDir,
		rng:           rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// SetObserver wires the source to emit observation.claude_transcript
// entries when transcript files are read. Pass nil to disable.
func (s *ClaudeHistorySource) SetObserver(o *Observer) { s.observer = o }

// Name returns the source identifier.
func (s *ClaudeHistorySource) Name() string {
	return "claude-history"
}

// Sample returns random items from Claude session transcripts.
func (s *ClaudeHistorySource) Sample(ctx context.Context, n int) ([]cognition.DreamItem, error) {
	// Find transcript files (*.jsonl)
	transcripts, err := s.findTranscripts()
	if err != nil {
		return nil, err
	}

	if len(transcripts) == 0 {
		return nil, nil
	}

	// Sample from transcripts
	var items []cognition.DreamItem

	// Limit transcripts to process
	maxTranscripts := 5
	if len(transcripts) > maxTranscripts {
		// Shuffle and take most recent
		s.rng.Shuffle(len(transcripts), func(i, j int) {
			transcripts[i], transcripts[j] = transcripts[j], transcripts[i]
		})
		transcripts = transcripts[:maxTranscripts]
	}

	for _, path := range transcripts {
		if ctx.Err() != nil {
			break
		}

		entries, err := s.parseTranscript(path)
		if err != nil {
			continue
		}

		// Add interesting entries
		for _, entry := range entries {
			items = append(items, entry)
			if len(items) >= n*2 { // Collect extra for random sampling
				break
			}
		}
	}

	// Randomly sample n items
	if len(items) > n {
		s.rng.Shuffle(len(items), func(i, j int) {
			items[i], items[j] = items[j], items[i]
		})
		items = items[:n]
	}

	return items, nil
}

// findTranscripts locates Claude session transcript files.
func (s *ClaudeHistorySource) findTranscripts() ([]string, error) {
	var transcripts []string

	err := filepath.WalkDir(s.transcriptDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // Skip errors
		}

		if d.IsDir() {
			return nil
		}

		// Look for JSONL files (session transcripts)
		if strings.HasSuffix(path, ".jsonl") {
			transcripts = append(transcripts, path)
		}

		// Limit total files
		if len(transcripts) > 20 {
			return filepath.SkipAll
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return transcripts, nil
}

// transcriptEntry represents a line in a Claude session transcript.
type transcriptEntry struct {
	Type       string          `json:"type"`
	Content    string          `json:"content"`
	ToolCalls  []toolCall      `json:"tool_calls,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	ToolResult json.RawMessage `json:"result,omitempty"`
}

type toolCall struct {
	Name  string                 `json:"name"`
	Input map[string]interface{} `json:"input,omitempty"`
}

// parseTranscript parses a JSONL transcript file and extracts interesting items.
func (s *ClaudeHistorySource) parseTranscript(path string) ([]cognition.DreamItem, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// Observe the substrate (full file content hash, no copy retained).
	if s.observer != nil {
		if content, err := os.ReadFile(path); err == nil {
			var mod time.Time
			if info, statErr := os.Stat(path); statErr == nil {
				mod = info.ModTime()
			}
			s.observer.Observe(journal.TypeObservationClaudeTranscript, s.Name(),
				"file://"+path, content, mod)
		}
	}

	var items []cognition.DreamItem
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 1MB max line

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Bytes()

		var entry transcriptEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}

		// Extract interesting items based on entry type
		extracted := s.extractFromEntry(&entry, path, lineNum)
		items = append(items, extracted...)

		// Limit items per file
		if len(items) > 50 {
			break
		}
	}

	return items, scanner.Err()
}

// extractFromEntry extracts DreamItems from a transcript entry.
func (s *ClaudeHistorySource) extractFromEntry(entry *transcriptEntry, path string, lineNum int) []cognition.DreamItem {
	var items []cognition.DreamItem

	relPath, _ := filepath.Rel(s.transcriptDir, path)

	switch entry.Type {
	case "user":
		// Check for corrections like "no, use X instead" or "actually, we should"
		if s.isCorrection(entry.Content) {
			items = append(items, cognition.DreamItem{
				ID:      makeItemID("claude-history", relPath, lineNum),
				Source:  "claude-history",
				Content: entry.Content,
				Path:    relPath,
				Metadata: map[string]any{
					"type":     "correction",
					"line_num": lineNum,
				},
			})
		}

		// Check for decisions/preferences
		if s.isDecision(entry.Content) {
			items = append(items, cognition.DreamItem{
				ID:      makeItemID("claude-history", relPath, lineNum),
				Source:  "claude-history",
				Content: entry.Content,
				Path:    relPath,
				Metadata: map[string]any{
					"type":     "decision",
					"line_num": lineNum,
				},
			})
		}

	case "assistant":
		// Extract tool uses
		for _, tc := range entry.ToolCalls {
			if s.isSignificantTool(tc.Name) {
				content := formatToolCall(tc)
				items = append(items, cognition.DreamItem{
					ID:      makeItemID("claude-history", relPath, lineNum),
					Source:  "claude-history",
					Content: content,
					Path:    relPath,
					Metadata: map[string]any{
						"type":      "tool_use",
						"tool_name": tc.Name,
						"line_num":  lineNum,
					},
				})
			}
		}

	case "tool_result":
		// Check for errors and fixes
		if s.isErrorOrFix(entry) {
			items = append(items, cognition.DreamItem{
				ID:      makeItemID("claude-history", relPath, lineNum),
				Source:  "claude-history",
				Content: string(entry.ToolResult),
				Path:    relPath,
				Metadata: map[string]any{
					"type":      "error_fix",
					"tool_name": entry.ToolName,
					"line_num":  lineNum,
				},
			})
		}
	}

	return items
}

// isCorrection checks if user message is a correction.
func (s *ClaudeHistorySource) isCorrection(content string) bool {
	content = strings.ToLower(content)

	correctionPatterns := []string{
		"no,",
		"no.",
		"actually,",
		"actually we",
		"instead,",
		"use x not y",
		"don't use",
		"we use",
		"we don't use",
		"that's wrong",
		"that's not right",
		"incorrect",
		"the correct",
		"the right way",
		"not that way",
		"we prefer",
		"we always",
		"we never",
	}

	for _, pattern := range correctionPatterns {
		if strings.Contains(content, pattern) {
			return true
		}
	}

	return false
}

// isDecision checks if user message contains a decision.
func (s *ClaudeHistorySource) isDecision(content string) bool {
	content = strings.ToLower(content)

	decisionPatterns := []string{
		"we decided",
		"the decision is",
		"let's go with",
		"let's use",
		"we'll use",
		"we should use",
		"we chose",
		"we're using",
		"our approach is",
		"the pattern is",
		"the convention is",
		"our convention",
		"our pattern",
		"our standard",
	}

	for _, pattern := range decisionPatterns {
		if strings.Contains(content, pattern) {
			return true
		}
	}

	return false
}

// isSignificantTool checks if a tool use is worth capturing.
func (s *ClaudeHistorySource) isSignificantTool(name string) bool {
	significantTools := map[string]bool{
		"Write":        true,
		"Edit":         true,
		"Bash":         true,
		"Read":         false, // Too common, skip
		"Grep":         false,
		"Glob":         false,
		"WebFetch":     true,
		"TodoWrite":    true,
		"NotebookEdit": true,
	}

	return significantTools[name]
}

// isErrorOrFix checks if a tool result contains an error and subsequent fix.
func (s *ClaudeHistorySource) isErrorOrFix(entry *transcriptEntry) bool {
	if len(entry.ToolResult) == 0 {
		return false
	}

	result := strings.ToLower(string(entry.ToolResult))

	errorPatterns := []string{
		"error:",
		"failed to",
		"cannot",
		"couldn't",
		"not found",
		"undefined",
		"unexpected",
		"permission denied",
		"syntax error",
		"type error",
	}

	for _, pattern := range errorPatterns {
		if strings.Contains(result, pattern) {
			return true
		}
	}

	return false
}

// formatToolCall formats a tool call for content.
func formatToolCall(tc toolCall) string {
	var sb strings.Builder
	sb.WriteString("Tool: ")
	sb.WriteString(tc.Name)
	sb.WriteString("\n")

	if filePath, ok := tc.Input["file_path"].(string); ok {
		sb.WriteString("File: ")
		sb.WriteString(filePath)
		sb.WriteString("\n")
	}

	if command, ok := tc.Input["command"].(string); ok {
		sb.WriteString("Command: ")
		// Truncate long commands
		if len(command) > 200 {
			command = command[:200] + "..."
		}
		sb.WriteString(command)
		sb.WriteString("\n")
	}

	if content, ok := tc.Input["content"].(string); ok {
		// Truncate long content
		if len(content) > 500 {
			content = content[:500] + "..."
		}
		sb.WriteString("Content:\n")
		sb.WriteString(content)
	}

	return sb.String()
}

// makeItemID creates a unique item ID.
func makeItemID(source, path string, lineNum int) string {
	// Clean path for ID
	cleanPath := regexp.MustCompile(`[^a-zA-Z0-9]+`).ReplaceAllString(path, "_")
	return fmt.Sprintf("%s:%s:%d", source, cleanPath, lineNum)
}
