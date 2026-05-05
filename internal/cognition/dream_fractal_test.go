package cognition

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dereksantos/cortex/internal/cognition/fractal"
	"github.com/dereksantos/cortex/pkg/cognition"
)

// TestDream_EnqueueNeighbors verifies that an item carrying region
// metadata produces neighbor follow-ups in the queue, and that
// buildFollowUpItem reconstructs a DreamItem with parent_item_id set.
func TestDream_EnqueueNeighbors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.go")
	body := strings.Repeat("// content line\n", 4096) // ~64 KiB
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	d := NewDream(nil, nil, NewActivityTracker(), "")

	parent := cognition.DreamItem{
		ID:     "project:big.go#offset=8192",
		Source: "project",
		Path:   "big.go",
		Metadata: map[string]any{
			"region_offset": int64(8192),
			"region_len":    4096,
			"file_size":     int64(len(body)),
			"full_path":     path,
			"rel_path":      "big.go",
		},
	}

	d.enqueueNeighbors(parent, 0)

	if d.followUps.Len() == 0 {
		t.Fatal("enqueueNeighbors should have queued at least one follow-up")
	}

	drained := d.followUps.Drain(8)
	if len(drained) == 0 {
		t.Fatal("drain returned nothing")
	}
	for _, fu := range drained {
		if fu.ParentItemID != parent.ID {
			t.Errorf("follow-up parent should be %q, got %q", parent.ID, fu.ParentItemID)
		}
		if fu.Depth != 1 {
			t.Errorf("follow-up depth should be 1, got %d", fu.Depth)
		}
		if fu.Source != "project" {
			t.Errorf("follow-up source should be 'project', got %q", fu.Source)
		}
		item, ok := buildFollowUpItem(fu)
		if !ok {
			t.Fatal("buildFollowUpItem failed for valid region")
		}
		if got, _ := item.Metadata["parent_item_id"].(string); got != parent.ID {
			t.Errorf("rebuilt item missing parent_item_id; got %q", got)
		}
		if got, _ := metaInt(item.Metadata, "fractal_depth"); got != 1 {
			t.Errorf("rebuilt item missing fractal_depth=1; got %d", got)
		}
		if !strings.HasPrefix(item.ID, "project:big.go#offset=") {
			t.Errorf("rebuilt ID should preserve offset suffix; got %q", item.ID)
		}
		if item.Content == "" {
			t.Errorf("rebuilt item should have non-empty content")
		}
	}
}

// TestDream_NoEnqueueWithoutRegionMeta verifies that a non-region item
// (whole-file source like cortex/git) does not trigger neighbor enqueues.
func TestDream_NoEnqueueWithoutRegionMeta(t *testing.T) {
	d := NewDream(nil, nil, NewActivityTracker(), "")

	d.enqueueNeighbors(cognition.DreamItem{
		ID:       "git:commit:abcdef12",
		Source:   "git",
		Path:     "abcdef12",
		Metadata: map[string]any{}, // no region_offset/region_len
	}, 0)
	if d.followUps.Len() != 0 {
		t.Errorf("non-region item must not enqueue follow-ups, got %d in queue", d.followUps.Len())
	}
}

// TestDream_NoveltyDedupesIdenticalRegions verifies that an item analyzed
// once is skipped on a second processItem call when content is unchanged
// — regardless of LLM availability (provider is nil → analyzeItem is a
// no-op, novelty still records).
func TestDream_NoveltyDedupesIdenticalRegions(t *testing.T) {
	d := NewDream(nil, nil, NewActivityTracker(), "")

	item := cognition.DreamItem{
		ID:      "project:foo.go#offset=0",
		Source:  "project",
		Content: "package foo\n",
		Path:    "foo.go",
		Metadata: map[string]any{
			"region_offset": int64(0),
			"region_len":    11,
			"file_size":     int64(11),
		},
	}
	gotInsight, skipped := d.processItem(t.Context(), item, nil, 0)
	if gotInsight {
		t.Errorf("with nil LLM no insight is possible")
	}
	if skipped {
		t.Errorf("first call must not be skipped")
	}

	// Second call with identical content must be skipped.
	_, skipped2 := d.processItem(t.Context(), item, nil, 0)
	if !skipped2 {
		t.Errorf("identical content must be deduped on second call")
	}

	// Mutating the content should defeat the dedupe.
	item.Content = "package foo\n// edited\n"
	_, skipped3 := d.processItem(t.Context(), item, nil, 0)
	if skipped3 {
		t.Errorf("changed content must not be deduped")
	}
}

// TestDream_BuildFollowUpItemMissingFile is graceful: a queued follow-up
// whose file disappeared between cycles must not panic; it should
// silently fail to build.
func TestDream_BuildFollowUpItemMissingFile(t *testing.T) {
	fu := fractal.FollowUp{
		Region:       fractal.Region{Path: "/nonexistent/path/abc", Offset: 0, Length: 100},
		ParentItemID: "p",
		Depth:        1,
		Source:       "project",
	}
	if _, ok := buildFollowUpItem(fu); ok {
		t.Errorf("missing file should not produce a DreamItem")
	}
}
