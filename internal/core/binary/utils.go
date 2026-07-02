package binary

import (
	"runtime"
	"strings"

	"mvmctl/pkg/errs"
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

// --- Version helpers ---

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

// --- Error helpers ---

func binaryError(code errs.Code, msg string) *errs.DomainError {
	return errs.New(code, msg)
}
