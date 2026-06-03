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
	seq                       uint64
	timestamp                 time.Time
	kind                      MemforgeTimelineEventKind
	allocatorAddress          uintptr
	allocatorName             string
	stack                     string
	allocationAddress         uintptr
	allocationRegionID        uint32
	allocationOffset          uint64
	rootAllocationOffset      uint64
	sizeBytes                 uint64
	originalSeq               uint64
	originalCreatedAt         time.Time
	freedAllocations          []MemforgeTimelineFreedAllocation
	arenaDataCapBytes         uint64
	arenaTotalBytes           uint64
	previousArenaDataCapBytes uint64
	opaqueBacking             bool
	tag                       string
}

type allocatorStats struct {
	allocatorName                                        string
	tag                                                  string
	creator                                              string
	opaqueBacking                                        bool
	destroyed                                            bool
	createdAt                                            time.Time
	destroyedAt                                          time.Time
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
// It skips internal frames and formats each frame as "Func (file.go:line)" joined by " → ".
func memforgeCaptureCallStack(skip int) string {
	const maxDepth = 16
	var pcs [maxDepth]uintptr
	n := runtime.Callers(skip+2, pcs[:]) // skip runtime + helper itself
	frames := runtime.CallersFrames(pcs[:n])

	stack := make([]string, 0, n)
	for {
		f, more := frames.Next()
		if !isInternalFrame(f.Function) {
			stack = append(stack, memforgeFormatStackFrame(f))
		}
		if !more {
			break
		}
	}
	slices.Reverse(stack)
	return strings.Join(stack, " → ")
}

func memforgeFormatStackFrame(frame runtime.Frame) string {
	fn := filepath.Base(frame.Function)
	if frame.File == "" || frame.Line <= 0 {
		return fn
	}
	return fmt.Sprintf("%s (%s:%d)", fn, filepath.Base(frame.File), frame.Line)
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
	if memcore.MemcoreMarkRegionIsOpaque(allocationPtr) {
		return 0
	}
	return uintptr(memcore.MemcoreMarkDereferenceUnsafe(allocationPtr))
}

func timelineAllocationRegionID(allocationPtr memcore.MarkRaw) uint32 {
	return memcore.MemcoreMarkRegionIDGet(allocationPtr)
}

func timelineAllocationOffset(allocationPtr memcore.MarkRaw) uint64 {
	return uint64(memcore.MemcoreMarkOffsetGet(allocationPtr))
}

func timelineAllocationRootOffset(allocationPtr memcore.MarkRaw) uint64 {
	if !memcore.MemcoreMarkIsValid(allocationPtr) {
		return 0
	}

	resolved := memcore.MemcoreMarkResolveToRoot(allocationPtr)
	return uint64(memcore.MemcoreMarkOffsetGet(resolved))
}

func memforgeAddressFromMark(mark memcore.MarkRaw) uintptr {
	if !memcore.MemcoreMarkIsValid(mark) {
		return 0
	}
	if memcore.MemcoreMarkRegionIsOpaque(mark) {
		return 0
	}
	return uintptr(memcore.MemcoreMarkDereference(mark))
}

func timelineFreedAllocationsFromLive(live []allocation) []MemforgeTimelineFreedAllocation {
	if len(live) == 0 {
		return nil
	}
	freed := make([]MemforgeTimelineFreedAllocation, len(live))
	for i, entry := range live {
		freed[i] = MemforgeTimelineFreedAllocation{
			AllocationAddress: timelineAllocationAddress(entry.ptr),
			SizeBytes:         entry.sizeBytes,
			OriginalSeq:       entry.seq,
			OriginalCreatedAt: entry.timestamp,
		}
	}
	return freed
}

// Register a new allocator and record its creation site.
func memforgeAllocatorRegister(
	allocatorPtr memcore.MarkRaw,
	name, tag string,
	dataRegionID uint32,
	arenaDataCapBytes, arenaTotalBytes uint64,
) {
	opaqueBacking := memcore.MemcoreRegionIsOpaque(dataRegionID)
	debugStats[allocatorPtr] = &allocatorStats{
		allocatorName: name,
		tag:           tag,
		opaqueBacking: opaqueBacking,
		createdAt:     time.Now(),
		creator:       memforgeCaptureCallStack(2),
	}
	appendTimelineEvent(timelineEvent{
		kind:              MemforgeTimelineEventAllocatorRegister,
		allocatorAddress:  timelineAllocatorAddress(allocatorPtr),
		allocatorName:     name,
		tag:               tag,
		stack:             memforgeCaptureCallStack(2),
		opaqueBacking:     opaqueBacking,
		arenaDataCapBytes: arenaDataCapBytes,
		arenaTotalBytes:   arenaTotalBytes,
	})
}

func memforgeAllocatorGrow(allocatorPtr memcore.MarkRaw, previousDataCapBytes, newDataCapBytes, newTotalBytes uint64) {
	allocatorName := ""
	if stats := debugStats[allocatorPtr]; stats != nil {
		allocatorName = stats.allocatorName
	}
	appendTimelineEvent(timelineEvent{
		kind:                      MemforgeTimelineEventAllocatorGrow,
		allocatorAddress:          timelineAllocatorAddress(allocatorPtr),
		allocatorName:             allocatorName,
		stack:                     memforgeCaptureCallStack(2),
		arenaDataCapBytes:         newDataCapBytes,
		arenaTotalBytes:           newTotalBytes,
		previousArenaDataCapBytes: previousDataCapBytes,
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
		kind:                 MemforgeTimelineEventAllocation,
		allocatorAddress:     timelineAllocatorAddress(allocatorPtr),
		allocatorName:        stats.allocatorName,
		stack:                creator,
		allocationAddress:    timelineAllocationAddress(allocationPtr),
		allocationRegionID:   timelineAllocationRegionID(allocationPtr),
		allocationOffset:     timelineAllocationOffset(allocationPtr),
		rootAllocationOffset: timelineAllocationRootOffset(allocationPtr),
		sizeBytes:            sizeBytes,
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
		kind:                 MemforgeTimelineEventFreeManual,
		allocatorAddress:     timelineAllocatorAddress(allocatorPtr),
		allocatorName:        stats.allocatorName,
		stack:                memforgeCaptureCallStack(2),
		allocationAddress:    timelineAllocationAddress(allocationPtr),
		allocationRegionID:   timelineAllocationRegionID(allocationPtr),
		allocationOffset:     timelineAllocationOffset(allocationPtr),
		rootAllocationOffset: timelineAllocationRootOffset(allocationPtr),
		sizeBytes:            removed.sizeBytes,
		originalSeq:          removed.seq,
		originalCreatedAt:    removed.timestamp,
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
	stats.destroyedAt = time.Now()
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
		allocators = append(allocators, buildAllocatorSnapshot(ptr, stats, snapshot.CapturedAt))
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

func buildAllocatorSnapshot(ptr memcore.MarkRaw, stats *allocatorStats, capturedAt time.Time) MemforgeAllocatorSnapshot {
	status, _, _ := allocatorStatusInfo(stats)
	snapshot := MemforgeAllocatorSnapshot{
		Name:                stats.allocatorName,
		Tag:                 stats.tag,
		Address:             memforgeAddressFromMark(ptr),
		OpaqueBacking:       stats.opaqueBacking,
		Destroyed:           stats.destroyed,
		CreatedAt:           stats.createdAt,
		DestroyedAt:         stats.destroyedAt,
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

	snapshot.TimeAlive = MemforgeArenaTimeAlive(MemforgeArenaSummary{
		CreatedAt:   snapshot.CreatedAt,
		DestroyedAt: snapshot.DestroyedAt,
		Destroyed:   snapshot.Destroyed,
	}, capturedAt)

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

/*
MemforgeMemoryDebug prints allocator usage and leaks using structured timeline analysis.
*/
func MemforgeMemoryDebug() {
	MemforgeMemoryDebugWithParams(MemforgeMemoryDebugParams{})
}

/*
MemforgeMemoryDebugWithParams prints timeline analysis with optional stack filtering and granular events.
*/
func MemforgeMemoryDebugWithParams(params MemforgeMemoryDebugParams) {
	snapshot := MemforgeMemoryTimelineSnapshotGet()
	filter := params.StackFilter
	if filter.MaxDepth == 0 && len(filter.IgnoreContains) == 0 && len(filter.IgnorePrefixes) == 0 {
		filter = MemforgeDebuggerDefaultStackFilter()
	}
	analysis := MemforgeMemoryTimelineAnalyze(snapshot, filter)
	report := MemforgeMemoryProfileAnalyze(analysis, filter, params.SizingVerdictFilter)
	memforgeMemorySystemReportDebugPrint(report, analysis, params.Timeline, params.DomainGroups)
}

type memforgeMemoryAnalysisPrintOptions struct {
	title              string
	showEventKindLines bool
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
		Seq:                       evt.seq,
		Timestamp:                 evt.timestamp,
		Kind:                      evt.kind,
		AllocatorAddress:          evt.allocatorAddress,
		AllocatorName:             evt.allocatorName,
		Stack:                     evt.stack,
		AllocationAddress:         evt.allocationAddress,
		AllocationRegionID:        evt.allocationRegionID,
		AllocationOffset:          evt.allocationOffset,
		RootAllocationOffset:      evt.rootAllocationOffset,
		SizeBytes:                 evt.sizeBytes,
		OriginalSeq:               evt.originalSeq,
		OriginalCreatedAt:         evt.originalCreatedAt,
		ArenaDataCapBytes:         evt.arenaDataCapBytes,
		ArenaTotalBytes:           evt.arenaTotalBytes,
		PreviousArenaDataCapBytes: evt.previousArenaDataCapBytes,
		OpaqueBacking:             evt.opaqueBacking,
		Tag:                       evt.tag,
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
	filter := MemforgeDebuggerDefaultStackFilter()
	analysis := MemforgeMemoryTimelineAnalyze(snapshot, filter)
	report := MemforgeMemoryProfileAnalyze(analysis, filter, MemforgeSizingVerdictFilter{})
	memforgeMemorySystemReportDebugPrint(report, analysis, params, nil)
}

func memforgeMemorySystemReportDebugPrint(
	report MemforgeMemorySystemReport,
	analysis MemforgeMemoryTimelineAnalysis,
	params MemforgeMemoryTimelineRenderParams,
	domainGroups map[string][]string,
) {
	if report.LeakDetected {
		memforgeMemoryAnalysisDebugPrint(analysis, params, memforgeMemoryAnalysisPrintOptions{
			title:              "MEMFORGE MEMORY DEBUGGER (Leaks Detected)",
			showEventKindLines: true,
		})
		reportTitle := "ALLOCATION PROFILE (Aggregated)"
		memforgeMemoryProfileReportDebugPrint(report, reportTitle, false, domainGroups)
		return
	}

	memforgeMemoryProfileReportDebugPrint(report, "MEMFORGE MEMORY SYSTEM REPORT (No Leaks)", true, domainGroups)
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

func memforgeMemoryProfileReportDebugPrint(
	report MemforgeMemorySystemReport,
	title string,
	includeVerdict bool,
	domainGroups map[string][]string,
) {
	fmt.Printf("\n%s=== %s ===%s\n", colorBoldCyan, title, colorReset)
	if !report.Available {
		fmt.Printf("%s(unavailable — rebuild with -tags memforge_debug)%s\n\n", colorYellow, colorReset)
		return
	}
	if report.TotalEvents == 0 && report.TotalArenas == 0 {
		fmt.Printf("%s🧩 No allocators registered%s\n\n", colorYellow, colorReset)
		return
	}

	totalCap := report.TotalMappableCapBytes + report.TotalOpaqueCapBytes
	highWater := report.GlobalCapacityHighWaterBytes
	if highWater == 0 {
		highWater = totalCap
	}

	fmt.Printf("%sTelemetry  :%s %d Events | %d Total Arenas (%s%d Active%s)\n",
		colorWhite, colorReset,
		report.TotalEvents, report.TotalArenas,
		colorGreen, report.ActiveArenas, colorReset)
	fmt.Printf("%sTotal Cap  :%s %s %s[Mappable: %s | Opaque: %s]%s\n",
		colorWhite, colorReset, humanBytes(float64(totalCap)), colorGray,
		humanBytes(float64(report.TotalMappableCapBytes)),
		humanBytes(float64(report.TotalOpaqueCapBytes)),
		colorReset)
	fmt.Printf("%sPeak Load  :%s %d Allocations | %s Global Peak\n",
		colorWhite, colorReset,
		report.GlobalPeakLiveAllocations,
		humanBytes(float64(report.GlobalPeakLiveBytes)))
	fmt.Printf("%sHigh-Water :%s Global Arena Capacity Peak: %s\n",
		colorWhite, colorReset, humanBytes(float64(highWater)))

	if len(report.Buckets) > 0 {
		memforgeProfileBucketsDebugPrint(report.Buckets, domainGroups)
	}

	if includeVerdict && len(report.SizingVerdicts) > 0 {
		fmt.Printf("\n%sSizing Efficiency Verdict:%s\n", colorBoldYellow, colorReset)
		for _, verdict := range report.SizingVerdicts {
			color := colorYellow
			if verdict.Kind == MemforgeSizingVerdictOverutilized {
				color = colorBoldRed
			}
			fmt.Printf("  %s%s%s\n", color, verdict.Message, colorReset)
		}
		if len(report.SizingVerdicts) > 0 {
			fmt.Printf("  %sSTRATEGY:%s Review preset slab sizes for buckets flagged above.\n", colorBoldCyan, colorReset)
		}
	}
}

func memforgeProfileBucketsDebugPrint(buckets []MemforgeArenaProfileBucket, domainGroups map[string][]string) {
	grouped, ungrouped := MemforgeDomainGroupBuckets(buckets, domainGroups)
	if grouped == nil {
		fmt.Printf("\n%sCategorized Allocation Profile (Aggregated by Call Site):%s\n", colorBoldYellow, colorReset)
		for _, bucket := range buckets {
			memforgeProfileBucketDebugPrint(bucket)
		}
		return
	}

	fmt.Printf("\n%sAllocation Profile by Domain:%s\n", colorBoldYellow, colorReset)
	for _, domain := range memforgeDomainGroupsSortedNames(domainGroups) {
		domainBuckets := grouped[domain]
		if len(domainBuckets) == 0 {
			continue
		}
		fmt.Printf("\n%s  %s%s\n", colorBoldMagenta, domain, colorReset)
		memforgeDomainProfileSummaryDebugPrint(MemforgeDomainProfileSummarize(domainBuckets))
		for _, bucket := range domainBuckets {
			memforgeProfileBucketDebugPrint(bucket)
		}
	}
	if len(ungrouped) > 0 {
		fmt.Printf("\n%s  Ungrouped%s\n", colorBoldMagenta, colorReset)
		memforgeDomainProfileSummaryDebugPrint(MemforgeDomainProfileSummarize(ungrouped))
		for _, bucket := range ungrouped {
			memforgeProfileBucketDebugPrint(bucket)
		}
	}
}

func memforgeDomainProfileSummaryDebugPrint(summary MemforgeDomainProfileSummary) {
	fmt.Printf("    %sSummary :%s %d types | %d arenas (%d active, %d destroyed)\n",
		colorWhite, colorReset,
		summary.BucketCount,
		summary.InstanceCount,
		summary.ActiveCount,
		summary.DestroyedCount)
	line := fmt.Sprintf("    %sSizing  :%s Configured %s",
		colorWhite, colorReset,
		humanBytes(float64(summary.ConfiguredCapBytes)))
	if summary.MappableCapBytes > 0 || summary.OpaqueCapBytes > 0 {
		line += fmt.Sprintf(" %s[Mappable: %s | Opaque: %s]%s",
			colorGray,
			humanBytes(float64(summary.MappableCapBytes)),
			humanBytes(float64(summary.OpaqueCapBytes)),
			colorReset)
	}
	line += fmt.Sprintf(" | Max peak %s | Ever %s\n",
		humanBytes(float64(summary.MaxPeakBytes)),
		humanBytes(float64(summary.TotalEverBytes)))
	fmt.Print(line)
}

func memforgeProfileBucketDebugPrint(bucket MemforgeArenaProfileBucket) {
	backingLabel := "Mappable"
	if bucket.OpaqueBacking {
		backingLabel = "Opaque"
	}

	fmt.Printf("\n%s[%s]%s %s (x%d Arena", colorGray, backingLabel, colorReset, bucket.DisplayLabel, bucket.InstanceCount)
	if bucket.InstanceCount != 1 {
		fmt.Print("s")
	}
	fmt.Println(")")
	fmt.Printf("  %sInstances:%s %d spawned, %d destroyed, %d active",
		colorWhite, colorReset,
		bucket.InstanceCount, bucket.DestroyedCount, bucket.ActiveCount)
	if bucket.MaxConcurrentActive > 1 {
		fmt.Printf("  %s(concurrent high-water: %d)%s", colorGray, bucket.MaxConcurrentActive, colorReset)
	}
	fmt.Println()
	fmt.Printf("  %sSizing   :%s Configured: %s | Max Peak Util: %s (%.1f%%) | Ever Alloc: %s\n",
		colorWhite, colorReset,
		humanBytes(float64(bucket.ConfiguredCapBytes)),
		humanBytes(float64(bucket.MaxPeakBytes)),
		bucket.MaxPeakUtilPercent,
		humanBytes(float64(bucket.TotalEverBytes)))
	if bucket.SiteLine != "" {
		fmt.Printf("  %sSite     :%s %s%s%s\n", colorWhite, colorReset, colorCyan, bucket.SiteLine, colorReset)
	}
}

func memforgeMemoryAnalysisDebugPrint(analysis MemforgeMemoryTimelineAnalysis, params MemforgeMemoryTimelineRenderParams, opts memforgeMemoryAnalysisPrintOptions) {
	title := opts.title
	if title == "" {
		title = "MEMFORGE MEMORY ANALYSIS"
	}

	fmt.Printf("\n%s=== %s ===%s\n", colorBoldCyan, title, colorReset)
	if !analysis.Available {
		fmt.Printf("%s(unavailable — rebuild with -tags memforge_debug)%s\n\n", colorYellow, colorReset)
		return
	}
	if analysis.TotalEvents == 0 && analysis.TotalAllocators == 0 {
		fmt.Printf("%s🧩 No allocators registered%s\n\n", colorYellow, colorReset)
		return
	}

	memforgeMemoryAnalysisSummaryDebugPrint(analysis, opts.showEventKindLines)
	memforgeLeakingArenaSummaryDebugPrint(analysis.Arenas, analysis.CapturedAt)

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

func memforgeMemoryAnalysisSummaryDebugPrint(analysis MemforgeMemoryTimelineAnalysis, showEventKinds bool) {
	fmt.Printf("%sEvents    :%s %d\n", colorWhite, colorReset, analysis.TotalEvents)
	fmt.Printf("%sAllocators:%s %d Total (%s%d Active%s, %s%d Destroyed%s)\n",
		colorWhite, colorReset, analysis.TotalAllocators,
		colorGreen, analysis.ActiveAllocators, colorReset,
		colorYellow, analysis.DestroyedAllocators, colorReset)
	fmt.Printf("%sLive      :%s %d Allocations, %s, %d Leaking Arenas\n",
		colorWhite, colorReset,
		analysis.TotalLiveAllocations,
		humanBytes(float64(analysis.TotalLiveBytes)),
		analysis.LeakingAllocators)
	fmt.Printf("%sPeak      :%s %d Allocations, %s\n",
		colorWhite, colorReset,
		analysis.TotalPeakLiveAllocations,
		humanBytes(float64(analysis.TotalPeakLiveBytes)))
	fmt.Printf("%sEver      :%s %d Allocations, %s\n",
		colorWhite, colorReset,
		analysis.TotalEverAllocations,
		humanBytes(float64(analysis.TotalEverBytes)))
	if analysis.TotalArenaDataCapBytes > 0 {
		fmt.Printf("%sArena cap :%s %s", colorWhite, colorReset, humanBytes(float64(analysis.TotalArenaDataCapBytes)))
		mappableMmap := memforgeAnalysisMappableMmapBytes(analysis.Arenas)
		if mappableMmap > 0 {
			fmt.Printf("  %sMmap:%s %s", colorWhite, colorReset, humanBytes(float64(mappableMmap)))
		}
		fmt.Println()
		fmt.Printf("%sUtil      :%s live %.1f%%  peak %.1f%%  ever %.1f%%\n",
			colorWhite, colorReset,
			analysis.LiveUtilizationPercent,
			analysis.PeakUtilizationPercent,
			analysis.EverUtilizationPercent)
	}

	if showEventKinds && analysis.TotalEvents > 0 {
		memforgeMemoryAnalysisEventKindsDebugPrint(analysis.Events)
	}

	fmt.Printf("%sVerdict   :%s ", colorWhite, colorReset)
	if analysis.LeakDetected {
		fmt.Printf("%sMEMORY LEAK DETECTED%s\n", colorBoldRed, colorReset)
	} else {
		fmt.Printf("%sNo leaks detected%s\n", colorBoldGreen, colorReset)
	}
	fmt.Println()
}

func memforgeAnalysisMappableMmapBytes(arenas []MemforgeArenaSummary) uint64 {
	var total uint64
	for _, arena := range arenas {
		if !arena.OpaqueBacking {
			total += arena.CurrentArenaTotalBytes
		}
	}
	return total
}

func memforgeMemoryAnalysisEventKindsDebugPrint(events []MemforgeTimelineEvent) {
	kindCounts := make(map[string]int)
	var firstTS, lastTS time.Time
	for i, evt := range events {
		kindCounts[string(evt.Kind)]++
		if i == 0 || evt.Timestamp.Before(firstTS) {
			firstTS = evt.Timestamp
		}
		if i == 0 || evt.Timestamp.After(lastTS) {
			lastTS = evt.Timestamp
		}
	}

	if !firstTS.IsZero() && !lastTS.IsZero() {
		fmt.Printf("%sSpan      :%s %s to %s (%s)\n",
			colorGray, colorReset,
			firstTS.Format("15:04:05.000"),
			lastTS.Format("15:04:05.000"),
			lastTS.Sub(firstTS).Round(time.Millisecond))
	}

	kinds := make([]string, 0, len(kindCounts))
	for kind := range kindCounts {
		kinds = append(kinds, kind)
	}
	slices.Sort(kinds)
	for _, kind := range kinds {
		fmt.Printf("%s  %-28s %s%d\n", colorGray, kind+":", colorReset, kindCounts[kind])
	}
}

func memforgeLeakingArenaSummaryDebugPrint(arenas []MemforgeArenaSummary, capturedAt time.Time) {
	leaking := make([]MemforgeArenaSummary, 0)
	for _, arena := range arenas {
		if arena.Leaking {
			leaking = append(leaking, arena)
		}
	}
	if len(leaking) == 0 {
		return
	}

	fmt.Printf("%s\nLeaking Arenas:%s\n", colorBoldRed, colorReset)
	for _, arena := range leaking {
		memforgeArenaSummaryEntryDebugPrint(arena, capturedAt)
	}
}

func memforgeArenaSummaryDebugPrint(arenas []MemforgeArenaSummary, capturedAt time.Time) {
	mappable := make([]MemforgeArenaSummary, 0, len(arenas))
	opaque := make([]MemforgeArenaSummary, 0, len(arenas))
	for _, arena := range arenas {
		if arena.OpaqueBacking {
			opaque = append(opaque, arena)
		} else {
			mappable = append(mappable, arena)
		}
	}

	memforgeArenaSummarySectionDebugPrint("Arena Summary (Mappable backing):", mappable, capturedAt)
	memforgeArenaSummarySectionDebugPrint("Arena Summary (Opaque backing):", opaque, capturedAt)
}

func memforgeArenaSummarySectionDebugPrint(title string, arenas []MemforgeArenaSummary, capturedAt time.Time) {
	if len(arenas) == 0 {
		return
	}

	fmt.Printf("%s\n%s:%s\n", colorBoldYellow, title, colorReset)
	for _, arena := range arenas {
		memforgeArenaSummaryEntryDebugPrint(arena, capturedAt)
	}
}

func memforgeArenaSummaryEntryDebugPrint(arena MemforgeArenaSummary, capturedAt time.Time) {
	leakColor := colorGreen
	leakLabel := "OK"
	if arena.Leaking {
		leakColor = colorBoldRed
		leakLabel = "LEAKING"
	}
	statusSuffix := ""
	if arena.Destroyed {
		statusSuffix = " destroyed"
	}
	fmt.Printf("  %s%s%s %s (%s)%s\n", leakColor, leakLabel, colorReset, arena.Name, formatTimelineAddress(arena.Address), statusSuffix)
	fmt.Printf("    created=%s\n", memforgeFormatDebugTimestamp(arena.CreatedAt, capturedAt))
	if arena.Destroyed {
		fmt.Printf("    destroyed=%s\n", memforgeFormatDebugTimestamp(arena.DestroyedAt, capturedAt))
	}
	fmt.Printf("    alive=%s\n", formatDuration(arena.TimeAlive))
	fmt.Printf("    last alloc=%s\n", memforgeFormatDebugTimestamp(arena.LastAllocationAt, capturedAt))
	fmt.Printf("    live=%d/%s peak=%d/%s ever=%d/%s\n",
		arena.LiveAllocations, humanBytes(float64(arena.LiveBytes)),
		arena.PeakLiveAllocations, humanBytes(float64(arena.PeakLiveBytes)),
		arena.EverAllocations, humanBytes(float64(arena.EverBytes)))
	memforgeArenaCapacityDebugPrint(arena)
	memforgeArenaUtilizationDebugPrint(arena)
	if len(arena.FilteredCreatorStack) > 0 {
		fmt.Printf("    created by: %s%s%s\n", colorCyan, strings.Join(arena.FilteredCreatorStack, " → "), colorReset)
	}
}

func memforgeFormatDebugTimestamp(at time.Time, capturedAt time.Time) string {
	if at.IsZero() {
		return "never"
	}
	label := at.Format("15:04:05.000")
	if capturedAt.IsZero() || capturedAt.Before(at) {
		return label
	}
	return fmt.Sprintf("%s (%s ago)", label, formatDuration(capturedAt.Sub(at)))
}

func memforgeArenaUtilizationDebugPrint(arena MemforgeArenaSummary) {
	if arena.CurrentArenaDataCapBytes == 0 {
		return
	}
	fmt.Printf("    %sUtil:%s live %.1f%%  peak %.1f%%  ever %.1f%%\n",
		colorWhite, colorReset,
		arena.LiveUtilizationPercent,
		arena.PeakUtilizationPercent,
		arena.EverUtilizationPercent)
}

func memforgeArenaCapacityDebugPrint(arena MemforgeArenaSummary) {
	if arena.CurrentArenaDataCapBytes == 0 && len(arena.CapacitySegments) == 0 {
		return
	}

	peakCap := arena.PeakArenaDataCapBytes
	if peakCap == 0 {
		peakCap = arena.CurrentArenaDataCapBytes
	}

	line := fmt.Sprintf("    %sArena:%s %s", colorWhite, colorReset, humanBytes(float64(arena.CurrentArenaDataCapBytes)))
	if !arena.OpaqueBacking && arena.CurrentArenaTotalBytes > 0 {
		line += fmt.Sprintf("  %sMmap:%s %s", colorWhite, colorReset, humanBytes(float64(arena.CurrentArenaTotalBytes)))
	}
	if peakCap > arena.CurrentArenaDataCapBytes {
		line += fmt.Sprintf("  %sPeak arena:%s %s", colorWhite, colorReset, humanBytes(float64(peakCap)))
	}
	fmt.Println(line)

	if len(arena.CapacitySegments) <= 1 {
		return
	}

	fmt.Printf("    %sCapacity segments:%s\n", colorGray, colorReset)
	for _, segment := range arena.CapacitySegments {
		endLabel := "capture"
		if !segment.EndedAt.IsZero() {
			endLabel = segment.EndedAt.Format("15:04:05.000")
		}
		growthNote := ""
		if segment.GrownFromBytes > 0 && segment.DataCapBytes > segment.GrownFromBytes {
			growthNote = fmt.Sprintf("  %s(+%s)%s",
				colorGray,
				humanBytes(float64(segment.DataCapBytes-segment.GrownFromBytes)),
				colorReset)
		}
		fmt.Printf("      %s – %s  %s%s\n",
			segment.StartedAt.Format("15:04:05.000"),
			endLabel,
			humanBytes(float64(segment.DataCapBytes)),
			growthNote)
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
	case MemforgeTimelineEventAllocatorRegister, MemforgeTimelineEventAllocatorGrow:
		if evt.Kind == MemforgeTimelineEventAllocatorRegister {
			backingLabel := "mappable"
			if evt.OpaqueBacking {
				backingLabel = "opaque"
			}
			fmt.Printf("  %sBacking:%s %s\n", colorWhite, colorReset, backingLabel)
		}
		if evt.ArenaDataCapBytes > 0 {
			line := fmt.Sprintf("  %sArena data cap:%s %s", colorWhite, colorReset, humanBytes(float64(evt.ArenaDataCapBytes)))
			if evt.ArenaTotalBytes > 0 {
				line += fmt.Sprintf("  %sMmap total:%s %s", colorWhite, colorReset, humanBytes(float64(evt.ArenaTotalBytes)))
			}
			if evt.Kind == MemforgeTimelineEventAllocatorGrow && evt.PreviousArenaDataCapBytes > 0 {
				line += fmt.Sprintf("  %s(from %s)%s", colorGray, humanBytes(float64(evt.PreviousArenaDataCapBytes)), colorReset)
			}
			fmt.Println(line)
		}
	case MemforgeTimelineEventAllocation, MemforgeTimelineEventFreeManual:
		if evt.AllocationAddress != 0 {
			fmt.Printf("  %sAddress:%s %s  %sSize:%s %s\n",
				colorWhite, colorReset, formatTimelineAddress(evt.AllocationAddress),
				colorWhite, colorReset, humanBytes(float64(evt.SizeBytes)))
		} else if evt.AllocationRegionID != 0 {
			fmt.Printf("  %sOpaque:%s region=%d offset=0x%x  %sSize:%s %s\n",
				colorWhite, colorReset, evt.AllocationRegionID, evt.AllocationOffset,
				colorWhite, colorReset, humanBytes(float64(evt.SizeBytes)))
		}
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
