// Copyright 2026 Alibaba Group
// Licensed under the Apache License, Version 2.0 (the "License");

package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	authpkg "github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/auth"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/commandservice"
	apperrors "github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/errors"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/executor"
)

const serviceCredentialRefreshInterval = time.Minute

type fixedProfileCredentials struct {
	profile         string
	load            func() (*authpkg.TokenData, error)
	refresh         func(context.Context) error
	resetTokenCache func()

	ensureMu sync.Mutex
}

func newFixedProfileCredentials(configDir, profile string) *fixedProfileCredentials {
	profile = strings.TrimSpace(profile)
	return &fixedProfileCredentials{
		profile: profile,
		load: func() (*authpkg.TokenData, error) {
			return authpkg.LoadTokenDataKeychainForCorpID(profile)
		},
		refresh: func(ctx context.Context) error {
			provider := authpkg.NewOAuthProvider(configDir, nil)
			configureOAuthProviderCompatibility(provider, configDir)
			token, err := provider.GetAccessToken(ctx)
			if err != nil {
				return err
			}
			if strings.TrimSpace(token) == "" {
				return fmt.Errorf("token refresh returned an empty access token")
			}
			return nil
		},
		resetTokenCache: ResetRuntimeTokenCache,
	}
}

func (c *fixedProfileCredentials) Ensure(ctx context.Context) error {
	c.ensureMu.Lock()
	defer c.ensureMu.Unlock()

	tokenData, err := c.loadAndValidate()
	if err != nil {
		return err
	}
	if !tokenData.IsAccessTokenValid() {
		if !tokenData.IsRefreshTokenValid() {
			return fmt.Errorf("fixed DWS service profile credentials are expired")
		}
		if err := c.refresh(ctx); err != nil {
			return fmt.Errorf("refresh fixed DWS service profile: %w", err)
		}
		tokenData, err = c.loadAndValidate()
		if err != nil {
			return err
		}
		if !tokenData.IsAccessTokenValid() {
			return fmt.Errorf("fixed DWS service profile has no valid access token after refresh")
		}
	}
	c.resetTokenCache()
	return nil
}

func (c *fixedProfileCredentials) Ready(context.Context) error {
	tokenData, err := c.loadAndValidate()
	if err != nil {
		return err
	}
	if !tokenData.IsAccessTokenValid() {
		return fmt.Errorf("fixed DWS service profile access token is not valid")
	}
	return nil
}

func (c *fixedProfileCredentials) loadAndValidate() (*authpkg.TokenData, error) {
	tokenData, err := c.load()
	if err != nil {
		return nil, fmt.Errorf("load fixed DWS service profile: %w", err)
	}
	if tokenData == nil || strings.TrimSpace(tokenData.CorpID) == "" {
		return nil, fmt.Errorf("fixed DWS service profile is unavailable")
	}
	if strings.TrimSpace(tokenData.CorpID) != c.profile {
		return nil, fmt.Errorf("fixed DWS service profile identity changed")
	}
	return tokenData, nil
}

type profileCredentialRunner struct {
	defaultProfile  string
	resolveProfile  func(string) (string, error)
	ensureProfile   func(context.Context, string) error
	next            executor.Runner
	refreshInterval time.Duration

	executionMu sync.Mutex
	lifecycleMu sync.Mutex
	started     bool
	closed      bool
	cancel      context.CancelFunc
	done        chan struct{}
}

func (r *profileCredentialRunner) Run(ctx context.Context, invocation executor.Invocation) (executor.Result, error) {
	return r.RunWithProfile(ctx, "", invocation)
}

func (r *profileCredentialRunner) RunWithProfile(ctx context.Context, selector string, invocation executor.Invocation) (executor.Result, error) {
	r.executionMu.Lock()
	defer r.executionMu.Unlock()
	if invocation.DryRun {
		return r.next.Run(ctx, invocation)
	}

	selector = strings.TrimSpace(selector)
	if selector == "" {
		selector = strings.TrimSpace(r.defaultProfile)
	}
	resolved, err := r.resolveProfile(selector)
	if err != nil {
		if errors.Is(err, authpkg.ErrProfileNotFound) || errors.Is(err, authpkg.ErrProfileAmbiguous) {
			return executor.Result{}, &commandservice.Error{
				Code:    commandservice.CodeInvalidArguments,
				Message: err.Error(),
				Cause:   err,
			}
		}
		return executor.Result{}, fmt.Errorf("resolve DWS service profile %q: %w", selector, err)
	}
	profile := strings.TrimSpace(resolved)
	if profile == "" {
		return executor.Result{}, fmt.Errorf("resolved DWS service profile has no corpId")
	}

	previousProfile := authpkg.RuntimeProfile()
	authpkg.SetRuntimeProfile(profile)
	defer authpkg.SetRuntimeProfile(previousProfile)

	if err := r.ensureProfile(ctx, profile); err != nil {
		return executor.Result{}, apperrors.NewAuth(
			"DWS service identity is unavailable",
			apperrors.WithReason("profile_unavailable"),
			apperrors.WithCause(err),
		)
	}
	return r.next.Run(ctx, invocation)
}

func (r *profileCredentialRunner) Start(parent context.Context) {
	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	if r.started || r.closed {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	r.cancel = cancel
	r.done = make(chan struct{})
	r.started = true
	go r.maintain(ctx)
}

func (r *profileCredentialRunner) Close() {
	r.lifecycleMu.Lock()
	r.closed = true
	cancel := r.cancel
	done := r.done
	r.lifecycleMu.Unlock()
	if cancel == nil || done == nil {
		return
	}
	cancel()
	<-done
}

func (r *profileCredentialRunner) maintain(ctx context.Context) {
	defer close(r.done)
	interval := r.refreshInterval
	if interval <= 0 {
		interval = serviceCredentialRefreshInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.maintainDefaultProfile(ctx)
		}
	}
}

func (r *profileCredentialRunner) maintainDefaultProfile(ctx context.Context) {
	r.executionMu.Lock()
	defer r.executionMu.Unlock()

	profile := strings.TrimSpace(r.defaultProfile)
	previousProfile := authpkg.RuntimeProfile()
	authpkg.SetRuntimeProfile(profile)
	defer authpkg.SetRuntimeProfile(previousProfile)

	if err := r.ensureProfile(ctx, profile); err != nil {
		slog.Warn("DWS service credential maintenance failed",
			"category", "auth",
			"reason", "default_profile_unavailable",
		)
	}
}
