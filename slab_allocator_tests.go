package memforge

import (
	commontesting "foundation/testing"
	"math/rand"
	"testing"
	"unsafe"
)

// testSlabStruct is a helper struct used for allocation tests.
// Its size and alignment are non-trivial.
type testSlabStruct struct {
	val1 uint64
	val2 float64
	val3 [16]byte
}

// TestFixedSlabAllocator is the main test runner for the slab allocator.
func TestFixedSlabAllocator(t *testing.T) {
	t.Run("create_and_destroy", testSlabCreateAndDestroy)
	t.Run("create_with_zero_capacity_panics", testSlabCreateWithZeroCapacityPanics)
	t.Run("malloc_and_calloc_objects", testSlabMallocAndCallocObjects)
	t.Run("calloc_zeroes_memory", testSlabCallocZeroesMemory)
	t.Run("allocation_exhaustion_panics", testSlabAllocationExhaustionPanics)
	t.Run("reset_allows_full_reuse", testSlabResetAllowsFullReuse)
	t.Run("unsafe_functions_work_as_expected", testSlabUnsafeFunctionsWork)
	t.Run("use_after_destroy_panics", testSlabUseAfterDestroyPanics)
}

// ---------------- Test Cases ----------------

func testSlabCreateAndDestroy(t *testing.T) {
	a := SlabAllocatorCreate[testSlabStruct](128)
	commontesting.Assert(a != nil, "allocator should not be nil after create", "allocator created", t)
	SlabAllocatorDestroy(a)
	commontesting.Assert(a.destroyed, "allocator should be marked as destroyed", "allocator destroyed", t)
}

func testSlabCreateWithZeroCapacityPanics(t *testing.T) {
	// Creation should immediately panic because capacity is 0.
	mustPanic(t, func() {
		SlabAllocatorCreate[testSlabStruct](0)
	})
	commontesting.Assert(true, "creating with zero capacity panicked as expected", "zero-cap create panics", t)
}

func testSlabMallocAndCallocObjects(t *testing.T) {
	const capacity = 8
	a := SlabAllocatorCreate[testSlabStruct](capacity)
	defer SlabAllocatorDestroy(a)

	// 1. Allocate some objects with Malloc
	p1 := SlabAllocatorMallocObject(a)
	p2 := SlabAllocatorMallocObject(a)

	// Write distinct data to them
	p1.val1 = 1111
	p2.val1 = 2222
	p1.val2 = 3.14
	p2.val2 = 6.28

	// 2. Allocate the rest with Calloc
	p3 := SlabAllocatorCallocObject(a)
	p4 := SlabAllocatorCallocObject(a)

	// 3. Verify data integrity of Malloc'd objects
	commontesting.Assert(p1.val1 == 1111, "p1 data was corrupted", "p1 data preserved", t)
	commontesting.Assert(p2.val1 == 2222, "p2 data was corrupted", "p2 data preserved", t)

	// 4. Verify memory is zeroed for Calloc'd objects
	commontesting.Assert(p3.val1 == 0 && p3.val2 == 0.0, "p3 was not zeroed", "p3 zeroed", t)
	commontesting.Assert(p4.val1 == 0 && p4.val2 == 0.0, "p4 was not zeroed", "p4 zeroed", t)

	// 5. Verify all pointers are unique
	ptrs := map[uintptr]bool{
		uintptr(unsafe.Pointer(p1)): true,
		uintptr(unsafe.Pointer(p2)): true,
		uintptr(unsafe.Pointer(p3)): true,
		uintptr(unsafe.Pointer(p4)): true,
	}
	commontesting.Assert(len(ptrs) == 4, "allocated pointers are not unique", "pointers unique", t)
}

func testSlabCallocZeroesMemory(t *testing.T) {
	a := SlabAllocatorCreate[testSlabStruct](4)
	defer SlabAllocatorDestroy(a)

	p := SlabAllocatorCalloc(a)
	byteSlice := unsafe.Slice((*byte)(p), unsafe.Sizeof(testSlabStruct{}))
	commontesting.Assert(allEqual(byteSlice, 0x00), "calloc did not zero memory", "calloc zeroed", t)

	// Ensure we can still write to it
	fillBytes(byteSlice, 0xFF)
	commontesting.Assert(allEqual(byteSlice, 0xFF), "write after calloc failed", "write after calloc ok", t)
}

func testSlabAllocationExhaustionPanics(t *testing.T) {
	const capacity = 4
	a := SlabAllocatorCreate[testSlabStruct](capacity)
	defer SlabAllocatorDestroy(a)

	// Allocate all available slots
	for i := 0; i < capacity; i++ {
		p := SlabAllocatorMalloc(a)
		commontesting.Assert(p != nil, "allocation failed before exhaustion", "allocation ok", t)
	}

	// Next allocation should panic
	mustPanic(t, func() {
		SlabAllocatorMalloc(a)
	})
	mustPanic(t, func() {
		SlabAllocatorCalloc(a)
	})
}

func testSlabResetAllowsFullReuse(t *testing.T) {
	const capacity = 8
	a := SlabAllocatorCreate[testSlabStruct](capacity)
	defer SlabAllocatorDestroy(a)

	ptrsBeforeReset := make([]unsafe.Pointer, capacity)
	for i := 0; i < capacity; i++ {
		ptrsBeforeReset[i] = SlabAllocatorMalloc(a)
	}

	// Verify it's full
	mustPanic(t, func() { SlabAllocatorMalloc(a) })

	// Reset the allocator
	SlabAllocatorReset(a)
	commontesting.Assert(true, "reset should not panic", "reset ok", t)

	// We should be able to allocate the full capacity again
	ptrsAfterReset := make([]unsafe.Pointer, capacity)
	for i := 0; i < capacity; i++ {
		ptrsAfterReset[i] = SlabAllocatorMalloc(a)
		commontesting.Assert(ptrsAfterReset[i] != nil, "allocation failed after reset", "re-allocation ok", t)
	}

	// The sequence of pointers should be identical, as the free list is reset the same way
	for i := 0; i < capacity; i++ {
		commontesting.Assert(ptrsBeforeReset[i] == ptrsAfterReset[i], "pointer mismatch after reset", "reset reuses memory predictably", t)
	}
}

func testSlabUnsafeFunctionsWork(t *testing.T) {
	a := SlabAllocatorCreate[testSlabStruct](4)
	defer SlabAllocatorDestroy(a)

	// Test unsafe malloc
	p1 := SlabAllocatorMallocObjectUnsafe(a)
	p1.val1 = rand.Uint64()
	commontesting.Assert(p1.val1 != 0, "unsafe malloc failed to return valid memory", "unsafe malloc ok", t)

	// Test unsafe calloc
	p2 := SlabAllocatorCallocObjectUnsafe(a)
	commontesting.Assert(p2.val1 == 0, "unsafe calloc did not zero memory", "unsafe calloc ok", t)
	p2.val1 = 1 // write to it
	commontesting.Assert(p2.val1 == 1, "write after unsafe calloc failed", "write after unsafe calloc ok", t)
}

func testSlabUseAfterDestroyPanics(t *testing.T) {
	a := SlabAllocatorCreate[testSlabStruct](16)
	SlabAllocatorDestroy(a)

	mustPanic(t, func() { SlabAllocatorMalloc(a) })
	mustPanic(t, func() { SlabAllocatorCalloc(a) })
	mustPanic(t, func() { SlabAllocatorReset(a) })
}
