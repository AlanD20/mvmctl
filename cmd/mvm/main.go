package main

import (
	"context"
	"log/slog"
	"os"

	"mvmctl/internal/app"
	"mvmctl/internal/cli/common"
	"mvmctl/internal/service/jailer"
	"mvmctl/internal/service/privileged"
)

type entrypoints struct {
	launchConsole     func(context.Context, []string) error
	launchChild       func(context.Context, []string) error
	privileged        func(context.Context, []string) error
	systemInstall     func(context.Context, []string) error
	hostAdministrator func(context.Context, []string) (*app.HostAdministratorResult, error)
	normal            func() int
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], entrypoints{
		launchConsole:     jailer.RunLaunchConsole,
		launchChild:       jailer.RunLaunchChild,
		privileged:        privileged.Run,
		systemInstall:     app.RunHostInstallSystemBinary,
		hostAdministrator: app.RunHostAdministrator,
		normal:            runCLI,
	}))
}

func run(ctx context.Context, args []string, entries entrypoints) int {
	if jailer.IsLaunchConsoleInvocation(args) {
		// The console relay must never enter ordinary CLI initialization, even
		// when its inherited setup channel rejects an unauthenticated invocation.
		if err := entries.launchConsole(ctx, args); err != nil {
			slog.Error("launch console failed", "error", err)
			return 1
		}
		return 0
	}
	if jailer.IsLaunchChildInvocation(args) {
		// The child owns a locked namespace thread. Every result returns directly
		// to main's os.Exit; it must never enter ordinary application initialization.
		if err := entries.launchChild(ctx, args); err != nil {
			slog.Error("launch child failed", "error", err)
			return 1
		}
		return 0
	}
	if privileged.IsInvocation(args) {
		if err := entries.privileged(ctx, args); err != nil {
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
	if app.IsHostAdministratorInvocation(args) {
		return runHostAdministrator(ctx, args, entries.hostAdministrator)
	}
	return entries.normal()
}

func runHostAdministrator(
	ctx context.Context,
	args []string,
	entry func(context.Context, []string) (*app.HostAdministratorResult, error),
) int {
	common.BeginOutput(args)
	result, err := entry(ctx, args)
	if common.StructuredOutput() {
		if result != nil && err != nil {
			common.RecordPartialResult(result)
		} else {
			common.RecordResult(result)
		}
		if common.FinishOutput(os.Stdout, err) != nil {
			return 1
		}
		return 0
	}
	if err != nil {
		if common.HandleErrors(func() error { return err })() != nil {
			return 1
		}
		return 0
	}
	common.Cli.Success("host administration completed")
	if result != nil && result.UserAddedToGroup {
		common.Cli.Info("log out and back in to activate mvm group membership")
	}
	return 0
}
