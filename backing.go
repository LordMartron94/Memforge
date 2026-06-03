package memforge

import "memcore"

/*
MemforgeDataBacking describes a pre-registered memcore data region for allocator construction.

[Context]
Used with CreateForDataRegion and ChainedLinearAllocatorCreate. The caller registers opaque or
CPU-mapped regions in memcore before passing them here. memforge never unregisters caller-owned regions.
*/
type MemforgeDataBacking struct {
	DataRegionID uint32
	DataCapBytes uint64
}

type linearAllocatorState struct {
	dataRegionID   uint32
	dataByteIdx    uint64
	dataCapBytes   uint64
	ownsDataRegion bool
	dataMmapBase   uintptr
	dataMmapSize   uint64
}

func linearAllocatorStateInit(
	backing MemforgeDataBacking,
	ownsDataRegion bool,
	dataMmapBase uintptr,
	dataMmapSize uint64,
) linearAllocatorState {
	return linearAllocatorState{
		dataRegionID:   backing.DataRegionID,
		dataCapBytes:   backing.DataCapBytes,
		dataByteIdx:    0,
		ownsDataRegion: ownsDataRegion,
		dataMmapBase:   dataMmapBase,
		dataMmapSize:   dataMmapSize,
	}
}

func linearAllocatorStateDestroy(state *linearAllocatorState) {
	if state == nil || !state.ownsDataRegion {
		return
	}
	memforgeDataRegionDestroy(state.dataRegionID, state.dataMmapBase, state.dataMmapSize)
	state.dataRegionID = 0
	state.dataMmapBase = 0
	state.dataMmapSize = 0
	state.ownsDataRegion = false
}

func linearAllocatorDataIdxGet(state *linearAllocatorState, requestedAlignment uint64) uint64 {
	return alignIdxUp(state.dataByteIdx, requestedAlignment)
}

func linearAllocatorCapacityGuarantee(state *linearAllocatorState, requestedSize, alignedIdx uint64) bool {
	return capacityGuarantee(alignedIdx, state.dataCapBytes, requestedSize)
}

func linearAllocatorIdxUpdate(state *linearAllocatorState, alignedIdx, sizeBytes uint64) {
	state.dataByteIdx = alignedIdx + sizeBytes
}

func linearAllocatorMallocMark(state *linearAllocatorState, alignedIdx uint64) memcore.MarkRaw {
	return memcore.MemcoreMarkCreate(state.dataRegionID, uintptr(alignedIdx))
}

func linearAllocatorResetState(state *linearAllocatorState) {
	state.dataByteIdx = 0
}

func memforgeDataBackingCreateFromMmap(sizeBytes uint64) (MemforgeDataBacking, uintptr, memcore.MemoryMap) {
	regionID, mmapBase, mmap := memforgeDataRegionCreate(sizeBytes)
	return MemforgeDataBacking{
		DataRegionID: regionID,
		DataCapBytes: sizeBytes,
	}, mmapBase, mmap
}

/*
MemforgeDataBackingCreateFromSubRegion registers a memcore alias window and returns backing for a child allocator.

The caller must unregister the sub-region when the child allocator is destroyed.
*/
func MemforgeDataBackingCreateFromSubRegion(windowStartMark memcore.MarkRaw, capacityBytes uint64) MemforgeDataBacking {
	subRegionID := memcore.MemcoreRegionRegisterSubRegionFromMark(windowStartMark, capacityBytes)
	return MemforgeDataBacking{
		DataRegionID: subRegionID,
		DataCapBytes: capacityBytes,
	}
}
