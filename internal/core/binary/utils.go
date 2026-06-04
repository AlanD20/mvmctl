package binary

import (
	"errors"
	"fmt"
	"runtime"
	"strings"

	"mvmctl/internal/infra/download"
	"mvmctl/internal/infra/errs"
)

// rustTargetTriple returns the Rust target triple for the current architecture.
// Used for locating cargo build output directories.
func rustTargetTriple() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64-unknown-linux-musl"
	case "arm64":
		return "aarch64-unknown-linux-musl"
	default:
		return runtime.GOARCH + "-unknown-linux-musl"
	}
}

// ── Version helpers ────────────────────────────────────────────────────────

// githubRelease models a single release entry from the GitHub API.
type githubRelease struct {
	TagName string `json:"tag_name"`
}

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
