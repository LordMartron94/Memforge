package memforge

import (
	"runtime"
	"testing"
)

// goHeapSink is used to ensure allocations made in benchmarks are not optimized
// away by the compiler and actually escape to the heap.
var goHeapSink any

// BenchmarkAllocatorComparisonSuite provides a comprehensive performance comparison
// between the Go heap allocator and the custom memforge allocators.
func BenchmarkAllocatorComparisonSuite(b *testing.B) {
	// --- Configuration ---
	const allocatorSize = 16 * 1024 * 1024 // 16 MiB
	const allocSize = 128
	const allocAlign = 8
	// Reset linear allocators when they are nearly full to keep benchmarks running.
	const linearResetThreshold = uint64(allocatorSize - 4096)
	// Reset manual allocator periodically in Malloc/Calloc tests to prevent OOM panics.
	const manualResetInterval = (allocatorSize / allocSize) / 2

	// A sample struct for object allocation benchmarks.
	type sampleObject struct {
		A, B, C, D uint64
	}

	// --- Allocator Setup ---
	fixedAlloc := FixedLinearAllocatorCreate(allocatorSize)
	defer FixedLinearAllocatorDestroy(fixedAlloc)

	dynamicAlloc := DynamicLinearAllocatorCreate(allocatorSize, func(currentCap, _ uint64) uint64 {
		return currentCap * 2
	})
	defer DynamicLinearAllocatorDestroy(dynamicAlloc)

	manualAlloc := FixedManualAllocatorCreate(uint(allocatorSize))
	defer FixedManualAllocatorDestroy(manualAlloc)

	// --- Malloc Benchmarks (Raw, Uninitialized Memory) ---

	b.Run("Malloc/GoHeap", func(b *testing.B) {
		b.ReportAllocs()
		b.Cleanup(runtime.GC)
		for b.Loop() {
			p := make([]byte, allocSize)
			goHeapSink = p
		}
	})

	b.Run("Malloc/FixedLinear", func(b *testing.B) {
		b.ReportAllocs()
		b.Cleanup(func() {
			FixedLinearAllocatorReset(fixedAlloc)
			runtime.GC()
		})
		for b.Loop() {
			if fixedAlloc.idx > linearResetThreshold {
				b.StopTimer()
				FixedLinearAllocatorReset(fixedAlloc)
				b.StartTimer()
			}
			p := FixedLinearAllocatorMalloc(fixedAlloc, allocSize, allocAlign)
			goHeapSink = p
		}
	})

	b.Run("Malloc/DynamicLinear", func(b *testing.B) {
		b.ReportAllocs()
		b.Cleanup(func() {
			DynamicLinearAllocatorReset(dynamicAlloc)
			runtime.GC()
		})
		for b.Loop() {
			if dynamicAlloc.idx > linearResetThreshold {
				b.StopTimer()
				DynamicLinearAllocatorReset(dynamicAlloc)
				b.StartTimer()
			}
			p := DynamicLinearAllocatorMalloc(dynamicAlloc, allocSize, allocAlign)
			goHeapSink = p
		}
	})

	b.Run("Malloc/FixedManual", func(b *testing.B) {
		b.ReportAllocs()
		b.Cleanup(func() {
			FixedManualAllocatorReset(manualAlloc)
			runtime.GC()
		})
		var i int
		for b.Loop() {
			if i%manualResetInterval == 0 && i > 0 {
				b.StopTimer()
				FixedManualAllocatorReset(manualAlloc)
				b.StartTimer()
			}
			p := FixedManualAllocatorMalloc(manualAlloc, allocSize, allocAlign)
			goHeapSink = p
			i++
		}
	})

	// --- Calloc Benchmarks (Zero-Initialized Memory) ---

	b.Run("Calloc/GoHeap", func(b *testing.B) {
		b.ReportAllocs()
		b.Cleanup(runtime.GC)
		for b.Loop() {
			p := make([]byte, allocSize)
			goHeapSink = p
		}
	})

	b.Run("Calloc/FixedLinear", func(b *testing.B) {
		b.ReportAllocs()
		b.Cleanup(func() {
			FixedLinearAllocatorReset(fixedAlloc)
			runtime.GC()
		})
		for b.Loop() {
			if fixedAlloc.idx > linearResetThreshold {
				b.StopTimer()
				FixedLinearAllocatorReset(fixedAlloc)
				b.StartTimer()
			}
			p := FixedLinearAllocatorCalloc(fixedAlloc, allocSize, allocAlign)
			goHeapSink = p
		}
	})

	b.Run("Calloc/DynamicLinear", func(b *testing.B) {
		b.ReportAllocs()
		b.Cleanup(func() {
			DynamicLinearAllocatorReset(dynamicAlloc)
			runtime.GC()
		})
		for b.Loop() {
			if dynamicAlloc.idx > linearResetThreshold {
				b.StopTimer()
				DynamicLinearAllocatorReset(dynamicAlloc)
				b.StartTimer()
			}
			p := DynamicLinearAllocatorCalloc(dynamicAlloc, allocSize, allocAlign)
			goHeapSink = p
		}
	})

	b.Run("Calloc/FixedManual", func(b *testing.B) {
		b.ReportAllocs()
		b.Cleanup(func() {
			FixedManualAllocatorReset(manualAlloc)
			runtime.GC()
		})
		var i int
		for b.Loop() {
			if i%manualResetInterval == 0 && i > 0 {
				b.StopTimer()
				FixedManualAllocatorReset(manualAlloc)
				b.StartTimer()
			}
			p := FixedManualAllocatorCalloc(manualAlloc, allocSize, allocAlign)
			goHeapSink = p
			i++
		}
	})

	// --- MallocObject Benchmarks (Typed Struct Allocation) ---

	b.Run("MallocObject/GoHeap", func(b *testing.B) {
		b.ReportAllocs()
		b.Cleanup(runtime.GC)
		for b.Loop() {
			p := new(sampleObject)
			goHeapSink = p
		}
	})

	b.Run("MallocObject/FixedLinear", func(b *testing.B) {
		b.ReportAllocs()
		b.Cleanup(func() {
			FixedLinearAllocatorReset(fixedAlloc)
			runtime.GC()
		})
		for b.Loop() {
			if fixedAlloc.idx > linearResetThreshold {
				b.StopTimer()
				FixedLinearAllocatorReset(fixedAlloc)
				b.StartTimer()
			}
			p := FixedLinearAllocatorMallocObject[sampleObject](fixedAlloc)
			goHeapSink = p
		}
	})

	b.Run("MallocObject/DynamicLinear", func(b *testing.B) {
		b.ReportAllocs()
		b.Cleanup(func() {
			DynamicLinearAllocatorReset(dynamicAlloc)
			runtime.GC()
		})
		for b.Loop() {
			if dynamicAlloc.idx > linearResetThreshold {
				b.StopTimer()
				DynamicLinearAllocatorReset(dynamicAlloc)
				b.StartTimer()
			}
			p := DynamicLinearAllocatorMallocObject[sampleObject](dynamicAlloc)
			goHeapSink = p
		}
	})

	b.Run("MallocObject/FixedManual", func(b *testing.B) {
		b.ReportAllocs()
		b.Cleanup(func() {
			FixedManualAllocatorReset(manualAlloc)
			runtime.GC()
		})
		var i int
		for b.Loop() {
			if i%manualResetInterval == 0 && i > 0 {
				b.StopTimer()
				FixedManualAllocatorReset(manualAlloc)
				b.StartTimer()
			}
			p := FixedManualAllocatorMallocObject[sampleObject](manualAlloc)
			goHeapSink = p
			i++
		}
	})

	// --- Manual Allocator Freeing Benchmarks ---

	b.Run("FixedManual/AllocFree", func(b *testing.B) {
		b.ReportAllocs()
		b.Cleanup(func() {
			FixedManualAllocatorReset(manualAlloc)
			runtime.GC()
		})
		for b.Loop() {
			p := FixedManualAllocatorMalloc(manualAlloc, allocSize, allocAlign)
			FixedManualAllocatorFree(manualAlloc, p)
		}
	})

	b.Run("FixedManual/AllocFreeMixed", func(b *testing.B) {
		b.ReportAllocs()
		b.Cleanup(func() {
			FixedManualAllocatorReset(manualAlloc)
			runtime.GC()
		})
		for b.Loop() {
			p1 := FixedManualAllocatorMalloc(manualAlloc, allocSize, allocAlign)
			p2 := FixedManualAllocatorMalloc(manualAlloc, allocSize, allocAlign)
			FixedManualAllocatorFree(manualAlloc, p1)
			goHeapSink = p2 // Keep p2 alive to avoid trivial free pattern
			FixedManualAllocatorFree(manualAlloc, p2)
		}
	})
}
