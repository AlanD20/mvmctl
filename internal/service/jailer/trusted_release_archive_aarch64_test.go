package jailer

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

// Rationale: An architecture supported by source derivation and ELF admission is not automatically an audited archive
// layout. Every version currently audited for x86_64 must remain closed for aarch64 until real archive evidence defines
// a separate complete member and format policy.
func TestTrustedReleaseArchivePolicyRejectsEveryUnauditedAArch64Release(t *testing.T) {
	t.Parallel()

	versions := []string{
		"1.10.1",
		"1.14.2",
		"1.14.3",
		"1.14.4",
		"1.15.0",
		"1.15.1",
		"1.16.0",
		"1.16.1",
	}
	for _, version := range versions {
		t.Run(version, func(t *testing.T) {
			t.Parallel()

			source, err := newTrustedReleaseSource(releaseSlot{version: version, architecture: "aarch64"})
			require.NoError(t, err)

			policy, err := newTrustedReleaseArchivePolicy(source)
			assert.Empty(t, policy.members)
			require.Error(t, err)
			assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
		})
	}
}

// Rationale: A forged policy must not turn the independently valid aarch64 source and ELF identities into archive
// authority. Rejection must happen before the ready archive stage is read, poisoned, or allowed to write selections.
func TestTrustedReleaseArchiveStageRejectsAArch64BeforeReadingArchive(t *testing.T) {
	t.Parallel()

	source, err := newTrustedReleaseSource(releaseSlot{version: "1.16.1", architecture: "aarch64"})
	require.NoError(t, err)
	header := auditedTrustedReleaseELFHeader()
	header[18] = 183
	// CONTRACT: The synthetic header declares ten 56-byte program headers beginning at byte 64, so byte 624 is the
	// smallest complete object admitted by the independent ELF policy. This is not evidence about upstream packaging.
	require.NoError(t, validateTrustedReleaseELFHeader(header, 624, source))

	fixture := newTrustedReleaseArchiveFixture(t)
	policy := fixture.policy
	policy.source = source
	compressed := fixture.compressed(t)
	stage := readyTrustedReleaseArchiveStageForTest(t, compressed)
	preadCalls := 0
	stage.deps.pread = func(context.Context, int, []byte, int64) (int, error) {
		preadCalls++
		return 0, unix.EIO
	}
	selected := &trustedReleaseArchiveCapture{}

	err = stage.extract(t.Context(), policy, selected)
	require.Error(t, err)
	assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
	assert.Zero(t, preadCalls)
	assert.Empty(t, selected.firecracker)
	assert.Empty(t, selected.jailer)
	assert.Equal(t, trustedReleaseArchiveStageReady, stage.state)
	assert.Equal(t, uint64(len(compressed)), stage.sizeBytes)
	if diff := cmp.Diff(trustedReleaseArchiveDigestForTest(compressed), stage.archiveDigest); diff != "" {
		t.Errorf("trusted release archive digest mismatch (-want +got):\n%s", diff)
	}
	offset, seekErr := unix.Seek(stage.fd, 0, unix.SEEK_CUR)
	require.NoError(t, seekErr)
	assert.Zero(t, offset)
}
