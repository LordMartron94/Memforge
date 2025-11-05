package memforge

import (
	"fmt"
	"memcore"
	"memstruct"
	"unsafe"
)

// FixedSlabAllocator is a pooled allocator for fixed-size objects of type T.
// All allocations live in a dedicated namespace inside a single mmap region.
// The allocator itself (the header) must stay on the Go heap.
//
// ⚠️ Do NOT store Go pointers inside memory returned by this allocator.
// ⚠️ Do NOT move the allocator struct itself into manual memory.
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

// SlabAllocatorCreate creates a new slab allocator inside its own mmap region.
//
// The allocator registers its own namespace. It also creates a metadata allocator
// (FixedLinearAllocator) that holds the free stack structure.
func SlabAllocatorCreate[T any](capacity uint64) memcore.MarkRaw {
	if capacity == 0 {
		panic("cannot create slab allocator with capacity 0")
	}

	objSize := memcore.SizeOf[T]()
	objAlign := memcore.AlignOf[T]()
	slotSize := alignIdxUp(objSize, objAlign)
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

	metaAlloc := FixedLinearAllocatorCreate(int(stackBytes))
	header.metaAllocator = metaAlloc

	stackPtr := FixedLinearAllocatorMalloc(metaAlloc, stackBytes, stackAlign)
	memstruct.StackInitializeAt[uint64](stackPtr, capacity)
	header.freeStack = stackPtr

	for i := capacity; i > 0; i-- {
		memstruct.StackPushUnsafe(stackPtr, i-1)
	}

	// --- Write header
	*header = FixedSlabAllocator[T]{
		allocatorAddr:      allocatorAddr,
		dataBaseOffset:     uintptr(headerAlignedSize),
		regionID:           regionID,
		allocatorTotalSize: totalBytes,
		slotSize:           slotSize,
		slotAlignment:      memcore.AlignOf[uint64](),
		slotCapacity:       capacity,
		freeStack:          stackPtr,
		metaAllocator:      metaAlloc,
	}

	memforgeAllocatorRegister(headerPtr, "Fixed Slab (Manual)")
	return headerPtr
}

// SlabAllocatorDestroy destroys the slab allocator and all registered pointers.
//
// Do NOT use the allocator after calling this.
func SlabAllocatorDestroy[T any](allocator memcore.MarkRaw) {
	header := memcore.MemcoreMarkDereferenceObject[FixedSlabAllocator[T]](allocator)

	memforgeAllocatorDestroy(allocator)
	memcore.MemcoreRegionUnregister(header.regionID)

	FixedLinearAllocatorDestroy(header.metaAllocator)

	if err := memcore.MemmapUnmapAt(unsafe.Pointer(header.allocatorAddr), int(header.allocatorTotalSize)); err != nil {
		panic(fmt.Errorf("slab allocator: failed to unmap: %w", err))
	}
}

// SlabAllocatorReset clears the allocator, restoring all slots to free state.
//
// Using previously returned pointers after reset is undefined behaviour.
func SlabAllocatorReset[T any](allocator memcore.MarkRaw) {
	header := memcore.MemcoreMarkDereferenceObject[FixedSlabAllocator[T]](allocator)

	memforgeAllocatorRemoveAll(allocator)

	// Reset free stack
	memstruct.StackClear[uint64](header.freeStack)
	for i := header.slotCapacity; i > 0; i-- {
		memstruct.StackPushUnsafe(header.freeStack, i-1)
	}
}

// SlabAllocatorMalloc allocates one slot and returns a memcore.MarkRaw.
//
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

// SlabAllocatorCalloc allocates and zeroes a slot.
//
//go:nosplit
func SlabAllocatorCalloc[T any](allocator memcore.MarkRaw) memcore.MarkRaw {
	header := memcore.MemcoreMarkDereferenceObject[FixedSlabAllocator[T]](allocator)

	ptr := SlabAllocatorMalloc[T](allocator)
	memcore.MemoryClearNoHeapPointers(memcore.MemcoreMarkDereference(ptr), uintptr(header.slotSize))
	return ptr
}

// SlabAllocatorMallocUnsafe allocates without validation.
//
//go:nosplit
func SlabAllocatorMallocUnsafe[T any](allocator memcore.MarkRaw) memcore.MarkRaw {
	header := memcore.MemcoreMarkDereferenceObject[FixedSlabAllocator[T]](allocator)
	idx := memstruct.StackPopUnsafe[uint64](header.freeStack)

	offset := memcore.SizeOf[FixedSlabAllocator[T]]() + idx*header.slotSize
	ptr := memcore.MemcoreMarkOffsetFrom(allocator, uintptr(offset))
	return ptr
}

// SlabAllocatorCallocUnsafe allocates and zeroes memory without validation.
//
//go:nosplit
func SlabAllocatorCallocUnsafe[T any](allocator memcore.MarkRaw) memcore.MarkRaw {
	ptr := SlabAllocatorMallocUnsafe[T](allocator)
	memcore.MemoryClearNoHeapPointers(memcore.MemcoreMarkDereference(ptr), uintptr(memcore.SizeOf[T]()))
	return ptr
}

// Convenience wrappers (typed access)
// They only exist for ergonomic access in Go code, and never store Go pointers.

func SlabAllocatorMallocObject[T any](allocator memcore.MarkRaw) *T {
	ptr := SlabAllocatorMalloc[T](allocator)
	return memcore.MemcoreMarkDereferenceObject[T](ptr)
}

func SlabAllocatorCallocObject[T any](allocator memcore.MarkRaw) *T {
	ptr := SlabAllocatorCalloc[T](allocator)
	return memcore.MemcoreMarkDereferenceObject[T](ptr)
}

func SlabAllocatorMallocObjectUnsafe[T any](allocator memcore.MarkRaw) *T {
	ptr := SlabAllocatorMallocUnsafe[T](allocator)
	return memcore.MemcoreMarkDereferenceObject[T](ptr)
}

func SlabAllocatorCallocObjectUnsafe[T any](allocator memcore.MarkRaw) *T {
	ptr := SlabAllocatorCallocUnsafe[T](allocator)
	return memcore.MemcoreMarkDereferenceObject[T](ptr)
}

// -------------------------------------------- PRIVATE HELPERS

//go:inline
func slabAllocatorDataIdxGet[T any](header *FixedSlabAllocator[T], idx uint64) uint64 {
	return alignIdxUp(uint64(header.dataBaseOffset)+(idx*header.slotSize), header.slotAlignment)
}
