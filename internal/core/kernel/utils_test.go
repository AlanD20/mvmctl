package kernel

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/ptr"
	"mvmctl/internal/lib/download"
	"mvmctl/internal/lib/model"
)

// ─── ParseFilename ───────────────────────────────────────────────────────────
// Rationale: ParseFilename extracts version, architecture and base name from
// kernel image filenames. Incorrect parsing would cause version resolution and
// arch-matching failures that silently download wrong kernels.

func TestParseFilename(t *testing.T) {
	tests := map[string]struct {
		filename string
		want     ParsedKernelFilename
	}{
		// edge / degraded paths first
		"empty_string": {
			filename: "",
			want:     ParsedKernelFilename{BaseName: "", Version: "-", Arch: "-"},
		},
		"not_a_kernel_name": {
			filename: "not-a-kernel",
			want:     ParsedKernelFilename{BaseName: "not", Version: "-", Arch: "-"},
		},
		"missing_arch_suffix": {
			filename: "vmlinux-6.8",
			want:     ParsedKernelFilename{BaseName: "vmlinux", Version: "6.8", Arch: "-"},
		},
		"rc_version_not_stripped": {
			filename: "vmlinux-4.19.0-rc2-x86_64",
			want:     ParsedKernelFilename{BaseName: "vmlinux", Version: "-", Arch: "x86_64"},
		},
		// happy paths
		"x86_64_official": {
			filename: "vmlinux-6.8-x86_64",
			want:     ParsedKernelFilename{BaseName: "vmlinux", Version: "6.8", Arch: "x86_64"},
		},
		"aarch64_kernel": {
			filename: "vmlinux-5.10.0-aarch64",
			want:     ParsedKernelFilename{BaseName: "vmlinux", Version: "5.10.0", Arch: "aarch64"},
		},
		"amd64_alias": {
			filename: "vmlinux-6.1-amd64",
			want:     ParsedKernelFilename{BaseName: "vmlinux", Version: "6.1", Arch: "amd64"},
		},
		"arm64_alias": {
			filename: "vmlinux-6.8-arm64",
			want:     ParsedKernelFilename{BaseName: "vmlinux", Version: "6.8", Arch: "arm64"},
		},
		"v_prefix_preserved": {
			filename: "vmlinux-v6.8-x86_64",
			want:     ParsedKernelFilename{BaseName: "vmlinux", Version: "v6.8", Arch: "x86_64"},
		},
		"bzImage_base_name": {
			filename: "bzImage-5.15-x86_64",
			want:     ParsedKernelFilename{BaseName: "bzImage", Version: "5.15", Arch: "x86_64"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := ParseFilename(tc.filename)
			assert.Empty(t, cmp.Diff(tc.want, got))
		})
	}
}

// ─── extractConfigKey ────────────────────────────────────────────────────────
// Rationale: extractConfigKey identifies CONFIG_* keys from kernel .config
// lines. False positives would inject bogus keys into the override map; false
// negatives would silently drop intended config overrides.

func TestExtractConfigKey(t *testing.T) {
	tests := map[string]struct {
		line string
		want string
	}{
		// no-key paths first
		"empty_line": {
			line: "",
			want: "",
		},
		"whitespace_only": {
			line: "  \t  ",
			want: "",
		},
		"plain_comment_no_not_set": {
			line: "# some comment",
			want: "",
		},
		"not_set_non_config": {
			line: "# FOO is not set",
			want: "",
		},
		"config_without_equals": {
			line: "CONFIG_FOO",
			want: "",
		},
		"random_text": {
			line: "some random text",
			want: "",
		},
		// found paths
		"enabled_as_y": {
			line: "CONFIG_FOO=y",
			want: "CONFIG_FOO",
		},
		"enabled_as_value": {
			line: "CONFIG_FOO=42",
			want: "CONFIG_FOO",
		},
		"enabled_empty_value": {
			line: "CONFIG_FOO=",
			want: "CONFIG_FOO",
		},
		"is_not_set_disabled": {
			line: "# CONFIG_FOO is not set",
			want: "CONFIG_FOO",
		},
		"is_not_set_with_leading_space": {
			line: "  # CONFIG_FOO is not set",
			want: "CONFIG_FOO",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := extractConfigKey(tc.line)
			assert.Empty(t, cmp.Diff(tc.want, got))
		})
	}
}

// ─── majorMinorFromVersion ───────────────────────────────────────────────────
// Rationale: majorMinorFromVersion extracts the major.minor prefix from a full
// version string. A wrong prefix would cause kernel config paths and download
// URLs to point at the wrong upstream directory, failing silently.

func TestMajorMinorFromVersion(t *testing.T) {
	tests := map[string]struct {
		version string
		want    string
	}{
		// degraded paths first
		"empty_string": {
			version: "",
			want:    "",
		},
		"single_segment_no_dot": {
			version: "6",
			want:    "6",
		},
		"no_dots_at_all": {
			version: "invalid",
			want:    "invalid",
		},
		// happy paths
		"major_dot_minor": {
			version: "6.8",
			want:    "6.8",
		},
		"major_dot_minor_dot_patch": {
			version: "5.10.0",
			want:    "5.10",
		},
		"with_rc_suffix": {
			version: "4.19.0-rc2",
			want:    "4.19",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := majorMinorFromVersion(tc.version)
			assert.Empty(t, cmp.Diff(tc.want, got))
		})
	}
}

// ─── kernelSpecsToResolverConfigs ────────────────────────────────────────────
// Rationale: kernelSpecsToResolverConfigs converts domain KernelSpec structs
// into download.ResolverConfigs for the shared version resolver. An incorrect
// conversion would skip resolver fields, drop format types, or fail to expand
// resolver-specific templates, causing version listing or download failures.

func TestKernelSpecsToResolverConfigs(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		specs []*model.KernelSpec
		want  []download.ResolverConfig
	}{
		// empty / nil paths first
		"nil_specs": {
			specs: nil,
			want:  []download.ResolverConfig{},
		},
		"empty_slice": {
			specs: []*model.KernelSpec{},
			want:  []download.ResolverConfig{},
		},
		// happy paths
		"minimal_firecracker_spec": {
			specs: []*model.KernelSpec{
				{
					Name:       "fc-kernel",
					KernelType: infra.KernelTypeFirecracker,
				},
			},
			want: []download.ResolverConfig{
				{
					Type:   infra.KernelTypeFirecracker,
					Name:   "fc-kernel",
					Format: "vmlinux",
				},
			},
		},
		"minimal_official_spec": {
			specs: []*model.KernelSpec{
				{
					Name:       "official-kernel",
					KernelType: infra.KernelTypeOfficial,
				},
			},
			want: []download.ResolverConfig{
				{
					Type:   infra.KernelTypeOfficial,
					Name:   "official-kernel",
					Format: "tar.xz",
				},
			},
		},
		"full_fields_no_resolver": {
			specs: []*model.KernelSpec{
				{
					Name:       "full-kernel",
					KernelType: infra.KernelTypeFirecracker,
					Version:    "6.8",
					Source:     "https://example.com/source",
					SHA256URL:  "https://example.com/sha256",
				},
			},
			want: []download.ResolverConfig{
				{
					Type:      infra.KernelTypeFirecracker,
					Name:      "full-kernel",
					Version:   "6.8",
					Source:    "https://example.com/source",
					SHA256URL: "https://example.com/sha256",
					Format:    "vmlinux",
				},
			},
		},
		"http_dir_resolver": {
			specs: []*model.KernelSpec{
				{
					Name:        "http-kernel",
					KernelType:  infra.KernelTypeOfficial,
					Resolver:    ptr.Ptr("http-dir"),
					VersionsURL: ptr.Ptr("https://example.com/versions"),
					Source:      "https://example.com/download/",
					SHA256URL:   "https://example.com/sha256",
					FilePattern: ptr.Ptr("linux-"),
					FileSuffix:  ptr.Ptr(".tar.xz"),
					Options: map[string]any{
						"version_discoveries": []any{"v6.8", "v6.7"},
					},
				},
			},
			want: []download.ResolverConfig{
				{
					Type:        infra.KernelTypeOfficial,
					Name:        "http-kernel",
					Resolver:    "http-dir",
					VersionsURL: "https://example.com/versions",
					Source:      "https://example.com/download/",
					SHA256URL:   "https://example.com/sha256",
					Format:      "tar.xz",
					DownloadURL: "https://example.com/download/",
					Options: download.ResolverOptions{
						VersionDiscoveries: []string{"v6.8", "v6.7"},
						FilePattern:        "linux-",
						FileSuffix:         ".tar.xz",
					},
				},
			},
		},
		"firecracker_s3_resolver": {
			specs: []*model.KernelSpec{
				{
					Name:            "s3-kernel",
					KernelType:      infra.KernelTypeFirecracker,
					Resolver:        ptr.Ptr("firecracker-s3"),
					ListURLTemplate: ptr.Ptr("https://s3.example.com/{version}/list"),
					Source:          "https://s3.example.com/",
					SHA256URL:       "https://s3.example.com/sha256",
				},
			},
			want: []download.ResolverConfig{
				{
					Type:            infra.KernelTypeFirecracker,
					Name:            "s3-kernel",
					Resolver:        "firecracker-s3",
					ListURLTemplate: "https://s3.example.com//list",
					Source:          "https://s3.example.com/",
					SHA256URL:       "https://s3.example.com/sha256",
					Format:          "vmlinux",
					DownloadURL:     "https://s3.example.com/firecracker-ci/{ci_version}/{arch}/vmlinux-{version}",
					Options: download.ResolverOptions{
						S3VersionPattern: "vmlinux-([\\d.]+)",
					},
				},
			},
		},
		"multiple_specs_preserved": {
			specs: []*model.KernelSpec{
				{Name: "first", KernelType: infra.KernelTypeFirecracker},
				{Name: "second", KernelType: infra.KernelTypeOfficial},
			},
			want: []download.ResolverConfig{
				{Type: infra.KernelTypeFirecracker, Name: "first", Format: "vmlinux"},
				{Type: infra.KernelTypeOfficial, Name: "second", Format: "tar.xz"},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := kernelSpecsToResolverConfigs(tc.specs)
			assert.Empty(t, cmp.Diff(tc.want, got))
		})
	}
}
