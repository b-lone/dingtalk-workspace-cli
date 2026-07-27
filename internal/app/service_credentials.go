// Copyright 2026 Alibaba Group
// Licensed under the Apache License, Version 2.0 (the "License");

package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	authpkg "github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/auth"
	apperrors "github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/errors"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/executor"
)

const serviceCredentialRefreshInterval = time.Minute

type fixedProfileCredentials struct {
	profile         string
	load            func() (*authpkg.TokenData, error)
	refresh         func(context.Context) error
	resetTokenCache func()
	refreshInterval time.Duration

	ensureMu  sync.Mutex
	startOnce sync.Once
	closeOnce sync.Once
	cancel    context.CancelFunc
	done      chan struct{}
}

func newFixedProfileCredentials(configDir, profile string) *fixedProfileCredentials {
	return &fixedProfileCredentials{
		profile: strings.TrimSpace(profile),
		load: func() (*authpkg.TokenData, error) {
			return authpkg.LoadTokenDataForProfileReadOnly(configDir, profile)
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
		refreshInterval: serviceCredentialRefreshInterval,
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

func (c *fixedProfileCredentials) Start(parent context.Context) {
	c.startOnce.Do(func() {
		ctx, cancel := context.WithCancel(parent)
		c.cancel = cancel
		c.done = make(chan struct{})
		go c.maintain(ctx)
	})
}

func (c *fixedProfileCredentials) Close() {
	c.closeOnce.Do(func() {
		if c.cancel == nil {
			return
		}
		c.cancel()
		<-c.done
	})
}

func (c *fixedProfileCredentials) maintain(ctx context.Context) {
	defer close(c.done)
	ticker := time.NewTicker(c.refreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.Ensure(ctx); err != nil {
				slog.Warn("DWS service credential maintenance failed",
					"category", "auth",
					"reason", "fixed_profile_unavailable",
				)
			}
		}
	}
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

type fixedProfileCredentialRunner struct {
	credentials *fixedProfileCredentials
	next        executor.Runner
}

func (r *fixedProfileCredentialRunner) Run(ctx context.Context, invocation executor.Invocation) (executor.Result, error) {
	if invocation.DryRun {
		return r.next.Run(ctx, invocation)
	}
	if err := r.credentials.Ensure(ctx); err != nil {
		return executor.Result{}, apperrors.NewAuth(
			"DWS service identity is unavailable",
			apperrors.WithReason("fixed_profile_unavailable"),
			apperrors.WithCause(err),
		)
	}
	return r.next.Run(ctx, invocation)
}
