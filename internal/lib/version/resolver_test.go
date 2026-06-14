package version_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/version"
)

// intPtr returns a pointer to n for constructing VersionSpec fields.
func intPtr(n int) *int { return &n }

// ─── VersionSpec.IsPartial ─────────────────────────────────────────────────────
// Rationale: IsPartial controls how Resolve selects a version (exact vs prefix
// match). An incorrect is-partial check would silently switch resolution strategies
// and return the wrong version.

func TestVersionSpec_IsPartial(t *testing.T) {
	tests := map[string]struct {
		spec version.VersionSpec
		want bool
	}{
		"empty_spec": {
			spec: version.VersionSpec{},
			want: true,
		},
		"major_only": {
			spec: version.VersionSpec{Major: intPtr(1)},
			want: true,
		},
		"major_minor": {
			spec: version.VersionSpec{Major: intPtr(1), Minor: intPtr(15)},
			want: true,
		},
		"major_minor_patch": {
			spec: version.VersionSpec{Major: intPtr(1), Minor: intPtr(15), Patch: intPtr(1)},
			want: false,
		},
		"latest_only": {
			spec: version.VersionSpec{IsLatest: true},
			want: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := tc.spec.IsPartial()
			assert.Equal(t, tc.want, got)
		})
	}
}

// ─── ParseSpec ─────────────────────────────────────────────────────────────────
// Rationale: ParseSpec is the entry point for all version resolution — it converts
// user-provided version strings into structured specs. A bug here would cause
// silent resolution failures (e.g. "v1.15.1" parsed as major=v only).

func TestParseSpec(t *testing.T) {
	tests := map[string]struct {
		spec    string
		want    version.VersionSpec
		wantErr string
	}{
		// Error paths first
		"non_numeric": {
			spec:    "abc",
			wantErr: "invalid version spec",
		},
		"partial_non_numeric_minor": {
			spec:    "1.abc",
			wantErr: "invalid version spec",
		},
		"partial_non_numeric_patch": {
			spec:    "1.15.abc",
			wantErr: "invalid version spec",
		},
		// Happy paths
		"empty_string": {
			spec: "",
			want: version.VersionSpec{IsLatest: true},
		},
		"latest_literal": {
			spec: "latest",
			want: version.VersionSpec{IsLatest: true},
		},
		"major_only": {
			spec: "1",
			want: version.VersionSpec{Major: intPtr(1)},
		},
		"major_minor": {
			spec: "1.15",
			want: version.VersionSpec{Major: intPtr(1), Minor: intPtr(15)},
		},
		"full_semver": {
			spec: "1.15.1",
			want: version.VersionSpec{Major: intPtr(1), Minor: intPtr(15), Patch: intPtr(1)},
		},
		"v_prefix": {
			spec: "v1.15.1",
			want: version.VersionSpec{Major: intPtr(1), Minor: intPtr(15), Patch: intPtr(1)},
		},
		"V_uppercase_prefix": {
			spec: "V1.15.1",
			want: version.VersionSpec{Major: intPtr(1), Minor: intPtr(15), Patch: intPtr(1)},
		},
		"v_with_nothing_after_is_error": {
			spec:    "v",
			wantErr: "invalid version spec",
		},
		"zero_major": {
			spec: "0",
			want: version.VersionSpec{Major: intPtr(0)},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := version.ParseSpec(tc.spec)

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}

			require.NoError(t, err)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ParseSpec() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── ParseSelector ─────────────────────────────────────────────────────────────
// Rationale: ParseSelector splits "type:version" selectors used in binary
// resolution. Incorrect splitting would misroute version lookups across domains.

func TestParseSelector(t *testing.T) {
	tests := map[string]struct {
		selector    string
		wantName    string
		wantVersion string
	}{
		"type_and_version": {
			selector:    "firecracker:1.15",
			wantName:    "firecracker",
			wantVersion: "1.15",
		},
		"version_only": {
			selector:    "1.15",
			wantName:    "",
			wantVersion: "1.15",
		},
		"type_only_returns_as_version": {
			selector:    "firecracker",
			wantName:    "",
			wantVersion: "firecracker",
		},
		"colon_prefix_only": {
			selector:    ":1.15",
			wantName:    "",
			wantVersion: "1.15",
		},
		"type_trailing_colon": {
			selector:    "firecracker:",
			wantName:    "firecracker",
			wantVersion: "",
		},
		"empty_selector": {
			selector:    "",
			wantName:    "",
			wantVersion: "",
		},
		"multi_colon": {
			selector:    "firecracker:1.15:extra",
			wantName:    "firecracker",
			wantVersion: "1.15:extra",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			gotName, gotVersion := version.ParseSelector(tc.selector)
			assert.Equal(t, tc.wantName, gotName)
			assert.Equal(t, tc.wantVersion, gotVersion)
		})
	}
}

// ─── SemverKey ─────────────────────────────────────────────────────────────────
// Rationale: SemverKey converts version strings to sortable integer slices used
// by Resolve for ordering. A sorting bug would cause "latest" to return the wrong
// version or partial matching to find a non-maximal match.

func TestSemverKey(t *testing.T) {
	tests := map[string]struct {
		v    string
		want []int
	}{
		// Error paths — returns []int{0} on any parse failure
		"non_numeric": {
			v:    "abc",
			want: []int{0},
		},
		"partial_non_numeric": {
			v:    "1.abc",
			want: []int{0},
		},
		"empty_string": {
			v:    "",
			want: []int{0},
		},
		// Happy paths
		"full_semver": {
			v:    "1.15.1",
			want: []int{1, 15, 1},
		},
		"v_prefix_stripped": {
			v:    "v1.15.1",
			want: []int{1, 15, 1},
		},
		"major_only": {
			v:    "1",
			want: []int{1},
		},
		"major_minor": {
			v:    "1.15",
			want: []int{1, 15},
		},
		"zero_version": {
			v:    "0.0.0",
			want: []int{0, 0, 0},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := version.SemverKey(tc.v)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("SemverKey() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── Resolve ───────────────────────────────────────────────────────────────────
// Rationale: Resolve is the core version selection function — it decides which
// binary/image version to use. Bugs here cause silent rollback or version not
// found errors at runtime.

func TestResolve(t *testing.T) {
	tests := map[string]struct {
		versions []string
		spec     version.VersionSpec
		want     string
		wantErr  string
	}{
		// Error paths first
		"empty_versions": {
			versions: nil,
			spec:     version.VersionSpec{},
			wantErr:  "No versions available",
		},
		"exact_match_not_found": {
			versions: []string{"1.15.0", "1.16.0"},
			spec:     version.VersionSpec{Major: intPtr(1), Minor: intPtr(15), Patch: intPtr(2)},
			wantErr:  "not found in available versions",
		},
		"partial_no_match": {
			versions: []string{"2.0.0", "2.1.0"},
			spec:     version.VersionSpec{Major: intPtr(1)},
			wantErr:  "No version matching spec",
		},
		// Happy paths
		"latest_returns_highest": {
			versions: []string{"1.15.0", "1.16.0", "1.14.0"},
			spec:     version.VersionSpec{IsLatest: true},
			want:     "1.16.0",
		},
		"latest_preserves_v_prefix": {
			versions: []string{"v1.15.0", "v1.16.0", "v1.14.0"},
			spec:     version.VersionSpec{IsLatest: true},
			want:     "v1.16.0",
		},
		"exact_match": {
			versions: []string{"1.15.0", "1.15.1", "1.16.0"},
			spec:     version.VersionSpec{Major: intPtr(1), Minor: intPtr(15), Patch: intPtr(1)},
			want:     "1.15.1",
		},
		"exact_match_v_prefix_in_list": {
			versions: []string{"v1.15.0", "v1.15.1", "v1.16.0"},
			spec:     version.VersionSpec{Major: intPtr(1), Minor: intPtr(15), Patch: intPtr(1)},
			want:     "1.15.1",
		},
		"partial_major_match_returns_highest": {
			versions: []string{"1.14.0", "1.16.0", "1.15.0"},
			spec:     version.VersionSpec{Major: intPtr(1)},
			want:     "1.16.0",
		},
		"partial_major_minor_match": {
			versions: []string{"1.15.0", "1.15.1", "1.16.0"},
			spec:     version.VersionSpec{Major: intPtr(1), Minor: intPtr(15)},
			want:     "1.15.1",
		},
		"partial_match_v_prefix_in_list": {
			versions: []string{"v1.15.0", "v1.15.1", "v1.16.0"},
			spec:     version.VersionSpec{Major: intPtr(1), Minor: intPtr(15)},
			want:     "1.15.1",
		},
		"latest_with_single_version": {
			versions: []string{"1.15.1"},
			spec:     version.VersionSpec{IsLatest: true},
			want:     "1.15.1",
		},
		"exact_match_lowest": {
			versions: []string{"1.15.0", "1.14.0"},
			spec:     version.VersionSpec{Major: intPtr(1), Minor: intPtr(14), Patch: intPtr(0)},
			want:     "1.14.0",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := version.Resolve(tc.versions, tc.spec)

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// ─── VersionGate.Require ───────────────────────────────────────────────────────
// Rationale: Require gates features behind a minimum binary version. A bug here
// would allow operations on incompatible binaries, causing silent failures.

func TestVersionGate_Require(t *testing.T) {
	tests := map[string]struct {
		binaryName string
		version    string
		minimum    string
		wantErr    string
	}{
		// Error paths first
		"empty_version": {
			binaryName: "Firecracker",
			version:    "",
			minimum:    "1.16",
			wantErr:    "Cannot determine Firecracker version",
		},
		"version_too_old": {
			binaryName: "Firecracker",
			version:    "1.15.1",
			minimum:    "1.16",
			wantErr:    "Firecracker v1.16+ required",
		},
		// Happy paths
		"dev_build_bypasses_gate": {
			binaryName: "Firecracker",
			version:    "dev-abc123",
			minimum:    "1.16",
		},
		"version_meets_requirement": {
			binaryName: "Firecracker",
			version:    "1.16.0",
			minimum:    "1.16",
		},
		"version_above_requirement": {
			binaryName: "Firecracker",
			version:    "2.0.0",
			minimum:    "1.16",
		},
		"exact_minimum": {
			binaryName: "Jailer",
			version:    "1.16",
			minimum:    "1.16",
		},
		"different_binary_name": {
			binaryName: "jailer",
			version:    "1.17.0",
			minimum:    "1.16",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var gate version.VersionGate
			err := gate.Require(tc.binaryName, tc.version, tc.minimum)

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}

			require.NoError(t, err)
		})
	}
}

// ─── VersionGate.ParseVersion ──────────────────────────────────────────────────
// Rationale: ParseVersion extracts numeric version components from version strings
// with non-numeric suffixes. A bug here would cause IsSatisfiedBy to make incorrect
// comparisons, silently skipping version gates.

func TestVersionGate_ParseVersion(t *testing.T) {
	tests := map[string]struct {
		version string
		want    []int
	}{
		// Error paths — returns nil for unparseable input
		"empty": {
			version: "",
			want:    nil,
		},
		"non_numeric": {
			version: "abc",
			want:    nil,
		},
		// Happy paths
		"full_semver": {
			version: "1.15.1",
			want:    []int{1, 15, 1},
		},
		"with_dev_suffix": {
			version: "1.15.1-dev20240101",
			want:    []int{1, 15, 1},
		},
		"major_minor": {
			version: "1.15",
			want:    []int{1, 15},
		},
		"major_only": {
			version: "1",
			want:    []int{1},
		},
		"zero_version": {
			version: "0.0.0",
			want:    []int{0, 0, 0},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var gate version.VersionGate
			got := gate.ParseVersion(tc.version)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ParseVersion() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── VersionGate.IsSatisfiedBy ────────────────────────────────────────────────
// Rationale: IsSatisfiedBy is the core comparison that drives VersionGate.Require.
// A comparison bug would gate features incorrectly (either blocking valid uses
// or allowing incompatible operations).

func TestVersionGate_IsSatisfiedBy(t *testing.T) {
	tests := map[string]struct {
		version string
		minimum string
		want    bool
	}{
		// Error paths — unparseable versions return false
		"non_numeric_version": {
			version: "abc",
			minimum: "1.0.0",
			want:    false,
		},
		"non_numeric_minimum": {
			version: "1.0.0",
			minimum: "abc",
			want:    false,
		},
		// Happy paths
		"version_equals_minimum": {
			version: "1.16.0",
			minimum: "1.16",
			want:    true,
		},
		"version_exceeds_minimum": {
			version: "2.0.0",
			minimum: "1.16",
			want:    true,
		},
		"version_below_minimum": {
			version: "1.15.1",
			minimum: "1.16",
			want:    false,
		},
		"longer_version_equal_to_shorter_min": {
			version: "1.16.1",
			minimum: "1.16",
			want:    true,
		},
		"shorter_version_not_equal_to_longer_min": {
			version: "1.16",
			minimum: "1.16.0",
			want:    false,
		},
		"shorter_below_longer_min": {
			version: "1.15",
			minimum: "1.16.0",
			want:    false,
		},
		"version_major_above": {
			version: "2.0",
			minimum: "1.16.0",
			want:    true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var gate version.VersionGate
			got := gate.IsSatisfiedBy(tc.version, tc.minimum)
			assert.Equal(t, tc.want, got)
		})
	}
}

// ─── SemverGreater ────────────────────────────────────────────────────────────
// Rationale: SemverGreater is used by SortVersions and Resolve for ordering.
// An incorrect comparison would produce wrong sort order (oldest first instead
// of newest first).

func TestSemverGreater(t *testing.T) {
	tests := map[string]struct {
		a, b string
		want bool
	}{
		"major_greater": {
			a: "2.0.0", b: "1.0.0", want: true,
		},
		"major_less": {
			a: "1.0.0", b: "2.0.0", want: false,
		},
		"minor_greater": {
			a: "1.16.0", b: "1.15.0", want: true,
		},
		"patch_greater": {
			a: "1.15.1", b: "1.15.0", want: true,
		},
		"equal": {
			a: "1.15.1", b: "1.15.1", want: false,
		},
		"v_prefix_stripped": {
			a: "v1.16.0", b: "1.15.0", want: true,
		},
		"shorter_greater_than_longer": {
			a: "1.16", b: "1.15.1", want: true,
		},
		"shorter_less_than_longer": {
			a: "1.15", b: "1.15.1", want: false,
		},
		"non_numeric_a_versus_valid_b": {
			a: "abc", b: "1.0.0", want: false,
		},
		"valid_a_versus_non_numeric_b": {
			a: "1.0.0", b: "abc", want: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := version.SemverGreater(tc.a, tc.b)
			assert.Equal(t, tc.want, got)
		})
	}
}

// ─── SortVersions ──────────────────────────────────────────────────────────────
// Rationale: SortVersions orders version lists for display and selection. An
// incorrect sort would show users the wrong "latest" version.

func TestSortVersions(t *testing.T) {
	tests := map[string]struct {
		input []string
		asc   []bool
		want  []string
	}{
		"descending_default": {
			input: []string{"1.15.0", "1.16.0", "1.14.0"},
			want:  []string{"1.16.0", "1.15.0", "1.14.0"},
		},
		"ascending": {
			input: []string{"1.16.0", "1.14.0", "1.15.0"},
			asc:   []bool{true},
			want:  []string{"1.14.0", "1.15.0", "1.16.0"},
		},
		"empty_slice": {
			input: []string{},
			want:  []string{},
		},
		"single_element": {
			input: []string{"1.15.1"},
			want:  []string{"1.15.1"},
		},
		"with_v_prefix": {
			input: []string{"v1.15.0", "v1.16.0", "v1.14.0"},
			want:  []string{"v1.16.0", "v1.15.0", "v1.14.0"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := make([]string, len(tc.input))
			copy(got, tc.input)
			version.SortVersions(got, tc.asc...)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("SortVersions() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── ParseSemverInts ───────────────────────────────────────────────────────────
// Rationale: ParseSemverInts extracts numeric version components up to the first
// non-numeric segment. Used by SemverGreater for ordering. A bug would cause
// incorrect comparison results.

func TestParseSemverInts(t *testing.T) {
	tests := map[string]struct {
		v    string
		want []int
	}{
		// Error/edge paths — stops at first non-numeric, returns partial result
		"non_numeric": {
			v:    "abc",
			want: nil,
		},
		"empty_string": {
			v:    "",
			want: nil,
		},
		"partial_non_numeric": {
			v:    "1.abc",
			want: []int{1},
		},
		// Happy paths
		"full_semver": {
			v:    "1.15.1",
			want: []int{1, 15, 1},
		},
		"v_prefix_stripped": {
			v:    "v1.15.1",
			want: []int{1, 15, 1},
		},
		"major_only": {
			v:    "1",
			want: []int{1},
		},
		"major_minor": {
			v:    "1.15",
			want: []int{1, 15},
		},
		"zero_version": {
			v:    "0.0.0",
			want: []int{0, 0, 0},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := version.ParseSemverInts(tc.v)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ParseSemverInts() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── IsValidVersion ────────────────────────────────────────────────────────────
// Rationale: IsValidVersion is used to validate resolved version strings before
// use. Accepting invalid versions would cause failures in downstream consumers
// that expect a strict "digits and dots" format.

func TestIsValidVersion(t *testing.T) {
	tests := map[string]struct {
		v    string
		want bool
	}{
		"invalid_empty":        {v: "", want: false},
		"invalid_non_numeric":  {v: "abc", want: false},
		"invalid_with_suffix":  {v: "1.2.3-beta", want: false},
		"invalid_leading_dot":  {v: ".1.2", want: false},
		"invalid_trailing_dot": {v: "1.2.", want: false},
		"valid_full_semver":    {v: "6.1.0", want: true},
		"valid_major_minor":    {v: "5.10", want: true},
		"valid_major_only":     {v: "1", want: true},
		"valid_many_parts":     {v: "1.2.3.4.5", want: true},
		"valid_zero":           {v: "0.0.0", want: true},
		"valid_single_zero":    {v: "0", want: true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := version.IsValidVersion(tc.v)
			assert.Equal(t, tc.want, got)
		})
	}
}

// ─── ExtractVersionFromFilename ────────────────────────────────────────────────
// Rationale: ExtractVersionFromFilename parses version strings out of binary
// filenames like "vmlinux-6.1.0-x86_64". A mismatch here would prevent VM
// kernel/binary discovery, causing silent startup failures.

func TestExtractVersionFromFilename(t *testing.T) {
	tests := map[string]struct {
		name    string
		wantVer string
		wantOK  bool
	}{
		// No match
		"no_version_in_filename": {
			name:    "vmlinux",
			wantVer: "", wantOK: false,
		},
		"no_dash_separator": {
			name:    "vmlinux6.1.0",
			wantVer: "", wantOK: false,
		},
		"non_numeric_after_dash": {
			name:    "no-version-here",
			wantVer: "", wantOK: false,
		},
		// Happy paths
		"standard_kernel_filename": {
			name:    "vmlinux-6.1.0-x86_64",
			wantVer: "6.1.0", wantOK: true,
		},
		"with_v_prefix_after_dash": {
			name:    "vmlinux-v6.1.0-arm64",
			wantVer: "6.1.0", wantOK: true,
		},
		"firecracker_binary": {
			name:    "firecracker-v1.15.1",
			wantVer: "1.15.1", wantOK: true,
		},
		"no_v_prefix_binary": {
			name:    "firecracker-1.15.1",
			wantVer: "1.15.1", wantOK: true,
		},
		"version_at_end": {
			name:    "some-tool-5.10",
			wantVer: "5.10", wantOK: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			gotVer, gotOK := version.ExtractVersionFromFilename(tc.name)
			assert.Equal(t, tc.wantVer, gotVer)
			assert.Equal(t, tc.wantOK, gotOK)
		})
	}
}
