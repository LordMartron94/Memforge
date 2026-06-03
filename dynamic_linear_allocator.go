package memforge

import (
	"fmt"
	"memcore"
	"unsafe"
)

/*
GrowthStrategy computes the new arena capacity when a dynamic linear allocator grows.

[Parameters]
currentCap - Arena data capacity before growth.
neededCap - Minimum capacity required to satisfy the pending allocation.

[Returns]
The new data capacity in bytes; must be greater than or equal to neededCap or growth panics.
*/
type GrowthStrategy func(currentCap, neededCap uint64) uint64

/*
DynamicLinearAllocator is a growable bump allocator in a single mmap data region.

[Context]
Like FixedLinearAllocator but expands the mapped data region via mremap when the bump arena is full.
Register a GrowthStrategy at creation (by function ID or DynamicLinearAllocatorCreateFunction).
CPU-only: no CreateForDataRegion variant.

[Complexity]
Malloc: O(1) amortized; occasional growth is O(n) over moved bytes.
Reset: O(1).

[Side Effects]
Growth may relocate the data mmap base; raw addresses derived before growth are invalid.

[Thread Safety]
Not safe for concurrent use from multiple goroutines.

[Invariants]
Do not store Go pointers in allocator-managed memory. Pointers become invalid after Reset, Destroy,
or a growth event that moves the region.
*/
type DynamicLinearAllocator struct {
	linearAllocatorState
	growthStrategyID memcore.FunctionID
}

/*
DynamicLinearAllocatorCreate maps a data region and initializes a dynamic linear allocator.

[Parameters]
initialCapacityBytes - Initial bump arena capacity.
growthStrategyID - Registered memcore function ID for a GrowthStrategy.

[Returns]
A memcore.MarkRaw to the allocator header in the memforge header store.

[Errors]
Panics if mmap fails or growthStrategyID does not resolve to a GrowthStrategy.
*/
func DynamicLinearAllocatorCreate(initialCapacityBytes uint64, growthStrategyID memcore.FunctionID, tag string) memcore.MarkRaw {
	backing, mmapBase, mmap := memforgeDataBackingCreateFromMmap(initialCapacityBytes)
	return dynamicLinearAllocatorCreateInternal(backing, true, mmapBase, uint64(len(mmap)), growthStrategyID, tag)
}

/*
DynamicLinearAllocatorCreateFunction is like DynamicLinearAllocatorCreate but registers growthStrategy.

[Parameters]
initialCapacityBytes - Initial bump arena capacity.
growthStrategy - Called when the arena must grow; must return capacity at least neededCap.

[Returns]
A memcore.MarkRaw to the allocator header in the memforge header store.

[Errors]
Panics if mmap fails.
*/
func DynamicLinearAllocatorCreateFunction(initialCapacityBytes uint64, growthStrategy GrowthStrategy, tag string) memcore.MarkRaw {
	backing, mmapBase, mmap := memforgeDataBackingCreateFromMmap(initialCapacityBytes)
	growthStrategyID := memcore.MemcoreFunctionRegisterTyped(growthStrategy)
	return dynamicLinearAllocatorCreateInternal(backing, true, mmapBase, uint64(len(mmap)), growthStrategyID, tag)
}

func dynamicLinearAllocatorCreateInternal(
	backing MemforgeDataBacking,
	ownsDataRegion bool,
	dataMmapBase uintptr,
	dataMmapSize uint64,
	growthStrategyID memcore.FunctionID,
	tag string,
) memcore.MarkRaw {
	headerMark := memforgeHeaderAllocate(
		uint64(memcore.SizeOf[DynamicLinearAllocator]()),
		uint64(memcore.AlignOf[DynamicLinearAllocator]()),
	)

	header := memcore.MemcoreMarkDereferenceObject[DynamicLinearAllocator](headerMark)
	header.linearAllocatorState = linearAllocatorStateInit(backing, ownsDataRegion, dataMmapBase, dataMmapSize)
	header.growthStrategyID = growthStrategyID

	memforgeAllocatorRegister(headerMark, "Dynamic Linear (Mmap)", tag, backing.DataRegionID, backing.DataCapBytes, backing.DataCapBytes)
	return headerMark
}

/*
DynamicLinearAllocatorDestroy unmaps owned data regions and unregisters tracked allocations.

[Invariants]
The allocator must not be used after this call.
*/
func DynamicLinearAllocatorDestroy(allocator memcore.MarkRaw) {
	header := memcore.MemcoreMarkDereferenceObject[DynamicLinearAllocator](allocator)

	memforgeAllocatorDestroy(allocator)
	linearAllocatorStateDestroy(&header.linearAllocatorState)
}

/*
DynamicLinearAllocatorMalloc allocates sizeBytes with alignment, growing the arena if needed.

[Parameters]
sizeBytes - Requested allocation size in bytes.
alignment - Required alignment; must be a power of two greater than zero.

[Returns]
A memcore.MarkRaw to uninitialized memory in the data region namespace.

[Errors]
Panics on invalid alignment, growth failure, growth strategy violation, or mmap remap failure.

[Complexity]
Time: O(1) amortized per call.

[Side Effects]
May grow and relocate the underlying data mmap; updates memforge telemetry on growth.
*/
//go:nosplit
func DynamicLinearAllocatorMalloc(allocator memcore.MarkRaw, sizeBytes, alignment uint64) memcore.MarkRaw {
	header := memcore.MemcoreMarkDereferenceObject[DynamicLinearAllocator](allocator)
	alignmentValidate(alignment)

	state := &header.linearAllocatorState
	alignedIdx := linearAllocatorDataIdxGet(state, alignment)
	if !linearAllocatorCapacityGuarantee(state, sizeBytes, alignedIdx) {
		dynamicLinearAllocatorGrow(allocator, header, alignedIdx+sizeBytes)
		alignedIdx = linearAllocatorDataIdxGet(state, alignment)
	}

	ptr := linearAllocatorMallocMark(state, alignedIdx)

	memforgeAllocationAdd(allocator, ptr, sizeBytes)
	linearAllocatorIdxUpdate(state, alignedIdx, sizeBytes)
	return ptr
}

/*
DynamicLinearAllocatorMallocUnsafe allocates without alignment validation.

[Errors]
Panics on out-of-memory or growth failures, same as DynamicLinearAllocatorMalloc.
*/
//go:nosplit
func DynamicLinearAllocatorMallocUnsafe(allocator memcore.MarkRaw, sizeBytes, alignment uint64) memcore.MarkRaw {
	header := memcore.MemcoreMarkDereferenceObject[DynamicLinearAllocator](allocator)

	state := &header.linearAllocatorState
	alignedIdx := linearAllocatorDataIdxGet(state, alignment)
	if !linearAllocatorCapacityGuarantee(state, sizeBytes, alignedIdx) {
		dynamicLinearAllocatorGrow(allocator, header, alignedIdx+sizeBytes)
		alignedIdx = linearAllocatorDataIdxGet(state, alignment)
	}

	ptr := linearAllocatorMallocMark(state, alignedIdx)

	memforgeAllocationAdd(allocator, ptr, sizeBytes)
	linearAllocatorIdxUpdate(state, alignedIdx, sizeBytes)
	return ptr
}

/*
DynamicLinearAllocatorCalloc allocates with alignment and zeroes the region.
*/
//go:nosplit
func DynamicLinearAllocatorCalloc(allocator memcore.MarkRaw, sizeBytes, alignment uint64) memcore.MarkRaw {
	ptr := DynamicLinearAllocatorMalloc(allocator, sizeBytes, alignment)
	memcore.MemoryClearNoHeapPointers(memcore.MemcoreMarkDereference(ptr), uintptr(sizeBytes))
	return ptr
}

/*
DynamicLinearAllocatorCallocUnsafe allocates and zeroes memory without alignment validation.
*/
//go:nosplit
func DynamicLinearAllocatorCallocUnsafe(allocator memcore.MarkRaw, sizeBytes, alignment uint64) memcore.MarkRaw {
	ptr := DynamicLinearAllocatorMallocUnsafe(allocator, sizeBytes, alignment)
	memcore.MemoryClearNoHeapPointers(memcore.MemcoreMarkDereference(ptr), uintptr(sizeBytes))
	return ptr
}

/*
DynamicLinearAllocatorMallocObject allocates storage for T using SizeOf and AlignOf.

[Returns]
A memcore.MarkRaw and a *T view. The *T must not be stored inside manually allocated memory.
*/
func DynamicLinearAllocatorMallocObject[T any](allocator memcore.MarkRaw) (memcore.MarkRaw, *T) {
	size := memcore.SizeOf[T]()
	align := memcore.AlignOf[T]()
	ptr := DynamicLinearAllocatorMalloc(allocator, size, align)
	return ptr, memcore.MemcoreMarkDereferenceObject[T](ptr)
}

/*
DynamicLinearAllocatorCallocObject allocates a zeroed value of T.

[Returns]
A memcore.MarkRaw and a *T view. The *T must not be stored inside manually allocated memory.
*/
func DynamicLinearAllocatorCallocObject[T any](allocator memcore.MarkRaw) (memcore.MarkRaw, *T) {
	size := memcore.SizeOf[T]()
	align := memcore.AlignOf[T]()
	ptr := DynamicLinearAllocatorCalloc(allocator, size, align)
	return ptr, memcore.MemcoreMarkDereferenceObject[T](ptr)
}

/*
DynamicLinearAllocatorReset sets the bump index to zero without shrinking the mapped capacity.

[Complexity]
Time: O(1).

[Side Effects]
Clears memforge allocation tracking. Does not zero memory or unmap excess capacity.

[Invariants]
All prior allocation marks are invalid after Reset.
*/
func DynamicLinearAllocatorReset(allocator memcore.MarkRaw) {
	header := memcore.MemcoreMarkDereferenceObject[DynamicLinearAllocator](allocator)
	memforgeAllocatorRemoveAll(allocator)
	linearAllocatorResetState(&header.linearAllocatorState)
}

// -------------------------- PRIVATE HELPERS --------------------------

/*
GrowthStrategyViolation is returned when a GrowthStrategy proposes capacity below neededCap.
*/
type GrowthStrategyViolation struct {
	PrevCapacity uint64
	Required     uint64
	Returned     uint64
}

func (e GrowthStrategyViolation) Error() string {
	return fmt.Sprintf(
		"growth strategy violation: returned %d < required %d (previous capacity %d)",
		e.Returned, e.Required, e.PrevCapacity,
	)
}

func dynamicLinearAllocatorGrow(allocator memcore.MarkRaw, header *DynamicLinearAllocator, neededCapacityBytes uint64) {
	state := &header.linearAllocatorState
	prevCap := state.dataCapBytes

	strategy := memcore.MemcoreFunctionRetrieveTyped[GrowthStrategy](header.growthStrategyID)
	newSize := strategy(prevCap, neededCapacityBytes)
	if newSize < neededCapacityBytes {
		panic(GrowthStrategyViolation{
			PrevCapacity: prevCap,
			Required:     neededCapacityBytes,
			Returned:     newSize,
		})
	}

	oldBase := unsafe.Pointer(state.dataMmapBase)
	oldSize := int(state.dataMmapSize)

	newMap, err := memcore.MemmapRemapAt(
		oldBase,
		oldSize,
		int(newSize),
		memcore.MREMAP_MAYMOVE,
	)
	if err != nil {
		panic(fmt.Errorf("dynamic allocator: could not grow memory: %w", err))
	}

	newBaseAddr := uintptr(unsafe.Pointer(&newMap[0]))
	state.dataMmapBase = newBaseAddr
	state.dataMmapSize = uint64(len(newMap))
	state.dataCapBytes = newSize

	memforgeAllocatorGrow(allocator, prevCap, newSize, newSize)
	memcore.MemcoreRegionBaseUpdate(state.dataRegionID, newBaseAddr)
}
