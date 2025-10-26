package memforge

import (
	"fmt"
	"memcore"
	"memcore/primitives"
	"unsafe"
)

// FixedManualAllocator is a fixed-size allocator that allows for manual freeing of memory.
// It is designed for high performance by avoiding locks and runtime overhead.
//
// Key Characteristics:
//   - Blazingly Fast: Achieves speed through direct memory manipulation.
//   - NOT Thread-Safe: Concurrent calls will lead to data races and undefined behavior.
//   - Stable Pointers: Memory is never moved or compacted, ensuring that returned
//     pointers remain valid for their entire lifetime.
type FixedManualAllocator struct {
	storage           memcore.MemoryMap
	metadataAllocator *FixedLinearAllocator

	freeMemory memoryFreeRegions
	ptrRefs    ptrRefTable

	cap       uint64
	destroyed bool
}

// FixedManualAllocatorCreate initializes a new manual allocator with a given size.
//
// ⚠️ Important: The allocator struct itself contains Go pointers and must reside on
// the Go heap, visible to the garbage collector. Do NOT allocate this struct in
// manually-managed memory.
//
// You may, however, point its internal data buffer to a manually managed memory
// region (e.g., from another allocator or a direct mmap call).
//
//   - ✅ Safe:   Allocator struct on Go heap, data buffer in manual memory.
//   - ❌ Unsafe: Allocator struct and data buffer both in manual memory.
func FixedManualAllocatorCreate(sizeBytes uint) *FixedManualAllocator {
	// --- Data Arena ---
	mmap, err := memcore.MemmapRequest(int(sizeBytes), memcore.PROT_READWRITE, memcore.MAP_ANON_PRIVATE)
	if err != nil {
		panic(fmt.Errorf("failure to create manual allocator data region: %w", err))
	}

	// --- Metadata Arena ---
	minTrackableAllocSize := memcore.SizeOf[uintptr]()

	if uint64(sizeBytes) < minTrackableAllocSize {
		sizeBytes = uint(minTrackableAllocSize)
	}

	maxPossibleAllocs := uint64(sizeBytes) / minTrackableAllocSize

	// Calculate the exact space needed for the pointer reference table in the worst case.
	requiredPtrRefBytes := maxPossibleAllocs * memcore.SizeOf[ptrRecord]()

	// Double the space to safely accommodate the free list metadata as well.
	// We also enforce a minimum metadata size (e.g., 64KB) for very small allocators
	// to ensure they remain functional.
	metadataAllocationSize := max(64*1024, requiredPtrRefBytes*2)

	metadataAllocator := FixedLinearAllocatorCreate(int(metadataAllocationSize))

	// --- Split Metadata ---
	ptrRefsTableAddr := FixedLinearAllocatorMalloc(
		metadataAllocator,
		uint64(metadataAllocationSize/2),
		memcore.AlignOf[ptrRecord](),
	)
	freeMemoryAddr := FixedLinearAllocatorMalloc(
		metadataAllocator,
		uint64(metadataAllocationSize/2),
		memcore.AlignOf[freeMemoryRegionBlock](),
	)

	ptrRefCapacity := uint64(metadataAllocationSize/2) / memcore.SizeOf[ptrRecord]()
	freeMemCapacity := uint64(metadataAllocationSize/2) / memcore.SizeOf[freeMemoryRegionBlock]()

	ptrRefs := ptrRefTable(
		primitives.FixedOrderedListCreateAt[ptrRecord](ptrRefsTableAddr, ptrRefCapacity),
	)
	freeMemory := memoryFreeRegions(
		primitives.FixedOrderedListCreateAt[freeMemoryRegionBlock](freeMemoryAddr, freeMemCapacity),
	)

	// Initialize with a single block representing all available memory.
	primitives.FixedOrderedListInsert(freeMemory, freeMemoryRegionBlock{
		memStartIdx: 0,
		sizeBytes:   uint64(sizeBytes),
	})

	return &FixedManualAllocator{
		storage:           mmap,
		metadataAllocator: metadataAllocator,
		cap:               uint64(sizeBytes),
		ptrRefs:           ptrRefs,
		freeMemory:        freeMemory,
		destroyed:         false,
	}
}

// FixedManualAllocatorDestroy releases all resources used by the allocator.
// Using the allocator after destruction will result in a panic.
func FixedManualAllocatorDestroy(allocator *FixedManualAllocator) {
	if allocator == nil || allocator.destroyed {
		return
	}
	memcore.MemmapUnmap(allocator.storage)
	memcore.MemmapUnmap(allocator.metadataAllocator.storage)
	allocator.freeMemory = nil
	allocator.ptrRefs = nil
	allocator.metadataAllocator = nil
	*allocator = FixedManualAllocator{destroyed: true}
}

// FixedManualAllocatorMalloc allocates a block of memory of `sizeBytes` with the
// specified `alignment`. The memory is not zeroed and may contain garbage.
//
// Storing Go pointers in this memory results in undefined behavior.
// Requesting 0 bytes returns an aligned pointer but does not change allocator state.
// Panics if no suitable memory region is found.
//
//go:nosplit
func FixedManualAllocatorMalloc(instance *FixedManualAllocator, sizeBytes uint64, alignment uint64) unsafe.Pointer {
	fixedManualAllocatorNotDestroyedGuarantee(instance)
	alignmentValidate(alignment)

	regionIdx, alignedIdx, spaceBefore, spaceAfter, err := getFreeAlignedIdx(instance, sizeBytes, alignment)
	if err != nil {
		panic("cannot allocate more memory than available")
	}

	updateFreeListAfterAllocation(instance, regionIdx, alignedIdx, sizeBytes, spaceBefore, spaceAfter)

	ptr := unsafe.Pointer(&instance.storage[alignedIdx])
	insertPtrRecord(instance, ptr, alignedIdx, sizeBytes)
	return ptr
}

// FixedManualAllocatorMallocUnsafe is a faster version of Malloc that skips
// alignment validation. Use only when alignment is guaranteed to be a power of two.
//
//go:nosplit
func FixedManualAllocatorMallocUnsafe(instance *FixedManualAllocator, sizeBytes uint64, alignment uint64) unsafe.Pointer {
	fixedManualAllocatorNotDestroyedGuarantee(instance)

	regionIdx, alignedIdx, spaceBefore, spaceAfter, err := getFreeAlignedIdx(instance, sizeBytes, alignment)
	if err != nil {
		panic("cannot allocate more memory than available")
	}

	updateFreeListAfterAllocation(instance, regionIdx, alignedIdx, sizeBytes, spaceBefore, spaceAfter)

	ptr := unsafe.Pointer(&instance.storage[alignedIdx])
	insertPtrRecord(instance, ptr, alignedIdx, sizeBytes)
	return ptr
}

// FixedManualAllocatorCalloc allocates and zero-initializes a block of memory.
//
//go:nosplit
func FixedManualAllocatorCalloc(instance *FixedManualAllocator, sizeBytes, alignment uint64) unsafe.Pointer {
	ptr := FixedManualAllocatorMalloc(instance, sizeBytes, alignment)
	memcore.MemoryClearNoHeapPointers(ptr, uintptr(sizeBytes))
	return ptr
}

// FixedManualAllocatorCallocUnsafe is a faster version of Calloc that skips
// alignment validation.
//
//go:nosplit
func FixedManualAllocatorCallocUnsafe(instance *FixedManualAllocator, sizeBytes, alignment uint64) unsafe.Pointer {
	ptr := FixedManualAllocatorMallocUnsafe(instance, sizeBytes, alignment)
	memcore.MemoryClearNoHeapPointers(ptr, uintptr(sizeBytes))
	return ptr
}

// FixedManualAllocatorMallocObject is a generic convenience wrapper around Malloc.
//
//go:nosplit
func FixedManualAllocatorMallocObject[T any](instance *FixedManualAllocator) *T {
	ptr := FixedManualAllocatorMalloc(instance, memcore.SizeOf[T](), memcore.AlignOf[T]())
	return (*T)(ptr)
}

// FixedManualAllocatorCallocObject is a generic convenience wrapper around Calloc.
//
//go:nosplit
func FixedManualAllocatorCallocObject[T any](instance *FixedManualAllocator) *T {
	ptr := FixedManualAllocatorCalloc(instance, memcore.SizeOf[T](), memcore.AlignOf[T]())
	return (*T)(ptr)
}

// FixedManualAllocatorFree releases a previously allocated block of memory, making it
// available for future allocations.
//
// Panics if the pointer is not known to the allocator. Double-freeing or freeing
// an invalid pointer results in undefined behavior.
//
//go:nosplit
func FixedManualAllocatorFree(instance *FixedManualAllocator, ptr unsafe.Pointer) {
	ptrRef := fixedManualAllocatorFindRef(instance, ptr)
	prevIdx, nextIdx := fixedManualAllocatorFindAdjacentRegions(instance, ptrRef)

	fixedManualAllocatorMergeOrInsert(instance, ptrRef, prevIdx, nextIdx)
	primitives.FixedOrderedListDeleteUnsafe(instance.ptrRefs, fixedManualAllocatorFindRefIndex(instance, ptr))
}

// FixedManualAllocatorReset clears all allocations, making the entire memory region
// available again. This is an O(1) operation that effectively defragments memory.
// Pointers from before the reset are invalidated.
func FixedManualAllocatorReset(instance *FixedManualAllocator) {
	fixedManualAllocatorNotDestroyedGuarantee(instance)
	FixedLinearAllocatorReset(instance.metadataAllocator)
	primitives.FixedOrderedListClear(instance.freeMemory)
	primitives.FixedOrderedListClear(instance.ptrRefs)
	primitives.FixedOrderedListInsert(instance.freeMemory, freeMemoryRegionBlock{
		memStartIdx: 0,
		sizeBytes:   instance.cap,
	})
}

// ---------------------------------- PRIVATE HELPERS ----------------------------------

// updateFreeListAfterAllocation modifies the free list metadata to account for a new allocation.
func updateFreeListAfterAllocation(instance *FixedManualAllocator, regionIdx uint, alignedIdx, sizeBytes, spaceBefore, spaceAfter uint64) {
	switch {
	case spaceBefore == 0 && spaceAfter == 0:
		// Entire region consumed, remove it from the free list.
		primitives.FixedOrderedListDeleteUnsafe(instance.freeMemory, uint64(regionIdx))

	case spaceBefore == 0 && spaceAfter > 0:
		// Allocation is at the start; shrink the region from the left.
		primitives.FixedOrderedListSetAtUnsafe(instance.freeMemory, uint64(regionIdx),
			freeMemoryRegionBlock{
				memStartIdx: alignedIdx + sizeBytes,
				sizeBytes:   spaceAfter,
			})

	case spaceBefore > 0 && spaceAfter == 0:
		// Allocation is at the end; shrink the region from the right.
		primitives.FixedOrderedListSetAtUnsafe(instance.freeMemory, uint64(regionIdx),
			freeMemoryRegionBlock{
				memStartIdx: alignedIdx - spaceBefore,
				sizeBytes:   spaceBefore,
			})

	case spaceBefore > 0 && spaceAfter > 0:
		// Allocation is in the middle; split the region into two.
		// Replace the current region with the prefix.
		primitives.FixedOrderedListSetAtUnsafe(instance.freeMemory, uint64(regionIdx),
			freeMemoryRegionBlock{
				memStartIdx: alignedIdx - spaceBefore,
				sizeBytes:   spaceBefore,
			})
		// Insert the new suffix region.
		primitives.FixedOrderedListInsertAtUnsafe(instance.freeMemory, uint64(regionIdx+1),
			freeMemoryRegionBlock{
				memStartIdx: alignedIdx + sizeBytes,
				sizeBytes:   spaceAfter,
			})
	}
}

// insertPtrRecord adds metadata for a new allocation to the pointer reference table.
func insertPtrRecord(instance *FixedManualAllocator, ptr unsafe.Pointer, alignedIdx, sizeBytes uint64) {
	newRecord := ptrRecord{key: uintptr(ptr), idx: alignedIdx, sizeBytes: sizeBytes}
	// Find the correct sorted position for the new record.
	insertionIdx := primitives.FixedOrderedListBinarySearchInsertionPoint(instance.ptrRefs, func(item ptrRecord) int8 {
		if item.idx < newRecord.idx {
			return -1
		}
		return 1
	})
	if err := primitives.FixedOrderedListInsertAt(instance.ptrRefs, insertionIdx, newRecord); err != nil {
		panic("pointer reference table overflow or corruption")
	}
}

//go:nosplit
//go:inline
func fixedManualAllocatorFindRef(instance *FixedManualAllocator, ptr unsafe.Pointer) ptrRecord {
	refIdx, err := primitives.FixedOrderedListBinarySearch(instance.ptrRefs, func(item ptrRecord) int8 {
		addr := uintptr(ptr)
		switch {
		case item.key < addr:
			return -1
		case item.key == addr:
			return 0
		default:
			return 1
		}
	})
	if err != nil {
		panic("cannot free: unknown pointer")
	}
	return primitives.FixedOrderedListItemGetAtUnsafe(instance.ptrRefs, refIdx)
}

//go:nosplit
//go:inline
func fixedManualAllocatorFindRefIndex(instance *FixedManualAllocator, ptr unsafe.Pointer) uint64 {
	refIdx, err := primitives.FixedOrderedListBinarySearch(instance.ptrRefs, func(item ptrRecord) int8 {
		addr := uintptr(ptr)
		switch {
		case item.key < addr:
			return -1
		case item.key == addr:
			return 0
		default:
			return 1
		}
	})
	if err != nil {
		panic("cannot free: unknown pointer index")
	}
	return refIdx
}

//go:nosplit
//go:inline
func fixedManualAllocatorFindAdjacentRegions(instance *FixedManualAllocator, ptrRef ptrRecord) (uint64, uint64) {
	return primitives.FixedOrderedListBinarySearchInterval(instance.freeMemory, func(item freeMemoryRegionBlock) int8 {
		if item.memStartIdx < ptrRef.idx {
			return -1
		}
		return 1
	})
}

//go:nosplit
func fixedManualAllocatorMergeOrInsert(instance *FixedManualAllocator, ptrRef ptrRecord, prevIdx, nextIdx uint64) {
	hasPrev := primitives.FixedOrderedListIsIdxValid(instance.freeMemory, prevIdx)
	hasNext := primitives.FixedOrderedListIsIdxValid(instance.freeMemory, nextIdx)

	switch {
	case hasPrev && hasNext:
		fixedManualAllocatorMergePrevNext(instance, ptrRef, prevIdx, nextIdx)
	case hasPrev:
		fixedManualAllocatorMergePrev(instance, ptrRef, prevIdx, nextIdx)
	case hasNext:
		fixedManualAllocatorMergeNext(instance, ptrRef, nextIdx)
	default:
		pushFreePointer(instance, ptrRef, nextIdx)
	}
}

//go:nosplit
//go:inline
func fixedManualAllocatorMergePrevNext(instance *FixedManualAllocator, ptrRef ptrRecord, prevIdx, nextIdx uint64) {
	prev := primitives.FixedOrderedListItemGetAtUnsafe(instance.freeMemory, prevIdx)
	next := primitives.FixedOrderedListItemGetAtUnsafe(instance.freeMemory, nextIdx)

	canMergePrev := canMergeRegions(prev.memStartIdx, prev.sizeBytes, ptrRef.idx)
	canMergeNext := canMergeRegions(ptrRef.idx, ptrRef.sizeBytes, next.memStartIdx)

	switch {
	case canMergePrev && canMergeNext:
		// Merge all three: prev + freed block + next
		size := prev.sizeBytes + ptrRef.sizeBytes + next.sizeBytes
		updateFreeRegion(instance, prevIdx, prev.memStartIdx, size)
		primitives.FixedOrderedListDelete(instance.freeMemory, nextIdx)
	case canMergePrev:
		fixedManualAllocatorMergePrev(instance, ptrRef, prevIdx, nextIdx)
	case canMergeNext:
		fixedManualAllocatorMergeNext(instance, ptrRef, nextIdx)
	default:
		pushFreePointer(instance, ptrRef, nextIdx)
	}
}

//go:nosplit
//go:inline
func fixedManualAllocatorMergePrev(instance *FixedManualAllocator, ptrRef ptrRecord, prevIdx, nextIdx uint64) {
	prev := primitives.FixedOrderedListItemGetAtUnsafe(instance.freeMemory, prevIdx)
	if !canMergeRegions(prev.memStartIdx, prev.sizeBytes, ptrRef.idx) {
		pushFreePointer(instance, ptrRef, nextIdx)
		return
	}
	size := prev.sizeBytes + ptrRef.sizeBytes
	updateFreeRegion(instance, prevIdx, prev.memStartIdx, size)
}

//go:nosplit
//go:inline
func fixedManualAllocatorMergeNext(instance *FixedManualAllocator, ptrRef ptrRecord, nextIdx uint64) {
	next := primitives.FixedOrderedListItemGetAtUnsafe(instance.freeMemory, nextIdx)
	if !canMergeRegions(ptrRef.idx, ptrRef.sizeBytes, next.memStartIdx) {
		pushFreePointer(instance, ptrRef, nextIdx)
		return
	}
	size := ptrRef.sizeBytes + next.sizeBytes
	updateFreeRegion(instance, nextIdx, ptrRef.idx, size)
}

//go:nosplit
//go:inline
func updateFreeRegion(instance *FixedManualAllocator, mdIdx, memIdx, memSize uint64) {
	newFreeRegion := freeMemoryRegionBlock{
		memStartIdx: memIdx,
		sizeBytes:   memSize,
	}
	primitives.FixedOrderedListSetAtUnsafe(instance.freeMemory, mdIdx, newFreeRegion)
}

//go:nosplit
//go:inline
func pushFreePointer(instance *FixedManualAllocator, ptrRef ptrRecord, mdIdx uint64) {
	newFreeRegion := freeMemoryRegionBlock{
		memStartIdx: ptrRef.idx,
		sizeBytes:   ptrRef.sizeBytes,
	}
	if err := primitives.FixedOrderedListInsertAt(instance.freeMemory, mdIdx, newFreeRegion); err != nil {
		panic("cannot push new free region: metadata capacity exceeded")
	}
}

//go:nosplit
//go:inline
func canMergeRegions(aIdx, aSize, bIdx uint64) bool {
	return aIdx+aSize == bIdx
}

//go:nosplit
//go:inline
func getFreeAlignedIdx(allocator *FixedManualAllocator, requestedSize, requestedAlignment uint64) (uint, uint64, uint64, uint64, error) {
	for i := uint(0); i < uint(primitives.FixedOrderedListLengthGet(allocator.freeMemory)); i++ {
		region := primitives.FixedOrderedListItemGetAtUnsafe(allocator.freeMemory, uint64(i))

		regionAlignedIdx := alignIdxUp(region.memStartIdx, requestedAlignment)
		if regionAlignedIdx < region.memStartIdx { // Check for overflow
			continue
		}

		spaceBefore := regionAlignedIdx - region.memStartIdx
		if spaceBefore > region.sizeBytes { // Not enough space even for alignment
			continue
		}

		adjustedSize := region.sizeBytes - spaceBefore
		if adjustedSize >= requestedSize {
			spaceAfter := adjustedSize - requestedSize
			return i, regionAlignedIdx, spaceBefore, spaceAfter, nil
		}
	}
	return 0, 0, 0, 0, fmt.Errorf("no suitable free region found for size %d and alignment %d", requestedSize, requestedAlignment)
}

//go:inline
func fixedManualAllocatorNotDestroyedGuarantee(instance *FixedManualAllocator) {
	if instance.destroyed {
		panic("cannot use a destroyed allocator")
	}
}

// ---------------------------------- TYPE DEFINITIONS ----------------------------------

type ptrRefTable *primitives.FixedOrderedList[ptrRecord]
type memoryFreeRegions *primitives.FixedOrderedList[freeMemoryRegionBlock]

// freeMemoryRegionBlock represents a contiguous region of unallocated memory.
type freeMemoryRegionBlock struct {
	memStartIdx uint64 // Byte index of the region start
	sizeBytes   uint64 // Total size in bytes
}

// ptrRecord tracks allocated pointer metadata.
type ptrRecord struct {
	key       uintptr // Address of the allocation (used for searching)
	idx       uint64  // Byte index inside allocator.storage
	sizeBytes uint64  // Size of allocation in bytes
}
