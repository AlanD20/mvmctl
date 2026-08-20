package main

import (
	"context"
	"io"
	"log/slog"
	"os"

	"mvmctl/internal/app"
	"mvmctl/internal/service/privileged"
)

type entrypoints struct {
	privileged    func(context.Context, []string, io.Reader) error
	systemInstall func(context.Context, []string) error
	normal        func() int
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdin, entrypoints{
		privileged:    privileged.Run,
		systemInstall: app.RunHostInstallSystemBinary,
		normal:        runCLI,
	}))
}

func run(ctx context.Context, args []string, input io.Reader, entries entrypoints) int {
	if privileged.IsInvocation(args) {
		if err := entries.privileged(ctx, args, input); err != nil {
			slog.Error("privileged request failed", "error", err)
			return 1
		}
		return 0
	}
	if app.IsHostInstallSystemBinaryInvocation(args) {
		if err := entries.systemInstall(ctx, args); err != nil {
			slog.Error("system binary installation failed", "error", err)
			return 1
		}
		return 0
	}
	return entries.normal()
}
