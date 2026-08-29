package jailer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"mvmctl/internal/lib/system"
)

// InstallRelease installs an already checksum-verified release archive into the trusted store.
func InstallRelease(
	ctx context.Context,
	version, arch, archivePath, expectedSHA256 string,
) (*InstallResult, error) {
	archiveFile, err := os.Open(archivePath)
	if err != nil {
		return nil, fmt.Errorf("open verified Firecracker archive: %w", err)
	}
	defer archiveFile.Close()

	var stdout, stderr bytes.Buffer
	cmd, err := system.SpawnService(ctx, system.SpawnConfig{
		Name:       "jailer",
		Privileged: true,
		Stdin:      archiveFile,
		Stdout:     &stdout,
		Stderr:     &stderr,
		Args: []string{
			"install", "--version", version, "--arch", arch, "--sha256", expectedSHA256,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("spawn trusted pair installer: %w", err)
	}
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("trusted pair installer failed: %s: %w", stderr.String(), err)
	}
	var result InstallResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return nil, fmt.Errorf("decode trusted pair installer result: %w", err)
	}
	return &result, nil
}

// Launch starts the privileged Jailer service for a validated per-VM manifest.
func Launch(
	ctx context.Context,
	vmID, vmDir string,
	stdin *os.File,
	stdout, stderr *os.File,
) (*os.Process, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// The VM must outlive the foreground CLI command; lifecycle controllers own shutdown.
	cmd, err := system.SpawnService(nil, system.SpawnConfig{
		Name:       "jailer",
		Privileged: true,
		Stdin:      stdin,
		Stdout:     stdout,
		Stderr:     stderr,
		Args:       []string{"launch", "--vm-id", vmID, "--vm-dir", vmDir},
	})
	if err != nil {
		return nil, err
	}
	return cmd.Process, nil
}

// Cleanup removes mounts and the chroot for one VM. It is idempotent.
func Cleanup(ctx context.Context, vmID string) error {
	return runAndWait(ctx, []string{"cleanup", "--vm-id", vmID})
}

// ExposeSnapshot bind-mounts one snapshot directory into an existing jail.
func ExposeSnapshot(ctx context.Context, vmID, snapshotDir string) error {
	vmDir := vmDirForManagedResource(vmID, snapshotDir)
	return runAndWait(ctx, []string{
		"expose-snapshot", "--vm-id", vmID, "--vm-dir", vmDir, "--snapshot-dir", snapshotDir,
	})
}

// ExposeVolume bind-mounts one volume into an existing jail.
func ExposeVolume(ctx context.Context, vmID, driveID, hostPath string, readOnly bool) error {
	vmDir := vmDirForManagedResource(vmID, hostPath)
	return runAndWait(ctx, []string{
		"expose-volume", "--vm-id", vmID, "--vm-dir", vmDir, "--drive-id", driveID,
		"--host-path", hostPath, "--read-only=" + strconv.FormatBool(readOnly),
	})
}

func vmDirForManagedResource(vmID, resourcePath string) string {
	cacheDir := filepath.Dir(filepath.Dir(filepath.Clean(resourcePath)))
	return filepath.Join(cacheDir, "vms", vmID)
}

// RemoveVolume unmounts one volume from an existing jail.
func RemoveVolume(ctx context.Context, vmID, driveID string) error {
	return runAndWait(ctx, []string{"remove-volume", "--vm-id", vmID, "--drive-id", driveID})
}

// RemoveRelease removes one trusted exact-version release directory.
func RemoveRelease(ctx context.Context, version string) error {
	return runAndWait(ctx, []string{"remove-release", "--version", version})
}

func runAndWait(ctx context.Context, args []string) error {
	var stderr bytes.Buffer
	cmd, err := system.SpawnService(ctx, system.SpawnConfig{
		Name:       "jailer",
		Privileged: true,
		Stderr:     &stderr,
		Args:       args,
	})
	if err != nil {
		return err
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("jailer service failed: %s: %w", stderr.String(), err)
	}
	return nil
}
