package guestfs

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/system"
	"mvmctl/pkg/errs"
)

const applianceBuildTimeout = 60 * time.Second

// --- Low-level helpers ---

// doTruncateFile expands a file to targetSize if it's smaller.
func doTruncateFile(path string, targetSize int64) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	if info.Size() < targetSize {
		if err := os.Truncate(path, targetSize); err != nil {
			slog.Warn("Failed to truncate file", "path", path, "error", err)
		}
	}
}

// --- Appliance management ---

// BuildAppliance builds the libguestfs fixed appliance for faster image ops.
// Uses KernelDetector to find a suitable upstream kernel with virtio drivers.
//
// Returns the path to the appliance directory if built, or empty string if
// skipped or failed.
func BuildAppliance(ctx context.Context, cacheDir string) (string, error) {
	makeTool, err := exec.LookPath("libguestfs-make-fixed-appliance")
	if err != nil {
		slog.Debug("libguestfs-make-fixed-appliance not found — skipping appliance build")
		return "", nil
	}

	applianceDir := filepath.Join(cacheDir, "appliance")
	if err := os.MkdirAll(applianceDir, infra.DirPerm); err != nil {
		return "", fmt.Errorf("create appliance dir: %w", err)
	}

	// Check if appliance already exists (has kernel, initrd, root files)
	requiredFiles := map[string]bool{"kernel": false, "initrd": false, "root": false}
	entries, err := os.ReadDir(applianceDir)
	if err == nil {
		for _, e := range entries {
			if _, ok := requiredFiles[e.Name()]; ok {
				requiredFiles[e.Name()] = true
			}
		}
		allPresent := true
		for _, present := range requiredFiles {
			if !present {
				allPresent = false
				break
			}
		}
		if allPresent {
			slog.Debug("libguestfs appliance already present", "path", applianceDir)
			return applianceDir, nil
		}
	}

	// Build environment
	env := os.Environ()
	kd := &KernelDetector{}
	kernelPath, modulesDir, kerr := kd.FindBestKernel(ctx)
	if kerr == nil && kernelPath != "" {
		env = append(env,
			"SUPERMIN_KERNEL="+kernelPath,
			"SUPERMIN_MODULES="+modulesDir,
		)
		slog.Debug("Forcing libguestfs appliance build with kernel",
			"kernel", kernelPath,
		)
	} else {
		slog.Warn("No suitable kernel with virtio drivers found in /boot — " +
			"appliance build may hang if the auto-selected kernel lacks virtio")
	}

	// Build environment map for RunCmdCompat (includes current env + kernel overrides)
	runEnv := make(map[string]string, len(env))
	for _, e := range env {
		if key, val, found := strings.Cut(e, "="); found && key != "" {
			runEnv[key] = val
		}
	}

	cmdCtx, cmdCancel := context.WithTimeout(ctx, applianceBuildTimeout)
	defer cmdCancel()

	result, err := system.DefaultRunner.Run(cmdCtx, []string{makeTool, applianceDir}, system.RunCmdOpts{
		Capture: true,
		Check:   true,
		Timeout: applianceBuildTimeout,
		Env:     runEnv,
	})
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "timed out") {
			slog.Warn("libguestfs appliance build timed out after 60s")
			return "", nil
		}
		if strings.Contains(errStr, "Command not found") {
			slog.Warn("libguestfs-make-fixed-appliance command not found")
			return "", nil
		}
		slog.Warn("libguestfs appliance build failed",
			"error", err,
			"output", result.Stdout+result.Stderr,
		)
		return "", nil
	}

	slog.Info("libguestfs fixed appliance built", "path", applianceDir)
	return applianceDir, nil
}

// PruneAppliance removes the libguestfs appliance folder from the selected cache.
func PruneAppliance(cacheDir string, dryRun bool) bool {
	applianceDir := filepath.Join(cacheDir, "appliance")
	removed := false

	if _, err := os.Stat(applianceDir); err == nil {
		if !dryRun {
			if err := os.RemoveAll(applianceDir); err == nil {
				removed = true
			}
		} else {
			removed = true
		}
	}

	return removed
}

// EnsureAppliance checks if the libguestfs fixed appliance cache exists.
// Returns an error with a helpful message if the appliance has not been built.
func EnsureAppliance(cacheDir string) error {
	applianceDir := filepath.Join(cacheDir, "appliance")
	required := map[string]bool{"kernel": false, "initrd": false, "root": false}

	entries, err := filepath.Glob(filepath.Join(applianceDir, "*"))
	if err != nil || len(entries) == 0 {
		return errs.New(errs.CodeGuestfsError, "libguestfs appliance cache not found. Run: mvm cache init")
	}

	for _, entry := range entries {
		name := filepath.Base(entry)
		if _, ok := required[name]; ok {
			required[name] = true
		}
	}

	for _, found := range required {
		if !found {
			return errs.New(errs.CodeGuestfsError, "libguestfs appliance cache not found. Run: mvm cache init")
		}
	}

	return nil
}
