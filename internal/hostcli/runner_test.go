// Copyright 2026 Alibaba Group
// Licensed under the Apache License, Version 2.0 (the "License");

package hostcli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	apperrors "github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/errors"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/executor"
)

func TestRunnerBuildArgumentsForReviewedCommands(t *testing.T) {
	runner := &Runner{
		defaultProfile: "corp-default",
		commandTimeout: 30 * time.Second,
	}
	instant := int64(1787882400000)
	tests := []struct {
		name       string
		profile    string
		invocation executor.Invocation
		want       []string
	}{
		{
			name:       "current user uses default profile",
			invocation: invocation("contact.get_current_user_profile", nil),
			want: []string{
				"--profile=corp-default", "--format=json", "--timeout=30",
				"contact", "user", "get-self",
			},
		},
		{
			name:    "contact search",
			profile: "corp-selected",
			invocation: invocation("contact.search_contact_by_key_word", map[string]any{
				"keyword": "天依",
			}),
			want: []string{
				"--profile=corp-selected", "--format=json", "--timeout=30",
				"contact", "user", "search", "--query", "天依",
			},
		},
		{
			name: "conversation info",
			invocation: invocation("chat.get_conversation_info", map[string]any{
				"openConversationId": "cid+value==",
			}),
			want: []string{
				"--profile=corp-default", "--format=json", "--timeout=30",
				"chat", "conversation-info", "--group", "cid+value==",
			},
		},
		{
			name:       "calendar list",
			invocation: invocation("calendar.list_calendars", nil),
			want: []string{
				"--profile=corp-default", "--format=json", "--timeout=30",
				"calendar", "book", "list",
			},
		},
		{
			name: "calendar events",
			invocation: invocation("calendar.list_calendar_events", map[string]any{
				"startTime":  instant,
				"endTime":    instant + int64(time.Hour/time.Millisecond),
				"calendarId": "primary",
				"cursor":     "next",
				"limit":      int64(100),
			}),
			want: []string{
				"--profile=corp-default", "--format=json", "--timeout=30",
				"calendar", "event", "list",
				"--start", "2026-08-28T02:00:00Z",
				"--end", "2026-08-28T03:00:00Z",
				"--calendar-id", "primary",
				"--cursor", "next",
				"--limit", "100",
			},
		},
		{
			name: "todo list",
			invocation: invocation("todo.get_user_todos_in_current_org", map[string]any{
				"roleTypes":           []any{"creator", "executor"},
				"pageNum":             "1",
				"pageSize":            "20",
				"todoStatus":          "false",
				"priorityList":        []any{int64(10), int64(40)},
				"planFinishDateStart": instant,
				"planFinishDateEnd":   instant + int64(time.Hour/time.Millisecond),
			}),
			want: []string{
				"--profile=corp-default", "--format=json", "--timeout=30",
				"todo", "task", "list",
				"--role-types", "creator,executor",
				"--page", "1",
				"--size", "20",
				"--status", "false",
				"--priority", "10,40",
				"--plan-finish-date-start", "2026-08-28T02:00:00Z",
				"--plan-finish-date-end", "2026-08-28T03:00:00Z",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := runner.buildArguments(test.profile, test.invocation)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("arguments = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestRunnerRejectsUnknownCommandsAndProperties(t *testing.T) {
	runner := &Runner{defaultProfile: "corp", commandTimeout: time.Second}
	for _, test := range []executor.Invocation{
		invocation("unknown.command", nil),
		invocation("chat.get_conversation_info", map[string]any{
			"openConversationId": "cid",
			"unexpected":         "value",
		}),
	} {
		if _, err := runner.buildArguments("", test); err == nil {
			t.Fatalf("buildArguments(%s) error = nil", test.CanonicalPath)
		}
	}
}

func TestRunnerExecutesJSONAndMapsStructuredErrors(t *testing.T) {
	success := newTestRunner(t, `#!/bin/sh
printf '%s' '{"success":true,"result":{"value":"ok"}}'
`, 1024)
	result, err := success.RunWithProfile(
		context.Background(),
		"corp-selected",
		invocation("contact.get_current_user_profile", nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Response["success"] != true {
		t.Fatalf("response = %#v", result.Response)
	}

	authFailure := newTestRunner(t, `#!/bin/sh
printf '%s' '{"code":2,"category":"auth","message":"expired","reason":"token_expired"}' >&2
exit 2
`, 1024)
	_, err = authFailure.Run(
		context.Background(),
		invocation("contact.get_current_user_profile", nil),
	)
	var typed *apperrors.Error
	if !errors.As(err, &typed) || typed.Category != apperrors.CategoryAuth || typed.Reason != "token_expired" {
		t.Fatalf("auth error = %#v", err)
	}
}

func TestRunnerRejectsMalformedAndOversizedOutput(t *testing.T) {
	for _, test := range []struct {
		name   string
		script string
		limit  int64
		reason string
	}{
		{name: "empty", script: "#!/bin/sh\nexit 0\n", limit: 128, reason: "wrapper_output_empty"},
		{name: "invalid json", script: "#!/bin/sh\nprintf 'not-json'\n", limit: 128, reason: "wrapper_output_invalid"},
		{name: "too large", script: "#!/bin/sh\nprintf '123456789'\n", limit: 8, reason: "wrapper_output_too_large"},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := newTestRunner(t, test.script, test.limit)
			_, err := runner.Run(context.Background(), invocation("contact.get_current_user_profile", nil))
			var typed *apperrors.Error
			if !errors.As(err, &typed) || typed.Reason != test.reason {
				t.Fatalf("error = %#v", err)
			}
		})
	}
}

func TestRunnerHonorsContextCancellation(t *testing.T) {
	runner := newTestRunner(t, "#!/bin/sh\n/bin/sleep 5\n", 1024)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := runner.Run(ctx, invocation("contact.get_current_user_profile", nil))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("canceled wrapper took %s", time.Since(started))
	}
}

func newTestRunner(t *testing.T, script string, maxOutputBytes int64) *Runner {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dws")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(Options{
		WrapperPath:    path,
		DefaultProfile: "corp-default",
		CommandTimeout: time.Second,
		MaxOutputBytes: maxOutputBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func invocation(canonicalPath string, params map[string]any) executor.Invocation {
	return executor.Invocation{
		CanonicalPath: canonicalPath,
		Params:        params,
	}
}

func TestValidateProfileSelector(t *testing.T) {
	if err := ValidateProfileSelector("dingd8e1123006514592"); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"", " ", "corp-a,corp-b", "corp\nnext", strings.Repeat("a", 256)} {
		if err := ValidateProfileSelector(value); err == nil {
			t.Fatalf("ValidateProfileSelector(%q) error = nil", value)
		}
	}
}
