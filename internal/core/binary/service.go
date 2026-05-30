package binary

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/download"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
	"mvmctl/internal/infra/system"
	"mvmctl/internal/infra/version"
)

// safeTagChars matches characters unsafe for filenames in mirror tags.
var safeTagChars = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

// Service is the stateless intra-domain orchestrator for binary operations.
// Mirrors Python mvmctl.core.binary._service.BinaryService exactly.
type Service struct {
	repo     Repository
	binDir   string
	cacheDir string
	dl       *download.Downloader
}

func NewService(repo Repository, binDir, cacheDir string) *Service {
	return &Service{
		repo:     repo,
		binDir:   binDir,
		cacheDir: cacheDir,
		dl:       download.New(),
	}
}

// ── List / Query ───────────────────────────────────────────────────────────

// ListAll lists all binaries, syncing is_present flag with filesystem.
func (s *Service) ListAll(ctx context.Context, remote bool, verify bool) ([]*model.BinaryItem, error) {
	binaries, err := s.repo.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	if !verify {
		return binaries, nil
	}

	var missingIDs []string
	for i := range binaries {
		if _, err := os.Stat(binaries[i].Path); os.IsNotExist(err) {
			missingIDs = append(missingIDs, binaries[i].ID)
			binaries[i].IsPresent = false
		}
	}

	if len(missingIDs) > 0 {
		if err := s.repo.UpdateManyIsPresent(ctx, missingIDs, false); err != nil {
			return nil, err
		}
	}

	return binaries, nil
}

// GetDefaultFirecracker returns the default firecracker binary, or nil if not set.
func (s *Service) GetDefaultFirecracker(ctx context.Context) (*model.BinaryItem, error) {
	return s.repo.GetDefault(ctx, "firecracker")
}

// ── Remote listing ─────────────────────────────────────────────────────────

// ListRemote fetches Firecracker release versions from GitHub.
func (s *Service) ListRemote(ctx context.Context, limit int) ([]string, error) {
	url := fmt.Sprintf("%s?per_page=%d", infra.FirecrackerGithubReleasesAPIURL, limit)

	raw, err := s.dl.GetContent(ctx, url, 30, map[string]string{"Accept": "application/json"}, true, 300)
	if err != nil {
		return nil, mapGitHubAPIError(err)
	}

	var releases []githubRelease
	if err := json.Unmarshal([]byte(raw), &releases); err != nil {
		return nil, binaryError(errs.CodeDownloadFailed,
			fmt.Sprintf("Unexpected response from GitHub: %v", err))
	}

	versions := make([]string, 0, len(releases))
	for _, rel := range releases {
		if rel.TagName != "" {
			versions = append(versions, NormalizeVersion(rel.TagName))
		}
	}

	sort.Slice(versions, func(i, j int) bool {
		return version.SemverGreater(versions[i], versions[j])
	})

	return versions, nil
}

// ── Download ───────────────────────────────────────────────────────────────

// DownloadFirecracker downloads firecracker + jailer for version, returns Binary list.
// arch is the target architecture (e.g. "x86_64", "aarch64") used in download URLs
// and tarball member names.
func (s *Service) DownloadFirecracker(ctx context.Context, version string, arch string, onProgress infra.ProgressCallback) ([]*model.BinaryItem, error) {
	normalizedVersion := NormalizeVersion(version)

	fcDest := filepath.Join(s.binDir, fmt.Sprintf("firecracker-v%s", normalizedVersion))
	jlDest := filepath.Join(s.binDir, fmt.Sprintf("jailer-v%s", normalizedVersion))
	tgzPath := filepath.Join(s.binDir, fmt.Sprintf("firecracker-v%s-%s.tgz", normalizedVersion, arch))

	tgzURL := fmt.Sprintf("%s/v%s/firecracker-v%s-%s.tgz",
		infra.FirecrackerGithubDownloadURL, normalizedVersion, normalizedVersion, arch)
	sha256URL := tgzURL + ".sha256.txt"

	// ── Step 1: Fetch SHA256 checksum ──
	var expectedSHA256 string
	sha256Content, err := s.dl.GetContent(ctx, sha256URL, 30, nil, true, 300)
	if err == nil {
		parts := strings.Fields(strings.TrimSpace(sha256Content))
		if len(parts) > 0 {
			expectedSHA256 = strings.ToLower(parts[0])
			slog.Info("Fetched checksum for Firecracker",
				"version", normalizedVersion,
				"sha256", expectedSHA256)
		}
	} else {
		slog.Debug("Could not fetch SHA-256 sidecar",
			"version", normalizedVersion,
			"error", err)
	}

	if expectedSHA256 == "" {
		return nil, binaryError(errs.CodeDownloadFailed,
			fmt.Sprintf("Checksum required for Firecracker v%s download", normalizedVersion),
		)
	}

	// ── Step 2: Download .tgz with checksum verification ──
	slog.Info("Downloading Firecracker", "version", normalizedVersion)
	if err := s.dl.DownloadFile(ctx, tgzURL, tgzPath, expectedSHA256, false, false, onProgress); err != nil {
		os.Remove(tgzPath)
		return nil, binaryError(errs.CodeDownloadFailed,
			fmt.Sprintf("Failed to download Firecracker v%s: %v", normalizedVersion, err),
		)
	}

	// ── Step 3: Extract firecracker and jailer from .tgz ──
	extractErr := extractFirecrackerArchive(tgzPath, normalizedVersion, arch, fcDest, jlDest)
	os.Remove(tgzPath)
	if extractErr != nil {
		os.Remove(fcDest)
		os.Remove(jlDest)
		return nil, extractErr
	}

	// ── Step 4: Create BinaryItems ──
	fcBinary, err := s.createBinaryItem("firecracker", normalizedVersion, fcDest, true)
	if err != nil {
		return nil, err
	}
	jlBinary, err := s.createBinaryItem("jailer", normalizedVersion, jlDest, true)
	if err != nil {
		return nil, err
	}

	return []*model.BinaryItem{fcBinary, jlBinary}, nil
}

// ── Remove ─────────────────────────────────────────────────────────────────

// Remove removes a binary from disk and database.
func (s *Service) Remove(ctx context.Context, binary *model.BinaryItem, force bool) (*model.BinaryItem, error) {
	vms := binary.VMs
	hasVMs := len(vms) > 0

	if hasVMs && !force {
		vmNames := make([]string, 0, len(vms))
		for _, vm := range vms {
			vmNames = append(vmNames, vm.Name)
		}
		return nil, binaryError(errs.CodeValidationFailed,
			fmt.Sprintf("Binary referenced by VMs: %s", strings.Join(vmNames, ", ")),
		)
	}

	if _, err := os.Stat(binary.Path); err == nil {
		if err := os.Remove(binary.Path); err != nil {
			return nil, binaryError(errs.CodeInternal, fmt.Sprintf("Failed to remove binary file: %v", err))
		}
	}

	// Hard delete if no VMs, soft delete if VMs exist (with force)
	if hasVMs {
		if err := s.repo.SoftDelete(ctx, binary.ID); err != nil {
			return nil, err
		}
	} else {
		if err := s.repo.Delete(ctx, binary.ID); err != nil {
			return nil, err
		}
	}

	return binary, nil
}

// RemoveMany removes multiple binaries.
func (s *Service) RemoveMany(ctx context.Context, binaries []*model.BinaryItem, force bool) ([]*model.BinaryItem, error) {
	var deleted []*model.BinaryItem
	for _, b := range binaries {
		result, err := s.Remove(ctx, b, force)
		if err != nil {
			return nil, err
		}
		deleted = append(deleted, result)
	}
	return deleted, nil
}

// ── Build from source ─────────────────────────────────────────────────────

// BuildFromSource builds Firecracker from source using Docker-based devtool.
func (s *Service) BuildFromSource(ctx context.Context, gitRef string) ([]*model.BinaryItem, error) {
	mirrorTag := safeTagChars.ReplaceAllString(gitRef, "_")
	mirrorDir, _ := infra.EnvGet("ASSET_MIRROR")

	// ── Check local asset mirror ──
	if mirrorDir != "" {
		cachedFC := filepath.Join(mirrorDir, fmt.Sprintf("firecracker-%s", mirrorTag))
		cachedJL := filepath.Join(mirrorDir, fmt.Sprintf("jailer-%s", mirrorTag))

		if system.FileExists(cachedFC) && system.FileExists(cachedJL) {
			buildVersion := fmt.Sprintf("dev-%s", mirrorTag)
			fcDest := filepath.Join(s.binDir, fmt.Sprintf("firecracker-%s", buildVersion))
			jlDest := filepath.Join(s.binDir, fmt.Sprintf("jailer-%s", buildVersion))

			fcBinary, err := s.copyBinary("firecracker", buildVersion, cachedFC, fcDest, "cached")
			if err != nil {
				return nil, err
			}
			jlBinary, err := s.copyBinary("jailer", buildVersion, cachedJL, jlDest, "cached")
			if err != nil {
				return nil, err
			}

			slog.Info("Using mirror cache for git ref", "ref", gitRef)
			return []*model.BinaryItem{fcBinary, jlBinary}, nil
		}
	}

	// ── Check git availability ──
	gitCheck := system.RunCmdCompat(ctx, []string{"which", "git"}, system.RunCmdOptions{Capture: true, Check: false})
	if gitCheck.ExitCode != 0 {
		return nil, binaryError(errs.CodeProcessError,
			"Git is required to build from source. "+
				"Install git (e.g., 'apt install git' or 'brew install git') "+
				"and try again.",
		)
	}

	srcDir := filepath.Join(s.cacheDir, "firecracker-src")

	// ── Clone or update repository ──
	if _, err := os.Stat(srcDir); os.IsNotExist(err) {
		slog.Info("Cloning Firecracker repository (this may take a while)...")
		cloneCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
		defer cancel()
		result := system.RunCmdCompat(cloneCtx, []string{"git", "clone", infra.FirecrackerGitRepoURL, srcDir},
			system.RunCmdOptions{Capture: false, Check: true})
		if result.Err != nil {
			return nil, binaryError(errs.CodeProcessError, fmt.Sprintf("Failed to clone Firecracker repository: %v", result.Err))
		}
	} else {
		slog.Info("Updating existing Firecracker repository...")
		fetchCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		result := system.RunCmdCompat(fetchCtx, []string{"git", "fetch", "origin"},
			system.RunCmdOptions{Cwd: srcDir, Capture: false, Check: true})
		if result.Err != nil {
			return nil, binaryError(errs.CodeProcessError, fmt.Sprintf("Failed to update Firecracker repository: %v", result.Err))
		}
	}

	// ── Checkout requested ref ──
	checkoutCtx, checkoutCancel := context.WithTimeout(ctx, 30*time.Second)
	defer checkoutCancel()
	result := system.RunCmdCompat(checkoutCtx, []string{"git", "checkout", gitRef},
		system.RunCmdOptions{Cwd: srcDir, Capture: false, Check: true})
	if result.Err != nil {
		return nil, binaryError(errs.CodeProcessError, fmt.Sprintf("Failed to checkout git ref '%s': %v", gitRef, result.Err))
	}

	// ── Resolve short commit hash ──
	revParseCtx, revParseCancel := context.WithTimeout(ctx, 10*time.Second)
	defer revParseCancel()
	hashResult := system.RunCmdCompat(revParseCtx, []string{"git", "rev-parse", "--short", "HEAD"},
		system.RunCmdOptions{Cwd: srcDir, Capture: true, Check: true})
	if hashResult.Err != nil {
		return nil, binaryError(errs.CodeProcessError, fmt.Sprintf("Failed to get commit hash: %v", hashResult.Err))
	}
	shortHash := strings.TrimSpace(hashResult.Stdout)
	buildVersion := fmt.Sprintf("dev-%s", shortHash)

	slog.Info("Building Firecracker from ref",
		"ref", gitRef,
		"commit", shortHash,
		"version", buildVersion,
	)
	slog.Info("This may take several minutes...")

	// ── Run the build with live output ──
	buildCtx, buildCancel := context.WithTimeout(ctx, 1800*time.Second)
	defer buildCancel()
	buildResult := system.RunCmdCompat(buildCtx, []string{"tools/devtool", "build", "--release"},
		system.RunCmdOptions{Cwd: srcDir, Capture: false, Check: false})
	if buildResult.Err != nil {
		return nil, binaryError(errs.CodeProcessError,
			fmt.Sprintf("Build process failed: %v", buildResult.Err),
		)
	}

	exitCode := buildResult.ExitCode
	buildStderr := buildResult.Stderr

	if exitCode != 0 {
		stderr := strings.TrimSpace(buildStderr)
		if len(stderr) > 500 {
			stderr = stderr[:500]
		}
		msg := fmt.Sprintf(
			"Firecracker build failed (exit %d) for ref '%s'. "+
				"Check the output above or run 'tools/devtool build --release' "+
				"manually in %s.",
			exitCode, gitRef, srcDir,
		)
		if stderr != "" {
			msg += fmt.Sprintf(" Stderr: %s", stderr)
		}
		return nil, binaryError(errs.CodeProcessError, msg)
	}

	// ── Locate built binaries ──
	buildOutput := filepath.Join(srcDir, "build", "cargo_target", "x86_64-unknown-linux-musl", "release")
	fcSrc := filepath.Join(buildOutput, "firecracker")
	jlSrc := filepath.Join(buildOutput, "jailer")

	var missing []string
	if !system.FileExists(fcSrc) {
		missing = append(missing, "firecracker")
	}
	if !system.FileExists(jlSrc) {
		missing = append(missing, "jailer")
	}
	if len(missing) > 0 {
		return nil, binaryError(errs.CodeInternal,
			fmt.Sprintf("Build completed but expected binaries not found: %s. Expected location: %s",
				strings.Join(missing, ", "), buildOutput),
		)
	}

	fcDest := filepath.Join(s.binDir, fmt.Sprintf("firecracker-%s", buildVersion))
	jlDest := filepath.Join(s.binDir, fmt.Sprintf("jailer-%s", buildVersion))

	fcBinary, err := s.copyBinary("firecracker", buildVersion, fcSrc, fcDest, "built")
	if err != nil {
		return nil, err
	}
	jlBinary, err := s.copyBinary("jailer", buildVersion, jlSrc, jlDest, "built")
	if err != nil {
		return nil, err
	}

	slog.Info("Built Firecracker", "version", buildVersion, "ref", gitRef)

	// ── Cache in mirror ──
	if mirrorDir != "" {
		os.MkdirAll(mirrorDir, 0755)
		cachedFC := filepath.Join(mirrorDir, fmt.Sprintf("firecracker-%s", mirrorTag))
		cachedJL := filepath.Join(mirrorDir, fmt.Sprintf("jailer-%s", mirrorTag))
		fcErr := infra.CopyPreservingMetadata(fcDest, cachedFC)
		jlErr := infra.CopyPreservingMetadata(jlDest, cachedJL)
		if fcErr == nil && jlErr == nil {
			slog.Info("Cached build in mirror for git ref", "ref", gitRef)
		} else {
			if fcErr != nil {
				slog.Warn("Failed to cache firecracker in mirror", "error", fcErr)
			}
			if jlErr != nil {
				slog.Warn("Failed to cache jailer in mirror", "error", jlErr)
			}
		}
	}

	return []*model.BinaryItem{fcBinary, jlBinary}, nil
}

// ── Service binaries ───────────────────────────────────────────────────────

// Repo returns the underlying repository for use by the API layer.
func (s *Service) Repo() Repository {
	return s.repo
}

// ── Internal helpers ───────────────────────────────────────────────────────

func (s *Service) createBinaryItem(name, versionStr, path string, resolveCIVersion bool) (*model.BinaryItem, error) {
	id, err := infra.HashGenerator{}.Binary(path, name, versionStr)
	if err != nil {
		return nil, binaryError(errs.CodeInternal, fmt.Sprintf("Failed to generate binary ID: %v", err))
	}

	var ciVer *string
	if resolveCIVersion {
		v := CIVersion(versionStr)
		ciVer = &v
	}

	now := time.Now().Format(time.RFC3339)

	return &model.BinaryItem{
		ID:          id,
		Name:        name,
		Version:     versionStr,
		FullVersion: "v" + versionStr,
		CIVersion:   ciVer,
		Path:        path,
		IsDefault:   false,
		IsPresent:   true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

// copyBinary handles: copy + chmod + createItem for one binary,
// with an error message label (e.g. "cached" or "built").
func (s *Service) copyBinary(name, version, src, dest, label string) (*model.BinaryItem, error) {
	if err := infra.CopyPreservingMetadata(src, dest); err != nil {
		return nil, binaryError(errs.CodeInternal,
			fmt.Sprintf("Failed to copy %s %s: %v", label, name, err))
	}
	system.MakeExecutable(dest)
	return s.createBinaryItem(name, version, dest, false)
}
