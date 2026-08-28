// Copyright 2026 Alibaba Group
// Licensed under the Apache License, Version 2.0 (the "License");

package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/cli"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/commandservice"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/hostcli"
)

type HostHTTPCommandServiceOptions struct {
	WrapperPath    string
	Profile        string
	CommandTimeout time.Duration
	MaxOutputBytes int64
}

func NewHostHTTPCommandService(ctx context.Context, options HostHTTPCommandServiceOptions) (*commandservice.Service, error) {
	runner, err := hostcli.NewRunner(hostcli.Options{
		WrapperPath:    options.WrapperPath,
		DefaultProfile: options.Profile,
		CommandTimeout: options.CommandTimeout,
		MaxOutputBytes: options.MaxOutputBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize trusted DWS host runner: %w", err)
	}
	schema, err := cli.LoadEmbeddedSchemaCatalog()
	if err != nil {
		return nil, fmt.Errorf("load embedded DWS Schema: %w", err)
	}
	service, err := commandservice.New(commandservice.Options{
		Version:     Version(),
		CatalogHash: schema.CatalogHash,
		SurfaceHash: schema.SurfaceHash,
		Index:       schema.Index,
		Runner:      runner,
		Ready:       runner.Ready,
		Allow: func(tool cli.ToolSpec) bool {
			return hostcli.Supports(strings.TrimSpace(tool.Identity.CanonicalPath))
		},
	})
	if err != nil {
		return nil, err
	}
	if err := service.Ready(ctx); err != nil {
		_ = service.Close()
		return nil, fmt.Errorf("trusted DWS host service is not ready: %w", err)
	}
	return service, nil
}
