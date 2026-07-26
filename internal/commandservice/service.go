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

// Package commandservice exposes the reviewed DWS MCP command contract to
// long-running adapters without accepting CLI argv or rebuilding Cobra.
package commandservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/cli"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/executor"
)

const (
	CodeCommandNotFound      = "command_not_found"
	CodeCommandUnavailable   = "command_unavailable"
	CodeInvalidArguments     = "invalid_arguments"
	CodeConfirmationRequired = "confirmation_required"
	CodeDryRunUnsupported    = "dry_run_unsupported"
)

// Error is a stable command-service error that preserves the underlying DWS
// error for HTTP status and diagnostics mapping.
type Error struct {
	Code    string
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// CommandSummary is the bounded discovery projection returned by the HTTP
// service. Full parameter and safety details remain available per command.
type CommandSummary struct {
	CanonicalPath string `json:"canonical_path"`
	Title         string `json:"title,omitempty"`
	Description   string `json:"description,omitempty"`
	Effect        string `json:"effect,omitempty"`
	Risk          string `json:"risk,omitempty"`
	Confirmation  string `json:"confirmation,omitempty"`
	Idempotency   string `json:"idempotency,omitempty"`
}

type Metadata struct {
	Version      string `json:"version"`
	CatalogHash  string `json:"catalog_hash"`
	SurfaceHash  string `json:"surface_hash,omitempty"`
	CommandCount int    `json:"command_count"`
}

type ExecuteRequest struct {
	Arguments map[string]any
	Confirmed bool
	DryRun    bool
}

type ExecuteResult struct {
	CanonicalPath string         `json:"canonical_path"`
	DryRun        bool           `json:"dry_run,omitempty"`
	Content       map[string]any `json:"content,omitempty"`
}

type Options struct {
	Version     string
	CatalogHash string
	SurfaceHash string
	Index       cli.SchemaIndex
	Runner      executor.Runner
	Ready       func(context.Context) error
	Close       func() error
	Allow       func(cli.ToolSpec) bool
}

// Service is a single-profile command executor. Execution is serialized
// because the existing DWS runner owns process-scoped transport and audit
// state; discovery and health reads remain concurrent.
type Service struct {
	version     string
	catalogHash string
	surfaceHash string
	index       cli.SchemaIndex
	runner      executor.Runner
	ready       func(context.Context) error
	close       func() error
	commands    map[string]cli.ToolSpec
	summaries   []CommandSummary
	executionMu sync.Mutex
	closeOnce   sync.Once
	closeErr    error
}

func New(options Options) (*Service, error) {
	if options.Runner == nil {
		return nil, fmt.Errorf("command service runner is required")
	}
	commands := make(map[string]cli.ToolSpec)
	summaries := make([]CommandSummary, 0)
	for _, canonical := range options.Index.CanonicalPaths() {
		tool, ok := options.Index.Resolve(canonical)
		if !ok || !directMCPCommand(tool) {
			continue
		}
		if options.Allow != nil && !options.Allow(tool) {
			continue
		}
		commands[canonical] = tool
		summaries = append(summaries, CommandSummary{
			CanonicalPath: canonical,
			Title:         tool.Title,
			Description:   tool.Description,
			Effect:        tool.Safety.Effect,
			Risk:          tool.Safety.Risk,
			Confirmation:  tool.Safety.Confirmation,
			Idempotency:   tool.Safety.Idempotency,
		})
	}
	if len(commands) == 0 {
		return nil, fmt.Errorf("command service has no eligible MCP commands")
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].CanonicalPath < summaries[j].CanonicalPath
	})
	return &Service{
		version:     strings.TrimSpace(options.Version),
		catalogHash: strings.TrimSpace(options.CatalogHash),
		surfaceHash: strings.TrimSpace(options.SurfaceHash),
		index:       options.Index,
		runner:      options.Runner,
		ready:       options.Ready,
		close:       options.Close,
		commands:    commands,
		summaries:   summaries,
	}, nil
}

func directMCPCommand(tool cli.ToolSpec) bool {
	if tool.Interface.Mode != cli.InterfaceModeMCP ||
		tool.Interface.Availability != cli.InterfaceAvailable ||
		tool.Interface.Ref == nil ||
		tool.Identity.ProductID == "pat" ||
		len(tool.Positionals) != 0 {
		return false
	}
	for _, parameter := range tool.Parameters {
		if strings.TrimSpace(parameter.Property) == "" ||
			strings.TrimSpace(parameter.RequiredWhen) != "" {
			return false
		}
	}
	return true
}

func (s *Service) Metadata() Metadata {
	return Metadata{
		Version:      s.version,
		CatalogHash:  s.catalogHash,
		SurfaceHash:  s.surfaceHash,
		CommandCount: len(s.commands),
	}
}

func (s *Service) ListCommands() []CommandSummary {
	out := make([]CommandSummary, len(s.summaries))
	copy(out, s.summaries)
	return out
}

func (s *Service) Command(canonical string) (cli.ToolSpec, error) {
	canonical = strings.TrimSpace(canonical)
	if tool, ok := s.commands[canonical]; ok {
		return tool, nil
	}
	if _, ok := s.index.Resolve(canonical); ok {
		return cli.ToolSpec{}, &Error{
			Code:    CodeCommandUnavailable,
			Message: fmt.Sprintf("command %q is not available through the HTTP MCP surface", canonical),
		}
	}
	return cli.ToolSpec{}, &Error{
		Code:    CodeCommandNotFound,
		Message: fmt.Sprintf("command %q was not found", canonical),
	}
}

func (s *Service) Ready(ctx context.Context) error {
	if s.ready == nil {
		return nil
	}
	return s.ready(ctx)
}

func (s *Service) Close() error {
	s.closeOnce.Do(func() {
		if s.close != nil {
			s.closeErr = s.close()
		}
	})
	return s.closeErr
}

func (s *Service) Execute(ctx context.Context, canonical string, request ExecuteRequest) (ExecuteResult, error) {
	tool, err := s.Command(canonical)
	if err != nil {
		return ExecuteResult{}, err
	}
	if request.DryRun && tool.DryRun == nil {
		return ExecuteResult{}, &Error{
			Code:    CodeDryRunUnsupported,
			Message: fmt.Sprintf("command %q does not publish a reviewed dry-run capability", canonical),
		}
	}
	if !request.DryRun && tool.Safety.Confirmation == "user_required" && !request.Confirmed {
		return ExecuteResult{}, &Error{
			Code:    CodeConfirmationRequired,
			Message: fmt.Sprintf("command %q requires explicit confirmation", canonical),
		}
	}

	params, err := buildInterfaceArguments(tool, request.Arguments)
	if err != nil {
		return ExecuteResult{}, &Error{
			Code:    CodeInvalidArguments,
			Message: err.Error(),
			Cause:   err,
		}
	}
	ref := tool.Interface.Ref
	invocation := executor.NewHelperInvocation(
		tool.Identity.PrimaryCLIPath,
		ref.ProductID,
		ref.RPCName,
		params,
	)
	invocation.CanonicalPath = tool.Identity.CanonicalPath
	invocation.DryRun = request.DryRun

	result, err := s.run(ctx, invocation)
	if err != nil {
		return ExecuteResult{}, err
	}
	content := result.Response
	if nested, ok := result.Response["content"].(map[string]any); ok {
		content = nested
	}
	return ExecuteResult{
		CanonicalPath: tool.Identity.CanonicalPath,
		DryRun:        request.DryRun,
		Content:       content,
	}, nil
}

func (s *Service) run(ctx context.Context, invocation executor.Invocation) (executor.Result, error) {
	s.executionMu.Lock()
	defer s.executionMu.Unlock()
	return s.runner.Run(ctx, invocation)
}

func buildInterfaceArguments(tool cli.ToolSpec, arguments map[string]any) (map[string]any, error) {
	if arguments == nil {
		arguments = map[string]any{}
	}
	byName := make(map[string]cli.ParameterSpec, len(tool.Parameters))
	for _, parameter := range tool.Parameters {
		byName[parameter.Name] = parameter
	}
	for name := range arguments {
		if _, ok := byName[name]; !ok {
			return nil, fmt.Errorf("unknown argument %q for command %q", name, tool.Identity.CanonicalPath)
		}
	}

	effective := make(map[string]any, len(arguments))
	present := make(map[string]bool, len(arguments))
	for _, parameter := range tool.Parameters {
		value, provided := arguments[parameter.Name]
		hasValue := provided
		if !hasValue {
			value, hasValue = parameterDefault(parameter)
		}
		if !hasValue {
			if parameter.Required {
				return nil, fmt.Errorf("missing required argument %q", parameter.Name)
			}
			continue
		}
		normalized, err := normalizeParameterValue(parameter, value, provided && value == nil)
		if err != nil {
			return nil, fmt.Errorf("argument %q: %w", parameter.Name, err)
		}
		effective[parameter.Name] = normalized
		present[parameter.Name] = true
	}
	if err := validateConstraints(tool.Constraints, present); err != nil {
		return nil, err
	}

	params := make(map[string]any, len(effective))
	propertyOwners := make(map[string]string, len(effective))
	for name, value := range effective {
		parameter := byName[name]
		property := parameter.Property
		if owner, exists := propertyOwners[property]; exists {
			return nil, fmt.Errorf("arguments %q and %q both map to interface property %q", owner, name, property)
		}
		propertyOwners[property] = name
		params[property] = value
	}
	return params, nil
}

func parameterDefault(parameter cli.ParameterSpec) (any, bool) {
	raw := parameter.InterfaceDefault
	reviewedInterfaceDefault := len(bytes.TrimSpace(raw)) != 0
	if !reviewedInterfaceDefault {
		raw = parameter.Default
	}
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	if !reviewedInterfaceDefault && zeroDefault(value) {
		return nil, false
	}
	return value, true
}

func zeroDefault(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(typed) == ""
	case bool:
		return !typed
	case json.Number:
		return typed.String() == "0" || typed.String() == "0.0"
	case []any:
		return len(typed) == 0
	case map[string]any:
		return len(typed) == 0
	default:
		return false
	}
}

func normalizeParameterValue(parameter cli.ParameterSpec, value any, explicitNull bool) (any, error) {
	if explicitNull || value == nil {
		return nil, errors.New("null is not allowed")
	}
	want := strings.TrimSpace(parameter.InterfaceType)
	if want == "" {
		want = strings.TrimSpace(parameter.Type)
	}
	var normalized any
	switch want {
	case "string", "":
		typed, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("must be a string")
		}
		normalized = typed
	case "boolean":
		typed, ok := value.(bool)
		if !ok {
			return nil, fmt.Errorf("must be a boolean")
		}
		normalized = typed
	case "integer":
		typed, err := integerValue(value)
		if err != nil {
			return nil, err
		}
		normalized = typed
	case "number":
		switch value.(type) {
		case json.Number, float64, float32, int, int32, int64, uint, uint32, uint64:
			normalized = value
		default:
			return nil, fmt.Errorf("must be a number")
		}
	case "array":
		if typed, ok := value.([]any); ok {
			normalized = typed
		} else if typed, ok := value.([]string); ok {
			normalized = typed
		} else if typed, ok := value.(string); ok {
			parts := strings.Split(typed, ",")
			normalizedParts := make([]string, 0, len(parts))
			for _, part := range parts {
				if part = strings.TrimSpace(part); part != "" {
					normalizedParts = append(normalizedParts, part)
				}
			}
			if len(normalizedParts) == 0 {
				return nil, fmt.Errorf("must be a non-empty array")
			}
			normalized = normalizedParts
		} else {
			return nil, fmt.Errorf("must be an array")
		}
	case "object":
		typed, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("must be an object")
		}
		normalized = typed
	default:
		return nil, fmt.Errorf("has unsupported interface type %q", want)
	}
	if len(parameter.Enum) > 0 {
		text, ok := normalized.(string)
		if !ok || !contains(parameter.Enum, text) {
			return nil, fmt.Errorf("must be one of %s", strings.Join(parameter.Enum, ", "))
		}
	}
	return normalized, nil
}

func integerValue(value any) (any, error) {
	switch typed := value.(type) {
	case json.Number:
		number, err := typed.Int64()
		if err != nil {
			return nil, fmt.Errorf("must be an integer")
		}
		return number, nil
	case int:
		return typed, nil
	case int32:
		return typed, nil
	case int64:
		return typed, nil
	case uint:
		return typed, nil
	case uint32:
		return typed, nil
	case uint64:
		if typed > math.MaxInt64 {
			return nil, fmt.Errorf("integer is out of range")
		}
		return int64(typed), nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) ||
			math.Trunc(typed) != typed ||
			typed < math.MinInt64 ||
			typed > math.MaxInt64 {
			return nil, fmt.Errorf("must be an integer")
		}
		return int64(typed), nil
	default:
		return nil, fmt.Errorf("must be an integer")
	}
}

func contains(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func validateConstraints(constraints cli.RuntimeSchemaConstraints, present map[string]bool) error {
	for _, group := range constraints.RequireOneOf {
		count := presentCount(group, present)
		if count == 0 {
			return fmt.Errorf("one of arguments %s is required", strings.Join(group, ", "))
		}
	}
	for _, group := range constraints.MutuallyExclusive {
		if presentCount(group, present) > 1 {
			return fmt.Errorf("arguments %s are mutually exclusive", strings.Join(group, ", "))
		}
	}
	for _, group := range constraints.RequireTogether {
		count := presentCount(group, present)
		if count != 0 && count != len(group) {
			return fmt.Errorf("arguments %s must be provided together", strings.Join(group, ", "))
		}
	}
	return nil
}

func presentCount(group []string, present map[string]bool) int {
	count := 0
	for _, name := range group {
		if present[name] {
			count++
		}
	}
	return count
}
