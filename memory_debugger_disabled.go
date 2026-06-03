//go:build !memforge_debug

package memforge

import "memcore"

/*
MemforgeMemorySnapshotGet returns an unavailable snapshot when memforge_debug is disabled.
*/
func MemforgeMemorySnapshotGet() MemforgeMemorySnapshot {
	return MemforgeMemorySnapshot{
		Available: false,
	}
}

// MemforgeMemoryDebug is a no-op when memforge_debug is disabled.
func MemforgeMemoryDebug() {}

// MemforgeMemoryDebugWithParams is a no-op when memforge_debug is disabled.
func MemforgeMemoryDebugWithParams(_ MemforgeMemoryDebugParams) {}

/*
MemforgeMemoryTimelineSnapshotGet returns an unavailable snapshot when memforge_debug is disabled.
*/
func MemforgeMemoryTimelineSnapshotGet() MemforgeMemoryTimelineSnapshot {
	return MemforgeMemoryTimelineSnapshot{
		Available: false,
	}
}

// MemforgeMemoryTimelineDebug is a no-op when memforge_debug is disabled.
func MemforgeMemoryTimelineDebug(_ MemforgeMemoryTimelineRenderParams) {}

func memforgeAllocatorRegister(
	allocatorPtr memcore.MarkRaw,
	name, tag string,
	dataRegionID uint32,
	arenaDataCapBytes, arenaTotalBytes uint64,
) {
}

func memforgeAllocatorGrow(allocatorPtr memcore.MarkRaw, previousDataCapBytes, newDataCapBytes, newTotalBytes uint64) {
}

func memforgeAllocationAdd(allocatorPtr, allocationPtr memcore.MarkRaw, sizeBytes uint64) {}

func memforgeAllocationRemove(allocatorPtr, allocationPtr memcore.MarkRaw) {}

func memforgeAllocatorRemoveAll(allocatorPtr memcore.MarkRaw) {}

func memforgeAllocatorDestroy(allocatorPtr memcore.MarkRaw) {}
