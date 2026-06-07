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
	// CloudInitDir is the single-directory mode — all cloud-init files live here.
	// Used in non-batch contexts (e.g. single-VM res pawn).
	CloudInitDir string

	// BaseDir is the shared batch directory containing per-VM subdirectories.
	// The URL path /<vm-id>/<file> resolves to <BaseDir>/<vm-id>/<file>.
	// Falls back to <BaseDir>/common/<file> for shared files.
	// Only one of CloudInitDir or BaseDir should be set.
	BaseDir string

	Port      int
	Host      string
	LogFile   string
	KillAfter time.Duration
}

// Run starts the NoCloud HTTP metadata server with the given config.
func Run(ctx context.Context, cfg Config) error {
	if cfg.Port == 0 {
		return fmt.Errorf("port is required")
	}

	var handler *cloudInitRequestHandler
	if cfg.BaseDir != "" {
		info, err := os.Stat(cfg.BaseDir)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("base directory does not exist: %s", cfg.BaseDir)
		}
		handler = &cloudInitRequestHandler{baseDir: cfg.BaseDir, singleDir: false}
	} else {
		info, err := os.Stat(cfg.CloudInitDir)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("cloud-init directory does not exist: %s", cfg.CloudInitDir)
		}
		handler = &cloudInitRequestHandler{baseDir: cfg.CloudInitDir, singleDir: true}
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
		Handler: handler,
	}

	baseDesc := cfg.BaseDir
	if baseDesc == "" {
		baseDesc = cfg.CloudInitDir
	}
	fmt.Fprintf(logFP, "NoCloud-net HTTP server starting on %s\n", server.Addr)
	fmt.Fprintf(logFP, "Serving cloud-init files from: %s\n", baseDesc)

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
