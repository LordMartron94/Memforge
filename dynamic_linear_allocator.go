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
DynamicLinearAllocator is a growable bump allocator in a single mmap region.

[Context]
Like FixedLinearAllocator but expands the mapped region via mremap when the bump arena is full.
Register a GrowthStrategy at creation (by function ID or DynamicLinearAllocatorCreateFunction).

[Complexity]
Malloc: O(1) amortized; occasional growth is O(n) over moved bytes.
Reset: O(1).

[Side Effects]
Growth may relocate the mmap base; raw addresses derived before growth are invalid.

[Thread Safety]
Not safe for concurrent use from multiple goroutines.

[Invariants]
Do not store Go pointers in allocator-managed memory. Pointers become invalid after Reset, Destroy,
or a growth event that moves the region.
*/
type DynamicLinearAllocator struct {
	allocatorAddr  uintptr
	dataBaseOffset uintptr
	regionID       uint32

	allocatorTotalSize uint64
	dataCapBytes       uint64
	dataByteIdx        uint64

	growthStrategyID memcore.FunctionID
}

/*
DynamicLinearAllocatorCreate maps a region and initializes a dynamic linear allocator.

[Parameters]
initialCapacityBytes - Initial bump arena capacity excluding the header.
growthStrategyID - Registered memcore function ID for a GrowthStrategy.

[Returns]
A memcore.MarkRaw to the allocator header.

[Errors]
Panics if mmap fails or growthStrategyID does not resolve to a GrowthStrategy.
*/
func DynamicLinearAllocatorCreate(initialCapacityBytes uint64, growthStrategyID memcore.FunctionID) memcore.MarkRaw {
	headerSize := memcore.SizeOf[DynamicLinearAllocator]()
	headerAlignedSize := alignIdxUp(headerSize, uint64(allocatorDataAddrAlignment))

	totalSize := initialCapacityBytes + headerAlignedSize

	mmap, err := memcore.MemmapRequest(int(totalSize), memcore.PROT_READWRITE, memcore.MAP_ANON_PRIVATE)
	if err != nil {
		panic(fmt.Errorf("failed to create dynamic allocator: %w", err))
	}

	allocatorAddr := uintptr(unsafe.Pointer(&mmap[0]))
	regionID := memcore.MemcoreRegionRegister(allocatorAddr, totalSize)

	allocatorPtr := memcore.MemcoreMarkCreate(regionID, 0)

	header := memcore.MemcoreMarkDereferenceObject[DynamicLinearAllocator](allocatorPtr)
	*header = DynamicLinearAllocator{
		allocatorAddr:      allocatorAddr,
		dataBaseOffset:     uintptr(headerAlignedSize),
		regionID:           regionID,
		allocatorTotalSize: totalSize,
		dataCapBytes:       initialCapacityBytes,
		dataByteIdx:        0,
		growthStrategyID:   growthStrategyID,
	}

	memforgeAllocatorRegister(allocatorPtr, "Dynamic Linear (Manual)", initialCapacityBytes, totalSize)

	return allocatorPtr
}

/*
DynamicLinearAllocatorCreateFunction is like DynamicLinearAllocatorCreate but registers growthStrategy.

[Parameters]
initialCapacityBytes - Initial bump arena capacity excluding the header.
growthStrategy - Called when the arena must grow; must return capacity at least neededCap.

[Returns]
A memcore.MarkRaw to the allocator header.

[Errors]
Panics if mmap fails.
*/
func DynamicLinearAllocatorCreateFunction(initialCapacityBytes uint64, growthStrategy GrowthStrategy) memcore.MarkRaw {
	headerSize := memcore.SizeOf[DynamicLinearAllocator]()
	headerAlignedSize := alignIdxUp(headerSize, uint64(allocatorDataAddrAlignment))

	totalSize := initialCapacityBytes + headerAlignedSize

	mmap, err := memcore.MemmapRequest(int(totalSize), memcore.PROT_READWRITE, memcore.MAP_ANON_PRIVATE)
	if err != nil {
		panic(fmt.Errorf("failed to create dynamic allocator: %w", err))
	}

	allocatorAddr := uintptr(unsafe.Pointer(&mmap[0]))
	regionID := memcore.MemcoreRegionRegister(allocatorAddr, totalSize)

	allocatorPtr := memcore.MemcoreMarkCreate(regionID, 0)

	growthStrategyID := memcore.MemcoreFunctionRegisterTyped(growthStrategy)

	header := memcore.MemcoreMarkDereferenceObject[DynamicLinearAllocator](allocatorPtr)
	*header = DynamicLinearAllocator{
		allocatorAddr:      allocatorAddr,
		dataBaseOffset:     uintptr(headerAlignedSize),
		regionID:           regionID,
		allocatorTotalSize: totalSize,
		dataCapBytes:       initialCapacityBytes,
		dataByteIdx:        0,
		growthStrategyID:   growthStrategyID,
	}

	memforgeAllocatorRegister(allocatorPtr, "Dynamic Linear (Manual)", initialCapacityBytes, totalSize)

	return allocatorPtr
}

/*
DynamicLinearAllocatorDestroy unmaps the region and unregisters all tracked allocations.

[Invariants]
The allocator must not be used after this call.
*/
func DynamicLinearAllocatorDestroy(allocator memcore.MarkRaw) {
	header := memcore.MemcoreMarkDereferenceObject[DynamicLinearAllocator](allocator)

	memforgeAllocatorDestroy(allocator)
	memcore.MemcoreRegionUnregister(header.regionID)

	if err := memcore.MemmapUnmapAt(unsafe.Pointer(header.allocatorAddr), int(header.allocatorTotalSize)); err != nil {
		panic(fmt.Errorf("failed to destroy dynamic allocator: %w", err))
	}
}

/*
DynamicLinearAllocatorMalloc allocates sizeBytes with alignment, growing the arena if needed.

[Parameters]
sizeBytes - Requested allocation size in bytes.
alignment - Required alignment; must be a power of two greater than zero.

[Returns]
A memcore.MarkRaw to uninitialized memory.

[Errors]
Panics on invalid alignment, growth failure, growth strategy violation, or mmap remap failure.

[Complexity]
Time: O(1) amortized per call.

[Side Effects]
May grow and relocate the underlying mmap; updates memforge telemetry on growth.
*/
//go:nosplit
func DynamicLinearAllocatorMalloc(allocator memcore.MarkRaw, sizeBytes, alignment uint64) memcore.MarkRaw {
	header := memcore.MemcoreMarkDereferenceObject[DynamicLinearAllocator](allocator)
	alignmentValidate(alignment)

	alignedIdx := dynamicLinearAllocatorDataIdxGet(header, alignment)
	if !dynamicLinearAllocatorCapacityGuarantee(header, sizeBytes, alignedIdx) {
		header = dynamicLinearAllocatorGrow(allocator, header, alignedIdx+sizeBytes)
	}

	offset := uintptr(alignedIdx)
	ptr := memcore.MemcoreMarkOffsetFrom(allocator, offset)

	memforgeAllocationAdd(allocator, ptr, sizeBytes)
	dynamicLinearAllocatorIdxUpdate(header, alignedIdx, sizeBytes)
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

	alignedIdx := dynamicLinearAllocatorDataIdxGet(header, alignment)
	if !dynamicLinearAllocatorCapacityGuarantee(header, sizeBytes, alignedIdx) {
		header = dynamicLinearAllocatorGrow(allocator, header, alignedIdx+sizeBytes)
	}

	offset := uintptr(alignedIdx)
	ptr := memcore.MemcoreMarkOffsetFrom(allocator, offset)

	memforgeAllocationAdd(allocator, ptr, sizeBytes)
	dynamicLinearAllocatorIdxUpdate(header, alignedIdx, sizeBytes)
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
	header.dataByteIdx = 0
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

//go:inline
func dynamicLinearAllocatorGrow(allocator memcore.MarkRaw, header *DynamicLinearAllocator, neededCapacityBytes uint64) *DynamicLinearAllocator {
	prev := *header

	strategy := memcore.MemcoreFunctionRetrieveTyped[GrowthStrategy](prev.growthStrategyID)
	newSize := strategy(prev.dataCapBytes, neededCapacityBytes)
	if newSize < neededCapacityBytes {
		panic(GrowthStrategyViolation{
			PrevCapacity: prev.dataCapBytes,
			Required:     neededCapacityBytes,
			Returned:     newSize,
		})
	}

	oldTotal := prev.allocatorTotalSize
	oldBase := unsafe.Pointer(prev.allocatorAddr)

	newTotal := memcore.SizeOf[DynamicLinearAllocator]() + newSize

	newMap, err := memcore.MemmapRemapAt(
		oldBase,
		int(oldTotal),
		int(newTotal),
		memcore.MREMAP_MAYMOVE,
	)
	if err != nil {
		panic(fmt.Errorf("dynamic allocator: could not grow memory: %w", err))
	}

	newBaseAddr := uintptr(unsafe.Pointer(&newMap[0]))
	newHeaderPtr := (*DynamicLinearAllocator)(unsafe.Pointer(newBaseAddr))

	*newHeaderPtr = prev
	newHeaderPtr.allocatorAddr = newBaseAddr
	newHeaderPtr.allocatorTotalSize = newTotal
	newHeaderPtr.dataCapBytes = newSize

	memforgeAllocatorGrow(allocator, prev.dataCapBytes, newSize, newTotal)
	memcore.MemcoreRegionBaseUpdate(newHeaderPtr.regionID, newBaseAddr)
	return newHeaderPtr
}

//go:inline
func dynamicLinearAllocatorCapacityGuarantee(header *DynamicLinearAllocator, requestedSize, alignedIdx uint64) bool {
	return capacityGuarantee(alignedIdx, header.dataCapBytes+uint64(header.dataBaseOffset), requestedSize)
}

//go:inline
func dynamicLinearAllocatorDataIdxGet(header *DynamicLinearAllocator, requestedAlignment uint64) uint64 {
	return alignIdxUp(uint64(header.dataBaseOffset)+header.dataByteIdx, requestedAlignment)
}

//go:inline
func dynamicLinearAllocatorIdxUpdate(header *DynamicLinearAllocator, alignedIdx, sizeBytes uint64) {
	header.dataByteIdx = (alignedIdx - uint64(header.dataBaseOffset)) + sizeBytes
}
