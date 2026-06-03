package memforge

import (
	"fmt"
	"memcore"
	"memstruct"
)

/*
ArenaGrowthHook provisions a new discrete data region when a chained linear allocator exhausts its active slab.

[Parameters]
minCapacityBytes - Minimum byte capacity required for the new region.

[Returns]
The new memcore region ID and its capacity in bytes.
*/
type ArenaGrowthHook func(minCapacityBytes uint64) (newRegionID uint32, capacityBytes uint64)

/*
ChainedLinearAllocator is a bump allocator that chains immutable data regions via a growth hook.

[Context]
Unlike DynamicLinearAllocator, growth never extends or copies the active region. When the active slab
is full, the hook provisions a new region and allocation continues at offset zero in that region.
Every returned mark lies entirely within one physical region.

[Complexity]
Malloc: O(1) amortized; growth is O(1) without data copy.
Reset: O(1) when regions are retained in history.

[Thread Safety]
Not safe for concurrent use from multiple goroutines.
*/
type ChainedLinearAllocator struct {
	activeRegionID uint32
	bumpIndex      uint64
	activeCapacity uint64
	growthHookID   memcore.FunctionID
	regionHistory  *memstruct.FixedOrderedList[uint32] // co-located in header-store metadata
}

/*
ChainedLinearAllocatorCreate initializes a chained linear allocator over caller-owned regions.

[Parameters]
initial - First data region already registered in memcore.
growth - Hook invoked when the active slab cannot satisfy an allocation.
tag - Human-readable label recorded by the memforge debugger (for example "Host Staging Pool").

[Returns]
A memcore.MarkRaw to the allocator header in the memforge header store.
*/
func ChainedLinearAllocatorCreate(initial MemforgeDataBacking, growth ArenaGrowthHook, tag string) memcore.MarkRaw {
	growthHookID := memcore.MemcoreFunctionRegisterTyped(growth)
	return chainedLinearAllocatorCreateInternal(initial, growthHookID, tag)
}

func chainedLinearAllocatorCreateInternal(initial MemforgeDataBacking, growthHookID memcore.FunctionID, tag string) memcore.MarkRaw {
	const maxHistoryRegions = 256

	headerMark := memforgeHeaderAllocate(
		uint64(memcore.SizeOf[ChainedLinearAllocator]()),
		uint64(memcore.AlignOf[ChainedLinearAllocator]()),
	)

	historyBytes := memstruct.FixedOrderedListRequiredBytes[uint32](maxHistoryRegions)
	historyAlign := memstruct.FixedOrderedListRequiredAlignment[uint32]()
	metaMark := memforgeHeaderAllocate(historyBytes, historyAlign)
	memstruct.FixedOrderedListInitializeAt[uint32](metaMark, maxHistoryRegions)

	header := memcore.MemcoreMarkDereferenceObject[ChainedLinearAllocator](headerMark)
	header.activeRegionID = initial.DataRegionID
	header.activeCapacity = initial.DataCapBytes
	header.bumpIndex = 0
	header.growthHookID = growthHookID
	header.regionHistory = memcore.MemcoreMarkDereferenceObjectUnsafe[memstruct.FixedOrderedList[uint32]](metaMark)
	memstruct.FixedOrderedListAppendUnsafeFast(header.regionHistory, initial.DataRegionID)

	memforgeAllocatorRegister(headerMark, "Chained Linear", tag, initial.DataRegionID, initial.DataCapBytes, initial.DataCapBytes)
	return headerMark
}

/*
ChainedLinearAllocatorDestroy clears telemetry and header storage only.

[Side Effects]
Does not unregister caller-owned data regions. The caller must free physical memory separately.
*/
func ChainedLinearAllocatorDestroy(allocator memcore.MarkRaw) {
	memforgeAllocatorDestroy(allocator)
}

/*
ChainedLinearAllocatorMalloc allocates sizeBytes with alignment from the active slab or a new slab from the hook.

[Returns]
A memcore.MarkRaw in the active data region namespace.

[Errors]
Panics when alignment is invalid or the growth hook cannot satisfy the request.
*/
//go:nosplit
func ChainedLinearAllocatorMalloc(allocator memcore.MarkRaw, sizeBytes, alignment uint64) memcore.MarkRaw {
	header := memcore.MemcoreMarkDereferenceObject[ChainedLinearAllocator](allocator)
	alignmentValidate(alignment)

	alignedIdx := alignIdxUp(header.bumpIndex, alignment)
	if !capacityGuarantee(alignedIdx, header.activeCapacity, sizeBytes) {
		chainedLinearAllocatorPivot(header, sizeBytes)
		alignedIdx = alignIdxUp(header.bumpIndex, alignment)
		if !capacityGuarantee(alignedIdx, header.activeCapacity, sizeBytes) {
			panic(fmt.Errorf("chained linear allocator: growth hook returned insufficient capacity for size %d", sizeBytes))
		}
	}

	ptr := memcore.MemcoreMarkCreate(header.activeRegionID, uintptr(alignedIdx))
	memforgeAllocationAdd(allocator, ptr, sizeBytes)
	header.bumpIndex = alignedIdx + sizeBytes
	return ptr
}

/*
ChainedLinearAllocatorReset reuses provisioned regions by rewinding to the first region at offset zero.

[Complexity]
Time: O(1).

[Side Effects]
Clears memforge allocation tracking. Retains region history for subsequent frames.
*/
func ChainedLinearAllocatorReset(allocator memcore.MarkRaw) {
	header := memcore.MemcoreMarkDereferenceObject[ChainedLinearAllocator](allocator)
	memforgeAllocatorRemoveAll(allocator)

	if memstruct.FixedOrderedListLengthGetFast(header.regionHistory) == 0 {
		header.bumpIndex = 0
		return
	}

	firstRegion, err := memstruct.FixedOrderedListItemGetAtFast(header.regionHistory, 0)
	if err != nil {
		panic(fmt.Errorf("chained linear allocator: invalid region history: %w", err))
	}

	header.activeRegionID = firstRegion
	header.bumpIndex = 0

	capacity, err := chainedLinearAllocatorRegionCapacityGet(firstRegion)
	if err != nil {
		panic(err)
	}
	header.activeCapacity = capacity
}

func chainedLinearAllocatorRegionCapacityGet(regionID uint32) (uint64, error) {
	if !memcore.MemcoreMarkIsValid(memcore.MemcoreMarkCreate(regionID, 0)) {
		return 0, fmt.Errorf("chained linear allocator: inactive region %d", regionID)
	}
	return memcore.MemcoreRegionSizeGet(regionID), nil
}

func chainedLinearAllocatorPivot(header *ChainedLinearAllocator, requestedSize uint64) {
	hook := memcore.MemcoreFunctionRetrieveTyped[ArenaGrowthHook](header.growthHookID)
	newRegionID, newCapacity := hook(requestedSize)
	if newCapacity < requestedSize {
		panic(fmt.Errorf("chained linear allocator: growth hook returned capacity %d < requested %d", newCapacity, requestedSize))
	}

	historyLen := memstruct.FixedOrderedListLengthGetFast(header.regionHistory)
	if historyLen > 0 {
		lastRegion, err := memstruct.FixedOrderedListItemGetAtFast(header.regionHistory, historyLen-1)
		if err != nil {
			panic(fmt.Errorf("chained linear allocator: invalid region history: %w", err))
		}
		if lastRegion != header.activeRegionID {
			memstruct.FixedOrderedListAppendUnsafeFast(header.regionHistory, header.activeRegionID)
		}
	} else {
		memstruct.FixedOrderedListAppendUnsafeFast(header.regionHistory, header.activeRegionID)
	}

	header.activeRegionID = newRegionID
	header.activeCapacity = newCapacity
	header.bumpIndex = 0

	memstruct.FixedOrderedListAppendUnsafeFast(header.regionHistory, newRegionID)
}
