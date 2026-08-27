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

type httpServiceProfile struct {
	corpID    string
	tokenData *authpkg.TokenData
}

func loadHTTPServiceProfile(configDir, selector string) (httpServiceProfile, error) {
	registered, err := authpkg.ResolveProfileReadOnly(configDir, selector)
	if err != nil {
		return httpServiceProfile{}, err
	}
	if registered == nil || strings.TrimSpace(registered.CorpID) == "" {
		return httpServiceProfile{}, fmt.Errorf("DWS service profile has no registered corpId")
	}
	corpID := strings.TrimSpace(registered.CorpID)
	tokenData, err := authpkg.LoadTokenDataKeychainForCorpID(corpID)
	if err != nil {
		return httpServiceProfile{}, err
	}
	if tokenData == nil || strings.TrimSpace(tokenData.CorpID) == "" {
		return httpServiceProfile{}, fmt.Errorf("DWS service profile token has no corpId")
	}
	if strings.TrimSpace(tokenData.CorpID) != corpID {
		return httpServiceProfile{}, fmt.Errorf("DWS service profile token identity does not match registered corpId")
	}
	return httpServiceProfile{corpID: corpID, tokenData: tokenData}, nil
}

// NewHTTPCommandService builds the plugin-free runtime used by dwsd. The
// startup profile remains the default identity while individual execute
// requests may select another registered profile.
func NewHTTPCommandService(ctx context.Context, profile string, timeout time.Duration) (*commandservice.Service, error) {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return nil, fmt.Errorf("DWS service profile is required")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("DWS service command timeout must be positive")
	}
	configDir := defaultConfigDir()
	loadedProfile, err := loadHTTPServiceProfile(configDir, profile)
	if err != nil {
		return nil, fmt.Errorf("load fixed DWS service profile: %w", err)
	}
	tokenData := loadedProfile.tokenData
	if tokenData == nil || strings.TrimSpace(tokenData.CorpID) == "" {
		return nil, fmt.Errorf("fixed DWS service profile has no corpId")
	}
	if !tokenData.IsAccessTokenValid() && !tokenData.IsRefreshTokenValid() {
		return nil, fmt.Errorf("fixed DWS service profile credentials are expired")
	}
	pinnedProfile := loadedProfile.corpID
	authpkg.SetRuntimeProfile(pinnedProfile)
	credentials := newFixedProfileCredentials(configDir, pinnedProfile)
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
	commandRunner := newHTTPCommandRunnerWithFlags(cli.NewEnvironmentLoader(), flags)
	credentialsByProfile := map[string]*fixedProfileCredentials{
		pinnedProfile: credentials,
	}
	profileRunner := &profileCredentialRunner{
		defaultProfile: pinnedProfile,
		resolveProfile: func(selector string) (string, error) {
			selected, err := authpkg.ResolveProfileReadOnly(configDir, selector)
			if err != nil {
				return "", err
			}
			if selected == nil || strings.TrimSpace(selected.CorpID) == "" {
				return "", fmt.Errorf("DWS service profile has no corpId")
			}
			return strings.TrimSpace(selected.CorpID), nil
		},
		ensureProfile: func(ctx context.Context, profile string) error {
			selected := credentialsByProfile[profile]
			if selected == nil {
				selected = newFixedProfileCredentials(configDir, profile)
				credentialsByProfile[profile] = selected
			}
			return selected.Ensure(ctx)
		},
		next: commandRunner,
	}
	service, err := commandservice.New(commandservice.Options{
		Version:     Version(),
		CatalogHash: schema.CatalogHash,
		SurfaceHash: schema.SurfaceHash,
		Index:       schema.Index,
		Runner:      profileRunner,
		Ready:       credentials.Ready,
		Close: func() error {
			profileRunner.Close()
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
	profileRunner.Start(ctx)
	return service, nil
}
