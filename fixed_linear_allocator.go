package memforge

import (
	"fmt"
	"memcore"
)

/*
FixedLinearAllocator is a fixed-capacity bump (arena) allocator backed by one data region.

[Context]
Allocator state lives in the memforge CPU header store. Data allocations are marks in the data
region namespace starting at offset zero. Use for scratch buffers, frame arenas, and other transient
workspaces scoped to Reset or Destroy.

[Complexity]
Malloc: O(1) time, O(1) auxiliary space per call.
Reset: O(1).

[Side Effects]
Malloc mutates the bump index and may register allocations with memforge debugger hooks when enabled.

[Thread Safety]
Not safe for concurrent use from multiple goroutines.

[Invariants]
Do not store Go pointers in allocator-managed memory. Pointers become invalid after Reset or Destroy.
alignment passed to Malloc must be a power of two greater than zero.
*/
type FixedLinearAllocator struct {
	linearAllocatorState
}

/*
FixedLinearAllocatorCreate maps a new data region and initializes a fixed linear allocator.

[Parameters]
sizeBytes - Capacity of the bump arena.
tag - Human-readable label recorded by the memforge debugger (for example "Renderer Scratch").

[Returns]
A memcore.MarkRaw to the allocator header in the memforge header store.

[Errors]
Panics if mmap fails.

[Side Effects]
Registers a memcore data region and memforge allocator telemetry entry.
*/
func FixedLinearAllocatorCreate(sizeBytes uint64, tag string) memcore.MarkRaw {
	backing, mmapBase, mmap := memforgeDataBackingCreateFromMmap(sizeBytes)
	return fixedLinearAllocatorCreateInternal(backing, true, mmapBase, uint64(len(mmap)), "Fixed Linear (Mmap)", tag)
}

/*
FixedLinearAllocatorCreateForDataRegion initializes a fixed linear allocator over caller-owned data.

[Parameters]
backing - Pre-registered memcore region and capacity. memforge does not unregister this region on Destroy.

[Returns]
A memcore.MarkRaw to the allocator header in the memforge header store.
*/
func FixedLinearAllocatorCreateForDataRegion(backing MemforgeDataBacking, tag string) memcore.MarkRaw {
	return fixedLinearAllocatorCreateInternal(backing, false, 0, 0, "Fixed Linear (External)", tag)
}

func fixedLinearAllocatorCreateInternal(
	backing MemforgeDataBacking,
	ownsDataRegion bool,
	dataMmapBase uintptr,
	dataMmapSize uint64,
	debugName string,
	tag string,
) memcore.MarkRaw {
	headerMark := memforgeHeaderAllocate(
		uint64(memcore.SizeOf[FixedLinearAllocator]()),
		uint64(memcore.AlignOf[FixedLinearAllocator]()),
	)

	header := memcore.MemcoreMarkDereferenceObject[FixedLinearAllocator](headerMark)
	header.linearAllocatorState = linearAllocatorStateInit(backing, ownsDataRegion, dataMmapBase, dataMmapSize)

	memforgeAllocatorRegister(headerMark, debugName, tag, backing.DataRegionID, backing.DataCapBytes, backing.DataCapBytes)
	return headerMark
}

/*
FixedLinearAllocatorDestroy tears down telemetry and owned data regions.

[Side Effects]
When the allocator owns its data region, unmaps memory and unregisters the memcore region.
Caller-owned data regions are not unregistered.

[Invariants]
The allocator must not be used after this call; subsequent access panics or is undefined.
*/
func FixedLinearAllocatorDestroy(allocator memcore.MarkRaw) {
	header := memcore.MemcoreMarkDereferenceObject[FixedLinearAllocator](allocator)

	memforgeAllocatorDestroy(allocator)
	linearAllocatorStateDestroy(&header.linearAllocatorState)
}

//go:inline
func fixedLinearAllocatorOOMError(state *linearAllocatorState, requestedSize uint64) error {
	return fmt.Errorf("fixed linear allocator: out of memory, requested: %v, available: %v, current idx: %v, cap: %v", requestedSize, state.dataCapBytes-state.dataByteIdx, state.dataByteIdx, state.dataCapBytes)
}

/*
FixedLinearAllocatorMalloc allocates sizeBytes with alignment from the bump arena.

[Parameters]
sizeBytes - Requested allocation size in bytes.
alignment - Required alignment; must be a power of two greater than zero.

[Returns]
A memcore.MarkRaw in the allocator data region namespace. Memory is uninitialized.

[Errors]
Panics if alignment is invalid or the arena lacks capacity.

[Complexity]
Time: O(1). Space: O(1) auxiliary per call.

[Side Effects]
Advances the bump index and may record the allocation for debugging.
*/
//go:nosplit
func FixedLinearAllocatorMalloc(allocator memcore.MarkRaw, sizeBytes, alignment uint64) memcore.MarkRaw {
	header := memcore.MemcoreMarkDereferenceObject[FixedLinearAllocator](allocator)
	alignmentValidate(alignment)

	alignedIdx := linearAllocatorDataIdxGet(&header.linearAllocatorState, alignment)
	if !linearAllocatorCapacityGuarantee(&header.linearAllocatorState, sizeBytes, alignedIdx) {
		panic(fixedLinearAllocatorOOMError(&header.linearAllocatorState, sizeBytes))
	}

	ptr := linearAllocatorMallocMark(&header.linearAllocatorState, alignedIdx)

	memforgeAllocationAdd(allocator, ptr, sizeBytes)

	linearAllocatorIdxUpdate(&header.linearAllocatorState, alignedIdx, sizeBytes)
	return ptr
}

/*
FixedLinearAllocatorMallocUnsafe allocates without alignment validation.

[Parameters]
sizeBytes - Requested allocation size in bytes.
alignment - Used for bump alignment only; not validated.

[Returns]
A memcore.MarkRaw to uninitialized memory.

[Errors]
Panics if the arena lacks capacity.

[Side Effects]
Same as FixedLinearAllocatorMalloc except alignment is not validated.
*/
//go:nosplit
func FixedLinearAllocatorMallocUnsafe(allocator memcore.MarkRaw, sizeBytes, alignment uint64) memcore.MarkRaw {
	header := memcore.MemcoreMarkDereferenceObject[FixedLinearAllocator](allocator)
	alignedIdx := linearAllocatorDataIdxGet(&header.linearAllocatorState, alignment)

	if !linearAllocatorCapacityGuarantee(&header.linearAllocatorState, sizeBytes, alignedIdx) {
		panic(fixedLinearAllocatorOOMError(&header.linearAllocatorState, sizeBytes))
	}

	ptr := linearAllocatorMallocMark(&header.linearAllocatorState, alignedIdx)

	memforgeAllocationAdd(allocator, ptr, sizeBytes)
	linearAllocatorIdxUpdate(&header.linearAllocatorState, alignedIdx, sizeBytes)

	return ptr
}

/*
FixedLinearAllocatorCalloc allocates sizeBytes with alignment and zeroes the region.

[Returns]
A memcore.MarkRaw to zeroed memory.

[Errors]
Panics on invalid alignment or out-of-memory, same as FixedLinearAllocatorMalloc.
*/
func FixedLinearAllocatorCalloc(allocator memcore.MarkRaw, sizeBytes, alignment uint64) memcore.MarkRaw {
	ptr := FixedLinearAllocatorMalloc(allocator, sizeBytes, alignment)
	memcore.MemoryClearNoHeapPointers(memcore.MemcoreMarkDereference(ptr), uintptr(sizeBytes))
	return ptr
}

/*
FixedLinearAllocatorCallocUnsafe allocates and zeroes memory without alignment validation.

[Errors]
Panics if the arena lacks capacity.
*/
//go:nosplit
func FixedLinearAllocatorCallocUnsafe(allocator memcore.MarkRaw, sizeBytes, alignment uint64) memcore.MarkRaw {
	ptr := FixedLinearAllocatorMallocUnsafe(allocator, sizeBytes, alignment)
	memcore.MemoryClearNoHeapPointers(memcore.MemcoreMarkDereference(ptr), uintptr(sizeBytes))
	return ptr
}

/*
FixedLinearAllocatorMallocObject allocates storage for T using SizeOf and AlignOf.

[Returns]
A memcore.MarkRaw and a *T view of the same storage. The *T must not be stored inside
manually allocated memory (Go pointer rules).

[Errors]
Panics on invalid alignment or out-of-memory, same as FixedLinearAllocatorMalloc.
*/
func FixedLinearAllocatorMallocObject[T any](allocator memcore.MarkRaw) (memcore.MarkRaw, *T) {
	size := memcore.SizeOf[T]()
	align := memcore.AlignOf[T]()
	ptr := FixedLinearAllocatorMalloc(allocator, size, align)
	return ptr, memcore.MemcoreMarkDereferenceObject[T](ptr)
}

/*
FixedLinearAllocatorCallocObject allocates a zeroed value of T.

[Returns]
A memcore.MarkRaw and a *T view. The *T must not be stored inside manually allocated memory.
*/
func FixedLinearAllocatorCallocObject[T any](allocator memcore.MarkRaw) (memcore.MarkRaw, *T) {
	size := memcore.SizeOf[T]()
	align := memcore.AlignOf[T]()
	ptr := FixedLinearAllocatorCalloc(allocator, size, align)
	return ptr, memcore.MemcoreMarkDereferenceObject[T](ptr)
}

/*
FixedLinearAllocatorReset sets the bump index to zero and reclaims the arena in O(1).

[Complexity]
Time: O(1). Space: O(1).

[Side Effects]
Clears memforge allocation tracking for this allocator. Does not zero memory.

[Invariants]
All pointers from prior Malloc calls are invalid after Reset; dereferencing them is undefined.
*/
func FixedLinearAllocatorReset(allocator memcore.MarkRaw) {
	header := memcore.MemcoreMarkDereferenceObject[FixedLinearAllocator](allocator)
	memforgeAllocatorRemoveAll(allocator)

	linearAllocatorResetState(&header.linearAllocatorState)
}
