package inputs

import (
	"regexp"
	"strings"

	"mvmctl/pkg/errs"
)

var exactBinaryReleaseVersion = regexp.MustCompile(
	`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?)?$`,
)

// BinaryRemoveInput identifies one or more exact Firecracker release slots.
type BinaryRemoveInput struct {
	Versions []string `json:"versions" yaml:"versions"`
}

// Resolve validates, normalizes, and de-duplicates exact release versions while preserving order.
func (input BinaryRemoveInput) Resolve() ([]string, error) {
	if len(input.Versions) == 0 {
		return nil, errs.New(errs.CodeValidationFailed, "at least one exact binary version is required")
	}
	versions := make([]string, 0, len(input.Versions))
	seen := make(map[string]struct{}, len(input.Versions))
	for _, candidate := range input.Versions {
		version := strings.TrimPrefix(candidate, "v")
		if !exactBinaryReleaseVersion.MatchString(version) || strings.Contains(version, "..") {
			return nil, errs.New(
				errs.CodeValidationFailed,
				"binary removal requires exact versions such as 1.16.1",
			)
		}
		if _, duplicate := seen[version]; duplicate {
			continue
		}
		seen[version] = struct{}{}
		versions = append(versions, version)
	}
	return versions, nil
}
