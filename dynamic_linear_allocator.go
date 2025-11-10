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
	regionID       uint32

	allocatorTotalSize uint64
	dataCapBytes       uint64
	dataByteIdx        uint64

	growthStrategyID memcore.FunctionID
}

// DynamicLinearAllocatorCreate creates a new dynamic allocator in its own mmap region.
//
// It allocates both the header and initial arena contiguously and registers a namespace
// for all pointers allocated within. The returned memcore.MarkRaw refers to the allocator header.
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

	memforgeAllocatorRegister(allocatorPtr, "Dynamic Linear (Manual)")

	return allocatorPtr
}

// DynamicLinearAllocatorCreateFunction creates a new dynamic allocator in its own mmap region.
// This variant automatically registers the growth strategy.
//
// It allocates both the header and initial arena contiguously and registers a namespace
// for all pointers allocated within. The returned memcore.MarkRaw refers to the allocator header.
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

	memforgeAllocatorRegister(allocatorPtr, "Dynamic Linear (Manual)")

	return allocatorPtr
}

// DynamicLinearAllocatorDestroy releases all memory and unregisters all pointers.
// After this call, the allocator is invalid and may not be reused.
func DynamicLinearAllocatorDestroy(allocator memcore.MarkRaw) {
	header := memcore.MemcoreMarkDereferenceObject[DynamicLinearAllocator](allocator)

	memforgeAllocatorDestroy(allocator)
	memcore.MemcoreRegionUnregister(header.regionID)

	if err := memcore.MemmapUnmapAt(unsafe.Pointer(header.allocatorAddr), int(header.allocatorTotalSize)); err != nil {
		panic(fmt.Errorf("failed to destroy dynamic allocator: %w", err))
	}
}

// DynamicLinearAllocatorMalloc allocates a block of memory from the dynamic allocator.
// Returns a memcore.MarkRaw inside the allocator’s namespace.
//
//go:nosplit
func DynamicLinearAllocatorMalloc(allocator memcore.MarkRaw, sizeBytes, alignment uint64) memcore.MarkRaw {
	header := memcore.MemcoreMarkDereferenceObject[DynamicLinearAllocator](allocator)
	alignmentValidate(alignment)

	alignedIdx := dynamicLinearAllocatorDataIdxGet(header, alignment)
	if !dynamicLinearAllocatorCapacityGuarantee(header, sizeBytes, alignedIdx) {
		header = dynamicLinearAllocatorGrow(header, alignedIdx+sizeBytes)
	}

	offset := uintptr(alignedIdx)
	ptr := memcore.MemcoreMarkOffsetFrom(allocator, offset)

	memforgeAllocationAdd(allocator, ptr, sizeBytes)
	dynamicLinearAllocatorIdxUpdate(header, alignedIdx, sizeBytes)
	return ptr
}

// DynamicLinearAllocatorMallocUnsafe allocates memory without validation.
//
//go:nosplit
func DynamicLinearAllocatorMallocUnsafe(allocator memcore.MarkRaw, sizeBytes, alignment uint64) memcore.MarkRaw {
	header := memcore.MemcoreMarkDereferenceObject[DynamicLinearAllocator](allocator)

	alignedIdx := dynamicLinearAllocatorDataIdxGet(header, alignment)
	if !dynamicLinearAllocatorCapacityGuarantee(header, sizeBytes, alignedIdx) {
		header = dynamicLinearAllocatorGrow(header, alignedIdx+sizeBytes)
	}

	offset := uintptr(alignedIdx)
	ptr := memcore.MemcoreMarkOffsetFrom(allocator, offset)

	memforgeAllocationAdd(allocator, ptr, sizeBytes)
	dynamicLinearAllocatorIdxUpdate(header, alignedIdx, sizeBytes)
	return ptr
}

// DynamicLinearAllocatorCalloc allocates and zeroes memory.
//
//go:nosplit
func DynamicLinearAllocatorCalloc(allocator memcore.MarkRaw, sizeBytes, alignment uint64) memcore.MarkRaw {
	ptr := DynamicLinearAllocatorMalloc(allocator, sizeBytes, alignment)
	memcore.MemoryClearNoHeapPointers(memcore.MemcoreMarkDereference(ptr), uintptr(sizeBytes))
	return ptr
}

// DynamicLinearAllocatorCallocUnsafe allocates and zeroes memory without validation.
//
//go:nosplit
func DynamicLinearAllocatorCallocUnsafe(allocator memcore.MarkRaw, sizeBytes, alignment uint64) memcore.MarkRaw {
	ptr := DynamicLinearAllocatorMallocUnsafe(allocator, sizeBytes, alignment)
	memcore.MemoryClearNoHeapPointers(memcore.MemcoreMarkDereference(ptr), uintptr(sizeBytes))
	return ptr
}

// DynamicLinearAllocatorMallocObject allocates a typed object.
// It also returns the allocation interpreted as *T which is not safe to store inside manually allocated memory.
func DynamicLinearAllocatorMallocObject[T any](allocator memcore.MarkRaw) (memcore.MarkRaw, *T) {
	size := memcore.SizeOf[T]()
	align := memcore.AlignOf[T]()
	ptr := DynamicLinearAllocatorMalloc(allocator, size, align)
	return ptr, memcore.MemcoreMarkDereferenceObject[T](ptr)
}

// DynamicLinearAllocatorCallocObject allocates a zeroed typed object.
// It also returns the allocation interpreted as *T which is not safe to store inside manually allocated memory.
func DynamicLinearAllocatorCallocObject[T any](allocator memcore.MarkRaw) (memcore.MarkRaw, *T) {
	size := memcore.SizeOf[T]()
	align := memcore.AlignOf[T]()
	ptr := DynamicLinearAllocatorCalloc(allocator, size, align)
	return ptr, memcore.MemcoreMarkDereferenceObject[T](ptr)
}

// DynamicLinearAllocatorReset resets the allocator, allowing reuse.
// All pointers within the namespace are unregistered.
// Using them afterward is undefined behaviour.
func DynamicLinearAllocatorReset(allocator memcore.MarkRaw) {
	header := memcore.MemcoreMarkDereferenceObject[DynamicLinearAllocator](allocator)
	memforgeAllocatorRemoveAll(allocator)
	header.dataByteIdx = 0
}

// -------------------------- PRIVATE HELPERS --------------------------

//go:inline
func dynamicLinearAllocatorGrow(header *DynamicLinearAllocator, neededCapacityBytes uint64) *DynamicLinearAllocator {
	prev := *header

	strategy := memcore.MemcoreFunctionRetrieveTyped[GrowthStrategy](prev.growthStrategyID)
	newSize := strategy(prev.dataCapBytes, neededCapacityBytes)
	if newSize < neededCapacityBytes {
		panic("growth strategy returned invalid new size")
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
