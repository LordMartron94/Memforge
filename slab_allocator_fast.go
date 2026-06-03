package memforge

import (
	"fmt"
	"memcore"
	"memstruct"
)

// SlabAllocatorMallocFast allocates a slot using a pre-dereferenced slab header.
//
//go:nosplit
func SlabAllocatorMallocFast[T any](allocatorMark memcore.MarkRaw, header *FixedSlabAllocator[T]) memcore.MarkRaw {
	idx, err := memstruct.StackPopFastAuto(header.freeStack)
	if err != nil {
		panic(fmt.Errorf("slab allocator: out of memory: %w", err))
	}

	ptr := slabAllocatorMarkFromSlotIndex(header, idx)
	memforgeAllocationAdd(allocatorMark, ptr, header.slotSize)
	return ptr
}

// SlabAllocatorMallocUnsafeFast allocates without stack bounds checks.
//
//go:nosplit
func SlabAllocatorMallocUnsafeFast[T any](allocatorMark memcore.MarkRaw, header *FixedSlabAllocator[T]) memcore.MarkRaw {
	idx := memstruct.StackPopUnsafeFastAuto(header.freeStack)

	ptr := slabAllocatorMarkFromSlotIndex(header, idx)
	memforgeAllocationAdd(allocatorMark, ptr, header.slotSize)
	return ptr
}

// SlabAllocatorResetFast resets the free stack using a pre-dereferenced header.
func SlabAllocatorResetFast[T any](allocatorMark memcore.MarkRaw, header *FixedSlabAllocator[T]) {
	memforgeAllocatorRemoveAll(allocatorMark)

	memstruct.StackClearFast(header.freeStack)
	for i := header.slotCapacity; i > 0; i-- {
		memstruct.StackPushUnsafeFastAuto(header.freeStack, i-1)
	}
}
