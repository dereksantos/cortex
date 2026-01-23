package cognition

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/dereksantos/cortex/internal/storage"
	"github.com/dereksantos/cortex/pkg/config"
)

// PruneConfig holds configuration for context pruning.
type PruneConfig struct {
	// SizeMultiplier is how many times larger Cortex can be vs project code.
	// Default: 3.0 (Cortex can be 3x project size)
	SizeMultiplier float64

	// MinProjectSize is the minimum assumed project size in bytes.
	// Prevents aggressive pruning on tiny projects.
	// Default: 10KB
	MinProjectSize int64

	// MinRetainCount is the minimum number of insights to always keep.
	// Default: 50
	MinRetainCount int

	// BatchSize is how many items to prune per cycle.
	// Default: 20
	BatchSize int

	// AgeWeight controls how much age affects pruning score (0-1).
	// Higher = older items pruned more aggressively.
	// Default: 0.6
	AgeWeight float64

	// ImportanceWeight controls how much importance affects pruning score (0-1).
	// Higher = low-importance items pruned more aggressively.
	// Default: 0.4
	ImportanceWeight float64
}

// DefaultPruneConfig returns sensible defaults for pruning.
func DefaultPruneConfig() PruneConfig {
	return PruneConfig{
		SizeMultiplier:   3.0,
		MinProjectSize:   10 * 1024, // 10KB
		MinRetainCount:   50,
		BatchSize:        20,
		AgeWeight:        0.6,
		ImportanceWeight: 0.4,
	}
}

// Pruner manages context size relative to project size.
type Pruner struct {
	mu sync.Mutex

	storage     *storage.Storage
	config      PruneConfig
	projectRoot string

	// Cached sizes (refreshed periodically)
	projectSize int64
	cortexSize  int64
	lastCheck   time.Time

	// State
	running     bool
	stateWriter *StateWriter
}

// NewPruner creates a new Pruner instance.
func NewPruner(store *storage.Storage, cfg *config.Config) *Pruner {
	return &Pruner{
		storage:     store,
		config:      DefaultPruneConfig(),
		projectRoot: cfg.ProjectRoot,
	}
}

// SetStateWriter sets the state writer for daemon status updates.
func (p *Pruner) SetStateWriter(sw *StateWriter) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stateWriter = sw
}

// SetConfig updates the pruner configuration.
func (p *Pruner) SetConfig(cfg PruneConfig) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.config = cfg
}

// GetProjectSize calculates the total size of source files in the project.
// Excludes directories (.git, node_modules), binary files, and large files.
func (p *Pruner) GetProjectSize() (int64, error) {
	if p.projectRoot == "" {
		return 0, fmt.Errorf("project root not set")
	}

	var totalSize int64

	// Directories to skip
	skipDirs := map[string]bool{
		".git": true, "node_modules": true, "vendor": true,
		"dist": true, "build": true, "target": true,
		"__pycache__": true, ".venv": true, "venv": true,
		".cortex": true,
	}

	// Binary/non-source extensions to skip
	skipExts := map[string]bool{
		// Compiled/binary
		".exe": true, ".bin": true, ".o": true, ".a": true, ".so": true,
		".dylib": true, ".dll": true, ".class": true, ".pyc": true,
		// Images
		".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
		".ico": true, ".svg": true, ".webp": true, ".bmp": true,
		// Archives
		".zip": true, ".tar": true, ".gz": true, ".rar": true,
		// Media
		".mp3": true, ".mp4": true, ".wav": true, ".avi": true,
		// Data/DB
		".db": true, ".sqlite": true, ".sqlite3": true,
		// Fonts
		".ttf": true, ".otf": true, ".woff": true, ".woff2": true,
		// PDFs and office
		".pdf": true, ".doc": true, ".docx": true, ".xls": true,
	}

	// Max file size to count (1MB) - larger files are likely not source
	const maxFileSize = 1 * 1024 * 1024

	err := filepath.Walk(p.projectRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		if info.IsDir() {
			if skipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}

		// Skip large files
		if info.Size() > maxFileSize {
			return nil
		}

		// Skip binary extensions
		ext := filepath.Ext(info.Name())
		if skipExts[ext] {
			return nil
		}

		totalSize += info.Size()
		return nil
	})

	if err != nil {
		return 0, fmt.Errorf("failed to walk project: %w", err)
	}

	return totalSize, nil
}

// GetCortexSize returns the current size of Cortex storage in bytes.
func (p *Pruner) GetCortexSize() (int64, error) {
	if p.storage == nil {
		return 0, fmt.Errorf("storage not initialized")
	}

	stats, err := p.storage.GetStats()
	if err != nil {
		return 0, fmt.Errorf("failed to get storage stats: %w", err)
	}

	// Get database file size if available
	if dbSize, ok := stats["db_size_bytes"].(int64); ok {
		return dbSize, nil
	}

	// Estimate from counts if db_size not available
	// Rough estimates: event ~500 bytes, insight ~200 bytes, embedding ~3KB
	var estimated int64
	if events, ok := stats["total_events"].(int); ok {
		estimated += int64(events) * 500
	}
	if insights, ok := stats["total_insights"].(int); ok {
		estimated += int64(insights) * 200
	}
	if embeddings, ok := stats["total_embeddings"].(int); ok {
		estimated += int64(embeddings) * 3000
	}

	return estimated, nil
}

// ShouldPrune checks if pruning is needed based on size ratio.
func (p *Pruner) ShouldPrune() (bool, float64, error) {
	projectSize, err := p.GetProjectSize()
	if err != nil {
		return false, 0, err
	}

	// Apply minimum project size
	if projectSize < p.config.MinProjectSize {
		projectSize = p.config.MinProjectSize
	}

	cortexSize, err := p.GetCortexSize()
	if err != nil {
		return false, 0, err
	}

	maxSize := int64(float64(projectSize) * p.config.SizeMultiplier)
	ratio := float64(cortexSize) / float64(projectSize)

	p.mu.Lock()
	p.projectSize = projectSize
	p.cortexSize = cortexSize
	p.lastCheck = time.Now()
	p.mu.Unlock()

	return cortexSize > maxSize, ratio, nil
}

// PruneResult contains the results of a prune operation.
type PruneResult struct {
	Pruned       int           // Number of items pruned
	ProjectSize  int64         // Project size in bytes
	CortexSize   int64         // Cortex size before pruning
	NewSize      int64         // Cortex size after pruning
	Ratio        float64       // Size ratio (cortex/project)
	Duration     time.Duration // How long pruning took
	Skipped      bool          // True if pruning was skipped
	SkipReason   string        // Why pruning was skipped
}

// MaybePrune checks if pruning is needed and performs it if so.
func (p *Pruner) MaybePrune(ctx context.Context) (*PruneResult, error) {
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return &PruneResult{Skipped: true, SkipReason: "already running"}, nil
	}
	p.running = true
	stateWriter := p.stateWriter
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		p.running = false
		p.mu.Unlock()
	}()

	// Check if pruning needed
	shouldPrune, ratio, err := p.ShouldPrune()
	if err != nil {
		return nil, fmt.Errorf("failed to check prune status: %w", err)
	}

	p.mu.Lock()
	projectSize := p.projectSize
	cortexSize := p.cortexSize
	p.mu.Unlock()

	if !shouldPrune {
		return &PruneResult{
			Skipped:     true,
			SkipReason:  fmt.Sprintf("under limit (ratio: %.1fx)", ratio),
			ProjectSize: projectSize,
			CortexSize:  cortexSize,
			Ratio:       ratio,
		}, nil
	}

	// Update state
	if stateWriter != nil {
		stateWriter.WriteMode("prune", fmt.Sprintf("Context %.1fx project size, pruning...", ratio))
	}

	start := time.Now()

	// Perform pruning
	pruned, err := p.pruneInsights(ctx)
	if err != nil {
		if stateWriter != nil {
			stateWriter.WriteMode("idle", "")
		}
		return nil, fmt.Errorf("prune failed: %w", err)
	}

	// Get new size
	newSize, _ := p.GetCortexSize()
	newRatio := float64(newSize) / float64(projectSize)

	if stateWriter != nil {
		stateWriter.WriteMode("idle", "")
	}

	log.Printf("Prune: removed %d insights (%.1fx -> %.1fx project size)", pruned, ratio, newRatio)

	return &PruneResult{
		Pruned:      pruned,
		ProjectSize: projectSize,
		CortexSize:  cortexSize,
		NewSize:     newSize,
		Ratio:       newRatio,
		Duration:    time.Since(start),
	}, nil
}

// pruneInsights removes low-value insights based on age and importance.
func (p *Pruner) pruneInsights(ctx context.Context) (int, error) {
	if p.storage == nil {
		return 0, fmt.Errorf("storage not initialized")
	}

	// Get all insights with their scores
	insights, err := p.storage.GetRecentInsights(1000) // Get up to 1000
	if err != nil {
		return 0, fmt.Errorf("failed to fetch insights: %w", err)
	}

	// Check minimum retain count
	if len(insights) <= p.config.MinRetainCount {
		return 0, nil
	}

	// Score each insight for pruning (higher score = more likely to prune)
	type scoredInsight struct {
		ID    int64
		Score float64
	}

	now := time.Now()
	maxAge := 30 * 24 * time.Hour // 30 days as reference max age
	scored := make([]scoredInsight, 0, len(insights))

	for _, ins := range insights {
		// Age score: older = higher (more likely to prune)
		age := now.Sub(ins.CreatedAt)
		ageScore := float64(age) / float64(maxAge)
		if ageScore > 1.0 {
			ageScore = 1.0
		}

		// Importance score: lower importance = higher prune score
		// Importance is 0-10, invert it
		importanceScore := 1.0 - (float64(ins.Importance) / 10.0)

		// Combined score
		score := (ageScore * p.config.AgeWeight) + (importanceScore * p.config.ImportanceWeight)

		scored = append(scored, scoredInsight{ID: ins.ID, Score: score})
	}

	// Sort by score descending (highest scores = prune first)
	for i := 0; i < len(scored)-1; i++ {
		for j := i + 1; j < len(scored); j++ {
			if scored[j].Score > scored[i].Score {
				scored[i], scored[j] = scored[j], scored[i]
			}
		}
	}

	// Prune up to BatchSize, but keep MinRetainCount
	maxPrune := len(insights) - p.config.MinRetainCount
	if maxPrune <= 0 {
		return 0, nil
	}
	if maxPrune > p.config.BatchSize {
		maxPrune = p.config.BatchSize
	}

	// Delete the highest-scoring (least valuable) insights
	pruned := 0
	for i := 0; i < maxPrune; i++ {
		if err := p.storage.ForgetInsight(scored[i].ID); err != nil {
			log.Printf("Prune: failed to delete insight %d: %v", scored[i].ID, err)
			continue
		}
		pruned++
	}

	return pruned, nil
}

// GetSizeReport returns a human-readable size report.
func (p *Pruner) GetSizeReport() (string, error) {
	projectSize, err := p.GetProjectSize()
	if err != nil {
		return "", err
	}

	cortexSize, err := p.GetCortexSize()
	if err != nil {
		return "", err
	}

	ratio := float64(cortexSize) / float64(projectSize)
	maxRatio := p.config.SizeMultiplier

	status := "OK"
	if ratio > maxRatio {
		status = "OVER LIMIT"
	} else if ratio > maxRatio*0.8 {
		status = "WARNING"
	}

	return fmt.Sprintf(
		"Project: %s | Cortex: %s | Ratio: %.1fx (max %.1fx) | %s",
		formatBytes(projectSize),
		formatBytes(cortexSize),
		ratio,
		maxRatio,
		status,
	), nil
}

// formatBytes formats bytes as human-readable string.
func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
