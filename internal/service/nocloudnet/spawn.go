package nocloudnet

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/system"
)

// SpawnResult holds the result of a successful spawn.
type SpawnResult struct {
	URL  string
	Port int
	PID  int
}

// Spawn starts a nocloud-net HTTP server subprocess and waits for it to be ready.
// The caller is responsible for providing a valid port via Config.Port.
// Use infra.FindFreePort to discover a free port before calling Spawn.
// Writes a PID file alongside the log file for external process tracking.
func Spawn(ctx context.Context, cfg Config) (*SpawnResult, error) {
	dirFlag := "--cloud-init-dir"
	dirVal := cfg.CloudInitDir
	if cfg.BaseDir != "" {
		dirFlag = "--base-dir"
		dirVal = cfg.BaseDir
	}
	cmd, err := system.SpawnService(nil, system.SpawnConfig{
		Name: "nocloudnet",
		Args: append([]string{"serve"},
			dirFlag, dirVal,
			"--port", fmt.Sprintf("%d", cfg.Port),
			"--host", cfg.Host,
			"--log-file", cfg.LogFile,
		),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to spawn nocloud-net server: %w", err)
	}

	pid := cmd.Process.Pid

	// Write PID file alongside the log file.
	pidFile := filepath.Join(filepath.Dir(cfg.LogFile), "nocloud-server.pid")
	if pidDir := filepath.Dir(pidFile); pidDir != "." {
		os.MkdirAll(pidDir, infra.DirPerm) //nolint:errcheck
	}
	os.WriteFile(pidFile, []byte(strconv.Itoa(pid)), 0644) //nolint:errcheck

	// Wait for the server to start listening.
	for range 50 {
		conn, dialErr := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", cfg.Host, cfg.Port), 100*time.Millisecond)
		if dialErr == nil {
			conn.Close()
			return &SpawnResult{
				URL:  fmt.Sprintf("http://%s:%d/", cfg.Host, cfg.Port),
				Port: cfg.Port,
				PID:  pid,
			}, nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	return nil, fmt.Errorf("nocloud-net server did not start listening within 5s")
}
