package memforge

/*
MemforgeAllocatorSizingPolicy configures how memforge debugger sizing verdicts treat an allocator tag.

Configure policies through MemforgeSizingVerdictFilter.TagPolicies when calling MemforgeMemoryDebugWithParams.
*/
type MemforgeAllocatorSizingPolicy uint8

const (
	MemforgeSizingPolicyNone MemforgeAllocatorSizingPolicy = 0

	// MemforgeSizingPolicyExactFit suppresses over-capacity verdicts for arenas sized to a known requirement.
	MemforgeSizingPolicyExactFit MemforgeAllocatorSizingPolicy = 1 << 0

	// MemforgeSizingPolicyReserveCapacity suppresses underutilization verdicts for intentionally oversized arenas.
	MemforgeSizingPolicyReserveCapacity MemforgeAllocatorSizingPolicy = 1 << 1
)

/*
MemforgeSizingVerdictFilter configures which sizing verdicts MemforgeMemoryProfileAnalyze emits.
*/
type MemforgeSizingVerdictFilter struct {
	// TagPolicies maps functional allocator tags to sizing policies applied at report time.
	TagPolicies map[string]MemforgeAllocatorSizingPolicy

	// IgnoreTags skips all sizing verdicts for buckets with matching functional tags.
	IgnoreTags []string

	// SuppressExactFitOverutilized skips over-capacity warnings when peak utilization is at least 99.5%.
	// Defaults to true when nil.
	SuppressExactFitOverutilized *bool
}

func memforgeSizingVerdictFilterResolve(filter MemforgeSizingVerdictFilter) MemforgeSizingVerdictFilter {
	if filter.SuppressExactFitOverutilized == nil {
		suppress := true
		filter.SuppressExactFitOverutilized = &suppress
	}
	return filter
}

func memforgeSizingPolicyForTag(tag string, filter MemforgeSizingVerdictFilter) MemforgeAllocatorSizingPolicy {
	if tag == "" || filter.TagPolicies == nil {
		return MemforgeSizingPolicyNone
	}
	return filter.TagPolicies[tag]
}

func memforgeSizingPolicyHas(policy, flag MemforgeAllocatorSizingPolicy) bool {
	return policy&flag != 0
}

func memforgeSizingVerdictTagIgnored(tag string, ignoreTags []string) bool {
	if tag == "" {
		return false
	}
	for _, ignored := range ignoreTags {
		if ignored == tag {
			return true
		}
	}
	return false
}

func memforgeSizingIsExactFitUtilization(peakUtilPercent float64) bool {
	return peakUtilPercent >= 99.5
}
