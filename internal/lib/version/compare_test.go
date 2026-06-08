package version_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"mvmctl/internal/lib/version"
)

// ─── IsAtLeast ────────────────────────────────────────────────────────────────
// Rationale: Must compare dotted version strings correctly, handling unequal
// lengths, non-numeric suffixes, and edge cases.

func TestIsAtLeast(t *testing.T) {
	tests := []struct {
		name string
		ver  string
		min  string
		want bool
	}{
		// Exact match
		{"exact", "5.10.0", "5.10.0", true},

		// Greater
		{"major_greater", "6.0.0", "5.10.0", true},
		{"minor_greater", "5.11.0", "5.10.0", true},
		{"patch_greater", "5.10.1", "5.10.0", true},

		// Less
		{"major_less", "4.99.99", "5.0.0", false},
		{"minor_less", "5.9.0", "5.10.0", false},

		// Unequal length
		{"short_ver_long_min", "5.10", "5.10.0", false},
		{"long_ver_short_min", "5.10.1", "5.10", true},

		// Edge: non-numeric suffix (e.g. "5.10.0-arch1")
		{"non_numeric_suffix_stripped", "5.10.0-arch1", "5.10.0", true},
		{"non_numeric_suffix_below", "5.9.0-arch1", "5.10.0", false},

		// Edge: completely non-numeric → false
		{"non_numeric_version", "abc", "5.10.0", false},

		// Edge: empty string (not a valid version → false)
		{"empty_version", "", "5.10.0", false},
		{"empty_min", "5.10.0", "", false}, // parseVersionNums("") → nil → isAtLeast returns false
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := version.IsAtLeast(tt.ver, tt.min)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ─── IsAtLeastFor ─────────────────────────────────────────────────────────────
// Rationale: Feature-gated version check. Min version comes from feature constant.

func TestIsAtLeastFor(t *testing.T) {
	tests := []struct {
		name    string
		ver     string
		feature version.Feature
		want    bool
	}{
		{"hotplug_above", "2.0.0", version.FeatureHotplug, true},
		{"hotplug_exact", "1.16", version.FeatureHotplug, true},
		{"hotplug_below", "1.15.0", version.FeatureHotplug, false},
		{"hotunplug_above", "2.0.0", version.FeatureHotUnplug, true},
		{"hotunplug_below", "1.15.99", version.FeatureHotUnplug, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := version.IsAtLeastFor(tt.ver, tt.feature)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ─── SplitVersionParts ────────────────────────────────────────────────────────
// Rationale: Must split "X.Y.Z-prerelease" into release []int and pre-release
// suffix, handling missing pre-release, dotted releases, non-numeric gracefully.

func TestSplitVersionParts(t *testing.T) {
	tests := []struct {
		name        string
		v           string
		wantRelease []int
		wantPre     string
	}{
		{"simple", "1.2.3", []int{1, 2, 3}, ""},
		{"two_parts", "5.10", []int{5, 10}, ""},
		{"single", "42", []int{42}, ""},
		{"with_prerelease", "1.2.3-rc1", []int{1, 2, 3}, "rc1"},
		{"with_dev_prerelease", "2.0.0-dev20240101", []int{2, 0, 0}, "dev20240101"},
		{"non_numeric_part", "1.a.3", []int{1, 0, 3}, ""}, // Atoi("a") → 0
		{"empty", "", []int{0}, ""},                         // Split("") → [""], Atoi("") → 0
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rel, pre := version.SplitVersionParts(tt.v)
			assert.Equal(t, tt.wantRelease, rel)
			assert.Equal(t, tt.wantPre, pre)
		})
	}
}

// ─── CompareVersions ──────────────────────────────────────────────────────────
// Rationale: PEP 440-compatible comparison. Must handle release segments,
// pre-release tags (dev < alpha < beta < rc < release < post), different
// lengths, and bound the comparison correctly.

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		name string
		a    string
		b    string
		want int // >0 if a>b, <0 if a<b, 0 if equal
	}{
		// Equality
		{"equal_simple", "1.0.0", "1.0.0", 0},
		{"equal_no_pre", "2.3", "2.3", 0},

		// Release segment differences
		{"major_greater", "2.0.0", "1.0.0", 1},
		{"major_less", "1.0.0", "2.0.0", -1},
		{"minor_greater", "1.2.0", "1.1.0", 1},
		{"patch_greater", "1.0.1", "1.0.0", 1},

		// Different lengths — longer with same prefix is greater
		{"longer_greater", "1.0.1", "1.0", 1},
		{"shorter_less", "1.0", "1.0.1", -1},

		// Pre-release: release > pre-release
		{"release_greater_than_prerelease", "1.0.0", "1.0.0-rc1", 1},
		{"prerelease_less_than_release", "1.0.0-rc1", "1.0.0", -1},

		// Pre-release ordering: dev < alpha < beta < rc
		{"dev_less_than_alpha", "1.0.0-dev1", "1.0.0-alpha1", -1},
		{"alpha_less_than_beta", "1.0.0-alpha1", "1.0.0-beta1", -1},
		{"beta_less_than_rc", "1.0.0-beta1", "1.0.0-rc1", -1},
		{"rc_less_than_post", "1.0.0-rc1", "1.0.0-post1", -1},

		// Same pre-release type: compare numeric suffix
		{"same_pre_diff_num", "1.0.0-rc1", "1.0.0-rc2", -1},
		{"same_pre_equal_num", "1.0.0-rc2", "1.0.0-rc2", 0},

		// Variant names (alpha/a, beta/b)
		{"alpha_variant_a", "1.0.0-a1", "1.0.0-alpha1", 0},
		{"beta_variant_b", "1.0.0-b1", "1.0.0-beta1", 0},

		// Both pre-release, different types with same num
		{"dev_vs_alpha_same_num", "1.0.0-dev1", "1.0.0-alpha1", -1},
		{"alpha_vs_beta_same_num", "1.0.0-alpha1", "1.0.0-beta1", -1},

		// Real-world examples
		{"kernel_minimum", "6.2.0", "5.10.0", 1},
		{"kernel_below", "5.4.0", "5.10.0", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := version.CompareVersions(tt.a, tt.b)
			if tt.want == 0 {
				assert.Equal(t, 0, got, "expected %s == %s", tt.a, tt.b)
			} else if tt.want > 0 {
				assert.Greater(t, got, 0, "expected %s > %s", tt.a, tt.b)
			} else {
				assert.Less(t, got, 0, "expected %s < %s", tt.a, tt.b)
			}
		})
	}
}
