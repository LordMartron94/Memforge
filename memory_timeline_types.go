package memforge

import "time"

/*
MemforgeTimelineEventKind identifies one recorded allocator lifecycle event.
*/
type MemforgeTimelineEventKind string

const (
	MemforgeTimelineEventAllocatorRegister   MemforgeTimelineEventKind = "allocator_register"
	MemforgeTimelineEventAllocatorDestroy    MemforgeTimelineEventKind = "allocator_destroy"
	MemforgeTimelineEventAllocation          MemforgeTimelineEventKind = "allocation"
	MemforgeTimelineEventFreeManual          MemforgeTimelineEventKind = "free_manual"
	MemforgeTimelineEventFreeRegionalReset   MemforgeTimelineEventKind = "free_regional_reset"
	MemforgeTimelineEventFreeRegionalDestroy MemforgeTimelineEventKind = "free_regional_destroy"
)

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
	Seq               uint64
	Timestamp         time.Time
	Kind              MemforgeTimelineEventKind
	AllocatorAddress  uintptr
	AllocatorName     string
	Stack             string
	AllocationAddress uintptr
	SizeBytes         uint64
	OriginalSeq       uint64
	OriginalCreatedAt time.Time
	FreedAllocations  []MemforgeTimelineFreedAllocation
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
