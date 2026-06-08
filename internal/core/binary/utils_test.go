package binary

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/download"
	"mvmctl/pkg/errs"
)

func TestNormalizeVersion(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    string
	}{
		{"strips_v", "v1.15.0", "1.15.0"},
		{"no_v", "1.15.0", "1.15.0"},
		{"empty", "", ""},
		{"only_v", "v", ""},
		{"v_in_middle", "1v.0.0", "1v.0.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeVersion(tt.version)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCIVersion(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    string
	}{
		{"full_semver", "1.15.0", "v1.15"},
		{"two_parts", "2.0", "v2.0"},
		{"single_part", "42", "v42"},
		{"with_v", "v1.15.0", "vv1.15"},
		{"empty", "", "v"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CIVersion(tt.version)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestMapGitHubAPIError(t *testing.T) {
	t.Run("http_403", func(t *testing.T) {
		err := download.HttpError{StatusCode: 403}
		got := mapGitHubAPIError(err)
		require.Error(t, got)
		var de *errs.DomainError
		if errors.As(got, &de) {
			assert.Equal(t, errs.CodeDownloadFailed, de.Code)
			assert.Contains(t, de.Message, "rate limit exceeded")
		}
	})

	t.Run("http_401", func(t *testing.T) {
		err := download.HttpError{StatusCode: 401}
		got := mapGitHubAPIError(err)
		require.Error(t, got)
		var de *errs.DomainError
		if errors.As(got, &de) {
			assert.Equal(t, errs.CodeDownloadFailed, de.Code)
			assert.Contains(t, de.Message, "authentication failed")
		}
	})

	t.Run("generic_error", func(t *testing.T) {
		err := errors.New("connection refused")
		got := mapGitHubAPIError(err)
		require.Error(t, got)
		var de *errs.DomainError
		if errors.As(got, &de) {
			assert.Equal(t, errs.CodeDownloadFailed, de.Code)
			assert.Contains(t, de.Message, "connection refused")
		}
	})

	t.Run("other_http_error", func(t *testing.T) {
		err := download.HttpError{StatusCode: 500}
		got := mapGitHubAPIError(err)
		require.Error(t, got)
		var de *errs.DomainError
		if errors.As(got, &de) {
			assert.Equal(t, errs.CodeDownloadFailed, de.Code)
			assert.Contains(t, de.Message, "500")
		}
	})
}

func TestRustTargetTriple(t *testing.T) {
	got := rustTargetTriple()
	// Should return something reasonable for the current arch
	assert.Contains(t, got, "-unknown-linux-musl")
}
