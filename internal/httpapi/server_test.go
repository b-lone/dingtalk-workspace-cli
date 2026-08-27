// Copyright 2026 Alibaba Group
// Licensed under the Apache License, Version 2.0 (the "License");

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/cli"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/commandservice"
)

const testBearerToken = "0123456789abcdef0123456789abcdef"

type fakeCommandService struct {
	readyError error
	executeErr error
	request    commandservice.ExecuteRequest
}

func (s *fakeCommandService) Metadata() commandservice.Metadata {
	return commandservice.Metadata{Version: "test", CommandCount: 1}
}

func (s *fakeCommandService) ListCommands() []commandservice.CommandSummary {
	return []commandservice.CommandSummary{{CanonicalPath: "sample.run"}}
}

func (s *fakeCommandService) Command(command string) (cli.ToolSpec, error) {
	if command != "sample.run" {
		return cli.ToolSpec{}, &commandservice.Error{
			Code:    commandservice.CodeCommandNotFound,
			Message: "command was not found",
		}
	}
	return cli.ToolSpec{
		Identity: cli.ToolIdentitySpec{
			ProductID:      "sample",
			Name:           "run",
			CanonicalPath:  "sample.run",
			Path:           "sample.run",
			CLIPath:        "sample run",
			PrimaryCLIPath: "sample run",
		},
		Interface: cli.InterfaceSpec{
			Mode:         cli.InterfaceModeMCP,
			Availability: cli.InterfaceAvailable,
			Ref:          &cli.InterfaceRefSpec{ProductID: "sample", RPCName: "run"},
		},
	}, nil
}

func (s *fakeCommandService) Execute(_ context.Context, command string, request commandservice.ExecuteRequest) (commandservice.ExecuteResult, error) {
	s.request = request
	if s.executeErr != nil {
		return commandservice.ExecuteResult{}, s.executeErr
	}
	return commandservice.ExecuteResult{
		CanonicalPath: command,
		Content:       map[string]any{"ok": true},
	}, nil
}

func (s *fakeCommandService) Ready(context.Context) error {
	return s.readyError
}

func TestServerHealthAndBearerBoundary(t *testing.T) {
	server := newTestServer(t, &fakeCommandService{}, 1024)

	health := performRequest(server.Handler(), http.MethodGet, "/healthz", "", "")
	if health.Code != http.StatusOK {
		t.Fatalf("GET /healthz status = %d, want 200", health.Code)
	}
	if health.Header().Get("X-Request-ID") == "" {
		t.Fatal("GET /healthz did not return X-Request-ID")
	}

	unauthorized := performRequest(server.Handler(), http.MethodGet, "/v1/schema", "", "")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized schema status = %d, want 401", unauthorized.Code)
	}

	authorized := performRequest(server.Handler(), http.MethodGet, "/v1/schema", "", testBearerToken)
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized schema status = %d, want 200; body=%s", authorized.Code, authorized.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(authorized.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["data"] == nil {
		t.Fatalf("schema response = %#v", payload)
	}
}

func TestServerExecutesStrictJSONRequest(t *testing.T) {
	service := &fakeCommandService{}
	server := newTestServer(t, service, 1024)
	response := performRequest(
		server.Handler(),
		http.MethodPost,
		"/v1/commands/sample.run/execute",
		`{"profile":"Alibaba","arguments":{"limit":3},"confirmed":true}`,
		testBearerToken,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("execute status = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	limit, ok := service.request.Arguments["limit"].(json.Number)
	if !ok || limit.String() != "3" || !service.request.Confirmed || service.request.Profile != "Alibaba" {
		t.Fatalf("decoded execute request = %#v", service.request)
	}

	invalid := performRequest(
		server.Handler(),
		http.MethodPost,
		"/v1/commands/sample.run/execute",
		`{"arguments":{},"unexpected":"value"}`,
		testBearerToken,
	)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("unknown request field status = %d, want 400", invalid.Code)
	}

	blankProfile := performRequest(
		server.Handler(),
		http.MethodPost,
		"/v1/commands/sample.run/execute",
		`{"profile":"   ","arguments":{}}`,
		testBearerToken,
	)
	if blankProfile.Code != http.StatusBadRequest {
		t.Fatalf("blank profile status = %d, want 400", blankProfile.Code)
	}

	request := httptest.NewRequest(http.MethodPost, "/v1/commands/sample.run/execute", strings.NewReader(`{"arguments":{}}`))
	request.Header.Set("Content-Type", "text/plain")
	request.Header.Set("Authorization", "Bearer "+testBearerToken)
	wrongContentType := httptest.NewRecorder()
	server.Handler().ServeHTTP(wrongContentType, request)
	if wrongContentType.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("wrong content type status = %d, want 415", wrongContentType.Code)
	}
}

func TestServerMapsCommandErrorsAndReadiness(t *testing.T) {
	service := &fakeCommandService{
		readyError: errors.New("profile unavailable"),
		executeErr: &commandservice.Error{
			Code:    commandservice.CodeConfirmationRequired,
			Message: "explicit confirmation is required",
		},
	}
	server := newTestServer(t, service, 1024)

	ready := performRequest(server.Handler(), http.MethodGet, "/readyz", "", "")
	if ready.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready status = %d, want 503", ready.Code)
	}
	execute := performRequest(
		server.Handler(),
		http.MethodPost,
		"/v1/commands/sample.run/execute",
		`{"arguments":{}}`,
		testBearerToken,
	)
	if execute.Code != http.StatusPreconditionRequired {
		t.Fatalf("execute error status = %d, want 428; body=%s", execute.Code, execute.Body.String())
	}
}

func TestServerRejectsOversizedBody(t *testing.T) {
	server := newTestServer(t, &fakeCommandService{}, 32)
	response := performRequest(
		server.Handler(),
		http.MethodPost,
		"/v1/commands/sample.run/execute",
		`{"arguments":{"value":"this request is intentionally too large"}}`,
		testBearerToken,
	)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status = %d, want 413; body=%s", response.Code, response.Body.String())
	}
}

func newTestServer(t *testing.T, service CommandService, maxBodyBytes int64) *Server {
	t.Helper()
	server, err := NewServer(Options{
		Service:        service,
		Token:          []byte(testBearerToken),
		CommandTimeout: time.Second,
		MaxBodyBytes:   maxBodyBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func performRequest(handler http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
