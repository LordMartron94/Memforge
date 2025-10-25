package memforge

import (
	commontesting "foundation/testing"
	"testing"
	"unsafe"
)

func TestDynamicLinearAllocator(t *testing.T) {
	t.Run("create_and_destroy", func(t *testing.T) { testDynamicCreateAndDestroy(t) })
	t.Run("malloc_alignment_and_bump", func(t *testing.T) { testDynamicMallocAlignmentAndBump(t) })
	t.Run("zero_size_allocation_no_bump", func(t *testing.T) { testDynamicZeroSizeAllocationNoBump(t) })
	t.Run("calloc_zeroes_memory", func(t *testing.T) { testDynamicCallocZeroes(t) })
	t.Run("reset_allows_reuse", func(t *testing.T) { testDynamicResetAllowsReuse(t) })
	t.Run("invalid_alignment_panics", func(t *testing.T) { testDynamicInvalidAlignmentPanics(t) })
	t.Run("malloc_object_and_calloc_object", func(t *testing.T) { testDynamicMallocAndCallocObject(t) })
	t.Run("use_after_destroy_panics", func(t *testing.T) { testDynamicUseAfterDestroyPanics(t) })
	t.Run("grows_when_capacity_exceeded", func(t *testing.T) { testDynamicGrowth(t) })
}

// ------------------------ helpers ------------------------

func testDynamicCreateAndDestroy(t *testing.T) {
	const sz = 4096
	a := DynamicLinearAllocatorCreate(sz, doublingGrowth)
	commontesting.Assert(a != nil, "allocator is nil after create", "allocator created", t)
	DynamicLinearAllocatorDestroy(a)
	commontesting.Assert(true, "destroy should not panic by itself", "allocator destroyed", t)
}

func testDynamicMallocAlignmentAndBump(t *testing.T) {
	const sz = 1 << 16
	a := DynamicLinearAllocatorCreate(sz, doublingGrowth)
	defer DynamicLinearAllocatorDestroy(a)

	p1 := DynamicLinearAllocatorMalloc(a, 24, 8)
	realP1 := DynamicLinearAllocatorRealPointerRetrieve(a, p1)
	commontesting.Assert(uintptr(realP1)%8 == 0, "p1 not 8-byte aligned", "p1 aligned to 8", t)

	p2 := DynamicLinearAllocatorMalloc(a, 32, 16)
	realP2 := DynamicLinearAllocatorRealPointerRetrieve(a, p2)
	commontesting.Assert(uintptr(realP2)%16 == 0, "p2 not 16-byte aligned", "p2 aligned to 16", t)

	s1 := unsafe.Slice((*byte)(realP1), 24)
	s2 := unsafe.Slice((*byte)(realP2), 32)
	fillBytes(s1, 0xAA)
	fillBytes(s2, 0xBB)
	commontesting.Assert(allEqual(s1, 0xAA), "s1 contents corrupted or overlap", "s1 preserved", t)
	commontesting.Assert(allEqual(s2, 0xBB), "s2 contents corrupted or overlap", "s2 preserved", t)
}

func testDynamicZeroSizeAllocationNoBump(t *testing.T) {
	const sz = 1024
	a := DynamicLinearAllocatorCreate(sz, doublingGrowth)
	defer DynamicLinearAllocatorDestroy(a)

	p0 := DynamicLinearAllocatorMalloc(a, 0, 64)
	p0Real := DynamicLinearAllocatorRealPointerRetrieve(a, p0)
	commontesting.Assert(uintptr(p0Real)%64 == 0, "p0 not 64-byte aligned", "p0 aligned to 64", t)

	p0b := DynamicLinearAllocatorMalloc(a, 0, 64)
	p0bReal := DynamicLinearAllocatorRealPointerRetrieve(a, p0b)
	commontesting.Assert(p0Real == p0bReal, "zero-size allocation bumped index", "zero-size did not bump", t)

	pReal := DynamicLinearAllocatorMalloc(a, 32, 64)
	pRealAddr := DynamicLinearAllocatorRealPointerRetrieve(a, pReal)
	commontesting.Assert(pRealAddr == p0Real, "real alloc after zero-size did not start at same aligned position", "real alloc reused aligned pos", t)
}

func testDynamicCallocZeroes(t *testing.T) {
	const sz = 2048
	a := DynamicLinearAllocatorCreate(sz, doublingGrowth)
	defer DynamicLinearAllocatorDestroy(a)

	const n = 128
	p := DynamicLinearAllocatorCalloc(a, n, 8)
	b := unsafe.Slice((*byte)(DynamicLinearAllocatorRealPointerRetrieve(a, p)), n)
	commontesting.Assert(allEqual(b, 0x00), "calloc memory not zeroed", "calloc memory zeroed", t)

	fillBytes(b, 0x5A)
	commontesting.Assert(allEqual(b, 0x5A), "write to calloc region failed", "write to calloc region ok", t)
}

func testDynamicResetAllowsReuse(t *testing.T) {
	const sz = 4096
	a := DynamicLinearAllocatorCreate(sz, doublingGrowth)
	defer DynamicLinearAllocatorDestroy(a)

	p1 := DynamicLinearAllocatorMalloc(a, 64, 32)
	p1Real := DynamicLinearAllocatorRealPointerRetrieve(a, p1)
	_ = DynamicLinearAllocatorMalloc(a, 128, 64)

	DynamicLinearAllocatorReset(a)
	p1b := DynamicLinearAllocatorMalloc(a, 64, 32)
	p1bReal := DynamicLinearAllocatorRealPointerRetrieve(a, p1b)

	commontesting.Assert(p1Real == p1bReal, "first pointer after reset differs; allocator did not reuse from start", "reset reused from start", t)
}

func testDynamicInvalidAlignmentPanics(t *testing.T) {
	const sz = 1024
	a := DynamicLinearAllocatorCreate(sz, doublingGrowth)
	defer DynamicLinearAllocatorDestroy(a)

	mustPanic(t, func() { _ = DynamicLinearAllocatorMalloc(a, 8, 0) })
	mustPanic(t, func() { _ = DynamicLinearAllocatorMalloc(a, 8, 24) })
}

func testDynamicMallocAndCallocObject(t *testing.T) {
	const sz = 4096
	a := DynamicLinearAllocatorCreate(sz, doublingGrowth)
	defer DynamicLinearAllocatorDestroy(a)

	type pod struct {
		A uint64
		B uint32
		C uint16
		D byte
	}

	// MallocObject
	ref := DynamicLinearAllocatorMallocObject[pod](a)
	obj := ref.Ptr(a)
	obj.A = 0xDEADBEEFCAFEBABE
	obj.B = 0xA1B2C3D4
	obj.C = 0xCCDD
	obj.D = 0x7F

	commontesting.Assert(obj.A == 0xDEADBEEFCAFEBABE, "obj.A mismatch", "obj.A ok", t)
	commontesting.Assert(obj.B == 0xA1B2C3D4, "obj.B mismatch", "obj.B ok", t)
	commontesting.Assert(obj.C == 0xCCDD, "obj.C mismatch", "obj.C ok", t)
	commontesting.Assert(obj.D == 0x7F, "obj.D mismatch", "obj.D ok", t)

	// CallocObject
	ref2 := DynamicLinearAllocatorCallocObject[pod](a)
	obj2 := ref2.Ptr(a)
	commontesting.Assert(obj2.A == 0 && obj2.B == 0 && obj2.C == 0 && obj2.D == 0,
		"calloc object not zero-initialized", "calloc object zero-initialized", t)

	commontesting.Assert(obj != obj2, "MallocObject and CallocObject returned same address", "distinct objects", t)
}

func testDynamicUseAfterDestroyPanics(t *testing.T) {
	const sz = 1024
	a := DynamicLinearAllocatorCreate(sz, doublingGrowth)
	DynamicLinearAllocatorDestroy(a)

	mustPanic(t, func() { _ = DynamicLinearAllocatorMalloc(a, 8, 8) })
	mustPanic(t, func() { DynamicLinearAllocatorReset(a) })
	mustPanic(t, func() { _ = DynamicLinearAllocatorCalloc(a, 16, 8) })
}

func testDynamicGrowth(t *testing.T) {
	const sz = 128
	a := DynamicLinearAllocatorCreate(sz, doublingGrowth)
	defer DynamicLinearAllocatorDestroy(a)

	// Fill initial capacity
	p1 := DynamicLinearAllocatorMalloc(a, sz-16, 8)
	p1Real := DynamicLinearAllocatorRealPointerRetrieve(a, p1)

	// Trigger growth by exceeding capacity
	p2 := DynamicLinearAllocatorMalloc(a, 256, 8)
	p2Real := DynamicLinearAllocatorRealPointerRetrieve(a, p2)

	// Growth occurred → baseAddr may have changed
	// Check p1 still points to same logical location
	p1RealAfter := DynamicLinearAllocatorRealPointerRetrieve(a, p1)
	commontesting.Assert(p1Real != nil && p1RealAfter != nil, "p1 became nil after growth", "p1 valid after growth", t)

	// The offsets should remain stable (p1 refers to same relative offset)
	// We can write to p1 before and after growth and check preservation
	b1 := unsafe.Slice((*byte)(p1Real), 16)
	fillBytes(b1, 0xEE)

	b1After := unsafe.Slice((*byte)(p1RealAfter), 16)
	commontesting.Assert(allEqual(b1After, 0xEE), "data in p1 corrupted after growth", "p1 data preserved after growth", t)

	commontesting.Assert(p2Real != nil, "p2 allocation after growth failed", "p2 allocation after growth succeeded", t)
}

// ------------------------ tiny utilities ------------------------

// doublingGrowth is a simple growth strategy for testing.
func doublingGrowth(currentCap, neededCap uint64) uint64 {
	newCap := currentCap
	for newCap < neededCap {
		newCap *= 2
	}
	return newCap
}
