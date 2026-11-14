package download

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
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
	"mvmctl/pkg/errs"
)

func TestDownloadFileChecksum(t *testing.T) {
	data := []byte("verified artifact bytes\n")
	for _, algorithm := range []checksumAlgorithm{checksumSHA256, checksumSHA512} {
		name := "sha256"
		digest := fmt.Sprintf("%x", sha256.Sum256(data))
		if algorithm == checksumSHA512 {
			name = "sha512"
			digest = fmt.Sprintf("%x", sha512.Sum512(data))
		}
		t.Run(name, func(t *testing.T) {
			for _, scenario := range []string{"http", "mirror", "corrupt_mirror_fallback"} {
				t.Run(scenario, func(t *testing.T) {
					root := t.TempDir()
					mirror := filepath.Join(root, "mirror")
					require.NoError(t, os.Mkdir(mirror, 0700))
					t.Setenv(infra.EnvKey("ASSET_MIRROR"), mirror)
					mirrorPath := filepath.Join(mirror, "artifact.img")
					if scenario == "mirror" || scenario == "corrupt_mirror_fallback" {
						mirrorData := data
						if scenario == "corrupt_mirror_fallback" {
							mirrorData = []byte("stale owned mirror fixture")
						}
						require.NoError(t, os.WriteFile(mirrorPath, mirrorData, 0600))
					}
					var gets atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						gets.Add(1)
						_, _ = w.Write(data)
					}))
					defer server.Close()
					d := New()
					d.retries = 1
					d.delay = 0
					dest := filepath.Join(root, "output", "artifact.img")
					var err error
					if algorithm == checksumSHA512 {
						err = d.DownloadFileSHA512(context.Background(), server.URL+"/artifact.img", dest,
							strings.ToUpper(digest), false, false, nil)
					} else {
						err = d.DownloadFile(context.Background(), server.URL+"/artifact.img", dest,
							strings.ToUpper(digest), false, false, nil)
					}
					require.NoError(t, err)
					wantGets := int32(1)
					if scenario == "mirror" {
						wantGets = 0
					}
					assert.Equal(t, wantGets, gets.Load())
					for _, path := range []string{dest, mirrorPath} {
						got, readErr := os.ReadFile(path)
						require.NoError(t, readErr)
						if diff := cmp.Diff(data, got); diff != "" {
							t.Errorf("%s content mismatch (-want +got):\n%s", path, diff)
						}
					}
				})
			}
		})
	}
}

func TestDownloadFileMalformedChecksumHasNoEffects(t *testing.T) {
	// Rationale: A wrong algorithm or malformed token must be rejected before
	// mirror copying, stale-mirror removal, destination creation, or artifact HTTP.
	tests := map[string]struct {
		algorithm checksumAlgorithm
		digest    string
	}{
		"sha512_passed_as_sha256": {algorithm: checksumSHA256, digest: strings.Repeat("a", 128)},
		"sha256_passed_as_sha512": {algorithm: checksumSHA512, digest: strings.Repeat("a", 64)},
		"invalid_sha256_hex":      {algorithm: checksumSHA256, digest: strings.Repeat("z", 64)},
		"invalid_sha512_hex":      {algorithm: checksumSHA512, digest: strings.Repeat("z", 128)},
		"truncated_sha512":        {algorithm: checksumSHA512, digest: strings.Repeat("a", 127)},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv(infra.EnvKey("ASSET_MIRROR"), root)
			mirrorPath := filepath.Join(root, "artifact.img")
			data := []byte("original mirror bytes")
			require.NoError(t, os.WriteFile(mirrorPath, data, 0400))
			before, err := os.Stat(mirrorPath)
			require.NoError(t, err)
			var gets atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				gets.Add(1)
				_, _ = w.Write(data)
			}))
			defer server.Close()
			destDir := filepath.Join(root, "not-created")
			dest := filepath.Join(destDir, "artifact.img")
			d := New()
			if tc.algorithm == checksumSHA512 {
				err = d.DownloadFileSHA512(context.Background(), server.URL+"/artifact.img", dest,
					tc.digest, true, true, nil)
			} else {
				err = d.DownloadFile(context.Background(), server.URL+"/artifact.img", dest,
					tc.digest, true, true, nil)
			}
			require.Error(t, err)
			var domainErr *errs.DomainError
			require.ErrorAs(t, err, &domainErr)
			assert.Equal(t, errs.CodeDownloadFailed, domainErr.Code)
			assert.Zero(t, gets.Load())
			_, statErr := os.Stat(destDir)
			assert.ErrorIs(t, statErr, os.ErrNotExist)
			after, statErr := os.Stat(mirrorPath)
			require.NoError(t, statErr)
			assert.True(t, os.SameFile(before, after))
			assert.Equal(t, before.Mode(), after.Mode())
			got, readErr := os.ReadFile(mirrorPath)
			require.NoError(t, readErr)
			if diff := cmp.Diff(data, got); diff != "" {
				t.Errorf("mirror content changed (-want +got):\n%s", diff)
			}
		})
	}
}
