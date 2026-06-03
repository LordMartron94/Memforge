package memforge

import (
	"memcore"
)

/*
FixedLinearAllocatorMallocFast allocates from a pre-dereferenced bump arena header.

The allocatorMark is still required for memforge allocation tracking. Memory must remain
stable between dereferencing the header and using this function.
*/
//go:nosplit
func FixedLinearAllocatorMallocFast(allocatorMark memcore.MarkRaw, header *FixedLinearAllocator, sizeBytes, alignment uint64) memcore.MarkRaw {
	alignmentValidate(alignment)

	alignedIdx := linearAllocatorDataIdxGet(&header.linearAllocatorState, alignment)
	if !linearAllocatorCapacityGuarantee(&header.linearAllocatorState, sizeBytes, alignedIdx) {
		panic(fixedLinearAllocatorOOMError(&header.linearAllocatorState, sizeBytes))
	}

	ptr := linearAllocatorMallocMark(&header.linearAllocatorState, alignedIdx)

	memforgeAllocationAdd(allocatorMark, ptr, sizeBytes)

	linearAllocatorIdxUpdate(&header.linearAllocatorState, alignedIdx, sizeBytes)
	return ptr
}

//go:nosplit
func FixedLinearAllocatorMallocUnsafeFast(allocatorMark memcore.MarkRaw, header *FixedLinearAllocator, sizeBytes, alignment uint64) memcore.MarkRaw {
	alignedIdx := linearAllocatorDataIdxGet(&header.linearAllocatorState, alignment)

	if !linearAllocatorCapacityGuarantee(&header.linearAllocatorState, sizeBytes, alignedIdx) {
		panic(fixedLinearAllocatorOOMError(&header.linearAllocatorState, sizeBytes))
	}

	ptr := linearAllocatorMallocMark(&header.linearAllocatorState, alignedIdx)

	memforgeAllocationAdd(allocatorMark, ptr, sizeBytes)
	linearAllocatorIdxUpdate(&header.linearAllocatorState, alignedIdx, sizeBytes)

	return ptr
}

func FixedLinearAllocatorCallocFast(allocatorMark memcore.MarkRaw, header *FixedLinearAllocator, sizeBytes, alignment uint64) memcore.MarkRaw {
	ptr := FixedLinearAllocatorMallocFast(allocatorMark, header, sizeBytes, alignment)
	memcore.MemoryClearNoHeapPointers(memcore.MemcoreMarkDereference(ptr), uintptr(sizeBytes))
	return ptr
}

//go:nosplit
func FixedLinearAllocatorCallocUnsafeFast(allocatorMark memcore.MarkRaw, header *FixedLinearAllocator, sizeBytes, alignment uint64) memcore.MarkRaw {
	ptr := FixedLinearAllocatorMallocUnsafeFast(allocatorMark, header, sizeBytes, alignment)
	memcore.MemoryClearNoHeapPointers(memcore.MemcoreMarkDereference(ptr), uintptr(sizeBytes))
	return ptr
}

func FixedLinearAllocatorMallocObjectFast[T any](allocatorMark memcore.MarkRaw, header *FixedLinearAllocator) (memcore.MarkRaw, *T) {
	size := memcore.SizeOf[T]()
	align := memcore.AlignOf[T]()
	ptr := FixedLinearAllocatorMallocFast(allocatorMark, header, size, align)
	return ptr, memcore.MemcoreMarkDereferenceObject[T](ptr)
}

func FixedLinearAllocatorCallocObjectFast[T any](allocatorMark memcore.MarkRaw, header *FixedLinearAllocator) (memcore.MarkRaw, *T) {
	size := memcore.SizeOf[T]()
	align := memcore.AlignOf[T]()
	ptr := FixedLinearAllocatorCallocFast(allocatorMark, header, size, align)
	return ptr, memcore.MemcoreMarkDereferenceObject[T](ptr)
}

func FixedLinearAllocatorResetFast(allocatorMark memcore.MarkRaw, header *FixedLinearAllocator) {
	memforgeAllocatorRemoveAll(allocatorMark)
	linearAllocatorResetState(&header.linearAllocatorState)
}
