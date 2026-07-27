// Copyright 2026 Alibaba Group
// Licensed under the Apache License, Version 2.0 (the "License");

package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	authpkg "github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/auth"
	apperrors "github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/errors"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/executor"
)

func TestFixedProfileCredentialsRefreshesExpiredAccessToken(t *testing.T) {
	tokenData := &authpkg.TokenData{
		AccessToken:  "expired",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(-time.Hour),
		RefreshExpAt: time.Now().Add(time.Hour),
		CorpID:       "ding-test",
	}
	refreshCalls := 0
	resetCalls := 0
	credentials := &fixedProfileCredentials{
		profile: "ding-test",
		load: func() (*authpkg.TokenData, error) {
			copy := *tokenData
			return &copy, nil
		},
		refresh: func(context.Context) error {
			refreshCalls++
			tokenData.AccessToken = "fresh"
			tokenData.ExpiresAt = time.Now().Add(time.Hour)
			return nil
		},
		resetTokenCache: func() {
			resetCalls++
		},
	}

	if err := credentials.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if refreshCalls != 1 {
		t.Fatalf("refresh calls = %d, want 1", refreshCalls)
	}
	if resetCalls != 1 {
		t.Fatalf("reset calls = %d, want 1", resetCalls)
	}
	if err := credentials.Ready(context.Background()); err != nil {
		t.Fatalf("Ready() error = %v", err)
	}
}

func TestFixedProfileCredentialsReadyIsReadOnly(t *testing.T) {
	refreshCalls := 0
	credentials := &fixedProfileCredentials{
		profile: "ding-test",
		load: func() (*authpkg.TokenData, error) {
			return &authpkg.TokenData{
				AccessToken:  "expired",
				RefreshToken: "refresh",
				ExpiresAt:    time.Now().Add(-time.Hour),
				RefreshExpAt: time.Now().Add(time.Hour),
				CorpID:       "ding-test",
			}, nil
		},
		refresh: func(context.Context) error {
			refreshCalls++
			return nil
		},
		resetTokenCache: func() {},
	}

	if err := credentials.Ready(context.Background()); err == nil {
		t.Fatal("Ready() error = nil, want invalid access token error")
	}
	if refreshCalls != 0 {
		t.Fatalf("refresh calls = %d, want 0", refreshCalls)
	}
}

func TestFixedProfileCredentialRunnerStopsBeforeCommand(t *testing.T) {
	next := &recordingRunner{}
	credentials := &fixedProfileCredentials{
		profile: "ding-test",
		load: func() (*authpkg.TokenData, error) {
			return nil, errors.New("unavailable")
		},
		refresh:         func(context.Context) error { return nil },
		resetTokenCache: func() {},
	}
	runner := &fixedProfileCredentialRunner{
		credentials: credentials,
		next:        next,
	}

	_, err := runner.Run(context.Background(), executor.Invocation{})
	if err == nil {
		t.Fatal("Run() error = nil, want auth error")
	}
	var authErr *apperrors.Error
	if !errors.As(err, &authErr) || authErr.Category != apperrors.CategoryAuth {
		t.Fatalf("Run() error = %#v, want auth category", err)
	}
	if next.called {
		t.Fatal("next runner was called with unavailable credentials")
	}
}

func TestFixedProfileCredentialRunnerPreservesDryRunBarrier(t *testing.T) {
	next := &recordingRunner{}
	credentials := &fixedProfileCredentials{
		profile: "ding-test",
		load: func() (*authpkg.TokenData, error) {
			return nil, errors.New("must not load credentials")
		},
		refresh:         func(context.Context) error { return nil },
		resetTokenCache: func() {},
	}
	runner := &fixedProfileCredentialRunner{
		credentials: credentials,
		next:        next,
	}

	if _, err := runner.Run(context.Background(), executor.Invocation{DryRun: true}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !next.called {
		t.Fatal("dry-run invocation did not reach the local runner")
	}
}

func TestFixedProfileCredentialsMaintainsAccessToken(t *testing.T) {
	var stateMu sync.Mutex
	tokenData := &authpkg.TokenData{
		AccessToken:  "expired",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(-time.Hour),
		RefreshExpAt: time.Now().Add(time.Hour),
		CorpID:       "ding-test",
	}
	refreshed := make(chan struct{}, 1)
	credentials := &fixedProfileCredentials{
		profile: "ding-test",
		load: func() (*authpkg.TokenData, error) {
			stateMu.Lock()
			defer stateMu.Unlock()
			copy := *tokenData
			return &copy, nil
		},
		refresh: func(context.Context) error {
			stateMu.Lock()
			tokenData.AccessToken = "fresh"
			tokenData.ExpiresAt = time.Now().Add(time.Hour)
			stateMu.Unlock()
			select {
			case refreshed <- struct{}{}:
			default:
			}
			return nil
		},
		resetTokenCache: func() {},
		refreshInterval: time.Millisecond,
	}
	credentials.Start(context.Background())
	t.Cleanup(credentials.Close)

	select {
	case <-refreshed:
	case <-time.After(time.Second):
		t.Fatal("credential maintenance did not refresh the access token")
	}
	if err := credentials.Ready(context.Background()); err != nil {
		t.Fatalf("Ready() error = %v", err)
	}
}

type recordingRunner struct {
	called bool
}

func (r *recordingRunner) Run(context.Context, executor.Invocation) (executor.Result, error) {
	r.called = true
	return executor.Result{}, nil
}
