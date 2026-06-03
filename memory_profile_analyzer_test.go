package memforge

import (
	"testing"
	"time"
)

func TestMemforgeMemoryProfileAnalyzeBuckets(t *testing.T) {
	now := time.Now()
	filter := MemforgeDebuggerDefaultStackFilter()
	snapshot := MemforgeMemoryTimelineSnapshot{
		Available:  true,
		CapturedAt: now.Add(2 * time.Second),
		Events: []MemforgeTimelineEvent{
			{
				Seq:               1,
				Timestamp:         now,
				Kind:              MemforgeTimelineEventAllocatorRegister,
				AllocatorAddress:  0x1000,
				AllocatorName:     "Fixed Linear (Mmap)",
				Tag:               "Renderer Core",
				ArenaDataCapBytes: 1024 * 1024,
			},
			{
				Seq:               2,
				Timestamp:         now.Add(time.Millisecond),
				Kind:              MemforgeTimelineEventAllocatorRegister,
				AllocatorAddress:  0x2000,
				AllocatorName:     "Fixed Linear (Mmap)",
				Tag:               "Renderer Core",
				ArenaDataCapBytes: 1024 * 1024,
			},
			{
				Seq:              3,
				Timestamp:        now.Add(2 * time.Millisecond),
				Kind:             MemforgeTimelineEventAllocation,
				AllocatorAddress: 0x1000,
				AllocatorName:    "Fixed Linear (Mmap)",
				SizeBytes:        512,
			},
			{
				Seq:              4,
				Timestamp:        now.Add(500 * time.Millisecond),
				Kind:             MemforgeTimelineEventAllocatorDestroy,
				AllocatorAddress: 0x1000,
				AllocatorName:    "Fixed Linear (Mmap)",
			},
			{
				Seq:              5,
				Timestamp:        now.Add(600 * time.Millisecond),
				Kind:             MemforgeTimelineEventAllocatorDestroy,
				AllocatorAddress: 0x2000,
				AllocatorName:    "Fixed Linear (Mmap)",
			},
		},
	}

	analysis := MemforgeMemoryTimelineAnalyze(snapshot, filter)
	report := MemforgeMemoryProfileAnalyze(analysis, filter, MemforgeSizingVerdictFilter{})

	if len(report.Buckets) != 1 {
		t.Fatalf("expected one bucket, got %d: %+v", len(report.Buckets), report.Buckets)
	}
	bucket := report.Buckets[0]
	if bucket.InstanceCount != 2 || bucket.DestroyedCount != 2 || bucket.ActiveCount != 0 {
		t.Fatalf("unexpected instance counts: %+v", bucket)
	}
	if bucket.Tag != "Renderer Core" {
		t.Fatalf("unexpected tag: %q", bucket.Tag)
	}
	if bucket.MaxConcurrentActive != 2 {
		t.Fatalf("expected concurrent high-water 2, got %d", bucket.MaxConcurrentActive)
	}
}

func TestMemforgeMemoryProfileAnalyzeSizingVerdicts(t *testing.T) {
	now := time.Now()
	filter := MemforgeStackFilter{}
	snapshot := MemforgeMemoryTimelineSnapshot{
		Available:  true,
		CapturedAt: now.Add(time.Second),
		Events: []MemforgeTimelineEvent{
			{
				Seq:               1,
				Timestamp:         now,
				Kind:              MemforgeTimelineEventAllocatorRegister,
				AllocatorAddress:  0x1000,
				AllocatorName:     "Chained Linear",
				Tag:               "Host Staging Pool",
				ArenaDataCapBytes: 64 * 1024 * 1024,
			},
			{
				Seq:              2,
				Timestamp:        now,
				Kind:             MemforgeTimelineEventAllocation,
				AllocatorAddress: 0x1000,
				SizeBytes:        80,
			},
		},
	}

	analysis := MemforgeMemoryTimelineAnalyze(snapshot, filter)
	report := MemforgeMemoryProfileAnalyze(analysis, filter, MemforgeSizingVerdictFilter{})

	if len(report.SizingVerdicts) == 0 {
		t.Fatalf("expected underutilized verdict")
	}
	if report.SizingVerdicts[0].Kind != MemforgeSizingVerdictUnderutilized {
		t.Fatalf("unexpected verdict kind: %s", report.SizingVerdicts[0].Kind)
	}
}

func TestMemforgeSizingVerdictFilterTagPolicies(t *testing.T) {
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
				AllocatorName:     "Fixed Linear (Mmap)",
				Tag:               "Allocator Metadata",
				ArenaDataCapBytes: 584,
			},
			{
				Seq:              2,
				Timestamp:        now,
				Kind:             MemforgeTimelineEventAllocation,
				AllocatorAddress: 0x1000,
				SizeBytes:        584,
			},
		},
	}

	analysis := MemforgeMemoryTimelineAnalyze(snapshot, MemforgeStackFilter{})
	report := MemforgeMemoryProfileAnalyze(analysis, MemforgeStackFilter{}, MemforgeSizingVerdictFilter{
		TagPolicies: map[string]MemforgeAllocatorSizingPolicy{
			"Allocator Metadata": MemforgeSizingPolicyExactFit,
		},
	})
	for _, verdict := range report.SizingVerdicts {
		if verdict.Kind == MemforgeSizingVerdictOverutilized {
			t.Fatalf("expected exact-fit tag policy to suppress over-capacity verdict: %+v", verdict)
		}
	}

	report = MemforgeMemoryProfileAnalyze(analysis, MemforgeStackFilter{}, MemforgeSizingVerdictFilter{
		SuppressExactFitOverutilized: boolPtr(false),
	})
	if len(report.SizingVerdicts) == 0 {
		t.Fatalf("expected over-capacity verdict without tag policy")
	}
}

func boolPtr(v bool) *bool {
	return &v
}
