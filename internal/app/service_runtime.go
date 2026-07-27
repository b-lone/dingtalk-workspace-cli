// Copyright 2026 Alibaba Group
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package app

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	authpkg "github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/auth"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/cli"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/commandservice"
)

// NewHTTPCommandService builds the fixed-profile, plugin-free runtime used by
// dwsd. The selected profile is resolved once and pinned to its immutable
// corpId for the lifetime of the process.
func NewHTTPCommandService(ctx context.Context, profile string, timeout time.Duration) (*commandservice.Service, error) {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return nil, fmt.Errorf("DWS service profile is required")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("DWS service command timeout must be positive")
	}
	tokenData, err := authpkg.LoadTokenDataForProfileReadOnly(defaultConfigDir(), profile)
	if err != nil {
		return nil, fmt.Errorf("load fixed DWS service profile: %w", err)
	}
	if tokenData == nil || strings.TrimSpace(tokenData.CorpID) == "" {
		return nil, fmt.Errorf("fixed DWS service profile has no corpId")
	}
	if !tokenData.IsAccessTokenValid() && !tokenData.IsRefreshTokenValid() {
		return nil, fmt.Errorf("fixed DWS service profile credentials are expired")
	}
	pinnedProfile := strings.TrimSpace(tokenData.CorpID)
	authpkg.SetRuntimeProfile(pinnedProfile)
	credentials := newFixedProfileCredentials(defaultConfigDir(), pinnedProfile)
	if err := credentials.Ensure(ctx); err != nil {
		return nil, fmt.Errorf("initialize fixed DWS service profile: %w", err)
	}

	configureLogLevel(&GlobalFlags{})
	injectStaticServers()
	schema, err := cli.LoadEmbeddedSchemaCatalog()
	if err != nil {
		CloseFileLogger()
		return nil, fmt.Errorf("load embedded DWS Schema: %w", err)
	}
	flags := &GlobalFlags{
		Format:  "json",
		Profile: pinnedProfile,
		Timeout: int(math.Ceil(timeout.Seconds())),
	}
	runner := newCommandRunnerWithFlags(cli.NewEnvironmentLoader(), flags)
	service, err := commandservice.New(commandservice.Options{
		Version:     Version(),
		CatalogHash: schema.CatalogHash,
		SurfaceHash: schema.SurfaceHash,
		Index:       schema.Index,
		Runner: &fixedProfileCredentialRunner{
			credentials: credentials,
			next:        runner,
		},
		Ready: credentials.Ready,
		Close: func() error {
			credentials.Close()
			StopAllStdioClients()
			CloseAuditSink()
			CloseFileLogger()
			return nil
		},
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
		CloseFileLogger()
		return nil, err
	}
	if err := service.Ready(ctx); err != nil {
		_ = service.Close()
		return nil, fmt.Errorf("DWS service is not ready: %w", err)
	}
	credentials.Start(ctx)
	return service, nil
}
