package memforge

import (
	"fmt"
	"foundation/benchmarking"
	"math/rand"
	"memcore"
	"os"
	"reflect"
	"runtime"
	"runtime/debug"
	"testing"
)

// Used to prevent compiler optimizing away allocations.
var goHeapSink any

func init() {
	debug.SetPanicOnFault(true)
	defer func() {
		if r := recover(); r != nil {
			debug.FreeOSMemory()
			os.Exit(1)
		}
	}()
}

// -----------------------------------------------------------------------------
// Utility helpers
// -----------------------------------------------------------------------------

func GetFunctionName(i interface{}) string {
	return runtime.FuncForPC(reflect.ValueOf(i).Pointer()).Name()
}

// -----------------------------------------------------------------------------
// Growth strategy for DynamicLinear
// -----------------------------------------------------------------------------

const maxGrowthMem = 8 * 1024 * 1024

var growthStrategyID memcore.FunctionID = DynamicLinearAllocatorGrowthTemplateDoubleOrNeededWithMaxPanicID(maxGrowthMem)

// -----------------------------------------------------------------------------
// Allocator Comparison Suite
// -----------------------------------------------------------------------------

func BenchmarkAllocatorSuite_Comparison(b *testing.B) {
	sizes := []int{32, 256, 1024}

	for _, size := range sizes {
		groupName := fmt.Sprintf("Throughput/Size=%d", size)

		// Go Heap baseline
		b.Run(groupName+"/GoHeap", func(b *testing.B) {
			benchmarking.BenchmarkWithMetrics(b,
				func(b *testing.B) struct{} { return struct{}{} },
				func(_ struct{}, b *testing.B) {
					for i := 0; i < b.N; i++ {
						data := make([]byte, size)
						goHeapSink = data
					}
				},
				func(_ struct{}, b *testing.B) {

				},
			)
		})

		// Fixed Linear Allocator
		b.Run(groupName+"/FixedLinear", func(b *testing.B) {
			type benchData struct {
				allocator    memcore.MarkRaw
				counter      int
				limit        int
				oldGCPercent int
			}

			benchmarking.BenchmarkWithMetrics(b,
				func(b *testing.B) benchData {
					old := debug.SetGCPercent(-1)
					const arenaSize = 8 * 1024 * 1024
					a := FixedLinearAllocatorCreate(arenaSize)
					return benchData{
						allocator:    a,
						counter:      0,
						limit:        arenaSize / size,
						oldGCPercent: old,
					}
				},
				func(d benchData, b *testing.B) {
					for i := 0; i < b.N; i++ {
						if d.counter >= d.limit {
							b.StopTimer()
							FixedLinearAllocatorReset(d.allocator)
							b.StartTimer()
							d.counter = 0
						}
						ptr := FixedLinearAllocatorMalloc(d.allocator, uint64(size), 16)
						_ = ptr
						d.counter++
					}
				},
				func(d benchData, b *testing.B) {
					FixedLinearAllocatorDestroy(d.allocator)
					debug.SetGCPercent(d.oldGCPercent)
				},
			)
		})

		// Dynamic Linear Allocator
		b.Run(groupName+"/DynamicLinear", func(b *testing.B) {
			type benchData struct {
				allocator    memcore.MarkRaw
				counter      int
				limit        int
				oldGCPercent int
			}

			benchmarking.BenchmarkWithMetrics(b,
				func(b *testing.B) benchData {
					old := debug.SetGCPercent(-1)
					const initialSize = 1 * 1024 * 1024
					const maxArenaSize = 8 * 1024 * 1024
					a := DynamicLinearAllocatorCreate(uint64(initialSize), growthStrategyID)
					return benchData{allocator: a, counter: 0, limit: maxArenaSize / size, oldGCPercent: old}
				},
				func(d benchData, b *testing.B) {
					for i := 0; i < b.N; i++ {
						if d.counter >= d.limit {
							b.StopTimer()
							DynamicLinearAllocatorReset(d.allocator)
							b.StartTimer()
							d.counter = 0
						}
						ptr := DynamicLinearAllocatorMalloc(d.allocator, uint64(size), 16)
						_ = ptr
						d.counter++
					}
				},
				func(d benchData, b *testing.B) {
					DynamicLinearAllocatorDestroy(d.allocator)
					debug.SetGCPercent(d.oldGCPercent)
				},
			)
		})

		// Fixed Manual Allocator
		b.Run(groupName+"/FixedManual", func(b *testing.B) {
			type benchData struct {
				allocator    memcore.MarkRaw
				counter      int
				limit        int
				oldGCPercent int
			}

			benchmarking.BenchmarkWithMetrics(b,
				func(b *testing.B) benchData {
					old := debug.SetGCPercent(-1)
					const arenaSize = 8 * 1024 * 1024
					a := FixedManualAllocatorCreate(uint64(arenaSize))
					return benchData{allocator: a, counter: 0, limit: arenaSize / size, oldGCPercent: old}
				},
				func(d benchData, b *testing.B) {
					for i := 0; i < b.N; i++ {
						if d.counter >= d.limit {
							b.StopTimer()
							FixedManualAllocatorReset(d.allocator)
							b.StartTimer()
							d.counter = 0
						}
						ptr := FixedManualAllocatorMalloc(d.allocator, uint64(size), 16)
						_ = ptr
						d.counter++
					}
				},
				func(d benchData, b *testing.B) {
					FixedManualAllocatorDestroy(d.allocator)
					debug.SetGCPercent(d.oldGCPercent)
				},
			)
		})

		// Slab Allocator (only when size ≤ 1024)
		if size <= 1024 {
			b.Run(groupName+"/Slab", func(b *testing.B) {
				type Block struct{ data [1024]byte }
				type benchData struct {
					allocator    memcore.MarkRaw
					ptrs         []memcore.MarkRaw
					capacity     uint64
					oldGCPercent int
				}

				benchmarking.BenchmarkWithMetrics(b,
					func(b *testing.B) benchData {
						old := debug.SetGCPercent(-1)
						const capacity = 10000
						a := SlabAllocatorCreate[Block](capacity)
						return benchData{
							allocator:    a,
							ptrs:         make([]memcore.MarkRaw, 0, capacity),
							capacity:     capacity,
							oldGCPercent: old,
						}
					},
					func(d benchData, b *testing.B) {
						for i := 0; i < b.N; i++ {
							if uint64(len(d.ptrs)) >= d.capacity {
								b.StopTimer()
								SlabAllocatorReset[Block](d.allocator)
								b.StartTimer()
								d.ptrs = d.ptrs[:0]
							}
							ptr := SlabAllocatorMalloc[Block](d.allocator)
							d.ptrs = append(d.ptrs, ptr)
						}
					},
					func(d benchData, b *testing.B) {
						SlabAllocatorDestroy[Block](d.allocator)
						debug.SetGCPercent(d.oldGCPercent)
					},
				)
			})
		}
	}
}

// -----------------------------------------------------------------------------
// Realistic Request Workload Suite
// -----------------------------------------------------------------------------

func BenchmarkAllocatorSuite_RealisticWorkloads(b *testing.B) {
	const allocsPerRequest = 50
	const avgSize = 128

	// --- GoHeap ---
	b.Run("RequestCycle/GoHeap", func(b *testing.B) {
		type benchData struct {
			objects [][]byte
		}
		benchmarking.BenchmarkWithMetrics(b,
			func(b *testing.B) benchData {
				return benchData{objects: make([][]byte, allocsPerRequest)}
			},
			func(d benchData, b *testing.B) {
				for i := 0; i < b.N; i++ {
					for j := 0; j < allocsPerRequest; j++ {
						obj := make([]byte, avgSize)
						d.objects[j] = obj
					}
					goHeapSink = d.objects
				}
			},
			nil,
		)
	})

	// --- FixedLinear ---
	b.Run("RequestCycle/FixedLinear", func(b *testing.B) {
		type benchData struct {
			allocator memcore.MarkRaw
			oldGC     int
		}
		benchmarking.BenchmarkWithMetrics(b,
			func(b *testing.B) benchData {
				old := debug.SetGCPercent(-1)
				a := FixedLinearAllocatorCreate(1 * 1024 * 1024)
				return benchData{allocator: a, oldGC: old}
			},
			func(d benchData, b *testing.B) {
				for i := 0; i < b.N; i++ {
					for j := 0; j < allocsPerRequest; j++ {
						ptr := FixedLinearAllocatorMalloc(d.allocator, avgSize, 16)
						_ = ptr
					}
					FixedLinearAllocatorReset(d.allocator)
				}
			},
			func(d benchData, b *testing.B) {
				FixedLinearAllocatorDestroy(d.allocator)
				debug.SetGCPercent(d.oldGC)
			},
		)
	})

	// --- DynamicLinear ---
	b.Run("RequestCycle/DynamicLinear", func(b *testing.B) {
		type benchData struct {
			allocator memcore.MarkRaw
			oldGC     int
		}
		benchmarking.BenchmarkWithMetrics(b,
			func(b *testing.B) benchData {
				old := debug.SetGCPercent(-1)
				a := DynamicLinearAllocatorCreate(128*1024, growthStrategyID)
				return benchData{allocator: a, oldGC: old}
			},
			func(d benchData, b *testing.B) {
				for i := 0; i < b.N; i++ {
					for j := 0; j < allocsPerRequest; j++ {
						ptr := DynamicLinearAllocatorMalloc(d.allocator, avgSize, 16)
						_ = ptr
					}
					DynamicLinearAllocatorReset(d.allocator)
				}
			},
			func(d benchData, b *testing.B) {
				DynamicLinearAllocatorDestroy(d.allocator)
				debug.SetGCPercent(d.oldGC)
			},
		)
	})

	// --- FixedManual ---
	b.Run("RequestCycle/FixedManual", func(b *testing.B) {
		type benchData struct {
			allocator memcore.MarkRaw
			oldGC     int
		}
		benchmarking.BenchmarkWithMetrics(b,
			func(b *testing.B) benchData {
				old := debug.SetGCPercent(-1)
				a := FixedManualAllocatorCreate(1 * 1024 * 1024)
				return benchData{allocator: a, oldGC: old}
			},
			func(d benchData, b *testing.B) {
				for i := 0; i < b.N; i++ {
					for j := 0; j < allocsPerRequest; j++ {
						ptr := FixedManualAllocatorMalloc(d.allocator, avgSize, 16)
						_ = ptr
					}
					FixedManualAllocatorReset(d.allocator)
				}
			},
			func(d benchData, b *testing.B) {
				FixedManualAllocatorDestroy(d.allocator)
				debug.SetGCPercent(d.oldGC)
			},
		)
	})
}

// -----------------------------------------------------------------------------
// Mixed-size Workload Suite
// -----------------------------------------------------------------------------

func BenchmarkAllocatorSuite_MixedSizeWorkload(b *testing.B) {
	sizes := []int{16, 32, 64, 128, 256, 512, 1024}

	// --- GoHeap ---
	b.Run("Mixed/GoHeap", func(b *testing.B) {
		benchmarking.BenchmarkWithMetrics(b,
			func(b *testing.B) struct{} { return struct{}{} },
			func(_ struct{}, b *testing.B) {
				for i := 0; i < b.N; i++ {
					size := sizes[i%len(sizes)]
					data := make([]byte, size)
					goHeapSink = data
				}
			},
			nil,
		)
	})

	// --- FixedLinear ---
	b.Run("Mixed/FixedLinear", func(b *testing.B) {
		type benchData struct {
			allocator memcore.MarkRaw
			oldGC     int
			bytesUsed uint64
		}
		const arenaSize = 8 * 1024 * 1024

		benchmarking.BenchmarkWithMetrics(b,
			func(b *testing.B) benchData {
				old := debug.SetGCPercent(-1)
				a := FixedLinearAllocatorCreate(arenaSize)
				return benchData{allocator: a, oldGC: old}
			},
			func(d benchData, b *testing.B) {
				for i := 0; i < b.N; i++ {
					size := uint64(sizes[i%len(sizes)])
					if d.bytesUsed+size >= uint64(arenaSize-512) {
						b.StopTimer()
						FixedLinearAllocatorReset(d.allocator)
						b.StartTimer()
						d.bytesUsed = 0
					}
					ptr := FixedLinearAllocatorMalloc(d.allocator, size, 16)
					_ = ptr
					d.bytesUsed += size
				}
			},
			func(d benchData, b *testing.B) {
				FixedLinearAllocatorDestroy(d.allocator)
				debug.SetGCPercent(d.oldGC)
			},
		)
	})

	// --- FixedManual ---
	b.Run("Mixed/FixedManual", func(b *testing.B) {
		type benchData struct {
			allocator memcore.MarkRaw
			oldGC     int
			count     int
		}
		const arenaSize = 8 * 1024 * 1024
		const allocsPerReset = arenaSize / 1024

		benchmarking.BenchmarkWithMetrics(b,
			func(b *testing.B) benchData {
				old := debug.SetGCPercent(-1)
				a := FixedManualAllocatorCreate(arenaSize)
				return benchData{allocator: a, oldGC: old}
			},
			func(d benchData, b *testing.B) {
				for i := 0; i < b.N; i++ {
					if d.count >= allocsPerReset {
						b.StopTimer()
						FixedManualAllocatorReset(d.allocator)
						b.StartTimer()
						d.count = 0
					}
					size := uint64(sizes[i%len(sizes)])
					ptr := FixedManualAllocatorMalloc(d.allocator, size, 16)
					_ = ptr
					d.count++
				}
			},
			func(d benchData, b *testing.B) {
				FixedManualAllocatorDestroy(d.allocator)
				debug.SetGCPercent(d.oldGC)
			},
		)
	})
}

// -----------------------------------------------------------------------------
// Fragmentation Suite (FixedManual only)
// -----------------------------------------------------------------------------

func BenchmarkAllocatorSuite_Fragmentation(b *testing.B) {
	// --- AllocSteady ---
	b.Run("AllocSteady/FixedManual", func(b *testing.B) {
		const arenaSize = 8 * 1024 * 1024
		const objSize = 128
		const poolSize = 4096
		const stride = 3

		type benchData struct {
			allocator memcore.MarkRaw
			ptrs      []memcore.MarkRaw
			oldGC     int
		}

		benchmarking.BenchmarkWithMetrics(b,
			func(b *testing.B) benchData {
				old := debug.SetGCPercent(-1)
				a := FixedManualAllocatorCreate(arenaSize)
				ptrs := make([]memcore.MarkRaw, poolSize)
				for i := range ptrs {
					ptrs[i] = FixedManualAllocatorMalloc(a, objSize, 16)
				}
				return benchData{allocator: a, ptrs: ptrs, oldGC: old}
			},
			func(d benchData, b *testing.B) {
				for i := 0; i < b.N; i++ {
					idx := i % poolSize
					FixedManualAllocatorFree(d.allocator, d.ptrs[idx])
					d.ptrs[idx] = FixedManualAllocatorMalloc(d.allocator, objSize, 16)
				}
			},
			func(d benchData, b *testing.B) {
				FixedManualAllocatorDestroy(d.allocator)
				debug.SetGCPercent(d.oldGC)
			},
		)
	})

	// --- BurstReset ---
	b.Run("BurstReset/FixedManual", func(b *testing.B) {
		const arenaSize = 8 * 1024 * 1024
		const objSize = 256
		const batch = 1024

		type benchData struct {
			allocator memcore.MarkRaw
			oldGC     int
		}

		benchmarking.BenchmarkWithMetrics(b,
			func(b *testing.B) benchData {
				old := debug.SetGCPercent(-1)
				a := FixedManualAllocatorCreate(arenaSize)
				return benchData{allocator: a, oldGC: old}
			},
			func(d benchData, b *testing.B) {
				for i := 0; i < b.N; i++ {
					for j := 0; j < batch; j++ {
						ptr := FixedManualAllocatorMalloc(d.allocator, objSize, 16)
						_ = ptr
					}
					FixedManualAllocatorReset(d.allocator)
				}
			},
			func(d benchData, b *testing.B) {
				FixedManualAllocatorDestroy(d.allocator)
				debug.SetGCPercent(d.oldGC)
			},
		)
	})

	// --- FragmentationStress ---
	b.Run("FragmentationStress/FixedManual", func(b *testing.B) {
		const arenaSize = 16 * 1024 * 1024
		const objMin, objMax = 32, 512
		const ops = 2048

		type benchData struct {
			allocator memcore.MarkRaw
			ptrs      []memcore.MarkRaw
			rnd       *rand.Rand
			oldGC     int
		}

		benchmarking.BenchmarkWithMetrics(b,
			func(b *testing.B) benchData {
				old := debug.SetGCPercent(-1)
				a := FixedManualAllocatorCreate(arenaSize)
				return benchData{
					allocator: a,
					ptrs:      make([]memcore.MarkRaw, ops),
					rnd:       rand.New(rand.NewSource(42)),
					oldGC:     old,
				}
			},
			func(d benchData, b *testing.B) {
				for i := 0; i < b.N; i++ {
					idx := d.rnd.Intn(ops)
					if memcore.MemcoreMarkIsValid(d.ptrs[idx]) && !memcore.MemcoreMarkOffsetIs(d.ptrs[idx], 0x0) && memcore.MemcoreMarkBelongsToRegion(d.ptrs[idx], (d.allocator)) {
						FixedManualAllocatorFree(d.allocator, d.ptrs[idx])
						d.ptrs[idx] = memcore.MarkRaw{}
					} else {
						size := uint64(objMin + d.rnd.Intn(objMax-objMin))
						d.ptrs[idx] = FixedManualAllocatorMalloc(d.allocator, size, 16)
					}
				}
			},
			func(d benchData, b *testing.B) {
				FixedManualAllocatorDestroy(d.allocator)
				debug.SetGCPercent(d.oldGC)
			},
		)
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
			type benchData struct {
				allocator memcore.MarkRaw
				oldGC     int
			}
			benchmarking.BenchmarkWithMetrics(b,
				func(b *testing.B) benchData {
					old := debug.SetGCPercent(-1)
					a := FixedManualAllocatorCreate(8 * 1024 * 1024)
					return benchData{allocator: a, oldGC: old}
				},
				func(d benchData, b *testing.B) {
					for i := 0; i < b.N; i++ {
						ptr := FixedManualAllocatorMalloc(d.allocator, uint64(size), 16)
						FixedManualAllocatorFree(d.allocator, ptr)
					}
				},
				func(d benchData, b *testing.B) {
					FixedManualAllocatorDestroy(d.allocator)
					debug.SetGCPercent(d.oldGC)
					runtime.GC()
				},
			)
		})
	}

	// -----------------------------------------
	// 2. Sustained mixed-size allocations
	// -----------------------------------------
	b.Run("MixedSizes", func(b *testing.B) {
		type benchData struct {
			allocator  memcore.MarkRaw
			oldGC      int
			blockSizes []int
		}
		benchmarking.BenchmarkWithMetrics(b,
			func(b *testing.B) benchData {
				old := debug.SetGCPercent(-1)
				a := FixedManualAllocatorCreate(8 * 1024 * 1024)
				return benchData{
					allocator:  a,
					oldGC:      old,
					blockSizes: []int{16, 32, 64, 128, 256, 512, 1024},
				}
			},
			func(d benchData, b *testing.B) {
				for i := 0; i < b.N; i++ {
					size := d.blockSizes[i%len(d.blockSizes)]
					ptr := FixedManualAllocatorMalloc(d.allocator, uint64(size), 16)
					FixedManualAllocatorFree(d.allocator, ptr)
				}
			},
			func(d benchData, b *testing.B) {
				FixedManualAllocatorDestroy(d.allocator)
				debug.SetGCPercent(d.oldGC)
				runtime.GC()
			},
		)
	})

	// -----------------------------------------
	// 3. Request cycle pattern — allocate N objects then free all
	// -----------------------------------------
	b.Run("RequestCyclePattern", func(b *testing.B) {
		const allocsPerCycle = 100
		const avgSize = 128
		type benchData struct {
			allocator memcore.MarkRaw
			oldGC     int
			ptrs      []memcore.MarkRaw
		}
		benchmarking.BenchmarkWithMetrics(b,
			func(b *testing.B) benchData {
				old := debug.SetGCPercent(-1)
				a := FixedManualAllocatorCreate(2 * 1024 * 1024)
				ptrs := make([]memcore.MarkRaw, allocsPerCycle)
				return benchData{allocator: a, oldGC: old, ptrs: ptrs}
			},
			func(d benchData, b *testing.B) {
				for i := 0; i < b.N; i++ {
					for j := 0; j < allocsPerCycle; j++ {
						d.ptrs[j] = FixedManualAllocatorMalloc(d.allocator, avgSize, 16)
					}
					for _, p := range d.ptrs {
						FixedManualAllocatorFree(d.allocator, p)
					}
				}
			},
			func(d benchData, b *testing.B) {
				FixedManualAllocatorDestroy(d.allocator)
				debug.SetGCPercent(d.oldGC)
				runtime.GC()
			},
		)
	})

	// -----------------------------------------
	// 4. Fragmentation simulation (interleaved alloc/free)
	// -----------------------------------------
	b.Run("FragmentationPattern", func(b *testing.B) {
		const preAllocCount = 1000
		const blockSize = 128
		type benchData struct {
			allocator memcore.MarkRaw
			ptrs      []memcore.MarkRaw
			oldGC     int
		}
		benchmarking.BenchmarkWithMetrics(b,
			func(b *testing.B) benchData {
				old := debug.SetGCPercent(-1)
				a := FixedManualAllocatorCreate(8 * 1024 * 1024)
				ptrs := make([]memcore.MarkRaw, preAllocCount)
				for i := range ptrs {
					ptrs[i] = FixedManualAllocatorMalloc(a, blockSize, 16)
				}
				for i := 0; i < preAllocCount; i += 2 {
					FixedManualAllocatorFree(a, ptrs[i])
				}
				return benchData{allocator: a, ptrs: ptrs, oldGC: old}
			},
			func(d benchData, b *testing.B) {
				for i := 0; i < b.N; i++ {
					ptr := FixedManualAllocatorMalloc(d.allocator, blockSize, 16)
					FixedManualAllocatorFree(d.allocator, ptr)
				}
			},
			func(d benchData, b *testing.B) {
				FixedManualAllocatorDestroy(d.allocator)
				debug.SetGCPercent(d.oldGC)
				runtime.GC()
			},
		)
	})

	// -----------------------------------------
	// 5. Stress test — allocate until exhaustion then reset
	// -----------------------------------------
	b.Run("Stress/ExhaustionAndReset", func(b *testing.B) {
		const arenaSize = 16 * 1024 * 1024
		const allocSize = 128
		const allocsPerCycle = arenaSize / allocSize

		type benchData struct {
			allocator memcore.MarkRaw
			oldGC     int
			sink      uintptr
		}

		benchmarking.BenchmarkWithMetrics(b,
			func(b *testing.B) benchData {
				old := debug.SetGCPercent(-1)
				a := FixedManualAllocatorCreate(arenaSize)
				return benchData{allocator: a, oldGC: old}
			},
			func(d benchData, b *testing.B) {
				for i := 0; i < b.N; i++ {
					for j := 0; j < allocsPerCycle-10; j++ {
						ptr := FixedManualAllocatorMalloc(d.allocator, allocSize, 16)
						d.sink ^= uintptr(memcore.MemcoreMarkDereference(ptr))
					}
					FixedManualAllocatorReset(d.allocator)
				}
				if d.sink == 0 {
					b.Errorf("impossible sink value")
				}
			},
			func(d benchData, b *testing.B) {
				FixedManualAllocatorDestroy(d.allocator)
				debug.SetGCPercent(d.oldGC)
				runtime.GC()
			},
		)
	})
}
