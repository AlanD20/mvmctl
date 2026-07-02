// Package update handles self-update for the mvm CLI binary.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"mvmctl/internal/lib/download"
	"mvmctl/internal/lib/version"

	"mvmctl/internal/infra"
)

// Service handles checking for and applying updates to the mvm binary.
type Service struct {
	gh *download.Remote
}

// NewService creates a new update Service for the mvmctl GitHub repo.
func NewService() *Service {
	return &Service{
		gh: download.NewGitHub(infra.MvmctlGitHubRepo),
	}
}

// CheckResult holds the result of a version check.
type CheckResult struct {
	CurrentVersion string
	LatestVersion  string
	HasUpdate      bool
}

// Check compares current version against latest GitHub release.
func (s *Service) Check(ctx context.Context) (*CheckResult, error) {
	rel, err := s.gh.LatestRelease(ctx)
	if err != nil {
		return nil, fmt.Errorf("check for update: %w", err)
	}

	latest := strings.TrimPrefix(rel.TagName, "v")
	current := cleanVersion(version.GetVersion(ctx))

	hasUpdate := version.Compare(latest, current) > 0

	return &CheckResult{
		CurrentVersion: current,
		LatestVersion:  latest,
		HasUpdate:      hasUpdate,
	}, nil
}

// Apply downloads and installs the latest version.
func (s *Service) Apply(ctx context.Context, force bool) error {
	rel, err := s.gh.LatestRelease(ctx)
	if err != nil {
		return fmt.Errorf("apply update: %w", err)
	}

	latest := strings.TrimPrefix(rel.TagName, "v")
	current := cleanVersion(version.GetVersion(ctx))

	if !force && version.Compare(latest, current) <= 0 {
		return fmt.Errorf("already up to date (v%s)", current)
	}

	// Find asset for current arch
	assetName := "mvm"
	if runtime.GOARCH == "arm64" {
		assetName = "mvm-arm64"
	}

	var binaryAsset *download.Asset
	var checksumAsset *download.Asset
	for _, a := range rel.Assets {
		if a.Name == assetName {
			binaryAsset = &a
		}
		if a.Name == "checksums.sha256" {
			checksumAsset = &a
		}
	}

	if binaryAsset == nil {
		return fmt.Errorf("no binary asset found for %s (assets: %d)", assetName, len(rel.Assets))
	}

	// Get current binary path
	currentPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("detect current binary path: %w", err)
	}

	// Ensure directory is writable
	dir := filepath.Dir(currentPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("ensure directory writable: %w", err)
	}

	// Download to temp file alongside current binary
	tmpPath := filepath.Join(dir, ".mvm.update")
	defer os.Remove(tmpPath)

	dl := download.New()
	if _, err := dl.WithDownload(ctx, binaryAsset.URL, tmpPath, nil, nil); err != nil {
		return fmt.Errorf("download binary: %w", err)
	}

	// Verify checksum if available
	if checksumAsset != nil {
		// Download checksums content in-memory
		checksumContent, err := dl.GetContent(ctx, download.RequestOpts{
			URL:     checksumAsset.URL,
			Timeout: 30,
		})
		if err != nil {
			return fmt.Errorf("download checksums: %w", err)
		}

		if err := verifyChecksum(checksumContent, tmpPath, assetName); err != nil {
			return fmt.Errorf("checksum verification failed: %w", err)
		}
	}

	// Atomic swap
	if err := os.Rename(tmpPath, currentPath); err != nil {
		return fmt.Errorf("install binary: %w", err)
	}

	// Restore permissions
	_ = os.Chmod(currentPath, 0755)

	return nil
}

// cleanVersion strips git metadata and the "v" prefix from a version string.
func cleanVersion(v string) string {
	v = strings.TrimPrefix(v, "v")
	if idx := strings.IndexAny(v, "+-"); idx >= 0 {
		v = v[:idx]
	}
	return v
}

// verifyChecksum checks that the binary at binaryPath matches the checksums
// content for assetName.
func verifyChecksum(checksumContent, binaryPath, assetName string) error {
	binaryData, err := os.ReadFile(binaryPath)
	if err != nil {
		return fmt.Errorf("read binary for checksum: %w", err)
	}
	hash := sha256.Sum256(binaryData)
	hexHash := hex.EncodeToString(hash[:])

	// Parse checksums file (format: <hash>  <filename>)
	for _, line := range strings.Split(checksumContent, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 && parts[1] == assetName {
			if parts[0] == hexHash {
				return nil
			}
			return fmt.Errorf("hash mismatch: expected %s, got %s", parts[0], hexHash)
		}
	}

	return fmt.Errorf("no checksum entry found for %s in checksums file", assetName)
}
