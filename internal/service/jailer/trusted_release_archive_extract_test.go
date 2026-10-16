package jailer

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"slices"
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

func TestTrustedReleaseArchiveStageExtractsSelectedMembersFromCompleteAuditedEnvelope(t *testing.T) {
	t.Parallel()

	for _, version := range []string{"1.16.1", "1.16.2", "1.17.0"} {
		t.Run(version, func(t *testing.T) {
			t.Parallel()
			fixture := newTrustedReleaseArchiveFixtureForVersion(t, version)
			slices.Reverse(fixture.members)
			compressed := fixture.compressed(t)
			stage := readyTrustedReleaseArchiveStageForTest(t, compressed)
			selected := &trustedReleaseArchiveCapture{}

			err := stage.extract(t.Context(), fixture.policy, selected)
			require.NoError(t, err)
			if diff := cmp.Diff(fixture.firecrackerBytes, selected.firecracker); diff != "" {
				t.Errorf("extracted Firecracker bytes mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(fixture.jailerBytes, selected.jailer); diff != "" {
				t.Errorf("extracted Jailer bytes mismatch (-want +got):\n%s", diff)
			}
			assert.Equal(t, trustedReleaseArchiveStageExtracted, stage.state)
			assert.Equal(t, uint64(len(compressed)), stage.sizeBytes)
			if diff := cmp.Diff(trustedReleaseArchiveDigestForTest(compressed), stage.archiveDigest); diff != "" {
				t.Errorf("trusted release archive digest mismatch (-want +got):\n%s", diff)
			}
			offset, err := unix.Seek(stage.fd, 0, unix.SEEK_CUR)
			require.NoError(t, err)
			assert.Zero(t, offset)

			err = stage.extract(t.Context(), fixture.policy, &trustedReleaseArchiveCapture{})
			require.Error(t, err)
			assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
		})
	}
}

// Rationale: The release identity selects the exact raw tar encoding. A nearby release's
// valid envelope and alternate encodings of numeric zero must not gain archive authority.
func TestTrustedReleaseArchiveStageRejectsReleaseTarProfileMismatch(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		version string
		mutate  func(*testing.T, *trustedReleaseArchiveFixture)
	}{
		"older release with plain GNU": {
			version: "1.16.1",
			mutate:  func(_ *testing.T, fixture *trustedReleaseArchiveFixture) { fixture.plainGNU = true },
		},
		"current release with local PAX": {
			version: "1.16.2",
			mutate:  func(_ *testing.T, fixture *trustedReleaseArchiveFixture) { fixture.plainGNU = false },
		},
		"1.17.0 with local PAX": {
			version: "1.17.0",
			mutate:  func(_ *testing.T, fixture *trustedReleaseArchiveFixture) { fixture.plainGNU = false },
		},
		"1.17.0 with older magic": {
			version: "1.17.0",
			mutate: func(_ *testing.T, fixture *trustedReleaseArchiveFixture) {
				fixture.members[0].mutateHeader = func(raw []byte) {
					copy(raw[257:265], []byte{'u', 's', 't', 'a', 'r', 0, '0', '0'})
				}
			},
		},
		"current release with older magic": {
			version: "1.16.2",
			mutate: func(_ *testing.T, fixture *trustedReleaseArchiveFixture) {
				fixture.members[0].mutateHeader = func(raw []byte) {
					copy(raw[257:265], []byte{'u', 's', 't', 'a', 'r', 0, '0', '0'})
				}
			},
		},
		"current release with NUL device major": {
			version: "1.16.2",
			mutate: func(_ *testing.T, fixture *trustedReleaseArchiveFixture) {
				fixture.members[0].mutateHeader = func(raw []byte) { clear(raw[329:337]) }
			},
		},
		"current release with NUL device minor": {
			version: "1.16.2",
			mutate: func(_ *testing.T, fixture *trustedReleaseArchiveFixture) {
				fixture.members[0].mutateHeader = func(raw []byte) { clear(raw[337:345]) }
			},
		},
		"current release with nonzero device major": {
			version: "1.16.2",
			mutate:  func(_ *testing.T, fixture *trustedReleaseArchiveFixture) { fixture.members[0].deviceMajor = 1 },
		},
		"current release with nonzero device minor": {
			version: "1.16.2",
			mutate:  func(_ *testing.T, fixture *trustedReleaseArchiveFixture) { fixture.members[0].deviceMinor = 1 },
		},
		"current release with GNU extension": {
			version: "1.16.2",
			mutate: func(_ *testing.T, fixture *trustedReleaseArchiveFixture) {
				fixture.members[0].mutateHeader = func(raw []byte) { raw[345] = 1 }
			},
		},
		"older release with octal zero device": {
			version: "1.16.1",
			mutate: func(t *testing.T, fixture *trustedReleaseArchiveFixture) {
				fixture.members[0].mutateHeader = func(raw []byte) {
					writeTrustedReleaseTarOctalForTest(t, raw[329:337], 0)
				}
			},
		},
		"current release with negative base-256 UID": {
			version: "1.16.2",
			mutate: func(_ *testing.T, fixture *trustedReleaseArchiveFixture) {
				fixture.members[0].mutateHeader = func(raw []byte) { raw[108] = 0xff }
			},
		},
		"current release with overflowing base-256 UID": {
			version: "1.16.2",
			mutate: func(_ *testing.T, fixture *trustedReleaseArchiveFixture) {
				fixture.members[0].mutateHeader = func(raw []byte) { raw[111] = 1 }
			},
		},
		"current release with octal-representable base-256 UID": {
			version: "1.16.2",
			mutate: func(_ *testing.T, fixture *trustedReleaseArchiveFixture) {
				fixture.members[0].mutateHeader = func(raw []byte) {
					copy(raw[108:116], []byte{0x80, 0, 0, 0, 0, 0x1f, 0xff, 0xff})
				}
			},
		},
		"older release with base-256 UID": {
			version: "1.16.1",
			mutate: func(_ *testing.T, fixture *trustedReleaseArchiveFixture) {
				fixture.members[0].mutateHeader = func(raw []byte) {
					copy(raw[108:116], []byte{0x80, 0, 0, 0, 0, 0xd7, 0xa8, 0xb0})
				}
			},
		},
		"current release with base-256 size": {
			version: "1.16.2",
			mutate: func(_ *testing.T, fixture *trustedReleaseArchiveFixture) {
				fixture.members[0].mutateHeader = func(raw []byte) {
					copy(raw[124:136], []byte{0x80, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1})
				}
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newTrustedReleaseArchiveFixtureForVersion(t, tc.version)
			tc.mutate(t, fixture)
			stage := readyTrustedReleaseArchiveStageForTest(t, fixture.compressed(t))
			selected := &trustedReleaseArchiveCapture{}
			err := stage.extract(t.Context(), fixture.policy, selected)
			require.Error(t, err)
			assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
			assert.Equal(t, trustedReleaseArchiveStageFailed, stage.state)
			assert.Empty(t, selected.firecracker)
			assert.Empty(t, selected.jailer)
		})
	}
}

func TestTrustedReleaseArchiveStageAcceptsExactSizeBoundaries(t *testing.T) {
	tests := map[string]func(*testing.T, *trustedReleaseArchiveFixture){
		"8 MiB member": func(_ *testing.T, fixture *trustedReleaseArchiveFixture) {
			fixture.members[0].data = make([]byte, trustedReleaseArchiveMaxMemberBytes)
		},
		"32 MiB decompressed stream": func(t *testing.T, fixture *trustedReleaseArchiveFixture) {
			base := fixture.decompressed(t)
			require.Less(t, uint64(len(base)), trustedReleaseArchiveMaxDecompressedBytes)
			fixture.trailingZeroBytes = int(trustedReleaseArchiveMaxDecompressedBytes) - len(base)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newTrustedReleaseArchiveFixture(t)
			mutate(t, fixture)
			stage := readyTrustedReleaseArchiveStageForTest(t, fixture.compressed(t))

			err := stage.extract(t.Context(), fixture.policy, &trustedReleaseArchiveCapture{})
			require.NoError(t, err)
			assert.Equal(t, trustedReleaseArchiveStageExtracted, stage.state)
		})
	}
}

func TestTrustedReleaseArchiveStageRejectsExpandedOrMalformedTarEnvelope(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*trustedReleaseArchiveFixture){
		"unexpected path": func(fixture *trustedReleaseArchiveFixture) {
			fixture.members[0].name = "../../etc/passwd"
		},
		"duplicate member": func(fixture *trustedReleaseArchiveFixture) {
			fixture.members[1].name = fixture.members[0].name
		},
		"missing member": func(fixture *trustedReleaseArchiveFixture) {
			fixture.members = fixture.members[:len(fixture.members)-1]
		},
		"extra member": func(fixture *trustedReleaseArchiveFixture) {
			extra := fixture.members[0]
			extra.name = fixture.policy.source.archiveRoot + "/extra"
			fixture.members = append(fixture.members, extra)
		},
		"symbolic link": func(fixture *trustedReleaseArchiveFixture) {
			fixture.members[0].typeFlag = '2'
			fixture.members[0].linkName = "target"
		},
		"hard link": func(fixture *trustedReleaseArchiveFixture) {
			fixture.members[0].typeFlag = '1'
			fixture.members[0].linkName = fixture.members[1].name
		},
		"character device": func(fixture *trustedReleaseArchiveFixture) {
			fixture.members[0].typeFlag = '3'
			fixture.members[0].deviceMajor = 1
		},
		"GNU sparse member": func(fixture *trustedReleaseArchiveFixture) {
			fixture.members[0].typeFlag = 'S'
		},
		"wrong member mode": func(fixture *trustedReleaseArchiveFixture) {
			fixture.members[0].mode = 0777
		},
		"oversized member": func(fixture *trustedReleaseArchiveFixture) {
			fixture.members[0].declaredSize = trustedReleaseArchiveMaxMemberBytes + 1
		},
		"unexpected PAX key": func(fixture *trustedReleaseArchiveFixture) {
			fixture.members[0].paxRecords = append(
				fixture.members[0].paxRecords,
				trustedReleasePAXRecordForTest{key: "path", value: "alternate"},
			)
		},
		"missing PAX mtime": func(fixture *trustedReleaseArchiveFixture) {
			fixture.members[0].paxRecords = []trustedReleasePAXRecordForTest{{key: "uid", value: "1"}}
		},
		"alternate PAX header name": func(fixture *trustedReleaseArchiveFixture) {
			fixture.members[0].paxName = "PaxHeader"
		},
		"global PAX header": func(fixture *trustedReleaseArchiveFixture) {
			fixture.members[0].paxTypeFlag = 'g'
		},
		"invalid member checksum": func(fixture *trustedReleaseArchiveFixture) {
			fixture.members[0].corruptHeaderChecksum = true
		},
		"nonzero member padding": func(fixture *trustedReleaseArchiveFixture) {
			fixture.members[0].paddingByte = 1
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseArchiveFixture(t)
			mutate(fixture)
			stage := readyTrustedReleaseArchiveStageForTest(t, fixture.compressed(t))
			err := stage.extract(t.Context(), fixture.policy, &trustedReleaseArchiveCapture{})
			require.Error(t, err)
			assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
			assert.Equal(t, trustedReleaseArchiveStageFailed, stage.state)
		})
	}
}

func TestTrustedReleaseArchiveStageRejectsInvalidTerminationOrGzipFraming(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*testing.T, *trustedReleaseArchiveFixture) []byte{
		"missing second tar end block": func(t *testing.T, fixture *trustedReleaseArchiveFixture) []byte {
			fixture.endBlocks = 1
			return fixture.compressed(t)
		},
		"nonzero second tar end block": func(t *testing.T, fixture *trustedReleaseArchiveFixture) []byte {
			fixture.endBlocks = 1
			fixture.trailingDecompressed = append([]byte{1}, make([]byte, trustedReleaseTarBlockBytes-1)...)
			return fixture.compressed(t)
		},
		"nonzero decompressed tail": func(t *testing.T, fixture *trustedReleaseArchiveFixture) []byte {
			fixture.trailingDecompressed = []byte{1}
			return fixture.compressed(t)
		},
		"decompressed overflow": func(t *testing.T, fixture *trustedReleaseArchiveFixture) []byte {
			fixture.trailingZeroBytes = int(trustedReleaseArchiveMaxDecompressedBytes)
			return fixture.compressed(t)
		},
		"concatenated gzip member": func(t *testing.T, fixture *trustedReleaseArchiveFixture) []byte {
			return append(fixture.compressed(t), gzipTrustedReleaseArchiveBytesForTest(t, []byte("second"))...)
		},
		"compressed trailing byte": func(t *testing.T, fixture *trustedReleaseArchiveFixture) []byte {
			return append(fixture.compressed(t), 1)
		},
		"corrupt gzip checksum": func(t *testing.T, fixture *trustedReleaseArchiveFixture) []byte {
			compressed := fixture.compressed(t)
			compressed[len(compressed)-8] ^= 1
			return compressed
		},
		"truncated gzip trailer": func(t *testing.T, fixture *trustedReleaseArchiveFixture) []byte {
			compressed := fixture.compressed(t)
			return compressed[:len(compressed)-4]
		},
	}
	for name, build := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseArchiveFixture(t)
			stage := readyTrustedReleaseArchiveStageForTest(t, build(t, fixture))
			err := stage.extract(t.Context(), fixture.policy, &trustedReleaseArchiveCapture{})
			require.Error(t, err)
			assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
			assert.Equal(t, trustedReleaseArchiveStageFailed, stage.state)
		})
	}
}

func TestTrustedReleaseArchiveStagePreservesInfrastructureFailures(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseArchiveFixture(t)
	stage := readyTrustedReleaseArchiveStageForTest(t, fixture.compressed(t))
	stage.deps.pread = func(context.Context, int, []byte, int64) (int, error) {
		return 0, unix.EIO
	}

	err := stage.extract(t.Context(), fixture.policy, &trustedReleaseArchiveCapture{})
	require.Error(t, err)
	assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
	assert.ErrorIs(t, err, unix.EIO)
	assert.Equal(t, trustedReleaseArchiveStageFailed, stage.state)
}

func TestTrustedReleaseArchiveStageHonorsCancellationAfterExtractionStarts(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseArchiveFixture(t)
	stage := readyTrustedReleaseArchiveStageForTest(t, fixture.compressed(t))
	ctx, cancel := context.WithCancel(t.Context())
	realPread := stage.deps.pread
	stage.deps.pread = func(ctx context.Context, fd int, value []byte, offset int64) (int, error) {
		count, err := realPread(ctx, fd, value, offset)
		cancel()
		return count, err
	}

	err := stage.extract(ctx, fixture.policy, &trustedReleaseArchiveCapture{})
	require.Error(t, err)
	assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, trustedReleaseArchiveStageFailed, stage.state)
}

func TestTrustedReleaseArchiveStageRejectsIdentityChangeAfterParsing(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseArchiveFixture(t)
	stage := readyTrustedReleaseArchiveStageForTest(t, fixture.compressed(t))
	realFstat := stage.deps.fstat
	fstatCalls := 0
	stage.deps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
		if err := realFstat(ctx, fd, stat); err != nil {
			return err
		}
		fstatCalls++
		if fstatCalls == 2 {
			stat.Ino++
		}
		return nil
	}

	err := stage.extract(t.Context(), fixture.policy, &trustedReleaseArchiveCapture{})
	require.Error(t, err)
	assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
	assert.Equal(t, trustedReleaseArchiveStageFailed, stage.state)
}

func TestTrustedReleaseArchiveStagePreservesSelectedWriterFailure(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseArchiveFixture(t)
	stage := readyTrustedReleaseArchiveStageForTest(t, fixture.compressed(t))
	selected := &trustedReleaseArchiveCapture{writeErr: unix.ENOSPC}

	err := stage.extract(t.Context(), fixture.policy, selected)
	require.Error(t, err)
	assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
	assert.ErrorIs(t, err, unix.ENOSPC)
	assert.Equal(t, trustedReleaseArchiveStageFailed, stage.state)
}

func TestTrustedReleaseArchiveStagePreservesSelectedWriterDomainError(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseArchiveFixture(t)
	stage := readyTrustedReleaseArchiveStageForTest(t, fixture.compressed(t))
	primary := errs.New(
		errs.CodeValidationFailed,
		"selected staging rejected bytes",
		errs.WithClass(errs.ClassConflict),
		errs.WithEntity("firecracker-stage"),
		errs.WithDetails(map[string]any{"staging_retained": true}),
	)
	selected := &trustedReleaseArchiveCapture{writeErr: primary}

	err := stage.extract(t.Context(), fixture.policy, selected)
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Same(t, primary, domainErr)
	assert.Equal(t, errs.CodeValidationFailed, domainErr.Code)
	assert.Equal(t, errs.ClassConflict, domainErr.Class)
	assert.Equal(t, "firecracker-stage", domainErr.Entity)
	if diff := cmp.Diff(map[string]any{"staging_retained": true}, domainErr.Details); diff != "" {
		t.Errorf("selected writer error details mismatch (-want +got):\n%s", diff)
	}
	assert.Equal(t, trustedReleaseArchiveStageFailed, stage.state)
}

func TestTrustedReleaseArchiveStageRejectsBeforeTransitionWhenInputsAreInvalid(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*testing.T, *trustedReleaseArchiveStage, trustedReleaseArchivePolicy) error{
		"canceled context": func(
			t *testing.T,
			stage *trustedReleaseArchiveStage,
			policy trustedReleaseArchivePolicy,
		) error {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			return stage.extract(ctx, policy, &trustedReleaseArchiveCapture{})
		},
		"nil selected writer": func(
			t *testing.T,
			stage *trustedReleaseArchiveStage,
			policy trustedReleaseArchivePolicy,
		) error {
			return stage.extract(t.Context(), policy, nil)
		},
		"forged policy": func(
			t *testing.T,
			stage *trustedReleaseArchiveStage,
			policy trustedReleaseArchivePolicy,
		) error {
			delete(policy.members, policy.source.firecrackerMember)
			return stage.extract(t.Context(), policy, &trustedReleaseArchiveCapture{})
		},
		"nonzero descriptor offset": func(
			t *testing.T,
			stage *trustedReleaseArchiveStage,
			policy trustedReleaseArchivePolicy,
		) error {
			_, err := unix.Seek(stage.fd, 1, unix.SEEK_SET)
			require.NoError(t, err)
			return stage.extract(t.Context(), policy, &trustedReleaseArchiveCapture{})
		},
		"forged tar profile": func(
			t *testing.T,
			stage *trustedReleaseArchiveStage,
			policy trustedReleaseArchivePolicy,
		) error {
			policy.tarProfile = trustedReleaseArchiveTarPlainGNU
			return stage.extract(t.Context(), policy, &trustedReleaseArchiveCapture{})
		},
	}
	for name, run := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseArchiveFixture(t)
			stage := readyTrustedReleaseArchiveStageForTest(t, fixture.compressed(t))
			err := run(t, stage, fixture.policy)
			require.Error(t, err)
			assert.Equal(t, trustedReleaseArchiveStageReady, stage.state)
		})
	}
}

type trustedReleaseArchiveFixture struct {
	policy               trustedReleaseArchivePolicy
	plainGNU             bool
	members              []trustedReleaseArchiveFixtureMember
	endBlocks            int
	trailingDecompressed []byte
	trailingZeroBytes    int
	firecrackerBytes     []byte
	jailerBytes          []byte
}

type trustedReleaseArchiveFixtureMember struct {
	name                  string
	mode                  uint64
	typeFlag              byte
	linkName              string
	deviceMajor           uint64
	deviceMinor           uint64
	data                  []byte
	declaredSize          uint64
	paxName               string
	paxTypeFlag           byte
	paxRecords            []trustedReleasePAXRecordForTest
	corruptHeaderChecksum bool
	mutateHeader          func([]byte)
	paddingByte           byte
}

func newTrustedReleaseArchiveFixture(t *testing.T) *trustedReleaseArchiveFixture {
	t.Helper()
	return newTrustedReleaseArchiveFixtureForVersion(t, "1.16.1")
}

// newTrustedReleaseArchiveFixtureForVersion builds the independently audited raw encoding for each release.
func newTrustedReleaseArchiveFixtureForVersion(t *testing.T, version string) *trustedReleaseArchiveFixture {
	t.Helper()

	source, err := newTrustedReleaseSource(releaseSlot{version: version, architecture: "x86_64"})
	require.NoError(t, err)
	policy, err := newTrustedReleaseArchivePolicy(source)
	require.NoError(t, err)
	names := make([]string, 0, len(policy.members))
	for name := range policy.members {
		names = append(names, name)
	}
	sort.Strings(names)
	fixture := &trustedReleaseArchiveFixture{
		policy:           policy,
		plainGNU:         version == "1.16.2" || version == "1.17.0",
		endBlocks:        2,
		firecrackerBytes: []byte("selected-firecracker"),
		jailerBytes:      []byte("selected-jailer"),
	}
	for _, name := range names {
		memberPolicy := policy.members[name]
		data := []byte("member:" + name)
		switch memberPolicy.selected {
		case trustedReleaseArchiveFirecracker:
			data = bytes.Clone(fixture.firecrackerBytes)
		case trustedReleaseArchiveJailer:
			data = bytes.Clone(fixture.jailerBytes)
		}
		fixture.members = append(fixture.members, trustedReleaseArchiveFixtureMember{
			name:        name,
			mode:        memberPolicy.mode,
			typeFlag:    trustedReleaseTarTypeRegular,
			data:        data,
			paxName:     "././@PaxHeader",
			paxTypeFlag: trustedReleaseTarTypePAX,
			paxRecords: []trustedReleasePAXRecordForTest{
				{key: "uid", value: "29852511"},
				{key: "mtime", value: "1782726985.0"},
			},
		})
	}
	return fixture
}

func (fixture *trustedReleaseArchiveFixture) compressed(t *testing.T) []byte {
	t.Helper()
	return gzipTrustedReleaseArchiveBytesForTest(t, fixture.decompressed(t))
}

func (fixture *trustedReleaseArchiveFixture) decompressed(t *testing.T) []byte {
	t.Helper()

	var decompressed bytes.Buffer
	for _, member := range fixture.members {
		if !fixture.plainGNU {
			paxPayload := trustedReleasePAXForTest(member.paxRecords...)
			paxHeader := trustedReleaseTarHeaderForTest(t, trustedReleaseTarHeader{
				name:      member.paxName,
				sizeBytes: uint64(len(paxPayload)),
				typeFlag:  member.paxTypeFlag,
			})
			decompressed.Write(paxHeader)
			decompressed.Write(paxPayload)
			writeTrustedReleaseArchivePaddingForTest(t, &decompressed, uint64(len(paxPayload)), 0)
		}

		declaredSize := member.declaredSize
		if declaredSize == 0 {
			declaredSize = uint64(len(member.data))
		}
		memberHeader := trustedReleaseTarHeaderForTest(t, trustedReleaseTarHeader{
			name:        member.name,
			mode:        member.mode,
			gid:         100,
			sizeBytes:   declaredSize,
			modTime:     1782726985,
			typeFlag:    member.typeFlag,
			linkName:    member.linkName,
			userName:    "jaehoc",
			groupName:   "amazon",
			deviceMajor: member.deviceMajor,
			deviceMinor: member.deviceMinor,
		})
		if fixture.plainGNU {
			copy(memberHeader[257:265], []byte{'u', 's', 't', 'a', 'r', ' ', ' ', 0})
			// NOTE: Official 1.16.2 and 1.17.0 members encode metadata UID 14133424 with these bytes.
			copy(memberHeader[108:116], []byte{0x80, 0, 0, 0, 0, 0xd7, 0xa8, 0xb0})
			writeTrustedReleaseTarOctalForTest(t, memberHeader[329:337], member.deviceMajor)
			writeTrustedReleaseTarOctalForTest(t, memberHeader[337:345], member.deviceMinor)
		}
		if member.mutateHeader != nil {
			member.mutateHeader(memberHeader)
		}
		writeTrustedReleaseTarChecksumForTest(t, memberHeader)
		if member.corruptHeaderChecksum {
			memberHeader[0] ^= 1
		}
		decompressed.Write(memberHeader)
		decompressed.Write(member.data)
		writeTrustedReleaseArchivePaddingForTest(t, &decompressed, uint64(len(member.data)), member.paddingByte)
	}
	decompressed.Write(bytes.Repeat(make([]byte, 1), fixture.endBlocks*trustedReleaseTarBlockBytes))
	decompressed.Write(fixture.trailingDecompressed)
	if fixture.trailingZeroBytes > 0 {
		_, err := io.CopyN(&decompressed, zeroReaderForTest{}, int64(fixture.trailingZeroBytes))
		require.NoError(t, err)
	}
	return decompressed.Bytes()
}

func writeTrustedReleaseArchivePaddingForTest(
	t *testing.T,
	destination *bytes.Buffer,
	sizeBytes uint64,
	value byte,
) {
	t.Helper()
	padding := (trustedReleaseTarBlockBytes - int(sizeBytes%trustedReleaseTarBlockBytes)) %
		trustedReleaseTarBlockBytes
	destination.Write(bytes.Repeat([]byte{value}, padding))
}

func gzipTrustedReleaseArchiveBytesForTest(t *testing.T, decompressed []byte) []byte {
	t.Helper()

	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	writer.Name = "firecracker-v1.16.1-x86_64.tar"
	_, err := writer.Write(decompressed)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	return compressed.Bytes()
}

func readyTrustedReleaseArchiveStageForTest(t *testing.T, compressed []byte) *trustedReleaseArchiveStage {
	t.Helper()

	stage, writer := newTrustedReleaseArchiveStreamFixture(t)
	t.Cleanup(func() {
		require.NoError(t, stage.Release(context.Background()))
		require.NoError(t, writer.Release(context.Background()))
	})
	require.NoError(
		t,
		stage.receive(
			t.Context(),
			bytes.NewReader(compressed),
			uint64(len(compressed)),
			trustedReleaseArchiveDigestForTest(compressed),
		),
	)
	return stage
}

type trustedReleaseArchiveCapture struct {
	firecracker []byte
	jailer      []byte
	writeErr    error
}

func (capture *trustedReleaseArchiveCapture) writeFirecracker(
	ctx context.Context,
	value []byte,
	offset uint64,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if offset != uint64(len(capture.firecracker)) {
		return fmt.Errorf("unexpected Firecracker offset %d", offset)
	}
	if capture.writeErr != nil {
		return capture.writeErr
	}
	capture.firecracker = append(capture.firecracker, value...)
	return nil
}

func (capture *trustedReleaseArchiveCapture) writeJailer(
	ctx context.Context,
	value []byte,
	offset uint64,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if offset != uint64(len(capture.jailer)) {
		return fmt.Errorf("unexpected Jailer offset %d", offset)
	}
	if capture.writeErr != nil {
		return capture.writeErr
	}
	capture.jailer = append(capture.jailer, value...)
	return nil
}

type zeroReaderForTest struct{}

func (zeroReaderForTest) Read(value []byte) (int, error) {
	clear(value)
	return len(value), nil
}

var _ trustedReleaseArchiveSelectedWriter = (*trustedReleaseArchiveCapture)(nil)
var _ io.Reader = zeroReaderForTest{}
