package memforge

import "slices"

/*
MemforgeDomainGroupBuckets partitions profile buckets by DomainGroups tag membership.

Buckets whose Tag is listed under a domain appear under that domain in declaration order
within each domain slice. Buckets with an empty tag, an unlisted tag, or a tag claimed by
no domain are returned in Ungrouped preserving the input order.
*/
func MemforgeDomainGroupBuckets(
	buckets []MemforgeArenaProfileBucket,
	domainGroups map[string][]string,
) (grouped map[string][]MemforgeArenaProfileBucket, ungrouped []MemforgeArenaProfileBucket) {
	if len(domainGroups) == 0 {
		return nil, buckets
	}

	tagDomain := memforgeDomainGroupsTagDomainIndex(domainGroups)
	grouped = make(map[string][]MemforgeArenaProfileBucket, len(domainGroups))
	for domain := range domainGroups {
		grouped[domain] = nil
	}

	for _, bucket := range buckets {
		domain, ok := tagDomain[bucket.Tag]
		if !ok || domain == "" {
			ungrouped = append(ungrouped, bucket)
			continue
		}
		grouped[domain] = append(grouped[domain], bucket)
	}
	return grouped, ungrouped
}

func memforgeDomainGroupsTagDomainIndex(domainGroups map[string][]string) map[string]string {
	tagDomain := make(map[string]string)
	domains := make([]string, 0, len(domainGroups))
	for domain := range domainGroups {
		domains = append(domains, domain)
	}
	slices.Sort(domains)
	for _, domain := range domains {
		for _, tag := range domainGroups[domain] {
			if tag == "" {
				continue
			}
			if _, claimed := tagDomain[tag]; claimed {
				continue
			}
			tagDomain[tag] = domain
		}
	}
	return tagDomain
}

/*
MemforgeDomainProfileSummary rolls up arena profile buckets belonging to one domain.
*/
type MemforgeDomainProfileSummary struct {
	BucketCount        int
	InstanceCount      int
	ActiveCount        int
	DestroyedCount     int
	ConfiguredCapBytes uint64
	MappableCapBytes   uint64
	OpaqueCapBytes     uint64
	MaxPeakBytes       uint64
	TotalEverBytes     uint64
}

/*
MemforgeDomainProfileSummarize aggregates bucket telemetry for one domain group.
*/
func MemforgeDomainProfileSummarize(buckets []MemforgeArenaProfileBucket) MemforgeDomainProfileSummary {
	summary := MemforgeDomainProfileSummary{
		BucketCount: len(buckets),
	}
	for _, bucket := range buckets {
		summary.InstanceCount += bucket.InstanceCount
		summary.ActiveCount += bucket.ActiveCount
		summary.DestroyedCount += bucket.DestroyedCount
		summary.ConfiguredCapBytes += bucket.ConfiguredCapBytes
		summary.TotalEverBytes += bucket.TotalEverBytes
		if bucket.MaxPeakBytes > summary.MaxPeakBytes {
			summary.MaxPeakBytes = bucket.MaxPeakBytes
		}
		if bucket.OpaqueBacking {
			summary.OpaqueCapBytes += bucket.ConfiguredCapBytes
		} else {
			summary.MappableCapBytes += bucket.ConfiguredCapBytes
		}
	}
	return summary
}

func memforgeDomainGroupsSortedNames(domainGroups map[string][]string) []string {
	names := make([]string, 0, len(domainGroups))
	for name := range domainGroups {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
