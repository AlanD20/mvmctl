package image

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

func TestParseImageChecksum(t *testing.T) {
	sha256Hex := fmt.Sprintf("%x", sha256.Sum256([]byte("fixture image")))
	sha512Hex := fmt.Sprintf("%x", sha512.Sum512([]byte("fixture image")))
	tests := map[string]struct {
		content string
		size    int
		want    string
		wantErr bool
	}{
		"empty":                 {size: sha512.Size, wantErr: true},
		"invalid_hex":           {content: strings.Repeat("z", 128), size: sha512.Size, wantErr: true},
		"truncated_sha512":      {content: sha512Hex[:126], size: sha512.Size, wantErr: true},
		"sha256_is_not_sha512":  {content: sha256Hex, size: sha512.Size, wantErr: true},
		"sha512_is_not_sha256":  {content: sha512Hex, size: sha256.Size, wantErr: true},
		"unmatched_filename":    {content: sha512Hex + " other.vhd", size: sha512.Size, wantErr: true},
		"extra_fields":          {content: sha512Hex + " extra image.vhd", size: sha512.Size, wantErr: true},
		"multiple_bare_digests": {content: sha512Hex + "\n" + sha512Hex, size: sha512.Size, wantErr: true},
		"ambiguous_matching_filename": {
			content: sha512Hex + " image.vhd\n" + sha512Hex + " *image.vhd",
			size:    sha512.Size,
			wantErr: true,
		},
		"bare_sha512_with_source_name": {content: sha512Hex + "\n", size: sha512.Size, want: sha512Hex},
		"named_sha512":                 {content: sha512Hex + " image.vhd", size: sha512.Size, want: sha512Hex},
		"binary_filename_and_path": {
			content: sha512Hex + " */release/image.vhd",
			size:    sha512.Size,
			want:    sha512Hex,
		},
		"uppercase_sha512": {content: strings.ToUpper(sha512Hex), size: sha512.Size, want: sha512Hex},
		"sha256_manifest_selection": {
			content: sha256Hex + " other.vhd\n" + sha256Hex + " image.vhd",
			size:    sha256.Size,
			want:    sha256Hex,
		},
		"bare_sha256": {content: sha256Hex, size: sha256.Size, want: sha256Hex},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := parseImageChecksum(tc.content, "image.vhd", tc.size)
			if tc.wantErr {
				require.Error(t, err)
				var domainErr *errs.DomainError
				require.ErrorAs(t, err, &domainErr)
				assert.Equal(t, errs.CodeDownloadFailed, domainErr.Code)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestDownloadImageChecksum(t *testing.T) {
	// Rationale: Alpine's actual single-token SHA512 response must verify image
	// bytes; malformed configured metadata must not silently become an absent checksum.
	data := make([]byte, 1024)
	copy(data[512:], "conectix")
	data[512+11] = 2 // VHD footer reserved feature bit.
	data[512+63] = 2 // Fixed VHD disk type.
	sha512Hex := fmt.Sprintf("%x", sha512.Sum512(data))
	sha256Hex := fmt.Sprintf("%x", sha256.Sum256(data))
	tests := map[string]struct {
		metadata      string
		suffix        string
		status        int
		mirror        bool
		alterArtifact bool
		noMetadata    bool
		wantCode      errs.Code
		wantGets      int32
	}{
		"empty_metadata_preserves_mirror": {suffix: ".sha512", mirror: true, wantCode: errs.CodeDownloadFailed},
		"missing_metadata_preserves_mirror": {
			suffix:   ".sha512",
			status:   http.StatusNotFound,
			mirror:   true,
			wantCode: errs.CodeDownloadFailed,
		},
		"invalid_hex_preserves_mirror": {
			metadata: strings.Repeat("z", 128),
			suffix:   ".sha512",
			mirror:   true,
			wantCode: errs.CodeDownloadFailed,
		},
		"wrong_digest_size_preserves_mirror": {
			metadata: sha256Hex,
			suffix:   ".sha512",
			mirror:   true,
			wantCode: errs.CodeDownloadFailed,
		},
		"sha512_cannot_flow_as_sha256": {
			metadata: sha512Hex + " image.vhd",
			suffix:   ".sha256",
			mirror:   true,
			wantCode: errs.CodeDownloadFailed,
		},
		"unmatched_metadata_preserves_mirror": {
			metadata: sha512Hex + " other.vhd",
			suffix:   ".sha512",
			mirror:   true,
			wantCode: errs.CodeDownloadFailed,
		},
		"bare_sha512_rejects_altered_http": {
			metadata:      sha512Hex,
			suffix:        ".sha512",
			alterArtifact: true,
			wantCode:      errs.CodeImageChecksumMismatch,
			wantGets:      1,
		},
		"sha256_rejects_altered_http": {
			metadata:      sha256Hex + " image.vhd",
			suffix:        ".sha256",
			alterArtifact: true,
			wantCode:      errs.CodeImageChecksumMismatch,
			wantGets:      1,
		},
		"bare_sha512_http":   {metadata: sha512Hex, suffix: ".sha512", wantGets: 1},
		"named_sha512_http":  {metadata: sha512Hex + " *image.vhd", suffix: ".sha512", wantGets: 1},
		"bare_sha512_mirror": {metadata: sha512Hex, suffix: ".sha512?metadata=1", mirror: true},
		"sha256_manifest_http": {
			metadata: sha256Hex + " other.vhd\n" + sha256Hex + " image.vhd",
			suffix:   ".sha256",
			wantGets: 1,
		},
		"sha256_mirror":                 {metadata: sha256Hex, suffix: ".sha256", mirror: true},
		"intentionally_absent_metadata": {noMetadata: true, wantGets: 1},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cacheDir := setupHermeticCache(t)
			mirrorDir := filepath.Join(cacheDir, "mirror")
			require.NoError(t, os.Mkdir(mirrorDir, 0700))
			t.Setenv(infra.EnvKey("ASSET_MIRROR"), mirrorDir)
			mirrorPath := filepath.Join(mirrorDir, "image.vhd")
			if tc.mirror {
				require.NoError(t, os.WriteFile(mirrorPath, data, 0400))
			}
			var artifactGets atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/image.vhd" {
					artifactGets.Add(1)
					artifact := append([]byte(nil), data...)
					if tc.alterArtifact {
						artifact[0] = 1
					}
					_, _ = w.Write(artifact)
					return
				}
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
				_, _ = w.Write([]byte(tc.metadata))
			}))
			defer server.Close()
			spec := &model.ImageSpec{Source: server.URL + "/image.vhd", Format: "vhd"}
			if !tc.noMetadata {
				spec.SHA256URL = server.URL + "/image.vhd" + tc.suffix
			}
			outputDir := filepath.Join(cacheDir, "output")
			require.NoError(t, os.Mkdir(outputDir, 0700))
			dest := filepath.Join(outputDir, "test-image.download")
			before := []byte("retain existing download on metadata failure")
			if tc.wantCode == errs.CodeDownloadFailed {
				require.NoError(t, os.WriteFile(dest, before, 0600))
			}
			path, err := NewService(
				fakeRepo{},
			).DownloadImage(context.Background(), spec, "test-image", outputDir, true, "", nil)
			assert.Equal(t, tc.wantGets, artifactGets.Load())
			if tc.mirror {
				got, readErr := os.ReadFile(mirrorPath)
				require.NoError(t, readErr)
				if diff := cmp.Diff(data, got); diff != "" {
					t.Errorf("mirror content changed (-want +got):\n%s", diff)
				}
			}
			if tc.wantCode != "" {
				require.Error(t, err)
				var domainErr *errs.DomainError
				require.ErrorAs(t, err, &domainErr)
				assert.Equal(t, tc.wantCode, domainErr.Code)
				if tc.wantCode == errs.CodeDownloadFailed {
					got, readErr := os.ReadFile(dest)
					require.NoError(t, readErr)
					if diff := cmp.Diff(before, got); diff != "" {
						t.Errorf("existing download changed (-want +got):\n%s", diff)
					}
				} else {
					_, statErr := os.Stat(dest)
					assert.True(t, errors.Is(statErr, os.ErrNotExist), "mismatched artifact must be removed")
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, dest, path)
			got, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			if diff := cmp.Diff(data, got); diff != "" {
				t.Errorf("downloaded image content (-want +got):\n%s", diff)
			}
		})
	}
}
