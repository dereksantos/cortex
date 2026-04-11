package measure

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// InjectionRecord tracks a context injection decision for feedback analysis.
type InjectionRecord struct {
	Timestamp time.Time `json:"ts"`
	Query     string    `json:"query"`
	ContentID string    `json:"content_id"`
	Worth     float64   `json:"worth"`
	Decision  string    `json:"decision"`   // inject, queue, wait, discard
	SessionID string    `json:"session_id"` // from hook context
}

// feedbackFile is the JSONL file within the context directory.
const feedbackFile = "measure_feedback.jsonl"

// RecordInjection appends an injection record to the feedback file.
func RecordInjection(contextDir string, record InjectionRecord) error {
	path := filepath.Join(contextDir, feedbackFile)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	data, err := json.Marshal(record)
	if err != nil {
		return err
	}

	_, err = f.Write(append(data, '\n'))
	return err
}

// LoadRecords reads all injection records from the feedback file.
func LoadRecords(contextDir string) ([]InjectionRecord, error) {
	path := filepath.Join(contextDir, feedbackFile)

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var records []InjectionRecord
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var record InjectionRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			continue // Skip malformed lines
		}
		records = append(records, record)
	}

	return records, scanner.Err()
}
