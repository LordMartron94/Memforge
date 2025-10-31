package memforge

import (
	"fmt"
	"memcore"
	"memcore/primitives"
	"unsafe"
)

// FixedSlabAllocator is a pooled allocator for use with a single object.
// Super fast, and flexible.
// Prefer to use this if possible when you must use something other than
// the fixed linear allocator (as it is still the fastest)
type FixedSlabAllocator[T any] struct {
	storage   memcore.MemoryMap
	freeStack *primitives.Stack[uint64]

	metaAllocator *FixedLinearAllocator
	base          uintptr
	slotSize      uint64
	slotCapacity  uint64
	destroyed     bool
}

// SlabAllocatorCreate initializes a new slab allocator with a given capacity.
//
// ⚠️ Important: The allocator struct itself contains Go pointers and must reside on
// the Go heap, visible to the garbage collector. Do NOT allocate this struct in
// manually-managed memory.
//
// You may, however, point its internal data buffer to a manually managed memory
// region (e.g., from another allocator or a direct mmap call).
//
//   - ✅ Safe:   Allocator struct on Go heap, data buffer in manual memory.
//   - ❌ Unsafe: Allocator struct and data buffer both in manual memory.
func SlabAllocatorCreate[T any](capacity uint64) *FixedSlabAllocator[T] {
	if capacity == 0 {
		panic("cannot create slab allocator with capacity of 0")
	}

	dataBytes := primitives.ContainerRequiredBytes[T](capacity)
	mmap, err := memcore.MemmapRequest(int(dataBytes), memcore.PROT_READWRITE, memcore.MAP_ANON_PRIVATE)

	if err != nil {
		panic(fmt.Errorf("failure to create slab allocator: %w", err))
	}

	stackBytes := primitives.ContainerRequiredBytes[uint64](capacity)

	metaAllocator := FixedLinearAllocatorCreate(int(stackBytes))
	stackAddr := FixedLinearAllocatorMalloc(metaAllocator, stackBytes, memcore.AlignOf[uint64]())

	freeStack := primitives.StackCreateAt[uint64](stackAddr, capacity)

	for i := capacity; i > 0; i-- {
		primitives.StackPushUnsafe(freeStack, i-1)
	}

	objectSize := memcore.SizeOf[T]()
	objectAlign := memcore.AlignOf[T]()
	slotSize := alignIdxUp(objectSize, objectAlign)

	allocator := &FixedSlabAllocator[T]{
		storage:       mmap,
		freeStack:     freeStack,
		metaAllocator: metaAllocator,
		slotSize:      slotSize,
		slotCapacity:  capacity,
		base:          uintptr(unsafe.Pointer(&mmap[0])),
		destroyed:     false,
	}

	memforgeAllocatorRegister(unsafe.Pointer(allocator), "Slab")

	return allocator
}

// SlabAllocatorDestroy destroys the allocator and cleans up.
// Do NOT use the allocator anymore.
func SlabAllocatorDestroy[T any](instance *FixedSlabAllocator[T]) {
	memcore.MemmapUnmap(instance.storage)
	FixedLinearAllocatorDestroy(instance.metaAllocator)
	memforgeAllocatorRemoveAll(unsafe.Pointer(instance))

	instance.storage = nil
	instance.metaAllocator = nil
	instance.destroyed = true
}

// SlabAllocatorReset allows memory within the allocator to be full re-used.
// It does not zero out memory.
// Using pointers from before the reset is undefined behaviour.
func SlabAllocatorReset[T any](instance *FixedSlabAllocator[T]) {
	slabAllocatorNotDestroyedGuarantee(instance)
	primitives.StackClear(instance.freeStack)
	memforgeAllocatorRemoveAll(unsafe.Pointer(instance))

	for i := instance.slotCapacity; i > 0; i-- {
		primitives.StackPushUnsafe(instance.freeStack, i-1)
	}
}

// SlabAllocatorMalloc allocates T from the allocator.
// It does not zero the memory, therefore it may contain garbage.
// Storing Go pointers ANYWHERE inside this allocation results in undefined behaviour.
//
//go:nosplit
func SlabAllocatorMalloc[T any](instance *FixedSlabAllocator[T]) unsafe.Pointer {
	slabAllocatorNotDestroyedGuarantee(instance)
	idx, err := primitives.StackPop(instance.freeStack)

	if err != nil {
		panic(fmt.Errorf("could not allocate: %w", err))
	}

	addr := unsafe.Add(unsafe.Pointer(instance.base), idx*instance.slotSize)
	memforgeAllocationAdd(unsafe.Pointer(instance), addr, instance.slotSize)
	return addr
}

// SlabAllocatorCalloc allocates T from the allocator.
// It also zeroes the memory.
// Storing Go pointers ANYWHERE inside this allocation results in undefined behaviour.
//
//go:nosplit
func SlabAllocatorCalloc[T any](instance *FixedSlabAllocator[T]) unsafe.Pointer {
	slabAllocatorNotDestroyedGuarantee(instance)
	idx, err := primitives.StackPop(instance.freeStack)

	if err != nil {
		panic(fmt.Errorf("could not allocate: %w", err))
	}

	addr := unsafe.Add(unsafe.Pointer(instance.base), idx*instance.slotSize)
	memforgeAllocationAdd(unsafe.Pointer(instance), addr, instance.slotSize)
	memcore.MemoryClearNoHeapPointers(addr, uintptr(instance.slotSize))
	return addr
}

// SlabAllocatorMallocUnsafe allocates T from the allocator.
// It does not zero the memory, therefore it may contain garbage.
// Storing Go pointers ANYWHERE inside this allocation results in undefined behaviour.
//
// Note: does not validate anything.
//
//go:nosplit
func SlabAllocatorMallocUnsafe[T any](instance *FixedSlabAllocator[T]) unsafe.Pointer {
	idx := primitives.StackPopUnsafe(instance.freeStack)
	addr := unsafe.Add(unsafe.Pointer(instance.base), idx*instance.slotSize)
	return addr
}

// SlabAllocatorCallocUnsafe allocates T from the allocator.
// It also zeroes the memory.
// Storing Go pointers ANYWHERE inside this allocation results in undefined behaviour.
//
// Note: does not validate anything.
//
//go:nosplit
func SlabAllocatorCallocUnsafe[T any](instance *FixedSlabAllocator[T]) unsafe.Pointer {
	idx := primitives.StackPopUnsafe(instance.freeStack)
	addr := unsafe.Add(unsafe.Pointer(instance.base), idx*instance.slotSize)
	memcore.MemoryClearNoHeapPointers(addr, uintptr(instance.slotSize))
	return addr
}

// SlabAllocatorMallocObject is a convenience wrapper around SlabAllocatorMalloc
//
//go:nosplit
func SlabAllocatorMallocObject[T any](instance *FixedSlabAllocator[T]) *T {
	addr := SlabAllocatorMalloc(instance)
	return (*T)(addr)
}

// SlabAllocatorCallocObject is a convenience wrapper around SlabAllocatorCalloc
//
//go:nosplit
func SlabAllocatorCallocObject[T any](instance *FixedSlabAllocator[T]) *T {
	addr := SlabAllocatorCalloc(instance)
	return (*T)(addr)
}

// SlabAllocatorMallocObjectUnsafe is a convenience wrapper around SlabAllocatorMallocUnsafe
//
//go:nosplit
func SlabAllocatorMallocObjectUnsafe[T any](instance *FixedSlabAllocator[T]) *T {
	addr := SlabAllocatorMallocUnsafe(instance)
	return (*T)(addr)
}

// SlabAllocatorCallocObjectUnsafe is a convenience wrapper around SlabAllocatorCallocUnsafe
//
//go:nosplit
func SlabAllocatorCallocObjectUnsafe[T any](instance *FixedSlabAllocator[T]) *T {
	addr := SlabAllocatorCallocUnsafe(instance)
	return (*T)(addr)
}

// -------------------------------------------- PRIVATE HELPERS

//go:inline
func slabAllocatorNotDestroyedGuarantee[T any](instance *FixedSlabAllocator[T]) {
	if instance.destroyed {
		panic("cannot use a destroyed allocator")
	}
}
