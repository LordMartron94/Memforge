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

	sb := strings.Builder{}

	writeHeader(&sb)
	typeGroups := groupAllocatorsByType(debugStats)
	typeNames := sortedTypeNames(typeGroups)

	var totals globalTotals

	for _, name := range typeNames {
		ptrs := typeGroups[name]
		typeTotal := computeTypeTotals(ptrs)

		totals.add(typeTotal)

		writeTypeSummary(&sb, name, ptrs, typeTotal)
	}

	writeGlobalSummary(&sb, totals, len(typeNames), len(debugStats))
	fmt.Println(sb.String())
}

// --- helpers ---

type globalTotals struct {
	totalAllocs, totalLiveAllocs int
	totalBytes, totalLiveBytes   uint64
}

func (t *globalTotals) add(other globalTotals) {
	t.totalAllocs += other.totalAllocs
	t.totalLiveAllocs += other.totalLiveAllocs
	t.totalBytes += other.totalBytes
	t.totalLiveBytes += other.totalLiveBytes
}

func writeHeader(sb *strings.Builder) {
	sb.WriteString("\n━━━━━━━━━━━━━━━━━━━━━━━\n")
	sb.WriteString("🧩 MEMFORGE MEMORY DEBUGGER\n")
	sb.WriteString(fmt.Sprintf("Time: %s\n", time.Now().Format(time.RFC3339)))
	sb.WriteString("━━━━━━━━━━━━━━━━━━━━━━━\n")
}

func writeGlobalSummary(sb *strings.Builder, totals globalTotals, typeCount, allocatorCount int) {
	sb.WriteString("\n━━━━━━━━━━━━━━━━━━━━━━━\n")
	sb.WriteString("📊 GLOBAL SUMMARY\n")
	sb.WriteString(fmt.Sprintf("  Allocator types: %d\n", typeCount))
	sb.WriteString(fmt.Sprintf("  Total allocators: %d\n", allocatorCount))
	sb.WriteString(fmt.Sprintf("  Total allocations: %d\n", totals.totalAllocs))
	sb.WriteString(fmt.Sprintf("  Live allocations:  %d\n", totals.totalLiveAllocs))
	sb.WriteString(fmt.Sprintf("  Bytes total: %s   Live: %s\n",
		humanBytes(float64(totals.totalBytes)),
		humanBytes(float64(totals.totalLiveBytes))))

	if totals.totalLiveAllocs > 0 {
		sb.WriteString(fmt.Sprintf("  🚨 %d live allocations remain across all allocators\n", totals.totalLiveAllocs))
	} else {
		sb.WriteString("  ✅ All memory freed — no leaks detected\n")
	}
	sb.WriteString("━━━━━━━━━━━━━━━━━━━━━━━\n")
}

func groupAllocatorsByType(stats map[uintptr]*allocatorStats) map[string][]uintptr {
	group := make(map[string][]uintptr)
	for ptr, st := range stats {
		group[st.allocatorName] = append(group[st.allocatorName], ptr)
	}
	return group
}

func sortedTypeNames(group map[string][]uintptr) []string {
	names := make([]string, 0, len(group))
	for name := range group {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func computeTypeTotals(ptrs []uintptr) globalTotals {
	var totals globalTotals
	for _, p := range ptrs {
		st := debugStats[p]
		totals.totalAllocs += len(st.allAllocations)
		totals.totalLiveAllocs += len(st.currentlyLiveAllocations)
		totals.totalBytes += st.totalBytes
		totals.totalLiveBytes += st.liveBytes
	}
	return totals
}

func writeTypeSummary(sb *strings.Builder, name string, ptrs []uintptr, totals globalTotals) {
	sb.WriteString(fmt.Sprintf("\n📦 %s\n", name))
	sb.WriteString(fmt.Sprintf("  allocators: %d  total=%s  live=%s  leaks=%d\n",
		len(ptrs),
		humanBytes(float64(totals.totalBytes)),
		humanBytes(float64(totals.totalLiveBytes)),
		totals.totalLiveAllocs))

	if len(ptrs) <= 5 || totals.totalLiveAllocs > 0 {
		for _, addr := range ptrs {
			writeAllocatorSummary(sb, addr)
		}
	}
}

func writeAllocatorSummary(sb *strings.Builder, addr uintptr) {
	st := debugStats[addr]
	if len(st.allAllocations) == 0 {
		return
	}

	allocCount := len(st.allAllocations)
	liveCount := len(st.currentlyLiveAllocations)
	if allocCount == 0 && liveCount == 0 {
		return
	}

	sb.WriteString(fmt.Sprintf("    → %#x  allocs=%-3d live=%-3d bytes=%-10s\n",
		addr, allocCount, liveCount, humanBytes(float64(st.totalBytes))))
	sb.WriteString(fmt.Sprintf("      created %s | %s\n",
		st.createdAt.Format("15:04:05"), st.creator))

	if liveCount > 0 {
		writeLiveAllocations(sb, st.currentlyLiveAllocations)
	}
}

func writeLiveAllocations(sb *strings.Builder, allocs []allocation) {
	live := slices.Clone(allocs)
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
		a := live[i]
		sb.WriteString(fmt.Sprintf("        • %#x  %s  at %s\n",
			a.ptr,
			humanBytes(float64(a.sizeBytes)),
			a.timestamp.Format("15:04:05")))

		if a.stack != "" {
			stack := indent(a.stack, "          ")
			sb.WriteString(stack)
			sb.WriteRune('\n')
		}
	}
}

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
