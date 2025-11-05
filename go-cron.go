// Copyright 2025 Focela Authors.
//
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the project root for full license information.

// Package main provides a cron scheduler with graceful shutdown.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/robfig/cron/v3"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
	builtBy = "unknown"

	logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
)

const (
	minArgs         = 3
	shutdownTimeout = 30 * time.Second
)

// execute runs a command and blocks until completion.
func execute(ctx context.Context, schedule string, command string, args []string) error {
	logger.Info("executing command", "schedule", schedule, "command", command, "args", args)

	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.Canceled {
			return fmt.Errorf("command cancelled: %w", err)
		}
		return fmt.Errorf("command execution failed: %w", err)
	}
	return nil
}

// create initializes the cron scheduler and returns it with a WaitGroup.
func create(ctx context.Context, schedule string, command string, args []string) (*cron.Cron, *sync.WaitGroup, error) {
	wg := &sync.WaitGroup{}

	parser := cron.NewParser(
		cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
	)

	if _, err := parser.Parse(schedule); err != nil {
		return nil, nil, fmt.Errorf("invalid schedule '%s': %w", schedule, err)
	}

	c := cron.New(cron.WithParser(parser))
	logger.Info("new cron scheduled", "schedule", schedule)

	c.AddFunc(schedule, func() {
		// Increment before context check to avoid shutdown race.
		wg.Add(1)
		defer wg.Done()

		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := execute(ctx, schedule, command, args); err != nil {
			logger.Error("command execution error", "schedule", schedule, "command", command, "error", err)
		}
	})

	return c, wg, nil
}

// stop gracefully shuts down the scheduler with a timeout.
func stop(c *cron.Cron, wg *sync.WaitGroup) {
	logger.Info("stopping scheduler")
	c.Stop()
	logger.Info("waiting for running jobs to complete", "timeout", shutdownTimeout)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		logger.Info("scheduler stopped successfully")
	case <-time.After(shutdownTimeout):
		logger.Warn("scheduler shutdown timeout reached, forcing exit")
	}
}

// showVersion prints build version information.
func showVersion() {
	fmt.Printf("go-cron version %s\n", version)
	fmt.Printf("commit: %s\n", commit)
	fmt.Printf("built: %s\n", date)
	fmt.Printf("built by: %s\n", builtBy)
}

// main parses arguments and runs the scheduler with signal handling.
func main() {
	if len(os.Args) >= 2 && os.Args[1] == "version" {
		showVersion()
		return
	}

	if len(os.Args) < minArgs {
		fmt.Println("Usage: go-cron [schedule] [command] [args ...]")
		fmt.Println("       go-cron version")
		os.Exit(1)
	}

	schedule := os.Args[1]
	command := os.Args[2]
	args := os.Args[3:]

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c, wg, err := create(ctx, schedule, command, args)
	if err != nil {
		logger.Error("failed to create scheduler", "error", err)
		os.Exit(1)
	}

	c.Start()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigChan
	logger.Info("received signal", "signal", sig)

	cancel()
	stop(c, wg)
	os.Exit(0)
}
