package memforge

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

const (
	memforgeSizingUnderutilizedThresholdPercent = 1.0
	memforgeSizingOverutilizedThresholdPercent  = 95.0
)

/*
MemforgeSizingVerdictKind classifies arena sizing efficiency heuristics.
*/
type MemforgeSizingVerdictKind string

const (
	MemforgeSizingVerdictUnderutilized MemforgeSizingVerdictKind = "underutilized"
	MemforgeSizingVerdictOverutilized  MemforgeSizingVerdictKind = "overutilized"
)

/*
MemforgeSizingVerdict is one actionable sizing note derived from peak utilization.
*/
type MemforgeSizingVerdict struct {
	Kind               MemforgeSizingVerdictKind
	BucketLabel        string
	ConfiguredCapBytes uint64
	PeakBytes          uint64
	PeakUtilPercent    float64
	Message            string
}

/*
MemforgeArenaProfileBucket aggregates allocator instances that share a functional tag or creation site.
*/
type MemforgeArenaProfileBucket struct {
	OpaqueBacking       bool
	AllocatorName       string
	Tag                 string
	DisplayLabel        string
	SiteLine            string
	InstanceCount       int
	DestroyedCount      int
	ActiveCount         int
	ConfiguredCapBytes  uint64
	MaxPeakUtilPercent  float64
	MaxPeakBytes        uint64
	TotalEverBytes      uint64
	MaxConcurrentActive int
}

/*
MemforgeMemorySystemReport is the strategic, aggregated view of memforge telemetry.
*/
type MemforgeMemorySystemReport struct {
	Available                    bool
	CapturedAt                   time.Time
	TotalEvents                  int
	TotalArenas                  int
	ActiveArenas                 int
	TotalMappableCapBytes        uint64
	TotalOpaqueCapBytes          uint64
	GlobalPeakLiveAllocations    int
	GlobalPeakLiveBytes          uint64
	GlobalCapacityHighWaterBytes uint64
	Buckets                      []MemforgeArenaProfileBucket
	SizingVerdicts               []MemforgeSizingVerdict
	LeakDetected                 bool
}

type profileBucketKey struct {
	opaque bool
	name   string
	tag    string
	site   string
}

type profileBucketAccumulator struct {
	key                 profileBucketKey
	displayLabel        string
	siteLine            string
	instanceCount       int
	destroyedCount      int
	activeCount         int
	configuredCapBytes  uint64
	maxPeakUtilPercent  float64
	maxPeakBytes        uint64
	totalEverBytes      uint64
	maxConcurrentActive int
	concurrentActive    int
}

/*
MemforgeDebuggerDefaultStackFilter returns stack filtering tuned for memforge debugger site lines.
*/
func MemforgeDebuggerDefaultStackFilter() MemforgeStackFilter {
	return MemforgeStackFilter{
		IgnorePrefixes: []string{"runtime.", "reflect.", "memcore.", "testing."},
		IgnoreContains: []string{
			"memforge.FixedLinearAllocatorCreate",
			"memforge.FixedLinearAllocatorCreateForDataRegion",
			"memforge.ChainedLinearAllocatorCreate",
			"memforge.DynamicLinearAllocatorCreate",
			"memforge.DynamicLinearAllocatorCreateFunction",
			"memforge.FixedManualAllocatorCreate",
			"memforge.FixedManualAllocatorCreateForDataRegion",
			"memforge.SlabAllocatorCreate",
			"memforge.SlabAllocatorCreateWithSlotSize",
			"memforge.SlabAllocatorCreateForDataRegion",
			"main.main",
		},
		MaxDepth: 2,
	}
}

/*
MemforgeMemoryProfileAnalyze builds an aggregated system report from timeline analysis output.
*/
func MemforgeMemoryProfileAnalyze(
	analysis MemforgeMemoryTimelineAnalysis,
	filter MemforgeStackFilter,
	verdictFilter MemforgeSizingVerdictFilter,
) MemforgeMemorySystemReport {
	report := MemforgeMemorySystemReport{
		Available:                 analysis.Available,
		CapturedAt:                analysis.CapturedAt,
		TotalEvents:               analysis.TotalEvents,
		TotalArenas:               analysis.TotalAllocators,
		ActiveArenas:              analysis.ActiveAllocators,
		GlobalPeakLiveAllocations: analysis.TotalPeakLiveAllocations,
		GlobalPeakLiveBytes:       analysis.TotalPeakLiveBytes,
		LeakDetected:              analysis.LeakDetected,
	}
	if !analysis.Available {
		return report
	}

	if filter.MaxDepth == 0 && len(filter.IgnoreContains) == 0 && len(filter.IgnorePrefixes) == 0 {
		filter = MemforgeDebuggerDefaultStackFilter()
	}

	addressBuckets := make(map[uintptr]profileBucketKey, analysis.TotalAllocators)
	accumulators := make(map[profileBucketKey]*profileBucketAccumulator)

	for _, arena := range analysis.Arenas {
		siteFrames := arena.FilteredCreatorStack
		if len(siteFrames) == 0 {
			siteFrames = MemforgeStackFilterApply(arenaSiteStackFromArena(arena), filter)
		}
		siteSig := stackSignature(siteFrames)
		key := profileBucketKey{
			opaque: arena.OpaqueBacking,
			name:   memforgeAllocatorBaseName(arena.Name),
			tag:    arena.Tag,
			site:   siteSig,
		}
		if key.tag != "" {
			key.site = ""
		}

		addressBuckets[arena.Address] = key

		acc := accumulators[key]
		if acc == nil {
			acc = &profileBucketAccumulator{
				key:          key,
				displayLabel: memforgeAllocatorDisplayLabel(key.name, key.tag),
				siteLine:     memforgeFormatProfileSiteLine(siteFrames),
			}
			accumulators[key] = acc
		}

		acc.instanceCount++
		if arena.Destroyed {
			acc.destroyedCount++
		} else {
			acc.activeCount++
		}

		configuredCap := arena.PeakArenaDataCapBytes
		if configuredCap == 0 {
			configuredCap = arena.CurrentArenaDataCapBytes
		}
		if configuredCap > acc.configuredCapBytes {
			acc.configuredCapBytes = configuredCap
		}

		if arena.PeakUtilizationPercent > acc.maxPeakUtilPercent {
			acc.maxPeakUtilPercent = arena.PeakUtilizationPercent
		}
		if arena.PeakLiveBytes > acc.maxPeakBytes {
			acc.maxPeakBytes = arena.PeakLiveBytes
		}
		acc.totalEverBytes += arena.EverBytes

		if arena.OpaqueBacking {
			report.TotalOpaqueCapBytes += configuredCap
		} else {
			report.TotalMappableCapBytes += configuredCap
		}
	}

	memforgeMemoryProfileTrackConcurrent(&report, analysis.Events, addressBuckets, accumulators)

	report.Buckets = make([]MemforgeArenaProfileBucket, 0, len(accumulators))
	for _, acc := range accumulators {
		report.Buckets = append(report.Buckets, MemforgeArenaProfileBucket{
			OpaqueBacking:       acc.key.opaque,
			AllocatorName:       acc.key.name,
			Tag:                 acc.key.tag,
			DisplayLabel:        acc.displayLabel,
			SiteLine:            acc.siteLine,
			InstanceCount:       acc.instanceCount,
			DestroyedCount:      acc.destroyedCount,
			ActiveCount:         acc.activeCount,
			ConfiguredCapBytes:  acc.configuredCapBytes,
			MaxPeakUtilPercent:  acc.maxPeakUtilPercent,
			MaxPeakBytes:        acc.maxPeakBytes,
			TotalEverBytes:      acc.totalEverBytes,
			MaxConcurrentActive: acc.maxConcurrentActive,
		})
	}

	slices.SortFunc(report.Buckets, compareProfileBuckets)
	report.SizingVerdicts = memforgeSizingVerdictsCollect(report.Buckets, verdictFilter)
	return report
}

func memforgeMemoryProfileTrackConcurrent(
	report *MemforgeMemorySystemReport,
	events []MemforgeTimelineEvent,
	addressBuckets map[uintptr]profileBucketKey,
	accumulators map[profileBucketKey]*profileBucketAccumulator,
) {
	activeCaps := make(map[uintptr]uint64)
	var activeCapacitySum uint64

	for _, evt := range events {
		switch evt.Kind {
		case MemforgeTimelineEventAllocatorRegister:
			capBytes := evt.ArenaDataCapBytes
			if capBytes == 0 {
				capBytes = evt.ArenaTotalBytes
			}
			activeCaps[evt.AllocatorAddress] = capBytes
			activeCapacitySum += capBytes
			if activeCapacitySum > report.GlobalCapacityHighWaterBytes {
				report.GlobalCapacityHighWaterBytes = activeCapacitySum
			}

			if key, ok := addressBuckets[evt.AllocatorAddress]; ok {
				if acc := accumulators[key]; acc != nil {
					acc.concurrentActive++
					if acc.concurrentActive > acc.maxConcurrentActive {
						acc.maxConcurrentActive = acc.concurrentActive
					}
				}
			}

		case MemforgeTimelineEventAllocatorGrow:
			previous := activeCaps[evt.AllocatorAddress]
			if previous == 0 {
				previous = evt.PreviousArenaDataCapBytes
			}
			newCap := evt.ArenaDataCapBytes
			if previous > 0 && newCap > previous {
				activeCapacitySum += newCap - previous
			} else if previous == 0 {
				activeCapacitySum += newCap
			}
			activeCaps[evt.AllocatorAddress] = newCap
			if activeCapacitySum > report.GlobalCapacityHighWaterBytes {
				report.GlobalCapacityHighWaterBytes = activeCapacitySum
			}

		case MemforgeTimelineEventAllocatorDestroy:
			if capBytes := activeCaps[evt.AllocatorAddress]; capBytes > 0 {
				if capBytes <= activeCapacitySum {
					activeCapacitySum -= capBytes
				} else {
					activeCapacitySum = 0
				}
				delete(activeCaps, evt.AllocatorAddress)
			}

			if key, ok := addressBuckets[evt.AllocatorAddress]; ok {
				if acc := accumulators[key]; acc != nil && acc.concurrentActive > 0 {
					acc.concurrentActive--
				}
			}
		}
	}
}

func memforgeSizingVerdictsCollect(buckets []MemforgeArenaProfileBucket, filter MemforgeSizingVerdictFilter) []MemforgeSizingVerdict {
	filter = memforgeSizingVerdictFilterResolve(filter)
	verdicts := make([]MemforgeSizingVerdict, 0, len(buckets))
	for _, bucket := range buckets {
		if bucket.ConfiguredCapBytes == 0 {
			continue
		}
		if memforgeSizingVerdictTagIgnored(bucket.Tag, filter.IgnoreTags) {
			continue
		}

		tagPolicy := memforgeSizingPolicyForTag(bucket.Tag, filter)

		label := bucket.DisplayLabel
		if bucket.Tag != "" {
			label = fmt.Sprintf("\"%s\"", bucket.Tag)
		}

		switch {
		case bucket.MaxPeakUtilPercent < memforgeSizingUnderutilizedThresholdPercent:
			if memforgeSizingPolicyHas(tagPolicy, MemforgeSizingPolicyReserveCapacity) {
				continue
			}
			verdicts = append(verdicts, MemforgeSizingVerdict{
				Kind:               MemforgeSizingVerdictUnderutilized,
				BucketLabel:        label,
				ConfiguredCapBytes: bucket.ConfiguredCapBytes,
				PeakBytes:          bucket.MaxPeakBytes,
				PeakUtilPercent:    bucket.MaxPeakUtilPercent,
				Message: fmt.Sprintf(
					"CRITICAL UNUSED SPACE: %s configured for %s but peaked at %s (%.1f%%).",
					label,
					profileHumanBytes(float64(bucket.ConfiguredCapBytes)),
					profileHumanBytes(float64(bucket.MaxPeakBytes)),
					bucket.MaxPeakUtilPercent,
				),
			})
		case bucket.MaxPeakUtilPercent > memforgeSizingOverutilizedThresholdPercent:
			if memforgeSizingPolicyHas(tagPolicy, MemforgeSizingPolicyExactFit) {
				continue
			}
			if *filter.SuppressExactFitOverutilized && memforgeSizingIsExactFitUtilization(bucket.MaxPeakUtilPercent) {
				continue
			}
			verdicts = append(verdicts, MemforgeSizingVerdict{
				Kind:               MemforgeSizingVerdictOverutilized,
				BucketLabel:        label,
				ConfiguredCapBytes: bucket.ConfiguredCapBytes,
				PeakBytes:          bucket.MaxPeakBytes,
				PeakUtilPercent:    bucket.MaxPeakUtilPercent,
				Message: fmt.Sprintf(
					"CRITICAL OVER-CAPACITY: %s configured for %s peaked at %.1f%% — increase preset or add growth headroom.",
					label,
					profileHumanBytes(float64(bucket.ConfiguredCapBytes)),
					bucket.MaxPeakUtilPercent,
				),
			})
		}
	}

	slices.SortFunc(verdicts, func(a, b MemforgeSizingVerdict) int {
		switch {
		case a.ConfiguredCapBytes > b.ConfiguredCapBytes:
			return -1
		case a.ConfiguredCapBytes < b.ConfiguredCapBytes:
			return 1
		default:
			return 0
		}
	})
	return verdicts
}

func profileHumanBytes(b float64) string {
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

func memforgeAllocatorBaseName(name string) string {
	base := name
	for _, suffix := range []string{" (Mmap)", " (External)", " (Manual)"} {
		base = strings.TrimSuffix(base, suffix)
	}
	return base
}

func memforgeAllocatorDisplayLabel(baseName, tag string) string {
	if tag != "" {
		return fmt.Sprintf("%s - \"%s\"", baseName, tag)
	}
	return baseName
}

func memforgeFormatProfileSiteLine(frames []string) string {
	if len(frames) == 0 {
		return "<unknown>"
	}
	return strings.Join(frames, " -> ")
}

func arenaSiteStackFromArena(arena MemforgeArenaSummary) string {
	if len(arena.FilteredCreatorStack) > 0 {
		return strings.Join(arena.FilteredCreatorStack, " → ")
	}
	return ""
}

func compareProfileBuckets(a, b MemforgeArenaProfileBucket) int {
	switch {
	case a.OpaqueBacking != b.OpaqueBacking:
		if !a.OpaqueBacking {
			return -1
		}
		return 1
	case a.AllocatorName < b.AllocatorName:
		return -1
	case a.AllocatorName > b.AllocatorName:
		return 1
	case a.Tag < b.Tag:
		return -1
	case a.Tag > b.Tag:
		return 1
	case a.SiteLine < b.SiteLine:
		return -1
	case a.SiteLine > b.SiteLine:
		return 1
	default:
		return 0
	}
}
