package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"mvmctl/internal/app"
	"mvmctl/internal/cli"
	"mvmctl/internal/cli/common"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	op, cleanup, err := app.Initialize(ctx)
	if err != nil {
		slog.Error("initialization failed", "error", err)
		os.Exit(1)
	}
	if cleanup != nil {
		defer cleanup()
	}

	// Execute CLI
	rootCmd := cli.NewRootCmd(op)
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		// Delegate ALL error handling to the single shared handler in helpers.go.
		common.HandleErrors(func() error { return err })()
	}
}
