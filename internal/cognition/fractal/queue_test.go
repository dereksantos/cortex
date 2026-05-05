package fractal

import (
	"testing"
	"time"
)

func enq(q *FollowUpQueue, parent string, depth int, off int64) {
	q.Enqueue(FollowUp{
		Region:       Region{Path: "f", Offset: off, Length: 10},
		ParentItemID: parent,
		Depth:        depth,
	})
}

func TestFollowUpQueue_FIFOPriorityByDepth(t *testing.T) {
	q := NewFollowUpQueue(0, 0)
	enq(q, "p1", 2, 100)
	time.Sleep(time.Millisecond)
	enq(q, "p2", 1, 200)
	time.Sleep(time.Millisecond)
	enq(q, "p3", 2, 300)
	time.Sleep(time.Millisecond)
	enq(q, "p4", 1, 400)

	got := q.Drain(4)
	if len(got) != 4 {
		t.Fatalf("expected 4, got %d", len(got))
	}
	if got[0].ParentItemID != "p2" || got[1].ParentItemID != "p4" {
		t.Errorf("depth-1 entries should drain first; got %v %v",
			got[0].ParentItemID, got[1].ParentItemID)
	}
	if got[2].Depth != 2 || got[3].Depth != 2 {
		t.Errorf("depth-2 entries should follow")
	}
}

func TestFollowUpQueue_DepthCap(t *testing.T) {
	q := NewFollowUpQueue(0, 0)
	q.Enqueue(FollowUp{Region: Region{Path: "f", Length: 10}, ParentItemID: "x", Depth: MaxFractalDepth + 1})
	if q.Len() != 0 {
		t.Errorf("entries beyond MaxFractalDepth must be dropped")
	}
}

func TestFollowUpQueue_Expiration(t *testing.T) {
	q := NewFollowUpQueue(0, 1*time.Millisecond)
	enq(q, "x", 1, 0)
	time.Sleep(5 * time.Millisecond)
	got := q.Drain(10)
	if len(got) != 0 {
		t.Errorf("expired follow-ups must be discarded")
	}
}

func TestFollowUpQueue_CapacityEviction(t *testing.T) {
	q := NewFollowUpQueue(2, time.Hour)
	enq(q, "a", 1, 0)
	enq(q, "b", 1, 0)
	enq(q, "c", 1, 0)
	if q.Len() != 2 {
		t.Errorf("queue should be capped at capacity")
	}
	got := q.Drain(2)
	if got[0].ParentItemID != "b" {
		t.Errorf("oldest entry should have been evicted")
	}
}

func TestFollowUpQueue_DrainPartial(t *testing.T) {
	q := NewFollowUpQueue(0, time.Hour)
	for i := 0; i < 5; i++ {
		enq(q, "p", 1, int64(i*100))
	}
	got := q.Drain(3)
	if len(got) != 3 {
		t.Errorf("drain(3) should return 3, got %d", len(got))
	}
	if q.Len() != 2 {
		t.Errorf("2 entries should remain, got %d", q.Len())
	}
}
