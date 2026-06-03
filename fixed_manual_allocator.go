package memforge

import (
	"fmt"
	"memcore"
	"memstruct"
)

const allocatorFailureCountResetHeuristic uint8 = 5

// allocationRecord stores metadata for a single active allocation.
type allocationRecord struct {
	ptr       memcore.MarkRaw
	sizeBytes uint64
}

// freeMemoryRegionBlock represents a contiguous region of unallocated memory.
type freeMemoryRegionBlock struct {
	memStartIdx uint64 // Byte index of region start
	sizeBytes   uint64 // Total size in bytes
}

/*
FixedManualAllocator is a fixed-size general-purpose allocator with malloc/free semantics.

[Context]
Supports arbitrary sizes and alignments within a single mmap arena. Frees coalesce adjacent free
regions. Metadata (free list and allocation records) lives in an embedded FixedLinearAllocator.
Use for persistent subsystems, registries, and sparse lifetimes within one region.

[Complexity]
Malloc and Free: O(n) over free regions in the worst case; typically much better with the region hint.

[Side Effects]
Malloc and Free mutate free lists and allocation records. Pointers are stable (no relocation).

[Thread Safety]
Not safe for concurrent use from multiple goroutines.

[Invariants]
Every allocation must be freed or the whole allocator reset or destroyed. Do not store Go pointers
in allocator-managed memory. Double-free or freeing foreign marks panic.
*/
type FixedManualAllocator struct {
	linearAllocatorState

	metadataAllocator memcore.MarkRaw
	freeMemory        *memstruct.FixedOrderedList[freeMemoryRegionBlock]
	ptrRefs           *memstruct.FixedOrderedList[allocationRecord]

	regionIdxAreaHint     uint64
	regionIdxFailureCount uint8
}

// ---------------------------------- CREATION & DESTRUCTION ----------------------------------

/*
FixedManualAllocatorCreate maps a region and initializes a manual allocator.

[Parameters]
sizeBytes - Data arena capacity excluding the allocator header.

[Returns]
A memcore.MarkRaw to the allocator header.

[Errors]
Panics if mmap fails or metadata list capacity is insufficient.
*/
func FixedManualAllocatorCreate(sizeBytes uint64, tag string) memcore.MarkRaw {
	backing, mmapBase, mmap := memforgeDataBackingCreateFromMmap(sizeBytes)
	return fixedManualAllocatorCreateInternal(backing, true, mmapBase, uint64(len(mmap)), "Fixed Manual (Mmap)", tag)
}

/*
FixedManualAllocatorCreateForDataRegion initializes a manual allocator over caller-owned data.

[Parameters]
backing - Pre-registered memcore data region. memforge does not unregister this region on Destroy.
*/
func FixedManualAllocatorCreateForDataRegion(backing MemforgeDataBacking, tag string) memcore.MarkRaw {
	return fixedManualAllocatorCreateInternal(backing, false, 0, 0, "Fixed Manual (External)", tag)
}

func fixedManualAllocatorCreateInternal(
	backing MemforgeDataBacking,
	ownsDataRegion bool,
	dataMmapBase uintptr,
	dataMmapSize uint64,
	debugName string,
	tag string,
) memcore.MarkRaw {
	sizeBytes := backing.DataCapBytes

	minTrack := memcore.SizeOf[uintptr]()
	if sizeBytes < uint64(minTrack) {
		sizeBytes = uint64(minTrack)
	}
	maxAllocs := backing.DataCapBytes / uint64(minTrack)

	ptrRefBytes := alignIdxUp(memstruct.FixedOrderedListRequiredBytes[allocationRecord](maxAllocs), memstruct.FixedOrderedListRequiredAlignment[allocationRecord]())
	freeListBytes := alignIdxUp(memstruct.FixedOrderedListRequiredBytes[freeMemoryRegionBlock](maxAllocs), memstruct.FixedOrderedListRequiredAlignment[freeMemoryRegionBlock]())
	metaBytes := ptrRefBytes + freeListBytes
	metaAlloc := FixedLinearAllocatorCreate(metaBytes, "Allocator Metadata")

	ptrRefsPtr := FixedLinearAllocatorMalloc(metaAlloc, ptrRefBytes, memcore.AlignOf[memstruct.FixedOrderedList[allocationRecord]]())
	freeListPtr := FixedLinearAllocatorMalloc(metaAlloc, freeListBytes, memcore.AlignOf[memstruct.FixedOrderedList[freeMemoryRegionBlock]]())

	memstruct.FixedOrderedListInitializeAt[allocationRecord](ptrRefsPtr, maxAllocs)
	memstruct.FixedOrderedListInitializeAt[freeMemoryRegionBlock](freeListPtr, maxAllocs)

	headerMark := memforgeHeaderAllocate(
		uint64(memcore.SizeOf[FixedManualAllocator]()),
		uint64(memcore.AlignOf[FixedManualAllocator]()),
	)
	allocator := memcore.MemcoreMarkDereferenceObject[FixedManualAllocator](headerMark)
	allocator.linearAllocatorState = linearAllocatorStateInit(backing, ownsDataRegion, dataMmapBase, dataMmapSize)
	allocator.metadataAllocator = metaAlloc
	allocator.freeMemory = memcore.MemcoreMarkDereferenceObjectUnsafe[memstruct.FixedOrderedList[freeMemoryRegionBlock]](freeListPtr)
	allocator.ptrRefs = memcore.MemcoreMarkDereferenceObjectUnsafe[memstruct.FixedOrderedList[allocationRecord]](ptrRefsPtr)
	memstruct.FixedOrderedListAppendUnsafeFast(allocator.freeMemory, freeMemoryRegionBlock{
		memStartIdx: 0,
		sizeBytes:   backing.DataCapBytes,
	})
	allocator.regionIdxAreaHint = 0
	allocator.regionIdxFailureCount = 0

	memforgeAllocatorRegister(headerMark, debugName, tag, backing.DataRegionID, backing.DataCapBytes, backing.DataCapBytes)
	return headerMark
}

/*
FixedManualAllocatorDestroy destroys metadata allocator, unmaps the region, and clears telemetry.

[Invariants]
The allocator and all marks from it must not be used after this call.
*/
func FixedManualAllocatorDestroy(allocator memcore.MarkRaw) {
	header := memcore.MemcoreMarkDereferenceObject[FixedManualAllocator](allocator)

	memforgeAllocatorDestroy(allocator)
	FixedLinearAllocatorDestroy(header.metadataAllocator)
	linearAllocatorStateDestroy(&header.linearAllocatorState)
}

// ---------------------------------- ALLOCATION ----------------------------------

/*
FixedManualAllocatorMalloc allocates sizeBytes with alignment from the first fitting free region.

[Parameters]
sizeBytes - Requested allocation size in bytes.
alignment - Required alignment; must be a power of two greater than zero.

[Returns]
A memcore.MarkRaw to uninitialized memory.

[Errors]
Panics on invalid alignment or when no free region can satisfy the request (includes fragmentation diagnostics).
*/
func FixedManualAllocatorMalloc(allocator memcore.MarkRaw, sizeBytes, alignment uint64) memcore.MarkRaw {
	header := memcore.MemcoreMarkDereferenceObject[FixedManualAllocator](allocator)
	alignmentValidate(alignment)

	regionIdx, alignedIdx, spaceBefore, spaceAfter, err := getFreeAlignedIdx(header, sizeBytes, alignment)
	if err != nil {
		panic(fmt.Errorf("manual allocator: out of memory: %w", err))
	}
	updateFreeListAfterAllocation(header, regionIdx, alignedIdx, sizeBytes, spaceBefore, spaceAfter)

	offset := uintptr(alignedIdx)
	ptr := memcore.MemcoreMarkCreate(header.dataRegionID, offset)
	memforgeAllocationAdd(allocator, ptr, sizeBytes)
	insertAllocationRecord(header, ptr, sizeBytes)
	return ptr
}

/*
FixedManualAllocatorMallocUnsafe is identical to FixedManualAllocatorMalloc but skips alignment validation.
*/
func FixedManualAllocatorMallocUnsafe(allocator memcore.MarkRaw, sizeBytes, alignment uint64) memcore.MarkRaw {
	header := memcore.MemcoreMarkDereferenceObject[FixedManualAllocator](allocator)
	regionIdx, alignedIdx, spaceBefore, spaceAfter, err := getFreeAlignedIdx(header, sizeBytes, alignment)
	if err != nil {
		panic(fmt.Errorf("manual allocator: out of memory: %w", err))
	}
	updateFreeListAfterAllocation(header, regionIdx, alignedIdx, sizeBytes, spaceBefore, spaceAfter)
	offset := uintptr(alignedIdx)
	ptr := memcore.MemcoreMarkCreate(header.dataRegionID, offset)
	memforgeAllocationAdd(allocator, ptr, sizeBytes)
	insertAllocationRecord(header, ptr, sizeBytes)
	return ptr
}

/*
FixedManualAllocatorCalloc allocates with alignment and zeroes the block.
*/
func FixedManualAllocatorCalloc(allocator memcore.MarkRaw, sizeBytes, alignment uint64) memcore.MarkRaw {
	ptr := FixedManualAllocatorMalloc(allocator, sizeBytes, alignment)
	memcore.MemoryClearNoHeapPointers(memcore.MemcoreMarkDereference(ptr), uintptr(sizeBytes))
	return ptr
}

/*
FixedManualAllocatorCallocUnsafe allocates and zeroes without alignment validation.
*/
func FixedManualAllocatorCallocUnsafe(allocator memcore.MarkRaw, sizeBytes, alignment uint64) memcore.MarkRaw {
	ptr := FixedManualAllocatorMallocUnsafe(allocator, sizeBytes, alignment)
	memcore.MemoryClearNoHeapPointers(memcore.MemcoreMarkDereference(ptr), uintptr(sizeBytes))
	return ptr
}

/*
FixedManualAllocatorMallocObject allocates storage for T using SizeOf and AlignOf.

[Returns]
A memcore.MarkRaw to uninitialized memory.
*/
func FixedManualAllocatorMallocObject[T any](allocator memcore.MarkRaw) memcore.MarkRaw {
	size := memcore.SizeOf[T]()
	align := memcore.AlignOf[T]()
	ptr := FixedManualAllocatorMalloc(allocator, size, align)
	return ptr
}

/*
FixedManualAllocatorCallocObject allocates a zeroed value of T.

[Returns]
A memcore.MarkRaw to zeroed memory.
*/
func FixedManualAllocatorCallocObject[T any](allocator memcore.MarkRaw) memcore.MarkRaw {
	size := memcore.SizeOf[T]()
	align := memcore.AlignOf[T]()
	ptr := FixedManualAllocatorCalloc(allocator, size, align)
	return ptr
}

// ---------------------------------- FREE ----------------------------------

/*
FixedManualAllocatorFree returns a block to the free list and coalesces with neighbors when possible.

[Parameters]
target - Mark previously returned by this allocator's Malloc family.

[Errors]
Panics if target is unknown, double-freed, or not owned by this allocator.

[Side Effects]
Removes the allocation from tracking and may merge adjacent free regions.
*/
func FixedManualAllocatorFree(allocator memcore.MarkRaw, target memcore.MarkRaw) {
	header := memcore.MemcoreMarkDereferenceObject[FixedManualAllocator](allocator)
	rec, refIdx := fixedManualAllocatorFindRef(header, target)
	prevIdx, nextIdx := fixedManualAllocatorFindAdjacentRegions(header, rec)

	fixedManualAllocatorMergeOrInsert(header, rec, prevIdx, nextIdx)
	memstruct.FixedOrderedListDeleteUnsafeFast(header.ptrRefs, refIdx)
	memforgeAllocationRemove(allocator, target)
}

// ---------------------------------- RESET ----------------------------------

/*
FixedManualAllocatorReset clears all live allocations and restores one free span over the data arena.

[Complexity]
Time: O(1) for list clears plus O(1) to seed the free block (metadata lists are cleared, not walked per allocation).

[Invariants]
All prior marks are invalid after Reset; they must not be freed or dereferenced.
*/
func FixedManualAllocatorReset(allocator memcore.MarkRaw) {
	header := memcore.MemcoreMarkDereferenceObject[FixedManualAllocator](allocator)

	memforgeAllocatorRemoveAll(allocator)

	memstruct.FixedOrderedListClearFast(header.freeMemory)
	memstruct.FixedOrderedListClearFast(header.ptrRefs)
	memstruct.FixedOrderedListAppendUnsafeFast(header.freeMemory, freeMemoryRegionBlock{
		memStartIdx: 0,
		sizeBytes:   header.dataCapBytes,
	})
	header.regionIdxAreaHint = 0
	header.regionIdxFailureCount = 0
}

// ---------------------------------- PRIVATE HELPERS ----------------------------------

// Insert a new record into ptrRefs.
func insertAllocationRecord(a *FixedManualAllocator, ptr memcore.MarkRaw, size uint64) {
	rec := allocationRecord{ptr: ptr, sizeBytes: size}
	insertIdx := memstruct.FixedOrderedListBinarySearchInsertionPointFast(a.ptrRefs, func(item allocationRecord) int8 {
		if fixedManualAllocatorGetIdxRelativeToDataRegion(a, item.ptr) < fixedManualAllocatorGetIdxRelativeToDataRegion(a, ptr) {
			return -1
		}
		return 1
	})
	if err := memstruct.FixedOrderedListInsertAtFast(a.ptrRefs, insertIdx, rec); err != nil {
		panic("manual allocator: pointer reference table overflow")
	}
}

// Find a record by pointer.
func fixedManualAllocatorFindRef(a *FixedManualAllocator, target memcore.MarkRaw) (*allocationRecord, uint64) {
	refIdx, err := memstruct.FixedOrderedListBinarySearchFast(a.ptrRefs, func(item allocationRecord) int8 {
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
		panic(fmt.Errorf("manual allocator: unknown pointer: %v", target))
	}
	return memstruct.FixedOrderedListItemPtrGetAtUnsafeFast(a.ptrRefs, refIdx), refIdx
}

// Find free regions adjacent to an allocation.
func fixedManualAllocatorFindAdjacentRegions(a *FixedManualAllocator, ref *allocationRecord) (uint64, uint64) {
	return memstruct.FixedOrderedListBinarySearchIntervalFast(a.freeMemory, func(item freeMemoryRegionBlock) int8 {
		if item.memStartIdx < uint64(fixedManualAllocatorGetIdxRelativeToDataRegion(a, ref.ptr)) {
			return -1
		}
		return 1
	})
}

// Merge or insert free region during deallocation.
func fixedManualAllocatorMergeOrInsert(a *FixedManualAllocator, ref *allocationRecord, prevIdx, nextIdx uint64) {
	hasPrev := memstruct.FixedOrderedListIsIdxValidFast(a.freeMemory, prevIdx)
	hasNext := memstruct.FixedOrderedListIsIdxValidFast(a.freeMemory, nextIdx)
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
	prev := memstruct.FixedOrderedListItemPtrGetAtUnsafeFast(a.freeMemory, prevIdx)
	next := memstruct.FixedOrderedListItemPtrGetAtUnsafeFast(a.freeMemory, nextIdx)
	canPrev := canMergeRegions(prev.memStartIdx, prev.sizeBytes, uint64(fixedManualAllocatorGetIdxRelativeToDataRegion(a, ref.ptr)))
	canNext := canMergeRegions(uint64(fixedManualAllocatorGetIdxRelativeToDataRegion(a, ref.ptr)), ref.sizeBytes, next.memStartIdx)

	switch {
	case canPrev && canNext:
		size := prev.sizeBytes + ref.sizeBytes + next.sizeBytes
		updateFreeRegion(a, prevIdx, prev.memStartIdx, size)
		memstruct.FixedOrderedListDeleteUnsafeFast(a.freeMemory, nextIdx)
	case canPrev:
		fixedManualAllocatorMergePrev(a, ref, prevIdx, nextIdx)
	case canNext:
		fixedManualAllocatorMergeNext(a, ref, nextIdx)
	default:
		pushFreePointer(a, ref, nextIdx)
	}
}

func fixedManualAllocatorMergePrev(a *FixedManualAllocator, ref *allocationRecord, prevIdx, nextIdx uint64) {
	prev := memstruct.FixedOrderedListItemPtrGetAtUnsafeFast(a.freeMemory, prevIdx)
	if !canMergeRegions(prev.memStartIdx, prev.sizeBytes, uint64(fixedManualAllocatorGetIdxRelativeToDataRegion(a, ref.ptr))) {
		pushFreePointer(a, ref, nextIdx)
		return
	}
	updateFreeRegion(a, prevIdx, prev.memStartIdx, prev.sizeBytes+ref.sizeBytes)
}

func fixedManualAllocatorMergeNext(a *FixedManualAllocator, ref *allocationRecord, nextIdx uint64) {
	next := memstruct.FixedOrderedListItemPtrGetAtUnsafeFast(a.freeMemory, nextIdx)
	if !canMergeRegions(uint64(fixedManualAllocatorGetIdxRelativeToDataRegion(a, ref.ptr)), ref.sizeBytes, next.memStartIdx) {
		pushFreePointer(a, ref, nextIdx)
		return
	}
	updateFreeRegion(a, nextIdx, uint64(fixedManualAllocatorGetIdxRelativeToDataRegion(a, ref.ptr)), ref.sizeBytes+next.sizeBytes)
}

func updateFreeRegion(a *FixedManualAllocator, mdIdx, memIdx, memSize uint64) {
	blk := memstruct.FixedOrderedListItemPtrGetAtUnsafeFast(a.freeMemory, mdIdx)
	blk.memStartIdx = memIdx
	blk.sizeBytes = memSize
	if mdIdx < a.regionIdxAreaHint {
		a.regionIdxAreaHint = mdIdx
	}
}

func pushFreePointer(a *FixedManualAllocator, ref *allocationRecord, mdIdx uint64) {
	newBlk := freeMemoryRegionBlock{memStartIdx: uint64(fixedManualAllocatorGetIdxRelativeToDataRegion(a, ref.ptr)), sizeBytes: ref.sizeBytes}
	if err := memstruct.FixedOrderedListInsertAtFast(a.freeMemory, mdIdx, newBlk); err != nil {
		panic("manual allocator: metadata overflow")
	}
	if mdIdx < a.regionIdxAreaHint {
		a.regionIdxAreaHint = mdIdx
	}
}

func canMergeRegions(aIdx, aSize, bIdx uint64) bool { return aIdx+aSize == bIdx }

// Search utilities for free regions
func getFreeAlignedIdx(a *FixedManualAllocator, requestedSize, requestedAlignment uint64) (uint64, uint64, uint64, uint64, error) {
	regionAmount := memstruct.FixedOrderedListLengthGetFast(a.freeMemory)
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
		memstruct.FixedOrderedListDeleteUnsafeFast(a.freeMemory, regionIdx)
		if a.regionIdxAreaHint > regionIdx && a.regionIdxAreaHint != 0 {
			a.regionIdxAreaHint--
		}

	case spaceBefore == 0 && spaceAfter > 0:
		// Allocation at the start; shrink region from the left.
		ptr := memstruct.FixedOrderedListItemPtrGetAtUnsafeFast(a.freeMemory, regionIdx)
		ptr.memStartIdx = alignedIdx + sizeBytes
		ptr.sizeBytes = spaceAfter

	case spaceBefore > 0 && spaceAfter == 0:
		// Allocation at the end; shrink region from the right.
		ptr := memstruct.FixedOrderedListItemPtrGetAtUnsafeFast(a.freeMemory, regionIdx)
		ptr.memStartIdx = alignedIdx - spaceBefore
		ptr.sizeBytes = spaceBefore

	case spaceBefore > 0 && spaceAfter > 0:
		// Allocation splits the region into two parts.
		ptr := memstruct.FixedOrderedListItemPtrGetAtUnsafeFast(a.freeMemory, regionIdx)
		ptr.memStartIdx = alignedIdx - spaceBefore
		ptr.sizeBytes = spaceBefore

		// Insert the second half immediately after.
		newBlock := freeMemoryRegionBlock{
			memStartIdx: alignedIdx + sizeBytes,
			sizeBytes:   spaceAfter,
		}
		if err := memstruct.FixedOrderedListInsertAtFast(a.freeMemory, regionIdx+1, newBlock); err != nil {
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
		region := memstruct.FixedOrderedListItemPtrGetAtUnsafeFast(a.freeMemory, i)
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
	totalRegions := memstruct.FixedOrderedListLengthGetFast(a.freeMemory)
	activeRegions := 0
	var largestFree, smallestFree uint64
	for i := uint64(0); i < totalRegions; i++ {
		r := memstruct.FixedOrderedListItemPtrGetAtUnsafeFast(a.freeMemory, i)
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
		a.regionIdxAreaHint,
		a.regionIdxFailureCount,
	)

	return 0, 0, 0, 0, fmt.Errorf("%s", errMsg)
}

//go:inline
func fixedManualAllocatorGetIdxRelativeToDataRegion(a *FixedManualAllocator, pointer memcore.MarkRaw) uintptr {
	if memcore.MemcoreMarkRegionIDGet(pointer) != a.dataRegionID {
		panic(fmt.Errorf("manual allocator: mark belongs to foreign region"))
	}
	return memcore.MemcoreMarkOffsetGet(pointer)
}
