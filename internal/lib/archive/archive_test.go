package archive

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// --- formatFromExtension ---
// Rationale: formatFromExtension maps file extensions to archive formats.
// Used as a fallback when magic-byte detection fails (e.g., file not found,
// unreadable, or truncated). Case-insensitive matching ensures correct
// detection regardless of filename casing.

func TestFormatFromExtension(t *testing.T) {
	tests := map[string]struct {
		path string
		want Format
	}{
		"empty_string": {
			path: "",
			want: FormatUnknown,
		},
		"no_extension": {
			path: "noextension",
			want: FormatUnknown,
		},
		"unknown_extension": {
			path: "file.txt",
			want: FormatUnknown,
		},
		"unknown_extension_dot_prefix": {
			path: ".hidden",
			want: FormatUnknown,
		},
		"tar_extension": {
			path: "archive.tar",
			want: FormatTar,
		},
		"tar_gz_extension": {
			path: "archive.tar.gz",
			want: FormatTarGzip,
		},
		"tgz_extension": {
			path: "archive.tgz",
			want: FormatTarGzip,
		},
		"tar_xz_extension": {
			path: "archive.tar.xz",
			want: FormatTarXz,
		},
		"case_insensitive_tar_gz": {
			path: "ARCHIVE.TAR.GZ",
			want: FormatTarGzip,
		},
		"case_insensitive_TGZ": {
			path: "ARCHIVE.TGZ",
			want: FormatTarGzip,
		},
		"case_insensitive_tar_xz": {
			path: "ARCHIVE.TAR.XZ",
			want: FormatTarXz,
		},
		"case_insensitive_tar": {
			path: "ARCHIVE.TAR",
			want: FormatTar,
		},
		"mixed_case_tar_gz": {
			path: "Archive.Tar.Gz",
			want: FormatTarGzip,
		},
		"path_with_directories": {
			path: "/some/deep/path/file.tar.gz",
			want: FormatTarGzip,
		},
		"tar_gz_with_query": {
			path: "file.tar.gz?param=value",
			want: FormatUnknown,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := formatFromExtension(tc.path)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("formatFromExtension(%q) mismatch (-want +got):\n%s", tc.path, diff)
			}
		})
	}
}

// --- firstErr ---
// Rationale: firstErr returns a if non-nil, otherwise b. Used to propagate
// the first encountered error when collecting errors from sequential operations
// (e.g., close/flush after write). Returning the wrong error would silently
// swallow the root cause.

func TestFirstErr(t *testing.T) {
	errA := errors.New("error a")
	errB := errors.New("error b")

	tests := map[string]struct {
		a    error
		b    error
		want error
	}{
		"both_nil": {
			a: nil, b: nil, want: nil,
		},
		"a_nil_b_non_nil": {
			a: nil, b: errB, want: errB,
		},
		"a_non_nil_b_nil": {
			a: errA, b: nil, want: errA,
		},
		"both_non_nil": {
			a: errA, b: errB, want: errA,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := firstErr(tc.a, tc.b)
			if tc.want == nil {
				if got != nil {
					t.Errorf("firstErr() = %v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("firstErr() = nil, want non-nil")
			}
			if got.Error() != tc.want.Error() {
				t.Errorf("firstErr() = %q, want %q", got.Error(), tc.want.Error())
			}
		})
	}
}
