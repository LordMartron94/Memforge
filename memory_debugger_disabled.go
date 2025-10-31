//go:build !memforge_debug

package memforge

import "unsafe"

// MemforgeMemoryDebug prints the current state of allocations.
func MemforgeMemoryDebug() {}

func memforgeAllocatorRegister(allocatorPtr unsafe.Pointer, name string) {}

func memforgeAllocationAdd(allocatorPtr, allocationPtr unsafe.Pointer, sizeBytes uint64) {}

func memforgeAllocationRemove(allocatorPtr, allocationPtr unsafe.Pointer) {}

func memforgeAllocatorRemoveAll(allocatorPtr unsafe.Pointer) {}
