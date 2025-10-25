package memforge

import (
	commontesting "foundation/testing"
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
	t.Run("use_after_destroy_panics", testManualUseAfterDestroyPanics)
	t.Run("double_free_panics", testManualDoubleFreePanics)
	t.Run("free_nonexistent_pointer_panics", testManualFreeNonexistentPanics)
}

// ---------------- helpers ----------------

func testManualCreateAndDestroy(t *testing.T) {
	a := FixedManualAllocatorCreate(1024)
	commontesting.Assert(a != nil, "allocator is nil after create", "allocator created", t)
	FixedManualAllocatorDestroy(a)
	commontesting.Assert(true, "destroy should not panic", "allocator destroyed", t)
}

func testManualMallocAlignmentAndBasicUse(t *testing.T) {
	a := FixedManualAllocatorCreate(4096)
	defer FixedManualAllocatorDestroy(a)

	// 1) allocate with 8-byte alignment
	p1 := FixedManualAllocatorMalloc(a, 64, 8)
	commontesting.Assert(uintptr(p1)%8 == 0, "p1 not 8-byte aligned", "p1 aligned to 8", t)

	// 2) allocate with 64-byte alignment
	p2 := FixedManualAllocatorMalloc(a, 128, 64)
	commontesting.Assert(uintptr(p2)%64 == 0, "p2 not 64-byte aligned", "p2 aligned to 64", t)

	// 3) verify independent regions
	s1 := unsafe.Slice((*byte)(p1), 64)
	s2 := unsafe.Slice((*byte)(p2), 128)
	fillBytes(s1, 0x11)
	fillBytes(s2, 0x22)

	commontesting.Assert(allEqual(s1, 0x11), "s1 overlap or corruption", "s1 preserved", t)
	commontesting.Assert(allEqual(s2, 0x22), "s2 overlap or corruption", "s2 preserved", t)
}

func testManualCallocZeroes(t *testing.T) {
	a := FixedManualAllocatorCreate(2048)
	defer FixedManualAllocatorDestroy(a)

	const n = 256
	p := FixedManualAllocatorCalloc(a, n, 16)
	s := unsafe.Slice((*byte)(p), n)
	commontesting.Assert(allEqual(s, 0x00), "calloc did not zero memory", "calloc zeroed", t)

	fillBytes(s, 0xAB)
	commontesting.Assert(allEqual(s, 0xAB), "write after calloc failed", "write after calloc ok", t)
}

func testManualFreeMergesAdjacentBlocks(t *testing.T) {
	a := FixedManualAllocatorCreate(1024)
	defer FixedManualAllocatorDestroy(a)

	// Allocate three blocks in sequence
	p1 := FixedManualAllocatorMalloc(a, 128, 8)
	p2 := FixedManualAllocatorMalloc(a, 128, 8)
	p3 := FixedManualAllocatorMalloc(a, 128, 8)

	// Free middle first
	FixedManualAllocatorFree(a, p2)
	// Free first → should merge p1+p2 region
	FixedManualAllocatorFree(a, p1)
	// Free third → should merge all into single free region
	FixedManualAllocatorFree(a, p3)

	// Allocate full again — should succeed if merge worked
	pFull := FixedManualAllocatorMalloc(a, 1024, 8)
	commontesting.Assert(pFull != nil, "merged region not allocated", "merged region reallocated", t)
}

func testManualFreeThenReallocate(t *testing.T) {
	a := FixedManualAllocatorCreate(2048)
	defer FixedManualAllocatorDestroy(a)

	p1 := FixedManualAllocatorMalloc(a, 256, 16)
	fillBytes(unsafe.Slice((*byte)(p1), 256), 0x77)

	FixedManualAllocatorFree(a, p1)
	p2 := FixedManualAllocatorMalloc(a, 256, 16)

	commontesting.Assert(p1 == p2, "freed region not reused", "freed region reused", t)
}

func testManualResetRestoresFullFreeRegion(t *testing.T) {
	a := FixedManualAllocatorCreate(2048)
	defer FixedManualAllocatorDestroy(a)

	_ = FixedManualAllocatorMalloc(a, 512, 8)
	_ = FixedManualAllocatorMalloc(a, 512, 8)
	FixedManualAllocatorReset(a)

	// Should be able to allocate full capacity again
	p := FixedManualAllocatorMalloc(a, 2048, 8)
	commontesting.Assert(p != nil, "reset did not restore full region", "reset restored full region", t)
}

func testManualFragmentationMergePattern(t *testing.T) {
	a := FixedManualAllocatorCreate(4096)
	defer FixedManualAllocatorDestroy(a)

	// Create a fragmentation pattern: alloc 4 equal blocks
	ptrs := make([]unsafe.Pointer, 4)
	for i := 0; i < 4; i++ {
		ptrs[i] = FixedManualAllocatorMalloc(a, 1024, 8)
	}

	// Free 0,2 then 1 then 3 → should merge into single big region
	FixedManualAllocatorFree(a, ptrs[0])
	FixedManualAllocatorFree(a, ptrs[2])
	FixedManualAllocatorFree(a, ptrs[1])
	FixedManualAllocatorFree(a, ptrs[3])

	// Try allocating the entire capacity again
	p := FixedManualAllocatorMalloc(a, 4096, 8)
	commontesting.Assert(p != nil, "fragmentation merge failed", "fragmentation merged successfully", t)
}

func testManualInvalidAlignmentPanics(t *testing.T) {
	a := FixedManualAllocatorCreate(1024)
	defer FixedManualAllocatorDestroy(a)

	mustPanic(t, func() { _ = FixedManualAllocatorMalloc(a, 8, 0) })
	mustPanic(t, func() { _ = FixedManualAllocatorMalloc(a, 8, 24) })
}

func testManualUseAfterDestroyPanics(t *testing.T) {
	a := FixedManualAllocatorCreate(1024)
	FixedManualAllocatorDestroy(a)

	mustPanic(t, func() { _ = FixedManualAllocatorMalloc(a, 8, 8) })
	mustPanic(t, func() { FixedManualAllocatorReset(a) })
	mustPanic(t, func() { _ = FixedManualAllocatorCalloc(a, 8, 8) })
}

func testManualDoubleFreePanics(t *testing.T) {
	a := FixedManualAllocatorCreate(1024)
	defer FixedManualAllocatorDestroy(a)

	p := FixedManualAllocatorMalloc(a, 128, 8)
	FixedManualAllocatorFree(a, p)
	mustPanic(t, func() { FixedManualAllocatorFree(a, p) })
}

func testManualFreeNonexistentPanics(t *testing.T) {
	a := FixedManualAllocatorCreate(1024)
	defer FixedManualAllocatorDestroy(a)

	// Create a fake pointer into the storage but not allocated
	fake := unsafe.Pointer(&a.storage[512])
	mustPanic(t, func() { FixedManualAllocatorFree(a, fake) })
}
