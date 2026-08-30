package loopmount

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/system"
	"mvmctl/pkg/errs"
)

func TestProbeRootfsMountSourceBindsRawDescriptor(t *testing.T) {
	t.Parallel()

	fixture := newTestRootfsFilesystemProbeFixture(3)
	fixture.results = []*system.RunResult{{ExitCode: 0, Stdout: "ext4\n"}}
	parent := testRootfsPartitionParent(3, rootfsReadWrite)
	partitions := testRootfsProbePartitionSet(parent, nil)

	source, err := probeRootfsMountSource(t.Context(), fixture.dependencies(), FilesystemExt4, parent, partitions)
	require.NoError(t, err)
	require.NotNil(t, source)
	require.NotNil(t, source.state)

	assert.Same(t, parent.state, source.state.parent)
	assert.Same(t, partitions.state, source.state.partitions)
	assert.Equal(t, rootfsMountSourceRaw, source.state.kind)
	assert.Equal(t, 50, source.state.fd)
	if diff := cmp.Diff(
		rootfsBlockIdentity{major: 7, minor: 3},
		source.state.identity,
		cmp.AllowUnexported(rootfsBlockIdentity{}),
	); diff != "" {
		t.Errorf("selected raw identity mismatch (-want +got):\n%s", diff)
	}
	assert.Zero(t, source.state.partition)
	assert.Equal(t, FilesystemExt4, source.state.filesystem)
	if diff := cmp.Diff([]int{50, 50}, fixture.fstatFDs); diff != "" {
		t.Errorf("raw identity probes mismatch (-want +got):\n%s", diff)
	}
	require.Len(t, fixture.calls, 1)
	assertRootfsBlkidCall(t, fixture.calls[0], "/dev/loop3")
	assert.False(t, parent.state.released)
	assert.False(t, partitions.state.released)
}

func TestRootfsSelectedSourceReleaseIsSharedAndCancellationIndependent(t *testing.T) {
	t.Parallel()

	parent := testRootfsPartitionParent(3, rootfsReadOnly)
	partitions := testRootfsProbePartitionSet(parent, nil)
	source := &rootfsSelectedSource{state: &rootfsSelectedSourceState{
		parent: parent.state, partitions: partitions.state, kind: rootfsMountSourceRaw,
		fd: 50, identity: parent.state.identity, filesystem: FilesystemExt4,
	}}
	copyOfSource := *source
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	require.NoError(t, source.Release(ctx))
	require.NoError(t, copyOfSource.Release(t.Context()))
	assert.True(t, source.state.released)
	assert.Nil(t, source.state.parent)
	assert.Nil(t, source.state.partitions)
	assert.Equal(t, -1, source.state.fd)
	if diff := cmp.Diff(
		rootfsBlockIdentity{},
		source.state.identity,
		cmp.AllowUnexported(rootfsBlockIdentity{}),
	); diff != "" {
		t.Errorf("released source identity mismatch (-want +got):\n%s", diff)
	}
	assert.Zero(t, source.state.kind)
	assert.Zero(t, source.state.partition)
	assert.Empty(t, source.state.filesystem)
}

// Rationale: copied source capabilities share one revocation transition; cleanup must remain race-safe when deferred
// paths converge concurrently after cancellation or a partial inspection failure.
func TestRootfsSelectedSourceReleaseSerializesConcurrentCopies(t *testing.T) {
	t.Parallel()

	parent := testRootfsPartitionParent(3, rootfsReadOnly)
	partitions := testRootfsProbePartitionSet(parent, nil)
	source := &rootfsSelectedSource{state: &rootfsSelectedSourceState{
		parent: parent.state, partitions: partitions.state, kind: rootfsMountSourceRaw,
		fd: 50, identity: parent.state.identity, filesystem: FilesystemExt4,
	}}

	const callers = 32
	start := make(chan struct{})
	results := make(chan error, callers)
	var ready sync.WaitGroup
	ready.Add(callers)
	for range callers {
		copyOfSource := *source
		go func() {
			ready.Done()
			<-start
			results <- copyOfSource.Release(t.Context())
		}()
	}
	ready.Wait()
	close(start)
	for range callers {
		require.NoError(t, <-results)
	}
	assert.True(t, source.state.released)
	assert.Equal(t, -1, source.state.fd)
}

func TestProbeRootfsMountSourceProbesSparsePartitionsAndBindsMatch(t *testing.T) {
	t.Parallel()

	fixture := newTestRootfsFilesystemProbeFixture(9)
	fixture.identities[61] = rootfsBlockIdentity{major: 259, minor: 101}
	fixture.identities[67] = rootfsBlockIdentity{major: 259, minor: 107}
	fixture.identities[76] = rootfsBlockIdentity{major: 259, minor: 116}
	fixture.results = []*system.RunResult{
		{ExitCode: rootfsBlkidNoSignatureExitCode},
		{ExitCode: rootfsBlkidNoSignatureExitCode},
		{ExitCode: 0, Stdout: "btrfs\n"},
		{ExitCode: 0, Stdout: "ext4\n"},
	}
	parent := testRootfsPartitionParent(9, rootfsReadOnly)
	partitions := testRootfsProbePartitionSet(parent, []rootfsPartitionDevice{
		{number: 1, fd: 61, identity: fixture.identities[61]},
		{number: 7, fd: 67, identity: fixture.identities[67]},
		{number: 16, fd: 76, identity: fixture.identities[76]},
	})

	source, err := probeRootfsMountSource(t.Context(), fixture.dependencies(), FilesystemExt4, parent, partitions)
	require.NoError(t, err)
	require.NotNil(t, source)
	assert.Equal(t, rootfsMountSourcePartition, source.state.kind)
	assert.Equal(t, 76, source.state.fd)
	assert.Equal(t, uint8(16), source.state.partition)
	if diff := cmp.Diff(
		fixture.identities[76],
		source.state.identity,
		cmp.AllowUnexported(rootfsBlockIdentity{}),
	); diff != "" {
		t.Errorf("selected partition identity mismatch (-want +got):\n%s", diff)
	}
	assert.Equal(t, FilesystemExt4, source.state.filesystem)
	if diff := cmp.Diff([]int{50, 50, 61, 61, 67, 67, 76, 76}, fixture.fstatFDs); diff != "" {
		t.Errorf("partition identity probes mismatch (-want +got):\n%s", diff)
	}
	require.Len(t, fixture.calls, 4)
	for index, path := range []string{"/dev/loop9", "/dev/loop9p1", "/dev/loop9p7", "/dev/loop9p16"} {
		assertRootfsBlkidCall(t, fixture.calls[index], path)
	}
}

func TestProbeRootfsMountSourceRejectsAmbiguousLayoutAfterProbingAllDevices(t *testing.T) {
	t.Parallel()

	fixture := newTestRootfsFilesystemProbeFixture(4)
	fixture.identities[61] = rootfsBlockIdentity{major: 259, minor: 1}
	fixture.results = []*system.RunResult{
		{ExitCode: 0, Stdout: "ext4\n"},
		{ExitCode: 0, Stdout: "ext4\n"},
	}
	parent := testRootfsPartitionParent(4, rootfsReadWrite)
	partitions := testRootfsProbePartitionSet(parent, []rootfsPartitionDevice{{
		number: 1, fd: 61, identity: fixture.identities[61],
	}})

	source, err := probeRootfsMountSource(t.Context(), fixture.dependencies(), FilesystemExt4, parent, partitions)
	requireRootfsFilesystemProbeError(t, err, "ambiguous")
	assert.Nil(t, source)
	assert.Len(t, fixture.calls, 2)
	assert.False(t, parent.state.released)
	assert.False(t, partitions.state.released)
}

func TestParseRootfsFilesystemProbeResultRejectsUntrustedOutput(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		result  *system.RunResult
		want    Filesystem
		wantErr string
	}{
		"missing result": {
			wantErr: "result is missing",
		},
		"unsupported filesystem": {
			result: &system.RunResult{ExitCode: 0, Stdout: "xfs\n"}, wantErr: "unsupported",
		},
		"missing canonical newline": {
			result: &system.RunResult{ExitCode: 0, Stdout: "ext4"}, wantErr: "malformed",
		},
		"multiple tokens": {
			result: &system.RunResult{ExitCode: 0, Stdout: "ext4\nbtrfs\n"}, wantErr: "malformed",
		},
		"leading whitespace": {
			result: &system.RunResult{ExitCode: 0, Stdout: " ext4\n"}, wantErr: "malformed",
		},
		"embedded nul": {
			result: &system.RunResult{ExitCode: 0, Stdout: "ext4\x00\n"}, wantErr: "malformed",
		},
		"oversized stdout": {
			result:  &system.RunResult{ExitCode: 0, Stdout: strings.Repeat("x", maxRootfsBlkidStdoutBytes+1)},
			wantErr: "stdout exceeds",
		},
		"oversized stderr": {
			result:  &system.RunResult{ExitCode: 4, Stderr: strings.Repeat("x", maxRootfsBlkidStderrBytes+1)},
			wantErr: "stderr exceeds",
		},
		"success with stderr": {
			result:  &system.RunResult{ExitCode: 0, Stdout: "ext4\n", Stderr: "warning"},
			wantErr: "unexpected stderr",
		},
		"success with no type token is empty": {
			result: &system.RunResult{ExitCode: 0},
		},
		"no signature": {
			result: &system.RunResult{ExitCode: rootfsBlkidNoSignatureExitCode},
		},
		"no signature with stdout": {
			result:  &system.RunResult{ExitCode: rootfsBlkidNoSignatureExitCode, Stdout: "ext4\n"},
			wantErr: "no-signature result emitted output",
		},
		"no signature with stderr is command failure": {
			result:  &system.RunResult{ExitCode: rootfsBlkidNoSignatureExitCode, Stderr: "cannot open"},
			wantErr: "no-signature result emitted output",
		},
		"usage failure": {
			result: &system.RunResult{ExitCode: rootfsBlkidFailureExitCode}, wantErr: "failed with exit code 4",
		},
		"ambivalent signature": {
			result: &system.RunResult{ExitCode: rootfsBlkidAmbivalentExitCode}, wantErr: "ambivalent",
		},
		"unexpected exit": {
			result: &system.RunResult{ExitCode: 17}, wantErr: "failed with exit code 17",
		},
		"ext2":  {result: &system.RunResult{ExitCode: 0, Stdout: "ext2\n"}, want: FilesystemExt2},
		"ext3":  {result: &system.RunResult{ExitCode: 0, Stdout: "ext3\n"}, want: FilesystemExt3},
		"ext4":  {result: &system.RunResult{ExitCode: 0, Stdout: "ext4\n"}, want: FilesystemExt4},
		"btrfs": {result: &system.RunResult{ExitCode: 0, Stdout: "btrfs\n"}, want: FilesystemBtrfs},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := parseRootfsFilesystemProbeResult(test.result)
			if test.wantErr != "" {
				requireRootfsFilesystemProbeError(t, err, test.wantErr)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

func TestProbeRootfsMountSourceRejectsCommandAndIdentityFailures(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		configure func(*testRootfsFilesystemProbeFixture)
		wantErr   string
	}{
		"runner failure": {
			configure: func(fixture *testRootfsFilesystemProbeFixture) {
				fixture.runErrors = []error{errors.New("spawn failed")}
			},
			wantErr: "execute filesystem probe",
		},
		"identity changed before probe": {
			configure: func(fixture *testRootfsFilesystemProbeFixture) {
				fixture.fstatIdentities[50] = []rootfsBlockIdentity{{major: 7, minor: 99}}
			},
			wantErr: "identity changed before",
		},
		"identity changed after probe": {
			configure: func(fixture *testRootfsFilesystemProbeFixture) {
				fixture.results = []*system.RunResult{{ExitCode: 0, Stdout: "ext4\n"}}
				fixture.fstatIdentities[50] = []rootfsBlockIdentity{
					{major: 7, minor: 3},
					{major: 7, minor: 99},
				}
			},
			wantErr: "identity changed after",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newTestRootfsFilesystemProbeFixture(3)
			test.configure(fixture)
			parent := testRootfsPartitionParent(3, rootfsReadWrite)
			partitions := testRootfsProbePartitionSet(parent, nil)

			source, err := probeRootfsMountSource(
				t.Context(), fixture.dependencies(), FilesystemExt4, parent, partitions,
			)
			requireRootfsFilesystemProbeError(t, err, test.wantErr)
			assert.Nil(t, source)
			assert.False(t, parent.state.released)
			assert.False(t, partitions.state.released)
		})
	}
}

func TestProbeRootfsMountSourceRejectsPartitionIdentityRace(t *testing.T) {
	// Rationale: A retained partition descriptor must be rejected if its kernel identity changes across the external
	// filesystem probe; otherwise probe output could authorize a different block device than the selected capability.
	t.Parallel()

	fixture := newTestRootfsFilesystemProbeFixture(6)
	partitionIdentity := rootfsBlockIdentity{major: 259, minor: 3}
	fixture.identities[61] = partitionIdentity
	fixture.fstatIdentities[61] = []rootfsBlockIdentity{partitionIdentity, {major: 259, minor: 4}}
	fixture.results = []*system.RunResult{
		{ExitCode: rootfsBlkidNoSignatureExitCode},
		{ExitCode: 0, Stdout: "ext4\n"},
	}
	parent := testRootfsPartitionParent(6, rootfsReadWrite)
	partitions := testRootfsProbePartitionSet(parent, []rootfsPartitionDevice{{
		number: 1, fd: 61, identity: partitionIdentity,
	}})

	source, err := probeRootfsMountSource(t.Context(), fixture.dependencies(), FilesystemExt4, parent, partitions)
	requireRootfsFilesystemProbeError(t, err, "identity changed after")
	assert.Nil(t, source)
	assert.Len(t, fixture.calls, 2)
}

func TestProbeRootfsMountSourceHonorsCancellation(t *testing.T) {
	// Rationale: Cancellation must stop probing before any later identity observation can authorize a mount source.
	t.Parallel()

	t.Run("cancelled before probing", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		fixture := newTestRootfsFilesystemProbeFixture(3)
		parent := testRootfsPartitionParent(3, rootfsReadWrite)
		partitions := testRootfsProbePartitionSet(parent, nil)

		source, err := probeRootfsMountSource(ctx, fixture.dependencies(), FilesystemExt4, parent, partitions)
		requireRootfsFilesystemProbeError(t, err, "context ended")
		assert.ErrorIs(t, err, context.Canceled)
		assert.Nil(t, source)
		assert.Empty(t, fixture.calls)
		assert.Empty(t, fixture.fstatFDs)
	})

	t.Run("cancelled while runner returns", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		fixture := newTestRootfsFilesystemProbeFixture(3)
		fixture.cancel = cancel
		fixture.results = []*system.RunResult{{ExitCode: 0, Stdout: "ext4\n"}}
		parent := testRootfsPartitionParent(3, rootfsReadWrite)
		partitions := testRootfsProbePartitionSet(parent, nil)

		source, err := probeRootfsMountSource(ctx, fixture.dependencies(), FilesystemExt4, parent, partitions)
		requireRootfsFilesystemProbeError(t, err, "context ended")
		assert.ErrorIs(t, err, context.Canceled)
		assert.Nil(t, source)
		if diff := cmp.Diff([]int{50}, fixture.fstatFDs); diff != "" {
			t.Errorf("cancelled identity probes mismatch (-want +got):\n%s", diff)
		}
		assert.Len(t, fixture.calls, 1)
	})
}

func TestProbeRootfsMountSourceRejectsMismatchedCapabilityOwnership(t *testing.T) {
	t.Parallel()

	fixture := newTestRootfsFilesystemProbeFixture(3)
	parent := testRootfsPartitionParent(3, rootfsReadWrite)
	otherParent := testRootfsPartitionParent(3, rootfsReadWrite)
	partitions := testRootfsProbePartitionSet(otherParent, nil)

	source, err := probeRootfsMountSource(t.Context(), fixture.dependencies(), FilesystemExt4, parent, partitions)
	requireRootfsFilesystemProbeError(t, err, "does not belong")
	assert.Nil(t, source)
	assert.Empty(t, fixture.calls)
	assert.Empty(t, fixture.fstatFDs)
}

type testRootfsFilesystemProbeFixture struct {
	identities      map[int]rootfsBlockIdentity
	fstatIdentities map[int][]rootfsBlockIdentity
	fstatCounts     map[int]int
	fstatFDs        []int
	results         []*system.RunResult
	runErrors       []error
	calls           []testRootfsFilesystemProbeCall
	cancel          context.CancelFunc
}

type testRootfsFilesystemProbeCall struct {
	args []string
	opts system.RunCmdOpts
}

func newTestRootfsFilesystemProbeFixture(loopIndex int) *testRootfsFilesystemProbeFixture {
	return &testRootfsFilesystemProbeFixture{
		identities: map[int]rootfsBlockIdentity{
			50: {major: 7, minor: uint32(loopIndex)},
		},
		fstatIdentities: make(map[int][]rootfsBlockIdentity),
		fstatCounts:     make(map[int]int),
	}
}

func (fixture *testRootfsFilesystemProbeFixture) dependencies() rootfsFilesystemProbeDeps {
	return rootfsFilesystemProbeDeps{
		run: func(ctx context.Context, args []string, opts system.RunCmdOpts) (*system.RunResult, error) {
			fixture.calls = append(fixture.calls, testRootfsFilesystemProbeCall{
				args: append([]string(nil), args...), opts: opts,
			})
			if fixture.cancel != nil {
				fixture.cancel()
			}
			index := len(fixture.calls) - 1
			if index < len(fixture.runErrors) && fixture.runErrors[index] != nil {
				return nil, fixture.runErrors[index]
			}
			if index < len(fixture.results) {
				return fixture.results[index], nil
			}
			return &system.RunResult{ExitCode: rootfsBlkidNoSignatureExitCode}, nil
		},
		fstat: func(_ context.Context, fd int, stat *unix.Stat_t) error {
			fixture.fstatFDs = append(fixture.fstatFDs, fd)
			identity := fixture.identities[fd]
			if sequence := fixture.fstatIdentities[fd]; len(sequence) > 0 {
				index := fixture.fstatCounts[fd]
				if index >= len(sequence) {
					index = len(sequence) - 1
				}
				identity = sequence[index]
				fixture.fstatCounts[fd]++
			}
			*stat = unix.Stat_t{Mode: unix.S_IFBLK | 0o600, Rdev: unix.Mkdev(identity.major, identity.minor)}
			return nil
		},
	}
}

func testRootfsProbePartitionSet(
	parent *rootfsLoopDevice,
	devices []rootfsPartitionDevice,
) *rootfsPartitionSet {
	return &rootfsPartitionSet{state: &rootfsPartitionSetState{
		parent:         parent.state,
		parentIndex:    parent.state.index,
		parentIdentity: parent.state.identity,
		devices:        devices,
	}}
}

func assertRootfsBlkidCall(t *testing.T, call testRootfsFilesystemProbeCall, path string) {
	t.Helper()
	wantArgs := []string{
		rootfsBlkidPath,
		"--probe",
		"--no-part-details",
		"--usages", "filesystem",
		"--match-tag", "TYPE",
		"--output", "value",
		"--",
		path,
	}
	if diff := cmp.Diff(wantArgs, call.args); diff != "" {
		t.Errorf("blkid argv mismatch (-want +got):\n%s", diff)
	}
	wantOpts := system.RunCmdOpts{
		Check:          false,
		Capture:        true,
		Env:            map[string]string{"LANG": "C", "LC_ALL": "C"},
		ReplaceEnv:     true,
		MaxStdoutBytes: maxRootfsBlkidStdoutBytes,
		MaxStderrBytes: maxRootfsBlkidStderrBytes,
		Privileged:     false,
		Interactive:    false,
		StartOnly:      false,
	}
	if diff := cmp.Diff(wantOpts, call.opts); diff != "" {
		t.Errorf("blkid options mismatch (-want +got):\n%s", diff)
	}
}

func requireRootfsFilesystemProbeError(t *testing.T, err error, contains string) {
	t.Helper()
	require.Error(t, err)
	assert.ErrorContains(t, err, contains)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeLoopMountError, domainErr.Code)
	assert.Equal(t, errs.ClassInternal, domainErr.Class)
}
