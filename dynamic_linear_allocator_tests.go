package memforge

import (
	commontesting "foundation/testing"
	"memcore"
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
	t.Run("grows_when_capacity_exceeded", func(t *testing.T) { testDynamicGrowth(t) })
}

// ------------------------ helpers ------------------------

func testDynamicCreateAndDestroy(t *testing.T) {
	const sz = 4096
	a := DynamicLinearAllocatorCreate(sz, growthStrategyIDTests)
	commontesting.Assert(a.IsValid(), "allocator pointer invalid after create", "allocator created", t)
	DynamicLinearAllocatorDestroy(a)

	mustPanic(t, func() { _ = DynamicLinearAllocatorMalloc(a, 8, 8) })
	mustPanic(t, func() { DynamicLinearAllocatorReset(a) })
}

func testDynamicMallocAlignmentAndBump(t *testing.T) {
	const sz = 1 << 16
	a := DynamicLinearAllocatorCreate(sz, growthStrategyIDTests)
	defer DynamicLinearAllocatorDestroy(a)

	p1 := DynamicLinearAllocatorMalloc(a, 24, 8)
	p2 := DynamicLinearAllocatorMalloc(a, 32, 16)

	realP1 := memcore.MemcorePointerDereferenceRaw(p1)
	realP2 := memcore.MemcorePointerDereferenceRaw(p2)

	commontesting.Assert(uintptr(realP1)%8 == 0, "p1 not 8-byte aligned", "p1 aligned to 8", t)
	commontesting.Assert(uintptr(realP2)%16 == 0, "p2 not 16-byte aligned", "p2 aligned to 16", t)

	s1 := unsafe.Slice((*byte)(realP1), 24)
	s2 := unsafe.Slice((*byte)(realP2), 32)
	fillBytes(s1, 0xAA)
	fillBytes(s2, 0xBB)
	commontesting.Assert(allEqual(s1, 0xAA), "s1 corrupted or overlap", "s1 preserved", t)
	commontesting.Assert(allEqual(s2, 0xBB), "s2 corrupted or overlap", "s2 preserved", t)
}

func testDynamicZeroSizeAllocationNoBump(t *testing.T) {
	const sz = 1024
	a := DynamicLinearAllocatorCreate(sz, growthStrategyIDTests)
	defer DynamicLinearAllocatorDestroy(a)

	p0 := DynamicLinearAllocatorMalloc(a, 0, 64)
	p0b := DynamicLinearAllocatorMalloc(a, 0, 64)
	pReal := DynamicLinearAllocatorMalloc(a, 32, 64)

	addr0 := memcore.PointerOffset(p0)
	addr0b := memcore.PointerOffset(p0b)
	addrReal := memcore.PointerOffset(pReal)

	commontesting.Assert(addr0 == addr0b, "zero-size allocation bumped index", "zero-size did not bump", t)
	commontesting.Assert(addr0 == addrReal, "real alloc after zero-size did not reuse aligned pos", "real alloc reused aligned pos", t)
}

func testDynamicCallocZeroes(t *testing.T) {
	const sz = 2048
	a := DynamicLinearAllocatorCreate(sz, growthStrategyIDTests)
	defer DynamicLinearAllocatorDestroy(a)

	const n = 128
	p := DynamicLinearAllocatorCalloc(a, n, 8)
	b := unsafe.Slice((*byte)(memcore.MemcorePointerDereferenceRaw(p)), n)
	commontesting.Assert(allEqual(b, 0x00), "calloc memory not zeroed", "calloc zeroed", t)

	fillBytes(b, 0x5A)
	commontesting.Assert(allEqual(b, 0x5A), "write to calloc region failed", "calloc region writable", t)
}

func testDynamicResetAllowsReuse(t *testing.T) {
	const sz = 4096
	a := DynamicLinearAllocatorCreate(sz, growthStrategyIDTests)
	defer DynamicLinearAllocatorDestroy(a)

	p1 := DynamicLinearAllocatorMalloc(a, 64, 32)
	addr1 := memcore.PointerOffset(p1)

	_ = DynamicLinearAllocatorMalloc(a, 128, 64)
	DynamicLinearAllocatorReset(a)

	p1b := DynamicLinearAllocatorMalloc(a, 64, 32)
	addr1b := memcore.PointerOffset(p1b)

	commontesting.Assert(addr1 == addr1b, "reset did not reuse from start", "reset reused from start", t)
}

func testDynamicInvalidAlignmentPanics(t *testing.T) {
	a := DynamicLinearAllocatorCreate(1024, growthStrategyIDTests)
	defer DynamicLinearAllocatorDestroy(a)
	mustPanic(t, func() { _ = DynamicLinearAllocatorMalloc(a, 8, 0) })
	mustPanic(t, func() { _ = DynamicLinearAllocatorMalloc(a, 8, 24) })
}

func testDynamicMallocAndCallocObject(t *testing.T) {
	type pod struct {
		A uint64
		B uint32
		C uint16
		D byte
	}

	a := DynamicLinearAllocatorCreate(4096, growthStrategyIDTests)
	defer DynamicLinearAllocatorDestroy(a)

	ref := DynamicLinearAllocatorMallocObject[pod](a)
	obj := memcore.MemcorePointerDereferenceObjectUnsafe[pod](ref)

	obj.A, obj.B, obj.C, obj.D = 0xDEADBEEFCAFEBABE, 0xA1B2C3D4, 0xCCDD, 0x7F
	commontesting.Assert(obj.D == 0x7F, "malloc object corrupted", "malloc object ok", t)

	ref2 := DynamicLinearAllocatorCallocObject[pod](a)
	obj2 := memcore.MemcorePointerDereferenceObjectUnsafe[pod](ref2)
	commontesting.Assert(obj2.A == 0, "calloc not zeroed", "calloc zeroed", t)
}

func testDynamicGrowth(t *testing.T) {
	const sz = 128
	a := DynamicLinearAllocatorCreate(sz, growthStrategyIDTests)
	defer DynamicLinearAllocatorDestroy(a)

	p1 := DynamicLinearAllocatorMalloc(a, sz-16, 8)
	p2 := DynamicLinearAllocatorMalloc(a, 256, 8)

	r1 := memcore.MemcorePointerDereferenceRaw(p1)
	r2 := memcore.MemcorePointerDereferenceRaw(p2)
	commontesting.Assert(r1 != nil && r2 != nil, "growth invalid pointers", "growth ok", t)
}

func doublingGrowth(cur, need uint64) uint64 {
	for cur < need {
		cur *= 2
	}
	return cur
}

var growthStrategyIDTests memcore.FunctionID = memcore.MemcoreFunctionRegisterTyped[GrowthStrategy](doublingGrowth)
