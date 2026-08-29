package main

import (
	"context"
	"log/slog"
	"os/signal"
	"syscall"

	"mvmctl/internal/app"
	"mvmctl/internal/cli"
	"mvmctl/internal/cli/common"
)

func runCLI() int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	op, cleanup, err := app.Initialize(ctx)
	if err != nil {
		slog.Error("initialization failed", "error", err)
		return 1
	}
	if cleanup != nil {
		defer cleanup()
	}

	rootCmd := cli.NewRootCmd(op)
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		// Delegate all public CLI error rendering to the shared handler.
		if common.HandleErrors(func() error { return err })() != nil {
			return 1
		}
	}
	return 0
}
