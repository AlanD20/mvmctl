package version_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"mvmctl/internal/lib/version"
)

// --- IsAtLeast ---
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

// --- IsAtLeastFor ---
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

// --- Compare ---
// Rationale: Full SemVer comparison with pre-release support. Must handle release
// segments, pre-release tags, different lengths, dotted pre-release segments,
// and numeric vs alphabetic pre-release segment ordering.

func TestCompare(t *testing.T) {
	tests := []struct {
		name string
		a    string
		b    string
		want int // >0 if a>b, <0 if a<b, 0 if equal
	}{
		// Equality
		{"equal_simple", "1.0.0", "1.0.0", 0},
		{"equal_no_pre", "2.3", "2.3", 0},
		{"equal_v_prefix", "v1.0.0", "1.0.0", 0},
		{"equal_both_v", "v1.0.0", "v1.0.0", 0},

		// Release segment differences
		{"major_greater", "2.0.0", "1.0.0", 1},
		{"major_less", "1.0.0", "2.0.0", -1},
		{"minor_greater", "1.2.0", "1.1.0", 1},
		{"patch_greater", "1.0.1", "1.0.0", 1},

		// Different lengths — longer with same prefix is greater
		{"longer_greater", "1.0.1", "1.0", 1},
		{"shorter_less", "1.0", "1.0.1", -1},

		// Pre-release: release > pre-release
		{"release_gt_prerelease", "1.0.0", "1.0.0-rc1", 1},
		{"prerelease_lt_release", "1.0.0-rc1", "1.0.0", -1},

		// Pre-release ordering: numeric < alphabetic segments
		{"numeric_lt_alpha_segment", "1.0.0-1", "1.0.0-alpha", -1},
		{"alpha_gt_numeric_segment", "1.0.0-alpha", "1.0.0-1", 1},

		// Alphabetical comparison for non-numeric
		{"alpha_lt_beta", "1.0.0-alpha", "1.0.0-beta", -1},
		{"beta_gt_alpha", "1.0.0-beta", "1.0.0-alpha", 1},
		{"equal_alpha", "1.0.0-alpha", "1.0.0-alpha", 0},

		// Same pre-release type: compare numeric suffix
		{"rc_num_less", "1.0.0-rc1", "1.0.0-rc2", -1},
		{"rc_num_equal", "1.0.0-rc2", "1.0.0-rc2", 0},
		{"rc_num_greater", "1.0.0-rc2", "1.0.0-rc1", 1},

		// Dev numeric ordering
		{"dev_num_less", "1.0.0-dev1", "1.0.0-dev2", -1},
		{"dev_num_equal", "1.0.0-dev1", "1.0.0-dev1", 0},

		// Dotted pre-release segments
		{"dotted_prerelease", "1.0.0-alpha.1", "1.0.0-alpha.1", 0},
		{"dotted_prerelease_lt", "1.0.0-alpha.1", "1.0.0-alpha.2", -1},
		{"dotted_prerelease_segment_lt", "1.0.0-alpha.1", "1.0.0-beta.1", -1},

		// Pre-release vs non-pre-release same release
		{"same_release_different_pre", "1.0.0-rc1", "1.0.0-dev1", 1}, // prefix ordering rc(3) > dev(0)
		{"pre_vs_none", "1.0.0-dev1", "1.0.0", -1},

		// Real-world examples
		{"kernel_6_gt_5", "6.2.0", "5.10.0", 1},
		{"kernel_5_lt_6", "5.4.0", "5.10.0", -1},

		// Empty pre-release parts
		{"both_no_pre", "1.0.0", "1.0.0", 0},

		// SemVer lexical ordering differs from PEP 440 ranks
		{"dev_lt_alpha", "1.0.0-dev", "1.0.0-alpha", -1}, // prefix ordering dev(0) < alpha(1)

		// Pre-release alphabetics — full ordering chain
		{"beta_lt_rc", "1.0.0-beta", "1.0.0-rc", -1}, // "b" < "r" lexicographically

		// Known-prefix comparison: "rc" prefix stripped, numeric suffix 10 > 2
		{"rc10_gt_rc2", "1.0.0-rc10", "1.0.0-rc2", 1},
		// Dotted segments split into alphabetic "rc" + numeric suffix, so 10 > 2
		{"dotted_rc10_gt_rc2", "1.0.0-rc.10", "1.0.0-rc.2", 1},

		// Non-numeric release parts
		{"non_numeric_release_part", "1.a.3", "1.0.3", 0}, // Atoi("a")→0, so [1,0,3] == [1,0,3]

		// Empty string handling
		{"empty_both", "", "", 0},
		{"empty_vs_valid", "", "1.0.0", -1}, // [0] < [1,0,0]

		// v prefix with prerelease
		{"v_prefix_prerelease", "v1.0.0-rc1", "1.0.0-rc1", 0},

		// Dotted pre-release with mixed numeric/alpha segments
		{"dotted_mixed_segments", "1.0.0-1.dev", "1.0.0-1.alpha", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := version.Compare(tt.a, tt.b)
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
