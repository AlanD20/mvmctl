package jailer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

// Rationale: one candidate capability must bind the exact finalized pair and manifest through the installed-release
// admission seam without ever exposing a partial canonical version directory.
func TestTrustedReleaseArchitectureStagesAndReadmitsCompleteCandidate(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseCandidateFixture(t)
	wantManifest := fixture.selected.manifest
	wantRaw, err := encodeTrustedReleaseManifest(wantManifest)
	require.NoError(t, err)

	candidate, err := fixture.architecture.stageCandidate(t.Context(), fixture.selected)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, candidate.Release(context.Background())) })
	assert.Equal(t, -1, fixture.architecture.fd)
	assert.Nil(t, fixture.architecture.slotLease)
	assert.Equal(t, -1, fixture.selected.firecracker.fd)
	assert.Equal(t, -1, fixture.selected.jailer.fd)
	assert.Equal(t, trustedReleaseSelectedStagesFailed, fixture.selected.state)
	assert.NoDirExists(t, fixture.store.slotPath)

	assert.Equal(t, fixture.candidateName, candidate.name)
	assert.NotNil(t, candidate.architecture)
	assert.GreaterOrEqual(t, candidate.architecture.fd, 0)
	assert.NotNil(t, candidate.directory)
	assert.GreaterOrEqual(t, candidate.directory.slotFD, 0)
	assert.NotNil(t, candidate.admission)
	assert.NotNil(t, candidate.admission.executables)
	if diff := cmp.Diff(wantRaw, candidate.canonicalManifest); diff != "" {
		t.Errorf("trusted release candidate canonical manifest mismatch (-want +got):\n%s", diff)
	}
	manifestOptions := cmp.AllowUnexported(
		trustedReleaseManifest{},
		releaseSlot{},
		trustedReleaseExecutable{},
	)
	if diff := cmp.Diff(wantManifest, candidate.admission.manifest, manifestOptions); diff != "" {
		t.Errorf("trusted release candidate admitted manifest mismatch (-want +got):\n%s", diff)
	}

	candidatePath := filepath.Join(fixture.store.architecturePath, fixture.candidateName)
	assertExactMode(t, candidatePath, 0700)
	wantLeaves := []string{
		trustedReleaseFirecrackerLeaf,
		trustedReleaseJailerLeaf,
		trustedReleaseManifestLeaf,
	}
	if diff := cmp.Diff(wantLeaves, directoryNamesForTest(t, candidatePath)); diff != "" {
		t.Errorf("trusted release candidate leaves mismatch (-want +got):\n%s", diff)
	}
	assertExactMode(t, filepath.Join(candidatePath, trustedReleaseFirecrackerLeaf), 0755)
	assertExactMode(t, filepath.Join(candidatePath, trustedReleaseJailerLeaf), 0755)
	assertExactMode(t, filepath.Join(candidatePath, trustedReleaseManifestLeaf), 0600)
	raw, err := os.ReadFile(filepath.Join(candidatePath, trustedReleaseManifestLeaf))
	require.NoError(t, err)
	if diff := cmp.Diff(wantRaw, raw); diff != "" {
		t.Errorf("trusted release candidate manifest file mismatch (-want +got):\n%s", diff)
	}
	for _, fd := range []int{
		candidate.admission.executables.firecrackerFD,
		candidate.admission.executables.jailerFD,
	} {
		flags, flagErr := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
		require.NoError(t, flagErr)
		assert.Equal(t, unix.O_RDONLY, flags&unix.O_ACCMODE)
	}

	require.NoError(t, candidate.Release(t.Context()))
	require.NoError(t, candidate.Release(t.Context()))
	assert.NoDirExists(t, candidatePath)
	assert.NoDirExists(t, fixture.store.slotPath)
}

func TestTrustedReleaseArchitectureRejectsCandidateAuthorityBeforeNamespaceMutation(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		mutate func(*trustedReleaseCandidateFixture)
		stages func(*trustedReleaseCandidateFixture) *trustedReleaseSelectedExecutableStages
	}{
		"inactive architecture": {
			mutate: func(fixture *trustedReleaseCandidateFixture) { fixture.architecture.fd = -1 },
		},
		"released slot lease": {
			mutate: func(fixture *trustedReleaseCandidateFixture) {
				fixture.architecture.slotLease.releaseLock.fd = -1
			},
		},
		"missing stages": {
			stages: func(*trustedReleaseCandidateFixture) *trustedReleaseSelectedExecutableStages { return nil },
		},
		"non-finalized stages": {
			mutate: func(fixture *trustedReleaseCandidateFixture) {
				fixture.selected.state = trustedReleaseSelectedStagesExtracted
			},
		},
		"mismatched stage slot": {
			mutate: func(fixture *trustedReleaseCandidateFixture) {
				fixture.selected.archivePolicy.source.slot.version = "1.16.0"
			},
		},
		"mismatched store policy": {
			mutate: func(fixture *trustedReleaseCandidateFixture) { fixture.selected.policy.expectedUID++ },
		},
		"mismatched manifest size": {
			mutate: func(fixture *trustedReleaseCandidateFixture) {
				fixture.selected.manifest.firecracker.sizeBytes++
			},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseCandidateFixture(t)
			before := directoryNamesForTest(t, fixture.store.architecturePath)
			originalArchitectureFD := fixture.architecture.fd
			originalReleaseLockFD := fixture.architecture.slotLease.releaseLock.fd
			if test.mutate != nil {
				test.mutate(fixture)
			}
			nameCalled := false
			fixture.architecture.deps.candidateName = func(context.Context, releaseSlot) (string, error) {
				nameCalled = true
				return fixture.candidateName, nil
			}
			selected := fixture.selected
			if test.stages != nil {
				selected = test.stages(fixture)
			}

			candidate, err := fixture.architecture.stageCandidate(t.Context(), selected)
			fixture.architecture.fd = originalArchitectureFD
			fixture.architecture.slotLease.releaseLock.fd = originalReleaseLockFD
			require.Error(t, err)
			assert.Nil(t, candidate)
			assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
			assert.False(t, nameCalled)
			assert.GreaterOrEqual(t, fixture.selected.firecracker.fd, 0)
			assert.GreaterOrEqual(t, fixture.selected.jailer.fd, 0)
			if diff := cmp.Diff(before, directoryNamesForTest(t, fixture.store.architecturePath)); diff != "" {
				t.Errorf("rejected candidate authority changed architecture entries (-before +after):\n%s", diff)
			}
		})
	}
}

func TestTrustedReleaseArchitectureRecoversBeforeCandidateNameGeneration(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseCandidateFixture(t)
	prefix, err := trustedReleaseCandidatePrefix(fixture.store.slot)
	require.NoError(t, err)
	unsafe := filepath.Join(fixture.store.architecturePath, prefix+"bad.tmp")
	require.NoError(t, os.Mkdir(unsafe, 0700))
	nameCalled := false
	fixture.architecture.deps.candidateName = func(context.Context, releaseSlot) (string, error) {
		nameCalled = true
		return fixture.candidateName, nil
	}

	candidate, err := fixture.architecture.stageCandidate(t.Context(), fixture.selected)
	require.Error(t, err)
	assert.Nil(t, candidate)
	assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
	assert.False(t, nameCalled)
	assert.Equal(t, trustedReleaseSelectedStagesFinalized, fixture.selected.state)
	assert.DirExists(t, unsafe)
	assert.NoDirExists(t, fixture.store.slotPath)
}

func TestTrustedReleaseArchitectureRejectsCandidateNameFailuresWithoutConsumingStages(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		name     func(*trustedReleaseCandidateFixture) (string, error)
		mkdir    func(context.Context, int, string, uint32) error
		want     error
		calls    int
		wantCode errs.Code
	}{
		"random source": {
			name: func(*trustedReleaseCandidateFixture) (string, error) { return "", unix.EIO },
			want: unix.EIO, calls: 1, wantCode: errs.CodeBinaryTrustedInstallFailed,
		},
		"invalid generated name": {
			name:  func(*trustedReleaseCandidateFixture) (string, error) { return "attacker", nil },
			calls: 1, wantCode: errs.CodeBinaryUntrusted,
		},
		"exclusive collisions exhausted": {
			name: func(fixture *trustedReleaseCandidateFixture) (string, error) {
				return fixture.candidateName, nil
			},
			mkdir: func(context.Context, int, string, uint32) error { return unix.EEXIST },
			want:  unix.EEXIST, calls: trustedReleaseCandidateNameAttempts,
			wantCode: errs.CodeBinaryTrustedInstallFailed,
		},
		"directory creation": {
			name: func(fixture *trustedReleaseCandidateFixture) (string, error) {
				return fixture.candidateName, nil
			},
			mkdir: func(context.Context, int, string, uint32) error { return unix.ENOSPC },
			want:  unix.ENOSPC, calls: 1, wantCode: errs.CodeBinaryTrustedInstallFailed,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseCandidateFixture(t)
			before := directoryNamesForTest(t, fixture.store.architecturePath)
			nameCalls := 0
			fixture.architecture.deps.candidateName = func(context.Context, releaseSlot) (string, error) {
				nameCalls++
				return test.name(fixture)
			}
			if test.mkdir != nil {
				fixture.architecture.deps.mkdirAt = test.mkdir
			}

			candidate, err := fixture.architecture.stageCandidate(t.Context(), fixture.selected)
			require.Error(t, err)
			assert.Nil(t, candidate)
			assert.Equal(t, test.wantCode, errs.AsDomainError(err).Code)
			if test.want != nil {
				assert.ErrorIs(t, err, test.want)
			}
			assert.Equal(t, test.calls, nameCalls)
			assert.Equal(t, trustedReleaseSelectedStagesFinalized, fixture.selected.state)
			assert.GreaterOrEqual(t, fixture.selected.firecracker.fd, 0)
			assert.GreaterOrEqual(t, fixture.selected.jailer.fd, 0)
			if diff := cmp.Diff(before, directoryNamesForTest(t, fixture.store.architecturePath)); diff != "" {
				t.Errorf("candidate name failure changed architecture entries (-before +after):\n%s", diff)
			}
		})
	}
}

func TestTrustedReleaseArchitectureNeverAdoptsCandidateNameCollision(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseCandidateFixture(t)
	collisionPath := filepath.Join(fixture.store.architecturePath, fixture.candidateName)
	markerPath := filepath.Join(collisionPath, "attacker-marker")
	nameCalls := 0
	fixture.architecture.deps.candidateName = func(context.Context, releaseSlot) (string, error) {
		nameCalls++
		if nameCalls == 1 {
			require.NoError(t, os.Mkdir(collisionPath, 0700))
			require.NoError(t, os.WriteFile(markerPath, []byte("retained"), 0600))
		}
		return fixture.candidateName, nil
	}

	candidate, err := fixture.architecture.stageCandidate(t.Context(), fixture.selected)
	require.Error(t, err)
	assert.Nil(t, candidate)
	assert.ErrorIs(t, err, unix.EEXIST)
	assert.Equal(t, trustedReleaseCandidateNameAttempts, nameCalls)
	assert.Equal(t, trustedReleaseSelectedStagesFinalized, fixture.selected.state)
	assert.FileExists(t, markerPath)
	assert.NoDirExists(t, fixture.store.slotPath)
}

func TestTrustedReleaseArchitectureCandidateCreationFailuresConsumeStagesAndDiscardDirectory(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		inject func(*testing.T, *trustedReleaseCandidateFixture) context.Context
		want   error
		code   errs.Code
	}{
		"open created directory": {
			inject: func(t *testing.T, fixture *trustedReleaseCandidateFixture) context.Context {
				realOpenAt := fixture.architecture.deps.openAt
				fixture.architecture.deps.openAt = func(
					ctx context.Context,
					parentFD int,
					name string,
					flags int,
					mode uint32,
				) (int, error) {
					if name == fixture.candidateName {
						return -1, unix.EIO
					}
					return realOpenAt(ctx, parentFD, name, flags, mode)
				}
				return t.Context()
			},
			want: unix.EIO,
		},
		"owner": {
			inject: func(t *testing.T, fixture *trustedReleaseCandidateFixture) context.Context {
				fixture.architecture.deps.fchown = func(context.Context, int, int, int) error { return unix.EPERM }
				return t.Context()
			},
			want: unix.EPERM,
		},
		"mode": {
			inject: func(t *testing.T, fixture *trustedReleaseCandidateFixture) context.Context {
				fixture.architecture.deps.fchmod = func(context.Context, int, uint32) error { return unix.EPERM }
				return t.Context()
			},
			want: unix.EPERM,
		},
		"directory metadata": {
			inject: func(t *testing.T, fixture *trustedReleaseCandidateFixture) context.Context {
				realFstat := fixture.architecture.deps.fstat
				calls := 0
				fixture.architecture.deps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
					if err := realFstat(ctx, fd, stat); err != nil {
						return err
					}
					calls++
					if calls == 1 {
						stat.Mode = stat.Mode&^07777 | 0755
					}
					return nil
				}
				return t.Context()
			},
			code: errs.CodeBinaryUntrusted,
		},
		"different filesystem": {
			inject: func(t *testing.T, fixture *trustedReleaseCandidateFixture) context.Context {
				realFstat := fixture.architecture.deps.fstat
				calls := 0
				fixture.architecture.deps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
					if err := realFstat(ctx, fd, stat); err != nil {
						return err
					}
					calls++
					if calls == 3 {
						stat.Dev++
					}
					return nil
				}
				return t.Context()
			},
		},
		"canceled after mkdir": {
			inject: func(t *testing.T, fixture *trustedReleaseCandidateFixture) context.Context {
				ctx, cancel := context.WithCancel(t.Context())
				realMkdirAt := fixture.architecture.deps.mkdirAt
				fixture.architecture.deps.mkdirAt = func(
					mkdirCtx context.Context,
					parentFD int,
					name string,
					mode uint32,
				) error {
					err := realMkdirAt(mkdirCtx, parentFD, name, mode)
					cancel()
					return err
				}
				return ctx
			},
			want: context.Canceled,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseCandidateFixture(t)
			before := directoryNamesForTest(t, fixture.store.architecturePath)
			ctx := test.inject(t, fixture)

			candidate, err := fixture.architecture.stageCandidate(ctx, fixture.selected)
			require.Error(t, err)
			assert.Nil(t, candidate)
			wantCode := test.code
			if wantCode == "" {
				wantCode = errs.CodeBinaryTrustedInstallFailed
			}
			assert.Equal(t, wantCode, errs.AsDomainError(err).Code)
			if test.want != nil {
				assert.ErrorIs(t, err, test.want)
			}
			assert.Equal(t, trustedReleaseSelectedStagesFailed, fixture.selected.state)
			assert.Equal(t, -1, fixture.selected.firecracker.fd)
			assert.Equal(t, -1, fixture.selected.jailer.fd)
			if diff := cmp.Diff(before, directoryNamesForTest(t, fixture.store.architecturePath)); diff != "" {
				t.Errorf("candidate creation failure retained namespace state (-before +after):\n%s", diff)
			}
			assert.NoDirExists(t, fixture.store.slotPath)
		})
	}
}

func TestTrustedReleaseCandidateLinksAndSyncsInDurableOrder(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseCandidateFixture(t)
	realLink := fixture.architecture.deps.linkAnonymousLeaf
	realFsync := fixture.architecture.deps.fsync
	var events []string
	var candidateFD int
	var manifestFD int
	fixture.architecture.deps.linkAnonymousLeaf = func(
		ctx context.Context,
		sourceFD int,
		targetFD int,
		leaf string,
	) error {
		events = append(events, "link "+leaf)
		candidateFD = targetFD
		if leaf == trustedReleaseManifestLeaf {
			manifestFD = sourceFD
		}
		return realLink(ctx, sourceFD, targetFD, leaf)
	}
	fixture.architecture.deps.fsync = func(ctx context.Context, fd int) error {
		switch fd {
		case fixture.architecture.fd:
			events = append(events, "sync architecture")
		case fixture.selected.firecracker.fd:
			events = append(events, "sync firecracker")
		case fixture.selected.jailer.fd:
			events = append(events, "sync jailer")
		case manifestFD:
			events = append(events, "sync manifest")
		case candidateFD:
			events = append(events, "sync candidate")
		default:
			events = append(events, "sync anonymous manifest")
		}
		return realFsync(ctx, fd)
	}

	candidate, err := fixture.architecture.stageCandidate(t.Context(), fixture.selected)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, candidate.Release(context.Background())) })
	want := []string{
		"sync architecture",
		"sync anonymous manifest",
		"link " + trustedReleaseFirecrackerLeaf,
		"sync firecracker",
		"link " + trustedReleaseJailerLeaf,
		"sync jailer",
		"link " + trustedReleaseManifestLeaf,
		"sync manifest",
		"sync candidate",
		"sync architecture",
	}
	if diff := cmp.Diff(want, events); diff != "" {
		t.Errorf("trusted release candidate durability order mismatch (-want +got):\n%s", diff)
	}
}

func TestTrustedReleaseCandidateRejectsEveryAnonymousLinkFailureAndDiscardsPartialState(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		failAt int
		cause  error
	}{
		"first lacks capability":   {failAt: 1, cause: unix.EPERM},
		"first crosses filesystem": {failAt: 1, cause: unix.EXDEV},
		"first collides":           {failAt: 1, cause: unix.EEXIST},
		"second":                   {failAt: 2, cause: unix.EIO},
		"third":                    {failAt: 3, cause: unix.ENOSPC},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseCandidateFixture(t)
			realLink := fixture.architecture.deps.linkAnonymousLeaf
			linkCalls := 0
			fixture.architecture.deps.linkAnonymousLeaf = func(
				ctx context.Context,
				sourceFD int,
				targetFD int,
				leaf string,
			) error {
				linkCalls++
				if linkCalls == test.failAt {
					return test.cause
				}
				return realLink(ctx, sourceFD, targetFD, leaf)
			}

			candidate, err := fixture.architecture.stageCandidate(t.Context(), fixture.selected)
			require.Error(t, err)
			assert.Nil(t, candidate)
			assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
			assert.ErrorIs(t, err, test.cause)
			assert.Equal(t, test.failAt, linkCalls)
			assert.Equal(t, -1, fixture.selected.firecracker.fd)
			assert.Equal(t, -1, fixture.selected.jailer.fd)
			assert.NoDirExists(t, filepath.Join(fixture.store.architecturePath, fixture.candidateName))
			assert.NoDirExists(t, fixture.store.slotPath)
		})
	}
}

func TestTrustedReleaseCandidateCancellationAfterFirstLinkUsesUncanceledDiscard(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseCandidateFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	realLink := fixture.architecture.deps.linkAnonymousLeaf
	fixture.architecture.deps.linkAnonymousLeaf = func(
		linkCtx context.Context,
		sourceFD int,
		targetFD int,
		leaf string,
	) error {
		err := realLink(linkCtx, sourceFD, targetFD, leaf)
		cancel()
		return err
	}
	realUnlinkAt := fixture.architecture.deps.unlinkAt
	cleanupWasCanceled := false
	fixture.architecture.deps.unlinkAt = func(
		cleanupCtx context.Context,
		parentFD int,
		name string,
		flags int,
	) error {
		cleanupWasCanceled = cleanupWasCanceled || cleanupCtx.Err() != nil
		return realUnlinkAt(cleanupCtx, parentFD, name, flags)
	}
	realSelectedClose := fixture.selected.deps.close
	fixture.selected.deps.close = func(cleanupCtx context.Context, fd int) error {
		cleanupWasCanceled = cleanupWasCanceled || cleanupCtx.Err() != nil
		return realSelectedClose(cleanupCtx, fd)
	}

	candidate, err := fixture.architecture.stageCandidate(ctx, fixture.selected)
	require.Error(t, err)
	assert.Nil(t, candidate)
	assert.ErrorIs(t, err, context.Canceled)
	assert.False(t, cleanupWasCanceled)
	assert.NoDirExists(t, filepath.Join(fixture.store.architecturePath, fixture.candidateName))
	assert.NoDirExists(t, fixture.store.slotPath)
}

func TestTrustedReleaseCandidateRejectsUnsafePostLinkMetadata(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*unix.Stat_t) error{
		"stat failure": func(*unix.Stat_t) error { return unix.EIO },
		"device": func(stat *unix.Stat_t) error {
			stat.Dev++
			return nil
		},
		"inode": func(stat *unix.Stat_t) error {
			stat.Ino++
			return nil
		},
		"type": func(stat *unix.Stat_t) error {
			stat.Mode = stat.Mode&^unix.S_IFMT | unix.S_IFDIR
			return nil
		},
		"link count": func(stat *unix.Stat_t) error {
			stat.Nlink++
			return nil
		},
		"owner UID": func(stat *unix.Stat_t) error {
			stat.Uid++
			return nil
		},
		"owner GID": func(stat *unix.Stat_t) error {
			stat.Gid++
			return nil
		},
		"mode": func(stat *unix.Stat_t) error {
			stat.Mode = stat.Mode&^07777 | 0700
			return nil
		},
		"size": func(stat *unix.Stat_t) error {
			stat.Size++
			return nil
		},
		"modification time": func(stat *unix.Stat_t) error {
			stat.Mtim.Nsec++
			return nil
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseCandidateFixture(t)
			realFstat := fixture.architecture.deps.fstat
			fixture.architecture.deps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
				if err := realFstat(ctx, fd, stat); err != nil {
					return err
				}
				if fd == fixture.selected.firecracker.fd && stat.Nlink == 1 {
					return mutate(stat)
				}
				return nil
			}

			candidate, err := fixture.architecture.stageCandidate(t.Context(), fixture.selected)
			require.Error(t, err)
			assert.Nil(t, candidate)
			assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
			if name == "stat failure" {
				assert.ErrorIs(t, err, unix.EIO)
			}
			assert.NoDirExists(t, filepath.Join(fixture.store.architecturePath, fixture.candidateName))
			assert.NoDirExists(t, fixture.store.slotPath)
		})
	}
}

func TestTrustedReleaseCandidateFsyncFailuresLeaveOnlyRecoverableState(t *testing.T) {
	t.Parallel()

	tests := []string{"firecracker", "jailer", "manifest", "candidate", "architecture"}
	for _, target := range tests {
		t.Run(target, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseCandidateFixture(t)
			realLink := fixture.architecture.deps.linkAnonymousLeaf
			realFsync := fixture.architecture.deps.fsync
			linked := 0
			manifestFD := -1
			candidateFD := -1
			fixture.architecture.deps.linkAnonymousLeaf = func(
				ctx context.Context,
				sourceFD int,
				targetFD int,
				leaf string,
			) error {
				if err := realLink(ctx, sourceFD, targetFD, leaf); err != nil {
					return err
				}
				linked++
				candidateFD = targetFD
				if leaf == trustedReleaseManifestLeaf {
					manifestFD = sourceFD
				}
				return nil
			}
			fixture.architecture.deps.fsync = func(ctx context.Context, fd int) error {
				fail := false
				switch target {
				case "firecracker":
					fail = linked >= 1 && fd == fixture.selected.firecracker.fd
				case "jailer":
					fail = linked >= 2 && fd == fixture.selected.jailer.fd
				case "manifest":
					fail = linked >= 3 && fd == manifestFD
				case "candidate":
					fail = linked >= 3 && fd == candidateFD
				case "architecture":
					fail = linked >= 3 && fd == fixture.architecture.fd
				}
				if fail {
					return unix.ENOSPC
				}
				return realFsync(ctx, fd)
			}

			candidate, err := fixture.architecture.stageCandidate(t.Context(), fixture.selected)
			require.Error(t, err)
			assert.Nil(t, candidate)
			assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(err).Code)
			assert.ErrorIs(t, err, unix.ENOSPC)
			assert.NoDirExists(t, fixture.store.slotPath)
			fixture.architecture.deps.fsync = realFsync
			require.NoError(t, fixture.architecture.recoverCandidates(t.Context()))
			assert.NoDirExists(t, filepath.Join(fixture.store.architecturePath, fixture.candidateName))
		})
	}
}

func TestTrustedReleaseCandidateRejectsReadmissionFailuresAndDiscardsCandidate(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		inject   func(*testing.T, *trustedReleaseCandidateFixture)
		want     error
		wantCode errs.Code
	}{
		"enumeration": {
			inject: func(_ *testing.T, fixture *trustedReleaseCandidateFixture) {
				realReadDirNames := fixture.architecture.deps.readDirNames
				candidateFD := -1
				realLink := fixture.architecture.deps.linkAnonymousLeaf
				fixture.architecture.deps.linkAnonymousLeaf = func(
					ctx context.Context,
					sourceFD int,
					targetFD int,
					leaf string,
				) error {
					candidateFD = targetFD
					return realLink(ctx, sourceFD, targetFD, leaf)
				}
				fixture.architecture.deps.readDirNames = func(ctx context.Context, fd int) ([]string, error) {
					if fd == candidateFD {
						return nil, unix.EIO
					}
					return realReadDirNames(ctx, fd)
				}
			},
			want: unix.EIO,
		},
		"unexpected leaf": {
			inject: func(_ *testing.T, fixture *trustedReleaseCandidateFixture) {
				realReadDirNames := fixture.architecture.deps.readDirNames
				candidateFD := -1
				realLink := fixture.architecture.deps.linkAnonymousLeaf
				fixture.architecture.deps.linkAnonymousLeaf = func(
					ctx context.Context,
					sourceFD int,
					targetFD int,
					leaf string,
				) error {
					candidateFD = targetFD
					return realLink(ctx, sourceFD, targetFD, leaf)
				}
				fixture.architecture.deps.readDirNames = func(ctx context.Context, fd int) ([]string, error) {
					names, err := realReadDirNames(ctx, fd)
					if err == nil && fd == candidateFD {
						return append(names, "unexpected"), nil
					}
					return names, err
				}
			},
			wantCode: errs.CodeBinaryUntrusted,
		},
		"manifest open": {
			inject: func(_ *testing.T, fixture *trustedReleaseCandidateFixture) {
				failCandidateReadmissionOpen(fixture, trustedReleaseManifestLeaf, unix.EIO)
			},
			want: unix.EIO,
		},
		"Firecracker open": {
			inject: func(_ *testing.T, fixture *trustedReleaseCandidateFixture) {
				failCandidateReadmissionOpen(fixture, trustedReleaseFirecrackerLeaf, unix.EIO)
			},
			want: unix.EIO,
		},
		"Jailer open": {
			inject: func(_ *testing.T, fixture *trustedReleaseCandidateFixture) {
				failCandidateReadmissionOpen(fixture, trustedReleaseJailerLeaf, unix.EIO)
			},
			want: unix.EIO,
		},
		"Firecracker read": {
			inject: func(_ *testing.T, fixture *trustedReleaseCandidateFixture) {
				firecrackerFD := captureCandidateReadmissionLeafFD(fixture, trustedReleaseFirecrackerLeaf)
				realPread := fixture.architecture.deps.pread
				fixture.architecture.deps.pread = func(
					ctx context.Context,
					fd int,
					value []byte,
					offset int64,
				) (int, error) {
					if fd == *firecrackerFD {
						return 0, unix.EIO
					}
					return realPread(ctx, fd, value, offset)
				}
			},
			want: unix.EIO,
		},
		"manifest read": {
			inject: func(_ *testing.T, fixture *trustedReleaseCandidateFixture) {
				manifestFD := captureCandidateReadmissionLeafFD(fixture, trustedReleaseManifestLeaf)
				realRead := fixture.architecture.deps.read
				fixture.architecture.deps.read = func(ctx context.Context, fd int, value []byte) (int, error) {
					if fd == *manifestFD {
						return 0, unix.EIO
					}
					return realRead(ctx, fd, value)
				}
			},
			want: unix.EIO,
		},
		"cancellation while reading manifest": {
			inject: func(t *testing.T, fixture *trustedReleaseCandidateFixture) {
				manifestFD := captureCandidateReadmissionLeafFD(fixture, trustedReleaseManifestLeaf)
				realRead := fixture.architecture.deps.read
				ctx, cancel := context.WithCancel(t.Context())
				fixture.context = ctx
				fixture.architecture.deps.read = func(readCtx context.Context, fd int, value []byte) (int, error) {
					count, err := realRead(readCtx, fd, value)
					if fd == *manifestFD && count > 0 {
						cancel()
					}
					return count, err
				}
			},
			want: context.Canceled,
		},
		"different valid manifest": {
			inject: func(t *testing.T, fixture *trustedReleaseCandidateFixture) {
				manifestFD := captureCandidateReadmissionLeafFD(fixture, trustedReleaseManifestLeaf)
				originalRaw, err := encodeTrustedReleaseManifest(fixture.selected.manifest)
				require.NoError(t, err)
				changed := fixture.selected.manifest
				changed.archiveDigest[0]++
				changedRaw, err := encodeTrustedReleaseManifest(changed)
				require.NoError(t, err)
				require.Len(t, changedRaw, len(originalRaw))
				realRead := fixture.architecture.deps.read
				fixture.architecture.deps.read = func(ctx context.Context, fd int, value []byte) (int, error) {
					count, readErr := realRead(ctx, fd, value)
					if fd == *manifestFD && count > 0 {
						copy(value[:count], changedRaw[:count])
					}
					return count, readErr
				}
			},
			wantCode: errs.CodeBinaryUntrusted,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseCandidateFixture(t)
			test.inject(t, fixture)

			candidate, err := fixture.architecture.stageCandidate(fixture.context, fixture.selected)
			require.Error(t, err)
			assert.Nil(t, candidate)
			wantCode := test.wantCode
			if wantCode == "" {
				wantCode = errs.CodeBinaryTrustedInstallFailed
			}
			assert.Equal(t, wantCode, errs.AsDomainError(err).Code)
			if test.want != nil {
				assert.ErrorIs(t, err, test.want)
			}
			assert.NoDirExists(t, filepath.Join(fixture.store.architecturePath, fixture.candidateName))
			assert.NoDirExists(t, fixture.store.slotPath)
		})
	}
}

func TestTrustedReleaseCandidateWritableStageCloseFailuresDiscardCandidate(t *testing.T) {
	t.Parallel()

	tests := []string{"manifest", "selected executables"}
	for _, target := range tests {
		t.Run(target, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseCandidateFixture(t)
			switch target {
			case "manifest":
				manifestFD := -1
				realLink := fixture.architecture.deps.linkAnonymousLeaf
				fixture.architecture.deps.linkAnonymousLeaf = func(
					ctx context.Context,
					sourceFD int,
					targetFD int,
					leaf string,
				) error {
					if leaf == trustedReleaseManifestLeaf {
						manifestFD = sourceFD
					}
					return realLink(ctx, sourceFD, targetFD, leaf)
				}
				realClose := fixture.architecture.deps.close
				fixture.architecture.deps.close = func(ctx context.Context, fd int) error {
					err := realClose(ctx, fd)
					if fd == manifestFD {
						return errors.Join(err, unix.EIO)
					}
					return err
				}
			case "selected executables":
				realClose := fixture.selected.deps.close
				fixture.selected.deps.close = func(ctx context.Context, fd int) error {
					err := realClose(ctx, fd)
					if fd == fixture.selected.firecracker.fd {
						return errors.Join(err, unix.EIO)
					}
					return err
				}
			default:
				t.Fatalf("unsupported close target %q", target)
			}

			candidate, err := fixture.architecture.stageCandidate(t.Context(), fixture.selected)
			require.Error(t, err)
			assert.Nil(t, candidate)
			assert.ErrorIs(t, err, unix.EIO)
			assert.Equal(t, -1, fixture.selected.firecracker.fd)
			assert.Equal(t, -1, fixture.selected.jailer.fd)
			assert.NoDirExists(t, filepath.Join(fixture.store.architecturePath, fixture.candidateName))
			assert.NoDirExists(t, fixture.store.slotPath)
		})
	}
}

func TestTrustedReleaseCandidateReleaseFailuresRemainRecoverable(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*trustedReleaseCandidate){
		"leaf unlink": func(candidate *trustedReleaseCandidate) {
			realUnlinkAt := candidate.directory.deps.unlinkAt
			candidate.directory.deps.unlinkAt = func(ctx context.Context, fd int, name string, flags int) error {
				if name == trustedReleaseManifestLeaf {
					return unix.EIO
				}
				return realUnlinkAt(ctx, fd, name, flags)
			}
		},
		"candidate sync": func(candidate *trustedReleaseCandidate) {
			realFsync := candidate.directory.deps.fsync
			candidate.directory.deps.fsync = func(ctx context.Context, fd int) error {
				if fd == candidate.directory.slotFD {
					return unix.EIO
				}
				return realFsync(ctx, fd)
			}
		},
		"candidate removal": func(candidate *trustedReleaseCandidate) {
			realUnlinkAt := candidate.architecture.deps.unlinkAt
			candidate.architecture.deps.unlinkAt = func(ctx context.Context, fd int, name string, flags int) error {
				if flags == unix.AT_REMOVEDIR {
					return unix.EIO
				}
				return realUnlinkAt(ctx, fd, name, flags)
			}
		},
		"architecture sync": func(candidate *trustedReleaseCandidate) {
			realFsync := candidate.architecture.deps.fsync
			candidate.architecture.deps.fsync = func(ctx context.Context, fd int) error {
				if fd == candidate.architecture.fd {
					return unix.EIO
				}
				return realFsync(ctx, fd)
			}
		},
		"admitted descriptor close": func(candidate *trustedReleaseCandidate) {
			realClose := candidate.admission.executables.deps.close
			candidate.admission.executables.deps.close = func(ctx context.Context, fd int) error {
				return errors.Join(realClose(ctx, fd), unix.EIO)
			}
		},
	}
	for name, inject := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newTrustedReleaseCandidateFixture(t)
			candidate, err := fixture.architecture.stageCandidate(t.Context(), fixture.selected)
			require.NoError(t, err)
			inject(candidate)

			err = candidate.Release(t.Context())
			require.Error(t, err)
			assert.ErrorIs(t, err, unix.EIO)
			assert.Equal(t, trustedReleaseCandidateReleased, candidate.state)
			assert.NoDirExists(t, fixture.store.slotPath)

			recovery, openErr := fixture.writer.openArchitectureForWrite(
				t.Context(),
				trustedReleaseSlotLeaseForWriteTest(fixture.store.slot),
			)
			require.NoError(t, openErr)
			t.Cleanup(func() { require.NoError(t, recovery.Release(context.Background())) })
			require.NoError(t, recovery.recoverCandidates(t.Context()))
			assert.NoDirExists(t, filepath.Join(fixture.store.architecturePath, fixture.candidateName))
		})
	}
}

func TestTrustedReleaseCandidateReleaseIgnoresCallerCancellation(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseCandidateFixture(t)
	candidate, err := fixture.architecture.stageCandidate(t.Context(), fixture.selected)
	require.NoError(t, err)
	cleanupWasCanceled := false
	realUnlinkAt := candidate.directory.deps.unlinkAt
	candidate.directory.deps.unlinkAt = func(ctx context.Context, fd int, name string, flags int) error {
		cleanupWasCanceled = cleanupWasCanceled || ctx.Err() != nil
		return realUnlinkAt(ctx, fd, name, flags)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	require.NoError(t, candidate.Release(ctx))
	assert.False(t, cleanupWasCanceled)
	assert.NoDirExists(t, filepath.Join(fixture.store.architecturePath, fixture.candidateName))
	assert.NoDirExists(t, fixture.store.slotPath)
}

func TestTrustedReleaseCandidateDiscardPreservesPrimaryDomainMetadata(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseCandidateFixture(t)
	candidate, err := fixture.architecture.stageCandidate(t.Context(), fixture.selected)
	require.NoError(t, err)
	realUnlinkAt := candidate.directory.deps.unlinkAt
	candidate.directory.deps.unlinkAt = func(ctx context.Context, fd int, name string, flags int) error {
		if name == trustedReleaseManifestLeaf {
			return unix.EIO
		}
		return realUnlinkAt(ctx, fd, name, flags)
	}
	primary := errs.New(
		errs.CodeValidationFailed,
		"distinctive candidate failure",
		errs.WithClass(errs.ClassConflict),
		errs.WithEntity("trusted-release-candidate"),
		errs.WithDetails(map[string]any{"candidate_retained": true}),
	)

	err = candidate.discard(t.Context(), true, primary)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Same(t, primary, domainErr)
	assert.Equal(t, errs.CodeValidationFailed, domainErr.Code)
	assert.Equal(t, errs.ClassConflict, domainErr.Class)
	assert.Equal(t, "trusted-release-candidate", domainErr.Entity)
	if diff := cmp.Diff(map[string]any{"candidate_retained": true}, domainErr.Details); diff != "" {
		t.Errorf("candidate cleanup changed primary details (-want +got):\n%s", diff)
	}
	assert.ErrorIs(t, err, unix.EIO)

	recovery, openErr := fixture.writer.openArchitectureForWrite(
		t.Context(),
		trustedReleaseSlotLeaseForWriteTest(fixture.store.slot),
	)
	require.NoError(t, openErr)
	t.Cleanup(func() { require.NoError(t, recovery.Release(context.Background())) })
	require.NoError(t, recovery.recoverCandidates(t.Context()))
	assert.NoDirExists(t, filepath.Join(fixture.store.architecturePath, fixture.candidateName))
}

func TestTrustedReleaseCandidateProductionDependenciesStayNarrow(t *testing.T) {
	t.Parallel()

	fixture := newTrustedReleaseStoreFixture(t)
	prefix, err := trustedReleaseCandidatePrefix(fixture.slot)
	require.NoError(t, err)
	name, err := randomTrustedReleaseCandidateName(t.Context(), fixture.slot)
	require.NoError(t, err)
	require.NoError(t, validateTrustedReleaseCandidateName(prefix, name))
	assert.Len(t, name, len(prefix)+trustedReleaseCandidateNonceBytes*2+len(trustedReleaseCandidateNameSuffix))

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	assert.ErrorIs(t, linkAnonymousTrustedReleaseLeaf(canceled, -1, -1, trustedReleaseManifestLeaf), context.Canceled)
	assert.ErrorIs(t, linkAnonymousTrustedReleaseLeaf(t.Context(), -1, -1, "unexpected"), unix.EINVAL)
}

func failCandidateReadmissionOpen(
	fixture *trustedReleaseCandidateFixture,
	leaf string,
	cause error,
) {
	realReadDirNames := fixture.architecture.deps.readDirNames
	realOpenAt := fixture.architecture.deps.openAt
	candidateFD := -1
	fixture.architecture.deps.readDirNames = func(ctx context.Context, fd int) ([]string, error) {
		names, err := realReadDirNames(ctx, fd)
		if err == nil && len(names) == 3 {
			candidateFD = fd
		}
		return names, err
	}
	fixture.architecture.deps.openAt = func(
		ctx context.Context,
		parentFD int,
		name string,
		flags int,
		mode uint32,
	) (int, error) {
		if parentFD == candidateFD && name == leaf {
			return -1, cause
		}
		return realOpenAt(ctx, parentFD, name, flags, mode)
	}
}

func captureCandidateReadmissionLeafFD(fixture *trustedReleaseCandidateFixture, leaf string) *int {
	realReadDirNames := fixture.architecture.deps.readDirNames
	realOpenAt := fixture.architecture.deps.openAt
	candidateFD := -1
	leafFD := -1
	fixture.architecture.deps.readDirNames = func(ctx context.Context, fd int) ([]string, error) {
		names, err := realReadDirNames(ctx, fd)
		if err == nil && len(names) == 3 {
			candidateFD = fd
		}
		return names, err
	}
	fixture.architecture.deps.openAt = func(
		ctx context.Context,
		parentFD int,
		name string,
		flags int,
		mode uint32,
	) (int, error) {
		fd, err := realOpenAt(ctx, parentFD, name, flags, mode)
		if err == nil && parentFD == candidateFD && name == leaf {
			leafFD = fd
		}
		return fd, err
	}
	return &leafFD
}

type trustedReleaseCandidateFixture struct {
	store         *trustedReleaseStoreFixture
	writer        *trustedReleaseStoreWriteLease
	architecture  *trustedReleaseArchitectureWriteLease
	selected      *trustedReleaseSelectedExecutableStages
	candidateName string
	context       context.Context
}

func newTrustedReleaseCandidateFixture(t *testing.T) *trustedReleaseCandidateFixture {
	return newTrustedReleaseCandidateFixtureWithSlotLease(t, nil)
}

func newTrustedReleaseCandidateFixtureWithSlotLease(
	t *testing.T,
	slotLease *releaseSlotLease,
) *trustedReleaseCandidateFixture {
	t.Helper()

	store := newTrustedReleaseStoreFixture(t)
	require.NoError(t, os.Remove(store.slotPath))
	writer, err := openTrustedReleaseStoreForWrite(t.Context(), store.deps, store.policy)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, writer.Release(context.Background())) })
	if slotLease == nil {
		slotLease = trustedReleaseSlotLeaseForWriteTest(store.slot)
	}
	architecture, err := writer.openArchitectureForWrite(t.Context(), slotLease)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, architecture.Release(context.Background())) })
	prefix, err := trustedReleaseCandidatePrefix(store.slot)
	require.NoError(t, err)
	candidateName := prefix + "00000000000000000000000000000000.tmp"
	architecture.deps.candidateName = func(context.Context, releaseSlot) (string, error) {
		return candidateName, nil
	}
	architecture.deps.linkAnonymousLeaf = linkAnonymousTrustedReleaseLeafForTest

	archiveFixture := newTrustedReleaseArchiveFixture(t)
	setTrustedReleaseSelectedArchiveBytesForTest(
		t,
		archiveFixture,
		trustedReleaseTestELF("candidate Firecracker"),
		trustedReleaseTestELF("candidate Jailer"),
	)
	archiveStage, err := writer.createArchiveStage(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, archiveStage.Release(context.Background())) })
	compressed := archiveFixture.compressed(t)
	require.NoError(t, archiveStage.receive(
		t.Context(),
		bytes.NewReader(compressed),
		uint64(len(compressed)),
		trustedReleaseArchiveDigestForTest(compressed),
	))
	selected, err := writer.createSelectedExecutableStages(t.Context(), archiveFixture.policy)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, selected.Release(context.Background())) })
	require.NoError(t, selected.extract(t.Context(), archiveStage))
	_, err = selected.finalize(t.Context())
	require.NoError(t, err)

	return &trustedReleaseCandidateFixture{
		store: store, writer: writer, architecture: architecture, selected: selected, candidateName: candidateName,
		context: t.Context(),
	}
}

func linkAnonymousTrustedReleaseLeafForTest(
	_ context.Context,
	sourceFD int,
	targetDirectoryFD int,
	targetLeaf string,
) error {
	source := "/proc/self/fd/" + strconv.Itoa(sourceFD)
	return unix.Linkat(unix.AT_FDCWD, source, targetDirectoryFD, targetLeaf, unix.AT_SYMLINK_FOLLOW)
}
