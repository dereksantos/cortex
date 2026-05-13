// Package commands provides CLI command implementations.
package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	intcognition "github.com/dereksantos/cortex/internal/cognition"
	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/config"
)

// WatchData aggregates all data needed for the watch command display.
// This eliminates duplicate file reads and provides a single source of truth.
type WatchData struct {
	// Core state
	Daemon     *intcognition.DaemonState
	Retrieval  *intcognition.RetrievalStats
	Background *intcognition.BackgroundMetrics
	Activity   []intcognition.ActivityLogEntry
	Sessions   []*storage.SessionMetadata

	// Derived data
	TopicWeights  map[string]float64
	RecentQueries []string

	// Aggregate stats
	TotalEvents   int
	TotalInsights int
}

// NewWatchData creates a new WatchData and loads all state.
func NewWatchData(cfg *config.Config, store *storage.Storage) *WatchData {
	w := &WatchData{}
	w.Refresh(cfg, store)
	return w
}

// Refresh reloads all state data from files and storage.
func (w *WatchData) Refresh(cfg *config.Config, store *storage.Storage) {
	// Read daemon state (live mode heartbeat — not in journal)
	statePath := intcognition.GetDaemonStatePath(cfg.ContextDir)
	w.Daemon, _ = intcognition.ReadDaemonState(statePath)

	// Read retrieval stats from journal projection (post-Z1 unification:
	// resolve.retrieval entries are the source of truth, the legacy
	// retrieval_stats.json snapshot is no longer written). Falls back to
	// the legacy file when storage is unavailable so this command works
	// against historical .cortex/ directories.
	w.Retrieval = retrievalStatsFromStorage(store)
	if w.Retrieval == nil {
		w.Retrieval, _ = intcognition.ReadRetrievalStats(cfg.ContextDir)
	}

	// Read background metrics
	w.Background, _ = intcognition.ReadBackgroundMetrics(cfg.ContextDir)

	// Read recent activity
	w.Activity, _ = intcognition.ReadRecentActivity(cfg.ContextDir, 10)

	// Get sessions from storage
	if store != nil {
		w.Sessions, _ = store.GetRecentSessions(5)
	}

	// Read session context for topic weights and recent queries
	sessionPath := filepath.Join(cfg.ContextDir, "session.json")
	if sessionData, err := os.ReadFile(sessionPath); err == nil {
		var session struct {
			TopicWeights  map[string]float64 `json:"topic_weights"`
			RecentQueries []struct {
				Text string `json:"text"`
			} `json:"recent_queries"`
		}
		if json.Unmarshal(sessionData, &session) == nil {
			w.TopicWeights = session.TopicWeights
			for _, q := range session.RecentQueries {
				w.RecentQueries = append(w.RecentQueries, q.Text)
			}
		}
	}

	// Compute aggregate stats (use highest of daemon state or storage)
	if w.Daemon != nil {
		w.TotalEvents = w.Daemon.Stats.Events
		w.TotalInsights = w.Daemon.Stats.Insights
	}
	if store != nil {
		if stats, err := store.GetStats(); err == nil {
			if e, ok := stats["total_events"].(int); ok && e > w.TotalEvents {
				w.TotalEvents = e
			}
			if i, ok := stats["total_insights"].(int); ok && i > w.TotalInsights {
				w.TotalInsights = i
			}
		}
	}
}

// retrievalStatsFromStorage materializes the watch UI's RetrievalStats
// view from the journal-projected storage.Retrievals list. Returns nil
// when no retrievals have been projected yet (caller falls back to the
// legacy retrieval_stats.json reader for historical data).
func retrievalStatsFromStorage(store *storage.Storage) *intcognition.RetrievalStats {
	if store == nil {
		return nil
	}
	recent := store.GetRetrievals(1)
	if len(recent) == 0 {
		return nil
	}
	agg := store.GetRetrievalStats()
	last := recent[0]
	return &intcognition.RetrievalStats{
		LastQuery:       last.QueryText,
		LastMode:        last.Mode,
		LastReflexMs:    0, // not separately measured in the journal
		LastReflectMs:   0, // not separately measured in the journal
		LastResolveMs:   last.ResolveMs,
		LastResults:     last.ResultCount,
		LastDecision:    last.Decision,
		TotalRetrievals: agg.Total,
		UpdatedAt:       last.RecordedAt,
	}
}

// RefreshSessions only refreshes session data (lighter weight).
func (w *WatchData) RefreshSessions(store *storage.Storage) {
	if store != nil {
		w.Sessions, _ = store.GetRecentSessions(5)
	}
}

// ModeStatus returns the current mode icon, name, and description.
func (w *WatchData) ModeStatus(frame int, animated bool) (icon, name, desc string) {
	icon = "○"
	name = "IDLE"
	desc = ""

	if w.Daemon != nil && w.Daemon.Mode != "" && w.Daemon.Mode != "idle" {
		if animated {
			icon = getAnimatedModeSpinner(w.Daemon.Mode, frame)
		} else {
			icon = getModeSpinner(w.Daemon.Mode)
		}

		name = strings.ToUpper(w.Daemon.Mode)
		if animated {
			switch w.Daemon.Mode {
			case "dream":
				name = "DREAMING"
			case "think":
				name = "THINKING"
			case "reflect":
				name = "REFLECTING"
			case "reflex":
				name = "REFLEX"
			case "resolve":
				name = "RESOLVING"
			default:
				name = strings.ToUpper(w.Daemon.Mode) + "ING"
			}
		}

		desc = w.Daemon.Description
		if desc == "" {
			desc = getDefaultModeDescription(w.Daemon.Mode)
		}
	}

	return icon, name, desc
}

// TopTopics returns the top N topics by weight (above minWeight).
func (w *WatchData) TopTopics(n int, minWeight float64) []TopicWeight {
	if len(w.TopicWeights) == 0 {
		return nil
	}

	topics := make([]TopicWeight, 0, len(w.TopicWeights))
	for topic, weight := range w.TopicWeights {
		if weight >= minWeight {
			topics = append(topics, TopicWeight{Topic: topic, Weight: weight})
		}
	}

	sort.Slice(topics, func(i, j int) bool {
		return topics[i].Weight > topics[j].Weight
	})

	if len(topics) > n {
		topics = topics[:n]
	}
	return topics
}

// TopicWeight represents a topic with its weight.
type TopicWeight struct {
	Topic  string
	Weight float64
}

// ABR computes the Agentic Benefit Ratio.
// ABR = quality(Fast + Think) / quality(Full)
// Returns 0 if not enough data is available.
func (w *WatchData) ABR() float64 {
	// For now, use cache hit rate as a proxy for ABR
	// A high cache hit rate means Think is successfully pre-computing
	// what would otherwise require Full mode
	if w.Background != nil && w.Background.CacheHitRate > 0 {
		return w.Background.CacheHitRate
	}
	return 0
}

// CacheHitRate returns the Think cache hit rate as a percentage string.
func (w *WatchData) CacheHitRate() string {
	if w.Background == nil {
		return "-"
	}
	total := w.Background.CacheHits + w.Background.CacheMisses
	if total == 0 {
		return "-"
	}
	pct := float64(w.Background.CacheHits) / float64(total) * 100
	return strings.TrimSuffix(strings.TrimSuffix(
		strings.TrimRight(strings.TrimRight(
			formatFloat(pct, 1), "0"), "."), "%")+"%", "")
}

// formatFloat formats a float with the specified precision.
func formatFloat(f float64, precision int) string {
	format := "%." + string(rune('0'+precision)) + "f"
	return strings.TrimRight(strings.TrimRight(
		strings.Replace(
			strings.Replace(
				strings.Replace(format, "%", "", 1),
				"f", "", 1),
			".", "", 1),
		"0"), ".")
}

// ThinkStatus returns a human-readable Think mode status.
func (w *WatchData) ThinkStatus() string {
	if w.Background == nil {
		return "inactive"
	}
	if w.Background.ActivityLevel > 0.7 {
		return "active (busy)"
	} else if w.Background.ActivityLevel > 0.3 {
		return "active"
	}
	return "idle"
}

// DreamStatus returns a human-readable Dream mode status.
func (w *WatchData) DreamStatus() string {
	if w.Background == nil {
		return "inactive"
	}
	if w.Background.IdleSeconds < 30 {
		return "waiting"
	}
	if w.Background.DreamQueueDepth > 0 {
		return "processing"
	}
	return "idle"
}

// Note: getModeSpinner and getDefaultModeDescription are defined in debug.go
