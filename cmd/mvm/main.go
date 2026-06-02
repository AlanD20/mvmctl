package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"mvmctl/internal/app"
	"mvmctl/internal/service/console"
	"mvmctl/internal/service/loopmount"
	"mvmctl/internal/service/nocloudnet"
)

func main() {
	// "mvm run <service>" dispatches to subprocess or in-process services.
	if len(os.Args) > 2 && os.Args[1] == "run" {
		switch os.Args[2] {
		case "nocloud-serve":
			nocloudnet.ServeNoCloudHTTP(context.Background(), os.Args[3:])
		case "console-relay":
			console.RunRelaySubprocess(os.Args[3:])
		case "provision":
			loopmount.RunProvision(os.Args[3:])
		default:
			fmt.Fprintf(os.Stderr, "Unknown service: %s\n", os.Args[2])
			os.Exit(1)
		}
		return
	}

	// Both SIGINT and SIGTERM cancel the context, giving in-flight operations a
	// chance to clean up. Exit code matches signal: 128 + signal number.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var exitCode int
	go func() {
		sig := <-sigCh
		exitCode = 128 + int(sig.(syscall.Signal))
		cancel()
	}()

	app.Run(ctx)
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}
