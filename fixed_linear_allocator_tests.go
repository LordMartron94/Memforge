package memforge

import (
	"fmt"
	foundationTesting "foundation/testing"
	"memcore"
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
}

// ------------------------ helpers (unexported) ------------------------

func testCreateAndDestroy(t *testing.T) {
	const sz = 4096
	a := FixedLinearAllocatorCreate(sz, "")
	FixedLinearAllocatorDestroy(a)
	mustPanic(t, func() { _ = FixedLinearAllocatorMalloc(a, 8, 8) })
	mustPanic(t, func() { FixedLinearAllocatorReset(a) })
	mustPanic(t, func() { _ = FixedLinearAllocatorCalloc(a, 16, 8) })
}

func testMallocAlignmentAndBump(t *testing.T) {
	const sz = 1 << 16
	a := FixedLinearAllocatorCreate(sz, "")
	defer FixedLinearAllocatorDestroy(a)

	p1 := FixedLinearAllocatorMalloc(a, 24, 8)
	p2 := FixedLinearAllocatorMalloc(a, 32, 16)

	r1 := memcore.MemcoreMarkDereference(p1)
	r2 := memcore.MemcoreMarkDereference(p2)

	foundationTesting.Assert(uintptr(r1)%8 == 0, fmt.Sprintf("p1 not 8-byte aligned: %v", r1), "p1 aligned to 8", t)
	foundationTesting.Assert(uintptr(r2)%16 == 0, fmt.Sprintf("p2 not 16-byte aligned: %v", r2), "p2 aligned to 16", t)

	s1 := unsafe.Slice((*byte)(r1), 24)
	s2 := unsafe.Slice((*byte)(r2), 32)
	fillBytes(s1, 0xAA)
	fillBytes(s2, 0xBB)
	foundationTesting.Assert(allEqual(s1, 0xAA), "s1 overlap", "s1 preserved", t)
	foundationTesting.Assert(allEqual(s2, 0xBB), "s2 overlap", "s2 preserved", t)
}

func testCapacityExhaustionPanics(t *testing.T) {
	const sz = 256
	a := FixedLinearAllocatorCreate(sz, "")
	defer FixedLinearAllocatorDestroy(a)

	_ = FixedLinearAllocatorMalloc(a, 128, 8)
	_ = FixedLinearAllocatorMalloc(a, 112, 16)

	mustPanic(t, func() { _ = FixedLinearAllocatorMalloc(a, 128, 8) })
}

func testZeroSizeAllocationNoBump(t *testing.T) {
	const sz = 1024
	a := FixedLinearAllocatorCreate(sz, "")
	defer FixedLinearAllocatorDestroy(a)

	p0 := FixedLinearAllocatorMalloc(a, 0, 64)
	p0b := FixedLinearAllocatorMalloc(a, 0, 64)
	pReal := FixedLinearAllocatorMalloc(a, 32, 64)

	foundationTesting.Assert(p0 == p0b, "zero-size bumped index", "zero-size stable", t)
	foundationTesting.Assert(p0 == pReal, "real alloc not reused", "real alloc reused", t)
}

func testCallocZeroes(t *testing.T) {
	const sz = 2048
	a := FixedLinearAllocatorCreate(sz, "")
	defer FixedLinearAllocatorDestroy(a)

	p := FixedLinearAllocatorCalloc(a, 128, 8)
	r := memcore.MemcoreMarkDereference(p)
	b := unsafe.Slice((*byte)(r), 128)

	foundationTesting.Assert(allEqual(b, 0x00), "calloc memory not zeroed", "calloc zeroed", t)
	fillBytes(b, 0x5A)
	foundationTesting.Assert(allEqual(b, 0x5A), "write failed", "write ok", t)
}

func testResetAllowsReuse(t *testing.T) {
	const sz = 4096
	a := FixedLinearAllocatorCreate(sz, "")
	defer FixedLinearAllocatorDestroy(a)

	p1 := FixedLinearAllocatorMalloc(a, 64, 32)

	_ = FixedLinearAllocatorMalloc(a, 128, 64)
	FixedLinearAllocatorReset(a)

	p1b := FixedLinearAllocatorMalloc(a, 64, 32)

	foundationTesting.Assert(p1 == p1b, "reset did not reuse", "reset reused", t)
}

func testInvalidAlignmentPanics(t *testing.T) {
	a := FixedLinearAllocatorCreate(1024, "")
	defer FixedLinearAllocatorDestroy(a)
	mustPanic(t, func() { _ = FixedLinearAllocatorMalloc(a, 8, 0) })
	mustPanic(t, func() { _ = FixedLinearAllocatorMalloc(a, 8, 24) })
}

func testMallocAndCallocObject(t *testing.T) {
	type pod struct {
		A uint64
		B uint32
		C uint16
		D byte
	}

	a := FixedLinearAllocatorCreate(4096, "")
	defer FixedLinearAllocatorDestroy(a)

	_, obj := FixedLinearAllocatorMallocObject[pod](a)
	obj.A, obj.B, obj.C, obj.D = 1, 2, 3, 4
	foundationTesting.Assert(obj.D == 4, "malloc object mismatch", "malloc ok", t)

	_, obj2 := FixedLinearAllocatorCallocObject[pod](a)
	foundationTesting.Assert(obj2.A == 0, "calloc not zeroed", "calloc ok", t)
	foundationTesting.Assert(obj != obj2, "malloc and calloc returned same addr", "distinct objects", t)
}

// ---------------- utilities ----------------

func mustPanic(t *testing.T, fn func()) {
	did := false
	defer func() {
		if r := recover(); r != nil {
			did = true
		}
		foundationTesting.Assert(did, "expected panic", "panic ok", t)
	}()
	fn()
}

func mustNotPanic(t *testing.T, fn func()) {
	did := false
	defer func() {
		if r := recover(); r != nil {
			did = true
		}
		foundationTesting.Assert(!did, "expected no panic", "panic occurred", t)
	}()
	fn()
}

func fillBytes(b []byte, v byte) {
	for i := range b {
		b[i] = v
	}
}

func allEqual(b []byte, v byte) bool {
	for _, x := range b {
		if x != v {
			return false
		}
	}
	return true
}
