package memforge

// Pointer stores the relative offset of the memory associated with a given allocation.
// This allows for stable relocation of memory.
// The Pointer struct has no Go pointers in it, so it is safe to store in custom-allocated memory.
type Pointer struct {
	offset uintptr
}

// AllocRef is a wrapper around a Pointer instance that allows for easy dereferencing.
// This reference does not contain a Go pointer and thus can be stored inside custom-allocated memory.
type AllocRef[T any] struct {
	ptr Pointer
}

// Ptr dereferences the allocation into the right object.
func (r AllocRef[T]) Ptr(alloc *DynamicLinearAllocator) *T {
	return (*T)(DynamicLinearAllocatorRealPointerRetrieve(alloc, r.ptr))
}

//go:inline
func capacityGuarantee(alignedIdx, cap, requestAmountBytes uint64) bool {
	return alignedIdx+requestAmountBytes <= cap
}

//go:inline
func alignmentValidate(alignment uint64) {
	if alignment <= 0 || (alignment&(alignment-1)) != 0 {
		panic("alignment must be a power of two and > 0")
	}
}

//go:inline
func alignIdxUp(idx uint64, alignment uint64) uint64 {
	mask := alignment - 1
	return (idx + mask) &^ mask
}
