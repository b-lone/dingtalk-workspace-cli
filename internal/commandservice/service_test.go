// Copyright 2026 Alibaba Group
// Licensed under the Apache License, Version 2.0 (the "License");

package commandservice

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/cli"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/executor"
)

type recordingRunner struct {
	invocations []executor.Invocation
}

func (r *recordingRunner) Run(_ context.Context, invocation executor.Invocation) (executor.Result, error) {
	r.invocations = append(r.invocations, invocation)
	return executor.Result{
		Invocation: invocation,
		Response: map[string]any{
			"content": map[string]any{"ok": true},
		},
	}, nil
}

type profileRecordingRunner struct{}

func (profileRecordingRunner) Run(_ context.Context, invocation executor.Invocation) (executor.Result, error) {
	return executor.Result{
		Invocation: invocation,
		Response: map[string]any{
			"content": map[string]any{"profile": "unscoped"},
		},
	}, nil
}

func (profileRecordingRunner) RunWithProfile(_ context.Context, profile string, invocation executor.Invocation) (executor.Result, error) {
	return executor.Result{
		Invocation: invocation,
		Response: map[string]any{
			"content": map[string]any{"profile": profile},
		},
	}, nil
}

func TestServiceListsAndExecutesReviewedMCPCommands(t *testing.T) {
	runner := &recordingRunner{}
	service := newTestService(t, runner)

	if got := service.Metadata().CommandCount; got != 2 {
		t.Fatalf("CommandCount = %d, want 2", got)
	}
	if got := service.ListCommands(); len(got) != 2 || got[0].CanonicalPath != "sample.remove" || got[1].CanonicalPath != "sample.run" {
		t.Fatalf("ListCommands() = %#v", got)
	}
	result, err := service.Execute(context.Background(), "sample.run", ExecuteRequest{
		Arguments: map[string]any{
			"input": "value",
			"limit": json.Number("3"),
		},
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.DryRun || !reflect.DeepEqual(result.Content, map[string]any{"ok": true}) {
		t.Fatalf("Execute() result = %#v", result)
	}
	if len(runner.invocations) != 1 {
		t.Fatalf("runner invocations = %d, want 1", len(runner.invocations))
	}
	got := runner.invocations[0]
	wantParams := map[string]any{
		"inputValue": "value",
		"maxResults": int64(3),
		"scope":      []string{"all"},
	}
	if got.CanonicalProduct != "sample-api" || got.Tool != "run_rpc" || got.CanonicalPath != "sample.run" || !got.DryRun {
		t.Fatalf("runner invocation identity = %#v", got)
	}
	if !reflect.DeepEqual(got.Params, wantParams) {
		t.Fatalf("runner params = %#v, want %#v", got.Params, wantParams)
	}
}

func TestServiceFailsClosedForConfirmationAndInvalidArguments(t *testing.T) {
	service := newTestService(t, &recordingRunner{})

	_, err := service.Execute(context.Background(), "sample.remove", ExecuteRequest{})
	assertServiceErrorCode(t, err, CodeConfirmationRequired)

	_, err = service.Execute(context.Background(), "sample.remove", ExecuteRequest{
		Confirmed: true,
		DryRun:    true,
	})
	assertServiceErrorCode(t, err, CodeDryRunUnsupported)

	_, err = service.Execute(context.Background(), "sample.run", ExecuteRequest{
		Arguments: map[string]any{"unknown": "value"},
	})
	assertServiceErrorCode(t, err, CodeInvalidArguments)

	_, err = service.Execute(context.Background(), "sample.local", ExecuteRequest{})
	assertServiceErrorCode(t, err, CodeCommandUnavailable)

	_, err = service.Execute(context.Background(), "sample.missing", ExecuteRequest{})
	assertServiceErrorCode(t, err, CodeCommandNotFound)
}

func TestServiceExecutesAgainstRequestedProfile(t *testing.T) {
	service := newTestService(t, profileRecordingRunner{})

	result, err := service.Execute(context.Background(), "sample.run", ExecuteRequest{
		Profile:   "Alibaba",
		Arguments: map[string]any{"input": "value"},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := result.Content["profile"]; got != "Alibaba" {
		t.Fatalf("Execute() profile = %v, want Alibaba", got)
	}
}

func TestServiceRejectsProfileWhenRunnerCannotSelectIt(t *testing.T) {
	service := newTestService(t, &recordingRunner{})

	_, err := service.Execute(context.Background(), "sample.run", ExecuteRequest{
		Profile:   "Alibaba",
		Arguments: map[string]any{"input": "value"},
	})
	assertServiceErrorCode(t, err, CodeInvalidArguments)
}

func newTestService(t *testing.T, runner executor.Runner) *Service {
	t.Helper()
	run := cli.ToolSpec{
		Identity: cli.ToolIdentitySpec{
			ProductID: "sample",
			Name:      "run",
			CLIPath:   "sample run",
		},
		Parameters: []cli.ParameterSpec{
			{
				Name:          "input",
				Type:          "string",
				Property:      "inputValue",
				Required:      true,
				InterfaceType: "string",
			},
			{
				Name:          "limit",
				Type:          "integer",
				Property:      "maxResults",
				Default:       json.RawMessage("0"),
				InterfaceType: "integer",
			},
			{
				Name:             "scopes",
				Type:             "array",
				Property:         "scope",
				InterfaceDefault: json.RawMessage(`"all"`),
				InterfaceType:    "array",
			},
		},
		DryRun: &cli.DryRunSpec{PreviewKind: cli.DryRunPreviewRequest},
		Safety: cli.SafetySpec{
			Effect:       "read",
			Confirmation: "not_required",
		},
		Interface: cli.InterfaceSpec{
			Ref:          &cli.InterfaceRefSpec{ProductID: "sample-api", RPCName: "run_rpc"},
			Mode:         cli.InterfaceModeMCP,
			Availability: cli.InterfaceAvailable,
		},
	}
	remove := cli.ToolSpec{
		Identity: cli.ToolIdentitySpec{
			ProductID: "sample",
			Name:      "remove",
			CLIPath:   "sample remove",
		},
		Safety: cli.SafetySpec{
			Effect:       "write",
			Confirmation: "user_required",
		},
		Interface: cli.InterfaceSpec{
			Ref:          &cli.InterfaceRefSpec{ProductID: "sample-api", RPCName: "remove_rpc"},
			Mode:         cli.InterfaceModeMCP,
			Availability: cli.InterfaceAvailable,
		},
	}
	local := cli.ToolSpec{
		Identity: cli.ToolIdentitySpec{
			ProductID: "sample",
			Name:      "local",
			CLIPath:   "sample local",
		},
		Interface: cli.InterfaceSpec{
			Mode:         cli.InterfaceModeLocal,
			Availability: cli.InterfaceAvailable,
		},
	}
	registry, err := cli.SchemaRegistryFromRuntime("test", []cli.ProductSpec{{
		ID:    "sample",
		Tools: []cli.ToolSpec{run, remove, local},
	}})
	if err != nil {
		t.Fatal(err)
	}
	index, err := registry.Index()
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(Options{
		Version:     "test",
		CatalogHash: "catalog",
		SurfaceHash: "surface",
		Index:       index,
		Runner:      runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func assertServiceErrorCode(t *testing.T, err error, want string) {
	t.Helper()
	var serviceErr *Error
	if !errors.As(err, &serviceErr) {
		t.Fatalf("error = %v, want *Error", err)
	}
	if serviceErr.Code != want {
		t.Fatalf("error code = %q, want %q", serviceErr.Code, want)
	}
}
