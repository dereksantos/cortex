package fractal

import (
	"container/list"
	"encoding/json"
	"hash/maphash"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// DefaultNoveltyCapacity is the LRU upper bound.
	DefaultNoveltyCapacity = 4096
	// DefaultSkipWindow is how long an item with the same content stays
	// "seen" before Dream will analyze it again.
	DefaultSkipWindow = 30 * time.Minute
)

// Novelty is an LRU cache of recently analyzed item IDs and their
// content hashes. It lets Dream skip items that already produced a
// verdict in the recent past, unless the content has changed.
type Novelty struct {
	mu       sync.Mutex
	cap      int
	skipWin  time.Duration
	order    *list.List
	index    map[string]*list.Element
	hashSeed maphash.Seed
}

type noveltyEntry struct {
	ItemID        string    `json:"item_id"`
	ContentHash   uint64    `json:"content_hash"`
	LastSeen      time.Time `json:"last_seen"`
	LastInsightAt time.Time `json:"last_insight_at,omitempty"`
}

// NewNovelty constructs a Novelty cache.
func NewNovelty(capacity int, skipWindow time.Duration) *Novelty {
	if capacity <= 0 {
		capacity = DefaultNoveltyCapacity
	}
	if skipWindow <= 0 {
		skipWindow = DefaultSkipWindow
	}
	return &Novelty{
		cap:      capacity,
		skipWin:  skipWindow,
		order:    list.New(),
		index:    make(map[string]*list.Element),
		hashSeed: maphash.MakeSeed(),
	}
}

// HashContent returns a stable hash for a region's content. The seed is
// per-process; it does not need to round-trip through the snapshot for
// dedupe to be effective within a daemon lifetime.
func (n *Novelty) HashContent(content string) uint64 {
	var h maphash.Hash
	h.SetSeed(n.hashSeed)
	_, _ = h.WriteString(content)
	return h.Sum64()
}

// Seen reports whether the given itemID was analyzed within the skip
// window with matching content. Returns false if the content hash is
// different (the file changed) or the entry is older than the window.
func (n *Novelty) Seen(itemID string, contentHash uint64) bool {
	n.mu.Lock()
	defer n.mu.Unlock()

	el, ok := n.index[itemID]
	if !ok {
		return false
	}
	e := el.Value.(*noveltyEntry)
	if e.ContentHash != contentHash {
		return false
	}
	return time.Since(e.LastSeen) < n.skipWin
}

// RecordSeen registers that itemID was analyzed with the given content
// hash. gotInsight=true updates LastInsightAt.
func (n *Novelty) RecordSeen(itemID string, contentHash uint64, gotInsight bool) {
	n.mu.Lock()
	defer n.mu.Unlock()

	now := time.Now()
	if el, ok := n.index[itemID]; ok {
		e := el.Value.(*noveltyEntry)
		e.ContentHash = contentHash
		e.LastSeen = now
		if gotInsight {
			e.LastInsightAt = now
		}
		n.order.MoveToFront(el)
		return
	}
	e := &noveltyEntry{
		ItemID:      itemID,
		ContentHash: contentHash,
		LastSeen:    now,
	}
	if gotInsight {
		e.LastInsightAt = now
	}
	el := n.order.PushFront(e)
	n.index[itemID] = el
	for n.order.Len() > n.cap {
		back := n.order.Back()
		if back == nil {
			break
		}
		evicted := back.Value.(*noveltyEntry)
		delete(n.index, evicted.ItemID)
		n.order.Remove(back)
	}
}

// Snapshot serializes the cache to disk. Best-effort; errors are returned
// but not fatal to Dream operation.
func (n *Novelty) Snapshot(path string) error {
	n.mu.Lock()
	entries := make([]*noveltyEntry, 0, n.order.Len())
	for e := n.order.Front(); e != nil; e = e.Next() {
		entries = append(entries, e.Value.(*noveltyEntry))
	}
	n.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(f).Encode(entries); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load restores a previous snapshot. Missing or corrupt files are
// non-fatal: the cache simply starts empty.
func (n *Novelty) Load(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	var entries []*noveltyEntry
	if err := json.NewDecoder(f).Decode(&entries); err != nil {
		return nil // corrupt — start empty
	}

	n.mu.Lock()
	defer n.mu.Unlock()
	n.order = list.New()
	n.index = make(map[string]*list.Element, len(entries))
	for _, e := range entries {
		if len(n.index) >= n.cap {
			break
		}
		el := n.order.PushBack(e)
		n.index[e.ItemID] = el
	}
	return nil
}

// Len returns the number of cached entries (for tests and metrics).
func (n *Novelty) Len() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.order.Len()
}
