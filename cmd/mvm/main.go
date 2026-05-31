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
