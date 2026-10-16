package binary

import (
	"strings"

	"mvmctl/pkg/errs"
)

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
