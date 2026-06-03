package memforge

import (
	"memcore"
)

// DynamicLinearAllocatorMallocFast allocates using a pre-dereferenced header.
//
//go:nosplit
func DynamicLinearAllocatorMallocFast(allocatorMark memcore.MarkRaw, header *DynamicLinearAllocator, sizeBytes, alignment uint64) memcore.MarkRaw {
	alignmentValidate(alignment)

	state := &header.linearAllocatorState
	alignedIdx := linearAllocatorDataIdxGet(state, alignment)
	if !linearAllocatorCapacityGuarantee(state, sizeBytes, alignedIdx) {
		dynamicLinearAllocatorGrow(allocatorMark, header, alignedIdx+sizeBytes)
		alignedIdx = linearAllocatorDataIdxGet(state, alignment)
	}

	ptr := linearAllocatorMallocMark(state, alignedIdx)

	memforgeAllocationAdd(allocatorMark, ptr, sizeBytes)
	linearAllocatorIdxUpdate(state, alignedIdx, sizeBytes)
	return ptr
}

// DynamicLinearAllocatorMallocUnsafeFast allocates without alignment validation.
//
//go:nosplit
func DynamicLinearAllocatorMallocUnsafeFast(allocatorMark memcore.MarkRaw, header *DynamicLinearAllocator, sizeBytes, alignment uint64) memcore.MarkRaw {
	state := &header.linearAllocatorState
	alignedIdx := linearAllocatorDataIdxGet(state, alignment)
	if !linearAllocatorCapacityGuarantee(state, sizeBytes, alignedIdx) {
		dynamicLinearAllocatorGrow(allocatorMark, header, alignedIdx+sizeBytes)
		alignedIdx = linearAllocatorDataIdxGet(state, alignment)
	}

	ptr := linearAllocatorMallocMark(state, alignedIdx)

	memforgeAllocationAdd(allocatorMark, ptr, sizeBytes)
	linearAllocatorIdxUpdate(state, alignedIdx, sizeBytes)
	return ptr
}

// DynamicLinearAllocatorResetFast resets bump state using a pre-dereferenced header.
func DynamicLinearAllocatorResetFast(allocatorMark memcore.MarkRaw, header *DynamicLinearAllocator) {
	memforgeAllocatorRemoveAll(allocatorMark)
	linearAllocatorResetState(&header.linearAllocatorState)
}
