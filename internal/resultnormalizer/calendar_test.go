// Copyright 2026 Alibaba Group
// Licensed under the Apache License, Version 2.0

package resultnormalizer

import "testing"

func TestNormalizeCalendarEventsFiltersAndSorts(t *testing.T) {
	payload := map[string]any{
		"result": map[string]any{
			"events": []any{
				map[string]any{
					"id": "later",
					"start": map[string]any{
						"dateTime": "2026-07-27T12:00:00+08:00",
					},
				},
				map[string]any{"reminders": nil},
				map[string]any{
					"id": "earlier",
					"start": map[string]any{
						"dateTime": "2026-07-27T10:00:00+08:00",
					},
				},
			},
		},
	}

	NormalizeCalendarEvents(payload)

	events := payload["result"].(map[string]any)["events"].([]any)
	if len(events) != 2 {
		t.Fatalf("event count = %d, want 2", len(events))
	}
	if got := events[0].(map[string]any)["id"]; got != "earlier" {
		t.Fatalf("first event id = %v, want earlier", got)
	}
	if got := events[1].(map[string]any)["id"]; got != "later" {
		t.Fatalf("second event id = %v, want later", got)
	}
}
