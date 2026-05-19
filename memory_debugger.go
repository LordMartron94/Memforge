//go:build memforge_debug

package memforge

import (
	"fmt"
	"memcore"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

// ANSI color codes
const (
	colorReset   = "\033[0m"
	colorRed     = "\033[31m"
	colorGreen   = "\033[32m"
	colorYellow  = "\033[33m"
	colorBlue    = "\033[34m"
	colorMagenta = "\033[35m"
	colorCyan    = "\033[36m"
	colorWhite   = "\033[37m"
	colorGray    = "\033[90m"

	colorBoldRed     = "\033[1;31m"
	colorBoldGreen   = "\033[1;32m"
	colorBoldYellow  = "\033[1;33m"
	colorBoldBlue    = "\033[1;34m"
	colorBoldMagenta = "\033[1;35m"
	colorBoldCyan    = "\033[1;36m"
	colorBoldWhite   = "\033[1;37m"

	// Background colors for highlights
	bgRed    = "\033[41m"
	bgYellow = "\033[43m"
	bgGreen  = "\033[42m"
)

var debugStats = make(map[memcore.MarkRaw]*allocatorStats)
var timelineEvents []timelineEvent
var timelineSeq uint64
var internalPrefixes = []string{"runtime.", "reflect.", "memcore.", "testing.", "memforge.test"}

type timelineEvent struct {
	seq               uint64
	timestamp         time.Time
	kind              MemforgeTimelineEventKind
	allocatorAddress  uintptr
	allocatorName     string
	stack             string
	allocationAddress uintptr
	sizeBytes         uint64
	originalSeq       uint64
	originalCreatedAt time.Time
	freedAllocations  []MemforgeTimelineFreedAllocation
}

type allocatorStats struct {
	allocatorName                                        string
	creator                                              string
	destroyed                                            bool
	createdAt                                            time.Time
	lastAllocAt                                          time.Time
	allAllocations                                       []allocation
	currentlyLiveAllocations                             []allocation
	totalBytes, liveBytes, peakLiveBytes, peakLiveAllocs uint64
}

type allocation struct {
	seq       uint64
	ptr       memcore.MarkRaw
	sizeBytes uint64
	timestamp time.Time
	creator   string
}

// memforgeCaptureCallStack returns a formatted call stack string for debugging.
// It skips internal frames and formats each frame as "Func → Func → Func".
func memforgeCaptureCallStack(skip int) string {
	const maxDepth = 16
	var pcs [maxDepth]uintptr
	n := runtime.Callers(skip+2, pcs[:]) // skip runtime + helper itself
	frames := runtime.CallersFrames(pcs[:n])

	stack := make([]string, 0, n)
	for {
		f, more := frames.Next()
		if !isInternalFrame(f.Function) {
			stack = append(stack, filepath.Base(f.Function))
		}
		if !more {
			break
		}
	}
	slices.Reverse(stack)
	return strings.Join(stack, " → ")
}

func isInternalFrame(fn string) bool {
	for _, p := range internalPrefixes {
		if strings.Contains(fn, p) {
			return true
		}
	}
	return false
}

func appendTimelineEvent(evt timelineEvent) {
	timelineSeq++
	evt.seq = timelineSeq
	evt.timestamp = time.Now()
	timelineEvents = append(timelineEvents, evt)
}

func timelineAllocatorAddress(allocatorPtr memcore.MarkRaw) uintptr {
	return uintptr(memcore.MemcoreMarkDereferenceUnsafe(allocatorPtr))
}

func timelineAllocationAddress(allocationPtr memcore.MarkRaw) uintptr {
	return uintptr(memcore.MemcoreMarkDereferenceUnsafe(allocationPtr))
}

func timelineFreedAllocationsFromLive(live []allocation) []MemforgeTimelineFreedAllocation {
	if len(live) == 0 {
		return nil
	}
	freed := make([]MemforgeTimelineFreedAllocation, len(live))
	for i, entry := range live {
		freed[i] = MemforgeTimelineFreedAllocation{
			AllocationAddress: uintptr(memcore.MemcoreMarkDereferenceUnsafe(entry.ptr)),
			SizeBytes:         entry.sizeBytes,
			OriginalSeq:       entry.seq,
			OriginalCreatedAt: entry.timestamp,
		}
	}
	return freed
}

// Register a new allocator and record its creation site.
func memforgeAllocatorRegister(allocatorPtr memcore.MarkRaw, name string) {
	debugStats[allocatorPtr] = &allocatorStats{
		allocatorName: name,
		createdAt:     time.Now(),
		creator:       memforgeCaptureCallStack(2),
	}
	appendTimelineEvent(timelineEvent{
		kind:             MemforgeTimelineEventAllocatorRegister,
		allocatorAddress: timelineAllocatorAddress(allocatorPtr),
		allocatorName:    name,
		stack:            memforgeCaptureCallStack(2),
	})
}

// Record a new allocation in the debug tracker.
func memforgeAllocationAdd(allocatorPtr, allocationPtr memcore.MarkRaw, sizeBytes uint64) {
	stats := debugStats[allocatorPtr]
	if stats == nil {
		return
	}

	creator := memforgeCaptureCallStack(2)
	now := time.Now()
	entry := allocation{
		ptr:       allocationPtr,
		sizeBytes: sizeBytes,
		timestamp: now,
		creator:   creator,
	}
	stats.lastAllocAt = now
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

	appendTimelineEvent(timelineEvent{
		kind:              MemforgeTimelineEventAllocation,
		allocatorAddress:  timelineAllocatorAddress(allocatorPtr),
		allocatorName:     stats.allocatorName,
		stack:             creator,
		allocationAddress: timelineAllocationAddress(allocationPtr),
		sizeBytes:         sizeBytes,
	})
	entry.seq = timelineSeq
	stats.allAllocations[len(stats.allAllocations)-1] = entry
	stats.currentlyLiveAllocations[len(stats.currentlyLiveAllocations)-1] = entry
}

// Remove a single freed allocation.
func memforgeAllocationRemove(allocatorPtr, allocationPtr memcore.MarkRaw) {
	stats := debugStats[allocatorPtr]
	if stats == nil {
		return
	}
	ptr2 := allocationPtr
	var removed allocation
	found := false
	stats.currentlyLiveAllocations = slices.DeleteFunc(stats.currentlyLiveAllocations, func(a allocation) bool {
		if a.ptr == ptr2 {
			removed = a
			found = true
			stats.liveBytes -= a.sizeBytes
			return true
		}
		return false
	})
	if !found {
		return
	}

	appendTimelineEvent(timelineEvent{
		kind:              MemforgeTimelineEventFreeManual,
		allocatorAddress:  timelineAllocatorAddress(allocatorPtr),
		allocatorName:     stats.allocatorName,
		stack:             memforgeCaptureCallStack(2),
		allocationAddress: timelineAllocationAddress(allocationPtr),
		sizeBytes:         removed.sizeBytes,
		originalSeq:       removed.seq,
		originalCreatedAt: removed.timestamp,
	})
}

// Marks an allocator as destroyed.
func memforgeAllocatorDestroy(allocatorPtr memcore.MarkRaw) {
	stats := debugStats[allocatorPtr]
	if stats == nil {
		return
	}

	live := slices.Clone(stats.currentlyLiveAllocations)
	if len(live) > 0 {
		appendTimelineEvent(timelineEvent{
			kind:             MemforgeTimelineEventFreeRegionalDestroy,
			allocatorAddress: timelineAllocatorAddress(allocatorPtr),
			allocatorName:    stats.allocatorName,
			stack:            memforgeCaptureCallStack(2),
			freedAllocations: timelineFreedAllocationsFromLive(live),
		})
	}

	appendTimelineEvent(timelineEvent{
		kind:             MemforgeTimelineEventAllocatorDestroy,
		allocatorAddress: timelineAllocatorAddress(allocatorPtr),
		allocatorName:    stats.allocatorName,
		stack:            memforgeCaptureCallStack(2),
	})

	stats.currentlyLiveAllocations = nil
	stats.liveBytes = 0
	stats.destroyed = true
}

// Remove all allocations for an allocator.
func memforgeAllocatorRemoveAll(allocatorPtr memcore.MarkRaw) {
	stats := debugStats[allocatorPtr]
	if stats == nil {
		return
	}

	live := slices.Clone(stats.currentlyLiveAllocations)
	if len(live) > 0 {
		appendTimelineEvent(timelineEvent{
			kind:             MemforgeTimelineEventFreeRegionalReset,
			allocatorAddress: timelineAllocatorAddress(allocatorPtr),
			allocatorName:    stats.allocatorName,
			stack:            memforgeCaptureCallStack(2),
			freedAllocations: timelineFreedAllocationsFromLive(live),
		})
	}

	stats.currentlyLiveAllocations = nil
	stats.liveBytes = 0
}

/*
MemforgeMemorySnapshotGet returns structured allocator and leak telemetry without printing.
*/
func MemforgeMemorySnapshotGet() MemforgeMemorySnapshot {
	snapshot := MemforgeMemorySnapshot{
		Available:  true,
		CapturedAt: time.Now(),
	}

	if len(debugStats) == 0 {
		return snapshot
	}

	typeGroups := groupAllocatorsByType(debugStats)
	typeNames := sortedTypeNames(typeGroups)
	snapshot.AllocatorTypeCount = len(typeNames)

	allocators := make([]MemforgeAllocatorSnapshot, 0, len(debugStats))
	for ptr, stats := range debugStats {
		allocators = append(allocators, buildAllocatorSnapshot(ptr, stats))
	}
	slices.SortFunc(allocators, func(a, b MemforgeAllocatorSnapshot) int {
		switch {
		case a.Name < b.Name:
			return -1
		case a.Name > b.Name:
			return 1
		default:
			return 0
		}
	})

	snapshot.Allocators = allocators
	snapshot.AllocatorCount = len(allocators)

	for _, allocator := range allocators {
		snapshot.TotalAllocationCount += allocator.TotalAllocations
		snapshot.LiveAllocationCount += allocator.LiveAllocations
		snapshot.TotalBytes += allocator.TotalBytes
		snapshot.LiveBytes += allocator.LiveBytes

		if allocator.Destroyed {
			snapshot.DestroyedAllocatorCount++
		} else {
			snapshot.ActiveAllocatorCount++
		}
		if allocator.LiveAllocations > 0 {
			snapshot.AllocatorsWithLiveAllocs++
		}
	}

	snapshot.LeakDetected = snapshot.LiveAllocationCount > 0
	return snapshot
}

func buildAllocatorSnapshot(ptr memcore.MarkRaw, stats *allocatorStats) MemforgeAllocatorSnapshot {
	status, _, _ := allocatorStatusInfo(stats)
	snapshot := MemforgeAllocatorSnapshot{
		Name:                stats.allocatorName,
		Address:             memforgeAddressFromMark(ptr),
		Destroyed:           stats.destroyed,
		CreatedAt:           stats.createdAt,
		Creator:             stats.creator,
		TotalAllocations:    len(stats.allAllocations),
		LiveAllocations:     len(stats.currentlyLiveAllocations),
		TotalBytes:          stats.totalBytes,
		LiveBytes:           stats.liveBytes,
		PeakLiveBytes:       stats.peakLiveBytes,
		PeakLiveAllocations: int(stats.peakLiveAllocs),
		LastAllocationAt:    stats.lastAllocAt,
		Status:              status,
	}

	if snapshot.LiveAllocations > 0 {
		snapshot.LiveAllocationDetails = buildLiveAllocationSnapshots(stats.currentlyLiveAllocations)
	}

	return snapshot
}

func buildLiveAllocationSnapshots(allocs []allocation) []MemforgeAllocationSnapshot {
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

	limit := min(3, len(live))
	details := make([]MemforgeAllocationSnapshot, 0, limit)
	for i := 0; i < limit; i++ {
		entry := live[i]
		details = append(details, MemforgeAllocationSnapshot{
			Address:   memforgeAddressFromMark(entry.ptr),
			SizeBytes: entry.sizeBytes,
			CreatedAt: entry.timestamp,
			Creator:   entry.creator,
		})
	}
	return details
}

func memforgeAddressFromMark(mark memcore.MarkRaw) uintptr {
	if !memcore.MemcoreMarkIsValid(mark) {
		return 0
	}
	return uintptr(memcore.MemcoreMarkDereference(mark))
}

// MemforgeMemoryDebug prints a focused summary of allocator usage and leaks.
func MemforgeMemoryDebug() {
	if len(debugStats) == 0 {
		fmt.Printf("\n%s╔════════════════════════════════════════════════════════════════════════════╗%s\n", colorBoldCyan, colorReset)
		fmt.Printf("%s║                        MEMFORGE MEMORY DEBUGGER                            ║%s\n", colorBoldCyan, colorReset)
		fmt.Printf("%s╚════════════════════════════════════════════════════════════════════════════╝%s\n", colorBoldCyan, colorReset)
		fmt.Printf("\n%s🧩 No allocators registered%s\n\n", colorYellow, colorReset)
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
	const boxWidth = 100
	sb.WriteString(fmt.Sprintf("\n%s╔%s╗%s\n", colorBoldCyan, strings.Repeat("═", boxWidth-2), colorReset))

	headerText := "MEMFORGE MEMORY DEBUGGER"
	padding := (boxWidth - 2 - len(headerText)) / 2
	sb.WriteString(fmt.Sprintf("%s║%s%s%s%s║%s\n",
		colorBoldCyan,
		strings.Repeat(" ", padding),
		headerText,
		strings.Repeat(" ", boxWidth-2-padding-len(headerText)),
		colorBoldCyan,
		colorReset))

	timeText := fmt.Sprintf("Time: %s", time.Now().Format("2006-01-02 15:04:05"))
	timePadding := (boxWidth - 2 - len(timeText)) / 2
	sb.WriteString(fmt.Sprintf("%s║%s%s%s%s%s║%s\n",
		colorBoldCyan,
		strings.Repeat(" ", timePadding),
		colorGray,
		timeText,
		colorReset,
		strings.Repeat(" ", boxWidth-2-timePadding-len(timeText)),
		colorReset))

	sb.WriteString(fmt.Sprintf("%s╚%s╝%s\n\n", colorBoldCyan, strings.Repeat("═", boxWidth-2), colorReset))
}

func writeGlobalSummary(sb *strings.Builder, totals globalTotals, typeCount, allocatorCount int) {
	const boxWidth = 100

	destroyedCount := 0
	for _, st := range debugStats {
		if st.destroyed {
			destroyedCount++
		}
	}
	activeCount := allocatorCount - destroyedCount

	sb.WriteString(fmt.Sprintf("\n%s┌─ Global Summary %s\n", colorBoldWhite, strings.Repeat("─", boxWidth-18)))

	// Statistics
	sb.WriteString(fmt.Sprintf("%s│%s  %sAllocator Types:%s %s%d%s\n",
		colorBoldWhite, colorReset, colorWhite, colorReset, colorCyan, typeCount, colorReset))

	sb.WriteString(fmt.Sprintf("%s│%s  %sTotal Allocators:%s %s%d%s",
		colorBoldWhite, colorReset, colorWhite, colorReset, colorBoldGreen, allocatorCount, colorReset))
	sb.WriteString(fmt.Sprintf("  %s(%sActive:%s %s%d%s  %sDestroyed:%s %s%d%s%s)%s\n",
		colorGray, colorWhite, colorReset, colorGreen, activeCount, colorReset,
		colorWhite, colorReset, colorYellow, destroyedCount, colorReset, colorGray, colorReset))

	sb.WriteString(fmt.Sprintf("%s│%s\n", colorBoldWhite, colorReset))

	// Memory statistics
	sb.WriteString(fmt.Sprintf("%s│%s  %sTotal Allocations:%s %s%d%s\n",
		colorBoldWhite, colorReset, colorWhite, colorReset, colorCyan, totals.totalAllocs, colorReset))

	liveAllocsColor := colorGreen
	if totals.totalLiveAllocs > 0 {
		liveAllocsColor = colorRed
	}
	sb.WriteString(fmt.Sprintf("%s│%s  %sLive Allocations:%s  %s%d%s\n",
		colorBoldWhite, colorReset, colorWhite, colorReset, liveAllocsColor, totals.totalLiveAllocs, colorReset))

	sb.WriteString(fmt.Sprintf("%s│%s\n", colorBoldWhite, colorReset))

	sb.WriteString(fmt.Sprintf("%s│%s  %sTotal Bytes Allocated:%s %s%s%s\n",
		colorBoldWhite, colorReset, colorWhite, colorReset, colorCyan, humanBytes(float64(totals.totalBytes)), colorReset))

	liveBytesColor := colorGreen
	if totals.totalLiveBytes > 0 {
		liveBytesColor = colorYellow
	}
	sb.WriteString(fmt.Sprintf("%s│%s  %sLive Bytes:%s           %s%s%s\n",
		colorBoldWhite, colorReset, colorWhite, colorReset, liveBytesColor, humanBytes(float64(totals.totalLiveBytes)), colorReset))

	sb.WriteString(fmt.Sprintf("%s│%s\n", colorBoldWhite, colorReset))

	// Final verdict
	if totals.totalLiveAllocs > 0 {
		sb.WriteString(fmt.Sprintf("%s│%s  %s⚠️  MEMORY LEAK DETECTED%s\n",
			colorBoldWhite, colorReset, colorBoldRed, colorReset))
		sb.WriteString(fmt.Sprintf("%s│%s  %s%d live allocation%s remain%s across all allocators\n",
			colorBoldWhite, colorReset, colorRed, totals.totalLiveAllocs,
			pluralize(totals.totalLiveAllocs), colorReset))
		sb.WriteString(fmt.Sprintf("%s│%s  %sLive memory: %s%s\n",
			colorBoldWhite, colorReset, colorRed, humanBytes(float64(totals.totalLiveBytes)), colorReset))
	} else {
		sb.WriteString(fmt.Sprintf("%s│%s  %s✅ All memory freed — no leaks detected%s\n",
			colorBoldWhite, colorReset, colorBoldGreen, colorReset))
	}

	sb.WriteString(fmt.Sprintf("%s└%s\n\n", colorBoldWhite, strings.Repeat("─", boxWidth-1)))
}

func groupAllocatorsByType(stats map[memcore.MarkRaw]*allocatorStats) map[string][]memcore.MarkRaw {
	group := make(map[string][]memcore.MarkRaw)
	for ptr, st := range stats {
		group[st.allocatorName] = append(group[st.allocatorName], ptr)
	}
	return group
}

func sortedTypeNames(group map[string][]memcore.MarkRaw) []string {
	names := make([]string, 0, len(group))
	for name := range group {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func computeTypeTotals(ptrs []memcore.MarkRaw) globalTotals {
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

func writeTypeSummary(sb *strings.Builder, name string, ptrs []memcore.MarkRaw, totals globalTotals) {
	const boxWidth = 100

	destroyedCount := 0
	for _, addr := range ptrs {
		if debugStats[addr].destroyed {
			destroyedCount++
		}
	}
	activeCount := len(ptrs) - destroyedCount

	// Type header
	remainingWidth := boxWidth - len(name) - len(fmt.Sprintf(" [%d allocators]", len(ptrs))) - 4
	if remainingWidth < 0 {
		remainingWidth = 0
	}

	sb.WriteString(fmt.Sprintf("%s┌─ %s%s%s%s %s%s\n",
		colorBoldYellow,
		colorBoldMagenta,
		name,
		colorReset,
		colorGray,
		fmt.Sprintf("[%d allocator%s]", len(ptrs), pluralize(len(ptrs))),
		strings.Repeat("─", remainingWidth)))

	// Summary statistics
	sb.WriteString(fmt.Sprintf("%s│%s  %sActive:%s %s%d%s  %sDestroyed:%s %s%d%s  ",
		colorYellow, colorReset,
		colorWhite, colorReset, colorGreen, activeCount, colorReset,
		colorWhite, colorReset, colorGray, destroyedCount, colorReset))

	leakColor := colorGreen
	leakSymbol := "✅"
	if totals.totalLiveAllocs > 0 {
		leakColor = colorRed
		leakSymbol = "🚨"
	}
	sb.WriteString(fmt.Sprintf("%sLeaks:%s %s%d%s %s%s\n",
		colorWhite, colorReset, leakColor, totals.totalLiveAllocs, colorReset, leakSymbol, colorReset))

	sb.WriteString(fmt.Sprintf("%s│%s  %sTotal:%s %s%s%s  %sLive:%s %s%s%s\n",
		colorYellow, colorReset,
		colorWhite, colorReset, colorCyan, humanBytes(float64(totals.totalBytes)), colorReset,
		colorWhite, colorReset, colorYellow, humanBytes(float64(totals.totalLiveBytes)), colorReset))

	sb.WriteString(fmt.Sprintf("%s│%s\n", colorYellow, colorReset))

	// Show allocator details if there are leaks or few allocators
	if len(ptrs) <= 5 || totals.totalLiveAllocs > 0 {
		for i, addr := range ptrs {
			writeAllocatorSummary(sb, addr, i == len(ptrs)-1)
		}
	} else {
		sb.WriteString(fmt.Sprintf("%s│%s  %s... %d more allocators (use fewer allocators or check for leaks to see details)%s\n",
			colorYellow, colorReset, colorGray, len(ptrs)-5, colorReset))
	}

	sb.WriteString(fmt.Sprintf("%s└%s%s\n\n", colorYellow, strings.Repeat("─", boxWidth-1), colorReset))
}

func writeAllocatorSummary(sb *strings.Builder, addr memcore.MarkRaw, isLast bool) {
	st := debugStats[addr]
	if len(st.allAllocations) == 0 {
		return
	}

	allocCount := len(st.allAllocations)
	liveCount := len(st.currentlyLiveAllocations)
	if allocCount == 0 && liveCount == 0 {
		return
	}

	var allocVal string
	if st.destroyed {
		allocVal = fmt.Sprintf("%s<destroyed>%s", colorGray, colorReset)
	} else {
		allocVal = fmt.Sprintf("%s0x%016x%s", colorBlue, memcore.MemcoreMarkDereference(addr), colorReset)
	}

	// Status and icon
	status, statusColor, icon := allocatorStatusInfo(st)

	sb.WriteString(fmt.Sprintf("%s│%s  %s%s%s %s  %sAllocs:%s %s%-4d%s %sLive:%s %s%-4d%s %sBytes:%s %s%-12s%s %s%s%s\n",
		colorYellow, colorReset,
		statusColor, icon, colorReset,
		allocVal,
		colorWhite, colorReset, colorCyan, allocCount, colorReset,
		colorWhite, colorReset, colorYellow, liveCount, colorReset,
		colorWhite, colorReset, colorMagenta, humanBytes(float64(st.totalBytes)), colorReset,
		statusColor, status, colorReset))

	// Creation info
	age := time.Since(st.createdAt)
	sb.WriteString(fmt.Sprintf("%s│%s     %s└─ Created:%s %s%s%s %s(%s ago)%s\n",
		colorYellow, colorReset,
		colorGray, colorReset,
		colorGray, st.createdAt.Format("15:04:05"), colorReset,
		colorGray, formatDuration(age), colorReset))

	// Creator callstack (indented)
	creatorLines := strings.Split(st.creator, "\n")
	for i, line := range creatorLines {
		if i == 0 {
			sb.WriteString(fmt.Sprintf("%s│%s        %s%s%s\n",
				colorYellow, colorReset, colorCyan, strings.TrimSpace(line), colorReset))
		} else {
			sb.WriteString(fmt.Sprintf("%s│%s        %s%s%s\n",
				colorYellow, colorReset, colorGray, strings.TrimSpace(line), colorReset))
		}
	}

	if liveCount > 0 {
		writeLiveAllocations(sb, st.currentlyLiveAllocations)
	}

	if !isLast {
		sb.WriteString(fmt.Sprintf("%s│%s\n", colorYellow, colorReset))
	}
}

func allocatorStatusInfo(st *allocatorStats) (string, string, string) {
	switch {
	case st.destroyed && len(st.currentlyLiveAllocations) > 0:
		return "DESTROYED + LEAKED", colorBoldRed, "💥"
	case st.destroyed:
		return "DESTROYED", colorGreen, "✅"
	case len(st.currentlyLiveAllocations) > 0:
		return "ACTIVE + LEAKING", colorBoldYellow, "⚠️"
	default:
		return "ACTIVE", colorGreen, "●"
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

	sb.WriteString(fmt.Sprintf("%s│%s\n", colorYellow, colorReset))
	sb.WriteString(fmt.Sprintf("%s│%s     %s🔴 Live Allocations (top %d by size):%s\n",
		colorYellow, colorReset, colorBoldRed, min(3, len(live)), colorReset))

	for i := 0; i < min(3, len(live)); i++ {
		a := live[i]
		age := time.Since(a.timestamp)

		sb.WriteString(fmt.Sprintf("%s│%s        %s•%s %sAddr:%s %s0x%016x%s  %sSize:%s %s%-10s%s %s(%s ago)%s\n",
			colorYellow, colorReset,
			colorRed, colorReset,
			colorWhite, colorReset,
			colorBlue, memcore.MemcoreMarkDereference(a.ptr), colorReset,
			colorWhite, colorReset,
			colorMagenta, humanBytes(float64(a.sizeBytes)), colorReset,
			colorGray, formatDuration(age), colorReset))

		// Callstack for allocation
		callstackLines := strings.Split(a.creator, " → ")
		for j, line := range callstackLines {
			if j == 0 {
				sb.WriteString(fmt.Sprintf("%s│%s          %s%s%s\n",
					colorYellow, colorReset, colorCyan, strings.TrimSpace(line), colorReset))
			} else {
				sb.WriteString(fmt.Sprintf("%s│%s          %s%s%s\n",
					colorYellow, colorReset, colorGray, strings.TrimSpace(line), colorReset))
			}
		}
	}

	if len(live) > 3 {
		sb.WriteString(fmt.Sprintf("%s│%s        %s... and %d more leaked allocation%s%s\n",
			colorYellow, colorReset, colorRed, len(live)-3, pluralize(len(live)-3), colorReset))
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

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	if d < time.Hour {
		return fmt.Sprintf("%.1fm", d.Minutes())
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%.1fh", d.Hours())
	}
	return fmt.Sprintf("%.1fd", d.Hours()/24)
}

func pluralize(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

/*
MemforgeMemoryTimelineSnapshotGet returns a copy of all recorded timeline events.
*/
func MemforgeMemoryTimelineSnapshotGet() MemforgeMemoryTimelineSnapshot {
	events := make([]MemforgeTimelineEvent, len(timelineEvents))
	for i, evt := range timelineEvents {
		events[i] = timelineEventToPublic(evt)
	}
	return MemforgeMemoryTimelineSnapshot{
		Available:  true,
		CapturedAt: time.Now(),
		EventCount: len(events),
		Events:     events,
	}
}

func timelineEventToPublic(evt timelineEvent) MemforgeTimelineEvent {
	out := MemforgeTimelineEvent{
		Seq:               evt.seq,
		Timestamp:         evt.timestamp,
		Kind:              evt.kind,
		AllocatorAddress:  evt.allocatorAddress,
		AllocatorName:     evt.allocatorName,
		Stack:             evt.stack,
		AllocationAddress: evt.allocationAddress,
		SizeBytes:         evt.sizeBytes,
		OriginalSeq:       evt.originalSeq,
		OriginalCreatedAt: evt.originalCreatedAt,
	}
	if len(evt.freedAllocations) > 0 {
		out.FreedAllocations = slices.Clone(evt.freedAllocations)
	}
	return out
}

/*
MemforgeMemoryTimelineDebug prints an arena-focused analysis of the recorded timeline.
*/
func MemforgeMemoryTimelineDebug(params MemforgeMemoryTimelineRenderParams) {
	snapshot := MemforgeMemoryTimelineSnapshotGet()
	analysis := MemforgeMemoryTimelineAnalyze(snapshot, MemforgeStackFilter{})
	memforgeMemoryTimelineAnalysisDebugPrint(analysis, params)
}

func memforgeMemoryTimelineAnalysisDebugPrint(analysis MemforgeMemoryTimelineAnalysis, params MemforgeMemoryTimelineRenderParams) {
	fmt.Printf("\n%s=== MEMFORGE MEMORY ANALYSIS ===%s\n", colorBoldCyan, colorReset)
	if !analysis.Available {
		fmt.Printf("%s(unavailable)%s\n\n", colorYellow, colorReset)
		return
	}
	if analysis.TotalEvents == 0 {
		fmt.Printf("%s(no events recorded)%s\n\n", colorYellow, colorReset)
		return
	}

	fmt.Printf("%sEvents:%s %d  %sLive:%s %d alloc / %s  %sLeaking arenas:%s %d\n\n",
		colorWhite, colorReset, analysis.TotalEvents,
		colorWhite, colorReset, analysis.TotalLiveAllocations, humanBytes(float64(analysis.TotalLiveBytes)),
		colorWhite, colorReset, analysis.LeakingAllocators)

	memforgeArenaSummaryDebugPrint(analysis.Arenas)

	if analysis.TotalLiveAllocations > 0 {
		fmt.Printf("%s\nLeak Groups (by filtered stack):%s\n", colorBoldYellow, colorReset)
		for _, group := range analysis.LeakGroups {
			memforgeLeakGroupDebugPrint(group)
		}
	}

	if params.MaxEvents != 0 {
		fmt.Printf("%s\nGranular Timeline:%s\n", colorBoldYellow, colorReset)
		limit := len(analysis.Events)
		if params.MaxEvents > 0 && params.MaxEvents < limit {
			limit = params.MaxEvents
		}
		for i := 0; i < limit; i++ {
			writeTimelineEventLine(&analysis.Events[i], params.ExpandRegionalFreed)
		}
		if limit < len(analysis.Events) {
			fmt.Printf("%s... %d more events not shown%s\n", colorGray, len(analysis.Events)-limit, colorReset)
		}
	}
	fmt.Println()
}

func memforgeArenaSummaryDebugPrint(arenas []MemforgeArenaSummary) {
	fmt.Printf("%s\nArena Summary:%s\n", colorBoldYellow, colorReset)
	for _, arena := range arenas {
		leakColor := colorGreen
		leakLabel := "OK"
		if arena.Leaking {
			leakColor = colorBoldRed
			leakLabel = "LEAKING"
		}
		fmt.Printf("  %s%s%s %s (%s)\n", leakColor, leakLabel, colorReset, arena.Name, formatTimelineAddress(arena.Address))
		fmt.Printf("    age=%s live=%d/%s peak=%d/%s ever=%d/%s\n",
			formatDuration(arena.AgeAtCapture),
			arena.LiveAllocations, humanBytes(float64(arena.LiveBytes)),
			arena.PeakLiveAllocations, humanBytes(float64(arena.PeakLiveBytes)),
			arena.EverAllocations, humanBytes(float64(arena.EverBytes)))
		if len(arena.FilteredCreatorStack) > 0 {
			fmt.Printf("    created by: %s%s%s\n", colorCyan, strings.Join(arena.FilteredCreatorStack, " → "), colorReset)
		}
	}
}

func memforgeLeakGroupDebugPrint(group MemforgeAllocationStackGroup) {
	fmt.Printf("  %s%d%s alloc %s%s%s via %s\n",
		colorYellow, group.AllocationCount, colorReset,
		colorMagenta, humanBytes(float64(group.TotalBytes)), colorReset,
		group.SampleAllocator)
	if len(group.FilteredStack) > 0 {
		fmt.Printf("    %s%s%s\n", colorGray, strings.Join(group.FilteredStack, " → "), colorReset)
	}
}

func writeTimelineEventLine(evt *MemforgeTimelineEvent, expandRegional bool) {
	fmt.Printf("%s#%-6d%s %s%s%s %s%s%s\n",
		colorGray, evt.Seq, colorReset,
		colorCyan, evt.Timestamp.Format("15:04:05.000"), colorReset,
		colorYellow, evt.Kind, colorReset)
	fmt.Printf("  %sAllocator:%s %s (%s)\n",
		colorWhite, colorReset, evt.AllocatorName, formatTimelineAddress(evt.AllocatorAddress))

	switch evt.Kind {
	case MemforgeTimelineEventAllocation, MemforgeTimelineEventFreeManual:
		fmt.Printf("  %sAddress:%s %s  %sSize:%s %s\n",
			colorWhite, colorReset, formatTimelineAddress(evt.AllocationAddress),
			colorWhite, colorReset, humanBytes(float64(evt.SizeBytes)))
		if evt.Kind == MemforgeTimelineEventFreeManual {
			fmt.Printf("  %sOrig:#%d%s at %s\n",
				colorGray, evt.OriginalSeq, colorReset, evt.OriginalCreatedAt.Format("15:04:05.000"))
		}
	case MemforgeTimelineEventFreeRegionalReset, MemforgeTimelineEventFreeRegionalDestroy:
		fmt.Printf("  %sFreed:%s %d allocation(s)\n", colorWhite, colorReset, len(evt.FreedAllocations))
		if expandRegional {
			for _, freed := range evt.FreedAllocations {
				fmt.Printf("    - %s %s (orig #%d at %s)\n",
					formatTimelineAddress(freed.AllocationAddress),
					humanBytes(float64(freed.SizeBytes)),
					freed.OriginalSeq,
					freed.OriginalCreatedAt.Format("15:04:05.000"))
			}
		}
	}

	if strings.TrimSpace(evt.Stack) != "" {
		fmt.Printf("  %s%s%s\n", colorGray, evt.Stack, colorReset)
	}
	fmt.Println()
}

func formatTimelineAddress(address uintptr) string {
	if address == 0 {
		return "<invalid>"
	}
	return fmt.Sprintf("0x%016x", address)
}
