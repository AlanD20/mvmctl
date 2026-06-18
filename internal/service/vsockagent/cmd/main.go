// Command vsockagent is the guest agent binary that runs inside the Firecracker VM.
// It listens on a vsock port and accepts JSON commands from the host.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"strings"

	"mvmctl/internal/lib/version"
	"mvmctl/internal/service/vsockagent"
)

func main() {
	port := flag.Int("port", 1024, "vsock port to listen on")
	token := flag.String("token", "", "auth token (overrides -token-file)")
	tokenFile := flag.String("token-file", "/var/run/mvm-vsock-agent.token", "path to auth token file")
	versionFlag := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	// Propagate ldflags-set BuildVersion into VersionString().
	version.SetBuildVersion(version.BuildVersion)

	if *versionFlag {
		fmt.Println(version.VersionString())
		os.Exit(0)
	}

	// Token resolution order:
	//   1. -token flag (explicit value, highest priority)
	//   2. -token-file path (read from file at startup)
	//   3. No token (skip auth)
	resolvedToken := *token
	if resolvedToken == "" {
		if data, err := os.ReadFile(*tokenFile); err == nil {
			resolvedToken = strings.TrimSpace(string(data))
			slog.Debug("loaded token from file", "path", *tokenFile)
		}
	}

	// Create context that is cancelled on SIGTERM or SIGINT.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill)
	defer stop()

	agent := vsockagent.New(*port, resolvedToken)
	slog.Info("starting guest agent", "port", *port, "auth", resolvedToken != "")

	if err := agent.Run(ctx); err != nil {
		log.Fatalf("agent exited with error: %v", err)
	}
}
