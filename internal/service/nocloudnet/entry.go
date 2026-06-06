package nocloudnet

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Config holds configuration for the NoCloud HTTP metadata server.
type Config struct {
	CloudInitDir string
	Port         int
	Host         string
	LogFile      string
	KillAfter    time.Duration
}

// Run starts the NoCloud HTTP metadata server with the given config.
// This is the canonical entry point for foreground execution.
// Port is required — use FindFreePort in callers to discover a free port.
// For background execution, use Spawn() instead.
func Run(ctx context.Context, cfg Config) error {
	// Validate port is set.
	if cfg.Port == 0 {
		return fmt.Errorf("port is required")
	}

	// Validate cloud-init directory exists.
	info, err := os.Stat(cfg.CloudInitDir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("cloud-init directory does not exist: %s", cfg.CloudInitDir)
	}

	// Open log file and redirect stdout/stderr to it.
	logFP, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("cannot open log file: %w", err)
	}
	defer logFP.Close()
	os.Stdout = logFP
	os.Stderr = logFP

	server := &http.Server{
		Addr:    fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Handler: &cloudInitRequestHandler{cloudInitDir: cfg.CloudInitDir},
	}

	fmt.Fprintf(logFP, "NoCloud-net HTTP server starting on %s\n", server.Addr)
	fmt.Fprintf(logFP, "Serving cloud-init files from: %s\n", cfg.CloudInitDir)

	// Shutdown triggers: OS signal (SIGTERM/SIGINT) or auto-kill timer.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		if cfg.KillAfter > 0 {
			select {
			case <-sigCh:
				fmt.Fprintf(logFP, "NoCloud-net server received signal, shutting down...\n")
			case <-time.After(cfg.KillAfter):
				fmt.Fprintf(logFP, "NoCloud-net server auto-kill timer expired after %v, shutting down\n", cfg.KillAfter)
			}
		} else {
			<-sigCh
			fmt.Fprintf(logFP, "NoCloud-net server received signal, shutting down...\n")
		}
		server.Shutdown(context.Background())
	}()

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("error starting server: %w", err)
	}

	server.Close()
	fmt.Fprintf(logFP, "NoCloud-net HTTP server stopped\n")
	return nil
}
