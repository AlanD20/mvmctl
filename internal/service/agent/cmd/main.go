// Command agent is the guest agent binary that runs inside the Firecracker VM.
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

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/version"
	"mvmctl/internal/service/agent"
)

func main() {
	port := flag.Int("port", 1024, "vsock port to listen on")
	token := flag.String("token", "", "auth token (overrides -token-file)")
	tokenFile := flag.String("token-file", infra.GuestAgentTokenPath, "path to auth token file")
	versionFlag := flag.Bool("version", false, "print version and exit")
	localSocket := flag.String("local-socket", "/var/run/mvm-agent.sock",
		"path to the daemon's local Unix socket (used by 'remote' subcommand)")
	flag.Parse()

	// Propagate ldflags-set BuildVersion into VersionString().
	version.SetBuildVersion(version.BuildVersion)

	if *versionFlag {
		fmt.Println(version.VersionString())
		os.Exit(0)
	}

	// Check for subcommand mode: "remote" as first non-flag argument.
	if flag.NArg() > 0 && flag.Arg(0) == "remote" {
		os.Exit(runRemoteSubcommand(*localSocket, flag.Args()[1:]))
	}

	resolvedToken, err := resolveAgentToken(*token, *tokenFile, os.ReadFile)
	if err != nil {
		log.Fatalf("agent authentication unavailable: %v", err)
	}
	slog.Debug("loaded agent authentication", "from_file", *token == "", "path", *tokenFile)

	// Create context that is cancelled on SIGTERM or SIGINT.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill)
	defer stop()

	agent := agent.New(*port, resolvedToken, *localSocket)
	slog.Info("starting guest agent", "port", *port, "auth", resolvedToken != "")

	if err := agent.Run(ctx); err != nil {
		log.Fatalf("agent exited with error: %v", err)
	}
}

func resolveAgentToken(
	explicitToken string,
	tokenFile string,
	readFile func(string) ([]byte, error),
) (string, error) {
	if explicitToken != "" {
		return explicitToken, nil
	}
	if tokenFile == "" {
		return "", fmt.Errorf("agent token file path is required")
	}
	if readFile == nil {
		return "", fmt.Errorf("agent token file reader is required")
	}
	data, err := readFile(tokenFile)
	if err != nil {
		return "", fmt.Errorf("read agent token file %q: %w", tokenFile, err)
	}
	resolved := strings.TrimSpace(string(data))
	if resolved == "" {
		return "", fmt.Errorf("agent token file %q is empty", tokenFile)
	}
	return resolved, nil
}
