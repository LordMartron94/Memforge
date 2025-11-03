package memforge

import (
	"fmt"
	"memcore"
	"memcore/primitives"
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
	namespace          uint32 // namespace for all slab allocations
	slotSize           uint64 // bytes per slot
	slotAlignment      uint64
	slotCapacity       uint64          // number of slots in the slab
	freeStack          memcore.Pointer // points to Stack[uint64]
	metaAllocator      memcore.Pointer // points to FixedLinearAllocator
}

// SlabAllocatorCreate creates a new slab allocator inside its own mmap region.
//
// The allocator registers its own namespace. It also creates a metadata allocator
// (FixedLinearAllocator) that holds the free stack structure.
func SlabAllocatorCreate[T any](capacity uint64) memcore.Pointer {
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
	namespace := memcore.MemcoreAddressSpaceRegister(allocatorAddr)

	headerPtr := memcore.MemcorePointerCreate(namespace, 0, memcore.TypeOf[FixedSlabAllocator[T]]())
	memcore.MemcorePointerRegister(headerPtr)
	header := memcore.MemcorePointerDereferenceObjectUnsafe[FixedSlabAllocator[T]](headerPtr)

	stackBytes := primitives.StackRequiredBytesGet[uint64](capacity)
	stackAlign := primitives.StackRequiredAlignmentGet[uint64]()

	metaAlloc := FixedLinearAllocatorCreate(int(stackBytes))
	header.metaAllocator = metaAlloc

	stackPtr := FixedLinearAllocatorMalloc(metaAlloc, stackBytes, stackAlign)
	memcore.MemcorePointerUpdateType(stackPtr, memcore.TypeOf[primitives.Stack[T]]())
	primitives.StackInitializeAt[uint64](stackPtr, capacity)
	header.freeStack = stackPtr

	for i := capacity; i > 0; i-- {
		primitives.StackPushUnsafe(stackPtr, i-1)
	}

	// --- Write header
	*header = FixedSlabAllocator[T]{
		allocatorAddr:      allocatorAddr,
		dataBaseOffset:     uintptr(headerAlignedSize),
		namespace:          namespace,
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
func SlabAllocatorDestroy[T any](allocator memcore.Pointer) {
	header := memcore.MemcorePointerDereferenceObjectUnsafe[FixedSlabAllocator[T]](allocator)

	memforgeAllocatorDestroy(allocator)
	memcore.MemcoreAddressSpaceUnregister(header.namespace)

	primitives.StackDestroy[uint64](header.freeStack)
	FixedLinearAllocatorDestroy(header.metaAllocator)

	if err := memcore.MemmapUnmapAt(unsafe.Pointer(header.allocatorAddr), int(header.allocatorTotalSize)); err != nil {
		panic(fmt.Errorf("slab allocator: failed to unmap: %w", err))
	}
}

// SlabAllocatorReset clears the allocator, restoring all slots to free state.
//
// Using previously returned pointers after reset is undefined behaviour.
func SlabAllocatorReset[T any](allocator memcore.Pointer) {
	header := memcore.MemcorePointerDereferenceObjectUnsafe[FixedSlabAllocator[T]](allocator)

	memforgeAllocatorRemoveAll(allocator)
	memcore.MemcoreAddressSpaceClearPointers(header.namespace)
	memcore.MemcorePointerRegister(allocator)

	// Reset free stack
	primitives.StackClear[uint64](header.freeStack)
	for i := header.slotCapacity; i > 0; i-- {
		primitives.StackPushUnsafe(header.freeStack, i-1)
	}
}

// SlabAllocatorMalloc allocates one slot and returns a memcore.Pointer.
//
//go:nosplit
func SlabAllocatorMalloc[T any](allocator memcore.Pointer) memcore.Pointer {
	header := memcore.MemcorePointerDereferenceObjectUnsafe[FixedSlabAllocator[T]](allocator)

	idx, err := primitives.StackPop[uint64](header.freeStack)
	if err != nil {
		panic(fmt.Errorf("slab allocator: out of memory: %w", err))
	}

	alignedIdx := slabAllocatorDataIdxGet(header, idx)
	ptr := memcore.MemcorePointerCreate(header.namespace, uintptr(alignedIdx), memcore.TypeOf[T]())
	memcore.MemcorePointerRegister(ptr)
	memforgeAllocationAdd(allocator, ptr, header.slotSize)
	return ptr
}

// SlabAllocatorCalloc allocates and zeroes a slot.
//
//go:nosplit
func SlabAllocatorCalloc[T any](allocator memcore.Pointer) memcore.Pointer {
	ptr := SlabAllocatorMalloc[T](allocator)
	memcore.MemoryClearNoHeapPointers(memcore.MemcorePointerDereferenceRaw(ptr), uintptr(memcore.SizeOf[T]()))
	return ptr
}

// SlabAllocatorMallocUnsafe allocates without validation.
//
//go:nosplit
func SlabAllocatorMallocUnsafe[T any](allocator memcore.Pointer) memcore.Pointer {
	header := memcore.MemcorePointerDereferenceObjectUnsafe[FixedSlabAllocator[T]](allocator)
	idx := primitives.StackPopUnsafe[uint64](header.freeStack)

	offset := memcore.SizeOf[FixedSlabAllocator[T]]() + idx*header.slotSize
	ptr := memcore.MemcorePointerCreate(header.namespace, uintptr(offset), memcore.TypeOf[T]())
	memcore.MemcorePointerRegister(ptr)
	return ptr
}

// SlabAllocatorCallocUnsafe allocates and zeroes memory without validation.
//
//go:nosplit
func SlabAllocatorCallocUnsafe[T any](allocator memcore.Pointer) memcore.Pointer {
	ptr := SlabAllocatorMallocUnsafe[T](allocator)
	memcore.MemoryClearNoHeapPointers(memcore.MemcorePointerDereferenceRaw(ptr), uintptr(memcore.SizeOf[T]()))
	return ptr
}

// Convenience wrappers (typed access)
// They only exist for ergonomic access in Go code, and never store Go pointers.

func SlabAllocatorMallocObject[T any](allocator memcore.Pointer) *T {
	ptr := SlabAllocatorMalloc[T](allocator)
	return memcore.MemcorePointerDereferenceObjectUnsafe[T](ptr)
}

func SlabAllocatorCallocObject[T any](allocator memcore.Pointer) *T {
	ptr := SlabAllocatorCalloc[T](allocator)
	return memcore.MemcorePointerDereferenceObjectUnsafe[T](ptr)
}

func SlabAllocatorMallocObjectUnsafe[T any](allocator memcore.Pointer) *T {
	ptr := SlabAllocatorMallocUnsafe[T](allocator)
	return memcore.MemcorePointerDereferenceObjectUnsafe[T](ptr)
}

func SlabAllocatorCallocObjectUnsafe[T any](allocator memcore.Pointer) *T {
	ptr := SlabAllocatorCallocUnsafe[T](allocator)
	return memcore.MemcorePointerDereferenceObjectUnsafe[T](ptr)
}

// -------------------------------------------- PRIVATE HELPERS

//go:inline
func slabAllocatorDataIdxGet[T any](header *FixedSlabAllocator[T], idx uint64) uint64 {
	return alignIdxUp(uint64(header.dataBaseOffset)+(idx*header.slotSize), header.slotAlignment)
}
