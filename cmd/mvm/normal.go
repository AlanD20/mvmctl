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

func runCLI() int {
	common.BeginOutput(os.Args[1:])
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	op, cleanup, err := app.Initialize(ctx)
	if err != nil {
		if common.StructuredOutput() {
			if outputErr := common.FinishOutput(os.Stdout, err); outputErr != nil {
				return 1
			}
		} else {
			slog.Error("initialization failed", "error", err)
		}
		return 1
	}
	if cleanup != nil {
		defer cleanup()
	}

	rootCmd := cli.NewRootCmd(op)
	err = rootCmd.ExecuteContext(ctx)
	if common.StructuredOutput() {
		if common.FinishOutput(os.Stdout, err) != nil {
			return 1
		}
		return 0
	}
	if err != nil {
		// Delegate all public CLI error rendering to the shared handler.
		if common.HandleErrors(func() error { return err })() != nil {
			return 1
		}
	}
	return 0
}
