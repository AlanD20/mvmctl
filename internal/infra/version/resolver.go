package version

import (
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"mvmctl/internal/infra/errs"
)

// versionCleanPattern matches Python's re.sub(r"^(\d+(?:\.\d+)*).*$", r"\1", version)
// Captures leading numeric version like "1.15.1" from "1.15.1-dev" or "1.15.1".
var versionCleanPattern = regexp.MustCompile(`^(\d+(?:\.\d+)*).*$`)

// ── VersionSpec ─────────────────────────────────────────────────────────────

// VersionSpec specifies a version to resolve.
//
// Created by ParseSpec and consumed by Resolve.
// Matches Python's VersionSpec dataclass exactly.
type VersionSpec struct {
	Major    *int
	Minor    *int
	Patch    *int
	IsLatest bool
}

// IsPartial returns true if the spec is a partial version (any field unspecified)
// and false if it is an exact version (all major/minor/patch set).
// Matches Python's VersionSpec.is_partial(): returns True when any component is None,
// including an empty spec VersionSpec() where all components are None.
func (s VersionSpec) IsPartial() bool {
	return s.Major == nil || s.Minor == nil || s.Patch == nil
}

// ParseSpec parses a version specification string into a structured VersionSpec.
//
// Rules:
//   - "" or "latest" → VersionSpec{IsLatest: true}
//   - "1" → VersionSpec{Major: 1}
//   - "1.15" → VersionSpec{Major: 1, Minor: 15}
//   - "1.15.1" → VersionSpec{Major: 1, Minor: 15, Patch: 1}
//   - "v1.15.1" → strips v prefix first
//
// Returns an error if any version component cannot be parsed as an integer.
// Matches Python's VersionResolver.parse_spec() exactly (Python raises ValueError).
func ParseSpec(spec string) (VersionSpec, error) {
	spec = strings.TrimSpace(spec)

	if spec == "" || spec == "latest" {
		return VersionSpec{IsLatest: true}, nil
	}

	// Strip 'v' or 'V' prefix
	if strings.HasPrefix(spec, "v") || strings.HasPrefix(spec, "V") {
		spec = spec[1:]
	}

	parts := strings.Split(spec, ".")
	var vs VersionSpec

	if len(parts) >= 1 {
		n, err := strconv.Atoi(parts[0])
		if err != nil {
			return VersionSpec{}, fmt.Errorf("invalid version spec %q: %w", spec, err)
		}
		vs.Major = &n
	}
	if len(parts) >= 2 {
		n, err := strconv.Atoi(parts[1])
		if err != nil {
			return VersionSpec{}, fmt.Errorf("invalid version spec %q: %w", spec, err)
		}
		vs.Minor = &n
	}
	if len(parts) >= 3 {
		n, err := strconv.Atoi(parts[2])
		if err != nil {
			return VersionSpec{}, fmt.Errorf("invalid version spec %q: %w", spec, err)
		}
		vs.Patch = &n
	}

	return vs, nil
}

// ParseSelector splits a "type:version" selector into its two parts.
//
// Splits on ":" with maxsplit=1.
//   - "firecracker:1.15" → ("firecracker", "1.15")
//   - "1.15" → ("", "1.15")
//   - "firecracker" → ("firecracker", "")
//   - ":1.15" → ("", "1.15")
//   - "firecracker:" → ("firecracker", "")
//
// Matches Python's VersionResolver.parse_selector() exactly.
// Returns empty string for name when no prefix was found (matching Python's None).
func ParseSelector(selector string) (name, version string) {
	if !strings.Contains(selector, ":") {
		return "", selector
	}

	parts := strings.SplitN(selector, ":", 2)
	prefix, value := parts[0], parts[1]
	if prefix == "" {
		return "", value
	}
	return prefix, value
}

// SemverKey converts a version string to a sortable slice of integers.
//
// Strips "v" prefix, splits on ".", converts each part to int.
// On parse failure, returns []int{0} so failed versions sort to the end.
// Matches Python's VersionResolver.semver_key() exactly.
func SemverKey(v string) []int {
	clean := strings.TrimPrefix(v, "v")
	parts := strings.Split(clean, ".")
	var nums []int
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return []int{0}
		}
		nums = append(nums, n)
	}
	return nums
}

// Resolve resolves a VersionSpec against a list of available versions.
//
// 1. Sorts versions descending by semver (newest first).
// 2. If spec.IsLatest → returns highest version.
// 3. If exact version (all parts set) → verifies existence, returns it.
// 4. If partial → iterates sorted versions, finds first matching prefix.
//
// Matches Python's VersionResolver.resolve() exactly.
func Resolve(versions []string, spec VersionSpec) (string, error) {
	if len(versions) == 0 {
		return "", errs.VersionError(fmt.Sprintf("No versions available to resolve spec %#v", spec))
	}

	// Work on a copy — never mutate the input list
	sorted := make([]string, len(versions))
	copy(sorted, versions)
	sort.SliceStable(sorted, func(i, j int) bool {
		ki := SemverKey(sorted[i])
		kj := SemverKey(sorted[j])
		for idx := 0; idx < len(ki) && idx < len(kj); idx++ {
			if ki[idx] != kj[idx] {
				return ki[idx] > kj[idx] // descending (newest first)
			}
		}
		return len(ki) > len(kj)
	})

	if spec.IsLatest {
		return sorted[0], nil
	}

	// Build the spec parts list for matching
	var specParts []int
	if spec.Major != nil {
		specParts = append(specParts, *spec.Major)
	}
	if spec.Minor != nil {
		specParts = append(specParts, *spec.Minor)
	}
	if spec.Patch != nil {
		specParts = append(specParts, *spec.Patch)
	}

	// Exact version — all three parts set
	if spec.Major != nil && spec.Minor != nil && spec.Patch != nil {
		targetParts := make([]string, len(specParts))
		for i, p := range specParts {
			targetParts[i] = strconv.Itoa(p)
		}
		target := strings.Join(targetParts, ".")
		for _, v := range versions {
			vClean := strings.TrimPrefix(v, "v")
			if v == target || vClean == target {
				return target, nil
			}
		}
		return "", errs.VersionError(
			fmt.Sprintf("Version '%s' not found in available versions: %v", target, versions),
		)
	}

	// Partial match — iterate sorted versions, find first matching prefix
	n := len(specParts)
	for _, v := range sorted {
		vClean := strings.TrimPrefix(v, "v")
		vParts := strings.Split(vClean, ".")
		if len(vParts) >= n {
			match := true
			for i := range n {
				vp, err := strconv.Atoi(vParts[i])
				if err != nil || vp != specParts[i] {
					match = false
					break
				}
			}
			if match {
				return vClean, nil
			}
		}
	}

	majorStr := "<nil>"
	if spec.Major != nil {
		majorStr = strconv.Itoa(*spec.Major)
	}
	minorStr := "<nil>"
	if spec.Minor != nil {
		minorStr = strconv.Itoa(*spec.Minor)
	}
	patchStr := "<nil>"
	if spec.Patch != nil {
		patchStr = strconv.Itoa(*spec.Patch)
	}
	return "", errs.VersionError(
		fmt.Sprintf("No version matching spec (major=%s, minor=%s, patch=%s) found in available versions: %v",
			majorStr, minorStr, patchStr, versions),
	)
}

// ── VersionGate ─────────────────────────────────────────────────────────────

// VersionGate gates features behind a minimum binary version.
// Matches Python's VersionGate class exactly.
//
// Usage:
//
//	var gate VersionGate
//	if err := gate.Require("firecracker", "1.15.1", "1.16"); err != nil { ... }
type VersionGate struct{}

// Require checks that version meets the minimum requirement.
//
// Args:
//   - binaryName: Human-readable binary name (e.g. "Firecracker").
//   - version: The version string to check (e.g. "1.15.1", "dev-abc123").
//   - minimum: Minimum version requirement (e.g. "1.16", "2", "1.16.0").
//
// Returns an error if version is too old or cannot be parsed.
// Matches Python's VersionGate.require() exactly.
func (g *VersionGate) Require(binaryName, version, minimum string) error {
	if version == "" {
		return errs.VersionGateError(
			fmt.Sprintf("Cannot determine %s version. %s v%s+ required.", binaryName, binaryName, minimum),
		)
	}

	// Dev builds always pass the gate.
	if strings.HasPrefix(version, "dev-") {
		return nil
	}

	if !g.IsSatisfiedBy(version, minimum) {
		return errs.VersionGateError(
			fmt.Sprintf(
				"%s v%s+ required for this operation (current: v%s). Stop the VM first, perform the operation, then start it again.",
				binaryName,
				minimum,
				version,
			),
		)
	}

	return nil
}

// ParseVersion parses a semver-like version string into a slice of ints.
//
// Handles formats like "1", "1.15", "1.15.1".
// Non-numeric suffixes (e.g., "1.15.1-dev") are stripped.
// Matches Python's VersionGate._parse_version() exactly.
// Python: re.sub(r"^(\d+(?:\.\d+)*).*$", r"\1", version) then split(".") and map(int).
func (g *VersionGate) ParseVersion(version string) []int {
	match := versionCleanPattern.FindStringSubmatch(version)
	if match == nil {
		return nil
	}
	cleaned := match[1]

	parts := strings.Split(cleaned, ".")
	var nums []int
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		nums = append(nums, n)
	}
	return nums
}

// IsSatisfiedBy compares version against minimum.
//
// Examples:
//   - (1, 15, 1) >= (1, 16) -> false  (15 < 16)
//   - (1, 16, 0) >= (1, 16) -> true
//   - (2, 0) >= (1, 16)     -> true  (2 > 1)
//   - (1, 16) >= (1, 16, 0) -> true  (only compare first 2)
//
// Matches Python's VersionGate._is_satisfied_by() exactly.
func (g *VersionGate) IsSatisfiedBy(version, minimum string) bool {
	vParts := g.ParseVersion(version)
	mParts := g.ParseVersion(minimum)

	if vParts == nil || mParts == nil {
		slog.Warn("Could not parse version", "version", version, "minimum", minimum)
		return false
	}

	// Compare component-by-component, stopping at the shorter tuple
	n := len(vParts)
	if len(mParts) < n {
		n = len(mParts)
	}
	for i := range n {
		if vParts[i] > mParts[i] {
			return true
		}
		if vParts[i] < mParts[i] {
			return false
		}
	}

	// All compared components are equal — satisfied if version has at least
	// as many components as minimum (or more).
	return len(vParts) >= len(mParts)
}

// ── Semver comparison helpers ──
// Moved from internal/core/binary/resolver.go per verdict #30.

// SemverGreater returns true if version a is semantically greater than version b.
func SemverGreater(a, b string) bool {
	va := ParseSemverInts(a)
	vb := ParseSemverInts(b)
	for i := 0; i < len(va) && i < len(vb); i++ {
		if va[i] != vb[i] {
			return va[i] > vb[i]
		}
	}
	return len(va) > len(vb)
}

// SortVersions sorts a slice of version strings in descending order (newest
// first). Pass asc=true for ascending order.
func SortVersions(versions []string, asc ...bool) {
	ascending := len(asc) > 0 && asc[0]
	sort.Slice(versions, func(i, j int) bool {
		if ascending {
			return SemverGreater(versions[j], versions[i])
		}
		return SemverGreater(versions[i], versions[j])
	})
}

// ParseSemverInts splits a version string into numeric components for comparison.
func ParseSemverInts(v string) []int {
	clean := strings.TrimPrefix(v, "v")
	parts := strings.Split(clean, ".")
	var nums []int
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			break
		}
		nums = append(nums, n)
	}
	return nums
}

// ── Version Validation & Extraction ─────────────────────────────────────────

// semverPattern matches a clean numeric version string like "6.1.0" or "5.10".
var semverPattern = regexp.MustCompile(`^\d+(\.\d+)*$`)

// IsValidVersion returns true if v is a non-empty string containing only
// digits and dots (e.g. "6.1.0", "5.10", "1").
func IsValidVersion(v string) bool {
	return v != "" && semverPattern.MatchString(v)
}

// versionExtractPattern matches a version in a filename.
// Not anchored — filenames may have extensions or suffixes after the version.
var versionExtractPattern = regexp.MustCompile(`-v?(\d+(?:\.\d+)*)`)

// ExtractVersionFromFilename extracts a numeric version from a filename suffix.
// Returns the version string and true if found, or ("", false) otherwise.
//
//	vmlinux-6.1.0-x86_64  -> "6.1.0", true
//	vmlinux-v6.1.0-arm64  -> "6.1.0", true  (v prefix is stripped)
//	vmlinux               -> "", false
func ExtractVersionFromFilename(name string) (string, bool) {
	m := versionExtractPattern.FindStringSubmatch(name)
	if len(m) >= 2 && m[1] != "" {
		return m[1], true
	}
	return "", false
}
