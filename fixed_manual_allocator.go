package memforge

import (
	"fmt"
	"memcore"
	"unsafe"
)

// FixedManualAllocator is a fixed-size allocator which allows for manual freeing.
// Blazingly fast.
// NOT thread-safe -- it does not just non-block, it is unsafe to call concurrently.
// Memory is not moved around, so no compacting, etc.
// This means returned pointers stay stable.
type FixedManualAllocator struct {
	storage           memcore.MemoryMap
	metadataAllocator *FixedLinearAllocator

	freeMemory memoryFreeRegions
	ptrRefs    ptrRefTable

	freeMemoryRegionsAmount uint
	ptrRefAmount            uint

	cap       uint64
	destroyed bool
}

// FixedManualAllocatorCreate creates an instance of the linear allocator.
func FixedManualAllocatorCreate(sizeBytes uint) *FixedManualAllocator {
	mmap, err := memcore.MemmapRequest((int)(sizeBytes), memcore.PROT_READWRITE, memcore.MAP_ANON_PRIVATE)

	if err != nil {
		panic(fmt.Errorf("failure to create manual allocator: %w", err))
	}

	metadataAllocationSize := max(64*1024, sizeBytes/128)
	metadataAllocator := FixedLinearAllocatorCreate(int(metadataAllocationSize))

	ptrRefsTableAddr := FixedLinearAllocatorMalloc(metadataAllocator, (uint64)(metadataAllocationSize/2), memcore.AlignOf[ptrRefTable]())
	freeMemoryAddr := FixedLinearAllocatorMalloc(metadataAllocator, (uint64)(metadataAllocationSize/2), memcore.AlignOf[memoryFreeRegions]())

	freeMemory := memoryFreeRegions(memcore.ArrayCreateAt[freeMemoryRegionBlock](freeMemoryAddr, uint64(metadataAllocationSize)))
	memcore.ArraySetAt(freeMemory, 0, freeMemoryRegionBlock{
		memStartIdx: 0,
		sizeBytes:   uint64(sizeBytes),
	})

	return &FixedManualAllocator{
		storage:                 mmap,
		metadataAllocator:       metadataAllocator,
		cap:                     (uint64)(sizeBytes),
		ptrRefs:                 ptrRefTable(memcore.ArrayCreateAt[ptrRecord](ptrRefsTableAddr, uint64(metadataAllocationSize))),
		freeMemory:              freeMemory,
		freeMemoryRegionsAmount: 1,
		ptrRefAmount:            0,
		destroyed:               false,
	}
}

// FixedManualAllocatorDestroy destroys the allocator and cleans up.
// Do NOT use the allocator anymore.
func FixedManualAllocatorDestroy(allocator *FixedManualAllocator) {
	memcore.MemmapUnmap(allocator.storage)
	*allocator = FixedManualAllocator{destroyed: true}
}

// FixedManualAllocatorMalloc allocates X amount of bytes from the allocator.
// It does not zero the memory, therefore it may contain garbage.
// Storing Go pointers ANYWHERE inside this allocation results in undefined behaviour.
// Requesting 0 bytes returns an aligned pointer but does not change the allocator state.
//
//go:nosplit
func FixedManualAllocatorMalloc(instance *FixedManualAllocator, sizeBytes uint64, alignment uint64) unsafe.Pointer {
	fixedManualAllocatorNotDestroyedGuarantee(instance)
	alignmentValidate(alignment)

	regionIdx, alignedIdx, spaceBefore, spaceAfter, err := getFreeAlignedIdx(instance, sizeBytes, alignment)

	if err != nil {
		panic("cannot allocate more memory than available")
	}

	memcore.ArrayDeleteAtUnsafe(instance.freeMemory, uint64(regionIdx))
	instance.freeMemoryRegionsAmount--

	if spaceBefore > 0 {
		memcore.ArraySetAtUnsafe(instance.freeMemory, uint64(regionIdx), freeMemoryRegionBlock{
			memStartIdx: alignedIdx - spaceBefore,
			sizeBytes:   spaceBefore,
		})
		instance.freeMemoryRegionsAmount++

		if spaceAfter > 0 {
			memcore.ArrayInsertAtUnsafe(instance.freeMemory, uint64(regionIdx+1), freeMemoryRegionBlock{
				memStartIdx: alignedIdx + sizeBytes,
				sizeBytes:   spaceAfter,
			})
			instance.freeMemoryRegionsAmount++
		}
	} else if spaceAfter > 0 {
		memcore.ArraySetAtUnsafe(instance.freeMemory, uint64(regionIdx), freeMemoryRegionBlock{
			memStartIdx: alignedIdx + sizeBytes,
			sizeBytes:   spaceAfter,
		})
		instance.freeMemoryRegionsAmount++
	}

	ptr := unsafe.Pointer(&instance.storage[alignedIdx])

	memcore.ArraySetAtUnsafe(instance.ptrRefs, uint64(instance.ptrRefAmount), ptrRecord{
		key:       uintptr(ptr),
		idx:       alignedIdx,
		sizeBytes: sizeBytes,
	})

	instance.ptrRefAmount++

	return ptr
}

// FixedManualAllocatorMallocUnsafe allocates X amount of bytes from the allocator.
// It does not zero the memory, therefore it may contain garbage.
// Storing Go pointers ANYWHERE inside this allocation results in undefined behaviour.
// Requesting 0 bytes returns an aligned pointer but does not change the allocator state.
// The unsafe version skips the alignment validation and the existence guarantee for the sake of performance.
//
//go:nosplit
func FixedManualAllocatorMallocUnsafe(instance *FixedManualAllocator, sizeBytes uint64, alignment uint64) unsafe.Pointer {
	regionIdx, alignedIdx, spaceBefore, spaceAfter, err := getFreeAlignedIdx(instance, sizeBytes, alignment)

	if err != nil {
		panic("cannot allocate more memory than available")
	}

	memcore.ArrayDeleteAt(instance.freeMemory, uint64(regionIdx))
	instance.freeMemoryRegionsAmount--

	if spaceBefore > 0 {
		memcore.ArraySetAtUnsafe(instance.freeMemory, uint64(regionIdx), freeMemoryRegionBlock{
			memStartIdx: alignedIdx - spaceBefore,
			sizeBytes:   spaceBefore,
		})
		instance.freeMemoryRegionsAmount++

		if spaceAfter > 0 {
			memcore.ArrayInsertAtUnsafe(instance.freeMemory, uint64(regionIdx+1), freeMemoryRegionBlock{
				memStartIdx: alignedIdx + sizeBytes,
				sizeBytes:   spaceAfter,
			})
			instance.freeMemoryRegionsAmount++
		}
	} else if spaceAfter > 0 {
		memcore.ArraySetAtUnsafe(instance.freeMemory, uint64(regionIdx), freeMemoryRegionBlock{
			memStartIdx: alignedIdx + sizeBytes,
			sizeBytes:   spaceAfter,
		})
		instance.freeMemoryRegionsAmount++
	}

	ptr := unsafe.Pointer(&instance.storage[alignedIdx])

	memcore.ArraySetAtUnsafe(instance.ptrRefs, uint64(instance.ptrRefAmount), ptrRecord{
		key:       uintptr(ptr),
		idx:       alignedIdx,
		sizeBytes: sizeBytes,
	})

	instance.ptrRefAmount++

	return ptr
}

// FixedManualAllocatorCalloc is similar to FixedManualAllocatorMalloc, except it also zeroes out the memory.
// Note: This is quite expensive (especially for larger amounts of memory),
// so avoid using it if at all possible.
//
//go:nosplit
func FixedManualAllocatorCalloc(instance *FixedManualAllocator, sizeBytes, alignment uint64) unsafe.Pointer {
	ptr := FixedManualAllocatorMalloc(instance, sizeBytes, alignment)
	memcore.MemoryClearNoHeapPointers(ptr, uintptr(sizeBytes))
	return ptr
}

// FixedManualAllocatorCallocUnsafe is similar to FixedManualAllocatorMallocUnsafe, except it also zeroes out the memory.
// Note: This is quite expensive (especially for larger amounts of memory),
// so avoid using it if at all possible.
//
//go:nosplit
func FixedManualAllocatorCallocUnsafe(instance *FixedManualAllocator, sizeBytes, alignment uint64) unsafe.Pointer {
	ptr := FixedManualAllocatorMallocUnsafe(instance, sizeBytes, alignment)
	memcore.MemoryClearNoHeapPointers(ptr, uintptr(sizeBytes))
	return ptr
}

// FixedManualAllocatorMallocObject is a convenience wrapper around FixedLinearAllocatorMalloc.
// It converts the allocated memory to the desired object type.
//
//go:nosplit
func FixedManualAllocatorMallocObject[T any](instance *FixedManualAllocator) *T {
	ptr := FixedManualAllocatorMalloc(instance, memcore.SizeOf[T](), memcore.AlignOf[T]())
	return (*T)(ptr)
}

// FixedManualAllocatorCallocObject is a convenience wrapper around FixedLinearAllocatorCalloc.
// It converts the allocated memory to the desired object type.
//
//go:nosplit
func FixedManualAllocatorCallocObject[T any](instance *FixedManualAllocator) *T {
	ptr := FixedManualAllocatorCalloc(instance, memcore.SizeOf[T](), memcore.AlignOf[T]())
	return (*T)(ptr)
}

// FixedManualAllocatorFree allows for the freeing of memory.
// It panics if it does not know the pointer (which could be when double-freeing too)
//
//go:nosplit
func FixedManualAllocatorFree(instance *FixedManualAllocator, ptr unsafe.Pointer) {
	ptrAddr := uintptr(ptr)

	refIdx, err := memcore.ArrayBinarySearch[ptrRecord](instance.ptrRefs, func(item ptrRecord) int8 {
		if item.key < ptrAddr {
			return -1
		}
		if item.key == ptrAddr {
			return 0
		}

		return 1
	})
	if err != nil {
		panic("cannot free, unknown pointer")
	}

	ptrRef := memcore.ArrayItemGetAtUnsafe(instance.ptrRefs, refIdx) // Unsafe is fine because we know for sure that the idx is valid.

	previousRegionMdIdx, nextRegionMdIdx := memcore.ArrayBinarySearchInterval(instance.freeMemory, func(item freeMemoryRegionBlock) int8 {
		if item.memStartIdx < ptrRef.idx {
			return -1
		}
		return 1
	})

	// At most we only have to merge two adjacent blocks of memory,
	// Because of the splitting logic, a single free memory block can split into:
	// 0, 1, or 2 other blocks of free memory, with ALWAYS a taken region of memory (unstored) in between.

	// Cases:
	// 1) Only the free region can be pushed in. -- freeMemregionsAmount += 1 => push
	// 2) The free region and the previous region can be merged. -- freeMemregionsAmount does not change => swap
	// 3) The free region the next region can be merged. -- freeMemregionsAmount does not change => swap
	// 4) The free region and both the previous and next region can be merged. -- freeMemregionsAmount does not change => swap w/ previous, delete next with shift left beyond

	previousRegionExists := memcore.ArrayIsIdxValid(instance.freeMemory, previousRegionMdIdx)
	nextRegionExists := memcore.ArrayIsIdxValid(instance.freeMemory, nextRegionMdIdx)

	if previousRegionExists {
		previousRegion := memcore.ArrayItemGetAtUnsafe(instance.freeMemory, previousRegionMdIdx)
		canMergePrevious := canMergeRegions(previousRegion.memStartIdx, previousRegion.sizeBytes, ptrRef.idx)

		if nextRegionExists {
			nextRegion := memcore.ArrayItemGetAtUnsafe(instance.freeMemory, nextRegionMdIdx)
			canMergeNext := canMergeRegions(ptrRef.idx, ptrRef.sizeBytes, nextRegion.memStartIdx)

			if canMergePrevious && canMergeNext {
				swapPtrRegionAndRegion(instance, previousRegionMdIdx, previousRegion.memStartIdx, previousRegion.sizeBytes+ptrRef.sizeBytes+nextRegion.sizeBytes)
				memcore.ArrayDeleteAndShiftAt(instance.freeMemory, nextRegionMdIdx) // Deleted nextRegionMdIdx and shifts all subsequent values left
			} else if canMergePrevious {
				swapPtrRegionAndRegion(instance, previousRegionMdIdx, previousRegion.memStartIdx, previousRegion.sizeBytes+ptrRef.sizeBytes)
			} else if canMergeNext {
				swapPtrRegionAndRegion(instance, nextRegionMdIdx, ptrRef.idx, ptrRef.sizeBytes+nextRegion.sizeBytes)
			} else {
				pushFreePointer(instance, ptrRef, nextRegionMdIdx)
			}
		} else {
			if canMergePrevious {
				swapPtrRegionAndRegion(instance, previousRegionMdIdx, previousRegion.memStartIdx, previousRegion.sizeBytes+ptrRef.sizeBytes)
			} else {
				pushFreePointer(instance, ptrRef, previousRegionMdIdx)
			}
		}
	} else if nextRegionExists {
		nextRegion := memcore.ArrayItemGetAtUnsafe(instance.freeMemory, nextRegionMdIdx)

		if canMergeRegions(ptrRef.idx, ptrRef.sizeBytes, nextRegion.memStartIdx) {
			swapPtrRegionAndRegion(instance, nextRegionMdIdx, ptrRef.idx, ptrRef.sizeBytes+nextRegion.sizeBytes)
		} else {
			pushFreePointer(instance, ptrRef, nextRegionMdIdx)
		}

	} // Don't have to check for neither exist because the binary interval search always returns valid indices.

	memcore.ArrayDeleteAtUnsafe(instance.ptrRefs, refIdx)
}

// FixedManualAllocatorReset sets the index of the allocator to 0, allowing the memory to be re-used.
// Using pointers created before resetting results in undefined behaviour.
// This automatically "defragments" the memory.
func FixedManualAllocatorReset(instance *FixedManualAllocator) {
	fixedManualAllocatorNotDestroyedGuarantee(instance)
	FixedLinearAllocatorReset(instance.metadataAllocator)
	memcore.ArrayClear(instance.freeMemory)
	memcore.ArrayClear(instance.ptrRefs)

	memcore.ArraySetAt(instance.freeMemory, 0, freeMemoryRegionBlock{
		memStartIdx: 0,
		sizeBytes:   instance.cap,
	})
	instance.freeMemoryRegionsAmount = 1
	instance.ptrRefAmount = 0
}

// ---------------------------------- PRIVATE HELPERS

//go:nosplit
//go:inline
func swapPtrRegionAndRegion(instance *FixedManualAllocator, mdIdx, memIdx, memSize uint64) {
	newFreeRegion := freeMemoryRegionBlock{
		memStartIdx: memIdx,
		sizeBytes:   memSize,
	}
	memcore.ArraySetAtUnsafe(instance.freeMemory, mdIdx, newFreeRegion) // Swap - free memory region amount stays same
}

//go:nosplit
//go:inline
func pushFreePointer(instance *FixedManualAllocator, ptrRef ptrRecord, mdIdx uint64) {
	newFreeRegion := freeMemoryRegionBlock{
		memStartIdx: ptrRef.idx,
		sizeBytes:   ptrRef.sizeBytes,
	}

	err := memcore.ArrayInsertAt(instance.freeMemory, mdIdx, newFreeRegion)
	if err != nil { // Insert shifts original idx and subsequents one slot to the right.
		panic("cannot push new region because there is no more metadata space") // This should technically not happen but just in case.
	}
	instance.freeMemoryRegionsAmount++
}

//go:nosplit
//go:inline
func canMergeRegions(aIdx, aSize, bIdx uint64) bool {
	return aIdx+aSize == bIdx
}

//go:nosplit
//go:inline
func getFreeAlignedIdx(allocator *FixedManualAllocator, requestedSize uint64, requestedAlignment uint64) (uint, uint64, uint64, uint64, error) {
	for i := uint(0); i < allocator.freeMemoryRegionsAmount; i++ {
		region, err := memcore.ArrayItemGetAt(allocator.freeMemory, (uint64)(i))
		if err != nil {
			return 0, 0, 0, 0, err
		}

		regionAlignedIdx := alignIdxUp(region.memStartIdx, requestedAlignment)
		leftOverSpaceBefore := regionAlignedIdx - region.memStartIdx

		if leftOverSpaceBefore > region.sizeBytes {
			continue // alignment pushes beyond this region completely
		}

		adjustedSize := region.sizeBytes - leftOverSpaceBefore

		if adjustedSize >= requestedSize {
			leftOverSpaceAfter := adjustedSize - requestedSize

			return i, regionAlignedIdx, leftOverSpaceBefore, leftOverSpaceAfter, nil
		}
	}

	return 0, 0, 0, 0, fmt.Errorf("no region free that satisfied requested size %v and requested alignment %v", requestedSize, requestedAlignment)
}

type ptrRefTable *memcore.Array[ptrRecord]
type memoryFreeRegions *memcore.Array[freeMemoryRegionBlock]

type freeMemoryRegionBlock struct {
	memStartIdx uint64
	sizeBytes   uint64
}

type ptrRecord struct {
	key       uintptr
	idx       uint64
	sizeBytes uint64
}

//go:inline
func fixedManualAllocatorNotDestroyedGuarantee(instance *FixedManualAllocator) {
	if instance.destroyed {
		panic("cannot use a destroyed allocator")
	}
}
