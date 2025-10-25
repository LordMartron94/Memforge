// Package memforge is a custom allocation implementation.
//
// It aims to be blazingly fast and give manual control (C-like) to clients.
package memforge

import (
	"fmt"
	"memcore"
	"unsafe"
)

// FixedLinearAllocator is a fixed-size bump allocator.
// Blazingly fast, but not as flexible.
// NOT thread-safe -- it does not just non-block, it is unsafe to call concurrently.
type FixedLinearAllocator struct {
	storage   memcore.MemoryMap
	idx       uint64
	cap       uint64
	destroyed bool
}

// FixedLinearAllocatorCreate creates an instance of the linear allocator.
func FixedLinearAllocatorCreate(sizeBytes int) *FixedLinearAllocator {
	mmap, err := memcore.MemmapRequest(sizeBytes, memcore.PROT_READWRITE, memcore.MAP_ANON_PRIVATE)

	if err != nil {
		panic(fmt.Errorf("failure to create linear allocator: %w", err))
	}

	return &FixedLinearAllocator{
		storage:   mmap,
		idx:       0,
		cap:       (uint64)(sizeBytes),
		destroyed: false,
	}
}

// FixedLinearAllocatorDestroy destroys the allocator and cleans up.
// Do NOT use the allocator anymore.
func FixedLinearAllocatorDestroy(allocator *FixedLinearAllocator) {
	memcore.MemmapUnmap(allocator.storage)
	*allocator = FixedLinearAllocator{destroyed: true}
}

// FixedLinearAllocatorMalloc allocates X amount of bytes from the allocator.
// It does not zero the memory, therefore it may contain garbage.
// Storing Go pointers ANYWHERE inside this allocation results in undefined behaviour.
// Requesting 0 bytes returns an aligned pointer but does not change the allocator state.
//
//go:nosplit
func FixedLinearAllocatorMalloc(instance *FixedLinearAllocator, sizeBytes, alignment uint64) unsafe.Pointer {
	fixedLinearAllocatorNotDestroyedGuarantee(instance)
	alignmentValidate(alignment)

	alignedIdx := alignIdxUp(instance.idx, alignment)

	if !capacityGuarantee(alignedIdx, instance.cap, sizeBytes) {
		panic("cannot allocate more memory than the allocator has available")
	}

	ptr := unsafe.Pointer(&instance.storage[alignedIdx])
	instance.idx = alignedIdx + sizeBytes

	return ptr
}

// FixedLinearAllocatorMallocUnsafe allocates X amount of bytes from the allocator.
// It does not zero the memory, therefore it may contain garbage.
// Storing Go pointers ANYWHERE inside this allocation results in undefined behaviour.
// Requesting 0 bytes returns an aligned pointer but does not change the allocator state.
// The unsafe version skips the alignment validation and the existence guarantee for the sake of performance.
//
//go:nosplit
func FixedLinearAllocatorMallocUnsafe(instance *FixedLinearAllocator, sizeBytes, alignment uint64) unsafe.Pointer {
	alignedIdx := alignIdxUp(instance.idx, alignment)

	if !capacityGuarantee(alignedIdx, instance.cap, sizeBytes) {
		panic("cannot allocate more memory than the allocator has available")
	}

	ptr := unsafe.Pointer(&instance.storage[alignedIdx])
	instance.idx = alignedIdx + sizeBytes

	return ptr
}

// FixedLinearAllocatorCalloc is similar to FixedLinearAllocatorMalloc, except it also zeroes out the memory.
// Note: This is quite expensive (especially for larger amounts of memory),
// so avoid using it if at all possible.
//
//go:nosplit
func FixedLinearAllocatorCalloc(instance *FixedLinearAllocator, sizeBytes, alignment uint64) unsafe.Pointer {
	ptr := FixedLinearAllocatorMalloc(instance, sizeBytes, alignment)
	memcore.MemoryClearNoHeapPointers(ptr, uintptr(sizeBytes))
	return ptr
}

// FixedLinearAllocatorCallocUnsafe is similar to FixedLinearAllocatorMallocUnsafe, except it also zeroes out the memory.
// Note: This is quite expensive (especially for larger amounts of memory),
// so avoid using it if at all possible.
//
//go:nosplit
func FixedLinearAllocatorCallocUnsafe(instance *FixedLinearAllocator, sizeBytes, alignment uint64) unsafe.Pointer {
	ptr := FixedLinearAllocatorMallocUnsafe(instance, sizeBytes, alignment)
	memcore.MemoryClearNoHeapPointers(ptr, uintptr(sizeBytes))
	return ptr
}

// FixedLinearAllocatorMallocObject is a convenience wrapper around FixedLinearAllocatorMalloc.
// It converts the allocated memory to the desired object type.
//
//go:nosplit
func FixedLinearAllocatorMallocObject[T any](instance *FixedLinearAllocator) *T {
	ptr := FixedLinearAllocatorMalloc(instance, memcore.SizeOf[T](), memcore.AlignOf[T]())
	return (*T)(ptr)
}

// FixedLinearAllocatorCallocObject is a convenience wrapper around FixedLinearAllocatorCalloc.
// It converts the allocated memory to the desired object type.
//
//go:nosplit
func FixedLinearAllocatorCallocObject[T any](instance *FixedLinearAllocator) *T {
	ptr := FixedLinearAllocatorCalloc(instance, memcore.SizeOf[T](), memcore.AlignOf[T]())
	return (*T)(ptr)
}

// FixedLinearAllocatorReset sets the index of the allocator to 0, allowing the memory to be re-used.
// Using pointers created before resetting results in undefined behaviour.
func FixedLinearAllocatorReset(instance *FixedLinearAllocator) {
	fixedLinearAllocatorNotDestroyedGuarantee(instance)

	instance.idx = 0
}

// ---------------------------------------- PRIVATE HELPERS

//go:inline
func fixedLinearAllocatorNotDestroyedGuarantee(instance *FixedLinearAllocator) {
	if instance.destroyed {
		panic("cannot use a destroyed allocator")
	}
}
