package binary

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
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

const (
	defaultArch = "x86_64"
	chunkSize   = 512 * 1024 // CONST_MIN_BINARY_SIZE_BYTES * CONST_BUFFER_SIZE_BYTES = 512 * 1024
)

// mvmctlVersion is overridden at build time via -ldflags.
var mvmctlVersion = "0.0.0"

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
	for _, b := range binaries {
		if _, err := os.Stat(b.Path); os.IsNotExist(err) {
			missingIDs = append(missingIDs, b.ID)
		}
	}

	if len(missingIDs) > 0 {
		if err := s.repo.UpdateManyIsPresent(ctx, missingIDs, false); err != nil {
			return nil, err
		}
		binaries, err = s.repo.ListAll(ctx)
		if err != nil {
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

	parsed, err := s.dl.GetJSON(ctx, url, 30, nil, true, 300)
	if err != nil {
		return nil, mapGitHubAPIError(err)
	}

	releases, ok := parsed.([]interface{})
	if !ok {
		typeName := fmt.Sprintf("%T", parsed)
		return nil, binaryError(errs.CodeDownloadFailed,
			fmt.Sprintf("Unexpected response from GitHub: expected list, got %s", typeName),
		)
	}

	versions := make([]string, 0, len(releases))
	for _, raw := range releases {
		release, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		tag, _ := release["tag_name"].(string)
		if tag != "" {
			versions = append(versions, NormalizeVersion(tag))
		}
	}

	sort.Slice(versions, func(i, j int) bool {
		return version.SemverGreater(versions[i], versions[j])
	})

	return versions, nil
}

// mapGitHubAPIError converts an error from the GitHub API into the Python-matching
// BinaryError with the same message wording.
func mapGitHubAPIError(err error) error {
	errMsg := err.Error()
	if strings.Contains(errMsg, "403") && strings.Contains(strings.ToLower(errMsg), "rate limit") {
		return binaryError(errs.CodeDownloadFailed,
			"Failed to fetch Firecracker releases from GitHub: "+
				"rate limit exceeded (HTTP 403). "+
				"Either wait for the rate limit to reset, or set a "+
				"GitHub token via the GITHUB_TOKEN environment variable "+
				"to increase your rate limit.",
		)
	}
	if strings.Contains(errMsg, "401") {
		return binaryError(errs.CodeDownloadFailed,
			"Failed to fetch Firecracker releases from GitHub: "+
				"authentication failed (HTTP 401). "+
				"Set a valid GitHub token via GITHUB_TOKEN.",
		)
	}
	return binaryError(errs.CodeDownloadFailed,
		fmt.Sprintf("Failed to fetch Firecracker releases from GitHub: %s", err),
	)
}

// ── Download ───────────────────────────────────────────────────────────────

// DownloadFirecracker downloads firecracker + jailer for version, returns Binary list.
func (s *Service) DownloadFirecracker(ctx context.Context, version string, binDir string) ([]*model.BinaryItem, error) {
	normalizedVersion := NormalizeVersion(version)

	if err := os.MkdirAll(binDir, 0755); err != nil {
		return nil, binaryError(errs.CodeInternal, fmt.Sprintf("Failed to create bin directory: %v", err))
	}

	fcDest := filepath.Join(binDir, fmt.Sprintf("firecracker-v%s", normalizedVersion))
	jlDest := filepath.Join(binDir, fmt.Sprintf("jailer-v%s", normalizedVersion))
	tgzPath := filepath.Join(binDir, fmt.Sprintf("firecracker-v%s-%s.tgz", normalizedVersion, defaultArch))

	tgzURL := fmt.Sprintf("%s/v%s/firecracker-v%s-%s.tgz",
		infra.FirecrackerGithubDownloadURL, normalizedVersion, normalizedVersion, defaultArch)
	sha256URL := tgzURL + ".sha256.txt"

	// ── Step 1: Fetch SHA256 checksum ──
	var expectedSHA256 string
	sha256Content, err := s.dl.GetRaw(ctx, sha256URL, 30, nil, true, 300)
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
	progress := func(currentBytes, totalBytes int64) {
		if currentBytes > 0 {
			slog.Debug("Download progress", "version", normalizedVersion, "bytesRead", currentBytes)
		}
	}
	if err := s.dl.DownloadFile(ctx, tgzURL, tgzPath, expectedSHA256, false, false, progress); err != nil {
		os.Remove(tgzPath)
		return nil, binaryError(errs.CodeDownloadFailed,
			fmt.Sprintf("Failed to download Firecracker v%s: %v", normalizedVersion, err),
		)
	}

	// ── Step 3: Extract firecracker and jailer from .tgz ──
	fcFound := false
	jlFound := false

	extractErr := func() error {
		f, err := os.Open(tgzPath)
		if err != nil {
			return fmt.Errorf("Failed to extract archive: %w", err)
		}
		defer f.Close()

		gzr, err := gzip.NewReader(f)
		if err != nil {
			return fmt.Errorf("Failed to extract archive: %w", err)
		}
		defer gzr.Close()

		tr := tar.NewReader(gzr)
		for {
			header, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return fmt.Errorf("Failed to extract archive: %w", err)
			}

			basename := filepath.Base(header.Name)
			var dest string

			switch basename {
			case fmt.Sprintf("firecracker-v%s-%s", normalizedVersion, defaultArch):
				dest = fcDest
				fcFound = true
			case fmt.Sprintf("jailer-v%s-%s", normalizedVersion, defaultArch):
				dest = jlDest
				jlFound = true
			default:
				continue
			}

			if err := extractTarMember(tr, dest, header.Name); err != nil {
				return err
			}
		}
		return nil
	}()

	os.Remove(tgzPath)

	if extractErr != nil {
		os.Remove(fcDest)
		os.Remove(jlDest)
		return nil, binaryError(errs.CodeInternal, extractErr.Error())
	}

	if !fcFound || !jlFound {
		os.Remove(fcDest)
		os.Remove(jlDest)
		return nil, binaryError(errs.CodeValidationFailed,
			fmt.Sprintf("Archive for v%s missing expected binaries", normalizedVersion),
		)
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

	binPath := binary.Path
	if _, err := os.Stat(binPath); err == nil {
		if err := os.Remove(binPath); err != nil {
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
func (s *Service) BuildFromSource(ctx context.Context, gitRef string, binDir string) ([]*model.BinaryItem, error) {
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return nil, binaryError(errs.CodeInternal, fmt.Sprintf("Failed to create bin directory: %v", err))
	}

	mirrorTag := sanitizeMirrorTag(gitRef)
	mirrorDir, _ := infra.EnvGet("ASSET_MIRROR")

	// ── Check local asset mirror ──
	if mirrorDir != "" {
		cachedFC := filepath.Join(mirrorDir, fmt.Sprintf("firecracker-%s", mirrorTag))
		cachedJL := filepath.Join(mirrorDir, fmt.Sprintf("jailer-%s", mirrorTag))

		if fileExists(cachedFC) && fileExists(cachedJL) {
			buildVersion := fmt.Sprintf("dev-%s", mirrorTag)
			fcDest := filepath.Join(binDir, fmt.Sprintf("firecracker-%s", buildVersion))
			jlDest := filepath.Join(binDir, fmt.Sprintf("jailer-%s", buildVersion))

			if err := infra.CopyFile(cachedFC, fcDest); err != nil {
				return nil, binaryError(errs.CodeInternal, fmt.Sprintf("Failed to copy cached firecracker: %v", err))
			}
			makeExecutable(fcDest)

			if err := infra.CopyFile(cachedJL, jlDest); err != nil {
				return nil, binaryError(errs.CodeInternal, fmt.Sprintf("Failed to copy cached jailer: %v", err))
			}
			makeExecutable(jlDest)

			slog.Info("Using mirror cache for git ref", "ref", gitRef)

			fcBinary, err := s.createBinaryItem("firecracker", buildVersion, fcDest, false)
			if err != nil {
				return nil, err
			}
			jlBinary, err := s.createBinaryItem("jailer", buildVersion, jlDest, false)
			if err != nil {
				return nil, err
			}
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
		if err := runCmd(cloneCtx, "git", "clone", infra.FirecrackerGitRepoURL, srcDir); err != nil {
			return nil, binaryError(errs.CodeProcessError, fmt.Sprintf("Failed to clone Firecracker repository: %v", err))
		}
	} else {
		slog.Info("Updating existing Firecracker repository...")
		fetchCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		if err := runCmdInDir(fetchCtx, srcDir, []string{"git", "fetch", "origin"}); err != nil {
			return nil, binaryError(errs.CodeProcessError, fmt.Sprintf("Failed to update Firecracker repository: %v", err))
		}
	}

	// ── Checkout requested ref ──
	checkoutCtx, checkoutCancel := context.WithTimeout(ctx, 30*time.Second)
	defer checkoutCancel()
	if err := runCmdInDir(checkoutCtx, srcDir, []string{"git", "checkout", gitRef}); err != nil {
		return nil, binaryError(errs.CodeProcessError, fmt.Sprintf("Failed to checkout git ref '%s': %v", gitRef, err))
	}

	// ── Resolve short commit hash ──
	revParseCtx, revParseCancel := context.WithTimeout(ctx, 10*time.Second)
	defer revParseCancel()
	hashOut, err := runCmdCapture(revParseCtx, srcDir, "git", "rev-parse", "--short", "HEAD")
	if err != nil {
		return nil, binaryError(errs.CodeProcessError, fmt.Sprintf("Failed to get commit hash: %v", err))
	}
	shortHash := strings.TrimSpace(hashOut)
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
	exitCode, buildStderr, buildErr := runBuildLive(buildCtx, srcDir, "tools/devtool", "build", "--release")
	if buildErr != nil {
		return nil, binaryError(errs.CodeProcessError,
			fmt.Sprintf("Build process failed: %v", buildErr),
		)
	}

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
	if !fileExists(fcSrc) {
		missing = append(missing, "firecracker")
	}
	if !fileExists(jlSrc) {
		missing = append(missing, "jailer")
	}
	if len(missing) > 0 {
		return nil, binaryError(errs.CodeInternal,
			fmt.Sprintf("Build completed but expected binaries not found: %s. Expected location: %s",
				strings.Join(missing, ", "), buildOutput),
		)
	}

	fcDest := filepath.Join(binDir, fmt.Sprintf("firecracker-%s", buildVersion))
	jlDest := filepath.Join(binDir, fmt.Sprintf("jailer-%s", buildVersion))

	if err := infra.CopyFile(fcSrc, fcDest); err != nil {
		return nil, binaryError(errs.CodeInternal, fmt.Sprintf("Failed to copy built firecracker: %v", err))
	}
	makeExecutable(fcDest)

	if err := infra.CopyFile(jlSrc, jlDest); err != nil {
		return nil, binaryError(errs.CodeInternal, fmt.Sprintf("Failed to copy built jailer: %v", err))
	}
	makeExecutable(jlDest)

	slog.Info("Built Firecracker", "version", buildVersion, "ref", gitRef)

	// ── Cache in mirror ──
	if mirrorDir != "" {
		os.MkdirAll(mirrorDir, 0755)
		cachedFC := filepath.Join(mirrorDir, fmt.Sprintf("firecracker-%s", mirrorTag))
		cachedJL := filepath.Join(mirrorDir, fmt.Sprintf("jailer-%s", mirrorTag))
		fcErr := infra.CopyFile(fcDest, cachedFC)
		jlErr := infra.CopyFile(jlDest, cachedJL)
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

	fcBinary, err := s.createBinaryItem("firecracker", buildVersion, fcDest, false)
	if err != nil {
		return nil, err
	}
	jlBinary, err := s.createBinaryItem("jailer", buildVersion, jlDest, false)
	if err != nil {
		return nil, err
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
	id, err := GenerateBinaryID(path, name, versionStr)
	if err != nil {
		return nil, binaryError(errs.CodeInternal, fmt.Sprintf("Failed to generate binary ID: %v", err))
	}

	var ciVer *string
	if resolveCIVersion {
		v := CIVersion(versionStr)
		ciVer = &v
	}

	now := time.Now().UTC().Format(time.RFC3339)

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

// ── Hash generation ────────────────────────────────────────────────────────

// GenerateBinaryID generates a content-addressed SHA256 hash for a binary.
func GenerateBinaryID(path, name, version string) (string, error) {
	fileHash, err := infra.SHA256FileHash(path)
	if err != nil {
		return "", fmt.Errorf("read binary file for hashing: %w", err)
	}
	combined := fileHash + ":" + name + ":" + version
	return infra.SHA256Hash([]byte(combined)), nil
}

// NormalizeVersion strips 'v' prefix from version.
func NormalizeVersion(version string) string {
	return strings.TrimPrefix(version, "v")
}

// CIVersion generates a CI version from a full version (e.g. "1.15.0" -> "v1.15").
// TODO(verdict#33): belongs in infra/version or similar shared utility
func CIVersion(version string) string {
	parts := strings.Split(version, ".")
	if len(parts) >= 2 {
		return "v" + parts[0] + "." + parts[1]
	}
	return "v" + version
}

// ── Standalone utility functions ───────────────────────────────────────────

// TODO(verdict#33): belongs in infra/strings or similar shared utility
func sanitizeMirrorTag(gitRef string) string {
	re := regexp.MustCompile(`[^a-zA-Z0-9._-]`)
	return re.ReplaceAllString(gitRef, "_")
}

// TODO(verdict#33): belongs in infra/fs or similar shared utility
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// makeExecutable adds executable bits (S_IXUSR | S_IXGRP | S_IXOTH) to
// the file's current permissions without changing the existing mode.
// TODO(verdict#33): belongs in infra/fs or similar shared utility
func makeExecutable(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat for chmod: %w", err)
	}
	newMode := fi.Mode().Perm() | 0111 // S_IXUSR | S_IXGRP | S_IXOTH
	return os.Chmod(path, newMode)
}

// findEmbeddedServiceBinary returns the path to an embedded service binary, or "" if not available.
// TODO(verdict#33): belongs in infra/fs or similar shared utility

func extractTarMember(reader *tar.Reader, dest string, memberName ...string) error {
	outFile, err := os.Create(dest)
	if err != nil {
		name := filepath.Base(dest)
		if len(memberName) > 0 {
			name = memberName[0]
		}
		return binaryError(errs.CodeInternal, fmt.Sprintf("Cannot read %s from archive", name))
	}
	defer outFile.Close()

	buf := make([]byte, chunkSize)
	for {
		n, readErr := reader.Read(buf)
		if n > 0 {
			if _, writeErr := outFile.Write(buf[:n]); writeErr != nil {
				return binaryError(errs.CodeInternal, fmt.Sprintf("Failed to write binary: %v", writeErr))
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			name := filepath.Base(dest)
			if len(memberName) > 0 {
				name = memberName[0]
			}
			return binaryError(errs.CodeInternal, fmt.Sprintf("Cannot read %s from archive: %v", name, readErr))
		}
	}

	if err := makeExecutable(dest); err != nil {
		return binaryError(errs.CodeInternal, fmt.Sprintf("Failed to set executable permissions: %v", err))
	}

	return nil
}

func binaryError(code errs.Code, msg string) *errs.DomainError {
	return &errs.DomainError{
		Code:    code,
		Op:      "binary",
		Message: msg,
	}
}

// ── Subprocess helpers ─────────────────────────────────────────────────────

func runCmd(ctx context.Context, cmd string, args ...string) error {
	fullArgs := append([]string{cmd}, args...)
	result := system.RunCmdCompat(ctx, fullArgs, system.RunCmdOptions{
		Capture: false,
		Check:   true,
	})
	return result.Err
}

func runCmdInDir(ctx context.Context, dir string, args []string) error {
	result := system.RunCmdCompat(ctx, args, system.RunCmdOptions{
		Cwd:     dir,
		Capture: false,
		Check:   true,
	})
	return result.Err
}

func runCmdCapture(ctx context.Context, dir, cmd string, args ...string) (string, error) {
	fullArgs := append([]string{cmd}, args...)
	result := system.RunCmdCompat(ctx, fullArgs, system.RunCmdOptions{
		Cwd:     dir,
		Capture: true,
		Check:   true,
	})
	if result.Err != nil {
		return "", result.Err
	}
	return result.Stdout, nil
}

// runBuildLive runs a build command with live stdout/stderr output.
func runBuildLive(ctx context.Context, dir, cmd string, args ...string) (int, string, error) {
	fullArgs := append([]string{cmd}, args...)
	result := system.RunCmdCompat(ctx, fullArgs, system.RunCmdOptions{
		Cwd:     dir,
		Capture: false,
		Check:   false,
	})

	if result.Err != nil {
		return -1, "", result.Err
	}
	return result.ExitCode, "", nil
}

// extractBinaryVMName extracts the "name" from a VM object.
// VMs are now typed as *model.VM from the shared model package.
func extractBinaryVMName(vm *model.VM) string {
	return vm.Name
}
