// Copyright 2026 Alibaba Group
// Licensed under the Apache License, Version 2.0

package resultnormalizer

import (
	"sort"
	"time"
)

const CalendarListEventsCanonicalPath = "calendar.list_calendar_events"

func NormalizeCommand(canonicalPath string, payload map[string]any) {
	if canonicalPath == CalendarListEventsCanonicalPath {
		NormalizeCalendarEvents(payload)
	}
}

func NormalizeCalendarEvents(payload map[string]any) {
	result, ok := payload["result"].(map[string]any)
	if !ok {
		return
	}
	events, ok := result["events"].([]any)
	if !ok {
		return
	}
	filtered := make([]any, 0, len(events))
	for _, event := range events {
		record, ok := event.(map[string]any)
		if !ok {
			continue
		}
		id, ok := record["id"].(string)
		if !ok || id == "" {
			continue
		}
		filtered = append(filtered, event)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		return calendarEventSortKey(filtered[i]) < calendarEventSortKey(filtered[j])
	})
	result["events"] = filtered
}

func calendarEventSortKey(event any) int64 {
	record, ok := event.(map[string]any)
	if !ok {
		return 0
	}
	if start, ok := record["start"].(map[string]any); ok {
		if dateTime, ok := start["dateTime"].(string); ok && dateTime != "" {
			if parsed, err := time.Parse(time.RFC3339, dateTime); err == nil {
				return parsed.UnixMilli()
			}
		}
	}
	if created, ok := record["created"].(float64); ok {
		return int64(created)
	}
	if updated, ok := record["updated"].(float64); ok {
		return int64(updated)
	}
	return 0
}
