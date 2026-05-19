package memforge

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestMemforgeStackFilterApply(t *testing.T) {
	filter := MemforgeStackFilter{
		IgnorePrefixes: []string{"runner."},
		MaxDepth:       2,
	}
	frames := MemforgeStackFilterApply("runner.Run → app.Allocate → app.Init", filter)
	if len(frames) != 2 || frames[0] != "app.Allocate" || frames[1] != "app.Init" {
		t.Fatalf("unexpected frames: %#v", frames)
	}

	boundary := MemforgeStackFilter{
		IgnorePrefixes: []string{"runner."},
		BoundaryMode:   true,
	}
	frames = MemforgeStackFilterApply("runner.Run → app.Allocate → app.Init", boundary)
	if len(frames) != 1 || frames[0] != "app.Allocate" {
		t.Fatalf("unexpected boundary frames: %#v", frames)
	}
}

func TestMemforgeMemoryTimelineAnalyzeReplay(t *testing.T) {
	now := time.Now()
	addr := uintptr(0x1000)
	snapshot := MemforgeMemoryTimelineSnapshot{
		Available:  true,
		CapturedAt: now.Add(2 * time.Second),
		Events: []MemforgeTimelineEvent{
			{
				Seq:              1,
				Timestamp:        now,
				Kind:             MemforgeTimelineEventAllocatorRegister,
				AllocatorAddress: addr,
				AllocatorName:    "arena-a",
				Stack:            "runner.Run → app.CreateArena",
			},
			{
				Seq:               2,
				Timestamp:         now.Add(time.Millisecond),
				Kind:              MemforgeTimelineEventAllocation,
				AllocatorAddress:  addr,
				AllocatorName:     "arena-a",
				Stack:             "runner.Run → app.Allocate",
				AllocationAddress: 0x2000,
				SizeBytes:         64,
			},
		},
	}

	analysis := MemforgeMemoryTimelineAnalyze(snapshot, MemforgeStackFilter{IgnorePrefixes: []string{"runner."}})
	if !analysis.LeakDetected || analysis.TotalLiveAllocations != 1 {
		t.Fatalf("expected live leak, got %+v", analysis)
	}
	if len(analysis.Arenas) != 1 || !analysis.Arenas[0].Leaking {
		t.Fatalf("expected leaking arena, got %+v", analysis.Arenas)
	}
	if len(analysis.LeakGroups) != 1 || analysis.LeakGroups[0].TotalBytes != 64 {
		t.Fatalf("expected leak group, got %+v", analysis.LeakGroups)
	}
	if len(analysis.Arenas[0].FilteredCreatorStack) != 1 || analysis.Arenas[0].FilteredCreatorStack[0] != "app.CreateArena" {
		t.Fatalf("unexpected filtered creator stack: %#v", analysis.Arenas[0].FilteredCreatorStack)
	}
}

func TestMemforgeMemoryTimelineJSONLRoundTrip(t *testing.T) {
	now := time.Now()
	snapshot := MemforgeMemoryTimelineSnapshot{
		Available:  true,
		CapturedAt: now,
		EventCount: 1,
		Events: []MemforgeTimelineEvent{
			{
				Seq:              1,
				Timestamp:        now,
				Kind:             MemforgeTimelineEventAllocatorRegister,
				AllocatorAddress: 0x1000,
				AllocatorName:    "arena-a",
				Stack:            "app.CreateArena",
			},
		},
	}

	var buf bytes.Buffer
	if err := MemforgeMemoryTimelineSnapshotWriteJSONL(&buf, snapshot); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if !strings.Contains(buf.String(), "\"type\":\"summary\"") {
		t.Fatalf("expected summary record, got %q", buf.String())
	}

	loaded, err := MemforgeMemoryTimelineSnapshotReadJSONL(&buf)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if len(loaded.Events) != 1 || loaded.Events[0].AllocatorName != "arena-a" {
		t.Fatalf("unexpected loaded snapshot: %+v", loaded)
	}
}
