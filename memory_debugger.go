//go:build memforge_debug

package memforge

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unsafe"
)

var debugStats = make(map[uintptr]*allocatorStats)

type allocatorStats struct {
	allocatorName            string
	createdAt                time.Time
	allAllocations           []allocation
	currentlyLiveAllocations []allocation
	totalBytes               uint64
	liveBytes                uint64
	peakLiveBytes            uint64
	peakLiveAllocs           uint64
}

type allocation struct {
	ptr       uintptr
	sizeBytes uint64
	timestamp time.Time
}

func memforgeAllocatorRegister(allocatorPtr unsafe.Pointer, name string) {
	debugStats[uintptr(allocatorPtr)] = &allocatorStats{
		allocatorName: name,
		createdAt:     time.Now(),
	}
}

func memforgeAllocationAdd(allocatorPtr, allocationPtr unsafe.Pointer, sizeBytes uint64) {
	ptr := uintptr(allocatorPtr)
	stats := debugStats[ptr]
	if stats == nil {
		return
	}

	allocation := allocation{
		ptr:       uintptr(allocationPtr),
		sizeBytes: sizeBytes,
		timestamp: time.Now(),
	}

	stats.allAllocations = append(stats.allAllocations, allocation)
	stats.currentlyLiveAllocations = append(stats.currentlyLiveAllocations, allocation)
	stats.totalBytes += sizeBytes
	stats.liveBytes += sizeBytes

	if stats.liveBytes > stats.peakLiveBytes {
		stats.peakLiveBytes = stats.liveBytes
	}
	if uint64(len(stats.currentlyLiveAllocations)) > stats.peakLiveAllocs {
		stats.peakLiveAllocs = uint64(len(stats.currentlyLiveAllocations))
	}
}

func memforgeAllocationRemove(allocatorPtr, allocationPtr unsafe.Pointer) {
	ptr := uintptr(allocatorPtr)
	stats := debugStats[ptr]
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

func memforgeAllocatorRemoveAll(allocatorPtr unsafe.Pointer) {
	ptr := uintptr(allocatorPtr)
	stats := debugStats[ptr]
	if stats == nil {
		return
	}
	stats.currentlyLiveAllocations = nil
	stats.liveBytes = 0
}

// MemforgeMemoryDebug prints the current state of allocations.
func MemforgeMemoryDebug() {
	sb := strings.Builder{}
	sb.WriteString("\n━━━━━━━━━━━━━━━━━━━━━━━\n")
	sb.WriteString("🧩 MEMFORGE MEMORY DEBUGGER\n")
	sb.WriteString(fmt.Sprintf("Time: %s\n", time.Now().Format(time.RFC3339)))
	sb.WriteString("━━━━━━━━━━━━━━━━━━━━━━━\n")

	var totalAllocs, liveAllocs int
	var totalBytes, liveBytes uint64

	for addr, stats := range debugStats {
		allAllocs := len(stats.allAllocations)
		liveAllocs := len(stats.currentlyLiveAllocations)
		totalAllocs += allAllocs
		liveAllocs += liveAllocs
		totalBytes += stats.totalBytes
		liveBytes += stats.liveBytes

		fragmentationRatio := 0.0
		if stats.totalBytes > 0 {
			fragmentationRatio = float64(stats.liveBytes) / float64(stats.totalBytes)
		}

		sb.WriteString(fmt.Sprintf("\n📦 Allocator: %s (ptr=%#x)\n", stats.allocatorName, addr))
		sb.WriteString(fmt.Sprintf("  Created:     %s\n", stats.createdAt.Format(time.Kitchen)))
		sb.WriteString(fmt.Sprintf("  Total allocs: %d   Live: %d   Peak: %d\n", allAllocs, liveAllocs, stats.peakLiveAllocs))
		sb.WriteString(fmt.Sprintf("  Bytes total:  %-10s   Live: %-10s   Peak: %-10s\n",
			humanBytes(float64(stats.totalBytes)),
			humanBytes(float64(stats.liveBytes)),
			humanBytes(float64(stats.peakLiveBytes))))
		sb.WriteString(fmt.Sprintf("  Fragmentation: %.1f%%\n", (1-fragmentationRatio)*100))

		if liveAllocs > 0 {
			// Sort by size descending
			live := slices.Clone(stats.currentlyLiveAllocations)
			slices.SortFunc(live, func(a, b allocation) int {
				if a.sizeBytes > b.sizeBytes {
					return -1
				}
				if a.sizeBytes < b.sizeBytes {
					return 1
				}
				return 0
			})
			max := min(3, len(live))
			sb.WriteString("  Top live allocations:\n")
			for i := 0; i < max; i++ {
				sb.WriteString(fmt.Sprintf("    • ptr=%#x size=%s at %s\n",
					live[i].ptr,
					humanBytes(float64(live[i].sizeBytes)),
					live[i].timestamp.Format("15:04:05")))
			}
		}
	}

	sb.WriteString("\n━━━━━━━━━━━━━━━━━━━━━━━\n")
	sb.WriteString("📊 GLOBAL SUMMARY\n")
	sb.WriteString(fmt.Sprintf("  Total allocators: %d\n", len(debugStats)))
	sb.WriteString(fmt.Sprintf("  Total allocations: %d\n", totalAllocs))
	sb.WriteString(fmt.Sprintf("  Live allocations:  %d\n", liveAllocs))
	sb.WriteString(fmt.Sprintf("  Bytes total: %s   Live: %s\n",
		humanBytes(float64(totalBytes)),
		humanBytes(float64(liveBytes))))
	if liveAllocs > 0 {
		sb.WriteString(fmt.Sprintf("🚨 WARNING: %d live allocations detected!\n", liveAllocs))
	} else {
		sb.WriteString("✅ No live allocations — all memory freed.\n")
	}
	sb.WriteString("━━━━━━━━━━━━━━━━━━━━━━━\n")

	fmt.Println(sb.String())
}

func humanBytes(b float64) string {
	if b < 1024 {
		return fmt.Sprintf("%.0f B", b)
	}
	k := b / 1024
	if k < 1024 {
		return fmt.Sprintf("%.2f KiB", k)
	}
	m := k / 1024
	if m < 1024 {
		return fmt.Sprintf("%.2f MiB", m)
	}
	g := m / 1024
	return fmt.Sprintf("%.2f GiB", g)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
