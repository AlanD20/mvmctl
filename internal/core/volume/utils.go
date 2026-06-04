package volume

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"mvmctl/internal/infra/model"
	"mvmctl/internal/infra/system"
)

// VolumesToDrives converts volume items to Firecracker drive configurations.
// Matches Python's VolumeService.volumes_to_drives().
func VolumesToDrives(vols []*model.VolumeItem) []model.DriveConfig {
	drives := make([]model.DriveConfig, 0, len(vols))
	for _, vol := range vols {
		if vol == nil {
			continue
		}
		drives = append(drives, model.DriveConfig{
			DriveID:      vol.ID,
			PathOnHost:   vol.Path,
			IsRootDevice: false,
			IsReadOnly:   vol.IsReadOnly,
		})
	}
	return drives
}

// formatProcessError formats an error from a subprocess command to match
// Python's ProcessError message format exactly.
//
// Python's ProcessError formats:
//   - Non-zero exit:   "Command failed (exit N): cmd\n[sanitized_stderr]"
//   - Command not found: "Command not found: cmd"
//   - Timeout:          "Command timed out after Ns: cmd"
//
// stderr is the captured stderr content (from an explicit buffer set on cmd.Stderr).
// If stderr is empty, falls back to exitErr.Stderr (populated by cmd.Output()).
func formatProcessError(cmdName string, stderr string, err error) string {
	var exitErr *exec.ExitError

	if !errors.As(err, &exitErr) {
		// Command not found — matches Python's FileNotFoundError → ProcessError("Command not found: cmd")
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Sprintf("Command not found: %s", cmdName)
		}
		// Other non-exit errors (e.g., context cancelled, permissions) — return raw error
		return err.Error()
	}

	// Non-zero exit — matches Python's CalledProcessError → ProcessError("Command failed (exit N): cmd")
	exitCode := exitErr.ExitCode()

	sanitized := sanitizeStderr(stderr)
	// Fall back to exitErr.Stderr (populated by cmd.Output() when cmd.Stderr was nil)
	if sanitized == "" && len(exitErr.Stderr) > 0 {
		sanitized = sanitizeStderr(string(exitErr.Stderr))
	}

	msg := fmt.Sprintf("Command failed (exit %d): %s", exitCode, cmdName)
	if sanitized != "" {
		msg += "\n" + sanitized
	}
	return msg
}

// sanitizeStderr matches Python's _sanitize_stderr(): strip, truncate to 100 chars, add "...".
func sanitizeStderr(stderr string) string {
	cleaned := strings.TrimSpace(stderr)
	const limit = 100
	if len(cleaned) > limit {
		return cleaned[:limit] + "..."
	}
	return cleaned
}

// GetDiskInfo returns disk information using qemu-img info.
// Matches Python's VolumeService.get_disk_info().
func GetDiskInfo(ctx context.Context, path string) (map[string]any, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, NewVolumeErrorf("Disk file not found: %s", path)
		}
		return nil, fmt.Errorf("stat disk file: %w", err)
	}

	result := system.RunCmdCompat(
		ctx,
		[]string{"qemu-img", "info", "--output=json", path},
		system.DefaultRunCmdOpts(),
	)
	if result.Err != nil {
		return nil, NewVolumeErrorf("qemu-img info failed: %s", result.Err.Error())
	}

	var data map[string]any
	if err := json.Unmarshal(result.StdoutBytes, &data); err != nil {
		return nil, fmt.Errorf("parse qemu-img info output: %w", err)
	}

	return data, nil
}
