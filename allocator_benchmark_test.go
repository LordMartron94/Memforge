package memforge

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"testing"
	"unsafe"
)

func benchmarkWithMetrics(b *testing.B, fn func(b *testing.B)) {
	var before, after runtime.MemStats
	b.ReportAllocs()
	runtime.GC()
	runtime.ReadMemStats(&before)
	b.ResetTimer()
	fn(b)
	b.StopTimer()
	runtime.ReadMemStats(&after)
	gcCount := float64(after.NumGC - before.NumGC)
	heapDelta := float64(int64(after.HeapAlloc) - int64(before.HeapAlloc))
	totalPauses := float64(after.PauseTotalNs - before.PauseTotalNs)
	b.ReportMetric(gcCount, "gc.count")
	b.ReportMetric(gcCount/float64(b.N), "gc.per.op")
	b.ReportMetric(heapDelta, "heap.delta.bytes")
	b.ReportMetric(totalPauses/float64(b.N), "ns/op.gc.pause")
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "ops/sec")
	b.ReportMetric(float64(after.Sys), "sys.bytes")
}

var goHeapSink any

// Growth strategy for dynamic allocator
func growthStrategy2x(currentCap, neededCap uint64) uint64 {
	newCap := currentCap * 2
	if newCap < neededCap {
		newCap = neededCap
	}
	return newCap
}

// BenchmarkAllocatorComparison - Comprehensive allocator comparison
func BenchmarkAllocatorComparison(b *testing.B) {
	sizes := []int{32, 256, 1024}

	for _, size := range sizes {
		groupName := fmt.Sprintf("Throughput/Size=%d", size)

		// Go Heap baseline
		b.Run(groupName+"/GoHeap", func(b *testing.B) {
			benchmarkWithMetrics(b, func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					data := make([]byte, size)
					goHeapSink = data
				}
			})
		})

		// Fixed Linear Allocator (bump allocator with reset)
		b.Run(groupName+"/FixedLinear", func(b *testing.B) {
			gcPercent := debug.SetGCPercent(-1)
			defer debug.SetGCPercent(gcPercent)

			arenaSize := 8 * 1024 * 1024 // 8 MiB
			allocator := FixedLinearAllocatorCreate(arenaSize)
			defer FixedLinearAllocatorDestroy(allocator)

			allocsPerReset := arenaSize / size
			counter := 0

			benchmarkWithMetrics(b, func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					if counter >= allocsPerReset {
						FixedLinearAllocatorReset(allocator)
						counter = 0
					}
					ptr := FixedLinearAllocatorMalloc(allocator, uint64(size), 16)
					goHeapSink = ptr
					counter++
				}
			})
		})

		// Dynamic Linear Allocator (can grow)
		b.Run(groupName+"/DynamicLinear", func(b *testing.B) {
			gcPercent := debug.SetGCPercent(-1)
			defer debug.SetGCPercent(gcPercent)

			initialSize := uint(1 * 1024 * 1024) // 1 MiB initial
			allocator := DynamicLinearAllocatorCreate(initialSize, growthStrategy2x)
			defer DynamicLinearAllocatorDestroy(allocator)

			arenaSize := 8 * 1024 * 1024
			allocsPerReset := arenaSize / size
			counter := 0

			benchmarkWithMetrics(b, func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					if counter >= allocsPerReset {
						DynamicLinearAllocatorReset(allocator)
						counter = 0
					}
					ptr := DynamicLinearAllocatorMalloc(allocator, uint64(size), 16)
					goHeapSink = ptr
					counter++
				}
			})
		})

		// Fixed Manual Allocator (supports free)
		b.Run(groupName+"/FixedManual", func(b *testing.B) {
			gcPercent := debug.SetGCPercent(-1)
			defer debug.SetGCPercent(gcPercent)

			arenaSize := uint(8 * 1024 * 1024) // 8 MiB
			allocator := FixedManualAllocatorCreate(arenaSize)
			defer FixedManualAllocatorDestroy(allocator)

			benchmarkWithMetrics(b, func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					ptr := FixedManualAllocatorMalloc(allocator, uint64(size), 16)
					goHeapSink = ptr
					FixedManualAllocatorFree(allocator, ptr)
				}
			})
		})

		// Slab Allocator (only for fixed sizes, most efficient for pooling)
		if size <= 1024 {
			b.Run(groupName+"/Slab", func(b *testing.B) {
				gcPercent := debug.SetGCPercent(-1)
				defer debug.SetGCPercent(gcPercent)

				// Create type of appropriate size
				type Block struct {
					data [1024]byte
				}

				capacity := uint64(10000)
				allocator := SlabAllocatorCreate[Block](capacity)
				defer SlabAllocatorDestroy(allocator)

				ptrs := make([]unsafe.Pointer, 0, capacity)

				benchmarkWithMetrics(b, func(b *testing.B) {
					for i := 0; i < b.N; i++ {
						if uint64(len(ptrs)) >= capacity {
							SlabAllocatorReset(allocator)
							ptrs = ptrs[:0]
						}
						ptr := SlabAllocatorMalloc(allocator)
						ptrs = append(ptrs, ptr)
						goHeapSink = ptr
					}
				})
			})
		}
	}
}

// BenchmarkAllocatorRealisticWorkloads - Realistic usage patterns
func BenchmarkAllocatorRealisticWorkloads(b *testing.B) {
	const allocsPerRequest = 50
	const avgAllocSize = 128

	// Simulate HTTP request handler pattern
	b.Run("RequestCycle/GoHeap", func(b *testing.B) {
		benchmarkWithMetrics(b, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				objects := make([][]byte, 0, allocsPerRequest)
				for j := 0; j < allocsPerRequest; j++ {
					obj := make([]byte, avgAllocSize)
					objects = append(objects, obj)
				}
				goHeapSink = objects
			}
		})
	})

	b.Run("RequestCycle/FixedLinear", func(b *testing.B) {
		gcPercent := debug.SetGCPercent(-1)
		defer debug.SetGCPercent(gcPercent)

		arenaSize := 1 * 1024 * 1024 // 1 MiB per request
		allocator := FixedLinearAllocatorCreate(arenaSize)
		defer FixedLinearAllocatorDestroy(allocator)

		benchmarkWithMetrics(b, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				objects := make([]unsafe.Pointer, 0, allocsPerRequest)
				for j := 0; j < allocsPerRequest; j++ {
					ptr := FixedLinearAllocatorMalloc(allocator, avgAllocSize, 16)
					objects = append(objects, ptr)
				}
				FixedLinearAllocatorReset(allocator)
				goHeapSink = objects
			}
		})
	})

	b.Run("RequestCycle/DynamicLinear", func(b *testing.B) {
		gcPercent := debug.SetGCPercent(-1)
		defer debug.SetGCPercent(gcPercent)

		allocator := DynamicLinearAllocatorCreate(128*1024, growthStrategy2x)
		defer DynamicLinearAllocatorDestroy(allocator)

		benchmarkWithMetrics(b, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				objects := make([]Pointer, 0, allocsPerRequest)
				for j := 0; j < allocsPerRequest; j++ {
					ptr := DynamicLinearAllocatorMalloc(allocator, avgAllocSize, 16)
					objects = append(objects, ptr)
				}
				DynamicLinearAllocatorReset(allocator)
				goHeapSink = objects
			}
		})
	})

	b.Run("RequestCycle/FixedManual", func(b *testing.B) {
		gcPercent := debug.SetGCPercent(-1)
		defer debug.SetGCPercent(gcPercent)

		allocator := FixedManualAllocatorCreate(1 * 1024 * 1024)
		defer FixedManualAllocatorDestroy(allocator)

		benchmarkWithMetrics(b, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				objects := make([]unsafe.Pointer, 0, allocsPerRequest)
				for j := 0; j < allocsPerRequest; j++ {
					ptr := FixedManualAllocatorMalloc(allocator, avgAllocSize, 16)
					objects = append(objects, ptr)
				}
				// Free all at end of request
				for _, ptr := range objects {
					FixedManualAllocatorFree(allocator, ptr)
				}
				goHeapSink = objects
			}
		})
	})
}

// BenchmarkAllocatorMixedSizeWorkload - Varied allocation sizes (realistic)
func BenchmarkAllocatorMixedSizeWorkload(b *testing.B) {
	sizes := []int{16, 32, 64, 128, 256, 512, 1024}

	b.Run("Mixed/GoHeap", func(b *testing.B) {
		benchmarkWithMetrics(b, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				size := sizes[i%len(sizes)]
				data := make([]byte, size)
				goHeapSink = data
			}
		})
	})

	b.Run("Mixed/FixedLinear", func(b *testing.B) {
		gcPercent := debug.SetGCPercent(-1)
		defer debug.SetGCPercent(gcPercent)

		const arenaSize = 8 * 1024 * 1024
		const align = 16

		allocator := FixedLinearAllocatorCreate(arenaSize)
		defer FixedLinearAllocatorDestroy(allocator)

		var bytesUsed uint64

		benchmarkWithMetrics(b, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				size := uint64(sizes[i%len(sizes)])
				if bytesUsed+size >= uint64(arenaSize-512) {
					FixedLinearAllocatorReset(allocator)
					bytesUsed = 0
				}

				ptr := FixedLinearAllocatorMalloc(allocator, size, align)
				goHeapSink = ptr

				bytesUsed += size
			}
		})
	})

	b.Run("Mixed/FixedManual", func(b *testing.B) {
		gcPercent := debug.SetGCPercent(-1)
		defer debug.SetGCPercent(gcPercent)

		allocator := FixedManualAllocatorCreate(8 * 1024 * 1024)
		defer FixedManualAllocatorDestroy(allocator)

		benchmarkWithMetrics(b, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				size := sizes[i%len(sizes)]
				ptr := FixedManualAllocatorMalloc(allocator, uint64(size), 16)
				goHeapSink = ptr
				FixedManualAllocatorFree(allocator, ptr)
			}
		})
	})
}

// BenchmarkAllocatorFragmentation - Tests allocator behavior under fragmentation
func BenchmarkAllocatorFragmentation(b *testing.B) {
	// Only manual allocator is affected by fragmentation
	b.Run("AllocFreePattern/FixedManual", func(b *testing.B) {
		gcPercent := debug.SetGCPercent(-1)
		defer debug.SetGCPercent(gcPercent)

		allocator := FixedManualAllocatorCreate(8 * 1024 * 1024)
		defer FixedManualAllocatorDestroy(allocator)

		// Allocate some objects, free every other one to create fragmentation
		const preAllocCount = 1000
		ptrs := make([]unsafe.Pointer, preAllocCount)
		for i := 0; i < preAllocCount; i++ {
			ptrs[i] = FixedManualAllocatorMalloc(allocator, 128, 16)
		}
		for i := 1; i < preAllocCount; i += 2 {
			FixedManualAllocatorFree(allocator, ptrs[i])
		}

		benchmarkWithMetrics(b, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				ptr := FixedManualAllocatorMalloc(allocator, 128, 16)
				goHeapSink = ptr
				FixedManualAllocatorFree(allocator, ptr)
			}
		})
	})

	b.Run("AllocFreePattern/GoHeap", func(b *testing.B) {
		// Create similar pattern for comparison
		const preAllocCount = 1000
		ptrs := make([][]byte, preAllocCount)
		for i := 0; i < preAllocCount; i++ {
			ptrs[i] = make([]byte, 128)
		}
		// Clear every other one
		for i := 1; i < preAllocCount; i += 2 {
			ptrs[i] = nil
		}
		runtime.KeepAlive(ptrs)

		benchmarkWithMetrics(b, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				data := make([]byte, 128)
				goHeapSink = data
			}
		})
	})
}

// BenchmarkFixedManualAllocatorSuite — dedicated suite for FixedManualAllocator performance and behavior
func BenchmarkFixedManualAllocatorSuite(b *testing.B) {
	sizes := []int{16, 64, 256, 1024}

	// Basic throughput comparison (malloc + free)
	for _, size := range sizes {
		b.Run(fmt.Sprintf("Throughput/Size=%d", size), func(b *testing.B) {
			gcPercent := debug.SetGCPercent(-1)
			defer debug.SetGCPercent(gcPercent)

			allocator := FixedManualAllocatorCreate(8 * 1024 * 1024)
			defer FixedManualAllocatorDestroy(allocator)

			benchmarkWithMetrics(b, func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					ptr := FixedManualAllocatorMalloc(allocator, uint64(size), 16)
					goHeapSink = ptr
					FixedManualAllocatorFree(allocator, ptr)
				}
			})
		})
	}

	// Sustained mixed-size allocations
	b.Run("MixedSizes", func(b *testing.B) {
		sizes := []int{16, 32, 64, 128, 256, 512, 1024}
		gcPercent := debug.SetGCPercent(-1)
		defer debug.SetGCPercent(gcPercent)

		allocator := FixedManualAllocatorCreate(8 * 1024 * 1024)
		defer FixedManualAllocatorDestroy(allocator)

		benchmarkWithMetrics(b, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				size := sizes[i%len(sizes)]
				ptr := FixedManualAllocatorMalloc(allocator, uint64(size), 16)
				goHeapSink = ptr
				FixedManualAllocatorFree(allocator, ptr)
			}
		})
	})

	// Realistic workload — allocate N objects and free all
	b.Run("RequestCyclePattern", func(b *testing.B) {
		const allocsPerCycle = 100
		const avgSize = 128

		gcPercent := debug.SetGCPercent(-1)
		defer debug.SetGCPercent(gcPercent)

		allocator := FixedManualAllocatorCreate(2 * 1024 * 1024)
		defer FixedManualAllocatorDestroy(allocator)

		benchmarkWithMetrics(b, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				ptrs := make([]unsafe.Pointer, 0, allocsPerCycle)
				for j := 0; j < allocsPerCycle; j++ {
					ptr := FixedManualAllocatorMalloc(allocator, avgSize, 16)
					ptrs = append(ptrs, ptr)
				}
				for _, p := range ptrs {
					FixedManualAllocatorFree(allocator, p)
				}
				goHeapSink = ptrs
			}
		})
	})

	// Fragmentation simulation (interleaved alloc/free)
	b.Run("FragmentationPattern", func(b *testing.B) {
		const preAllocCount = 1000
		const blockSize = 128

		gcPercent := debug.SetGCPercent(-1)
		defer debug.SetGCPercent(gcPercent)

		allocator := FixedManualAllocatorCreate(8 * 1024 * 1024)
		defer FixedManualAllocatorDestroy(allocator)

		ptrs := make([]unsafe.Pointer, preAllocCount)
		for i := range ptrs {
			ptrs[i] = FixedManualAllocatorMalloc(allocator, blockSize, 16)
		}
		for i := 0; i < preAllocCount; i += 2 {
			FixedManualAllocatorFree(allocator, ptrs[i])
		}

		benchmarkWithMetrics(b, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				ptr := FixedManualAllocatorMalloc(allocator, blockSize, 16)
				goHeapSink = ptr
				FixedManualAllocatorFree(allocator, ptr)
			}
		})
	})

	// Stress test: continuously allocate until exhaustion then reset
	b.Run("Stress/ExhaustionAndReset", func(b *testing.B) {
		gcPercent := debug.SetGCPercent(-1)
		defer debug.SetGCPercent(gcPercent)

		const arenaSize = 16 * 1024 * 1024
		const allocSize = 128

		allocator := FixedManualAllocatorCreate(arenaSize)
		defer FixedManualAllocatorDestroy(allocator)

		var allocated []unsafe.Pointer

		benchmarkWithMetrics(b, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				ptr := FixedManualAllocatorMalloc(allocator, allocSize, 16)
				if ptr == nil {
					// Reset allocator when full
					for _, p := range allocated {
						FixedManualAllocatorFree(allocator, p)
					}
					allocated = allocated[:0]
					continue
				}
				allocated = append(allocated, ptr)
				if len(allocated) > 1000 {
					for _, p := range allocated {
						FixedManualAllocatorFree(allocator, p)
					}
					allocated = allocated[:0]
				}
				goHeapSink = ptr
			}
		})
	})
}
