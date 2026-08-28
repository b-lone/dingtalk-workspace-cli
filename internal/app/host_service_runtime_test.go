// Copyright 2026 Alibaba Group
// Licensed under the Apache License, Version 2.0 (the "License");

package app

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func TestHostHTTPCommandServicePublishesOnlyInfinityReadCommands(t *testing.T) {
	service, err := NewHostHTTPCommandService(context.Background(), HostHTTPCommandServiceOptions{
		WrapperPath:    writeIdentityWrapper(t, "corp-alibaba"),
		Profile:        "corp-alibaba",
		CommandTimeout: time.Second,
		MaxOutputBytes: 4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	want := []string{
		"calendar.list_calendar_events",
		"calendar.list_calendars",
		"chat.get_conversation_info",
		"contact.get_current_user_profile",
		"contact.search_contact_by_key_word",
		"todo.get_user_todos_in_current_org",
	}
	got := make([]string, 0, len(service.ListCommands()))
	for _, command := range service.ListCommands() {
		got = append(got, command.CanonicalPath)
	}
	sort.Strings(got)
	if len(got) != len(want) {
		t.Fatalf("command count = %d, want %d; commands=%v", len(got), len(want), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("commands = %v, want %v", got, want)
		}
	}
}

func TestHostHTTPCommandServiceRejectsMismatchedDefaultIdentity(t *testing.T) {
	_, err := NewHostHTTPCommandService(context.Background(), HostHTTPCommandServiceOptions{
		WrapperPath:    writeIdentityWrapper(t, "corp-other"),
		Profile:        "corp-alibaba",
		CommandTimeout: time.Second,
		MaxOutputBytes: 4096,
	})
	if err == nil {
		t.Fatal("NewHostHTTPCommandService() error = nil, want identity mismatch")
	}
}

func writeIdentityWrapper(t *testing.T, corpID string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dws")
	payload := `#!/bin/sh
printf '%s' '{"result":[{"orgEmployeeModel":{"corpId":"` + corpID + `","userId":"user-1"}}],"success":true}'
`
	if err := os.WriteFile(path, []byte(payload), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
