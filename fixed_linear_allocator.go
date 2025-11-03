// Package memforge provides low-level manual memory allocators built on top of memcore.
package memforge

import (
	"fmt"
	"memcore"
	"unsafe"
)

// FixedLinearAllocator is a fixed-size bump allocator built entirely on top of memcore.Pointer.
//
// The allocator maintains a namespace containing:
//   - Its own header (this struct)
//   - A contiguous memory region (the arena)
//
// It is blazingly fast (O(1) per allocation) but not thread-safe.
// The allocator does not perform bounds tracking beyond simple linear capacity checks.
//
// ⚠️ Do NOT store Go pointers inside manually allocated memory returned by this allocator.
type FixedLinearAllocator struct {
	allocatorAddr      uintptr
	allocatorTotalSize uint64
	dataBaseOffset     uintptr
	namespace          uint32
	dataByteIdx        uint64
	dataCapBytes       uint64
}

// FixedLinearAllocatorCreate creates a FixedLinearAllocator within its own mmap region.
// Both the header and its managed memory live in the same mapped space.
//
// The allocator automatically registers a namespace for all future allocations.
// On success, it returns a `memcore.Pointer` to the allocator header itself.
func FixedLinearAllocatorCreate(sizeBytes int) memcore.Pointer {
	headerSize := memcore.SizeOf[FixedLinearAllocator]()
	headerAlignedSize := alignIdxUp(uint64(headerSize), uint64(allocatorDataAddrAlignment))

	totalSize := headerAlignedSize + uint64(sizeBytes)

	mmap, err := memcore.MemmapRequest(int(totalSize), memcore.PROT_READWRITE, memcore.MAP_ANON_PRIVATE)
	if err != nil {
		panic(fmt.Errorf("failed to create linear allocator: %w", err))
	}

	allocatorPtrRaw := unsafe.Pointer(&mmap[0])
	allocatorAddr := uintptr(allocatorPtrRaw)

	addressSpace := memcore.MemcoreAddressSpaceRegister(allocatorAddr)

	allocatorPtr := memcore.MemcorePointerCreate(addressSpace, 0, memcore.TypeOf[FixedLinearAllocator]())
	memcore.MemcorePointerRegister(allocatorPtr)

	header := memcore.MemcorePointerDereferenceObjectUnsafe[FixedLinearAllocator](allocatorPtr)
	*header = FixedLinearAllocator{
		allocatorAddr:      allocatorAddr,
		allocatorTotalSize: totalSize,
		dataBaseOffset:     uintptr(headerAlignedSize),
		namespace:          addressSpace,
		dataByteIdx:        0,
		dataCapBytes:       uint64(sizeBytes),
	}

	memforgeAllocatorRegister(allocatorPtr, "Fixed Linear (Manual)")

	return allocatorPtr
}

// FixedLinearAllocatorDestroy unmaps and unregisters the allocator and all its allocations.
// Do NOT use the allocator after calling this.
func FixedLinearAllocatorDestroy(allocator memcore.Pointer) {
	header := memcore.MemcorePointerDereferenceObjectUnsafe[FixedLinearAllocator](allocator)

	memforgeAllocatorDestroy(allocator)

	memcore.MemcoreAddressSpaceUnregister(header.namespace)

	if err := memcore.MemmapUnmapAt(unsafe.Pointer(header.allocatorAddr), int(header.allocatorTotalSize)); err != nil {
		panic(fmt.Errorf("failed to destroy allocator: %w", err))
	}
}

//go:inline
func fixedLinearAllocatorOOMError(allocator *FixedLinearAllocator, requestedSize uint64) error {
	return fmt.Errorf("fixed linear allocator: out of memory, requested: %v, available: %v, current idx: %v, cap: %v", requestedSize, allocator.dataCapBytes-allocator.dataByteIdx, allocator.dataByteIdx, allocator.dataCapBytes)
}

// FixedLinearAllocatorMalloc allocates `sizeBytes` bytes of memory with the given alignment.
// Returns a memcore.Pointer inside the allocator's namespace.
// The memory is not zeroed.
//
//go:nosplit
func FixedLinearAllocatorMalloc(allocator memcore.Pointer, sizeBytes, alignment uint64) memcore.Pointer {
	header := memcore.MemcorePointerDereferenceObjectUnsafe[FixedLinearAllocator](allocator)
	alignmentValidate(alignment)

	alignedIdx := fixedLinearAllocatorDataIdxGet(header, alignment)
	if !fixedLinearAllocatorCapacityGuarantee(header, sizeBytes, alignedIdx) {
		panic(fixedLinearAllocatorOOMError(header, sizeBytes))
	}

	offset := uintptr(alignedIdx)
	ptr := memcore.MemcorePointerCreate(header.namespace, offset, memcore.TypeOf[byte]())
	memcore.MemcorePointerRegister(ptr)

	memforgeAllocationAdd(allocator, ptr, sizeBytes)

	fixedLinearAllocatorIdxUpdate(header, alignedIdx, sizeBytes)
	return ptr
}

// FixedLinearAllocatorMallocUnsafe allocates memory without validation.
//
//go:nosplit
func FixedLinearAllocatorMallocUnsafe(allocator memcore.Pointer, sizeBytes, alignment uint64) memcore.Pointer {
	header := memcore.MemcorePointerDereferenceObjectUnsafe[FixedLinearAllocator](allocator)
	alignedIdx := fixedLinearAllocatorDataIdxGet(header, alignment)

	if !fixedLinearAllocatorCapacityGuarantee(header, sizeBytes, alignedIdx) {
		panic(fixedLinearAllocatorOOMError(header, sizeBytes))
	}

	offset := uintptr(alignedIdx)
	ptr := memcore.MemcorePointerCreate(header.namespace, offset, memcore.TypeOf[byte]())
	memcore.MemcorePointerRegister(ptr)

	memforgeAllocationAdd(allocator, ptr, sizeBytes)
	fixedLinearAllocatorIdxUpdate(header, alignedIdx, sizeBytes)

	return ptr
}

// FixedLinearAllocatorCalloc allocates and zeroes memory.
func FixedLinearAllocatorCalloc(allocator memcore.Pointer, sizeBytes, alignment uint64) memcore.Pointer {
	ptr := FixedLinearAllocatorMalloc(allocator, sizeBytes, alignment)
	memcore.MemoryClearNoHeapPointers(memcore.MemcorePointerDereferenceRaw(ptr), uintptr(sizeBytes))
	return ptr
}

// FixedLinearAllocatorCallocUnsafe allocates and zeroes memory without validation.
//
//go:nosplit
func FixedLinearAllocatorCallocUnsafe(allocator memcore.Pointer, sizeBytes, alignment uint64) memcore.Pointer {
	ptr := FixedLinearAllocatorMallocUnsafe(allocator, sizeBytes, alignment)
	memcore.MemoryClearNoHeapPointers(memcore.MemcorePointerDereferenceRaw(ptr), uintptr(sizeBytes))
	return ptr
}

// FixedLinearAllocatorMallocObject allocates and returns a typed object.
// It returns a memcore.Pointer to the object, properly aligned.
func FixedLinearAllocatorMallocObject[T any](allocator memcore.Pointer) memcore.Pointer {
	size := memcore.SizeOf[T]()
	align := memcore.AlignOf[T]()
	ptr := FixedLinearAllocatorMalloc(allocator, size, align)
	memcore.MemcorePointerUpdateType(ptr, memcore.TypeOf[T]())
	return ptr
}

// FixedLinearAllocatorCallocObject allocates a zeroed typed object.
// It returns a memcore.Pointer to the object.
func FixedLinearAllocatorCallocObject[T any](allocator memcore.Pointer) memcore.Pointer {
	size := memcore.SizeOf[T]()
	align := memcore.AlignOf[T]()
	ptr := FixedLinearAllocatorCalloc(allocator, size, align)
	memcore.MemcorePointerUpdateType(ptr, memcore.TypeOf[T]())
	return ptr
}

// FixedLinearAllocatorReset resets the allocator’s bump index,
// allowing the region to be reused.
//
// All previously allocated pointers are unregistered.
// Using them afterward is undefined behaviour.
func FixedLinearAllocatorReset(allocator memcore.Pointer) {
	header := memcore.MemcorePointerDereferenceObjectUnsafe[FixedLinearAllocator](allocator)
	memforgeAllocatorRemoveAll(allocator)
	memcore.MemcoreAddressSpaceClearPointers(header.namespace)
	memcore.MemcorePointerRegister(allocator)

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
