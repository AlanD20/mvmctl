package image

// Internal package: tests both exported and unexported functions.
// Unexported functions (e.g., getTemplateVariables, isImageNotFoundError)
// cannot be accessed from an external test package.

import (
	"errors"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"

	"mvmctl/internal/lib/download"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// --- calculateMinimumImageSizeMB ---
// Rationale: calculateMinimumImageSizeMB determines the minimum rootfs size from
// content bytes with headroom. A bug here would create filesystems too small for
// the image or waste disk space on tiny images.

func TestCalculateMinimumImageSizeMB(t *testing.T) {
	tests := map[string]struct {
		contentBytes int64
		want         int
	}{
		// Edge cases first — boundaries around the minimum threshold
		"zero_bytes_returns_minimum":    {contentBytes: 0, want: MinRootfsSizeMiB},
		"small_content_returns_minimum": {contentBytes: 50 * MiB, want: MinRootfsSizeMiB},
		"just_below_threshold": {
			contentBytes: 102 * MiB,
			want:         MinRootfsSizeMiB,
		}, // 102*1.25=127.5→int=127 < 128
		"at_exact_threshold": {
			contentBytes: 103 * MiB,
			want:         128,
		}, // 103*1.25=128.75→int=128 ≥ 128

		// Happy paths — calculated headroom above minimum
		"moderate_content": {contentBytes: 200 * MiB, want: 250},   // 200*1.25=250
		"large_content":    {contentBytes: 1024 * MiB, want: 1280}, // 1024*1.25=1280
		"single_mebibyte":  {contentBytes: MiB, want: MinRootfsSizeMiB},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := calculateMinimumImageSizeMB(tc.contentBytes)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("calculateMinimumImageSizeMB() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// --- specFromVersion ---
// Rationale: specFromVersion constructs an ImageSpec from a resolved VersionInfo.
// A bug here would produce incorrect image metadata (type, version, source, format)
// used by download, extraction, and provisioning — every subsequent pipeline step.

func TestSpecFromVersion(t *testing.T) {
	tests := map[string]struct {
		v    model.VersionInfo
		arch string
		want *model.ImageSpec
	}{
		// Edge cases
		"empty_version_info": {
			v:    model.VersionInfo{},
			arch: "x86_64",
			want: &model.ImageSpec{
				Name: " ",
				Arch: "x86_64",
			},
		},
		"empty_arch": {
			v: model.VersionInfo{
				Type:        "ubuntu",
				Version:     "22.04",
				DownloadURL: "https://example.com/img",
				Format:      "squashfs",
			},
			arch: "",
			want: &model.ImageSpec{
				Type:    "ubuntu",
				Version: "22.04",
				Name:    "ubuntu 22.04",
				Source:  "https://example.com/img",
				Format:  "squashfs",
				Arch:    "",
			},
		},

		// Happy paths
		"basic_version": {
			v: model.VersionInfo{
				Type:        "ubuntu",
				Version:     "22.04",
				DownloadURL: "https://example.com/ubuntu-22.04.squashfs",
				Format:      "squashfs",
			},
			arch: "x86_64",
			want: &model.ImageSpec{
				Type:    "ubuntu",
				Version: "22.04",
				Name:    "ubuntu 22.04",
				Source:  "https://example.com/ubuntu-22.04.squashfs",
				Format:  "squashfs",
				Arch:    "x86_64",
			},
		},
		"with_sha256_and_display_name": {
			v: model.VersionInfo{
				Type:        "debian",
				Version:     "12",
				DownloadURL: "http://example.com/debian-12.qcow2",
				SHA256URL:   "http://example.com/debian-12.sha256",
				Format:      "qcow2",
				DisplayName: "Debian 12 Bookworm",
			},
			arch: "aarch64",
			want: &model.ImageSpec{
				Type:    "debian",
				Version: "12",
				Name:    "debian 12",
				Source:  "http://example.com/debian-12.qcow2",
				Format:  "qcow2",
				Arch:    "aarch64",
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := specFromVersion(tc.v, tc.arch)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("specFromVersion() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// --- getTemplateVariables ---
// Rationale: getTemplateVariables provides the variable substitution map for
// source URL templates. Missing or incorrect keys would cause download URL
// resolution failures that manifest as confusing "image not found" errors.

func TestGetTemplateVariables(t *testing.T) {
	svc := &Service{}
	spec := &model.ImageSpec{
		Arch:    "x86_64",
		Type:    "ubuntu",
		Version: "22.04",
	}
	vars := svc.getTemplateVariables(spec, "ci-v1")

	tests := map[string]struct {
		key  string
		want string
	}{
		"ci_version_key":     {key: "ci_version", want: "ci-v1"},
		"arch_key":           {key: "arch", want: "x86_64"},
		"image_type_key":     {key: "image_type", want: "ubuntu"},
		"version_key":        {key: "version", want: "22.04"},
		"image_version_key":  {key: "image_version", want: "22.04"},
		"ubuntu_version_key": {key: "ubuntu_version", want: "22.04"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, ok := vars[tc.key]
			assert.True(t, ok, "getTemplateVariables() should contain key %q", tc.key)
			assert.Equal(t, tc.want, got, "getTemplateVariables()[%q]", tc.key)
		})
	}

	// Edge case: nil spec causes panic (the function dereferences spec.* directly).
	t.Run("nil_spec_panics", func(t *testing.T) {
		svc := &Service{}
		assert.Panics(t, func() {
			svc.getTemplateVariables(nil, "ci-v1")
		}, "getTemplateVariables(nil) should panic due to nil pointer dereference")
	})
}

// --- resolveConfigName ---
// Rationale: resolveConfigName maps a type name to its human-readable display
// name from the image type config. A bug here would show wrong or empty names
// in CLI list output and version display.

func TestResolveConfigName(t *testing.T) {
	configs := []download.ResolverConfig{
		{Type: "ubuntu", Name: "Ubuntu LTS"},
		{Type: "debian", Name: "Debian"},
		{Type: "alpine", Name: ""},
	}

	tests := map[string]struct {
		typeName string
		want     string
	}{
		// Edge cases first
		"nonexistent_type":     {typeName: "nonexistent", want: ""},
		"empty_type_name":      {typeName: "", want: ""},
		"type_with_empty_name": {typeName: "alpine", want: ""},

		// Happy paths
		"existing_type": {typeName: "ubuntu", want: "Ubuntu LTS"},
		"second_type":   {typeName: "debian", want: "Debian"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := resolveConfigName(configs, tc.typeName)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("resolveConfigName() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// --- isImageNotFoundError ---
// Rationale: isImageNotFoundError controls the fallthrough chain in Resolve().
// A false-negative (not detecting ImageNotFoundError) would propagate fatal
// errors instead of trying the next resolution method. A false-positive
// (detecting when it's not) would swallow real errors — both break resolution.

func TestIsImageNotFoundError(t *testing.T) {
	tests := map[string]struct {
		err  error
		want bool
	}{
		// Error paths first — must return false for non-matching errors
		"nil_error": {
			err:  nil,
			want: false,
		},
		"standard_go_error": {
			err:  errors.New("generic error"),
			want: false,
		},
		"other_image_domain_error": {
			err:  errs.New(errs.CodeImageError, "some image error"),
			want: false,
		},
		"wrapped_not_found": {
			// isImageNotFoundError uses direct type assertion (no unwrap).
			err:  fmt.Errorf("wrap: %w", errs.New(errs.CodeImageNotFound, "inner")),
			want: false,
		},
		"not_found_from_other_domain": {
			err:  errs.New(errs.CodeVMNotFound, "vm not found"),
			want: false,
		},

		// Happy paths
		"image_not_found_direct": {
			err:  errs.New(errs.CodeImageNotFound, "image x not found"),
			want: true,
		},
		"image_not_found_not_found_helper": {
			err:  errs.NotFound(errs.CodeImageNotFound, "image not found via helper"),
			want: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := isImageNotFoundError(tc.err)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("isImageNotFoundError() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// --- Skipped: copyViaSendfile, copyViaIO ---
// Rationale: Both functions perform real filesystem I/O (os.Open, unix.Sendfile,
// io.Copy between file descriptors). They take file paths, not io.Writer/io.Reader
// as originally assumed. These are integration-level functions tested by system
// tests (MaterializeTo fallback chain).
