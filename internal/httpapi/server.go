// Copyright 2026 Alibaba Group
// Licensed under the Apache License, Version 2.0 (the "License");

// Package httpapi provides the authenticated HTTP boundary for DWS commands.
// Execute requests may select a registered profile without accepting CLI argv.
package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/cli"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/commandservice"
	apperrors "github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/errors"
)

type CommandService interface {
	Metadata() commandservice.Metadata
	ListCommands() []commandservice.CommandSummary
	Command(string) (cli.ToolSpec, error)
	Execute(context.Context, string, commandservice.ExecuteRequest) (commandservice.ExecuteResult, error)
	Ready(context.Context) error
}

type Options struct {
	Service        CommandService
	Token          []byte
	CommandTimeout time.Duration
	MaxBodyBytes   int64
	Logger         *slog.Logger
}

type Server struct {
	service        CommandService
	token          []byte
	commandTimeout time.Duration
	maxBodyBytes   int64
	logger         *slog.Logger
	handler        http.Handler
}

type executeRequest struct {
	Profile   json.RawMessage `json:"profile,omitempty"`
	Arguments map[string]any  `json:"arguments"`
	Confirmed bool            `json:"confirmed,omitempty"`
	DryRun    bool            `json:"dry_run,omitempty"`
}

type responseEnvelope struct {
	RequestID string        `json:"request_id"`
	Data      any           `json:"data,omitempty"`
	Error     *errorPayload `json:"error,omitempty"`
}

type errorPayload struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Category  string `json:"category,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Retryable bool   `json:"retryable,omitempty"`
}

func NewServer(options Options) (*Server, error) {
	if options.Service == nil {
		return nil, errors.New("HTTP command service is required")
	}
	if len(options.Token) < 32 {
		return nil, errors.New("HTTP service token must contain at least 32 bytes")
	}
	if options.CommandTimeout <= 0 {
		return nil, errors.New("HTTP command timeout must be positive")
	}
	if options.MaxBodyBytes <= 0 {
		return nil, errors.New("HTTP max body bytes must be positive")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	server := &Server{
		service:        options.Service,
		token:          append([]byte(nil), options.Token...),
		commandTimeout: options.CommandTimeout,
		maxBodyBytes:   options.MaxBodyBytes,
		logger:         logger,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", server.handleHealth)
	mux.HandleFunc("GET /readyz", server.handleReady)
	mux.Handle("GET /v1/schema", server.requireBearer(http.HandlerFunc(server.handleSchema)))
	mux.Handle("GET /v1/schema/{command}", server.requireBearer(http.HandlerFunc(server.handleCommand)))
	mux.Handle("POST /v1/commands/{command}/execute", server.requireBearer(http.HandlerFunc(server.handleExecute)))
	server.handler = server.withRequestContext(mux)
	return server, nil
}

func (s *Server) Handler() http.Handler {
	return s.handler
}

func (s *Server) HTTPServer(address string) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      s.commandTimeout + 10*time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
}

func (s *Server) withRequestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestID := newRequestID()
		writer.Header().Set("X-Request-ID", requestID)
		writer.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(writer, request.WithContext(context.WithValue(request.Context(), requestIDKey{}, requestID)))
	})
}

func (s *Server) requireBearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		supplied := strings.TrimSpace(request.Header.Get("Authorization"))
		const prefix = "Bearer "
		if !strings.HasPrefix(supplied, prefix) ||
			subtle.ConstantTimeCompare([]byte(strings.TrimSpace(strings.TrimPrefix(supplied, prefix))), s.token) != 1 {
			writer.Header().Set("WWW-Authenticate", "Bearer")
			s.writeError(writer, request, http.StatusUnauthorized, errorPayload{
				Code:    "unauthorized",
				Message: "a valid bearer token is required",
			})
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func (s *Server) handleHealth(writer http.ResponseWriter, request *http.Request) {
	s.writeData(writer, request, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) handleReady(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
	defer cancel()
	if err := s.service.Ready(ctx); err != nil {
		s.writeError(writer, request, http.StatusServiceUnavailable, errorPayload{
			Code:      "not_ready",
			Message:   "DWS service is not ready",
			Retryable: true,
		})
		return
	}
	s.writeData(writer, request, http.StatusOK, map[string]any{"status": "ready"})
}

func (s *Server) handleSchema(writer http.ResponseWriter, request *http.Request) {
	s.writeData(writer, request, http.StatusOK, map[string]any{
		"metadata": s.service.Metadata(),
		"commands": s.service.ListCommands(),
	})
}

func (s *Server) handleCommand(writer http.ResponseWriter, request *http.Request) {
	command := request.PathValue("command")
	tool, err := s.service.Command(command)
	if err != nil {
		s.writeMappedError(writer, request, command, err)
		return
	}
	payload, err := tool.ToPayload()
	if err != nil {
		s.writeMappedError(writer, request, command, err)
		return
	}
	s.writeData(writer, request, http.StatusOK, payload)
}

func (s *Server) handleExecute(writer http.ResponseWriter, request *http.Request) {
	if !hasJSONContentType(request.Header.Get("Content-Type")) {
		s.writeError(writer, request, http.StatusUnsupportedMediaType, errorPayload{
			Code:    "unsupported_media_type",
			Message: "Content-Type must be application/json",
		})
		return
	}
	var input executeRequest
	if err := decodeJSONBody(writer, request, s.maxBodyBytes, &input); err != nil {
		status := http.StatusBadRequest
		code := "invalid_json"
		if errors.As(err, new(*http.MaxBytesError)) {
			status = http.StatusRequestEntityTooLarge
			code = "request_too_large"
		}
		s.writeError(writer, request, status, errorPayload{
			Code:    code,
			Message: "request body is not a valid DWS execute request",
		})
		return
	}
	profile := ""
	if len(input.Profile) != 0 {
		var providedProfile string
		if err := json.Unmarshal(input.Profile, &providedProfile); err != nil {
			s.writeError(writer, request, http.StatusBadRequest, errorPayload{
				Code:    commandservice.CodeInvalidArguments,
				Message: "profile must be a string when provided",
			})
			return
		}
		profile = strings.TrimSpace(providedProfile)
		if profile == "" {
			s.writeError(writer, request, http.StatusBadRequest, errorPayload{
				Code:    commandservice.CodeInvalidArguments,
				Message: "profile must not be blank when provided",
			})
			return
		}
	}
	command := request.PathValue("command")
	ctx, cancel := context.WithTimeout(request.Context(), s.commandTimeout)
	defer cancel()
	result, err := s.service.Execute(ctx, command, commandservice.ExecuteRequest{
		Profile:   profile,
		Arguments: input.Arguments,
		Confirmed: input.Confirmed,
		DryRun:    input.DryRun,
	})
	if err != nil {
		s.writeMappedError(writer, request, command, err)
		return
	}
	s.writeData(writer, request, http.StatusOK, result)
}

func (s *Server) writeMappedError(writer http.ResponseWriter, request *http.Request, command string, err error) {
	status, payload := mapError(err)
	s.logger.Warn("DWS HTTP request failed",
		"request_id", requestIDFromContext(request.Context()),
		"command", command,
		"status", status,
		"code", payload.Code,
		"category", payload.Category,
	)
	s.writeError(writer, request, status, payload)
}

func mapError(err error) (int, errorPayload) {
	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout, errorPayload{
			Code:      "command_timeout",
			Message:   "DWS command timed out",
			Retryable: true,
		}
	}
	if errors.Is(err, context.Canceled) {
		return http.StatusRequestTimeout, errorPayload{
			Code:      "request_canceled",
			Message:   "DWS command was canceled",
			Retryable: true,
		}
	}
	var serviceErr *commandservice.Error
	if errors.As(err, &serviceErr) {
		status := http.StatusBadRequest
		switch serviceErr.Code {
		case commandservice.CodeCommandNotFound:
			status = http.StatusNotFound
		case commandservice.CodeCommandUnavailable, commandservice.CodeDryRunUnsupported:
			status = http.StatusUnprocessableEntity
		case commandservice.CodeConfirmationRequired:
			status = http.StatusPreconditionRequired
		}
		return status, errorPayload{
			Code:    serviceErr.Code,
			Message: serviceErr.Message,
		}
	}
	var dwsErr *apperrors.Error
	if errors.As(err, &dwsErr) {
		status := http.StatusInternalServerError
		code := "upstream_internal_error"
		message := "DWS command failed"
		switch dwsErr.Category {
		case apperrors.CategoryValidation:
			status = http.StatusBadRequest
			code = "upstream_validation_error"
			message = "DWS rejected the command request"
		case apperrors.CategoryAuth:
			status = http.StatusServiceUnavailable
			code = "upstream_auth_error"
			message = "DWS service identity is unavailable"
		case apperrors.CategoryDiscovery:
			status = http.StatusServiceUnavailable
			code = "upstream_discovery_error"
			message = "DWS command discovery is unavailable"
		case apperrors.CategoryAPI:
			status = http.StatusBadGateway
			code = "upstream_api_error"
			message = "DWS upstream request failed"
			if dwsErr.Retryable {
				status = http.StatusServiceUnavailable
			}
		}
		return status, errorPayload{
			Code:      code,
			Message:   message,
			Category:  string(dwsErr.Category),
			Reason:    dwsErr.Reason,
			Retryable: dwsErr.Retryable,
		}
	}
	return http.StatusInternalServerError, errorPayload{
		Code:    "internal_error",
		Message: "DWS service encountered an internal error",
	}
}

func hasJSONContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && mediaType == "application/json"
}

func decodeJSONBody(writer http.ResponseWriter, request *http.Request, limit int64, target any) error {
	body := http.MaxBytesReader(writer, request.Body, limit)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain one JSON value")
		}
		return err
	}
	return nil
}

func (s *Server) writeData(writer http.ResponseWriter, request *http.Request, status int, data any) {
	s.writeJSON(writer, status, responseEnvelope{
		RequestID: requestIDFromContext(request.Context()),
		Data:      data,
	})
}

func (s *Server) writeError(writer http.ResponseWriter, request *http.Request, status int, payload errorPayload) {
	s.writeJSON(writer, status, responseEnvelope{
		RequestID: requestIDFromContext(request.Context()),
		Error:     &payload,
	})
}

func (s *Server) writeJSON(writer http.ResponseWriter, status int, payload responseEnvelope) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(payload); err != nil {
		s.logger.Error("write DWS HTTP response failed", "request_id", payload.RequestID)
	}
}

type requestIDKey struct{}

func newRequestID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "unavailable"
	}
	return hex.EncodeToString(value[:])
}

func requestIDFromContext(ctx context.Context) string {
	value, _ := ctx.Value(requestIDKey{}).(string)
	return value
}
