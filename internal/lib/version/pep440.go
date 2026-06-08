package version

import (
	"strconv"
	"strings"
)

// CompareVersions performs PEP 440-aligned version comparison.
// Returns positive if a > b, negative if a < b, 0 if equal.
func CompareVersions(a, b string) int {
	aRelease, aPre := SplitVersionParts(a)
	bRelease, bPre := SplitVersionParts(b)

	for i := 0; i < len(aRelease) && i < len(bRelease); i++ {
		if aRelease[i] != bRelease[i] {
			return aRelease[i] - bRelease[i]
		}
	}
	if len(aRelease) != len(bRelease) {
		return len(aRelease) - len(bRelease)
	}

	if aPre == "" && bPre == "" {
		return 0
	}
	if aPre != "" && bPre != "" {
		return comparePreRelease(aPre, bPre)
	}
	if aPre != "" {
		return -1
	}
	return 1
}

// SplitVersionParts splits a version string into release components and
// an optional pre-release suffix (everything after the first '-').
func SplitVersionParts(v string) (release []int, preRelease string) {
	if before, after, found := strings.Cut(v, "-"); found {
		preRelease = after
		v = before
	}

	parts := strings.Split(v, ".")
	release = make([]int, len(parts))
	for i, p := range parts {
		n, _ := strconv.Atoi(p)
		release[i] = n
	}
	return
}

// comparePreRelease compares two PEP 440 pre-release tags.
func comparePreRelease(a, b string) int {
	aRank, aNum := parsePreReleaseTag(a)
	bRank, bNum := parsePreReleaseTag(b)
	if aRank != bRank {
		return aRank - bRank
	}
	return aNum - bNum
}

// parsePreReleaseTag extracts the rank and numeric suffix from a pre-release tag.
func parsePreReleaseTag(tag string) (rank int, num int) {
	tag = strings.ToLower(tag)
	switch {
	case strings.HasPrefix(tag, "dev"):
		rank = 0
		num, _ = strconv.Atoi(strings.TrimPrefix(tag, "dev"))
	case strings.HasPrefix(tag, "alpha") || strings.HasPrefix(tag, "a") && !strings.HasPrefix(tag, "al"):
		if strings.HasPrefix(tag, "alpha") {
			num, _ = strconv.Atoi(strings.TrimPrefix(tag, "alpha"))
		} else {
			num, _ = strconv.Atoi(strings.TrimPrefix(tag, "a"))
		}
		rank = 1
	case strings.HasPrefix(tag, "beta") || strings.HasPrefix(tag, "b") && !strings.HasPrefix(tag, "be"):
		if strings.HasPrefix(tag, "beta") {
			num, _ = strconv.Atoi(strings.TrimPrefix(tag, "beta"))
		} else {
			num, _ = strconv.Atoi(strings.TrimPrefix(tag, "b"))
		}
		rank = 2
	case strings.HasPrefix(tag, "rc"):
		rank = 3
		num, _ = strconv.Atoi(strings.TrimPrefix(tag, "rc"))
	case strings.HasPrefix(tag, "post"):
		rank = 5
		num, _ = strconv.Atoi(strings.TrimPrefix(tag, "post"))
	default:
		rank = 3
		num, _ = strconv.Atoi(tag)
	}
	return
}
