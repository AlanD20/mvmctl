package binary

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"mvmctl/internal/infra/download"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/system"
)

// ── Constants ──────────────────────────────────────────────────────────────

const chunkSize = 512 * 1024 // CONST_MIN_BINARY_SIZE_BYTES * CONST_BUFFER_SIZE_BYTES = 512 * 1024

// githubRelease models a single release entry from the GitHub API.
type githubRelease struct {
	TagName string `json:"tag_name"`
}

// ── Version helpers ────────────────────────────────────────────────────────

// NormalizeVersion strips 'v' prefix from version.
func NormalizeVersion(version string) string {
	return strings.TrimPrefix(version, "v")
}

// CIVersion generates a CI version from a full version (e.g. "1.15.0" -> "v1.15").
func CIVersion(version string) string {
	parts := strings.Split(version, ".")
	if len(parts) >= 2 {
		return "v" + parts[0] + "." + parts[1]
	}
	return "v" + version
}

// ── Error helpers ──────────────────────────────────────────────────────────

func binaryError(code errs.Code, msg string) *errs.DomainError {
	return &errs.DomainError{
		Code:    code,
		Op:      "binary",
		Message: msg,
	}
}

// mapGitHubAPIError converts an error from the GitHub API into the Python-matching
// BinaryError with the same message wording.
func mapGitHubAPIError(err error) error {
	var httpErr download.HttpError
	if errors.As(err, &httpErr) {
		switch httpErr.StatusCode {
		case 403:
			return binaryError(errs.CodeDownloadFailed,
				"Failed to fetch Firecracker releases from GitHub: "+
					"rate limit exceeded (HTTP 403). "+
					"Either wait for the rate limit to reset, or set a "+
					"GitHub token via the GITHUB_TOKEN environment variable "+
					"to increase your rate limit.",
			)
		case 401:
			return binaryError(errs.CodeDownloadFailed,
				"Failed to fetch Firecracker releases from GitHub: "+
					"authentication failed (HTTP 401). "+
					"Set a valid GitHub token via GITHUB_TOKEN.",
			)
		}
	}
	return binaryError(errs.CodeDownloadFailed,
		fmt.Sprintf("Failed to fetch Firecracker releases from GitHub: %s", err),
	)
}

// ── Archive helpers ────────────────────────────────────────────────────────

func extractMemberFromTar(reader *tar.Reader, dest string, memberName ...string) error {
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

	if err := system.MakeExecutable(dest); err != nil {
		return binaryError(errs.CodeInternal, fmt.Sprintf("Failed to set executable permissions: %v", err))
	}

	return nil
}

// extractFirecrackerArchive opens a .tgz and extracts the firecracker and jailer
// binaries matching the given version and architecture.
// Mirrors the extraction block in Python's BinaryService.download_firecracker().
func extractFirecrackerArchive(tgzPath, normalizedVersion, arch, fcDest, jlDest string) error {
	f, err := os.Open(tgzPath)
	if err != nil {
		return binaryError(errs.CodeInternal,
			fmt.Sprintf("Failed to extract archive: %v", err))
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return binaryError(errs.CodeInternal,
			fmt.Sprintf("Failed to extract archive: %v", err))
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	fcFound := false
	jlFound := false

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return binaryError(errs.CodeInternal,
				fmt.Sprintf("Failed to extract archive: %v", err))
		}

		basename := filepath.Base(header.Name)
		var dest string

		switch basename {
		case fmt.Sprintf("firecracker-v%s-%s", normalizedVersion, arch):
			dest = fcDest
			fcFound = true
		case fmt.Sprintf("jailer-v%s-%s", normalizedVersion, arch):
			dest = jlDest
			jlFound = true
		default:
			continue
		}

		if err := extractMemberFromTar(tr, dest, header.Name); err != nil {
			return err
		}
	}

	if !fcFound || !jlFound {
		return binaryError(errs.CodeValidationFailed,
			fmt.Sprintf("Archive for v%s missing expected binaries", normalizedVersion),
		)
	}

	return nil
}
