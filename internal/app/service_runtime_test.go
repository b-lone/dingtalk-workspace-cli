// Copyright 2026 Alibaba Group
// Licensed under the Apache License, Version 2.0 (the "License");

package app

import (
	"testing"
	"time"

	authpkg "github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/auth"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/cli"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/commandservice"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/executor"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/keychain"
)

func TestHTTPCommandServiceSurfaceUsesEmbeddedSchemaAndStaticEndpoints(t *testing.T) {
	injectStaticServers()
	schema, err := cli.LoadEmbeddedSchemaCatalog()
	if err != nil {
		t.Fatal(err)
	}
	service, err := commandservice.New(commandservice.Options{
		Version:     "test",
		CatalogHash: schema.CatalogHash,
		SurfaceHash: schema.SurfaceHash,
		Index:       schema.Index,
		Runner:      executor.EchoRunner{},
		Allow: func(tool cli.ToolSpec) bool {
			ref := tool.Interface.Ref
			if ref == nil {
				return false
			}
			_, ok := directRuntimeEndpoint(ref.ProductID, ref.RPCName)
			return ok
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if service.Metadata().CommandCount < 100 {
		t.Fatalf("HTTP command count = %d, want at least 100", service.Metadata().CommandCount)
	}
	if _, err := service.Command("contact.get_current_user_profile"); err != nil {
		t.Fatalf("current-user read command is not exposed: %v", err)
	}
}

func TestLoadHTTPServiceProfileRejectsMismatchedTokenIdentity(t *testing.T) {
	t.Setenv(keychain.DisableKeychainEnv, "1")
	configDir := t.TempDir()
	if err := authpkg.SaveProfiles(configDir, &authpkg.ProfilesConfig{
		Version:        1,
		PrimaryProfile: "corp-registered",
		CurrentProfile: "corp-registered",
		Profiles: []authpkg.Profile{
			{Name: "Alibaba", CorpID: "corp-registered", CorpName: "阿里巴巴"},
		},
	}); err != nil {
		t.Fatalf("SaveProfiles() error = %v", err)
	}
	if err := authpkg.SaveTokenDataKeychainForCorpID("corp-registered", &authpkg.TokenData{
		AccessToken:  "access",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(time.Hour),
		RefreshExpAt: time.Now().Add(24 * time.Hour),
		CorpID:       "corp-other",
	}); err != nil {
		t.Fatalf("SaveTokenDataKeychainForCorpID() error = %v", err)
	}

	if _, err := loadHTTPServiceProfile(configDir, "Alibaba"); err == nil {
		t.Fatal("loadHTTPServiceProfile() error = nil, want identity mismatch")
	}
}
