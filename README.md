# memforge

High-performance manual memory allocators for Go, providing C-like control over memory allocation.

## Overview

`memforge` provides a collection of allocators that bypass Go's garbage collector, giving you precise control over memory allocation patterns for performance-critical code. Each allocator is designed for specific use cases and exposes a functional, data-oriented API.

## Design Philosophy

- **Functional Style**: Allocators are data structures manipulated through free functions (e.g., `FixedLinearAllocatorMalloc`), not methods
- **Explicit Behavior**: No interfaces or hidden behavior; allocation semantics are always clear
- **Zero GC Overhead**: All memory is allocated via `memcore.MemmapRequest` (mmap), never touching the Go heap
- **Type Safety**: Generic allocators for type-specific pools (e.g., `FixedSlabAllocator[T]`)

## Allocators

### FixedLinearAllocator

A fast, fixed-capacity bump (arena) allocator.

**Characteristics:**
- ⚡ **Extremely fast** - almost no overhead beyond pointer arithmetic
- 📈 **Sequential allocations only** - uses a bump pointer
- ❌ **No individual frees** - only reset supported
- 🔄 **O(1) reset** - returns allocator to pristine state
- 🚫 **Not thread-safe** - use per-goroutine

**Use Cases:**
- Temporary arenas
- Frame buffers
- Transient workspaces
- Scratch buffers

**Example:**
```go
allocator := memforge.FixedLinearAllocatorCreate(uint64(memcore.MegaByte))
defer memforge.FixedLinearAllocatorDestroy(allocator)

// Allocate some memory
mark := memforge.FixedLinearAllocatorMalloc(allocator, 1024, 8)
ptr := memcore.MemcoreMarkDereference(mark)

// Use memory...

// Reset when done (frees all allocations at once)
memforge.FixedLinearAllocatorReset(allocator)
```

### DynamicLinearAllocator

A growable bump allocator that automatically expands its underlying region.

**Characteristics:**
- 📈 **Sequential allocations** - like FixedLinear
- 📏 **Auto-growing** - expands on demand using `mremap`
- ❌ **No individual frees** - only reset supported
- 🔄 **O(1) reset**
- 🚫 **Not thread-safe**

**Use Cases:**
- Dynamic staging areas
- Build pipelines
- Data accumulation with unknown total size

**Important:** Growth may move the memory region, invalidating previous `unsafe.Pointer` values. Always use `MarkRaw` values instead.

**Example:**
```go
growthFn := func(currentCap, neededCap uint64) uint64 {
    return max(currentCap*2, neededCap)
}
allocator := memforge.DynamicLinearAllocatorCreateFunction(
    uint64(memcore.KiloByte),
    growthFn,
)
defer memforge.DynamicLinearAllocatorDestroy(allocator)
```

### FixedManualAllocator

A general-purpose allocator supporting malloc/free semantics.

**Characteristics:**
- ✅ **Individual frees** - supports malloc/free
- 🔗 **Merges adjacent free regions** - reduces fragmentation
- 📐 **Arbitrary sizes and alignments**
- 🔄 **O(1) reset** - returns to pristine state
- 🚫 **Not thread-safe**

**Use Cases:**
- Subsystems requiring persistent allocations
- Sparse allocation patterns
- Component registries

**Performance:** Fast, but slower than linear allocators due to metadata management.

**Example:**
```go
allocator := memforge.FixedManualAllocatorCreate(uint64(memcore.MegaByte))
defer memforge.FixedManualAllocatorDestroy(allocator)

mark1 := memforge.FixedManualAllocatorMalloc(allocator, 1024, 8)
mark2 := memforge.FixedManualAllocatorMalloc(allocator, 2048, 16)

// Free individual allocations
memforge.FixedManualAllocatorFree(allocator, mark1)
memforge.FixedManualAllocatorFree(allocator, mark2)
```

### FixedSlabAllocator[T]

A type-specialized pooled allocator for objects of uniform size.

**Characteristics:**
- 🎯 **Fixed-size allocations** - specialized for type `T`
- ♻️ **Automatic pooling** - maintains internal free stack
- ⚡ **Very fast** - near-constant time
- 🔄 **O(n) reset** - reinitializes free list
- 🚫 **Not thread-safe**

**Use Cases:**
- Entity/component pools
- High-frequency object allocation
- Small object caches

**Example:**
```go
type Entity struct {
    ID    uint64
    Value float32
}

allocator := memforge.FixedSlabAllocatorCreate[Entity](1000)
defer memforge.FixedSlabAllocatorDestroy(allocator)

// Allocate entities (uninitialized)
mark1 := memforge.FixedSlabAllocatorMalloc(allocator)
entity1 := memcore.MemcoreMarkDereferenceObject[Entity](mark1)
entity1.ID = 1
entity1.Value = 42.0

// Or use Calloc for zero-initialized memory
mark2 := memforge.FixedSlabAllocatorCalloc(allocator)
entity2 := memcore.MemcoreMarkDereferenceObject[Entity](mark2)
// entity2 is already zeroed
```

## Allocation Functions

Each allocator provides:

- **`Malloc(sizeBytes, alignment uint64)`** - Allocate uninitialized memory
- **`Calloc(sizeBytes, alignment uint64)`** - Allocate zero-initialized memory
- **`Reset()`** - Reset allocator (behavior varies by type)
- **`Destroy()`** - Unmap memory and destroy allocator

## Safety Guidelines

⚠️ **Critical Warnings:**

### No Go Pointers
Memory returned by `memforge` allocators is outside the Go heap. **Never store Go pointers** in this memory—it will corrupt the garbage collector and cause undefined behavior.

### Thread Safety
All allocators are **strictly single-threaded**. Do not use them concurrently. If multiple goroutines need manual memory, give each its own allocator.

### Lifetime and Ownership
Pointers become invalid after their allocator is reset or destroyed. Never retain or dereference such pointers.

### Alignment
Always respect alignment requirements. All functions accept an explicit alignment parameter; passing non-power-of-two values will panic.

### Double Frees
With `FixedManualAllocator`, double frees or freeing unknown pointers cause undefined behavior (panic).

## Choosing an Allocator

| Allocator | Frees | Growth | Speed | Use Case |
|-----------|-------|--------|-------|----------|
| `FixedLinearAllocator` | ❌ | ❌ | ★★★★★ | Temporary arenas |
| `DynamicLinearAllocator` | ❌ | ✅ | ★★★★☆ | Dynamic staging |
| `FixedManualAllocator` | ✅ | ❌ | ★★★☆☆ | Persistent regions |
| `FixedSlabAllocator[T]` | ✅ (pool) | ❌ | ★★★★★ | Object pools |

## Memory Debugger

`memforge` includes a memory debugger that can track allocations and detect leaks:

```go
defer memforge.MemforgeMemoryDebug()
// ... use allocators ...
// Debug info printed on defer
```

## Integration

`memforge` is designed to work with:
- **memcore** - Uses `MemmapRequest` for memory allocation
- **memstruct** - Provides data structures that can use memforge allocators
- **memarch** - Factory layer combining memforge with memstruct

## Example: Creating a Data Structure

```go
// Create an allocator
allocator := memforge.FixedLinearAllocatorCreate(uint64(memcore.MegaByte))
defer memforge.FixedLinearAllocatorDestroy(allocator)

// Wrap as an allocation function
allocFn := func(sizeBytes, alignment uint64) memcore.MarkRaw {
    return memforge.FixedLinearAllocatorMalloc(allocator, sizeBytes, alignment)
}

// Use with memarch to create data structures
arrayMark, arrayPtr := memarch.MemArchArrayCreate[int](allocFn, 100)
// arrayPtr is ready to use
```

## Performance Characteristics

- **FixedLinearAllocator**: ~2-3 CPU cycles per allocation (pointer increment)
- **DynamicLinearAllocator**: ~2-3 cycles, plus occasional `mremap` overhead on growth
- **FixedManualAllocator**: ~10-50 cycles (depends on fragmentation)
- **FixedSlabAllocator**: ~5-10 cycles (free list manipulation)

All allocators avoid GC pauses entirely, making them suitable for real-time systems and high-frequency trading applications.
