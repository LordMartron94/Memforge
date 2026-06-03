package memforge

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

type jsonlTimelineFreedAllocation struct {
	AllocationAddress string `json:"alloc_addr"`
	SizeBytes         uint64 `json:"size_bytes"`
	OriginalSeq       uint64 `json:"orig_seq"`
	OriginalCreatedAt string `json:"orig_ts"`
}

type jsonlTimelineEvent struct {
	Seq                       uint64                         `json:"seq"`
	Timestamp                 string                         `json:"ts"`
	Kind                      string                         `json:"kind"`
	AllocatorAddress          string                         `json:"allocator_addr"`
	AllocatorName             string                         `json:"allocator_name"`
	Stack                     []string                       `json:"stack,omitempty"`
	AllocationAddress         string                         `json:"alloc_addr,omitempty"`
	SizeBytes                 uint64                         `json:"size_bytes,omitempty"`
	OriginalSeq               uint64                         `json:"orig_seq,omitempty"`
	OriginalCreatedAt         string                         `json:"orig_ts,omitempty"`
	Freed                     []jsonlTimelineFreedAllocation `json:"freed,omitempty"`
	ArenaDataCapBytes         uint64                         `json:"arena_data_cap_bytes,omitempty"`
	ArenaTotalBytes           uint64                         `json:"arena_total_bytes,omitempty"`
	PreviousArenaDataCapBytes uint64                         `json:"prev_arena_data_cap_bytes,omitempty"`
	Tag                       string                         `json:"tag,omitempty"`
	OpaqueBacking             bool                           `json:"opaque_backing,omitempty"`
}

type jsonlTimelineSummary struct {
	Type       string `json:"type"`
	Provider   string `json:"provider,omitempty"`
	CapturedAt string `json:"captured_at,omitempty"`
	EventCount int    `json:"event_count,omitempty"`
}

/*
MemforgeMemoryTimelineSnapshotWriteJSONL writes one JSON object per event plus a trailing summary record.
*/
func MemforgeMemoryTimelineSnapshotWriteJSONL(w io.Writer, snapshot MemforgeMemoryTimelineSnapshot) error {
	encoder := json.NewEncoder(w)
	for _, evt := range snapshot.Events {
		if err := encoder.Encode(timelineEventToJSONL(evt)); err != nil {
			return fmt.Errorf("failed to write timeline event: %w", err)
		}
	}

	summary := jsonlTimelineSummary{
		Type:       "summary",
		Provider:   "memforge",
		CapturedAt: snapshot.CapturedAt.Format(time.RFC3339Nano),
		EventCount: snapshot.EventCount,
	}
	if err := encoder.Encode(summary); err != nil {
		return fmt.Errorf("failed to write timeline summary: %w", err)
	}
	return nil
}

/*
MemforgeMemoryTimelineSnapshotReadJSONL parses timeline events written by MemforgeMemoryTimelineSnapshotWriteJSONL.
*/
func MemforgeMemoryTimelineSnapshotReadJSONL(r io.Reader) (MemforgeMemoryTimelineSnapshot, error) {
	snapshot := MemforgeMemoryTimelineSnapshot{
		Available:  true,
		CapturedAt: time.Now(),
	}

	scanner := bufio.NewScanner(r)
	const maxLineBytes = 16 * 1024 * 1024
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, maxLineBytes)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var summary jsonlTimelineSummary
		if err := json.Unmarshal([]byte(line), &summary); err == nil && summary.Type == "summary" {
			if summary.EventCount > 0 {
				snapshot.EventCount = summary.EventCount
			}
			if summary.CapturedAt != "" {
				if capturedAt, err := time.Parse(time.RFC3339Nano, summary.CapturedAt); err == nil {
					snapshot.CapturedAt = capturedAt
				}
			}
			continue
		}

		var record jsonlTimelineEvent
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return MemforgeMemoryTimelineSnapshot{}, fmt.Errorf("failed to parse timeline JSONL line: %w", err)
		}
		evt, err := timelineEventFromJSONL(record)
		if err != nil {
			return MemforgeMemoryTimelineSnapshot{}, err
		}
		snapshot.Events = append(snapshot.Events, evt)
	}
	if err := scanner.Err(); err != nil {
		return MemforgeMemoryTimelineSnapshot{}, fmt.Errorf("failed reading timeline JSONL: %w", err)
	}

	snapshot.EventCount = len(snapshot.Events)
	return snapshot, nil
}

func timelineEventToJSONL(evt MemforgeTimelineEvent) jsonlTimelineEvent {
	out := jsonlTimelineEvent{
		Seq:               evt.Seq,
		Timestamp:         evt.Timestamp.Format(time.RFC3339Nano),
		Kind:              string(evt.Kind),
		AllocatorAddress:  formatTimelineAddressForJSONL(evt.AllocatorAddress),
		AllocatorName:     evt.AllocatorName,
		Stack:             timelineStackSplit(evt.Stack),
		AllocationAddress: formatTimelineAddressForJSONL(evt.AllocationAddress),
		SizeBytes:         evt.SizeBytes,
		OriginalSeq:       evt.OriginalSeq,
	}
	if !evt.OriginalCreatedAt.IsZero() {
		out.OriginalCreatedAt = evt.OriginalCreatedAt.Format(time.RFC3339Nano)
	}
	out.ArenaDataCapBytes = evt.ArenaDataCapBytes
	out.ArenaTotalBytes = evt.ArenaTotalBytes
	out.PreviousArenaDataCapBytes = evt.PreviousArenaDataCapBytes
	out.OpaqueBacking = evt.OpaqueBacking
	out.Tag = evt.Tag
	if len(evt.FreedAllocations) > 0 {
		out.Freed = make([]jsonlTimelineFreedAllocation, len(evt.FreedAllocations))
		for i, freed := range evt.FreedAllocations {
			entry := jsonlTimelineFreedAllocation{
				AllocationAddress: formatTimelineAddressForJSONL(freed.AllocationAddress),
				SizeBytes:         freed.SizeBytes,
				OriginalSeq:       freed.OriginalSeq,
			}
			if !freed.OriginalCreatedAt.IsZero() {
				entry.OriginalCreatedAt = freed.OriginalCreatedAt.Format(time.RFC3339Nano)
			}
			out.Freed[i] = entry
		}
	}
	return out
}

func timelineEventFromJSONL(record jsonlTimelineEvent) (MemforgeTimelineEvent, error) {
	ts, err := time.Parse(time.RFC3339Nano, record.Timestamp)
	if err != nil {
		return MemforgeTimelineEvent{}, fmt.Errorf("invalid timeline timestamp '%s': %w", record.Timestamp, err)
	}

	evt := MemforgeTimelineEvent{
		Seq:                       record.Seq,
		Timestamp:                 ts,
		Kind:                      MemforgeTimelineEventKind(record.Kind),
		AllocatorAddress:          parseTimelineAddress(record.AllocatorAddress),
		AllocatorName:             record.AllocatorName,
		Stack:                     strings.Join(record.Stack, " → "),
		AllocationAddress:         parseTimelineAddress(record.AllocationAddress),
		SizeBytes:                 record.SizeBytes,
		OriginalSeq:               record.OriginalSeq,
		ArenaDataCapBytes:         record.ArenaDataCapBytes,
		ArenaTotalBytes:           record.ArenaTotalBytes,
		PreviousArenaDataCapBytes: record.PreviousArenaDataCapBytes,
		OpaqueBacking:             record.OpaqueBacking,
		Tag:                       record.Tag,
	}
	if record.OriginalCreatedAt != "" {
		origTS, err := time.Parse(time.RFC3339Nano, record.OriginalCreatedAt)
		if err != nil {
			return MemforgeTimelineEvent{}, fmt.Errorf("invalid orig_ts '%s': %w", record.OriginalCreatedAt, err)
		}
		evt.OriginalCreatedAt = origTS
	}
	if len(record.Freed) > 0 {
		evt.FreedAllocations = make([]MemforgeTimelineFreedAllocation, len(record.Freed))
		for i, freed := range record.Freed {
			entry := MemforgeTimelineFreedAllocation{
				AllocationAddress: parseTimelineAddress(freed.AllocationAddress),
				SizeBytes:         freed.SizeBytes,
				OriginalSeq:       freed.OriginalSeq,
			}
			if freed.OriginalCreatedAt != "" {
				origTS, err := time.Parse(time.RFC3339Nano, freed.OriginalCreatedAt)
				if err != nil {
					return MemforgeTimelineEvent{}, fmt.Errorf("invalid freed orig_ts '%s': %w", freed.OriginalCreatedAt, err)
				}
				entry.OriginalCreatedAt = origTS
			}
			evt.FreedAllocations[i] = entry
		}
	}
	return evt, nil
}

func timelineStackSplit(stack string) []string {
	trimmed := strings.TrimSpace(stack)
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, " → ")
}

func formatTimelineAddressForJSONL(address uintptr) string {
	if address == 0 {
		return "<invalid>"
	}
	return fmt.Sprintf("0x%016x", address)
}

func parseTimelineAddress(raw string) uintptr {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "<invalid>" {
		return 0
	}
	trimmed = strings.TrimPrefix(trimmed, "0x")
	trimmed = strings.TrimPrefix(trimmed, "0X")
	value, err := strconv.ParseUint(trimmed, 16, 64)
	if err != nil {
		return 0
	}
	return uintptr(value)
}
