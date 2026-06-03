package memforge

import (
	"fmt"
	"memcore"
	"memstruct"
)

/*
FixedSlabAllocator is a fixed-capacity pool of uniform slots for type T.

[Context]
Slots are indexed; a free stack tracks available indices. Allocator state lives in the memforge CPU
header store; slot payload lives in a dedicated data region starting at offset zero.

[Complexity]
Malloc and Free: O(1).
Reset: O(n) over slotCapacity to rebuild the free stack.

[Thread Safety]
Not safe for concurrent use from multiple goroutines.

[Invariants]
Do not store Go pointers in slot memory. Returned Malloc memory is uninitialized unless using Calloc.
Marks are invalid after Reset or Destroy.
*/
type FixedSlabAllocator[T any] struct {
	linearAllocatorState
	slotSize      uint64
	slotAlignment uint64
	slotCapacity  uint64
	freeStack     memcore.MarkRaw
	metaAllocator memcore.MarkRaw
}

func SlabAllocatorCreate[T any](capacity uint64, tag string) memcore.MarkRaw {
	return SlabAllocatorCreateWithSlotSize[T](capacity, memcore.SizeOf[T](), memcore.AlignOf[T](), tag)
}

func SlabAllocatorCreateWithSlotSize[T any](capacity, slotSizeBytes, slotAlignment uint64, tag string) memcore.MarkRaw {
	slotSize := alignIdxUp(slotSizeBytes, slotAlignment)
	dataBytes := slotSize * capacity
	backing, mmapBase, mmap := memforgeDataBackingCreateFromMmap(dataBytes)
	return slabAllocatorCreateInternal[T](backing, true, mmapBase, uint64(len(mmap)), capacity, slotSize, slotAlignment, "Fixed Slab (Mmap)", tag)
}

func SlabAllocatorCreateForDataRegion[T any](
	backing MemforgeDataBacking,
	capacity, slotSizeBytes, slotAlignment uint64,
	tag string,
) memcore.MarkRaw {
	slotSize := alignIdxUp(slotSizeBytes, slotAlignment)
	return slabAllocatorCreateInternal[T](backing, false, 0, 0, capacity, slotSize, slotAlignment, "Fixed Slab (External)", tag)
}

func slabAllocatorCreateInternal[T any](
	backing MemforgeDataBacking,
	ownsDataRegion bool,
	dataMmapBase uintptr,
	dataMmapSize uint64,
	capacity, slotSize, slotAlignment uint64,
	debugName string,
	tag string,
) memcore.MarkRaw {
	if capacity == 0 {
		panic("slab allocator: capacity must be greater than zero")
	}
	if slotSize == 0 {
		panic("slab allocator: slot size must be greater than zero")
	}
	alignmentValidate(slotAlignment)

	dataBytes := slotSize * capacity
	if backing.DataCapBytes < dataBytes {
		panic(fmt.Errorf("slab allocator: backing capacity %d < required %d", backing.DataCapBytes, dataBytes))
	}

	stackBytes := memstruct.StackRequiredBytesGet[uint64](capacity)
	stackAlign := memstruct.StackRequiredAlignmentGet[uint64]()

	metaAlloc := FixedLinearAllocatorCreate(stackBytes, "Allocator Metadata")
	stackPtr := FixedLinearAllocatorMalloc(metaAlloc, stackBytes, stackAlign)
	memstruct.StackInitializeAt[uint64](stackPtr, capacity)
	for i := capacity; i > 0; i-- {
		memstruct.StackPushUnsafe(stackPtr, i-1)
	}

	headerMark := memforgeHeaderAllocate(
		uint64(memcore.SizeOf[FixedSlabAllocator[T]]()),
		uint64(memcore.AlignOf[FixedSlabAllocator[T]]()),
	)
	header := memcore.MemcoreMarkDereferenceObject[FixedSlabAllocator[T]](headerMark)
	header.linearAllocatorState = linearAllocatorStateInit(backing, ownsDataRegion, dataMmapBase, dataMmapSize)
	header.slotSize = slotSize
	header.slotAlignment = slotAlignment
	header.slotCapacity = capacity
	header.freeStack = stackPtr
	header.metaAllocator = metaAlloc

	memforgeAllocatorRegister(headerMark, debugName, tag, backing.DataRegionID, dataBytes, dataBytes)
	return headerMark
}

func SlabAllocatorDestroy[T any](allocator memcore.MarkRaw) {
	header := memcore.MemcoreMarkDereferenceObject[FixedSlabAllocator[T]](allocator)

	memforgeAllocatorDestroy(allocator)
	FixedLinearAllocatorDestroy(header.metaAllocator)
	linearAllocatorStateDestroy(&header.linearAllocatorState)
}

func SlabAllocatorReset[T any](allocator memcore.MarkRaw) {
	header := memcore.MemcoreMarkDereferenceObject[FixedSlabAllocator[T]](allocator)

	memforgeAllocatorRemoveAll(allocator)

	memstruct.StackClear[uint64](header.freeStack)
	for i := header.slotCapacity; i > 0; i-- {
		memstruct.StackPushUnsafe(header.freeStack, i-1)
	}
}

//go:nosplit
func SlabAllocatorMalloc[T any](allocator memcore.MarkRaw) memcore.MarkRaw {
	header := memcore.MemcoreMarkDereferenceObject[FixedSlabAllocator[T]](allocator)

	idx, err := memstruct.StackPop[uint64](header.freeStack)
	if err != nil {
		panic(fmt.Errorf("slab allocator: out of memory: %w", err))
	}

	ptr := slabAllocatorMarkFromSlotIndex(header, idx)
	memforgeAllocationAdd(allocator, ptr, header.slotSize)
	return ptr
}

//go:nosplit
func SlabAllocatorCalloc[T any](allocator memcore.MarkRaw) memcore.MarkRaw {
	ptr := SlabAllocatorMalloc[T](allocator)
	memcore.MemoryClearNoHeapPointers(memcore.MemcoreMarkDereference(ptr), uintptr(headerSlotSizeGet[T](allocator)))
	return ptr
}

//go:nosplit
func SlabAllocatorMallocUnsafe[T any](allocator memcore.MarkRaw) memcore.MarkRaw {
	header := memcore.MemcoreMarkDereferenceObject[FixedSlabAllocator[T]](allocator)
	idx := memstruct.StackPopUnsafe[uint64](header.freeStack)

	ptr := slabAllocatorMarkFromSlotIndex(header, idx)
	memforgeAllocationAdd(allocator, ptr, header.slotSize)
	return ptr
}

//go:nosplit
func SlabAllocatorCallocUnsafe[T any](allocator memcore.MarkRaw) memcore.MarkRaw {
	ptr := SlabAllocatorMallocUnsafe[T](allocator)
	memcore.MemoryClearNoHeapPointers(memcore.MemcoreMarkDereference(ptr), uintptr(headerSlotSizeGet[T](allocator)))
	return ptr
}

func SlabAllocatorFree[T any](allocator memcore.MarkRaw, slot memcore.MarkRaw) {
	header := memcore.MemcoreMarkDereferenceObject[FixedSlabAllocator[T]](allocator)
	_, slotDataIdx := slabAllocatorSlotIndexFromMark(header, slot)
	if slotDataIdx > header.slotCapacity-1 {
		panic(fmt.Errorf("invalid slot idx (%d) which is outside of capacity (%d)", slotDataIdx, header.slotCapacity))
	}

	memstruct.StackPushUnsafe(header.freeStack, slotDataIdx)
	memforgeAllocationRemove(allocator, slot)
}

func SlabAllocatorFreeUnsafe[T any](allocator memcore.MarkRaw, slot memcore.MarkRaw) {
	header := memcore.MemcoreMarkDereferenceObject[FixedSlabAllocator[T]](allocator)
	_, slotDataIdx := slabAllocatorSlotIndexFromMark(header, slot)

	memstruct.StackPushUnsafe(header.freeStack, slotDataIdx)
	memforgeAllocationRemove(allocator, slot)
}

func SlabAllocatorSlotSizeGet[T any](allocator memcore.MarkRaw) uint64 {
	header := memcore.MemcoreMarkDereferenceObject[FixedSlabAllocator[T]](allocator)
	return header.slotSize
}

func headerSlotSizeGet[T any](allocator memcore.MarkRaw) uint64 {
	return SlabAllocatorSlotSizeGet[T](allocator)
}

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

func slabAllocatorMarkFromSlotIndex[T any](header *FixedSlabAllocator[T], idx uint64) memcore.MarkRaw {
	offset := idx * header.slotSize
	return memcore.MemcoreMarkCreate(header.dataRegionID, uintptr(offset))
}

func slabAllocatorSlotIndexFromMark[T any](header *FixedSlabAllocator[T], slot memcore.MarkRaw) (dataOffset, slotDataIdx uint64) {
	if memcore.MemcoreMarkRegionIDGet(slot) != header.dataRegionID {
		panic(fmt.Errorf("slab allocator: mark belongs to foreign region"))
	}

	dataOffset = uint64(memcore.MemcoreMarkOffsetGet(slot))
	if dataOffset%header.slotSize != 0 {
		panic(fmt.Errorf("misaligned pointer given (not aligned)"))
	}

	return dataOffset, dataOffset / header.slotSize
}
