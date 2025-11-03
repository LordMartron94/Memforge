package memforge

import (
	commontesting "foundation/testing"
	"memcore"
	"testing"
	"unsafe"
)

func TestFixedManualAllocator(t *testing.T) {
	t.Run("create_and_destroy", testManualCreateAndDestroy)
	t.Run("malloc_alignment_and_basic_use", testManualMallocAlignmentAndBasicUse)
	t.Run("calloc_zeroes_memory", testManualCallocZeroes)
	t.Run("free_merges_adjacent_blocks", testManualFreeMergesAdjacentBlocks)
	t.Run("free_then_reallocate_reuses_block", testManualFreeThenReallocate)
	t.Run("reset_restores_full_free_region", testManualResetRestoresFullFreeRegion)
	t.Run("fragmentation_and_merge_pattern", testManualFragmentationMergePattern)
	t.Run("invalid_alignment_panics", testManualInvalidAlignmentPanics)
}

func testManualCreateAndDestroy(t *testing.T) {
	a := FixedManualAllocatorCreate(1024)
	commontesting.Assert(a.IsValid(), "allocator pointer invalid", "allocator created", t)

	mustNotPanic(t, func() {
		FixedManualAllocatorDestroy(a)
	})

	mustPanic(t, func() { _ = FixedManualAllocatorMalloc(a, 8, 8) })
	mustPanic(t, func() { FixedManualAllocatorReset(a) })
}

func testManualMallocAlignmentAndBasicUse(t *testing.T) {
	a := FixedManualAllocatorCreate(4096)
	defer FixedManualAllocatorDestroy(a)

	p1 := FixedManualAllocatorMalloc(a, 64, 8)
	p2 := FixedManualAllocatorMalloc(a, 128, 64)

	r1 := memcore.MemcorePointerDereferenceRaw(p1)
	r2 := memcore.MemcorePointerDereferenceRaw(p2)

	commontesting.Assert(uintptr(r1)%8 == 0, "p1 not aligned", "p1 aligned", t)
	commontesting.Assert(uintptr(r2)%64 == 0, "p2 not aligned", "p2 aligned", t)
}

func testManualCallocZeroes(t *testing.T) {
	a := FixedManualAllocatorCreate(2048)
	defer FixedManualAllocatorDestroy(a)

	p := FixedManualAllocatorCalloc(a, 256, 16)
	r := memcore.MemcorePointerDereferenceRaw(p)
	buf := unsafe.Slice((*byte)(r), 256)
	commontesting.Assert(allEqual(buf, 0x00), "calloc not zeroed", "calloc ok", t)
}

func testManualFreeMergesAdjacentBlocks(t *testing.T) {
	a := FixedManualAllocatorCreate(1024)
	defer FixedManualAllocatorDestroy(a)

	p1 := FixedManualAllocatorMalloc(a, 128, 8)
	p2 := FixedManualAllocatorMalloc(a, 128, 8)
	p3 := FixedManualAllocatorMalloc(a, 128, 8)

	// Free in non-sequential order to test merging
	FixedManualAllocatorFree(a, p2)
	FixedManualAllocatorFree(a, p1)
	FixedManualAllocatorFree(a, p3)

	// If blocks merged correctly, we should be able to allocate the full capacity
	_ = FixedManualAllocatorMalloc(a, 1024, 8)
}

func testManualFreeThenReallocate(t *testing.T) {
	a := FixedManualAllocatorCreate(2048)
	defer FixedManualAllocatorDestroy(a)

	p1 := FixedManualAllocatorMalloc(a, 256, 16)
	FixedManualAllocatorFree(a, p1)
	p2 := FixedManualAllocatorMalloc(a, 256, 16)
	commontesting.Assert(memcore.PointerOffset(p1) == memcore.PointerOffset(p2),
		"freed region not reused", "freed region reused", t)
}

func testManualResetRestoresFullFreeRegion(t *testing.T) {
	a := FixedManualAllocatorCreate(2048)
	defer FixedManualAllocatorDestroy(a)

	_ = FixedManualAllocatorMalloc(a, 512, 8)
	_ = FixedManualAllocatorMalloc(a, 512, 8)

	FixedManualAllocatorReset(a)

	p := FixedManualAllocatorMalloc(a, 2048, 8)
	commontesting.Assert(p.IsValid(), "reset did not restore region", "reset ok", t)
}

func testManualFragmentationMergePattern(t *testing.T) {
	a := FixedManualAllocatorCreate(4096)
	defer FixedManualAllocatorDestroy(a)

	ptrs := make([]memcore.Pointer, 4)
	for i := 0; i < 4; i++ {
		ptrs[i] = FixedManualAllocatorMalloc(a, 1024, 8)
	}

	// Free in alternating pattern, then complete
	FixedManualAllocatorFree(a, ptrs[0])
	FixedManualAllocatorFree(a, ptrs[2])
	FixedManualAllocatorFree(a, ptrs[1])
	FixedManualAllocatorFree(a, ptrs[3])

	p := FixedManualAllocatorMalloc(a, 4096, 8)
	commontesting.Assert(p.IsValid(), "fragmentation merge failed", "merge ok", t)

}

func testManualInvalidAlignmentPanics(t *testing.T) {
	a := FixedManualAllocatorCreate(1024)
	defer FixedManualAllocatorDestroy(a)
	mustPanic(t, func() { _ = FixedManualAllocatorMalloc(a, 8, 0) })
	mustPanic(t, func() { _ = FixedManualAllocatorMalloc(a, 8, 24) })
}
