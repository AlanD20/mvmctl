package jailer

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
)

func TestTrustedReleaseArchivePolicyMatchesAuditedX8664Layouts(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		version      string
		cpuTemplates []string
	}{
		"legacy lower-case templates": {
			version:      "1.10.1",
			cpuTemplates: []string{"c3", "t2a", "t2", "t2s", "v1n1", "t2cl"},
		},
		"current upper-case templates": {
			version:      "1.16.1",
			cpuTemplates: []string{"C3", "T2A", "T2", "T2S", "V1N1", "T2CL"},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			source, err := newTrustedReleaseSource(releaseSlot{version: tc.version, architecture: "x86_64"})
			require.NoError(t, err)
			policy, err := newTrustedReleaseArchivePolicy(source)
			require.NoError(t, err)

			want := expectedTrustedReleaseArchiveMembers(tc.version, tc.cpuTemplates)
			if diff := cmp.Diff(
				want,
				policy.members,
				cmp.AllowUnexported(trustedReleaseArchiveMemberPolicy{}),
			); diff != "" {
				t.Errorf("trusted release archive members mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestTrustedReleaseArchivePolicyAcceptsOnlyExactAuditedVersions(t *testing.T) {
	t.Parallel()

	for _, version := range []string{
		"1.10.1",
		"1.14.2",
		"1.14.3",
		"1.14.4",
		"1.15.0",
		"1.15.1",
		"1.16.0",
		"1.16.1",
	} {
		t.Run(version, func(t *testing.T) {
			t.Parallel()

			source, err := newTrustedReleaseSource(releaseSlot{version: version, architecture: "x86_64"})
			require.NoError(t, err)
			policy, err := newTrustedReleaseArchivePolicy(source)
			require.NoError(t, err)
			assert.Len(t, policy.members, 24)
		})
	}
}

func TestTrustedReleaseArchivePolicyRejectsUnauditedLayouts(t *testing.T) {
	t.Parallel()

	tests := map[string]releaseSlot{
		"older x86_64 version": {version: "1.10.0", architecture: "x86_64"},
		"gap x86_64 version":   {version: "1.14.1", architecture: "x86_64"},
		"newer x86_64 version": {version: "1.16.2", architecture: "x86_64"},
		"aarch64 archive":      {version: "1.16.1", architecture: "aarch64"},
		"prerelease archive":   {version: "1.16.1-dev", architecture: "x86_64"},
	}
	for name, slot := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			source, err := newTrustedReleaseSource(slot)
			require.NoError(t, err)
			policy, err := newTrustedReleaseArchivePolicy(source)
			assert.Empty(t, policy.members)
			require.Error(t, err)
			assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
		})
	}
}

func TestTrustedReleaseArchivePolicyRejectsForgedSourceIdentity(t *testing.T) {
	t.Parallel()

	source, err := newTrustedReleaseSource(releaseSlot{version: "1.16.1", architecture: "x86_64"})
	require.NoError(t, err)
	source.archiveRoot = "release-v1.16.0-x86_64"

	policy, err := newTrustedReleaseArchivePolicy(source)
	assert.Empty(t, policy.members)
	require.Error(t, err)
	assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
}

func TestTrustedReleaseArchivePolicyFreezesBounds(t *testing.T) {
	t.Parallel()

	assert.Equal(t, uint64(32*1024*1024), trustedReleaseArchiveMaxDecompressedBytes)
	assert.Equal(t, uint64(8*1024*1024), trustedReleaseArchiveMaxMemberBytes)
	assert.Equal(t, 24, trustedReleaseArchiveMemberCount)
	assert.Equal(t, 128, trustedReleaseArchiveMaxPAXBytes)
}

func expectedTrustedReleaseArchiveMembers(
	version string,
	cpuTemplates []string,
) map[string]trustedReleaseArchiveMemberPolicy {
	root := "release-v" + version + "-x86_64/"
	binarySuffix := "-v" + version + "-x86_64"
	members := map[string]trustedReleaseArchiveMemberPolicy{
		root + "SHA256SUMS":  {mode: 0644},
		root + "LICENSE":     {mode: 0644},
		root + "THIRD-PARTY": {mode: 0644},
		root + "NOTICE":      {mode: 0644},
		root + "firecracker_spec-v" + version + ".yaml":      {mode: 0644},
		root + "seccomp-filter-v" + version + "-x86_64.json": {mode: 0644},
		root + "firecracker" + binarySuffix: {
			mode:     0755,
			selected: trustedReleaseArchiveFirecracker,
		},
		root + "firecracker" + binarySuffix + ".debug":         {mode: 0644},
		root + "jailer" + binarySuffix:                         {mode: 0755, selected: trustedReleaseArchiveJailer},
		root + "jailer" + binarySuffix + ".debug":              {mode: 0644},
		root + "cpu-template-helper" + binarySuffix:            {mode: 0755},
		root + "cpu-template-helper" + binarySuffix + ".debug": {mode: 0644},
		root + "rebase-snap" + binarySuffix:                    {mode: 0755},
		root + "rebase-snap" + binarySuffix + ".debug":         {mode: 0644},
		root + "seccompiler-bin" + binarySuffix:                {mode: 0755},
		root + "seccompiler-bin" + binarySuffix + ".debug":     {mode: 0644},
		root + "snapshot-editor" + binarySuffix:                {mode: 0755},
		root + "snapshot-editor" + binarySuffix + ".debug":     {mode: 0644},
	}
	for _, template := range cpuTemplates {
		members[root+template+"-v"+version+".json"] = trustedReleaseArchiveMemberPolicy{mode: 0644}
	}
	return members
}
