package console

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/system"
)

// SpawnResult holds the result of a successful spawn.
type SpawnResult struct {
	SocketPath string
	PID        int
}

// Spawn starts a console relay subprocess with the given config and PTY file.
// Writes a PID file alongside the socket for external process tracking.
func Spawn(ctx context.Context, cfg Config, ptyFile *os.File) (*SpawnResult, error) {
	args := []string{
		"--vm-id", cfg.VMID,
		"--vm-path", cfg.VMPath,
		"--pty-fd", "3",
	}
	if cfg.VMName != "" {
		args = append(args, "--vm-name", cfg.VMName)
	}

	cmd, err := system.SpawnService(nil, system.SpawnConfig{
		Name:       "console",
		ExtraFiles: []*os.File{ptyFile},
		Args:       append([]string{"relay"}, args...),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to spawn console relay: %w", err)
	}

	pid := cmd.Process.Pid

	// Write PID file alongside the socket.
	pidFile := filepath.Join(cfg.VMPath, DefaultConsolePIDFilename)
	if pidDir := filepath.Dir(pidFile); pidDir != "." {
		os.MkdirAll(pidDir, infra.DirPerm) //nolint:errcheck
	}
	os.WriteFile(pidFile, []byte(strconv.Itoa(pid)), 0644) //nolint:errcheck

	// Wait for subprocess to create the socket.
	socketPath := filepath.Join(cfg.VMPath, DefaultConsoleSocketFilename)
	for range 50 {
		if _, err := os.Stat(socketPath); err == nil {
			return &SpawnResult{
				SocketPath: socketPath,
				PID:        pid,
			}, nil
		}
		time.Sleep(50 * time.Millisecond)
	}

	return nil, fmt.Errorf("console relay socket %s did not appear within 2.5s", socketPath)
}
