package version

import (
	"strconv"
	"strings"
)

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
