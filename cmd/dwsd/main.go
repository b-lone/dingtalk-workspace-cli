// Copyright 2026 Alibaba Group
// Licensed under the Apache License, Version 2.0 (the "License");

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/app"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/httpapi"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("dwsd stopped", "error", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) > 0 && arguments[0] == "healthcheck" {
		return runHealthcheck(arguments[1:])
	}
	if len(arguments) > 0 {
		return fmt.Errorf("unknown dwsd command %q", arguments[0])
	}
	return runServer()
}

func runServer() error {
	config, err := httpapi.LoadConfigFromEnvironment()
	if err != nil {
		return fmt.Errorf("load service configuration: %w", err)
	}
	defer clear(config.Token)

	processContext, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	service, err := app.NewHTTPCommandService(
		processContext,
		config.Profile,
		config.CommandTimeout,
	)
	if err != nil {
		return err
	}
	defer service.Close()

	apiServer, err := httpapi.NewServer(httpapi.Options{
		Service:        service,
		Token:          config.Token,
		CommandTimeout: config.CommandTimeout,
		MaxBodyBytes:   config.MaxBodyBytes,
	})
	if err != nil {
		return err
	}
	server := apiServer.HTTPServer(config.ListenAddress)
	shutdownComplete := make(chan error, 1)
	go func() {
		<-processContext.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		shutdownComplete <- server.Shutdown(shutdownContext)
	}()

	metadata := service.Metadata()
	slog.Info("DWS HTTP service listening",
		"address", config.ListenAddress,
		"version", metadata.Version,
		"command_count", metadata.CommandCount,
	)
	err = server.ListenAndServe()
	if !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve DWS HTTP API: %w", err)
	}
	if shutdownErr := <-shutdownComplete; shutdownErr != nil {
		return fmt.Errorf("shut down DWS HTTP API: %w", shutdownErr)
	}
	return nil
}

func runHealthcheck(arguments []string) error {
	flags := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	url := flags.String("url", "http://127.0.0.1:8080/readyz", "readiness URL")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("healthcheck accepts no positional arguments")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, *url, nil)
	if err != nil {
		return fmt.Errorf("build healthcheck request: %w", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("call readiness endpoint: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("readiness endpoint returned HTTP %d", response.StatusCode)
	}
	return nil
}
