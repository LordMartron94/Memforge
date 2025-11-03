package memforge

import (
	"fmt"
	"memcore"
	"unsafe"
)

// GrowthStrategy determines how the dynamic allocator expands.
// currentCap = current capacity, neededCap = new required capacity.
type GrowthStrategy func(currentCap, neededCap uint64) uint64

// DynamicLinearAllocator is a dynamic bump allocator with namespace isolation.
// It owns its own namespace and can grow via Mremap.
type DynamicLinearAllocator struct {
	allocatorAddr  uintptr
	dataBaseOffset uintptr
	namespace      uint32

	allocatorTotalSize uint64
	dataCapBytes       uint64
	dataByteIdx        uint64

	growthStrategy GrowthStrategy
}

// DynamicLinearAllocatorCreate creates a new dynamic allocator in its own mmap region.
//
// It allocates both the header and initial arena contiguously and registers a namespace
// for all pointers allocated within. The returned memcore.Pointer refers to the allocator header.
func DynamicLinearAllocatorCreate(initialCapacityBytes uint64, growthStrategy GrowthStrategy) memcore.Pointer {
	if growthStrategy == nil {
		panic("DynamicLinearAllocatorCreate: a growth strategy must be provided")
	}

	headerSize := memcore.SizeOf[DynamicLinearAllocator]()
	headerAlignedSize := alignIdxUp(headerSize, uint64(allocatorDataAddrAlignment))

	totalSize := initialCapacityBytes + headerAlignedSize

	mmap, err := memcore.MemmapRequest(int(totalSize), memcore.PROT_READWRITE, memcore.MAP_ANON_PRIVATE)
	if err != nil {
		panic(fmt.Errorf("failed to create dynamic allocator: %w", err))
	}

	allocatorAddr := uintptr(unsafe.Pointer(&mmap[0]))
	namespace := memcore.MemcoreAddressSpaceRegister(allocatorAddr)

	allocatorPtr := memcore.MemcorePointerCreate(namespace, 0, memcore.TypeOf[DynamicLinearAllocator]())
	memcore.MemcorePointerRegister(allocatorPtr)

	header := memcore.MemcorePointerDereferenceObjectUnsafe[DynamicLinearAllocator](allocatorPtr)
	*header = DynamicLinearAllocator{
		allocatorAddr:      allocatorAddr,
		dataBaseOffset:     uintptr(headerAlignedSize),
		namespace:          namespace,
		allocatorTotalSize: totalSize,
		dataCapBytes:       initialCapacityBytes,
		dataByteIdx:        0,
		growthStrategy:     growthStrategy,
	}

	memforgeAllocatorRegister(allocatorPtr, "Dynamic Linear (Manual)")

	return allocatorPtr
}

// DynamicLinearAllocatorDestroy releases all memory and unregisters all pointers.
// After this call, the allocator is invalid and may not be reused.
func DynamicLinearAllocatorDestroy(allocator memcore.Pointer) {
	header := memcore.MemcorePointerDereferenceObjectUnsafe[DynamicLinearAllocator](allocator)

	memforgeAllocatorDestroy(allocator)
	memcore.MemcoreAddressSpaceUnregister(header.namespace)

	if err := memcore.MemmapUnmapAt(unsafe.Pointer(header.allocatorAddr), int(header.allocatorTotalSize)); err != nil {
		panic(fmt.Errorf("failed to destroy dynamic allocator: %w", err))
	}
}

// DynamicLinearAllocatorMalloc allocates a block of memory from the dynamic allocator.
// Returns a memcore.Pointer inside the allocator’s namespace.
//
//go:nosplit
func DynamicLinearAllocatorMalloc(allocator memcore.Pointer, sizeBytes, alignment uint64) memcore.Pointer {
	header := memcore.MemcorePointerDereferenceObjectUnsafe[DynamicLinearAllocator](allocator)
	alignmentValidate(alignment)

	alignedIdx := dynamicLinearAllocatorDataIdxGet(header, alignment)
	if !capacityGuarantee(alignedIdx, header.dataCapBytes, sizeBytes) {
		dynamicLinearAllocatorGrow(header, alignedIdx+sizeBytes)
	}

	offset := uintptr(alignedIdx)
	ptr := memcore.MemcorePointerCreate(header.namespace, offset, memcore.TypeOf[byte]())
	memcore.MemcorePointerRegister(ptr)

	memforgeAllocationAdd(allocator, ptr, sizeBytes)
	dynamicLinearAllocatorIdxUpdate(header, alignedIdx, sizeBytes)
	return ptr
}

// DynamicLinearAllocatorMallocUnsafe allocates memory without validation.
//
//go:nosplit
func DynamicLinearAllocatorMallocUnsafe(allocator memcore.Pointer, sizeBytes, alignment uint64) memcore.Pointer {
	header := memcore.MemcorePointerDereferenceObjectUnsafe[DynamicLinearAllocator](allocator)

	alignedIdx := dynamicLinearAllocatorDataIdxGet(header, alignment)
	if !capacityGuarantee(alignedIdx, header.dataCapBytes, sizeBytes) {
		dynamicLinearAllocatorGrow(header, alignedIdx+sizeBytes)
	}

	offset := uintptr(alignedIdx)
	ptr := memcore.MemcorePointerCreate(header.namespace, offset, memcore.TypeOf[byte]())
	memcore.MemcorePointerRegister(ptr)

	memforgeAllocationAdd(allocator, ptr, sizeBytes)
	dynamicLinearAllocatorIdxUpdate(header, alignedIdx, sizeBytes)
	return ptr
}

// DynamicLinearAllocatorCalloc allocates and zeroes memory.
//
//go:nosplit
func DynamicLinearAllocatorCalloc(allocator memcore.Pointer, sizeBytes, alignment uint64) memcore.Pointer {
	ptr := DynamicLinearAllocatorMalloc(allocator, sizeBytes, alignment)
	memcore.MemoryClearNoHeapPointers(memcore.MemcorePointerDereferenceRaw(ptr), uintptr(sizeBytes))
	return ptr
}

// DynamicLinearAllocatorCallocUnsafe allocates and zeroes memory without validation.
//
//go:nosplit
func DynamicLinearAllocatorCallocUnsafe(allocator memcore.Pointer, sizeBytes, alignment uint64) memcore.Pointer {
	ptr := DynamicLinearAllocatorMallocUnsafe(allocator, sizeBytes, alignment)
	memcore.MemoryClearNoHeapPointers(memcore.MemcorePointerDereferenceRaw(ptr), uintptr(sizeBytes))
	return ptr
}

// DynamicLinearAllocatorMallocObject allocates a typed object.
//
//go:nosplit
func DynamicLinearAllocatorMallocObject[T any](allocator memcore.Pointer) memcore.Pointer {
	size := memcore.SizeOf[T]()
	align := memcore.AlignOf[T]()
	ptr := DynamicLinearAllocatorMalloc(allocator, size, align)
	memcore.MemcorePointerUpdateType(ptr, memcore.TypeOf[T]())
	return ptr
}

// DynamicLinearAllocatorCallocObject allocates a zeroed typed object.
//
//go:nosplit
func DynamicLinearAllocatorCallocObject[T any](allocator memcore.Pointer) memcore.Pointer {
	size := memcore.SizeOf[T]()
	align := memcore.AlignOf[T]()
	ptr := DynamicLinearAllocatorCalloc(allocator, size, align)
	memcore.MemcorePointerUpdateType(ptr, memcore.TypeOf[T]())
	return ptr
}

// DynamicLinearAllocatorReset resets the allocator, allowing reuse.
// All pointers within the namespace are unregistered.
// Using them afterward is undefined behaviour.
func DynamicLinearAllocatorReset(allocator memcore.Pointer) {
	header := memcore.MemcorePointerDereferenceObjectUnsafe[DynamicLinearAllocator](allocator)
	memforgeAllocatorRemoveAll(allocator)
	memcore.MemcoreAddressSpaceClearPointers(header.namespace)
	memcore.MemcorePointerRegister(allocator)
	header.dataByteIdx = 0
}

// -------------------------- PRIVATE HELPERS --------------------------

//go:inline
func dynamicLinearAllocatorGrow(header *DynamicLinearAllocator, neededCapacityBytes uint64) {
	newSize := header.growthStrategy(header.dataCapBytes, neededCapacityBytes)
	if newSize < neededCapacityBytes {
		panic("growth strategy returned invalid new size")
	}

	newMap, err := memcore.MemmapRemapAt(
		unsafe.Pointer(header.allocatorAddr),
		int(header.dataCapBytes+memcore.SizeOf[DynamicLinearAllocator]()),
		int(newSize+memcore.SizeOf[DynamicLinearAllocator]()),
		memcore.MREMAP_MAYMOVE,
	)
	if err != nil {
		panic(fmt.Errorf("dynamic allocator: could not grow memory: %w", err))
	}

	header.allocatorAddr = uintptr(unsafe.Pointer(&newMap[0]))
	header.dataCapBytes = newSize

	// Reflect updated base address in the namespace mapping
	memcore.MemcorePointerBaseAddressUpdate(header.namespace, header.allocatorAddr)
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
