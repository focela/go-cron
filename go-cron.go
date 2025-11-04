/*
 * Copyright 2025 Focela Authors.
 *
 * SPDX-License-Identifier: Apache-2.0
 *
 * Licensed under the Apache License, Version 2.0.
 * See the LICENSE file in the project root for full license information.
 */

// Package main provides a cron scheduler that waits for running jobs to complete before exiting.
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
	minArgs = 3
)

// execute runs command with args and blocks until completion. Returns error on failure or cancel.
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

// create initializes cron scheduler with schedule and command. Returns scheduler, WaitGroup, and error.
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
		// Check ctx before incrementing WaitGroup to avoid race on shutdown.
		select {
		case <-ctx.Done():
			return
		default:
		}

		wg.Add(1)
		defer wg.Done()

		if err := execute(ctx, schedule, command, args); err != nil {
			logger.Error("command execution error", "schedule", schedule, "command", command, "error", err)
		}
	})

	return c, wg, nil
}

// stop shuts down scheduler and blocks until all running jobs complete.
func stop(c *cron.Cron, wg *sync.WaitGroup) {
	logger.Info("stopping scheduler")
	c.Stop()
	logger.Info("waiting for running jobs to complete")
	wg.Wait()
	logger.Info("scheduler stopped successfully")
}

// showVersion prints version information to stdout.
func showVersion() {
	fmt.Printf("go-cron version %s\n", version)
	fmt.Printf("commit: %s\n", commit)
	fmt.Printf("built: %s\n", date)
	fmt.Printf("built by: %s\n", builtBy)
}

// main parses args and runs cron scheduler with SIGINT/SIGTERM signal handling.
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
