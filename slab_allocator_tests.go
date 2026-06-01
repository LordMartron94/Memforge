package memforge

import (
	commontesting "foundation/testing"
	"math/rand"
	"memcore"
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
	t.Run("create_with_custom_slot_size", testSlabCreateWithCustomSlotSize)
	t.Run("create_with_zero_slot_size_panics", testSlabCreateWithZeroSlotSizePanics)
}

// ---------------- Test Cases ----------------

func testSlabCreateAndDestroy(t *testing.T) {
	a := SlabAllocatorCreate[testSlabStruct](128)

	mustNotPanic(t, func() { memcore.MemcoreMarkDereferenceObject[FixedSlabAllocator[testSlabStruct]](a) })

	SlabAllocatorDestroy[testSlabStruct](a)
	mustPanic(t, func() { memcore.MemcoreMarkDereferenceObject[FixedSlabAllocator[testSlabStruct]](a) })

	mustPanic(t, func() { SlabAllocatorMalloc[testSlabStruct](a) })
	mustPanic(t, func() { SlabAllocatorCalloc[testSlabStruct](a) })
	mustPanic(t, func() { SlabAllocatorReset[testSlabStruct](a) })
}

func testSlabCreateWithZeroCapacityPanics(t *testing.T) {
	mustPanic(t, func() {
		SlabAllocatorCreate[testSlabStruct](0)
	})
}

func testSlabCreateWithZeroSlotSizePanics(t *testing.T) {
	mustPanic(t, func() {
		SlabAllocatorCreateWithSlotSize[testSlabStruct](8, 0, memcore.AlignOf[testSlabStruct]())
	})
}

func testSlabCreateWithCustomSlotSize(t *testing.T) {
	const customSlotBytes uint64 = 256
	align := memcore.AlignOf[testSlabStruct]()

	a := SlabAllocatorCreateWithSlotSize[testSlabStruct](4, customSlotBytes, align)
	defer SlabAllocatorDestroy[testSlabStruct](a)

	if got := SlabAllocatorSlotSizeGet[testSlabStruct](a); got < customSlotBytes {
		t.Fatalf("slot size %d smaller than requested %d", got, customSlotBytes)
	}

	m1 := SlabAllocatorMalloc[testSlabStruct](a)
	m2 := SlabAllocatorMalloc[testSlabStruct](a)

	off1 := memcore.MemcoreMarkSubtractBaseOffset(m1, uintptr(memcore.MemcoreMarkDereferenceObject[FixedSlabAllocator[testSlabStruct]](a).dataBaseOffset))
	off2 := memcore.MemcoreMarkSubtractBaseOffset(m2, uintptr(memcore.MemcoreMarkDereferenceObject[FixedSlabAllocator[testSlabStruct]](a).dataBaseOffset))
	slotSize := SlabAllocatorSlotSizeGet[testSlabStruct](a)
	if uint64(off2-off1) != slotSize {
		t.Fatalf("slot spacing %d, want %d", off2-off1, slotSize)
	}

	// Write distinct patterns in the full custom slot without overlapping.
	p1 := memcore.MemcoreMarkDereference(m1)
	p2 := memcore.MemcoreMarkDereference(m2)
	memcore.MemoryClearNoHeapPointers(p1, uintptr(slotSize))
	*(*byte)(p1) = 0xAA
	*(*byte)(unsafe.Pointer(uintptr(p2) + uintptr(slotSize) - 1)) = 0xBB
	if *(*byte)(p2) == 0xAA {
		t.Fatal("adjacent custom slots overlap")
	}
}

func testSlabMallocAndCallocObjects(t *testing.T) {
	const capacity = 8
	a := SlabAllocatorCreate[testSlabStruct](capacity)
	defer SlabAllocatorDestroy[testSlabStruct](a)

	// 1. Allocate some objects with Malloc
	p1 := SlabAllocatorMallocObject[testSlabStruct](a)
	p2 := SlabAllocatorMallocObject[testSlabStruct](a)

	// Write distinct data to them
	p1.val1 = 1111
	p2.val1 = 2222
	p1.val2 = 3.14
	p2.val2 = 6.28

	// 2. Allocate the rest with Calloc
	p3 := SlabAllocatorCallocObject[testSlabStruct](a)
	p4 := SlabAllocatorCallocObject[testSlabStruct](a)

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
	defer SlabAllocatorDestroy[testSlabStruct](a)

	ptr := SlabAllocatorCalloc[testSlabStruct](a)
	p := memcore.MemcoreMarkDereference(ptr)
	byteSlice := unsafe.Slice((*byte)(p), unsafe.Sizeof(testSlabStruct{}))
	commontesting.Assert(allEqual(byteSlice, 0x00), "calloc did not zero memory", "calloc zeroed", t)

	// Ensure we can still write to it
	fillBytes(byteSlice, 0xFF)
	commontesting.Assert(allEqual(byteSlice, 0xFF), "write after calloc failed", "write after calloc ok", t)
}

func testSlabAllocationExhaustionPanics(t *testing.T) {
	const capacity = 4
	a := SlabAllocatorCreate[testSlabStruct](capacity)
	defer SlabAllocatorDestroy[testSlabStruct](a)

	// Allocate all available slots
	for i := 0; i < capacity; i++ {
		ptr := SlabAllocatorMalloc[testSlabStruct](a)
		commontesting.Assert(ptr != memcore.MarkRaw{}, "allocation failed before exhaustion", "allocation ok", t)
	}

	// Next allocation should panic
	mustPanic(t, func() {
		SlabAllocatorMalloc[testSlabStruct](a)
	})
	mustPanic(t, func() {
		SlabAllocatorCalloc[testSlabStruct](a)
	})
}

func testSlabResetAllowsFullReuse(t *testing.T) {
	const capacity = 8
	a := SlabAllocatorCreate[testSlabStruct](capacity)
	defer SlabAllocatorDestroy[testSlabStruct](a)

	ptrsBeforeReset := make([]memcore.MarkRaw, capacity)
	for i := 0; i < capacity; i++ {
		ptrsBeforeReset[i] = SlabAllocatorMalloc[testSlabStruct](a)
	}

	// Verify it's full
	mustPanic(t, func() { SlabAllocatorMalloc[testSlabStruct](a) })

	// Reset the allocator - this should not panic
	mustNotPanic(t, func() {
		SlabAllocatorReset[testSlabStruct](a)
	})

	// We should be able to allocate the full capacity again
	ptrsAfterReset := make([]memcore.MarkRaw, capacity)
	for i := 0; i < capacity; i++ {
		ptrsAfterReset[i] = SlabAllocatorMalloc[testSlabStruct](a)
		commontesting.Assert(ptrsAfterReset[i] != memcore.MarkRaw{}, "allocation failed after reset", "re-allocation ok", t)
	}

	// The sequence of pointers should be identical, as the free list is reset the same way
	for i := 0; i < capacity; i++ {
		addr1 := memcore.MemcoreMarkDereference(ptrsBeforeReset[i])
		addr2 := memcore.MemcoreMarkDereference(ptrsAfterReset[i])
		commontesting.Assert(addr1 == addr2, "pointer mismatch after reset", "reset reuses memory predictably", t)
	}
}

func testSlabUnsafeFunctionsWork(t *testing.T) {
	a := SlabAllocatorCreate[testSlabStruct](4)
	defer SlabAllocatorDestroy[testSlabStruct](a)

	// Test unsafe malloc - verify we get non-nil memory
	p1 := SlabAllocatorMallocObjectUnsafe[testSlabStruct](a)
	randomValue := rand.Uint64()
	if randomValue == 0 {
		randomValue = 1 // ensure non-zero for test
	}
	p1.val1 = randomValue
	commontesting.Assert(p1.val1 == randomValue, "unsafe malloc failed to return valid memory", "unsafe malloc ok", t)

	// Test unsafe calloc - verify memory is zeroed
	p2 := SlabAllocatorCallocObjectUnsafe[testSlabStruct](a)
	commontesting.Assert(p2.val1 == 0 && p2.val2 == 0.0, "unsafe calloc did not zero memory", "unsafe calloc ok", t)

	// Verify we can write to calloc'd memory
	p2.val1 = 12345
	commontesting.Assert(p2.val1 == 12345, "write after unsafe calloc failed", "write after unsafe calloc ok", t)
}
