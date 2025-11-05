// Package memforge implements high-performance manual memory allocators for Go.
//
// # Overview
//
// memforge provides a collection of allocators that give manual control over memory,
// bypassing the Go garbage collector for hot or performance-critical systems.
// Each allocator is designed for a distinct usage pattern — from temporary arenas
// to long-lived, manually freed regions — and exposes C-like semantics.
//
// The design follows a data-oriented, functional style: each allocator is a
// data structure manipulated through free functions (e.g. FixedLinearAllocatorMalloc).
// No allocator has methods or implements interfaces; behaviour is always explicit.
//
// All allocators allocate memory through memcore.MemmapRequest (mmap), returning
// raw memory that must never contain Go pointers.
//
// ----------------------------------------------------------------------------
// Allocator Summary
// ----------------------------------------------------------------------------
//
// FixedLinearAllocator
// --------------------
// A fast, fixed-capacity bump (arena) allocator.
//
// • Allocations: sequential only (bump pointer)
// • Frees: not supported individually
// • Reset: O(1) via FixedLinearAllocatorReset
// • Thread safety: none (use per-goroutine)
// • Typical use: temporary arenas, frame buffers, transient workspaces.
//
// Performance:
//
//	Extremely fast — almost no overhead beyond pointer arithmetic.
//	Allocations are linear in memory, zero fragmentation.
//
// Safety Guidelines:
//   - Do not store Go pointers inside manually allocated memory.
//   - Calling Reset invalidates all previous pointers.
//
// Example use cases:
//
//	scratch buffers, short-lived data, staging arenas.
//
// ----------------------------------------------------------------------------
//
// DynamicLinearAllocator
// ----------------------
// A growable bump allocator that automatically expands its underlying region.
//
// • Allocations: sequential (like FixedLinear), automatically grows on demand
// • Frees: not supported individually
// • Reset: O(1) via DynamicLinearAllocatorReset
// • Thread safety: none
// • Typical use: dynamic staging areas where capacity is unknown in advance.
//
// Performance:
//
//	Slightly slower than FixedLinear due to occasional growth (mremap), but
//	still extremely fast for sequential allocation patterns.
//
// Safety Guidelines:
//   - Provide a valid GrowthStrategy; omitting it will panic.
//   - Growth is performed with mremap and may move the memory region;
//     previously derived unsafe.Pointer values must be considered invalid
//     after a growth event.
//   - Do not store Go pointers in the allocated memory.
//
// Example use cases:
//
//	adaptive buffers, build pipelines, data accumulation with unknown total size.
//
// ----------------------------------------------------------------------------
//
// FixedManualAllocator
// --------------------
// A general-purpose allocator supporting malloc/free semantics.
//
// • Allocations: arbitrary sizes and alignments
// • Frees: supported; merges adjacent free regions
// • Reset: O(1), returns allocator to pristine state
// • Thread safety: none
// • Typical use: subsystems requiring persistent or sparse allocations.
//
// Performance:
//
//	Fast, but slower than linear allocators due to metadata management.
//	Optimized for stable pointer lifetimes and predictable fragmentation behaviour.
//
// Safety Guidelines:
//   - Every allocation must eventually be freed with FixedManualAllocatorFree,
//     unless the entire allocator is reset or destroyed.
//   - Double frees or freeing unknown pointers cause undefined behaviour (panic).
//   - Do not mix memory from multiple allocators.
//   - Never store Go pointers in allocator-managed memory.
//   - Using a pointer after free or reset is undefined behaviour.
//
// Example use cases:
//
//	component registries, object pools, persistent custom containers.
//
// ----------------------------------------------------------------------------
//
// FixedSlabAllocator[T]
// ----------------------
// A type-specialized pooled allocator for objects of uniform size.
//
// • Allocations: fixed-size (type T)
// • Frees: automatic via internal free stack (not exposed directly)
// • Reset: O(n) reinitialization of the free list
// • Thread safety: none
// • Typical use: high-frequency object allocation where reuse is frequent.
//
// Performance:
//
//	Very fast (near-constant time). Ideal when the object size and count are known.
//
// Safety Guidelines:
//   - Designed for homogeneous objects; do not mix types.
//   - Returned memory is uninitialized (Malloc) or zeroed (Calloc).
//   - Using a pointer after Reset is undefined behaviour.
//   - Do not store Go pointers inside the slabs.
//
// Example use cases:
//
//	entity/component pools, small object caches, high-throughput freelists.
//
// ----------------------------------------------------------------------------
//
// General Safety Guidelines
// ----------------------------------------------------------------------------
//
//   - Pointers
//     Memory returned by memforge allocators is outside the Go heap.
//     Storing Go pointers inside these regions is undefined behaviour and can
//     corrupt the garbage collector. Only store plain data, structs without Go
//     pointer fields, or manually managed handles.
//
//   - Thread Safety
//     All allocators are strictly single-threaded and must not be used concurrently.
//     If multiple goroutines need manual memory, give each its own allocator.
//
//   - Lifetime and Ownership
//     Pointers become invalid after their allocator is reset or destroyed.
//     Never retain or dereference such pointers.
//
//   - Alignment
//     Always respect alignment requirements. All memforge functions accept
//     an explicit alignment parameter; passing non-power-of-two values will panic.
//
//   - Destruction
//     Every allocator provides a Destroy function. Calling Destroy unmaps the
//     underlying memory and renders the allocator unusable. Accessing it
//     afterward results in panic.
//
// ----------------------------------------------------------------------------
//
// Choosing an Allocator
// ----------------------------------------------------------------------------
//
// | Allocator             | Frees | Growth | Speed | Use Case |
// |------------------------|-------|---------|--------|-----------|
// | FixedLinearAllocator   | ✗     | ✗       | ★★★★★ | Temporary arenas |
// | DynamicLinearAllocator | ✗     | ✓       | ★★★★☆ | Dynamic staging |
// | FixedManualAllocator   | ✓     | ✗       | ★★★☆☆ | Persistent regions |
// | FixedSlabAllocator[T]  | ✓ (pool) | ✗   | ★★★★★ | Object pools |
//
// ----------------------------------------------------------------------------
//
// # Summary
//
// memforge provides the building blocks for deterministic, allocation-free
// systems programming in Go. The allocators are low-level primitives intended
// for experts who require precise control over performance and memory layout.
// Improper use can crash the program or corrupt memory; treat these tools
// as unsafe systems-level components.
package memforge
