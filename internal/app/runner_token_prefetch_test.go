// Copyright 2026 Alibaba Group
// Licensed under the Apache License, Version 2.0 (the "License");

package app

import (
	"context"
	"testing"
	"time"

	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/cli"
)

func TestRuntimeRunnerDisablesAsyncTokenPrefetchForProfileScopedService(t *testing.T) {
	called := make(chan struct{}, 1)
	runner, ok := newHTTPCommandRunnerWithFlags(cli.NewEnvironmentLoader(), &GlobalFlags{}).(*runtimeRunner)
	if !ok {
		t.Fatal("newHTTPCommandRunnerWithFlags() did not return a runtime runner")
	}
	runner.prefetchToken = func(context.Context) {
		called <- struct{}{}
	}

	runner.startAsyncTokenPrefetch(context.Background())
	select {
	case <-called:
		t.Fatal("async token prefetch ran for a profile-scoped service request")
	case <-time.After(50 * time.Millisecond):
	}
}
