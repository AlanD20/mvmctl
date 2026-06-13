package ssh

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"time"
)

// ProbeUntilReady retries SSH probe commands until the VM responds or the
// total timeout expires. Unlike a TCP port check, an SSH probe confirms the
// VM is fully booted (cloud-init finished, no apt locks, network configured).
func ProbeUntilReady(ctx context.Context, ip, user, keyPath string, timeout time.Duration) (time.Duration, error) {
	deadline := time.Now().Add(timeout)
	attempt := 0

	for {
		attempt++
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 0, fmt.Errorf("SSH connection timed out after waiting %ds for VM to become reachable",
				int(timeout.Seconds()))
		}

		// Build a lightweight SSH probe: connect with a 2s timeout and run
		// "echo ok". If exit 0, the VM is fully booted and SSH-ready.
		probeArgs := buildSSHOpts(ip, user, keyPath, probeSSHTimeout)
		probeArgs = append(probeArgs, "echo ok")

		probeCtx, probeCancel := context.WithTimeout(ctx, probeSSHTimeout*time.Second)
		cmd := exec.CommandContext(probeCtx, probeArgs[0], probeArgs[1:]...)
		cmd.Stdout = nil
		cmd.Stderr = nil
		err := cmd.Run()
		probeCancel()

		if err == nil {
			// SSH command succeeded — VM is ready.
			if attempt > 1 {
				slog.Debug(
					"VM SSH-ready after probe",
					"attempts",
					attempt,
					"elapsed",
					time.Since(deadline.Add(-timeout)).String(),
				)
			}
			return remaining, nil
		}

		// Probe failed (connection refused, timeout, auth error, etc.)
		// — retry after a short interval.
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(probeInterval):
		}
	}
}

// buildSSHOpts builds the base SSH argument list shared by all SSH connections.
// connectTimeout is in seconds; 0 means no ConnectTimeout flag.
func buildSSHOpts(ip, user, keyPath string, connectTimeout int) []string {
	opts := []string{
		"ssh",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "BatchMode=yes",
		"-o", "LogLevel=ERROR",
		"-o", "ServerAliveInterval=2",
		"-o", "ServerAliveCountMax=3",
	}
	if connectTimeout > 0 {
		opts = append(opts, "-o", fmt.Sprintf("ConnectTimeout=%d", connectTimeout))
	}
	if keyPath != "" {
		opts = append(opts, "-i", keyPath)
	}
	opts = append(opts, fmt.Sprintf("%s@%s", user, ip))
	return opts
}
