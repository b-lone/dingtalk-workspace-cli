// Copyright 2026 Alibaba Group
// Licensed under the Apache License, Version 2.0 (the "License");

package httpapi

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadConfigFromEnvironmentReadsSecretFiles(t *testing.T) {
	directory := t.TempDir()
	profilePath := filepath.Join(directory, "profile")
	tokenPath := filepath.Join(directory, "token")
	wrapperPath := filepath.Join(directory, "dws")
	if err := os.WriteFile(profilePath, []byte("fixed-profile\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte(testBearerToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wrapperPath, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DWS_SERVICE_PROFILE_FILE", profilePath)
	t.Setenv("DWS_SERVICE_TOKEN_FILE", tokenPath)
	t.Setenv("DWS_SERVICE_WRAPPER_PATH", wrapperPath)
	t.Setenv("DWS_SERVICE_LISTEN_ADDR", "127.0.0.1:18080")
	t.Setenv("DWS_SERVICE_COMMAND_TIMEOUT", "45s")
	t.Setenv("DWS_SERVICE_MAX_BODY_BYTES", "2048")
	t.Setenv("DWS_SERVICE_MAX_OUTPUT_BYTES", "4096")

	config, err := LoadConfigFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if config.Profile != "fixed-profile" ||
		string(config.Token) != testBearerToken ||
		config.ListenAddress != "127.0.0.1:18080" ||
		config.CommandTimeout != 45*time.Second ||
		config.MaxBodyBytes != 2048 ||
		config.MaxOutputBytes != 4096 ||
		config.WrapperPath != wrapperPath {
		t.Fatalf("config = %#v", config)
	}
}

func TestLoadConfigFromEnvironmentRejectsUnsafeWrapper(t *testing.T) {
	directory := t.TempDir()
	profilePath := filepath.Join(directory, "profile")
	tokenPath := filepath.Join(directory, "token")
	wrapperPath := filepath.Join(directory, "dws")
	if err := os.WriteFile(profilePath, []byte("fixed-profile"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte(testBearerToken), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wrapperPath, []byte("#!/bin/sh\n"), 0o722); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(wrapperPath, 0o722); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DWS_SERVICE_PROFILE_FILE", profilePath)
	t.Setenv("DWS_SERVICE_TOKEN_FILE", tokenPath)
	t.Setenv("DWS_SERVICE_WRAPPER_PATH", wrapperPath)

	if _, err := LoadConfigFromEnvironment(); err == nil {
		t.Fatal("LoadConfigFromEnvironment() accepted a writable wrapper")
	}
}

func TestLoadConfigFromEnvironmentRejectsBroadSecretPermissions(t *testing.T) {
	directory := t.TempDir()
	profilePath := filepath.Join(directory, "profile")
	tokenPath := filepath.Join(directory, "token")
	wrapperPath := filepath.Join(directory, "dws")
	if err := os.WriteFile(profilePath, []byte("fixed-profile"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte(testBearerToken), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wrapperPath, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DWS_SERVICE_PROFILE_FILE", profilePath)
	t.Setenv("DWS_SERVICE_TOKEN_FILE", tokenPath)
	t.Setenv("DWS_SERVICE_WRAPPER_PATH", wrapperPath)

	if _, err := LoadConfigFromEnvironment(); err == nil {
		t.Fatal("LoadConfigFromEnvironment() accepted a group-readable profile file")
	}
}
