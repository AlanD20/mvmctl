package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"mvmctl/internal/app"
	"mvmctl/internal/service/nocloudnet"
)

func main() {
	// Hidden subcommand for nocloud server (spawned as standalone process)
	if len(os.Args) > 1 && os.Args[1] == "_nocloud_serve" {
		nocloudnet.ServeNoCloudHTTP(os.Args[2:])
		return
	}

	// Hidden subcommand for loop-mount provisioning (invoked via sudo)
	if len(os.Args) > 1 && os.Args[1] == "_provision" {
		runProvision(os.Args[2:])
		return
	}

	// SIGTERM handler: match Python's _handle_sigterm (sys.exit(128+15)=143).
	sigtermCh := make(chan os.Signal, 1)
	signal.Notify(sigtermCh, syscall.SIGTERM)
	go func() {
		<-sigtermCh
		os.Exit(143)
	}()

	// SIGINT handler: graceful context cancellation (exit code 130 matches Python).
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT)
	defer stop()

	app.Run(ctx)
}
