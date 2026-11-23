package jailer

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
)

func TestNewTrustedReleaseSourceDerivesOfficialIdentity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		slot releaseSlot
		want trustedReleaseSource
	}{
		{
			name: "x86_64 patch release",
			slot: releaseSlot{version: "1.16.1", architecture: "x86_64"},
			want: trustedReleaseSource{
				slot:              releaseSlot{version: "1.16.1", architecture: "x86_64"},
				archiveName:       "firecracker-v1.16.1-x86_64.tgz",
				archiveURL:        "https://github.com/firecracker-microvm/firecracker/releases/download/v1.16.1/firecracker-v1.16.1-x86_64.tgz",
				checksumURL:       "https://github.com/firecracker-microvm/firecracker/releases/download/v1.16.1/firecracker-v1.16.1-x86_64.tgz.sha256.txt",
				archiveRoot:       "release-v1.16.1-x86_64",
				firecrackerMember: "release-v1.16.1-x86_64/firecracker-v1.16.1-x86_64",
				jailerMember:      "release-v1.16.1-x86_64/jailer-v1.16.1-x86_64",
			},
		},
		{
			name: "aarch64 prerelease",
			slot: releaseSlot{version: "0.22.0-beta5", architecture: "aarch64"},
			want: trustedReleaseSource{
				slot:              releaseSlot{version: "0.22.0-beta5", architecture: "aarch64"},
				archiveName:       "firecracker-v0.22.0-beta5-aarch64.tgz",
				archiveURL:        "https://github.com/firecracker-microvm/firecracker/releases/download/v0.22.0-beta5/firecracker-v0.22.0-beta5-aarch64.tgz",
				checksumURL:       "https://github.com/firecracker-microvm/firecracker/releases/download/v0.22.0-beta5/firecracker-v0.22.0-beta5-aarch64.tgz.sha256.txt",
				archiveRoot:       "release-v0.22.0-beta5-aarch64",
				firecrackerMember: "release-v0.22.0-beta5-aarch64/firecracker-v0.22.0-beta5-aarch64",
				jailerMember:      "release-v0.22.0-beta5-aarch64/jailer-v0.22.0-beta5-aarch64",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := newTrustedReleaseSource(tc.slot)
			require.NoError(t, err)
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(trustedReleaseSource{}, releaseSlot{})); diff != "" {
				t.Errorf("newTrustedReleaseSource() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestNewTrustedReleaseSourceRejectsInvalidSlot(t *testing.T) {
	t.Parallel()

	tests := map[string]releaseSlot{
		"empty version":          {version: "", architecture: "x86_64"},
		"leading v":              {version: "v1.16.1", architecture: "x86_64"},
		"path-like version":      {version: "1.16.1/../../root", architecture: "x86_64"},
		"query-like version":     {version: "1.16.1?download=1", architecture: "x86_64"},
		"architecture alias":     {version: "1.16.1", architecture: "amd64"},
		"path-like architecture": {version: "1.16.1", architecture: "../x86_64"},
	}

	for name, slot := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := newTrustedReleaseSource(slot)
			require.Error(t, err)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, errs.CodeValidationFailed, domainErr.Code)
		})
	}
}

func TestTrustedReleaseStoreLeavesAreFixed(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "firecracker", trustedReleaseFirecrackerLeaf)
	assert.Equal(t, "jailer", trustedReleaseJailerLeaf)
	assert.Equal(t, "manifest.json", trustedReleaseManifestLeaf)
}
