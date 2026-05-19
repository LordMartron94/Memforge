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

func memforgeAllocatorRegister(allocatorPtr memcore.MarkRaw, name string) {}

func memforgeAllocationAdd(allocatorPtr, allocationPtr memcore.MarkRaw, sizeBytes uint64) {}

func memforgeAllocationRemove(allocatorPtr, allocationPtr memcore.MarkRaw) {}

func memforgeAllocatorRemoveAll(allocatorPtr memcore.MarkRaw) {}

func memforgeAllocatorDestroy(allocatorPtr memcore.MarkRaw) {}
