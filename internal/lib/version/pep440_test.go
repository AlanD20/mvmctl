package version_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"

	"mvmctl/internal/lib/version"
)

// --- SplitVersionParts (additional edge cases) ---
// Rationale: Extends coverage beyond compare_test.go's TestSplitVersionParts with
// four-part versions, multi-dash pre-releases, and dashes-without-digits. These
// edge cases exercise the full SplitVersionParts codepath.

func TestSplitVersionPartsEdgeCases(t *testing.T) {
	tests := map[string]struct {
		v           string
		wantRelease []int
		wantPre     string
	}{
		"plain_release": {
			v: "1.2.3", wantRelease: []int{1, 2, 3}, wantPre: "",
		},
		"major_minor": {
			v: "5.10", wantRelease: []int{5, 10}, wantPre: "",
		},
		"single_component": {
			v: "42", wantRelease: []int{42}, wantPre: "",
		},
		"with_rc_prerelease": {
			v: "1.2.3-rc1", wantRelease: []int{1, 2, 3}, wantPre: "rc1",
		},
		"with_dev_prerelease": {
			v: "2.0.0-dev20240101", wantRelease: []int{2, 0, 0}, wantPre: "dev20240101",
		},
		"non_numeric_part_defaults_to_zero": {
			v: "1.a.3", wantRelease: []int{1, 0, 3}, wantPre: "",
		},
		"empty_string": {
			v: "", wantRelease: []int{0}, wantPre: "",
		},
		"four_parts": {
			v: "1.2.3.4", wantRelease: []int{1, 2, 3, 4}, wantPre: "",
		},
		"multi_dash_in_prerelease": {
			v: "1.0.0-alpha.1", wantRelease: []int{1, 0, 0}, wantPre: "alpha.1",
		},
		"all_zeros": {
			v: "0.0.0", wantRelease: []int{0, 0, 0}, wantPre: "",
		},
		"dash_without_digits_after": {
			v: "1.0-", wantRelease: []int{1, 0}, wantPre: "",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			gotRelease, gotPre := version.SplitVersionParts(tc.v)
			if diff := cmp.Diff(tc.wantRelease, gotRelease); diff != "" {
				t.Errorf("SplitVersionParts() release mismatch (-want +got):\n%s", diff)
			}
			assert.Equal(t, tc.wantPre, gotPre)
		})
	}
}

// --- CompareVersions (pre-release edge cases) ---
// Rationale: Tests pre-release tag comparisons (dev < alpha < beta < rc < post),
// release-vs-pre-release ordering, variant names (a vs alpha, b vs beta), unknown
// tags, and numeric suffix ordering. Exercises comparePreRelease and
// parsePreReleaseTag indirectly (they are unexported — see compare_test.go for
// the basic CompareVersions coverage).

func TestCompareVersionsPreRelease(t *testing.T) {
	tests := map[string]struct {
		a, b string
		want int // >0 if a>b, <0 if a<b, 0 if equal
	}{
		// Release comparisons
		"equal_release": {"1.0.0", "1.0.0", 0},
		"major_greater": {"2.0.0", "1.0.0", 1},
		"major_less":    {"1.0.0", "2.0.0", -1},
		"minor_greater": {"1.2.0", "1.1.0", 1},
		"patch_greater": {"1.0.1", "1.0.0", 1},

		// Different lengths
		"longer_greater": {"1.0.1", "1.0", 1},
		"shorter_less":   {"1.0", "1.0.1", -1},

		// Release vs pre-release: release > any pre-release
		"release_gt_prerelease": {"1.0.0", "1.0.0-rc1", 1},
		"prerelease_lt_release": {"1.0.0-rc1", "1.0.0", -1},

		// Pre-release ordering: dev < alpha < beta < rc < post
		"dev_lt_alpha":  {"1.0.0-dev1", "1.0.0-alpha1", -1},
		"alpha_lt_beta": {"1.0.0-alpha1", "1.0.0-beta1", -1},
		"beta_lt_rc":    {"1.0.0-beta1", "1.0.0-rc1", -1},
		"rc_lt_post":    {"1.0.0-rc1", "1.0.0-post1", -1},

		// Same pre-release type, different numeric suffix
		"rc_num_less":    {"1.0.0-rc1", "1.0.0-rc2", -1},
		"rc_num_equal":   {"1.0.0-rc1", "1.0.0-rc1", 0},
		"rc_num_greater": {"1.0.0-rc2", "1.0.0-rc1", 1},

		// Dev numeric ordering
		"dev_num_less":       {"1.0.0-dev1", "1.0.0-dev2", -1},
		"dev_num_equal":      {"1.0.0-dev1", "1.0.0-dev1", 0},
		"dev_without_number": {"1.0.0-dev", "1.0.0-dev", 0},

		// Variant names (a == alpha, b == beta)
		"alpha_and_a_equal": {"1.0.0-alpha1", "1.0.0-a1", 0},
		"beta_and_b_equal":  {"1.0.0-beta1", "1.0.0-b1", 0},

		// Post vs other pre-release types
		"post_gt_rc":   {"1.0.0-post1", "1.0.0-rc1", 1},
		"post_gt_beta": {"1.0.0-post1", "1.0.0-beta1", 1},

		// Default/unknown tag — rank=3 (same as rc), num=0
		"unknown_lt_rc":      {"1.0.0-unknown", "1.0.0-rc1", -1},
		"unknown_eq_rc_zero": {"1.0.0-unknown", "1.0.0-rc0", 0},

		// Both have unknown tags — numeric suffix not parsed (Atoi on full tag = 0)
		"both_unknown_same": {"1.0.0-unknown", "1.0.0-unknown", 0},

		// Real-world scenarios
		"kernel_6_dot_2_gt_5_dot_10": {"6.2.0", "5.10.0", 1},
		"kernel_5_dot_4_lt_5_dot_10": {"5.4.0", "5.10.0", -1},

		// post tag is treated as pre-release (rank 5)
		"release_gt_post": {"1.0.0", "1.0.0-post1", 1},
		"post_lt_release": {"1.0.0-post1", "1.0.0", -1},

		// Both have same post tag
		"post_equal":    {"1.0.0-post1", "1.0.0-post1", 0},
		"post_num_less": {"1.0.0-post1", "1.0.0-post2", -1},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := version.CompareVersions(tc.a, tc.b)
			if tc.want == 0 {
				assert.Equal(t, 0, got, "expected %s == %s", tc.a, tc.b)
			} else if tc.want > 0 {
				assert.Greater(t, got, 0, "expected %s > %s", tc.a, tc.b)
			} else {
				assert.Less(t, got, 0, "expected %s < %s", tc.a, tc.b)
			}
		})
	}
}
