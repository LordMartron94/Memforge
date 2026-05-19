package memforge

import (
	"slices"
	"strings"
	"time"
)

/*
MemforgeStackFilter configures client-supplied stack-frame filtering for timeline analysis.

[Context]
Memforge does not assume which frames are framework boilerplate. The client supplies IgnorePrefixes and IgnoreContains. BoundaryMode keeps only the first surviving frame after filtering.
*/
type MemforgeStackFilter struct {
	IgnorePrefixes []string
	IgnoreContains []string
	MaxDepth       int
	BoundaryMode   bool
}

/*
MemforgeArenaSummary is allocator-level telemetry derived from a timeline replay.
*/
type MemforgeArenaSummary struct {
	Name                 string
	Address              uintptr
	Destroyed            bool
	CreatedAt            time.Time
	DestroyedAt          time.Time
	LastAllocationAt     time.Time
	AgeAtCapture         time.Duration
	EverAllocations      int
	LiveAllocations      int
	PeakLiveAllocations  int
	EverBytes            uint64
	LiveBytes            uint64
	PeakLiveBytes        uint64
	FilteredCreatorStack []string
	Leaking              bool
}

/*
MemforgeAllocationStackGroup aggregates currently-live allocations by filtered stack signature.
*/
type MemforgeAllocationStackGroup struct {
	FilteredStack   []string
	AllocationCount int
	TotalBytes      uint64
	OldestAt        time.Time
	NewestAt        time.Time
	SampleAllocator string
}

/*
MemforgeMemoryTimelineAnalysis is the focus-driven view of a timeline snapshot.
*/
type MemforgeMemoryTimelineAnalysis struct {
	Available            bool
	CapturedAt           time.Time
	SourceKind           string
	SourcePath           string
	Arenas               []MemforgeArenaSummary
	LeakGroups           []MemforgeAllocationStackGroup
	Events               []MemforgeTimelineEvent
	TotalEvents          int
	TotalAllocators      int
	ActiveAllocators     int
	DestroyedAllocators  int
	LeakingAllocators    int
	TotalLiveAllocations int
	TotalLiveBytes       uint64
	LeakDetected         bool
}

type arenaReplayState struct {
	summary MemforgeArenaSummary
}

type liveAllocationState struct {
	seq              uint64
	allocatorAddress uintptr
	allocatorName    string
	sizeBytes        uint64
	filteredStack    []string
	createdAt        time.Time
}

/*
MemforgeStackFilterApply returns stack frames after applying filter rules to a raw " → "-joined stack.
*/
func MemforgeStackFilterApply(rawStack string, filter MemforgeStackFilter) []string {
	trimmed := strings.TrimSpace(rawStack)
	if trimmed == "" {
		return nil
	}

	parts := strings.Split(trimmed, " → ")
	frames := make([]string, 0, len(parts))
	for _, part := range parts {
		frame := strings.TrimSpace(part)
		if frame == "" || memforgeStackFrameIgnored(frame, filter) {
			continue
		}
		frames = append(frames, frame)
		if filter.BoundaryMode {
			return frames
		}
	}

	if filter.MaxDepth > 0 && len(frames) > filter.MaxDepth {
		frames = frames[:filter.MaxDepth]
	}
	return frames
}

func memforgeStackFrameIgnored(frame string, filter MemforgeStackFilter) bool {
	for _, prefix := range filter.IgnorePrefixes {
		if prefix != "" && strings.HasPrefix(frame, prefix) {
			return true
		}
	}
	for _, contains := range filter.IgnoreContains {
		if contains != "" && strings.Contains(frame, contains) {
			return true
		}
	}
	return false
}

func stackSignature(frames []string) string {
	if len(frames) == 0 {
		return "<unknown>"
	}
	return strings.Join(frames, " → ")
}

/*
MemforgeMemoryTimelineAnalyze replays timeline events into arena summaries and leak groups.

[Side Effects]
Pure function. Does not mutate the input snapshot.
*/
func MemforgeMemoryTimelineAnalyze(snapshot MemforgeMemoryTimelineSnapshot, filter MemforgeStackFilter) MemforgeMemoryTimelineAnalysis {
	analysis := MemforgeMemoryTimelineAnalysis{
		Available:   snapshot.Available,
		CapturedAt:  snapshot.CapturedAt,
		SourceKind:  "memory",
		TotalEvents: len(snapshot.Events),
		Events:      slices.Clone(snapshot.Events),
	}
	if !snapshot.Available {
		return analysis
	}
	if analysis.CapturedAt.IsZero() {
		analysis.CapturedAt = time.Now()
	}

	arenas := make(map[uintptr]*arenaReplayState)
	live := make(map[uint64]liveAllocationState)

	for _, evt := range snapshot.Events {
		switch evt.Kind {
		case MemforgeTimelineEventAllocatorRegister:
			arena := &arenaReplayState{
				summary: MemforgeArenaSummary{
					Name:                 evt.AllocatorName,
					Address:              evt.AllocatorAddress,
					CreatedAt:            evt.Timestamp,
					FilteredCreatorStack: MemforgeStackFilterApply(evt.Stack, filter),
				},
			}
			arenas[evt.AllocatorAddress] = arena

		case MemforgeTimelineEventAllocation:
			arena := arenaForReplay(arenas, evt.AllocatorAddress, evt.AllocatorName)
			arena.summary.EverAllocations++
			arena.summary.EverBytes += evt.SizeBytes
			arena.summary.LiveAllocations++
			arena.summary.LiveBytes += evt.SizeBytes
			if arena.summary.LiveAllocations > arena.summary.PeakLiveAllocations {
				arena.summary.PeakLiveAllocations = arena.summary.LiveAllocations
			}
			if arena.summary.LiveBytes > arena.summary.PeakLiveBytes {
				arena.summary.PeakLiveBytes = arena.summary.LiveBytes
			}
			if evt.Timestamp.After(arena.summary.LastAllocationAt) {
				arena.summary.LastAllocationAt = evt.Timestamp
			}

			live[evt.Seq] = liveAllocationState{
				seq:              evt.Seq,
				allocatorAddress: evt.AllocatorAddress,
				allocatorName:    evt.AllocatorName,
				sizeBytes:        evt.SizeBytes,
				filteredStack:    MemforgeStackFilterApply(evt.Stack, filter),
				createdAt:        evt.Timestamp,
			}

		case MemforgeTimelineEventFreeManual:
			if entry, ok := live[evt.OriginalSeq]; ok {
				removeLiveAllocation(arenas, live, entry)
			}

		case MemforgeTimelineEventFreeRegionalReset, MemforgeTimelineEventFreeRegionalDestroy:
			for _, freed := range evt.FreedAllocations {
				if entry, ok := live[freed.OriginalSeq]; ok {
					removeLiveAllocation(arenas, live, entry)
				}
			}

		case MemforgeTimelineEventAllocatorDestroy:
			if arena, ok := arenas[evt.AllocatorAddress]; ok {
				arena.summary.Destroyed = true
				arena.summary.DestroyedAt = evt.Timestamp
				arena.summary.LiveAllocations = 0
				arena.summary.LiveBytes = 0
			}
			for seq, entry := range live {
				if entry.allocatorAddress == evt.AllocatorAddress {
					delete(live, seq)
				}
			}
		}
	}

	analysis.Arenas = make([]MemforgeArenaSummary, 0, len(arenas))
	for _, arena := range arenas {
		arena.summary.AgeAtCapture = analysis.CapturedAt.Sub(arena.summary.CreatedAt)
		arena.summary.Leaking = !arena.summary.Destroyed && arena.summary.LiveAllocations > 0
		analysis.Arenas = append(analysis.Arenas, arena.summary)
	}
	slices.SortFunc(analysis.Arenas, compareArenaSummaries)

	leakGroups := make(map[string]*MemforgeAllocationStackGroup)
	for _, entry := range live {
		sig := stackSignature(entry.filteredStack)
		group, ok := leakGroups[sig]
		if !ok {
			group = &MemforgeAllocationStackGroup{
				FilteredStack:   slices.Clone(entry.filteredStack),
				OldestAt:        entry.createdAt,
				NewestAt:        entry.createdAt,
				SampleAllocator: entry.allocatorName,
			}
			leakGroups[sig] = group
		}
		group.AllocationCount++
		group.TotalBytes += entry.sizeBytes
		if entry.createdAt.Before(group.OldestAt) {
			group.OldestAt = entry.createdAt
		}
		if entry.createdAt.After(group.NewestAt) {
			group.NewestAt = entry.createdAt
		}
	}

	analysis.LeakGroups = make([]MemforgeAllocationStackGroup, 0, len(leakGroups))
	for _, group := range leakGroups {
		analysis.LeakGroups = append(analysis.LeakGroups, *group)
	}
	slices.SortFunc(analysis.LeakGroups, func(a, b MemforgeAllocationStackGroup) int {
		switch {
		case a.TotalBytes > b.TotalBytes:
			return -1
		case a.TotalBytes < b.TotalBytes:
			return 1
		default:
			return 0
		}
	})

	analysis.TotalAllocators = len(analysis.Arenas)
	for _, arena := range analysis.Arenas {
		if arena.Destroyed {
			analysis.DestroyedAllocators++
		} else {
			analysis.ActiveAllocators++
		}
		if arena.Leaking {
			analysis.LeakingAllocators++
		}
		analysis.TotalLiveAllocations += arena.LiveAllocations
		analysis.TotalLiveBytes += arena.LiveBytes
	}
	analysis.LeakDetected = analysis.TotalLiveAllocations > 0
	return analysis
}

func arenaForReplay(arenas map[uintptr]*arenaReplayState, address uintptr, name string) *arenaReplayState {
	if arena, ok := arenas[address]; ok {
		return arena
	}
	arena := &arenaReplayState{
		summary: MemforgeArenaSummary{
			Name:    name,
			Address: address,
		},
	}
	arenas[address] = arena
	return arena
}

func removeLiveAllocation(arenas map[uintptr]*arenaReplayState, live map[uint64]liveAllocationState, entry liveAllocationState) {
	delete(live, entry.seq)
	if arena, ok := arenas[entry.allocatorAddress]; ok {
		arena.summary.LiveAllocations--
		if entry.sizeBytes <= arena.summary.LiveBytes {
			arena.summary.LiveBytes -= entry.sizeBytes
		} else {
			arena.summary.LiveBytes = 0
		}
	}
}

func compareArenaSummaries(a, b MemforgeArenaSummary) int {
	if a.Leaking != b.Leaking {
		if a.Leaking {
			return -1
		}
		return 1
	}
	switch {
	case a.LiveBytes > b.LiveBytes:
		return -1
	case a.LiveBytes < b.LiveBytes:
		return 1
	case a.Name < b.Name:
		return -1
	case a.Name > b.Name:
		return 1
	default:
		return 0
	}
}
