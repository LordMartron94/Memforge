package memforge

import (
	"fmt"
	"memcore"
	"unsafe"
)

/*
FixedLinearAllocator is a fixed-capacity bump (arena) allocator in a single mmap region.

[Context]
The header and arena live in one memcore region. Allocations advance a bump index sequentially;
individual frees are not supported. Use for scratch buffers, frame arenas, and other transient
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
	allocatorAddr      uintptr
	allocatorTotalSize uint64
	dataBaseOffset     uintptr
	regionID           uint32
	dataByteIdx        uint64
	dataCapBytes       uint64
}

/*
FixedLinearAllocatorCreate maps a new region and initializes a fixed linear allocator.

[Parameters]
sizeBytes - Capacity of the bump arena excluding the allocator header.

[Returns]
A memcore.MarkRaw to the allocator header in the new region namespace.

[Errors]
Panics if mmap fails.

[Side Effects]
Registers a memcore region and memforge allocator telemetry entry.
*/
func FixedLinearAllocatorCreate(sizeBytes uint64) memcore.MarkRaw {
	headerSize := memcore.SizeOf[FixedLinearAllocator]()
	headerAlignedSize := alignIdxUp(uint64(headerSize), uint64(allocatorDataAddrAlignment))

	totalSize := headerAlignedSize + sizeBytes

	mmap, err := memcore.MemmapRequest(int(totalSize), memcore.PROT_READWRITE, memcore.MAP_ANON_PRIVATE)
	if err != nil {
		panic(fmt.Errorf("failed to create linear allocator: %w", err))
	}

	allocatorPtrRaw := unsafe.Pointer(&mmap[0])
	allocatorAddr := uintptr(allocatorPtrRaw)

	regionID := memcore.MemcoreRegionRegister(allocatorAddr, totalSize)

	allocatorPtr := memcore.MemcoreMarkCreate(regionID, 0)

	header := memcore.MemcoreMarkDereferenceObject[FixedLinearAllocator](allocatorPtr)
	*header = FixedLinearAllocator{
		allocatorAddr:      allocatorAddr,
		allocatorTotalSize: totalSize,
		dataBaseOffset:     uintptr(headerAlignedSize),
		regionID:           regionID,
		dataByteIdx:        0,
		dataCapBytes:       sizeBytes,
	}

	memforgeAllocatorRegister(allocatorPtr, "Fixed Linear (Manual)", sizeBytes, totalSize)

	return allocatorPtr
}

/*
FixedLinearAllocatorDestroy unmaps the region and unregisters all tracked allocations.

[Side Effects]
Unmaps memory, unregisters the memcore region, and clears memforge telemetry for allocator.

[Invariants]
The allocator must not be used after this call; subsequent access panics or is undefined.
*/
func FixedLinearAllocatorDestroy(allocator memcore.MarkRaw) {
	header := memcore.MemcoreMarkDereferenceObject[FixedLinearAllocator](allocator)

	memforgeAllocatorDestroy(allocator)

	memcore.MemcoreRegionUnregister(header.regionID)

	if err := memcore.MemmapUnmapAt(unsafe.Pointer(header.allocatorAddr), int(header.allocatorTotalSize)); err != nil {
		panic(fmt.Errorf("failed to destroy allocator: %w", err))
	}
}

//go:inline
func fixedLinearAllocatorOOMError(allocator *FixedLinearAllocator, requestedSize uint64) error {
	return fmt.Errorf("fixed linear allocator: out of memory, requested: %v, available: %v, current idx: %v, cap: %v", requestedSize, allocator.dataCapBytes-allocator.dataByteIdx, allocator.dataByteIdx, allocator.dataCapBytes)
}

/*
FixedLinearAllocatorMalloc allocates sizeBytes with alignment from the bump arena.

[Parameters]
sizeBytes - Requested allocation size in bytes.
alignment - Required alignment; must be a power of two greater than zero.

[Returns]
A memcore.MarkRaw offset into the allocator namespace. Memory is uninitialized.

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

	alignedIdx := fixedLinearAllocatorDataIdxGet(header, alignment)
	if !fixedLinearAllocatorCapacityGuarantee(header, sizeBytes, alignedIdx) {
		panic(fixedLinearAllocatorOOMError(header, sizeBytes))
	}

	offset := uintptr(alignedIdx)
	ptr := memcore.MemcoreMarkOffsetFrom(allocator, offset)

	memforgeAllocationAdd(allocator, ptr, sizeBytes)

	fixedLinearAllocatorIdxUpdate(header, alignedIdx, sizeBytes)
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
	alignedIdx := fixedLinearAllocatorDataIdxGet(header, alignment)

	if !fixedLinearAllocatorCapacityGuarantee(header, sizeBytes, alignedIdx) {
		panic(fixedLinearAllocatorOOMError(header, sizeBytes))
	}

	offset := uintptr(alignedIdx)
	ptr := memcore.MemcoreMarkOffsetFrom(allocator, offset)

	memforgeAllocationAdd(allocator, ptr, sizeBytes)
	fixedLinearAllocatorIdxUpdate(header, alignedIdx, sizeBytes)

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

	header.dataByteIdx = 0
}

// -------------------------- PRIVATE HELPERS --------------------------

//go:inline
func fixedLinearAllocatorCapacityGuarantee(header *FixedLinearAllocator, requestedSize, alignedIdx uint64) bool {
	return capacityGuarantee(alignedIdx, header.dataCapBytes+uint64(header.dataBaseOffset), requestedSize)
}

//go:inline
func fixedLinearAllocatorDataIdxGet(header *FixedLinearAllocator, requestedAlignment uint64) uint64 {
	return alignIdxUp(uint64(header.dataBaseOffset)+header.dataByteIdx, requestedAlignment)
}

//go:inline
func fixedLinearAllocatorIdxUpdate(header *FixedLinearAllocator, alignedIdx, sizeBytes uint64) {
	header.dataByteIdx = (alignedIdx - uint64(header.dataBaseOffset)) + sizeBytes
}
