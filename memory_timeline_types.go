package memforge

import "time"

/*
MemforgeTimelineEventKind identifies one recorded allocator lifecycle event.
*/
type MemforgeTimelineEventKind string

const (
	MemforgeTimelineEventAllocatorRegister   MemforgeTimelineEventKind = "allocator_register"
	MemforgeTimelineEventAllocatorGrow       MemforgeTimelineEventKind = "allocator_grow"
	MemforgeTimelineEventAllocatorDestroy    MemforgeTimelineEventKind = "allocator_destroy"
	MemforgeTimelineEventAllocation          MemforgeTimelineEventKind = "allocation"
	MemforgeTimelineEventFreeManual          MemforgeTimelineEventKind = "free_manual"
	MemforgeTimelineEventFreeRegionalReset   MemforgeTimelineEventKind = "free_regional_reset"
	MemforgeTimelineEventFreeRegionalDestroy MemforgeTimelineEventKind = "free_regional_destroy"
)

/*
MemforgeArenaCapacitySegment is one contiguous period where an allocator arena had a fixed data capacity.

EndedAt is zero when the segment was still active at analysis capture time.
*/
type MemforgeArenaCapacitySegment struct {
	StartedAt      time.Time
	EndedAt        time.Time
	DataCapBytes   uint64
	TotalBytes     uint64
	GrownFromBytes uint64
}

/*
MemforgeTimelineFreedAllocation records one allocation cleared by a regional reset or destroy sweep.
*/
type MemforgeTimelineFreedAllocation struct {
	AllocationAddress uintptr
	SizeBytes         uint64
	OriginalSeq       uint64
	OriginalCreatedAt time.Time
}

/*
MemforgeTimelineEvent is one ordered entry in the memforge allocation timeline.

[Context]
Regional free events populate FreedAllocations. Allocation and manual-free events populate AllocationAddress, SizeBytes, and (for manual free) OriginalSeq and OriginalCreatedAt.
*/
type MemforgeTimelineEvent struct {
	Seq                       uint64
	Timestamp                 time.Time
	Kind                      MemforgeTimelineEventKind
	AllocatorAddress          uintptr
	AllocatorName             string
	Stack                     string
	AllocationAddress         uintptr
	AllocationRegionID        uint32
	AllocationOffset          uint64
	RootAllocationOffset      uint64
	SizeBytes                 uint64
	OriginalSeq               uint64
	OriginalCreatedAt         time.Time
	FreedAllocations          []MemforgeTimelineFreedAllocation
	ArenaDataCapBytes         uint64
	ArenaTotalBytes           uint64
	PreviousArenaDataCapBytes uint64
	OpaqueBacking             bool
	Tag                       string
}

/*
MemforgeMemoryTimelineSnapshot is a point-in-time copy of all recorded timeline events.

Available is false when the binary was not built with the memforge_debug tag.
*/
type MemforgeMemoryTimelineSnapshot struct {
	Available  bool
	CapturedAt time.Time
	EventCount int
	Events     []MemforgeTimelineEvent
}

/*
MemforgeMemoryTimelineRenderParams configures optional terminal output for MemforgeMemoryTimelineDebug.
*/
type MemforgeMemoryTimelineRenderParams struct {
	MaxEvents           int
	ExpandRegionalFreed bool
}

/*
MemforgeMemoryDebugParams configures MemforgeMemoryDebugWithParams.

Timeline.MaxEvents defaults to 0 (arena summary and leak groups only). Set MaxEvents > 0 to include a granular event listing.
*/
type MemforgeMemoryDebugParams struct {
	StackFilter         MemforgeStackFilter
	SizingVerdictFilter MemforgeSizingVerdictFilter
	Timeline            MemforgeMemoryTimelineRenderParams
}
