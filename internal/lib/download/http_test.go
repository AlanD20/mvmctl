package download

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra"
)

// --- isRetryableError ---
// Rationale: isRetryableError determines whether a download error should trigger
// a retry. Context cancellations should never retry, while network, HTTP, and
// filesystem errors are transient and worth retrying.

func TestIsRetryableError(t *testing.T) {
	tests := map[string]struct {
		err  error
		want bool
	}{
		"nil": {
			err:  nil,
			want: false,
		},
		"plain_error": {
			err:  errors.New("something went wrong"),
			want: false,
		},
		"context_canceled": {
			err:  context.Canceled,
			want: false,
		},
		"context_deadline_exceeded": {
			err:  context.DeadlineExceeded,
			want: false,
		},
		"url_error": {
			err:  &url.Error{Op: "GET", URL: "http://example.com", Err: errors.New("connection refused")},
			want: true,
		},
		"http_error": {
			err:  HttpError{StatusCode: 500, URL: "http://example.com"},
			want: true,
		},
		"os_path_error": {
			err:  &os.PathError{Op: "open", Path: "/tmp/file", Err: errors.New("no space left")},
			want: true,
		},
		"os_link_error": {
			err:  &os.LinkError{Op: "rename", Old: "/tmp/a", New: "/tmp/b", Err: errors.New("cross-device")},
			want: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := isRetryableError(tc.err)
			assert.Equal(t, tc.want, got, "isRetryableError(%v)", tc.err)
		})
	}
}

// --- extractFilename ---
// Rationale: extractFilename parses the last path segment from a URL and strips
// query parameters. Used for determining local mirror filenames. Incorrect
// parsing would cause mirror misses or wrong cache keys.

func TestExtractFilename(t *testing.T) {
	tests := map[string]struct {
		rawURL string
		want   string
	}{
		"empty_string": {
			rawURL: "",
			want:   "",
		},
		"no_path_segments": {
			rawURL: "no-path",
			want:   "no-path",
		},
		"url_with_query_params": {
			rawURL: "file.tar.gz?param=value",
			want:   "file.tar.gz",
		},
		"absolute_path": {
			rawURL: "/absolute/path/file.txt",
			want:   "file.txt",
		},
		"https_url": {
			rawURL: "https://example.com/file.tar.gz",
			want:   "file.tar.gz",
		},
		"https_url_nested_path": {
			rawURL: "https://example.com/path/to/file",
			want:   "file",
		},
		"trailing_slash_no_filename": {
			rawURL: "https://example.com/dir/",
			want:   "",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := extractFilename(tc.rawURL)
			assert.Equal(t, tc.want, got, "extractFilename(%q)", tc.rawURL)
		})
	}
}

// --- HttpDiskCache.key ---
// Rationale: key computes a SHA256 hex digest of the URL for use as a cache
// filename. Deterministic, collision-resistant, and always non-empty.

func TestHttpDiskCacheKey(t *testing.T) {
	cache := &HttpDiskCache{}

	t.Run("non_empty_output", func(t *testing.T) {
		k := cache.key("https://example.com/file.tar.gz")
		assert.NotEmpty(t, k, "key must not be empty")
	})

	t.Run("same_url_same_key", func(t *testing.T) {
		k1 := cache.key("https://example.com/file.tar.gz")
		k2 := cache.key("https://example.com/file.tar.gz")
		assert.Equal(t, k1, k2, "same URL must produce same key")
	})

	t.Run("different_url_different_key", func(t *testing.T) {
		k1 := cache.key("https://example.com/a.tar.gz")
		k2 := cache.key("https://example.com/b.tar.gz")
		assert.NotEqual(t, k1, k2, "different URLs must produce different keys")
	})

	t.Run("hex_encoded_sha256_length", func(t *testing.T) {
		k := cache.key("https://example.com/file.tar.gz")
		assert.Equal(t, 64, len(k), "SHA256 hex digest must be 64 characters")
	})
}

// --- HttpDiskCache.Path ---
// Rationale: Path returns the absolute filesystem path for a cached URL.
// It joins the infra temp directory, the cache subdirectory, and the key.

func TestHttpDiskCachePath(t *testing.T) {
	cache := &HttpDiskCache{}
	url := "https://example.com/file.tar.gz"
	got := cache.Path(url)

	require.NotEmpty(t, got, "Path must not be empty")

	expectedKey := cache.key(url)

	assert.True(t, strings.HasPrefix(got, infra.GetTempDir()), "Path must start with temp dir")
	assert.True(t, strings.HasSuffix(got, expectedKey), "Path must end with cache key")
}
