package memforge

import (
	foundationTesting "foundation/testing"
	"testing"
	"unsafe"
)

func TestLinearAllocator(t *testing.T) {

	t.Run("create_and_destroy", func(t *testing.T) { testCreateAndDestroy(t) })
	t.Run("malloc_alignment_and_bump", func(t *testing.T) { testMallocAlignmentAndBump(t) })
	t.Run("capacity_exhaustion_panics", func(t *testing.T) { testCapacityExhaustionPanics(t) })
	t.Run("zero_size_allocation_no_bump", func(t *testing.T) { testZeroSizeAllocationNoBump(t) })
	t.Run("calloc_zeroes_memory", func(t *testing.T) { testCallocZeroes(t) })
	t.Run("reset_allows_reuse", func(t *testing.T) { testResetAllowsReuse(t) })
	t.Run("invalid_alignment_panics", func(t *testing.T) { testInvalidAlignmentPanics(t) })
	t.Run("malloc_object_and_calloc_object", func(t *testing.T) { testMallocAndCallocObject(t) })
	t.Run("use_after_destroy_panics", func(t *testing.T) { testUseAfterDestroyPanics(t) })
}

// ------------------------ helpers (unexported) ------------------------

func testCreateAndDestroy(t *testing.T) {
	const sz = 4096
	a := FixedLinearAllocatorCreate(sz)
	foundationTesting.Assert(a != nil, "allocator is nil after create", "allocator created", t)
	FixedLinearAllocatorDestroy(a)
	foundationTesting.Assert(true, "destroy should not panic by itself", "allocator destroyed", t)
}

func testMallocAlignmentAndBump(t *testing.T) {
	const sz = 1 << 16
	a := FixedLinearAllocatorCreate(sz)
	defer FixedLinearAllocatorDestroy(a)

	// 1) Basic alignment check
	p1 := FixedLinearAllocatorMalloc(a, 24, 8)
	foundationTesting.Assert(uintptr(p1)%8 == 0, "p1 not 8-byte aligned", "p1 aligned to 8", t)

	// 2) Next allocation with stricter alignment
	p2 := FixedLinearAllocatorMalloc(a, 32, 16)
	foundationTesting.Assert(uintptr(p2)%16 == 0, "p2 not 16-byte aligned", "p2 aligned to 16", t)

	// 3) Ensure non-overlap: write to both regions and verify they keep values
	s1 := unsafe.Slice((*byte)(p1), 24)
	s2 := unsafe.Slice((*byte)(p2), 32)
	fillBytes(s1, 0xAA)
	fillBytes(s2, 0xBB)
	foundationTesting.Assert(allEqual(s1, 0xAA), "s1 contents corrupted or overlap", "s1 preserved", t)
	foundationTesting.Assert(allEqual(s2, 0xBB), "s2 contents corrupted or overlap", "s2 preserved", t)
}

func testCapacityExhaustionPanics(t *testing.T) {
	const sz = 256
	a := FixedLinearAllocatorCreate(sz)
	defer FixedLinearAllocatorDestroy(a)

	// Allocate almost everything
	_ = FixedLinearAllocatorMalloc(a, 128, 8)
	_ = FixedLinearAllocatorMalloc(a, 112, 16) // leaves not much (depending on padding)

	// Next big allocation should panic
	mustPanic(t, func() { _ = FixedLinearAllocatorMalloc(a, 128, 8) })
}

func testZeroSizeAllocationNoBump(t *testing.T) {
	const sz = 1024
	a := FixedLinearAllocatorCreate(sz)
	defer FixedLinearAllocatorDestroy(a)

	// First, do a zero-sized allocation with alignment 64
	p0 := FixedLinearAllocatorMalloc(a, 0, 64)
	foundationTesting.Assert(uintptr(p0)%64 == 0, "p0 not 64-byte aligned", "p0 aligned to 64", t)

	// Another zero-sized allocation with same alignment should return same pointer (no bump)
	p0b := FixedLinearAllocatorMalloc(a, 0, 64)
	foundationTesting.Assert(p0 == p0b, "zero-size allocation bumped index", "zero-size did not bump", t)

	// Now do a real allocation with same alignment and ensure it returns the same place,
	// since idx should not have moved yet.
	pReal := FixedLinearAllocatorMalloc(a, 32, 64)
	foundationTesting.Assert(pReal == p0, "real alloc after zero-size did not start at same aligned position", "real alloc reused aligned pos", t)
}

func testCallocZeroes(t *testing.T) {
	const sz = 2048
	a := FixedLinearAllocatorCreate(sz)
	defer FixedLinearAllocatorDestroy(a)

	const n = 128
	p := FixedLinearAllocatorCalloc(a, n, 8)
	b := unsafe.Slice((*byte)(p), n)
	foundationTesting.Assert(allEqual(b, 0x00), "calloc memory not zeroed", "calloc memory zeroed", t)

	// Write and ensure values stick
	fillBytes(b, 0x5A)
	foundationTesting.Assert(allEqual(b, 0x5A), "write to calloc region failed", "write to calloc region ok", t)
}

func testResetAllowsReuse(t *testing.T) {
	const sz = 4096
	a := FixedLinearAllocatorCreate(sz)
	defer FixedLinearAllocatorDestroy(a)

	// First allocation sequence
	p1 := FixedLinearAllocatorMalloc(a, 64, 32)
	_ = FixedLinearAllocatorMalloc(a, 128, 64)

	// Reset and allocate same as first — should return same aligned pointer
	FixedLinearAllocatorReset(a)
	p1b := FixedLinearAllocatorMalloc(a, 64, 32)
	foundationTesting.Assert(p1 == p1b, "first pointer after reset differs; allocator did not reuse from start", "reset reused from start", t)
}

func testInvalidAlignmentPanics(t *testing.T) {
	const sz = 1024
	a := FixedLinearAllocatorCreate(sz)
	defer FixedLinearAllocatorDestroy(a)

	// alignment = 0
	mustPanic(t, func() { _ = FixedLinearAllocatorMalloc(a, 8, 0) })

	// alignment not power of two (e.g., 24)
	mustPanic(t, func() { _ = FixedLinearAllocatorMalloc(a, 8, 24) })
}

func testMallocAndCallocObject(t *testing.T) {
	const sz = 4096
	a := FixedLinearAllocatorCreate(sz)
	defer FixedLinearAllocatorDestroy(a)

	// Plain scalar-only struct (no pointers!)
	type pod struct {
		A uint64
		B uint32
		C uint16
		D byte
	}

	// MallocObject: not zeroed
	obj := FixedLinearAllocatorMallocObject[pod](a)
	// we can set fields and read them back
	obj.A = 0xDEADBEEFCAFEBABE
	obj.B = 0xA1B2C3D4
	obj.C = 0xCCDD
	obj.D = 0x7F

	foundationTesting.Assert(obj.A == 0xDEADBEEFCAFEBABE, "obj.A mismatch", "obj.A ok", t)
	foundationTesting.Assert(obj.B == 0xA1B2C3D4, "obj.B mismatch", "obj.B ok", t)
	foundationTesting.Assert(obj.C == 0xCCDD, "obj.C mismatch", "obj.C ok", t)
	foundationTesting.Assert(obj.D == 0x7F, "obj.D mismatch", "obj.D ok", t)

	// CallocObject: zeroed
	obj2 := FixedLinearAllocatorCallocObject[pod](a)
	foundationTesting.Assert(obj2.A == 0 && obj2.B == 0 && obj2.C == 0 && obj2.D == 0,
		"calloc object not zero-initialized", "calloc object zero-initialized", t)

	// Ensure distinct objects (different addresses)
	foundationTesting.Assert(obj != obj2, "MallocObject and CallocObject returned same address", "distinct objects", t)
}

func testUseAfterDestroyPanics(t *testing.T) {
	const sz = 1024
	a := FixedLinearAllocatorCreate(sz)
	FixedLinearAllocatorDestroy(a)

	mustPanic(t, func() { _ = FixedLinearAllocatorMalloc(a, 8, 8) })
	mustPanic(t, func() { FixedLinearAllocatorReset(a) })
	mustPanic(t, func() { _ = FixedLinearAllocatorCalloc(a, 16, 8) })
}

// ------------------------ tiny utilities ------------------------

func mustPanic(t *testing.T, fn func()) {
	didPanic := false
	defer func() {
		if r := recover(); r != nil {
			didPanic = true
		}
		foundationTesting.Assert(
			didPanic,
			"expected panic but none occurred",
			"panic occurred as expected",
			t,
		)
	}()
	fn()
}

func fillBytes(b []byte, v byte) {
	for i := range b {
		b[i] = v
	}
}

func allEqual(b []byte, v byte) bool {
	for i := range b {
		if b[i] != v {
			return false
		}
	}
	return true
}
