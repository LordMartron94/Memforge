package memforge

import (
	"fmt"
	"memcore"
	"memstruct"
)

// ChainedLinearAllocatorMallocFast allocates using a pre-dereferenced header; allocatorMark is for tracking.
//
//go:nosplit
func ChainedLinearAllocatorMallocFast(allocatorMark memcore.MarkRaw, header *ChainedLinearAllocator, sizeBytes, alignment uint64) memcore.MarkRaw {
	alignmentValidate(alignment)

	alignedIdx := alignIdxUp(header.bumpIndex, alignment)
	if !capacityGuarantee(alignedIdx, header.activeCapacity, sizeBytes) {
		chainedLinearAllocatorPivot(header, sizeBytes)
		alignedIdx = alignIdxUp(header.bumpIndex, alignment)
		if !capacityGuarantee(alignedIdx, header.activeCapacity, sizeBytes) {
			panic(fmt.Errorf("chained linear allocator: growth hook returned insufficient capacity for size %d", sizeBytes))
		}
	}

	ptr := memcore.MemcoreMarkCreate(header.activeRegionID, uintptr(alignedIdx))
	memforgeAllocationAdd(allocatorMark, ptr, sizeBytes)
	header.bumpIndex = alignedIdx + sizeBytes
	return ptr
}

// ChainedLinearAllocatorResetFast rewinds the chain using a pre-dereferenced header.
func ChainedLinearAllocatorResetFast(allocatorMark memcore.MarkRaw, header *ChainedLinearAllocator) {
	memforgeAllocatorRemoveAll(allocatorMark)

	if memstruct.FixedOrderedListLengthGetFast(header.regionHistory) == 0 {
		header.bumpIndex = 0
		return
	}

	firstRegion, err := memstruct.FixedOrderedListItemGetAtFast(header.regionHistory, 0)
	if err != nil {
		panic(fmt.Errorf("chained linear allocator: invalid region history: %w", err))
	}

	header.activeRegionID = firstRegion
	header.bumpIndex = 0

	capacity, err := chainedLinearAllocatorRegionCapacityGet(firstRegion)
	if err != nil {
		panic(err)
	}
	header.activeCapacity = capacity
}
