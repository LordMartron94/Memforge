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

func TestMemforgeDomainGroupBuckets(t *testing.T) {
	buckets := []MemforgeArenaProfileBucket{
		{Tag: "Renderer Core", DisplayLabel: "core"},
		{Tag: "API Scratch", DisplayLabel: "scratch"},
		{Tag: "Gameplay", DisplayLabel: "game"},
		{Tag: "", DisplayLabel: "untagged", SiteLine: "main.main"},
	}
	domainGroups := map[string][]string{
		"Renderer": {"Renderer Core"},
		"Vulkan":   {"API Scratch"},
		"Tutorial": {"Gameplay"},
	}

	buckets[0].InstanceCount = 3
	buckets[0].ConfiguredCapBytes = 3000
	buckets[0].MaxPeakBytes = 900

	grouped, ungrouped := MemforgeDomainGroupBuckets(buckets, domainGroups)
	if len(grouped["Renderer"]) != 1 || grouped["Renderer"][0].Tag != "Renderer Core" {
		t.Fatalf("unexpected renderer buckets: %+v", grouped["Renderer"])
	}
	if len(grouped["Vulkan"]) != 1 || grouped["Vulkan"][0].Tag != "API Scratch" {
		t.Fatalf("unexpected vulkan buckets: %+v", grouped["Vulkan"])
	}
	if len(grouped["Tutorial"]) != 1 {
		t.Fatalf("unexpected tutorial buckets: %+v", grouped["Tutorial"])
	}
	if len(ungrouped) != 1 || ungrouped[0].DisplayLabel != "untagged" {
		t.Fatalf("expected one ungrouped bucket, got %+v", ungrouped)
	}

	summary := MemforgeDomainProfileSummarize(grouped["Renderer"])
	if summary.BucketCount != 1 || summary.InstanceCount != 3 || summary.ConfiguredCapBytes != 3000 {
		t.Fatalf("unexpected renderer summary: %+v", summary)
	}
	if summary.MaxPeakBytes != 900 {
		t.Fatalf("expected max peak 900, got %d", summary.MaxPeakBytes)
	}
}

func TestMemforgeDomainProfileSummarize(t *testing.T) {
	buckets := []MemforgeArenaProfileBucket{
		{
			InstanceCount:      2,
			ActiveCount:        1,
			DestroyedCount:     1,
			ConfiguredCapBytes: 1024,
			MaxPeakBytes:       512,
			TotalEverBytes:     400,
		},
		{
			OpaqueBacking:      true,
			InstanceCount:      1,
			ActiveCount:        1,
			ConfiguredCapBytes: 2048,
			MaxPeakBytes:       1024,
			TotalEverBytes:     800,
		},
	}
	summary := MemforgeDomainProfileSummarize(buckets)
	if summary.BucketCount != 2 || summary.InstanceCount != 3 || summary.ActiveCount != 2 {
		t.Fatalf("unexpected counts: %+v", summary)
	}
	if summary.ConfiguredCapBytes != 3072 || summary.MappableCapBytes != 1024 || summary.OpaqueCapBytes != 2048 {
		t.Fatalf("unexpected cap split: %+v", summary)
	}
	if summary.MaxPeakBytes != 1024 || summary.TotalEverBytes != 1200 {
		t.Fatalf("unexpected peaks or ever bytes: %+v", summary)
	}
}
