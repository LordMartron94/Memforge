package memforge

import (
	foundationTesting "foundation/testing"
	"memcore"
	"testing"
)

func TestChainedLinearAllocator(t *testing.T) {
	memcore.MemcoreMarkManagementStateReset(false)

	const slabSize = 256
	regionCounter := uint32(0)

	nextRegion := func(sizeBytes uint64) (uint32, uint64) {
		regionCounter++
		capacity := slabSize
		if sizeBytes > uint64(capacity) {
			capacity = int(sizeBytes)
		}
		regionID := memcore.MemcoreRegionRegisterOpaque(uint64(capacity))
		return regionID, uint64(capacity)
	}

	firstID, firstCap := nextRegion(0)
	allocator := ChainedLinearAllocatorCreate(
		MemforgeDataBacking{DataRegionID: firstID, DataCapBytes: firstCap},
		nextRegion,
		"",
	)
	defer ChainedLinearAllocatorDestroy(allocator)

	p1 := ChainedLinearAllocatorMalloc(allocator, 64, 8)
	foundationTesting.Assert(memcore.MemcoreMarkRegionIDGet(p1) == firstID, "first slab region mismatch", "first slab region", t)

	_ = ChainedLinearAllocatorMalloc(allocator, 200, 8)
	p3 := ChainedLinearAllocatorMalloc(allocator, 32, 8)
	secondRegion := memcore.MemcoreMarkRegionIDGet(p3)
	foundationTesting.Assert(secondRegion != firstID, "expected new slab region", "new slab region", t)
	foundationTesting.Assert(memcore.MemcoreMarkOffsetGet(p3) == 0, "expected offset zero in new slab", "offset zero", t)

	ChainedLinearAllocatorReset(allocator)
	p4 := ChainedLinearAllocatorMalloc(allocator, 16, 8)
	foundationTesting.Assert(memcore.MemcoreMarkRegionIDGet(p4) == firstID, "reset did not reuse first region", "reset reused first region", t)
}

func TestFixedLinearAllocatorOpaqueDataRegion(t *testing.T) {
	memcore.MemcoreMarkManagementStateReset(false)

	regionID := memcore.MemcoreRegionRegisterOpaque(4096)
	allocator := FixedLinearAllocatorCreateForDataRegion(MemforgeDataBacking{
		DataRegionID: regionID,
		DataCapBytes: 4096,
	}, "")
	defer FixedLinearAllocatorDestroy(allocator)

	mark := FixedLinearAllocatorMalloc(allocator, 128, 16)
	foundationTesting.Assert(memcore.MemcoreMarkRegionIDGet(mark) == regionID, "wrong region", "correct region", t)
	foundationTesting.Assert(memcore.MemcoreMarkRegionIsOpaque(mark), "mark not opaque", "opaque mark", t)
}
