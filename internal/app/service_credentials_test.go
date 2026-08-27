// Copyright 2026 Alibaba Group
// Licensed under the Apache License, Version 2.0 (the "License");

package app

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	authpkg "github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/auth"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/commandservice"
	apperrors "github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/errors"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/executor"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/keychain"
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

func TestFixedProfileCredentialsLoadsCorpSlotWithoutRegistry(t *testing.T) {
	t.Setenv(keychain.DisableKeychainEnv, "1")
	configDir := t.TempDir()
	if err := authpkg.SaveTokenDataKeychainForCorpID("corp-fixed", &authpkg.TokenData{
		AccessToken:  "access",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(time.Hour),
		RefreshExpAt: time.Now().Add(24 * time.Hour),
		CorpID:       "corp-fixed",
	}); err != nil {
		t.Fatalf("SaveTokenDataKeychainForCorpID() error = %v", err)
	}
	if err := os.WriteFile(authpkg.ProfilesPath(configDir), []byte("{"), 0o600); err != nil {
		t.Fatalf("WriteFile(profiles.json) error = %v", err)
	}

	credentials := newFixedProfileCredentials(configDir, "corp-fixed")
	if err := credentials.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure() error = %v, want direct corp-slot load", err)
	}
}

func TestProfileCredentialRunnerStopsBeforeCommand(t *testing.T) {
	next := &recordingRunner{}
	runner := &profileCredentialRunner{
		defaultProfile: "ding-test",
		resolveProfile: func(selector string) (string, error) {
			if selector != "ding-test" {
				t.Fatalf("default selector = %q, want ding-test", selector)
			}
			return "ding-test", nil
		},
		ensureProfile: func(context.Context, string) error {
			return errors.New("unavailable")
		},
		next: next,
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

func TestProfileCredentialRunnerPreservesDryRunBarrier(t *testing.T) {
	next := &recordingRunner{}
	runner := &profileCredentialRunner{
		defaultProfile: "ding-test",
		resolveProfile: func(string) (string, error) {
			return "", errors.New("must not resolve credentials for dry-run")
		},
		ensureProfile: func(context.Context, string) error {
			return errors.New("must not load credentials")
		},
		next: next,
	}

	if _, err := runner.RunWithProfile(context.Background(), "unknown", executor.Invocation{DryRun: true}); err != nil {
		t.Fatalf("RunWithProfile() error = %v", err)
	}
	if !next.called {
		t.Fatal("dry-run invocation did not reach the local runner")
	}
}

func TestProfileCredentialRunnerSelectsAndRestoresProfile(t *testing.T) {
	previousProfile := authpkg.RuntimeProfile()
	authpkg.SetRuntimeProfile("ding-default")
	t.Cleanup(func() {
		authpkg.SetRuntimeProfile(previousProfile)
	})

	next := &runtimeProfileRunner{}
	ensuredProfile := ""
	runner := &profileCredentialRunner{
		defaultProfile: "ding-default",
		resolveProfile: func(selector string) (string, error) {
			if selector != "Alibaba" {
				t.Fatalf("profile selector = %q, want Alibaba", selector)
			}
			return "ding-alibaba", nil
		},
		ensureProfile: func(_ context.Context, profile string) error {
			ensuredProfile = profile
			if got := authpkg.RuntimeProfile(); got != "ding-alibaba" {
				t.Fatalf("runtime profile during credential check = %q, want ding-alibaba", got)
			}
			return nil
		},
		next: next,
	}

	if _, err := runner.RunWithProfile(context.Background(), "Alibaba", executor.Invocation{}); err != nil {
		t.Fatalf("RunWithProfile() error = %v", err)
	}
	if ensuredProfile != "ding-alibaba" {
		t.Fatalf("ensured profile = %q, want ding-alibaba", ensuredProfile)
	}
	if len(next.profiles) != 1 || next.profiles[0] != "ding-alibaba" {
		t.Fatalf("command runtime profiles = %#v, want [ding-alibaba]", next.profiles)
	}
	if got := authpkg.RuntimeProfile(); got != "ding-default" {
		t.Fatalf("runtime profile after command = %q, want ding-default", got)
	}
}

func TestProfileCredentialRunnerClassifiesSelectorErrors(t *testing.T) {
	t.Run("default profile registry failure is internal", func(t *testing.T) {
		next := &recordingRunner{}
		ensureCalled := false
		runner := &profileCredentialRunner{
			defaultProfile: "ding-default",
			resolveProfile: func(selector string) (string, error) {
				if selector != "ding-default" {
					t.Fatalf("default selector = %q, want ding-default", selector)
				}
				return "", errors.New("parse profiles")
			},
			ensureProfile: func(context.Context, string) error {
				ensureCalled = true
				return nil
			},
			next: next,
		}

		_, err := runner.Run(context.Background(), executor.Invocation{})
		var authErr *apperrors.Error
		if err == nil || errors.As(err, &authErr) {
			t.Fatalf("Run() error = %#v, want non-auth internal error", err)
		}
		if ensureCalled || next.called {
			t.Fatal("credentials or command ran after default profile registry failure")
		}
	})

	t.Run("unknown profile is invalid arguments", func(t *testing.T) {
		next := &recordingRunner{}
		runner := &profileCredentialRunner{
			defaultProfile: "ding-default",
			resolveProfile: func(string) (string, error) {
				return "", authpkg.ErrProfileNotFound
			},
			ensureProfile: func(context.Context, string) error { return nil },
			next:          next,
		}

		_, err := runner.RunWithProfile(context.Background(), "missing", executor.Invocation{})
		var serviceErr *commandservice.Error
		if !errors.As(err, &serviceErr) || serviceErr.Code != commandservice.CodeInvalidArguments {
			t.Fatalf("RunWithProfile() error = %#v, want invalid_arguments", err)
		}
		if next.called {
			t.Fatal("command ran for an unknown profile")
		}
	})

	t.Run("registry failure is not an auth outage", func(t *testing.T) {
		runner := &profileCredentialRunner{
			defaultProfile: "ding-default",
			resolveProfile: func(string) (string, error) {
				return "", errors.New("parse profiles")
			},
			ensureProfile: func(context.Context, string) error { return nil },
			next:          &recordingRunner{},
		}

		_, err := runner.RunWithProfile(context.Background(), "Alibaba", executor.Invocation{})
		var authErr *apperrors.Error
		if err == nil || errors.As(err, &authErr) {
			t.Fatalf("RunWithProfile() error = %#v, want non-auth internal error", err)
		}
	})
}

func TestProfileCredentialRunnerMaintainsDefaultProfile(t *testing.T) {
	previousProfile := authpkg.RuntimeProfile()
	authpkg.SetRuntimeProfile("outside")
	t.Cleanup(func() {
		authpkg.SetRuntimeProfile(previousProfile)
	})

	maintained := make(chan string, 1)
	runner := &profileCredentialRunner{
		defaultProfile: "ding-default",
		resolveProfile: func(string) (string, error) {
			return "", errors.New("must not resolve the default profile")
		},
		ensureProfile: func(_ context.Context, profile string) error {
			if runtimeProfile := authpkg.RuntimeProfile(); runtimeProfile != profile {
				return errors.New("credential maintenance used the wrong runtime profile")
			}
			select {
			case maintained <- profile:
			default:
			}
			return nil
		},
		next:            &runtimeProfileRunner{},
		refreshInterval: time.Millisecond,
	}
	runner.Start(context.Background())
	t.Cleanup(runner.Close)

	select {
	case profile := <-maintained:
		if profile != "ding-default" {
			t.Fatalf("maintained profile = %q, want ding-default", profile)
		}
	case <-time.After(time.Second):
		t.Fatal("default profile credential maintenance did not run")
	}
	runner.executionMu.Lock()
	runner.executionMu.Unlock()
	if got := authpkg.RuntimeProfile(); got != "outside" {
		t.Fatalf("runtime profile after maintenance = %q, want outside", got)
	}
}

func TestProfileCredentialRunnerDoesNotStartAfterClose(t *testing.T) {
	maintained := make(chan struct{}, 1)
	runner := &profileCredentialRunner{
		defaultProfile: "ding-default",
		resolveProfile: func(string) (string, error) {
			return "", errors.New("must not resolve the default profile")
		},
		ensureProfile: func(context.Context, string) error {
			maintained <- struct{}{}
			return nil
		},
		next:            &runtimeProfileRunner{},
		refreshInterval: time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	runner.Close()
	runner.Start(ctx)
	select {
	case <-maintained:
		t.Fatal("credential maintenance started after the runner was closed")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestProfileCredentialRunnerSerializesProfileSelection(t *testing.T) {
	previousProfile := authpkg.RuntimeProfile()
	authpkg.SetRuntimeProfile("ding-default")
	t.Cleanup(func() {
		authpkg.SetRuntimeProfile(previousProfile)
	})

	firstEntered := make(chan struct{})
	secondEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	releaseSecond := make(chan struct{})
	firstResult := make(chan error, 1)
	secondResult := make(chan error, 1)
	defer func() {
		select {
		case <-releaseFirst:
		default:
			close(releaseFirst)
		}
		select {
		case <-releaseSecond:
		default:
			close(releaseSecond)
		}
	}()

	runner := &profileCredentialRunner{
		defaultProfile: "ding-default",
		resolveProfile: func(selector string) (string, error) {
			switch selector {
			case "first":
				return "ding-first", nil
			case "second":
				return "ding-second", nil
			default:
				return "", errors.New("unknown test profile")
			}
		},
		ensureProfile: func(context.Context, string) error {
			return nil
		},
		next: &blockingRuntimeProfileRunner{
			firstEntered:  firstEntered,
			secondEntered: secondEntered,
			releaseFirst:  releaseFirst,
			releaseSecond: releaseSecond,
		},
	}

	go func() {
		_, err := runner.RunWithProfile(context.Background(), "first", executor.Invocation{})
		firstResult <- err
	}()
	<-firstEntered
	go func() {
		_, err := runner.RunWithProfile(context.Background(), "second", executor.Invocation{})
		secondResult <- err
	}()

	select {
	case <-secondEntered:
		t.Error("second profile entered while the first profile was still active")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstResult; err != nil {
		t.Fatalf("first RunWithProfile() error = %v", err)
	}
	select {
	case <-secondEntered:
	case <-time.After(time.Second):
		t.Fatal("second profile did not enter after the first profile completed")
	}
	close(releaseSecond)
	if err := <-secondResult; err != nil {
		t.Fatalf("second RunWithProfile() error = %v", err)
	}
}

type recordingRunner struct {
	called bool
}

func (r *recordingRunner) Run(context.Context, executor.Invocation) (executor.Result, error) {
	r.called = true
	return executor.Result{}, nil
}

type runtimeProfileRunner struct {
	profiles []string
}

type blockingRuntimeProfileRunner struct {
	firstEntered  chan struct{}
	secondEntered chan struct{}
	releaseFirst  chan struct{}
	releaseSecond chan struct{}
}

func (r *blockingRuntimeProfileRunner) Run(_ context.Context, invocation executor.Invocation) (executor.Result, error) {
	switch authpkg.RuntimeProfile() {
	case "ding-first":
		close(r.firstEntered)
		<-r.releaseFirst
	case "ding-second":
		close(r.secondEntered)
		<-r.releaseSecond
	}
	return executor.Result{Invocation: invocation}, nil
}

func (r *runtimeProfileRunner) Run(_ context.Context, invocation executor.Invocation) (executor.Result, error) {
	r.profiles = append(r.profiles, authpkg.RuntimeProfile())
	return executor.Result{Invocation: invocation}, nil
}
