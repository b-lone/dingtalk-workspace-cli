// Copyright 2026 Alibaba Group
// Licensed under the Apache License, Version 2.0 (the "License");

// Package hostcli executes the reviewed DWS HTTP command surface through a
// trusted host wrapper without exposing a generic argv boundary.
package hostcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	apperrors "github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/errors"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/executor"
)

const (
	maxProfileSelectorBytes = 255
	maxStderrBytes          = int64(64 << 10)
)

type valueEncoding uint8

const (
	encodeString valueEncoding = iota
	encodeInteger
	encodeBooleanText
	encodeCSV
	encodeUnixMilliseconds
)

type parameterSpec struct {
	property string
	flag     string
	encoding valueEncoding
}

type commandSpec struct {
	path       []string
	parameters []parameterSpec
}

var reviewedCommands = map[string]commandSpec{
	"contact.get_current_user_profile": {
		path: []string{"contact", "user", "get-self"},
	},
	"contact.search_contact_by_key_word": {
		path: []string{"contact", "user", "search"},
		parameters: []parameterSpec{
			{property: "keyword", flag: "query", encoding: encodeString},
		},
	},
	"chat.get_conversation_info": {
		path: []string{"chat", "conversation-info"},
		parameters: []parameterSpec{
			{property: "openConversationId", flag: "group", encoding: encodeString},
		},
	},
	"calendar.list_calendars": {
		path: []string{"calendar", "book", "list"},
	},
	"calendar.list_calendar_events": {
		path: []string{"calendar", "event", "list"},
		parameters: []parameterSpec{
			{property: "startTime", flag: "start", encoding: encodeUnixMilliseconds},
			{property: "endTime", flag: "end", encoding: encodeUnixMilliseconds},
			{property: "calendarId", flag: "calendar-id", encoding: encodeString},
			{property: "cursor", flag: "cursor", encoding: encodeString},
			{property: "limit", flag: "limit", encoding: encodeInteger},
		},
	},
	"todo.get_user_todos_in_current_org": {
		path: []string{"todo", "task", "list"},
		parameters: []parameterSpec{
			{property: "roleTypes", flag: "role-types", encoding: encodeCSV},
			{property: "pageNum", flag: "page", encoding: encodeInteger},
			{property: "pageSize", flag: "size", encoding: encodeInteger},
			{property: "todoStatus", flag: "status", encoding: encodeBooleanText},
			{property: "priorityList", flag: "priority", encoding: encodeCSV},
			{property: "planFinishDateStart", flag: "plan-finish-date-start", encoding: encodeUnixMilliseconds},
			{property: "planFinishDateEnd", flag: "plan-finish-date-end", encoding: encodeUnixMilliseconds},
		},
	},
}

type Options struct {
	WrapperPath    string
	DefaultProfile string
	CommandTimeout time.Duration
	MaxOutputBytes int64
}

type Runner struct {
	wrapperPath    string
	defaultProfile string
	commandTimeout time.Duration
	maxOutputBytes int64

	readyMu sync.RWMutex
	ready   bool
}

func NewRunner(options Options) (*Runner, error) {
	path, err := validateWrapperPath(options.WrapperPath)
	if err != nil {
		return nil, err
	}
	profile := strings.TrimSpace(options.DefaultProfile)
	if err := ValidateProfileSelector(profile); err != nil {
		return nil, fmt.Errorf("invalid default DWS profile: %w", err)
	}
	if options.CommandTimeout <= 0 {
		return nil, errors.New("DWS wrapper command timeout must be positive")
	}
	if options.MaxOutputBytes <= 0 {
		return nil, errors.New("DWS wrapper max output bytes must be positive")
	}
	return &Runner{
		wrapperPath:    path,
		defaultProfile: profile,
		commandTimeout: options.CommandTimeout,
		maxOutputBytes: options.MaxOutputBytes,
	}, nil
}

func Supports(canonicalPath string) bool {
	_, ok := reviewedCommands[strings.TrimSpace(canonicalPath)]
	return ok
}

func ValidateProfileSelector(value string) error {
	if value == "" || strings.TrimSpace(value) != value {
		return errors.New("profile selector must not be blank or contain surrounding whitespace")
	}
	if !utf8.ValidString(value) || len(value) > maxProfileSelectorBytes {
		return errors.New("profile selector is invalid or too long")
	}
	if strings.Contains(value, ",") {
		return errors.New("profile selector must identify exactly one profile")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return errors.New("profile selector contains a control character")
		}
	}
	return nil
}

func (r *Runner) Run(ctx context.Context, invocation executor.Invocation) (executor.Result, error) {
	return r.RunWithProfile(ctx, "", invocation)
}

func (r *Runner) RunWithProfile(ctx context.Context, profile string, invocation executor.Invocation) (executor.Result, error) {
	selected := profile
	if selected == "" {
		selected = r.defaultProfile
	}
	arguments, err := r.buildArguments(selected, invocation)
	if err != nil {
		return executor.Result{}, apperrors.NewValidation(
			"trusted DWS wrapper arguments are invalid",
			apperrors.WithReason("wrapper_arguments_invalid"),
			apperrors.WithCause(err),
		)
	}
	result, err := r.execute(ctx, arguments, invocation)
	if err != nil && selected == r.defaultProfile {
		var typed *apperrors.Error
		if errors.As(err, &typed) && typed.Category == apperrors.CategoryAuth {
			r.setReady(false)
		}
	}
	return result, err
}

func (r *Runner) Ready(ctx context.Context) error {
	r.readyMu.RLock()
	ready := r.ready
	r.readyMu.RUnlock()
	if ready {
		return nil
	}
	invocation := executor.Invocation{
		CanonicalPath: "contact.get_current_user_profile",
		Params:        map[string]any{},
	}
	result, err := r.RunWithProfile(ctx, r.defaultProfile, invocation)
	if err != nil {
		r.setReady(false)
		return err
	}
	if err := validateIdentity(result.Response, r.defaultProfile); err != nil {
		typed := apperrors.NewAuth(
			"trusted DWS wrapper identity does not match the configured profile",
			apperrors.WithReason("profile_identity_mismatch"),
			apperrors.WithCause(err),
		)
		r.setReady(false)
		return typed
	}
	r.setReady(true)
	return nil
}

func (r *Runner) setReady(ready bool) {
	r.readyMu.Lock()
	defer r.readyMu.Unlock()
	r.ready = ready
}

func (r *Runner) buildArguments(profile string, invocation executor.Invocation) ([]string, error) {
	if profile == "" {
		profile = r.defaultProfile
	}
	if err := ValidateProfileSelector(profile); err != nil {
		return nil, err
	}
	spec, ok := reviewedCommands[strings.TrimSpace(invocation.CanonicalPath)]
	if !ok {
		return nil, fmt.Errorf("unsupported canonical command %q", invocation.CanonicalPath)
	}
	properties := make(map[string]parameterSpec, len(spec.parameters))
	for _, parameter := range spec.parameters {
		properties[parameter.property] = parameter
	}
	for property := range invocation.Params {
		if _, ok := properties[property]; !ok {
			return nil, fmt.Errorf("unsupported property %q for command %q", property, invocation.CanonicalPath)
		}
	}
	timeoutSeconds := int64((r.commandTimeout + time.Second - 1) / time.Second)
	arguments := []string{
		"--profile=" + profile,
		"--format=json",
		"--timeout=" + strconv.FormatInt(timeoutSeconds, 10),
	}
	arguments = append(arguments, spec.path...)
	for _, parameter := range spec.parameters {
		value, ok := invocation.Params[parameter.property]
		if !ok {
			continue
		}
		encoded, err := encodeValue(value, parameter.encoding)
		if err != nil {
			return nil, fmt.Errorf("property %q: %w", parameter.property, err)
		}
		arguments = append(arguments, "--"+parameter.flag, encoded)
	}
	return arguments, nil
}

func (r *Runner) execute(ctx context.Context, arguments []string, invocation executor.Invocation) (executor.Result, error) {
	command := exec.CommandContext(ctx, r.wrapperPath, arguments...)
	configureProcess(command)
	command.Env = controlledEnvironment()
	stdout, err := command.StdoutPipe()
	if err != nil {
		return executor.Result{}, wrapperInternalError("wrapper_stdout_pipe_failed", err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return executor.Result{}, wrapperInternalError("wrapper_stderr_pipe_failed", err)
	}
	if err := command.Start(); err != nil {
		return executor.Result{}, wrapperInternalError("wrapper_start_failed", err)
	}
	stdoutResult := make(chan streamResult, 1)
	stderrResult := make(chan streamResult, 1)
	go func() { stdoutResult <- readBounded(stdout, r.maxOutputBytes) }()
	go func() { stderrResult <- readBounded(stderr, maxStderrBytes) }()
	waitErr := command.Wait()
	stdoutData := <-stdoutResult
	stderrData := <-stderrResult
	if ctxErr := ctx.Err(); ctxErr != nil {
		return executor.Result{}, ctxErr
	}
	if stdoutData.err != nil {
		return executor.Result{}, wrapperInternalError("wrapper_output_read_failed", stdoutData.err)
	}
	if stderrData.err != nil {
		return executor.Result{}, wrapperInternalError("wrapper_error_read_failed", stderrData.err)
	}
	if stdoutData.overflow {
		return executor.Result{}, wrapperInternalError("wrapper_output_too_large", nil)
	}
	if waitErr != nil {
		return executor.Result{}, mapWrapperFailure(waitErr, stderrData.data)
	}
	payload := bytes.TrimSpace(stdoutData.data)
	if len(payload) == 0 {
		return executor.Result{}, wrapperInternalError("wrapper_output_empty", nil)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var response map[string]any
	if err := decoder.Decode(&response); err != nil || response == nil {
		return executor.Result{}, wrapperInternalError("wrapper_output_invalid", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return executor.Result{}, wrapperInternalError("wrapper_output_invalid", err)
	}
	return executor.Result{Invocation: invocation, Response: response}, nil
}

func validateIdentity(response map[string]any, expectedCorpID string) error {
	if response["success"] != true {
		return errors.New("identity response did not declare success")
	}
	records, ok := response["result"].([]any)
	if !ok || len(records) == 0 {
		return errors.New("identity response has no result")
	}
	record, ok := records[0].(map[string]any)
	if !ok {
		return errors.New("identity record is invalid")
	}
	employee, ok := record["orgEmployeeModel"].(map[string]any)
	if !ok {
		return errors.New("identity employee is missing")
	}
	corpID, _ := employee["corpId"].(string)
	userID, _ := employee["userId"].(string)
	if strings.TrimSpace(corpID) != expectedCorpID {
		return fmt.Errorf("identity corpId does not match")
	}
	if strings.TrimSpace(userID) == "" {
		return errors.New("identity userId is missing")
	}
	return nil
}

func validateWrapperPath(raw string) (string, error) {
	path := strings.TrimSpace(raw)
	if path == "" || !filepath.IsAbs(path) {
		return "", errors.New("DWS wrapper path must be absolute")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("inspect DWS wrapper: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("DWS wrapper must be a regular file")
	}
	if info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("DWS wrapper must be executable")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return "", errors.New("DWS wrapper must not be writable by group or others")
	}
	return path, nil
}

func encodeValue(value any, encoding valueEncoding) (string, error) {
	switch encoding {
	case encodeString:
		text, ok := value.(string)
		if !ok || strings.TrimSpace(text) == "" || containsControl(text) {
			return "", errors.New("must be a non-blank string without control characters")
		}
		return text, nil
	case encodeInteger:
		return integerText(value)
	case encodeBooleanText:
		switch typed := value.(type) {
		case bool:
			return strconv.FormatBool(typed), nil
		case string:
			if typed == "true" || typed == "false" {
				return typed, nil
			}
		}
		return "", errors.New("must be true or false")
	case encodeCSV:
		return csvText(value)
	case encodeUnixMilliseconds:
		milliseconds, err := integerValue(value)
		if err != nil {
			return "", err
		}
		return time.UnixMilli(milliseconds).UTC().Format(time.RFC3339), nil
	default:
		return "", errors.New("unsupported value encoding")
	}
}

func integerText(value any) (string, error) {
	integer, err := integerValue(value)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(integer, 10), nil
}

func integerValue(value any) (int64, error) {
	switch typed := value.(type) {
	case json.Number:
		integer, err := typed.Int64()
		if err != nil {
			return 0, errors.New("must be an integer")
		}
		return integer, nil
	case int:
		return int64(typed), nil
	case int32:
		return int64(typed), nil
	case int64:
		return typed, nil
	case uint:
		if uint64(typed) > uint64(^uint64(0)>>1) {
			return 0, errors.New("integer is out of range")
		}
		return int64(typed), nil
	case uint32:
		return int64(typed), nil
	case uint64:
		if typed > uint64(^uint64(0)>>1) {
			return 0, errors.New("integer is out of range")
		}
		return int64(typed), nil
	case string:
		integer, err := strconv.ParseInt(typed, 10, 64)
		if err != nil {
			return 0, errors.New("must be an integer")
		}
		return integer, nil
	default:
		return 0, errors.New("must be an integer")
	}
}

func csvText(value any) (string, error) {
	var values []any
	switch typed := value.(type) {
	case []any:
		values = typed
	case []string:
		values = make([]any, len(typed))
		for index, item := range typed {
			values[index] = item
		}
	default:
		return "", errors.New("must be an array")
	}
	if len(values) == 0 {
		return "", errors.New("must be a non-empty array")
	}
	encoded := make([]string, 0, len(values))
	for _, value := range values {
		var text string
		switch typed := value.(type) {
		case string:
			text = typed
		default:
			var err error
			text, err = integerText(typed)
			if err != nil {
				return "", errors.New("array items must be strings or integers")
			}
		}
		if strings.TrimSpace(text) == "" || strings.Contains(text, ",") || containsControl(text) {
			return "", errors.New("array item is invalid")
		}
		encoded = append(encoded, text)
	}
	return strings.Join(encoded, ","), nil
}

func containsControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

type streamResult struct {
	data     []byte
	overflow bool
	err      error
}

func readBounded(reader io.Reader, limit int64) streamResult {
	buffer := bytes.NewBuffer(make([]byte, 0, minInt64(limit, 32<<10)))
	chunk := make([]byte, 32<<10)
	var total int64
	overflow := false
	for {
		count, err := reader.Read(chunk)
		if count > 0 {
			remaining := limit - total
			if remaining > 0 {
				writeCount := int64(count)
				if writeCount > remaining {
					writeCount = remaining
				}
				_, _ = buffer.Write(chunk[:writeCount])
			}
			total += int64(count)
			if total > limit {
				overflow = true
			}
		}
		if errors.Is(err, io.EOF) {
			return streamResult{data: buffer.Bytes(), overflow: overflow}
		}
		if err != nil {
			return streamResult{data: buffer.Bytes(), overflow: overflow, err: err}
		}
	}
}

func minInt64(left, right int64) int {
	if left < right {
		return int(left)
	}
	return int(right)
}

func controlledEnvironment() []string {
	keys := []string{"HOME", "USER", "LOGNAME", "TMPDIR", "LANG", "LC_ALL"}
	environment := make([]string, 0, len(keys)+1)
	for _, key := range keys {
		if value := os.Getenv(key); value != "" && !containsControl(value) {
			environment = append(environment, key+"="+value)
		}
	}
	path := os.Getenv("PATH")
	if path == "" || containsControl(path) {
		path = "/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin"
	}
	environment = append(environment, "PATH="+path)
	return environment
}

func mapWrapperFailure(waitErr error, stderr []byte) error {
	payload := parseWrapperError(stderr)
	reason := payload.Reason
	if reason == "" {
		var exitError *exec.ExitError
		if errors.As(waitErr, &exitError) {
			reason = "wrapper_exit_" + strconv.Itoa(exitError.ExitCode())
		} else {
			reason = "wrapper_execute_failed"
		}
	}
	options := []apperrors.Option{
		apperrors.WithReason(reason),
		apperrors.WithRetryable(payload.Retryable),
		apperrors.WithCause(waitErr),
	}
	switch payload.Category {
	case string(apperrors.CategoryAuth):
		return apperrors.NewAuth("trusted DWS wrapper identity is unavailable", options...)
	case string(apperrors.CategoryValidation):
		return apperrors.NewValidation("trusted DWS wrapper rejected the command", options...)
	case string(apperrors.CategoryDiscovery):
		return apperrors.NewDiscovery("trusted DWS wrapper discovery failed", options...)
	case string(apperrors.CategoryAPI):
		return apperrors.NewAPI("trusted DWS wrapper upstream request failed", options...)
	}
	var exitError *exec.ExitError
	if errors.As(waitErr, &exitError) {
		switch exitError.ExitCode() {
		case 1:
			return apperrors.NewAPI("trusted DWS wrapper upstream request failed", options...)
		case 2:
			return apperrors.NewAuth("trusted DWS wrapper identity is unavailable", options...)
		case 3:
			return apperrors.NewValidation("trusted DWS wrapper rejected the command", options...)
		case 6:
			return apperrors.NewDiscovery("trusted DWS wrapper discovery failed", options...)
		}
	}
	return apperrors.NewInternal("trusted DWS wrapper failed", options...)
}

type wrapperErrorPayload struct {
	Category  string `json:"category"`
	Reason    string `json:"reason"`
	Retryable bool   `json:"retryable"`
}

func parseWrapperError(stderr []byte) wrapperErrorPayload {
	data := bytes.TrimSpace(stderr)
	if len(data) == 0 {
		return wrapperErrorPayload{}
	}
	var direct wrapperErrorPayload
	if json.Unmarshal(data, &direct) == nil && direct.Category != "" {
		return direct
	}
	var envelope struct {
		Error wrapperErrorPayload `json:"error"`
	}
	if json.Unmarshal(data, &envelope) == nil {
		return envelope.Error
	}
	return wrapperErrorPayload{}
}

func wrapperInternalError(reason string, cause error) error {
	options := []apperrors.Option{apperrors.WithReason(reason)}
	if cause != nil {
		options = append(options, apperrors.WithCause(cause))
	}
	return apperrors.NewInternal("trusted DWS wrapper response is unavailable", options...)
}
