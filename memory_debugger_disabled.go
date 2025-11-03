//go:build !memforge_debug

package memforge

import "memcore"

// MemforgeMemoryDebug is a no-op when memforge_debug is disabled.
func MemforgeMemoryDebug() {}

func memforgeAllocatorRegister(allocatorPtr memcore.Pointer, name string) {}

func memforgeAllocationAdd(allocatorPtr, allocationPtr memcore.Pointer, sizeBytes uint64) {}

func memforgeAllocationRemove(allocatorPtr, allocationPtr memcore.Pointer) {}

func memforgeAllocatorRemoveAll(allocatorPtr memcore.Pointer) {}

func memforgeAllocatorDestroy(allocatorPtr memcore.Pointer) {}
