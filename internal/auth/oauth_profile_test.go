// Copyright 2026 Alibaba Group
// Licensed under the Apache License, Version 2.0 (the "License");

package auth

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type profileRefreshRoundTripFunc func(*http.Request) (*http.Response, error)

func (f profileRefreshRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestGetAccessTokenForCorpIDRefreshesWithoutProfileRegistry(t *testing.T) {
	cleanupKeychain(t)
	configDir := t.TempDir()
	const corpID = "corp-fixed"
	const corruptRegistry = "{not-json"

	SetClientID("profile-client-id")
	SetClientSecret("profile-client-secret")
	resetClientIDFromMCP()
	t.Cleanup(func() {
		SetClientID("")
		SetClientSecret("")
		resetClientIDFromMCP()
	})

	if err := SaveTokenDataKeychainForCorpID(corpID, &TokenData{
		AccessToken:  "expired-access",
		RefreshToken: "valid-refresh",
		ExpiresAt:    time.Now().Add(-time.Hour),
		RefreshExpAt: time.Now().Add(time.Hour),
		CorpID:       corpID,
		ClientID:     "profile-client-id",
	}); err != nil {
		t.Fatalf("SaveTokenDataKeychainForCorpID() error = %v", err)
	}
	registryPath := filepath.Join(configDir, profilesJSONFile)
	if err := os.WriteFile(registryPath, []byte(corruptRegistry), 0o600); err != nil {
		t.Fatalf("WriteFile(profiles.json) error = %v", err)
	}

	var calls atomic.Int32
	provider := NewOAuthProvider(configDir, nil)
	provider.httpClient = &http.Client{Transport: profileRefreshRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"accessToken":"fresh-access","refreshToken":"fresh-refresh","expiresIn":7200}`,
			)),
		}, nil
	})}

	accessToken, err := provider.GetAccessTokenForCorpID(context.Background(), corpID)
	if err != nil {
		t.Fatalf("GetAccessTokenForCorpID() error = %v", err)
	}
	if accessToken != "fresh-access" {
		t.Fatalf("access token = %q, want fresh-access", accessToken)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("refresh HTTP calls = %d, want 1", got)
	}
	stored, err := LoadTokenDataKeychainForCorpID(corpID)
	if err != nil {
		t.Fatalf("LoadTokenDataKeychainForCorpID() error = %v", err)
	}
	if stored.AccessToken != "fresh-access" || stored.CorpID != corpID {
		t.Fatalf("stored token = %#v", stored)
	}
	registry, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatalf("ReadFile(profiles.json) error = %v", err)
	}
	if string(registry) != corruptRegistry {
		t.Fatalf("profiles.json = %q, want unchanged %q", registry, corruptRegistry)
	}
}
