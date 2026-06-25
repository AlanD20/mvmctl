package version

import (
	"strconv"
	"strings"
)

// Compare compares two semver-like version strings.
// Returns positive if a > b, negative if a < b, 0 if equal.
// Handles "v" prefix stripping, pre-release suffixes (after "-"), and
// dotted pre-release segments (e.g. "alpha.1").
func Compare(a, b string) int {
	a = strings.TrimPrefix(a, "v")
	b = strings.TrimPrefix(b, "v")
	aRelease, aPre, _ := strings.Cut(a, "-")
	bRelease, bPre, _ := strings.Cut(b, "-")
	aParts := splitVersion(aRelease)
	bParts := splitVersion(bRelease)
	for i := 0; i < len(aParts) && i < len(bParts); i++ {
		if aParts[i] != bParts[i] {
			return aParts[i] - bParts[i]
		}
	}
	if len(aParts) != len(bParts) {
		return len(aParts) - len(bParts)
	}
	switch {
	case aPre == "" && bPre == "":
		return 0
	case aPre == "":
		return 1
	case bPre == "":
		return -1
	default:
		return comparePreReleaseParts(aPre, bPre)
	}
}

// splitVersion splits a dotted numeric version string into ints.
func splitVersion(v string) []int {
	parts := strings.Split(v, ".")
	nums := make([]int, len(parts))
	for i, p := range parts {
		n, _ := strconv.Atoi(p)
		nums[i] = n
	}
	return nums
}

// comparePreReleaseParts compares two pre-release tags segment by segment.
// Numeric segments compare numerically; alphabetic segments compare
// lexicographically; numeric < alphabetic.
func comparePreReleaseParts(a, b string) int {
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")
	for i := 0; i < len(aParts) && i < len(bParts); i++ {
		aNum, aIsNum := tryParseInt(aParts[i])
		bNum, bIsNum := tryParseInt(bParts[i])
		switch {
		case aIsNum && bIsNum:
			if aNum != bNum {
				return aNum - bNum
			}
		case aIsNum:
			return -1
		case bIsNum:
			return 1
		default:
			// Both are non-numeric strings — try known-prefix comparison
			aPfx, aN, aHasPfx := splitKnownPrefix(aParts[i])
			bPfx, bN, bHasPfx := splitKnownPrefix(bParts[i])
			if aHasPfx && bHasPfx {
				if aPfx != bPfx {
					return comparePrefix(aPfx, bPfx)
				}
				if aN != bN {
					return aN - bN
				}
				continue // same prefix, same number
			}
			// Fall back to lexicographic comparison
			if aParts[i] != bParts[i] {
				if aParts[i] > bParts[i] {
					return 1
				}
				return -1
			}
		}
	}
	return len(aParts) - len(bParts)
}

// tryParseInt attempts to parse s as an integer, returning the value
// and a boolean indicating success.
func tryParseInt(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

// knownPrefixes lists pre-release prefixes in ascending precedence order.
var knownPrefixes = []string{"dev", "alpha", "beta", "rc"}

// splitKnownPrefix splits a pre-release segment into a known prefix and its
// numeric suffix. Returns ("rc", 10) for "rc10", ("dev", 0) for "dev".
func splitKnownPrefix(s string) (prefix string, num int, ok bool) {
	for _, pfx := range knownPrefixes {
		if s == pfx {
			return pfx, 0, true
		}
		if strings.HasPrefix(s, pfx) {
			n, err := strconv.Atoi(strings.TrimPrefix(s, pfx))
			if err == nil {
				return pfx, n, true
			}
		}
	}
	return "", 0, false
}

// comparePrefix compares two known pre-release prefixes by their rank order.
// Order: dev(0) < alpha(1) < beta(2) < rc(3)
func comparePrefix(a, b string) int {
	var aRank, bRank int
	for i, pfx := range knownPrefixes {
		if pfx == a {
			aRank = i
		}
		if pfx == b {
			bRank = i
		}
	}
	return aRank - bRank
}

// IsAtLeast checks if ver >= minVersion.
func IsAtLeast(ver, minVersion string) bool {
	return isAtLeast(parseVersionNums(ver), parseVersionNums(minVersion))
}

// isAtLeast compares two parsed version component slices.
// Returns true if vParts >= mParts.
func isAtLeast(vParts, mParts []int) bool {
	if vParts == nil || mParts == nil {
		return false
	}
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
	return len(vParts) >= len(mParts)
}

// parseVersionNums extracts numeric version components from a version string.
func parseVersionNums(v string) []int {
	match := versionCleanPattern.FindStringSubmatch(v)
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

// Feature identifies a feature gated behind a minimum version.
type Feature string

const (
	FeatureHotplug   Feature = "1.16"
	FeatureHotUnplug Feature = "1.16"
)

// IsAtLeastFor checks if ver >= the minimum version required by feature f.
func IsAtLeastFor(ver string, f Feature) bool {
	return IsAtLeast(ver, string(f))
}
