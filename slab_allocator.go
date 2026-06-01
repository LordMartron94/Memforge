package memforge

import (
	"fmt"
	"memcore"
	"memstruct"
	"unsafe"
)

/*
FixedSlabAllocator is a fixed-capacity pool of uniform slots for type T.

[Context]
Slots are indexed; a free stack tracks available indices. Malloc pops an index, Free pushes it back.
The header struct must remain on the Go heap; only slot payload lives in the mmap region.

[Complexity]
Malloc and Free: O(1).
Reset: O(n) over slotCapacity to rebuild the free stack.

[Thread Safety]
Not safe for concurrent use from multiple goroutines.

[Invariants]
Do not store Go pointers in slot memory. Do not move this header into manual memory. Returned Malloc
memory is uninitialized unless using Calloc. Marks are invalid after Reset or Destroy.
*/
type FixedSlabAllocator[T any] struct {
	allocatorAddr      uintptr // base address of mmap region
	dataBaseOffset     uintptr
	allocatorTotalSize uint64
	regionID           uint32 // namespace for all slab allocations
	slotSize           uint64 // bytes per slot
	slotAlignment      uint64
	slotCapacity       uint64          // number of slots in the slab
	freeStack          memcore.MarkRaw // points to Stack[uint64]
	metaAllocator      memcore.MarkRaw // points to FixedLinearAllocator
}

/*
SlabAllocatorCreate maps a region and initializes a slab pool sized from SizeOf and AlignOf T.

[Parameters]
capacity - Number of slots; must be greater than zero.

[Returns]
A memcore.MarkRaw to the slab header.

[Errors]
Panics if capacity is zero or mmap fails.

[See Also]
SlabAllocatorCreateWithSlotSize when slot bytes come from memarch.LayoutBlueprint.
*/
func SlabAllocatorCreate[T any](capacity uint64) memcore.MarkRaw {
	return slabAllocatorCreate[T](capacity, memcore.SizeOf[T](), memcore.AlignOf[T]())
}

/*
SlabAllocatorCreateWithSlotSize maps a region and initializes a slab with explicit slot geometry.

[Context]
Use when each slot must hold a layout-planned block larger than SizeOf[T], for example the span
returned by MemArchLayoutBlueprintRequiredBytesGet paired with MemArchLayoutBlueprintRequiredAlignmentGet.
T remains the generic handle for the allocator API; bind slots with marks or pointers only if they
fit within the declared slot size.

[Parameters]
capacity - Number of slots; must be greater than zero.
slotSizeBytes - Minimum payload bytes per slot before alignment rounding.
slotAlignment - Required slot alignment; must be a power of two greater than zero.

[Returns]
A memcore.MarkRaw to the slab header.

[Errors]
Panics if capacity or slotSizeBytes is zero, alignment is invalid, or mmap fails.
*/
func SlabAllocatorCreateWithSlotSize[T any](capacity, slotSizeBytes, slotAlignment uint64) memcore.MarkRaw {
	return slabAllocatorCreate[T](capacity, slotSizeBytes, slotAlignment)
}

func slabAllocatorCreate[T any](capacity, slotSizeBytes, slotAlignment uint64) memcore.MarkRaw {
	if capacity == 0 {
		panic("slab allocator: capacity must be greater than zero")
	}
	if slotSizeBytes == 0 {
		panic("slab allocator: slot size must be greater than zero")
	}
	alignmentValidate(slotAlignment)

	slotSize := alignIdxUp(slotSizeBytes, slotAlignment)
	headerSize := memcore.SizeOf[FixedSlabAllocator[T]]()
	headerAlignedSize := alignIdxUp(uint64(headerSize), uint64(allocatorDataAddrAlignment))
	totalBytes := headerAlignedSize + slotSize*capacity

	mmap, err := memcore.MemmapRequest(int(totalBytes), memcore.PROT_READWRITE, memcore.MAP_ANON_PRIVATE)
	if err != nil {
		panic(fmt.Errorf("slab allocator: mmap failed: %w", err))
	}

	allocatorAddr := uintptr(unsafe.Pointer(&mmap[0]))
	regionID := memcore.MemcoreRegionRegister(allocatorAddr, totalBytes)

	headerPtr := memcore.MemcoreMarkCreate(regionID, 0)
	header := memcore.MemcoreMarkDereferenceObject[FixedSlabAllocator[T]](headerPtr)

	stackBytes := memstruct.StackRequiredBytesGet[uint64](capacity)
	stackAlign := memstruct.StackRequiredAlignmentGet[uint64]()

	metaAlloc := FixedLinearAllocatorCreate(stackBytes)
	stackPtr := FixedLinearAllocatorMalloc(metaAlloc, stackBytes, stackAlign)
	memstruct.StackInitializeAt[uint64](stackPtr, capacity)
	for i := capacity; i > 0; i-- {
		memstruct.StackPushUnsafe(stackPtr, i-1)
	}

	*header = FixedSlabAllocator[T]{
		allocatorAddr:      allocatorAddr,
		dataBaseOffset:     uintptr(headerAlignedSize),
		regionID:           regionID,
		allocatorTotalSize: totalBytes,
		slotSize:           slotSize,
		slotAlignment:      slotAlignment,
		slotCapacity:       capacity,
		freeStack:          stackPtr,
		metaAllocator:      metaAlloc,
	}

	dataCapBytes := slotSize * capacity
	memforgeAllocatorRegister(headerPtr, "Fixed Slab (Manual)", dataCapBytes, totalBytes)
	return headerPtr
}

/*
SlabAllocatorDestroy destroys the metadata linear allocator, unmaps the slab region, and clears telemetry.

[Invariants]
The allocator must not be used after this call.
*/
func SlabAllocatorDestroy[T any](allocator memcore.MarkRaw) {
	header := memcore.MemcoreMarkDereferenceObject[FixedSlabAllocator[T]](allocator)

	memforgeAllocatorDestroy(allocator)
	memcore.MemcoreRegionUnregister(header.regionID)

	FixedLinearAllocatorDestroy(header.metaAllocator)

	if err := memcore.MemmapUnmapAt(unsafe.Pointer(header.allocatorAddr), int(header.allocatorTotalSize)); err != nil {
		panic(fmt.Errorf("slab allocator: failed to unmap: %w", err))
	}
}

/*
SlabAllocatorReset rebuilds the free stack so every slot is available again.

[Complexity]
Time: O(n) where n is slotCapacity.

[Invariants]
All prior slot marks are invalid after Reset.
*/
func SlabAllocatorReset[T any](allocator memcore.MarkRaw) {
	header := memcore.MemcoreMarkDereferenceObject[FixedSlabAllocator[T]](allocator)

	memforgeAllocatorRemoveAll(allocator)

	// Reset free stack
	memstruct.StackClear[uint64](header.freeStack)
	for i := header.slotCapacity; i > 0; i-- {
		memstruct.StackPushUnsafe(header.freeStack, i-1)
	}
}

/*
SlabAllocatorMalloc pops a free slot and returns a mark to uninitialized storage.

[Returns]
A memcore.MarkRaw sized to the aligned slot for T.

[Errors]
Panics when the free stack is empty.

[Complexity]
Time: O(1). Space: O(1).
*/
//go:nosplit
func SlabAllocatorMalloc[T any](allocator memcore.MarkRaw) memcore.MarkRaw {
	header := memcore.MemcoreMarkDereferenceObject[FixedSlabAllocator[T]](allocator)

	idx, err := memstruct.StackPop[uint64](header.freeStack)
	if err != nil {
		panic(fmt.Errorf("slab allocator: out of memory: %w", err))
	}

	alignedIdx := slabAllocatorDataIdxGet(header, idx)
	ptr := memcore.MemcoreMarkOffsetFrom(allocator, uintptr(alignedIdx))
	memforgeAllocationAdd(allocator, ptr, header.slotSize)
	return ptr
}

/*
SlabAllocatorCalloc allocates a slot and zeroes the full slot byte span.
*/
//go:nosplit
func SlabAllocatorCalloc[T any](allocator memcore.MarkRaw) memcore.MarkRaw {
	header := memcore.MemcoreMarkDereferenceObject[FixedSlabAllocator[T]](allocator)

	ptr := SlabAllocatorMalloc[T](allocator)
	memcore.MemoryClearNoHeapPointers(memcore.MemcoreMarkDereference(ptr), uintptr(header.slotSize))
	return ptr
}

/*
SlabAllocatorMallocUnsafe pops a free slot without checking for exhaustion beyond stack underflow.

[Errors]
Panics if the free stack is empty (via StackPopUnsafe).
*/
//go:nosplit
func SlabAllocatorMallocUnsafe[T any](allocator memcore.MarkRaw) memcore.MarkRaw {
	header := memcore.MemcoreMarkDereferenceObject[FixedSlabAllocator[T]](allocator)
	idx := memstruct.StackPopUnsafe[uint64](header.freeStack)

	alignedIdx := slabAllocatorDataIdxGet(header, idx)
	ptr := memcore.MemcoreMarkOffsetFrom(allocator, uintptr(alignedIdx))
	memforgeAllocationAdd(allocator, ptr, header.slotSize)
	return ptr
}

/*
SlabAllocatorCallocUnsafe allocates via MallocUnsafe and zeroes the full slot byte span.
*/
//go:nosplit
func SlabAllocatorCallocUnsafe[T any](allocator memcore.MarkRaw) memcore.MarkRaw {
	header := memcore.MemcoreMarkDereferenceObject[FixedSlabAllocator[T]](allocator)
	ptr := SlabAllocatorMallocUnsafe[T](allocator)
	memcore.MemoryClearNoHeapPointers(memcore.MemcoreMarkDereference(ptr), uintptr(header.slotSize))
	return ptr
}

/*
SlabAllocatorFree returns a slot index to the free stack.

[Parameters]
slot - Mark previously returned by this allocator's Malloc family for the same T.

[Errors]
Panics if slot is outside the data region, misaligned, or has an invalid slot index.

[Side Effects]
Does not clear slot bytes; a later Malloc may reuse stale contents unless the client zeroes or uses Calloc.

[Invariants]
Do not use slot after Free. Malloc after Free without zeroing may observe old data.
*/
func SlabAllocatorFree[T any](allocator memcore.MarkRaw, slot memcore.MarkRaw) {
	header := memcore.MemcoreMarkDereferenceObject[FixedSlabAllocator[T]](allocator)
	slotPtr := memcore.MemcoreMarkDereferenceObjectUnsafe[T](slot)
	baseOffset, dataOffset, slotDataIdx := slabAllocatorItemIdxGet(header, slotPtr)

	if baseOffset < uint64(header.dataBaseOffset) {
		panic(fmt.Errorf("invalid pointer which does not belong to our data"))
	}

	if dataOffset%header.slotSize != 0 {
		panic(fmt.Errorf("misaligned pointer given (not aligned)"))
	}

	if slotDataIdx > header.slotCapacity-1 {
		panic(fmt.Errorf("invalid slot idx (%d) which is outside of capacity (%d)", slotDataIdx, header.slotCapacity))
	}

	memstruct.StackPushUnsafe(header.freeStack, slotDataIdx)
	memforgeAllocationRemove(allocator, slot)
}

/*
SlabAllocatorFreeUnsafe returns a slot to the free stack without bounds or alignment checks.

[Side Effects]
Same reuse semantics as SlabAllocatorFree; incorrect slot marks corrupt the free stack.
*/
func SlabAllocatorFreeUnsafe[T any](allocator memcore.MarkRaw, slot memcore.MarkRaw) {
	header := memcore.MemcoreMarkDereferenceObject[FixedSlabAllocator[T]](allocator)
	slotPtr := memcore.MemcoreMarkDereferenceObjectUnsafe[T](slot)
	_, _, slotDataIdx := slabAllocatorItemIdxGet(header, slotPtr)

	memstruct.StackPushUnsafe(header.freeStack, slotDataIdx)
	memforgeAllocationRemove(allocator, slot)
}

/*
SlabAllocatorSlotSizeGet returns the aligned byte size of each slot.

[Context]
Matches the value used when the allocator was created, including alignment rounding from
SlabAllocatorCreateWithSlotSize.
*/
func SlabAllocatorSlotSizeGet[T any](allocator memcore.MarkRaw) uint64 {
	header := memcore.MemcoreMarkDereferenceObject[FixedSlabAllocator[T]](allocator)
	return header.slotSize
}

/*
SlabAllocatorMallocObject returns a *T view of a new slab slot (uninitialized).

[Side Effects]
Convenience wrapper only; does not store Go pointers in manual memory. Unsafe when slot size exceeds sizeof(T).
*/
func SlabAllocatorMallocObject[T any](allocator memcore.MarkRaw) *T {
	ptr := SlabAllocatorMalloc[T](allocator)
	return memcore.MemcoreMarkDereferenceObject[T](ptr)
}

/*
SlabAllocatorCallocObject returns a *T view of a zeroed slab slot.
*/
func SlabAllocatorCallocObject[T any](allocator memcore.MarkRaw) *T {
	ptr := SlabAllocatorCalloc[T](allocator)
	return memcore.MemcoreMarkDereferenceObject[T](ptr)
}

/*
SlabAllocatorMallocObjectUnsafe returns a *T view via SlabAllocatorMallocUnsafe.
*/
func SlabAllocatorMallocObjectUnsafe[T any](allocator memcore.MarkRaw) *T {
	ptr := SlabAllocatorMallocUnsafe[T](allocator)
	return memcore.MemcoreMarkDereferenceObject[T](ptr)
}

/*
SlabAllocatorCallocObjectUnsafe returns a *T view via SlabAllocatorCallocUnsafe.
*/
func SlabAllocatorCallocObjectUnsafe[T any](allocator memcore.MarkRaw) *T {
	ptr := SlabAllocatorCallocUnsafe[T](allocator)
	return memcore.MemcoreMarkDereferenceObject[T](ptr)
}

// -------------------------------------------- PRIVATE HELPERS

func slabAllocatorItemIdxGet[T any](header *FixedSlabAllocator[T], item *T) (baseOffset, dataOffset, idx uint64) {
	headerAddr := uintptr(unsafe.Pointer(header))
	itemAddr := uintptr(unsafe.Pointer(item))

	itemAddrOffset := itemAddr - headerAddr
	itemAddrDataOffset := itemAddrOffset - header.dataBaseOffset
	return uint64(itemAddrOffset), uint64(itemAddrDataOffset), uint64(itemAddrDataOffset) / header.slotSize
}

//go:inline
func slabAllocatorDataIdxGet[T any](header *FixedSlabAllocator[T], idx uint64) uint64 {
	return alignIdxUp(uint64(header.dataBaseOffset)+(idx*header.slotSize), header.slotAlignment)
}
