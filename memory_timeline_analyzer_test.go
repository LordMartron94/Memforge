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
				Seq:               1,
				Timestamp:         now,
				Kind:              MemforgeTimelineEventAllocatorRegister,
				AllocatorAddress:  addr,
				AllocatorName:     "arena-a",
				Stack:             "runner.Run → app.CreateArena",
				ArenaDataCapBytes: 512,
				ArenaTotalBytes:   640,
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
	if analysis.Arenas[0].CurrentArenaDataCapBytes != 512 || analysis.Arenas[0].CurrentArenaTotalBytes != 640 {
		t.Fatalf("unexpected arena capacity: %+v", analysis.Arenas[0])
	}
	if analysis.TotalPeakLiveAllocations != 1 || analysis.TotalPeakLiveBytes != 64 {
		t.Fatalf("unexpected peak totals: allocations=%d bytes=%d", analysis.TotalPeakLiveAllocations, analysis.TotalPeakLiveBytes)
	}
	if analysis.TotalEverAllocations != 1 || analysis.TotalEverBytes != 64 {
		t.Fatalf("unexpected ever totals: allocations=%d bytes=%d", analysis.TotalEverAllocations, analysis.TotalEverBytes)
	}
}

func TestMemforgeMemoryTimelineAnalyzeArenaGrowSegments(t *testing.T) {
	now := time.Now()
	addr := uintptr(0x1000)
	snapshot := MemforgeMemoryTimelineSnapshot{
		Available:  true,
		CapturedAt: now.Add(3 * time.Second),
		Events: []MemforgeTimelineEvent{
			{
				Seq:               1,
				Timestamp:         now,
				Kind:              MemforgeTimelineEventAllocatorRegister,
				AllocatorAddress:  addr,
				AllocatorName:     "dynamic",
				ArenaDataCapBytes: 1024,
				ArenaTotalBytes:   1152,
			},
			{
				Seq:                       2,
				Timestamp:                 now.Add(time.Second),
				Kind:                      MemforgeTimelineEventAllocatorGrow,
				AllocatorAddress:          addr,
				AllocatorName:             "dynamic",
				PreviousArenaDataCapBytes: 1024,
				ArenaDataCapBytes:         4096,
				ArenaTotalBytes:           4224,
			},
		},
	}

	analysis := MemforgeMemoryTimelineAnalyze(snapshot, MemforgeStackFilter{})
	if len(analysis.Arenas) != 1 {
		t.Fatalf("expected one arena, got %+v", analysis.Arenas)
	}
	arena := analysis.Arenas[0]
	if arena.CurrentArenaDataCapBytes != 4096 || arena.PeakArenaDataCapBytes != 4096 {
		t.Fatalf("unexpected current/peak cap: %+v", arena)
	}
	if len(arena.CapacitySegments) != 2 {
		t.Fatalf("expected two capacity segments, got %+v", arena.CapacitySegments)
	}
	if arena.CapacitySegments[0].DataCapBytes != 1024 || !arena.CapacitySegments[0].EndedAt.Equal(now.Add(time.Second)) {
		t.Fatalf("unexpected first segment: %+v", arena.CapacitySegments[0])
	}
	if arena.CapacitySegments[1].DataCapBytes != 4096 || arena.CapacitySegments[1].GrownFromBytes != 1024 {
		t.Fatalf("unexpected second segment: %+v", arena.CapacitySegments[1])
	}
}

func TestMemforgeMemoryTimelineAnalyzeAggregatePeakEverTotals(t *testing.T) {
	now := time.Now()
	snapshot := MemforgeMemoryTimelineSnapshot{
		Available:  true,
		CapturedAt: now.Add(2 * time.Second),
		Events: []MemforgeTimelineEvent{
			{
				Seq:              1,
				Timestamp:        now,
				Kind:             MemforgeTimelineEventAllocatorRegister,
				AllocatorAddress: 0x1000,
				AllocatorName:    "arena-a",
			},
			{
				Seq:              2,
				Timestamp:        now,
				Kind:             MemforgeTimelineEventAllocatorRegister,
				AllocatorAddress: 0x2000,
				AllocatorName:    "arena-b",
			},
			{
				Seq:               3,
				Timestamp:         now.Add(time.Millisecond),
				Kind:              MemforgeTimelineEventAllocation,
				AllocatorAddress:  0x1000,
				AllocatorName:     "arena-a",
				AllocationAddress: 0x3000,
				SizeBytes:         64,
			},
			{
				Seq:               4,
				Timestamp:         now.Add(2 * time.Millisecond),
				Kind:              MemforgeTimelineEventAllocation,
				AllocatorAddress:  0x2000,
				AllocatorName:     "arena-b",
				AllocationAddress: 0x4000,
				SizeBytes:         128,
			},
			{
				Seq:               5,
				Timestamp:         now.Add(3 * time.Millisecond),
				Kind:              MemforgeTimelineEventAllocation,
				AllocatorAddress:  0x1000,
				AllocatorName:     "arena-a",
				AllocationAddress: 0x5000,
				SizeBytes:         32,
			},
		},
	}

	analysis := MemforgeMemoryTimelineAnalyze(snapshot, MemforgeStackFilter{})
	if analysis.TotalPeakLiveAllocations != 3 || analysis.TotalPeakLiveBytes != 224 {
		t.Fatalf("unexpected peak totals: allocations=%d bytes=%d", analysis.TotalPeakLiveAllocations, analysis.TotalPeakLiveBytes)
	}
	if analysis.TotalEverAllocations != 3 || analysis.TotalEverBytes != 224 {
		t.Fatalf("unexpected ever totals: allocations=%d bytes=%d", analysis.TotalEverAllocations, analysis.TotalEverBytes)
	}
}

func TestMemforgeMemoryTimelineAnalyzeDeterministicArenaOrder(t *testing.T) {
	now := time.Now()
	snapshot := MemforgeMemoryTimelineSnapshot{
		Available:  true,
		CapturedAt: now.Add(2 * time.Second),
		Events: []MemforgeTimelineEvent{
			{
				Seq:               1,
				Timestamp:         now,
				Kind:              MemforgeTimelineEventAllocatorRegister,
				AllocatorAddress:  0x3000,
				AllocatorName:     "Fixed Linear (Manual)",
				Stack:             "app.Main → rendering.WindowManagerCreate",
				ArenaDataCapBytes: 1024,
				ArenaTotalBytes:   1152,
			},
			{
				Seq:               2,
				Timestamp:         now.Add(2 * time.Millisecond),
				Kind:              MemforgeTimelineEventAllocatorRegister,
				AllocatorAddress:  0x1000,
				AllocatorName:     "Fixed Linear (Manual)",
				Stack:             "app.Main → rendering.GraphicalRendererCreate",
				ArenaDataCapBytes: 1024,
				ArenaTotalBytes:   1152,
			},
			{
				Seq:               3,
				Timestamp:         now.Add(3 * time.Millisecond),
				Kind:              MemforgeTimelineEventAllocatorRegister,
				AllocatorAddress:  0x2000,
				AllocatorName:     "Fixed Linear (Manual)",
				Stack:             "app.Main → rendering.GraphicalRendererCreate",
				ArenaDataCapBytes: 1024,
				ArenaTotalBytes:   1152,
			},
		},
	}

	analysis := MemforgeMemoryTimelineAnalyze(snapshot, MemforgeStackFilter{})
	if len(analysis.Arenas) != 3 {
		t.Fatalf("expected 3 arenas, got %d", len(analysis.Arenas))
	}

	if analysis.Arenas[0].Address != 0x3000 {
		t.Fatalf("unexpected arena[0] order: %#x", analysis.Arenas[0].Address)
	}
	if analysis.Arenas[1].Address != 0x1000 {
		t.Fatalf("unexpected arena[1] order: %#x", analysis.Arenas[1].Address)
	}
	if analysis.Arenas[2].Address != 0x2000 {
		t.Fatalf("unexpected arena[2] order: %#x", analysis.Arenas[2].Address)
	}
}

func TestMemforgeMemoryTimelineAnalyzeUtilizationMetrics(t *testing.T) {
	now := time.Now()
	snapshot := MemforgeMemoryTimelineSnapshot{
		Available:  true,
		CapturedAt: now.Add(time.Second),
		Events: []MemforgeTimelineEvent{
			{
				Seq:               1,
				Timestamp:         now,
				Kind:              MemforgeTimelineEventAllocatorRegister,
				AllocatorAddress:  0x1000,
				AllocatorName:     "arena-a",
				ArenaDataCapBytes: 1000,
				ArenaTotalBytes:   1100,
			},
			{
				Seq:               2,
				Timestamp:         now.Add(time.Millisecond),
				Kind:              MemforgeTimelineEventAllocation,
				AllocatorAddress:  0x1000,
				AllocatorName:     "arena-a",
				AllocationAddress: 0x2000,
				SizeBytes:         250,
			},
		},
	}

	analysis := MemforgeMemoryTimelineAnalyze(snapshot, MemforgeStackFilter{})
	if analysis.LiveUtilizationPercent != 25 || analysis.EverUtilizationPercent != 25 {
		t.Fatalf("unexpected global utilization: live=%.1f ever=%.1f", analysis.LiveUtilizationPercent, analysis.EverUtilizationPercent)
	}
	if len(analysis.Arenas) != 1 {
		t.Fatalf("expected one arena, got %d", len(analysis.Arenas))
	}
	arena := analysis.Arenas[0]
	if arena.LiveUtilizationPercent != 25 || arena.EverUtilizationPercent != 25 || arena.PeakUtilizationPercent != 25 {
		t.Fatalf("unexpected arena utilization: live=%.1f peak=%.1f ever=%.1f",
			arena.LiveUtilizationPercent, arena.PeakUtilizationPercent, arena.EverUtilizationPercent)
	}
	if arena.LastAllocationAt.IsZero() {
		t.Fatalf("expected last allocation timestamp")
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
