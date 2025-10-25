package memforge

import (
	"fmt"
	"memcore"
	"unsafe"
)

// GrowthStrategy determines the strategy for growth the dynamic allocator uses.
// currentCap = the current storage capacity.
// neededCap = the newly needed capacity for this allocation.
// return value = the new capacity
type GrowthStrategy func(currentCap, neededCap uint64) uint64

// DynamicLinearAllocator is a dynamically-sized bump allocator.
// Supremely fast, and quite flexible. (less fast than the linear allocator)
// NOT thread-safe -- it does not just non-block, it is unsafe to call concurrently.
type DynamicLinearAllocator struct {
	storage  memcore.MemoryMap
	baseAddr uintptr

	cap uint64
	idx uint64

	growthStrategy GrowthStrategy

	destroyed bool
}

// DynamicLinearAllocatorCreate creates a new Dynamic Allocator instance with X initial capacity.
func DynamicLinearAllocatorCreate(initialCapacityBytes uint, growthStrategy GrowthStrategy) *DynamicLinearAllocator {
	mmap, err := memcore.MemmapRequest((int)(initialCapacityBytes), memcore.PROT_READWRITE, memcore.MAP_ANON_PRIVATE)

	if err != nil {
		panic(fmt.Errorf("failure to create dynamic allocator: %w", err))
	}

	var growStrat GrowthStrategy = growthStrategy

	if growStrat == nil {
		panic("a grow strategy must be provided; if your intent was to have a fixed allocator, use the fixed linear allocator")
	}

	return &DynamicLinearAllocator{
		storage:        mmap,
		baseAddr:       uintptr(unsafe.Pointer(&mmap[0])),
		cap:            (uint64)(initialCapacityBytes),
		idx:            0,
		growthStrategy: growStrat,
		destroyed:      false,
	}
}

// DynamicLinearAllocatorDestroy destroys the allocator and cleans up.
// Do NOT use the allocator anymore.
func DynamicLinearAllocatorDestroy(allocator *DynamicLinearAllocator) {
	memcore.MemmapUnmap(allocator.storage)
	*allocator = DynamicLinearAllocator{destroyed: true}
}

// DynamicLinearAllocatorMalloc allocates X amount of bytes from the allocator.
// It does not zero the memory, therefore it may contain garbage.
// Storing Go pointers ANYWHERE inside this allocation results in undefined behaviour.
// Requesting 0 bytes returns an aligned pointer but does not change the allocator state.
//
//go:nosplit
func DynamicLinearAllocatorMalloc(instance *DynamicLinearAllocator, sizeBytes, alignment uint64) Pointer {
	dynamicLinearAllocatorNotDestroyedGuarantee(instance)
	alignmentValidate(alignment)

	alignedIdx := alignIdxUp(instance.idx, alignment)

	if !capacityGuarantee(alignedIdx, instance.cap, sizeBytes) {
		dynamicLinearAllocatorGrow(instance, alignedIdx+sizeBytes)
	}

	ptr := unsafe.Pointer(&instance.storage[alignedIdx])
	addr := uintptr(ptr)
	relativeAddr := addr - instance.baseAddr

	instance.idx = alignedIdx + sizeBytes

	return Pointer{
		offset: relativeAddr,
	}
}

// DynamicLinearAllocatorMallocUnsafe allocates X amount of bytes from the allocator.
// It does not zero the memory, therefore it may contain garbage.
// Storing Go pointers ANYWHERE inside this allocation results in undefined behaviour.
// Requesting 0 bytes returns an aligned pointer but does not change the allocator state.
// The unsafe version skips the alignment validation and the existence guarantee for the sake of performance.
//
//go:nosplit
func DynamicLinearAllocatorMallocUnsafe(instance *DynamicLinearAllocator, sizeBytes, alignment uint64) Pointer {
	alignedIdx := alignIdxUp(instance.idx, alignment)

	if !capacityGuarantee(alignedIdx, instance.cap, sizeBytes) {
		dynamicLinearAllocatorGrow(instance, alignedIdx+sizeBytes)
	}

	ptr := unsafe.Pointer(&instance.storage[alignedIdx])
	addr := uintptr(ptr)
	relativeAddr := addr - instance.baseAddr

	instance.idx = alignedIdx + sizeBytes

	return Pointer{
		offset: relativeAddr,
	}
}

// DynamicLinearAllocatorCalloc is similar to DynamicLinearAllocatorMalloc, except it also zeroes out the memory.
// Note: This is quite expensive (especially for larger amounts of memory),
// so avoid using it if at all possible.
//
//go:nosplit
func DynamicLinearAllocatorCalloc(instance *DynamicLinearAllocator, sizeBytes, alignment uint64) Pointer {
	ptr := DynamicLinearAllocatorMalloc(instance, sizeBytes, alignment)
	realPtr := DynamicLinearAllocatorRealPointerRetrieve(instance, ptr)
	memcore.MemoryClearNoHeapPointers(realPtr, uintptr(sizeBytes))
	return ptr
}

// DynamicLinearAllocatorCallocUnsafe is similar to DynamicLinearAllocatorMallocUnsafe, except it also zeroes out the memory.
// Note: This is quite expensive (especially for larger amounts of memory),
// so avoid using it if at all possible.
//
//go:nosplit
func DynamicLinearAllocatorCallocUnsafe(instance *DynamicLinearAllocator, sizeBytes, alignment uint64) Pointer {
	ptr := DynamicLinearAllocatorMallocUnsafe(instance, sizeBytes, alignment)
	realPtr := DynamicLinearAllocatorRealPointerRetrieve(instance, ptr)
	memcore.MemoryClearNoHeapPointers(realPtr, uintptr(sizeBytes))
	return ptr
}

// DynamicLinearAllocatorMallocObject is a convenience wrapper around DynamicLinearAllocatorMalloc.
// It converts the allocated memory to a wrapper for the desired object type.
// Example:
//
//	ref := allocator.DynamicLinearAllocatorMallocObject[MyStruct](alloc)
//	ref.Ptr(alloc).Field = 123
//
//go:nosplit
func DynamicLinearAllocatorMallocObject[T any](instance *DynamicLinearAllocator) AllocRef[T] {
	ptr := DynamicLinearAllocatorMalloc(instance, memcore.SizeOf[T](), memcore.AlignOf[T]())
	return AllocRef[T]{
		ptr: ptr,
	}
}

// DynamicLinearAllocatorCallocObject is a convenience wrapper around DynamicLinearAllocatorCalloc.
// It converts the allocated memory to a wrapper for the desired object type.
// Example:
//
//	ref := allocator.DynamicLinearAllocatorMallocObject[MyStruct](alloc)
//	ref.Ptr(alloc).Field = 123
//
//go:nosplit
func DynamicLinearAllocatorCallocObject[T any](instance *DynamicLinearAllocator) AllocRef[T] {
	ptr := DynamicLinearAllocatorCalloc(instance, memcore.SizeOf[T](), memcore.AlignOf[T]())
	return AllocRef[T]{
		ptr: ptr,
	}
}

// DynamicLinearAllocatorRealPointerRetrieve translates a Pointer object to a Go unsafe.Pointer.
// This returned value can be used to cast into a given type.
// However, the unsafe.Pointer can not be stored in custom-allocated memory as it is undefined behaviour.
//
//go:nosplit
func DynamicLinearAllocatorRealPointerRetrieve(instance *DynamicLinearAllocator, pointer Pointer) unsafe.Pointer {
	addr := instance.baseAddr + pointer.offset
	return unsafe.Pointer(addr)
}

// DynamicLinearAllocatorReset sets the index of the allocator to 0, allowing the memory to be re-used.
// Using pointers created before resetting results in undefined behaviour.
func DynamicLinearAllocatorReset(instance *DynamicLinearAllocator) {
	dynamicLinearAllocatorNotDestroyedGuarantee(instance)

	instance.idx = 0
}

// ----------------------------------- PRIVATE HELPERS

//go:inline
func dynamicLinearAllocatorNotDestroyedGuarantee(instance *DynamicLinearAllocator) {
	if instance.destroyed {
		panic("cannot use a destroyed allocator")
	}
}

//go:inline
func dynamicLinearAllocatorGrow(instance *DynamicLinearAllocator, neededCapacityBytes uint64) {
	newSizeBytes := instance.growthStrategy(instance.cap, neededCapacityBytes)

	if newSizeBytes < neededCapacityBytes {
		panic("growth strategy returned invalid new size")
	}

	newMap, err := memcore.MemmapRemap(instance.storage, int(newSizeBytes), memcore.MREMAP_MAYMOVE)

	if err != nil {
		panic(fmt.Errorf("could not grow memory: %w", err))
	}

	instance.storage = newMap
	instance.baseAddr = uintptr(unsafe.Pointer(&newMap[0]))
	instance.cap = newSizeBytes
}
