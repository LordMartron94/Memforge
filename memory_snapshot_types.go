package memforge

import "time"

/*
MemforgeAllocationSnapshot records one live allocation tracked by the memforge debugger.
*/
type MemforgeAllocationSnapshot struct {
	Address   uintptr
	SizeBytes uint64
	CreatedAt time.Time
	Creator   string
}

/*
MemforgeAllocatorSnapshot records allocator-level telemetry captured at snapshot time.
*/
type MemforgeAllocatorSnapshot struct {
	Name                  string
	Address               uintptr
	Destroyed             bool
	CreatedAt             time.Time
	Creator               string
	TotalAllocations      int
	LiveAllocations       int
	TotalBytes            uint64
	LiveBytes             uint64
	PeakLiveBytes         uint64
	PeakLiveAllocations   int
	LastAllocationAt      time.Time
	Status                string
	LiveAllocationDetails []MemforgeAllocationSnapshot
}

/*
MemforgeMemorySnapshot is a structured post-run view of memforge allocator state.

Available is false when the binary was not built with the memforge_debug tag.
*/
type MemforgeMemorySnapshot struct {
	Available                bool
	CapturedAt               time.Time
	AllocatorTypeCount       int
	AllocatorCount           int
	ActiveAllocatorCount     int
	DestroyedAllocatorCount  int
	AllocatorsWithLiveAllocs int
	TotalAllocationCount     int
	LiveAllocationCount      int
	TotalBytes               uint64
	LiveBytes                uint64
	LeakDetected             bool
	Allocators               []MemforgeAllocatorSnapshot
}
