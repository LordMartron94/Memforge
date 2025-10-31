//go:build memforge_debug

package memforge

import (
	"fmt"
	"runtime"
	"slices"
	"strings"
	"time"
	"unsafe"
)

var debugStats = make(map[uintptr]*allocatorStats)

type allocatorStats struct {
	allocatorName                                        string
	createdAt                                            time.Time
	creator                                              string // file:line (func)
	allAllocations                                       []allocation
	currentlyLiveAllocations                             []allocation
	totalBytes, liveBytes, peakLiveBytes, peakLiveAllocs uint64
}

type allocation struct {
	ptr       uintptr
	sizeBytes uint64
	timestamp time.Time
	stack     string // only captured when leak debugging
}

// Register a new allocator and record its creation site.
func memforgeAllocatorRegister(allocatorPtr unsafe.Pointer, name string) {
	var pcs [3]uintptr
	n := runtime.Callers(2, pcs[:])
	frame, _ := runtime.CallersFrames(pcs[:n]).Next()

	debugStats[uintptr(allocatorPtr)] = &allocatorStats{
		allocatorName: name,
		createdAt:     time.Now(),
		creator:       fmt.Sprintf("%s:%d (%s)", frame.File, frame.Line, frame.Function),
	}
}

// Record a new allocation in the debug tracker.
func memforgeAllocationAdd(allocatorPtr, allocationPtr unsafe.Pointer, sizeBytes uint64) {
	ptr := uintptr(allocatorPtr)
	stats := debugStats[ptr]
	if stats == nil {
		return
	}

	// only capture stack when debugging mode enabled or large allocation
	stack := ""
	if sizeBytes > 4096 {
		buf := make([]byte, 512)
		n := runtime.Stack(buf, false)
		stack = string(buf[:n])
	}

	entry := allocation{
		ptr:       uintptr(allocationPtr),
		sizeBytes: sizeBytes,
		timestamp: time.Now(),
		stack:     stack,
	}
	stats.allAllocations = append(stats.allAllocations, entry)
	stats.currentlyLiveAllocations = append(stats.currentlyLiveAllocations, entry)
	stats.totalBytes += sizeBytes
	stats.liveBytes += sizeBytes

	if stats.liveBytes > stats.peakLiveBytes {
		stats.peakLiveBytes = stats.liveBytes
	}
	if uint64(len(stats.currentlyLiveAllocations)) > stats.peakLiveAllocs {
		stats.peakLiveAllocs = uint64(len(stats.currentlyLiveAllocations))
	}
}

// Remove a single freed allocation.
func memforgeAllocationRemove(allocatorPtr, allocationPtr unsafe.Pointer) {
	stats := debugStats[uintptr(allocatorPtr)]
	if stats == nil {
		return
	}
	ptr2 := uintptr(allocationPtr)
	stats.currentlyLiveAllocations = slices.DeleteFunc(stats.currentlyLiveAllocations, func(a allocation) bool {
		if a.ptr == ptr2 {
			stats.liveBytes -= a.sizeBytes
			return true
		}
		return false
	})
}

// Remove all allocations for a destroyed allocator.
func memforgeAllocatorRemoveAll(allocatorPtr unsafe.Pointer) {
	stats := debugStats[uintptr(allocatorPtr)]
	if stats == nil {
		return
	}
	stats.currentlyLiveAllocations = nil
	stats.liveBytes = 0
}

// MemforgeMemoryDebug prints a focused summary of allocator usage and leaks.
func MemforgeMemoryDebug() {
	if len(debugStats) == 0 {
		fmt.Println("🧩 Memforge: no allocators registered.")
		return
	}

	var (
		totalAllocs, totalLiveAllocs int
		totalBytes, totalLiveBytes   uint64
		sb                           strings.Builder
	)

	sb.WriteString("\n━━━━━━━━━━━━━━━━━━━━━━━\n")
	sb.WriteString("🧩 MEMFORGE MEMORY DEBUGGER\n")
	sb.WriteString(fmt.Sprintf("Time: %s\n", time.Now().Format(time.RFC3339)))
	sb.WriteString("━━━━━━━━━━━━━━━━━━━━━━━\n")

	// --- aggregate by allocator type ---
	typeGroup := make(map[string][]uintptr)
	for ptr, stats := range debugStats {
		typeGroup[stats.allocatorName] = append(typeGroup[stats.allocatorName], ptr)
	}

	// Sort allocator types alphabetically
	typeNames := make([]string, 0, len(typeGroup))
	for name := range typeGroup {
		typeNames = append(typeNames, name)
	}
	slices.Sort(typeNames)

	for _, name := range typeNames {
		ptrs := typeGroup[name]

		// Compute totals per type
		typeTotalAllocs, typeLiveAllocs := 0, 0
		typeTotalBytes, typeLiveBytes := uint64(0), uint64(0)
		for _, p := range ptrs {
			st := debugStats[p]
			typeTotalAllocs += len(st.allAllocations)
			typeLiveAllocs += len(st.currentlyLiveAllocations)
			typeTotalBytes += st.totalBytes
			typeLiveBytes += st.liveBytes
		}

		totalAllocs += typeTotalAllocs
		totalLiveAllocs += typeLiveAllocs
		totalBytes += typeTotalBytes
		totalLiveBytes += typeLiveBytes

		sb.WriteString(fmt.Sprintf("\n📦 %s\n", name))
		sb.WriteString(fmt.Sprintf("  allocators: %d  total=%s  live=%s  leaks=%d\n",
			len(ptrs),
			humanBytes(float64(typeTotalBytes)),
			humanBytes(float64(typeLiveBytes)),
			typeLiveAllocs))

		// If only a few allocators of this type, print them individually.
		if len(ptrs) <= 5 || typeLiveAllocs > 0 {
			for _, addr := range ptrs {
				st := debugStats[addr]
				if len(st.allAllocations) == 0 {
					continue
				}
				allocCount := len(st.allAllocations)
				liveCount := len(st.currentlyLiveAllocations)
				if allocCount == 0 && liveCount == 0 {
					continue
				}

				sb.WriteString(fmt.Sprintf("    → %#x  allocs=%-3d live=%-3d bytes=%-10s\n",
					addr, allocCount, liveCount, humanBytes(float64(st.totalBytes))))
				sb.WriteString(fmt.Sprintf("      created %s | %s\n",
					st.createdAt.Format("15:04:05"), st.creator))

				if liveCount > 0 {
					live := slices.Clone(st.currentlyLiveAllocations)
					slices.SortFunc(live, func(a, b allocation) int {
						switch {
						case a.sizeBytes > b.sizeBytes:
							return -1
						case a.sizeBytes < b.sizeBytes:
							return 1
						default:
							return 0
						}
					})

					sb.WriteString("      🔴 Live allocations:\n")
					for i := 0; i < min(3, len(live)); i++ {
						sb.WriteString(fmt.Sprintf("        • %#x  %s  at %s\n",
							live[i].ptr,
							humanBytes(float64(live[i].sizeBytes)),
							live[i].timestamp.Format("15:04:05")))

						if live[i].stack != "" {
							stack := indent(live[i].stack, "          ")
							sb.WriteString(stack)
							sb.WriteRune('\n')
						}
					}
				}
			}
		}
	}

	// --- global summary ---
	sb.WriteString("\n━━━━━━━━━━━━━━━━━━━━━━━\n")
	sb.WriteString("📊 GLOBAL SUMMARY\n")
	sb.WriteString(fmt.Sprintf("  Allocator types: %d\n", len(typeNames)))
	sb.WriteString(fmt.Sprintf("  Total allocators: %d\n", len(debugStats)))
	sb.WriteString(fmt.Sprintf("  Total allocations: %d\n", totalAllocs))
	sb.WriteString(fmt.Sprintf("  Live allocations:  %d\n", totalLiveAllocs))
	sb.WriteString(fmt.Sprintf("  Bytes total: %s   Live: %s\n",
		humanBytes(float64(totalBytes)),
		humanBytes(float64(totalLiveBytes))))

	if totalLiveAllocs > 0 {
		sb.WriteString(fmt.Sprintf("  🚨 %d live allocations remain across all allocators\n", totalLiveAllocs))
	} else {
		sb.WriteString("  ✅ All memory freed — no leaks detected\n")
	}
	sb.WriteString("━━━━━━━━━━━━━━━━━━━━━━━\n")

	fmt.Println(sb.String())
}

// --- helpers ---

func humanBytes(b float64) string {
	switch {
	case b < 1024:
		return fmt.Sprintf("%.0f B", b)
	case b < 1024*1024:
		return fmt.Sprintf("%.2f KiB", b/1024)
	case b < 1024*1024*1024:
		return fmt.Sprintf("%.2f MiB", b/1024/1024)
	default:
		return fmt.Sprintf("%.2f GiB", b/1024/1024/1024)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}
