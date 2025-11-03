package memforge

import (
	"fmt"
	"memcore"
	"memcore/primitives"
	"unsafe"
)

const allocatorFailureCountResetHeuristic uint8 = 5

// allocationRecord stores metadata for a single active allocation.
type allocationRecord struct {
	ptr       memcore.Pointer
	sizeBytes uint64
}

// freeMemoryRegionBlock represents a contiguous region of unallocated memory.
type freeMemoryRegionBlock struct {
	memStartIdx uint64 // Byte index of region start
	sizeBytes   uint64 // Total size in bytes
}

// FixedManualAllocator provides low-level manual allocation/freeing
// within a fixed-size memory-mapped region. It uses `memcore.Pointer`
// as the single authoritative representation of all allocations.
//
// Characteristics:
//   - No runtime GC overhead
//   - Deterministic layout
//   - Stable pointers (no relocation)
//   - Manual allocation and freeing
//   - Not thread-safe
type FixedManualAllocator struct {
	allocatorAddr       uintptr
	allocatorRegionSize uint64
	dataRegionOffset    uintptr
	addressSpace        uint32
	storage             memcore.MemoryMap

	metadataAllocator memcore.Pointer // FixedLinearAllocator
	freeMemory        memcore.Pointer // FixedOrderedList[freeMemoryRegionBlock]
	ptrRefs           memcore.Pointer // FixedOrderedList[allocationRecord]

	dataBytesCap          uint64
	regionIdxAreaHint     uint64
	regionIdxFailureCount uint8
}

// ---------------------------------- CREATION & DESTRUCTION ----------------------------------

// FixedManualAllocatorCreate creates a new manual allocator managing
// `sizeBytes` of memory.
func FixedManualAllocatorCreate(sizeBytes uint64) memcore.Pointer {
	headerSize := memcore.SizeOf[FixedManualAllocator]()
	headerAlignedSize := alignIdxUp(uint64(headerSize), uint64(allocatorDataAddrAlignment))

	totalSize := headerAlignedSize + uint64(sizeBytes)

	mmap, err := memcore.MemmapRequest(int(totalSize), memcore.PROT_READWRITE, memcore.MAP_ANON_PRIVATE)
	if err != nil {
		panic(fmt.Errorf("manual allocator: mmap failed: %w", err))
	}

	allocatorPtrRaw := unsafe.Pointer(&mmap[0])
	allocatorAddr := uintptr(allocatorPtrRaw)

	addressSpace := memcore.MemcoreAddressSpaceRegister(allocatorAddr)

	// Allocate and register allocator header pointer
	allocatorPtr := memcore.MemcorePointerCreate(addressSpace, 0, memcore.TypeOf[FixedManualAllocator]())
	memcore.MemcorePointerRegister(allocatorPtr)
	allocator := memcore.MemcorePointerDereferenceObjectUnsafe[FixedManualAllocator](allocatorPtr)

	// Determine metadata capacity heuristics
	minTrack := memcore.SizeOf[uintptr]()
	if sizeBytes < uint64(minTrack) {
		sizeBytes = uint64(minTrack)
	}
	maxAllocs := sizeBytes / uint64(minTrack)

	// Allocate metadata arena
	ptrRefBytes := alignIdxUp(primitives.FixedOrderedListRequiredBytes[allocationRecord](maxAllocs), primitives.FixedOrderedListRequiredAlignment[allocationRecord]())
	freeListBytes := alignIdxUp(primitives.FixedOrderedListRequiredBytes[freeMemoryRegionBlock](maxAllocs), primitives.FixedOrderedListRequiredAlignment[freeMemoryRegionBlock]())
	metaBytes := ptrRefBytes + freeListBytes
	metaAlloc := FixedLinearAllocatorCreate(int(metaBytes))

	// Allocate list memory
	ptrRefsPtr := FixedLinearAllocatorMalloc(metaAlloc, ptrRefBytes, memcore.AlignOf[primitives.FixedOrderedList[allocationRecord]]())
	memcore.MemcorePointerUpdateType(ptrRefsPtr, memcore.TypeOf[primitives.FixedOrderedList[allocationRecord]]())

	freeListPtr := FixedLinearAllocatorMalloc(metaAlloc, freeListBytes, memcore.AlignOf[primitives.FixedOrderedList[freeMemoryRegionBlock]]())
	memcore.MemcorePointerUpdateType(freeListPtr, memcore.TypeOf[primitives.FixedOrderedList[freeMemoryRegionBlock]]())

	// Initialize metadata lists
	primitives.FixedOrderedListInitializeAt[allocationRecord](ptrRefsPtr, maxAllocs)
	primitives.FixedOrderedListInitializeAt[freeMemoryRegionBlock](freeListPtr, maxAllocs)

	// Initialize full free region
	primitives.FixedOrderedListAppendUnsafe(freeListPtr, freeMemoryRegionBlock{
		memStartIdx: 0,
		sizeBytes:   sizeBytes,
	})

	// Write allocator header
	*allocator = FixedManualAllocator{
		allocatorAddr:         allocatorAddr,
		dataRegionOffset:      uintptr(headerAlignedSize),
		allocatorRegionSize:   totalSize,
		addressSpace:          addressSpace,
		storage:               mmap,
		metadataAllocator:     metaAlloc,
		freeMemory:            freeListPtr,
		ptrRefs:               ptrRefsPtr,
		dataBytesCap:          sizeBytes,
		regionIdxAreaHint:     0,
		regionIdxFailureCount: 0,
	}

	memforgeAllocatorRegister(allocatorPtr, "Fixed Manual (Pointer)")
	return allocatorPtr
}

// FixedManualAllocatorDestroy releases all memory associated with the allocator,
// unregistering its namespace and invalidating all pointers created from it.
//
// Using the allocator after destruction will panic.
func FixedManualAllocatorDestroy(allocator memcore.Pointer) {
	header := memcore.MemcorePointerDereferenceObjectUnsafe[FixedManualAllocator](allocator)

	memforgeAllocatorDestroy(allocator)

	primitives.FixedOrderedListDestroy[freeMemoryRegionBlock](header.freeMemory)
	primitives.FixedOrderedListDestroy[allocationRecord](header.ptrRefs)

	memcore.MemcoreAddressSpaceUnregister(header.addressSpace)

	FixedLinearAllocatorDestroy(header.metadataAllocator)

	if err := memcore.MemmapUnmapAt(unsafe.Pointer(header.allocatorAddr), int(header.allocatorRegionSize)); err != nil {
		panic(fmt.Errorf("manual allocator: unmap failed: %w", err))
	}
}

// ---------------------------------- ALLOCATION ----------------------------------

// FixedManualAllocatorMalloc allocates a manually managed memory block of
// `sizeBytes` and `alignment`, returning a `memcore.Pointer` to it.
// The memory is uninitialized and may contain garbage.
//
// Panics if insufficient free space exists.
func FixedManualAllocatorMalloc(allocator memcore.Pointer, sizeBytes, alignment uint64) memcore.Pointer {
	header := memcore.MemcorePointerDereferenceObjectUnsafe[FixedManualAllocator](allocator)
	alignmentValidate(alignment)

	regionIdx, alignedIdx, spaceBefore, spaceAfter, err := getFreeAlignedIdx(header, sizeBytes, alignment)
	if err != nil {
		panic(fmt.Errorf("manual allocator: out of memory: %w", err))
	}
	updateFreeListAfterAllocation(header, regionIdx, alignedIdx, sizeBytes, spaceBefore, spaceAfter)

	offset := uintptr(alignedIdx)
	ptr := memcore.MemcorePointerCreate(header.addressSpace, header.dataRegionOffset+offset, memcore.TypeOf[byte]())
	memcore.MemcorePointerRegister(ptr)
	memforgeAllocationAdd(allocator, ptr, sizeBytes)
	insertAllocationRecord(header, ptr, sizeBytes)
	return ptr
}

// FixedManualAllocatorMallocUnsafe is identical to Malloc but skips alignment validation.
func FixedManualAllocatorMallocUnsafe(allocator memcore.Pointer, sizeBytes, alignment uint64) memcore.Pointer {
	header := memcore.MemcorePointerDereferenceObjectUnsafe[FixedManualAllocator](allocator)
	regionIdx, alignedIdx, spaceBefore, spaceAfter, err := getFreeAlignedIdx(header, sizeBytes, alignment)
	if err != nil {
		panic(fmt.Errorf("manual allocator: out of memory: %w", err))
	}
	updateFreeListAfterAllocation(header, regionIdx, alignedIdx, sizeBytes, spaceBefore, spaceAfter)
	offset := uintptr(alignedIdx)
	ptr := memcore.MemcorePointerCreate(header.addressSpace, header.dataRegionOffset+offset, memcore.TypeOf[byte]())
	memcore.MemcorePointerRegister(ptr)
	memforgeAllocationAdd(allocator, ptr, sizeBytes)
	insertAllocationRecord(header, ptr, sizeBytes)
	return ptr
}

// FixedManualAllocatorCalloc allocates and zeroes a block of memory.
func FixedManualAllocatorCalloc(allocator memcore.Pointer, sizeBytes, alignment uint64) memcore.Pointer {
	ptr := FixedManualAllocatorMalloc(allocator, sizeBytes, alignment)
	memcore.MemoryClearNoHeapPointers(memcore.MemcorePointerDereferenceRaw(ptr), uintptr(sizeBytes))
	return ptr
}

// FixedManualAllocatorCallocUnsafe allocates and zeroes a block without alignment validation.
func FixedManualAllocatorCallocUnsafe(allocator memcore.Pointer, sizeBytes, alignment uint64) memcore.Pointer {
	ptr := FixedManualAllocatorMallocUnsafe(allocator, sizeBytes, alignment)
	memcore.MemoryClearNoHeapPointers(memcore.MemcorePointerDereferenceRaw(ptr), uintptr(sizeBytes))
	return ptr
}

// FixedManualAllocatorMallocObject allocates and returns a typed object.
// It returns a memcore.Pointer to the object, properly aligned.
func FixedManualAllocatorMallocObject[T any](allocator memcore.Pointer) memcore.Pointer {
	size := memcore.SizeOf[T]()
	align := memcore.AlignOf[T]()
	ptr := FixedManualAllocatorMalloc(allocator, size, align)
	memcore.MemcorePointerUpdateType(ptr, memcore.TypeOf[T]())
	return ptr
}

// FixedManualAllocatorCallocObject allocates a zeroed typed object.
// It returns a memcore.Pointer to the object.
func FixedManualAllocatorCallocObject[T any](allocator memcore.Pointer) memcore.Pointer {
	size := memcore.SizeOf[T]()
	align := memcore.AlignOf[T]()
	ptr := FixedManualAllocatorCalloc(allocator, size, align)
	memcore.MemcorePointerUpdateType(ptr, memcore.TypeOf[T]())
	return ptr
}

// ---------------------------------- FREE ----------------------------------

// FixedManualAllocatorFree releases a previously allocated block and merges
// it with adjacent free regions if possible. Panics if the pointer was not
// allocated by this allocator.
func FixedManualAllocatorFree(allocator memcore.Pointer, target memcore.Pointer) {
	header := memcore.MemcorePointerDereferenceObjectUnsafe[FixedManualAllocator](allocator)
	rec, refIdx := fixedManualAllocatorFindRef(header, target)
	prevIdx, nextIdx := fixedManualAllocatorFindAdjacentRegions(header, rec)

	fixedManualAllocatorMergeOrInsert(header, rec, prevIdx, nextIdx)
	primitives.FixedOrderedListDeleteUnsafe[allocationRecord](header.ptrRefs, refIdx)
	memforgeAllocationRemove(allocator, target)
	memcore.MemcorePointerUnregister(target)
}

// ---------------------------------- RESET ----------------------------------

// FixedManualAllocatorReset clears all allocations, restoring the entire
// region as a single free block. All previously returned pointers become invalid.
func FixedManualAllocatorReset(allocator memcore.Pointer) {
	header := memcore.MemcorePointerDereferenceObjectUnsafe[FixedManualAllocator](allocator)

	memforgeAllocatorRemoveAll(allocator)
	memcore.MemcoreAddressSpaceClearPointers(header.addressSpace)
	memcore.MemcorePointerRegister(allocator)

	primitives.FixedOrderedListClear[freeMemoryRegionBlock](header.freeMemory)
	primitives.FixedOrderedListClear[allocationRecord](header.ptrRefs)
	primitives.FixedOrderedListAppendUnsafe(header.freeMemory, freeMemoryRegionBlock{
		memStartIdx: 0,
		sizeBytes:   header.dataBytesCap,
	})
	header.regionIdxAreaHint = 0
	header.regionIdxFailureCount = 0
}

// ---------------------------------- PRIVATE HELPERS ----------------------------------

// Insert a new record into ptrRefs.
func insertAllocationRecord(a *FixedManualAllocator, ptr memcore.Pointer, size uint64) {
	rec := allocationRecord{ptr: ptr, sizeBytes: size}
	insertIdx := primitives.FixedOrderedListBinarySearchInsertionPoint(a.ptrRefs, func(item allocationRecord) int8 {
		if fixedManualAllocatorGetIdxRelativeToDataRegion(a, item.ptr) < fixedManualAllocatorGetIdxRelativeToDataRegion(a, ptr) {
			return -1
		}
		return 1
	})
	if err := primitives.FixedOrderedListInsertAt(a.ptrRefs, insertIdx, rec); err != nil {
		panic("manual allocator: pointer reference table overflow")
	}
}

// Find a record by pointer.
func fixedManualAllocatorFindRef(a *FixedManualAllocator, target memcore.Pointer) (*allocationRecord, uint64) {
	refIdx, err := primitives.FixedOrderedListBinarySearch(a.ptrRefs, func(item allocationRecord) int8 {
		switch {
		case fixedManualAllocatorGetIdxRelativeToDataRegion(a, item.ptr) < fixedManualAllocatorGetIdxRelativeToDataRegion(a, target):
			return -1
		case fixedManualAllocatorGetIdxRelativeToDataRegion(a, item.ptr) == fixedManualAllocatorGetIdxRelativeToDataRegion(a, target):
			return 0
		default:
			return 1
		}
	})
	if err != nil {
		panic(fmt.Errorf("manual allocator: unknown pointer: %s", target))
	}
	return primitives.FixedOrderedListItemPtrGetAtUnsafe[allocationRecord](a.ptrRefs, refIdx), refIdx
}

// Find free regions adjacent to an allocation.
func fixedManualAllocatorFindAdjacentRegions(a *FixedManualAllocator, ref *allocationRecord) (uint64, uint64) {
	return primitives.FixedOrderedListBinarySearchInterval(a.freeMemory, func(item freeMemoryRegionBlock) int8 {
		if item.memStartIdx < uint64(fixedManualAllocatorGetIdxRelativeToDataRegion(a, ref.ptr)) {
			return -1
		}
		return 1
	})
}

// Merge or insert free region during deallocation.
func fixedManualAllocatorMergeOrInsert(a *FixedManualAllocator, ref *allocationRecord, prevIdx, nextIdx uint64) {
	hasPrev := primitives.FixedOrderedListIsIdxValid[freeMemoryRegionBlock](a.freeMemory, prevIdx)
	hasNext := primitives.FixedOrderedListIsIdxValid[freeMemoryRegionBlock](a.freeMemory, nextIdx)
	switch {
	case hasPrev && hasNext:
		fixedManualAllocatorMergePrevNext(a, ref, prevIdx, nextIdx)
	case hasPrev:
		fixedManualAllocatorMergePrev(a, ref, prevIdx, nextIdx)
	case hasNext:
		fixedManualAllocatorMergeNext(a, ref, nextIdx)
	default:
		pushFreePointer(a, ref, nextIdx)
	}
}

func fixedManualAllocatorMergePrevNext(a *FixedManualAllocator, ref *allocationRecord, prevIdx, nextIdx uint64) {
	prev := primitives.FixedOrderedListItemPtrGetAtUnsafe[freeMemoryRegionBlock](a.freeMemory, prevIdx)
	next := primitives.FixedOrderedListItemPtrGetAtUnsafe[freeMemoryRegionBlock](a.freeMemory, nextIdx)
	canPrev := canMergeRegions(prev.memStartIdx, prev.sizeBytes, uint64(fixedManualAllocatorGetIdxRelativeToDataRegion(a, ref.ptr)))
	canNext := canMergeRegions(uint64(fixedManualAllocatorGetIdxRelativeToDataRegion(a, ref.ptr)), ref.sizeBytes, next.memStartIdx)

	switch {
	case canPrev && canNext:
		size := prev.sizeBytes + ref.sizeBytes + next.sizeBytes
		updateFreeRegion(a, prevIdx, prev.memStartIdx, size)
		primitives.FixedOrderedListDeleteUnsafe[freeMemoryRegionBlock](a.freeMemory, nextIdx)
	case canPrev:
		fixedManualAllocatorMergePrev(a, ref, prevIdx, nextIdx)
	case canNext:
		fixedManualAllocatorMergeNext(a, ref, nextIdx)
	default:
		pushFreePointer(a, ref, nextIdx)
	}
}

func fixedManualAllocatorMergePrev(a *FixedManualAllocator, ref *allocationRecord, prevIdx, nextIdx uint64) {
	prev := primitives.FixedOrderedListItemPtrGetAtUnsafe[freeMemoryRegionBlock](a.freeMemory, prevIdx)
	if !canMergeRegions(prev.memStartIdx, prev.sizeBytes, uint64(fixedManualAllocatorGetIdxRelativeToDataRegion(a, ref.ptr))) {
		pushFreePointer(a, ref, nextIdx)
		return
	}
	updateFreeRegion(a, prevIdx, prev.memStartIdx, prev.sizeBytes+ref.sizeBytes)
}

func fixedManualAllocatorMergeNext(a *FixedManualAllocator, ref *allocationRecord, nextIdx uint64) {
	next := primitives.FixedOrderedListItemPtrGetAtUnsafe[freeMemoryRegionBlock](a.freeMemory, nextIdx)
	if !canMergeRegions(uint64(fixedManualAllocatorGetIdxRelativeToDataRegion(a, ref.ptr)), ref.sizeBytes, next.memStartIdx) {
		pushFreePointer(a, ref, nextIdx)
		return
	}
	updateFreeRegion(a, nextIdx, uint64(fixedManualAllocatorGetIdxRelativeToDataRegion(a, ref.ptr)), ref.sizeBytes+next.sizeBytes)
}

func updateFreeRegion(a *FixedManualAllocator, mdIdx, memIdx, memSize uint64) {
	blk := primitives.FixedOrderedListItemPtrGetAtUnsafe[freeMemoryRegionBlock](a.freeMemory, mdIdx)
	blk.memStartIdx = memIdx
	blk.sizeBytes = memSize
	if mdIdx < a.regionIdxAreaHint {
		a.regionIdxAreaHint = mdIdx
	}
}

func pushFreePointer(a *FixedManualAllocator, ref *allocationRecord, mdIdx uint64) {
	newBlk := freeMemoryRegionBlock{memStartIdx: uint64(fixedManualAllocatorGetIdxRelativeToDataRegion(a, ref.ptr)), sizeBytes: ref.sizeBytes}
	if err := primitives.FixedOrderedListInsertAt(a.freeMemory, mdIdx, newBlk); err != nil {
		panic("manual allocator: metadata overflow")
	}
	if mdIdx < a.regionIdxAreaHint {
		a.regionIdxAreaHint = mdIdx
	}
}

func canMergeRegions(aIdx, aSize, bIdx uint64) bool { return aIdx+aSize == bIdx }

// Search utilities for free regions
func getFreeAlignedIdx(a *FixedManualAllocator, requestedSize, requestedAlignment uint64) (uint64, uint64, uint64, uint64, error) {
	regionAmount := primitives.FixedOrderedListLengthGet[freeMemoryRegionBlock](a.freeMemory)
	idx, alignedIdx, spaceBefore, spaceAfter, err := freeAlignedIdxLoop(a.regionIdxAreaHint, regionAmount, a, requestedSize, requestedAlignment)
	if err == nil {
		return idx, alignedIdx, spaceBefore, spaceAfter, nil
	}
	a.regionIdxFailureCount++
	if a.regionIdxFailureCount > allocatorFailureCountResetHeuristic {
		a.regionIdxFailureCount = 0
		a.regionIdxAreaHint = 0
	}
	start := uint64(0)
	end := a.regionIdxAreaHint
	if end > regionAmount {
		end = regionAmount
	}
	return freeAlignedIdxLoop(start, end, a, requestedSize, requestedAlignment)
}

func updateFreeListAfterAllocation(
	a *FixedManualAllocator,
	regionIdx, alignedIdx, sizeBytes, spaceBefore, spaceAfter uint64,
) {
	switch {
	case spaceBefore == 0 && spaceAfter == 0:
		// Entire region consumed; remove from free list.
		primitives.FixedOrderedListDeleteUnsafe[freeMemoryRegionBlock](a.freeMemory, regionIdx)
		if a.regionIdxAreaHint > regionIdx && a.regionIdxAreaHint != 0 {
			a.regionIdxAreaHint--
		}

	case spaceBefore == 0 && spaceAfter > 0:
		// Allocation at the start; shrink region from the left.
		ptr := primitives.FixedOrderedListItemPtrGetAtUnsafe[freeMemoryRegionBlock](a.freeMemory, regionIdx)
		ptr.memStartIdx = alignedIdx + sizeBytes
		ptr.sizeBytes = spaceAfter

	case spaceBefore > 0 && spaceAfter == 0:
		// Allocation at the end; shrink region from the right.
		ptr := primitives.FixedOrderedListItemPtrGetAtUnsafe[freeMemoryRegionBlock](a.freeMemory, regionIdx)
		ptr.memStartIdx = alignedIdx - spaceBefore
		ptr.sizeBytes = spaceBefore

	case spaceBefore > 0 && spaceAfter > 0:
		// Allocation splits the region into two parts.
		ptr := primitives.FixedOrderedListItemPtrGetAtUnsafe[freeMemoryRegionBlock](a.freeMemory, regionIdx)
		ptr.memStartIdx = alignedIdx - spaceBefore
		ptr.sizeBytes = spaceBefore

		// Insert the second half immediately after.
		newBlock := freeMemoryRegionBlock{
			memStartIdx: alignedIdx + sizeBytes,
			sizeBytes:   spaceAfter,
		}
		if err := primitives.FixedOrderedListInsertAt(a.freeMemory, regionIdx+1, newBlock); err != nil {
			panic(fmt.Errorf("manual allocator: free list insertion failed: %w", err))
		}

		if regionIdx < a.regionIdxAreaHint {
			a.regionIdxAreaHint++ // shift area hint if elements moved
		}
	}
}

func freeAlignedIdxLoop(
	startIdx, lastIdxExclusive uint64,
	a *FixedManualAllocator,
	requestedSize, requestedAlignment uint64,
) (uint64, uint64, uint64, uint64, error) {
	for i := startIdx; i < lastIdxExclusive; i++ {
		region := primitives.FixedOrderedListItemPtrGetAtUnsafe[freeMemoryRegionBlock](a.freeMemory, i)
		regionAlignedIdx := alignIdxUp(region.memStartIdx, requestedAlignment)

		if regionAlignedIdx < region.memStartIdx {
			continue
		}

		spaceBefore := regionAlignedIdx - region.memStartIdx
		if spaceBefore > region.sizeBytes {
			continue
		}

		adjustedSize := region.sizeBytes - spaceBefore
		if adjustedSize >= requestedSize {
			spaceAfter := adjustedSize - requestedSize
			a.regionIdxAreaHint = i
			a.regionIdxFailureCount = 0
			return i, regionAlignedIdx, spaceBefore, spaceAfter, nil
		}
	}

	// Detailed diagnostic context
	totalRegions := primitives.FixedOrderedListLengthGet[freeMemoryRegionBlock](a.freeMemory)
	activeRegions := 0
	var largestFree, smallestFree uint64
	for i := uint64(0); i < totalRegions; i++ {
		r := primitives.FixedOrderedListItemPtrGetAtUnsafe[freeMemoryRegionBlock](a.freeMemory, i)
		if r.sizeBytes == 0 {
			continue
		}
		activeRegions++
		if r.sizeBytes > largestFree {
			largestFree = r.sizeBytes
		}
		if smallestFree == 0 || (r.sizeBytes < smallestFree && r.sizeBytes > 0) {
			smallestFree = r.sizeBytes
		}
	}

	errMsg := fmt.Sprintf(
		"manual allocator: no suitable free region found\n"+
			"  Requested Size:      %d bytes\n"+
			"  Requested Alignment: %d bytes\n"+
			"  Scanned Regions:     %d (from %d to %d)\n"+
			"  Active Free Regions: %d\n"+
			"  Smallest Free Block: %d bytes\n"+
			"  Largest Free Block:  %d bytes\n"+
			"  Allocator Data Off:  0x%x\n"+
			"  Region Hint Index:   %d\n"+
			"  Hint Failure Count:  %d\n"+
			"  Possible Cause:      fragmentation, misalignment, or data offset overflow",
		requestedSize,
		requestedAlignment,
		lastIdxExclusive-startIdx,
		startIdx,
		lastIdxExclusive-1,
		activeRegions,
		smallestFree,
		largestFree,
		a.dataRegionOffset,
		a.regionIdxAreaHint,
		a.regionIdxFailureCount,
	)

	return 0, 0, 0, 0, fmt.Errorf("%s", errMsg)
}

//go:inline
func fixedManualAllocatorGetIdxRelativeToDataRegion(a *FixedManualAllocator, pointer memcore.Pointer) uintptr {
	return memcore.PointerOffset(pointer) - a.dataRegionOffset
}
