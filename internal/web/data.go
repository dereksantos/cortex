// Package web provides a lightweight HTTP dashboard for the Cortex daemon.
package web

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	intcognition "github.com/dereksantos/cortex/internal/cognition"
	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/config"
)

// DashboardData is the JSON payload sent to the web dashboard.
type DashboardData struct {
	Mode          string             `json:"mode"`
	ModeDesc      string             `json:"mode_description"`
	Events        int                `json:"events"`
	Insights      int                `json:"insights"`
	ActivityLevel float64            `json:"activity_level"`
	IdleSeconds   int                `json:"idle_seconds"`
	CacheHitRate  string             `json:"cache_hit_rate"`
	DaemonRunning bool               `json:"daemon_running"`
	Activity      []ActivityEntry    `json:"activity"`
	Topics        map[string]float64 `json:"topics"`
	Sessions      []SessionEntry     `json:"sessions"`
	Timestamp     string             `json:"timestamp"`
}

// ActivityEntry is a single activity log line for the dashboard.
type ActivityEntry struct {
	Time        string `json:"time"`
	Mode        string `json:"mode"`
	Description string `json:"description"`
	LatencyMs   int64  `json:"latency_ms,omitempty"`
}

// SessionEntry is a session summary for the dashboard.
type SessionEntry struct {
	SessionID    string `json:"session_id"`
	StartedAt    string `json:"started_at"`
	EventCount   int    `json:"event_count"`
	LastAction   string `json:"last_action"`
	ProjectPath  string `json:"project_path"`
}

// BuildDashboardData reads state files and storage to produce the dashboard payload.
// Errors are silently ignored to return partial data (same approach as the watch TUI).
func BuildDashboardData(cfg *config.Config, store *storage.Storage) *DashboardData {
	d := &DashboardData{
		Mode:      "idle",
		Timestamp: time.Now().Format(time.RFC3339),
	}

	// Daemon state
	statePath := intcognition.GetDaemonStatePath(cfg.ContextDir)
	if state, err := intcognition.ReadDaemonState(statePath); err == nil && state != nil {
		d.DaemonRunning = true
		d.Mode = state.Mode
		d.ModeDesc = state.Description
		d.Events = state.Stats.Events
		d.Insights = state.Stats.Insights
	}

	// Background metrics
	if metrics, err := intcognition.ReadBackgroundMetrics(cfg.ContextDir); err == nil && metrics != nil {
		d.ActivityLevel = metrics.ActivityLevel
		d.IdleSeconds = metrics.IdleSeconds

		total := metrics.CacheHits + metrics.CacheMisses
		if total > 0 {
			pct := float64(metrics.CacheHits) / float64(total) * 100
			d.CacheHitRate = formatPercent(pct)
		} else {
			d.CacheHitRate = "-"
		}
	}

	// Activity log
	if entries, err := intcognition.ReadRecentActivity(cfg.ContextDir, 20); err == nil {
		for _, e := range entries {
			d.Activity = append(d.Activity, ActivityEntry{
				Time:        e.Timestamp.Format("15:04:05"),
				Mode:        e.Mode,
				Description: e.Description,
				LatencyMs:   e.LatencyMs,
			})
		}
	}

	// Sessions from storage
	if store != nil {
		if sessions, err := store.GetRecentSessions(5); err == nil {
			for _, s := range sessions {
				d.Sessions = append(d.Sessions, SessionEntry{
					SessionID:   s.SessionID,
					StartedAt:   s.StartedAt.Format("15:04:05"),
					EventCount:  s.EventCount,
					LastAction:  s.LastAction,
					ProjectPath: s.ProjectPath,
				})
			}
		}

		// Override event/insight counts from storage if higher
		if stats, err := store.GetStats(); err == nil {
			if e, ok := stats["total_events"].(int); ok && e > d.Events {
				d.Events = e
			}
			if i, ok := stats["total_insights"].(int); ok && i > d.Insights {
				d.Insights = i
			}
		}
	}

	// Topic weights from session.json
	sessionPath := filepath.Join(cfg.ContextDir, "session.json")
	if data, err := os.ReadFile(sessionPath); err == nil {
		var session struct {
			TopicWeights map[string]float64 `json:"topic_weights"`
		}
		if json.Unmarshal(data, &session) == nil && len(session.TopicWeights) > 0 {
			d.Topics = session.TopicWeights
		}
	}

	return d
}

func formatPercent(pct float64) string {
	if pct == float64(int(pct)) {
		return fmt.Sprintf("%.0f%%", pct)
	}
	return fmt.Sprintf("%.1f%%", pct)
}
