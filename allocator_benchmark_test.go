package memforge

import (
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"runtime"
	"runtime/debug"
	"testing"
	"unsafe"
)

func GetFunctionName(i interface{}) string {
	return runtime.FuncForPC(reflect.ValueOf(i).Pointer()).Name()
}

func benchmarkWithMetrics(b *testing.B, fn func(b *testing.B)) {
	name := b.Name()
	fmt.Fprintf(os.Stderr, "🔹 Running %s...\n", name)
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
	fmt.Fprintf(os.Stderr, "✅ Finished %s\n", name)
}

var goHeapSink any

// Growth strategy for dynamic allocator
const maxGrowthMem = 8 * 1024 * 1024

func growthStrategy2x(currentCap, neededCap uint64) uint64 {
	newCap := currentCap * 2
	if newCap < neededCap {
		newCap = neededCap
	}

	if newCap > maxGrowthMem {
		panic(fmt.Sprintf("benchmark setup went wrong, more mem requested than available. requested=%v/available=%v", newCap, maxGrowthMem))
	}

	return newCap
}

// BenchmarkAllocatorComparison - Comprehensive allocator comparison
func BenchmarkAllocatorSuite_Comparison(b *testing.B) {
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
						b.StopTimer()
						FixedLinearAllocatorReset(allocator)
						b.StartTimer()
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
						b.StopTimer()
						DynamicLinearAllocatorReset(allocator)
						b.StartTimer()
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

			arenaSize := 8 * 1024 * 1024 // 8 MiB
			allocsPerReset := arenaSize / size
			allocator := FixedManualAllocatorCreate(uint(arenaSize))
			defer FixedManualAllocatorDestroy(allocator)

			counter := 0

			benchmarkWithMetrics(b, func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					if counter >= allocsPerReset {
						b.StopTimer()
						FixedManualAllocatorReset(allocator)
						b.StartTimer()
						counter = 0
					}

					ptr := FixedManualAllocatorMalloc(allocator, uint64(size), 16)
					goHeapSink = ptr
					counter++
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
							b.StopTimer()
							SlabAllocatorReset(allocator)
							b.StartTimer()
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
func BenchmarkAllocatorSuite_RealisticWorkloads(b *testing.B) {
	const allocsPerRequest = 50
	const avgAllocSize = 128

	// Simulate HTTP request handler pattern
	b.Run("RequestCycle/GoHeap", func(b *testing.B) {
		objects := make([][]byte, allocsPerRequest)

		benchmarkWithMetrics(b, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				for j := 0; j < allocsPerRequest; j++ {
					obj := make([]byte, avgAllocSize)
					objects[j] = obj
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

		objects := make([]unsafe.Pointer, allocsPerRequest)

		benchmarkWithMetrics(b, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				for j := 0; j < allocsPerRequest; j++ {
					ptr := FixedLinearAllocatorMalloc(allocator, avgAllocSize, 16)
					objects[j] = ptr
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

		objects := make([]Pointer, allocsPerRequest)

		benchmarkWithMetrics(b, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				for j := 0; j < allocsPerRequest; j++ {
					ptr := DynamicLinearAllocatorMalloc(allocator, avgAllocSize, 16)
					objects[j] = ptr
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

		objects := make([]unsafe.Pointer, allocsPerRequest)

		benchmarkWithMetrics(b, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				for j := 0; j < allocsPerRequest; j++ {
					ptr := FixedManualAllocatorMalloc(allocator, avgAllocSize, 16)
					objects[j] = ptr
				}
				FixedManualAllocatorReset(allocator)
				goHeapSink = objects
			}
		})
	})
}

// BenchmarkAllocatorMixedSizeWorkload - Varied allocation sizes (realistic)
func BenchmarkAllocatorSuite_MixedSizeWorkload(b *testing.B) {
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
					b.StopTimer()
					FixedLinearAllocatorReset(allocator)
					b.StartTimer()
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

		arenaSize := 8 * 1024 * 1024
		allocsPerReset := arenaSize / 1024
		allocator := FixedManualAllocatorCreate(uint(arenaSize))
		defer FixedManualAllocatorDestroy(allocator)

		counter := 0
		benchmarkWithMetrics(b, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				if counter >= allocsPerReset {
					b.StopTimer()
					FixedManualAllocatorReset(allocator)
					b.StartTimer()
					counter = 0
				}

				size := sizes[i%len(sizes)]
				ptr := FixedManualAllocatorMalloc(allocator, uint64(size), 16)
				goHeapSink = ptr
				counter++
			}
		})
	})
}

// BenchmarkAllocatorFragmentation - Tests allocator behavior under fragmentation
func BenchmarkAllocatorSuite_Fragmentation(b *testing.B) {
	// Only manual allocator is affected by fragmentation
	b.Run("AllocSteady/FixedManual", func(b *testing.B) {
		gcPercent := debug.SetGCPercent(-1)
		defer debug.SetGCPercent(gcPercent)

		const arenaSize = 8 * 1024 * 1024
		const objSize = 128
		const poolSize = 4096 // working set
		const stride = 3      // how often to free

		allocator := FixedManualAllocatorCreate(arenaSize)
		defer FixedManualAllocatorDestroy(allocator)

		ptrs := make([]unsafe.Pointer, poolSize)
		for i := range ptrs {
			ptrs[i] = FixedManualAllocatorMalloc(allocator, objSize, 16)
		}

		benchmarkWithMetrics(b, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				idx := uint64(i % poolSize)
				FixedManualAllocatorFree(allocator, ptrs[idx])
				ptrs[idx] = FixedManualAllocatorMalloc(allocator, objSize, 16)

				if i%stride == 0 {
					goHeapSink = ptrs[idx]
				}
			}
		})
	})

	b.Run("BurstReset/FixedManual", func(b *testing.B) {
		const arenaSize = 8 * 1024 * 1024
		const objSize = 256
		const batchSize = 1024

		gcPercent := debug.SetGCPercent(-1)
		defer debug.SetGCPercent(gcPercent)

		allocator := FixedManualAllocatorCreate(arenaSize)
		defer FixedManualAllocatorDestroy(allocator)

		benchmarkWithMetrics(b, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				for j := 0; j < batchSize; j++ {
					ptr := FixedManualAllocatorMalloc(allocator, objSize, 16)
					goHeapSink = ptr
				}
				FixedManualAllocatorReset(allocator)
			}
		})
	})

	b.Run("FragmentationStress/FixedManual", func(b *testing.B) {
		const arenaSize = 16 * 1024 * 1024
		const objMin = 32
		const objMax = 512
		const ops = 2048

		gcPercent := debug.SetGCPercent(-1)
		defer debug.SetGCPercent(gcPercent)

		allocator := FixedManualAllocatorCreate(arenaSize)
		defer FixedManualAllocatorDestroy(allocator)

		rnd := rand.New(rand.NewSource(42))
		ptrs := make([]unsafe.Pointer, ops)

		benchmarkWithMetrics(b, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				idx := rnd.Intn(ops)
				if ptrs[idx] != nil {
					FixedManualAllocatorFree(allocator, ptrs[idx])
					ptrs[idx] = nil
				} else {
					size := uint64(objMin + rnd.Intn(objMax-objMin))
					ptrs[idx] = FixedManualAllocatorMalloc(allocator, size, 16)
				}
			}
		})
	})
}

// BenchmarkFixedManualAllocatorSuite — dedicated suite for FixedManualAllocator performance and behavior
func BenchmarkFixedManualAllocator_Suite(b *testing.B) {
	sizes := []int{16, 64, 256, 1024}

	// -----------------------------------------
	// 1. Basic throughput comparison (malloc + free)
	// -----------------------------------------
	for _, size := range sizes {
		b.Run(fmt.Sprintf("Throughput/Size=%d", size), func(b *testing.B) {
			oldGC := debug.SetGCPercent(-1)
			defer debug.SetGCPercent(oldGC)

			allocator := FixedManualAllocatorCreate(8 * 1024 * 1024)

			benchmarkWithMetrics(b, func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					ptr := FixedManualAllocatorMalloc(allocator, uint64(size), 16)
					FixedManualAllocatorFree(allocator, ptr)
				}
			})

			FixedManualAllocatorDestroy(allocator)
			allocator = nil
			runtime.GC()
		})
	}

	// -----------------------------------------
	// 2. Sustained mixed-size allocations
	// -----------------------------------------
	b.Run("MixedSizes", func(b *testing.B) {
		blockSizes := []int{16, 32, 64, 128, 256, 512, 1024}
		oldGC := debug.SetGCPercent(-1)
		defer debug.SetGCPercent(oldGC)

		allocator := FixedManualAllocatorCreate(8 * 1024 * 1024)

		benchmarkWithMetrics(b, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				size := blockSizes[i%len(blockSizes)]
				ptr := FixedManualAllocatorMalloc(allocator, uint64(size), 16)
				FixedManualAllocatorFree(allocator, ptr)
			}
		})

		FixedManualAllocatorDestroy(allocator)
		allocator = nil
		runtime.GC()
	})

	// -----------------------------------------
	// 3. Request cycle pattern — allocate N objects then free all
	// -----------------------------------------
	b.Run("RequestCyclePattern", func(b *testing.B) {
		const allocsPerCycle = 100
		const avgSize = 128

		oldGC := debug.SetGCPercent(-1)
		defer debug.SetGCPercent(oldGC)

		allocator := FixedManualAllocatorCreate(2 * 1024 * 1024)
		ptrs := make([]unsafe.Pointer, allocsPerCycle)

		benchmarkWithMetrics(b, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				for j := 0; j < allocsPerCycle; j++ {
					ptrs[j] = FixedManualAllocatorMalloc(allocator, avgSize, 16)
				}
				for _, p := range ptrs {
					FixedManualAllocatorFree(allocator, p)
				}
			}
		})

		// cleanup
		for i := range ptrs {
			ptrs[i] = nil
		}
		ptrs = nil
		FixedManualAllocatorDestroy(allocator)
		allocator = nil
		runtime.GC()
	})

	// -----------------------------------------
	// 4. Fragmentation simulation (interleaved alloc/free)
	// -----------------------------------------
	b.Run("FragmentationPattern", func(b *testing.B) {
		const preAllocCount = 1000
		const blockSize = 128

		oldGC := debug.SetGCPercent(-1)
		defer debug.SetGCPercent(oldGC)

		allocator := FixedManualAllocatorCreate(8 * 1024 * 1024)
		ptrs := make([]unsafe.Pointer, preAllocCount)

		for i := range ptrs {
			ptrs[i] = FixedManualAllocatorMalloc(allocator, blockSize, 16)
		}
		for i := 0; i < preAllocCount; i += 2 {
			FixedManualAllocatorFree(allocator, ptrs[i])
			ptrs[i] = nil
		}

		benchmarkWithMetrics(b, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				ptr := FixedManualAllocatorMalloc(allocator, blockSize, 16)
				FixedManualAllocatorFree(allocator, ptr)
			}
		})

		for i := range ptrs {
			ptrs[i] = nil
		}
		ptrs = nil
		FixedManualAllocatorDestroy(allocator)
		allocator = nil
		runtime.GC()
	})

	var sink uintptr

	// -----------------------------------------
	// 5. Stress test — allocate until exhaustion then reset
	// -----------------------------------------
	b.Run("Stress/ExhaustionAndReset", func(b *testing.B) {
		const arenaSize = 16 * 1024 * 1024
		const allocSize = 128
		const allocsPerCycle = arenaSize / allocSize

		oldGC := debug.SetGCPercent(-1)
		defer debug.SetGCPercent(oldGC)

		allocator := FixedManualAllocatorCreate(arenaSize)
		defer FixedManualAllocatorDestroy(allocator)

		benchmarkWithMetrics(b, func(b *testing.B) {

			for i := 0; i < b.N; i++ {
				for j := 0; j < allocsPerCycle-10; j++ {
					ptr := FixedManualAllocatorMalloc(allocator, allocSize, 16)
					sink ^= uintptr(ptr)
				}
				FixedManualAllocatorReset(allocator)
			}
			if sink == 0 {
				b.Errorf("impossible sink value")
			}
		})
	})
}
