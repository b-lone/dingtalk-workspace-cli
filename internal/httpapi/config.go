// Copyright 2026 Alibaba Group
// Licensed under the Apache License, Version 2.0 (the "License");

package httpapi

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultListenAddress  = ":8080"
	defaultCommandTimeout = 30 * time.Second
	defaultMaxBodyBytes   = int64(1 << 20)
	defaultMaxOutputBytes = int64(16 << 20)
	maxMaxBodyBytes       = int64(16 << 20)
	maxMaxOutputBytes     = int64(64 << 20)
)

type Config struct {
	ListenAddress  string
	Profile        string
	WrapperPath    string
	Token          []byte
	CommandTimeout time.Duration
	MaxBodyBytes   int64
	MaxOutputBytes int64
}

func LoadConfigFromEnvironment() (Config, error) {
	listenAddress := strings.TrimSpace(os.Getenv("DWS_SERVICE_LISTEN_ADDR"))
	if listenAddress == "" {
		listenAddress = defaultListenAddress
	}
	if err := validateListenAddress(listenAddress); err != nil {
		return Config{}, err
	}
	profile, err := readSecretFile("DWS_SERVICE_PROFILE_FILE")
	if err != nil {
		return Config{}, err
	}
	token, err := readSecretFile("DWS_SERVICE_TOKEN_FILE")
	if err != nil {
		return Config{}, err
	}
	if len(token) < 32 {
		return Config{}, fmt.Errorf("DWS service token must contain at least 32 bytes")
	}
	wrapperPath, err := executableEnvironment("DWS_SERVICE_WRAPPER_PATH")
	if err != nil {
		return Config{}, err
	}
	commandTimeout, err := durationEnvironment("DWS_SERVICE_COMMAND_TIMEOUT", defaultCommandTimeout)
	if err != nil {
		return Config{}, err
	}
	maxBodyBytes, err := int64Environment("DWS_SERVICE_MAX_BODY_BYTES", defaultMaxBodyBytes)
	if err != nil {
		return Config{}, err
	}
	if maxBodyBytes > maxMaxBodyBytes {
		return Config{}, fmt.Errorf("DWS_SERVICE_MAX_BODY_BYTES must not exceed %d", maxMaxBodyBytes)
	}
	maxOutputBytes, err := int64Environment("DWS_SERVICE_MAX_OUTPUT_BYTES", defaultMaxOutputBytes)
	if err != nil {
		return Config{}, err
	}
	if maxOutputBytes > maxMaxOutputBytes {
		return Config{}, fmt.Errorf("DWS_SERVICE_MAX_OUTPUT_BYTES must not exceed %d", maxMaxOutputBytes)
	}
	return Config{
		ListenAddress:  listenAddress,
		Profile:        string(profile),
		WrapperPath:    wrapperPath,
		Token:          token,
		CommandTimeout: commandTimeout,
		MaxBodyBytes:   maxBodyBytes,
		MaxOutputBytes: maxOutputBytes,
	}, nil
}

func executableEnvironment(name string) (string, error) {
	path := strings.TrimSpace(os.Getenv(name))
	if path == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%s must be an absolute path", name)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", name, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("%s must reference an executable regular file", name)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return "", fmt.Errorf("%s must not be writable by group or others", name)
	}
	return path, nil
}

func validateListenAddress(address string) error {
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid DWS_SERVICE_LISTEN_ADDR: %w", err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("invalid DWS_SERVICE_LISTEN_ADDR port %q", port)
	}
	return nil
}

func readSecretFile(environmentName string) ([]byte, error) {
	path := strings.TrimSpace(os.Getenv(environmentName))
	if path == "" {
		return nil, fmt.Errorf("%s is required", environmentName)
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%s must be an absolute path", environmentName)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", environmentName, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must reference a regular file", environmentName)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s must not be readable or writable by group or others", environmentName)
	}
	value, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", environmentName, err)
	}
	value = []byte(strings.TrimSpace(string(value)))
	if len(value) == 0 {
		return nil, fmt.Errorf("%s is empty", environmentName)
	}
	return value, nil
}

func durationEnvironment(name string, defaultValue time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return defaultValue, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return value, nil
}

func int64Environment(name string, defaultValue int64) (int64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return defaultValue, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return value, nil
}
