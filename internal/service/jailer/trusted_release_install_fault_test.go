package jailer

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

// Rationale: every object handoff before the atomic commit must fail with a zero result and release all authority.
// A failure at one boundary must never fall through to another body mode or publication transaction.
func TestReleaseAuthorityInstallPrecommitHandoffFailures(t *testing.T) {
	tests := map[string]struct {
		caller     func(*trustedReleaseInstallFixture) *trustedReleaseCallerArchive
		prepareOld bool
		intent     trustedReleaseInstallIntent
		mutate     func(*testing.T, *trustedReleaseInstallFixture)
	}{
		"checksum fetch": {
			caller: installCallerArchiveForFaultTest,
			intent: trustedReleaseInstallAbsentOnly,
			mutate: func(_ *testing.T, fixture *trustedReleaseInstallFixture) {
				fixture.authority.checksumAuthority.client.Transport = trustedReleaseRoundTripFunc(
					func(*http.Request) (*http.Response, error) { return nil, unix.EIO },
				)
			},
		},
		"store open": {
			caller: installCallerArchiveForFaultTest,
			intent: trustedReleaseInstallAbsentOnly,
			mutate: func(_ *testing.T, fixture *trustedReleaseInstallFixture) {
				fixture.authority.storeDeps.open = func(context.Context, string, int, uint32) (int, error) {
					return -1, unix.EIO
				}
			},
		},
		"archive stage create": {
			caller: installCallerArchiveForFaultTest,
			intent: trustedReleaseInstallAbsentOnly,
			mutate: func(_ *testing.T, fixture *trustedReleaseInstallFixture) {
				realOpenAt := fixture.authority.storeDeps.openAt
				fixture.authority.storeDeps.openAt = func(
					ctx context.Context, parentFD int, name string, flags int, mode uint32,
				) (int, error) {
					if flags == trustedReleaseArchiveStageFlags {
						return -1, unix.EIO
					}
					return realOpenAt(ctx, parentFD, name, flags, mode)
				}
			},
		},
		"caller receive": {
			caller: func(fixture *trustedReleaseInstallFixture) *trustedReleaseCallerArchive {
				return &trustedReleaseCallerArchive{
					reader:    bytes.NewReader(fixture.archive[:len(fixture.archive)-1]),
					sizeBytes: uint64(len(fixture.archive)),
				}
			},
			intent: trustedReleaseInstallAbsentOnly,
			mutate: func(*testing.T, *trustedReleaseInstallFixture) {},
		},
		"root fetch": {
			caller: func(*trustedReleaseInstallFixture) *trustedReleaseCallerArchive { return nil },
			intent: trustedReleaseInstallAbsentOnly,
			mutate: func(_ *testing.T, fixture *trustedReleaseInstallFixture) {
				fixture.authority.archiveFetcher.client.Transport = trustedReleaseRoundTripFunc(
					func(*http.Request) (*http.Response, error) { return nil, unix.EIO },
				)
			},
		},
		"selected stages create": {
			caller: installCallerArchiveForFaultTest,
			intent: trustedReleaseInstallAbsentOnly,
			mutate: func(_ *testing.T, fixture *trustedReleaseInstallFixture) {
				realOpenAt := fixture.authority.storeDeps.openAt
				fixture.authority.storeDeps.openAt = func(
					ctx context.Context, parentFD int, name string, flags int, mode uint32,
				) (int, error) {
					if flags == trustedReleaseSelectedStageFlags {
						return -1, unix.EIO
					}
					return realOpenAt(ctx, parentFD, name, flags, mode)
				}
			},
		},
		"archive extraction": {
			caller: installCallerArchiveForFaultTest,
			intent: trustedReleaseInstallAbsentOnly,
			mutate: func(_ *testing.T, fixture *trustedReleaseInstallFixture) {
				realOpenAt := fixture.authority.storeDeps.openAt
				realPread := fixture.authority.storeDeps.pread
				archiveFD := -1
				fixture.authority.storeDeps.openAt = func(
					ctx context.Context, parentFD int, name string, flags int, mode uint32,
				) (int, error) {
					fd, err := realOpenAt(ctx, parentFD, name, flags, mode)
					if err == nil && flags == trustedReleaseArchiveStageFlags {
						archiveFD = fd
					}
					return fd, err
				}
				fixture.authority.storeDeps.pread = func(
					ctx context.Context, fd int, value []byte, offset int64,
				) (int, error) {
					if fd == archiveFD {
						return 0, unix.EIO
					}
					return realPread(ctx, fd, value, offset)
				}
			},
		},
		"archive checked close": {
			caller: installCallerArchiveForFaultTest,
			intent: trustedReleaseInstallAbsentOnly,
			mutate: func(_ *testing.T, fixture *trustedReleaseInstallFixture) {
				realOpenAt := fixture.authority.storeDeps.openAt
				realClose := fixture.authority.storeDeps.close
				archiveFD := -1
				fixture.authority.storeDeps.openAt = func(
					ctx context.Context, parentFD int, name string, flags int, mode uint32,
				) (int, error) {
					fd, err := realOpenAt(ctx, parentFD, name, flags, mode)
					if err == nil && flags == trustedReleaseArchiveStageFlags {
						archiveFD = fd
					}
					return fd, err
				}
				fixture.authority.storeDeps.close = func(ctx context.Context, fd int) error {
					err := realClose(ctx, fd)
					if fd == archiveFD {
						return errors.Join(err, unix.EIO)
					}
					return err
				}
			},
		},
		"selected finalization": {
			caller: installCallerArchiveForFaultTest,
			intent: trustedReleaseInstallAbsentOnly,
			mutate: func(_ *testing.T, fixture *trustedReleaseInstallFixture) {
				realFchmod := fixture.authority.storeDeps.fchmod
				fixture.authority.storeDeps.fchmod = func(ctx context.Context, fd int, mode uint32) error {
					if mode == trustedReleaseStoreExecutableMode {
						return unix.EIO
					}
					return realFchmod(ctx, fd, mode)
				}
			},
		},
		"slot lock": {
			caller: installCallerArchiveForFaultTest,
			intent: trustedReleaseInstallAbsentOnly,
			mutate: func(_ *testing.T, fixture *trustedReleaseInstallFixture) {
				realFlock := fixture.instances.deps.flock
				fixture.instances.deps.flock = func(ctx context.Context, fd int, how int) error {
					if how == unix.LOCK_EX|unix.LOCK_NB {
						return unix.EIO
					}
					return realFlock(ctx, fd, how)
				}
			},
		},
		"architecture open": {
			caller: installCallerArchiveForFaultTest,
			intent: trustedReleaseInstallAbsentOnly,
			mutate: func(_ *testing.T, fixture *trustedReleaseInstallFixture) {
				realOpenAt := fixture.authority.storeDeps.openAt
				fixture.authority.storeDeps.openAt = func(
					ctx context.Context, parentFD int, name string, flags int, mode uint32,
				) (int, error) {
					if name == fixture.store.slot.architecture {
						return -1, unix.EIO
					}
					return realOpenAt(ctx, parentFD, name, flags, mode)
				}
			},
		},
		"store writer checked close": {
			caller: installCallerArchiveForFaultTest,
			intent: trustedReleaseInstallAbsentOnly,
			mutate: func(_ *testing.T, fixture *trustedReleaseInstallFixture) {
				realOpenAt := fixture.authority.storeDeps.openAt
				realClose := fixture.authority.storeDeps.close
				binariesFD := -1
				fixture.authority.storeDeps.openAt = func(
					ctx context.Context, parentFD int, name string, flags int, mode uint32,
				) (int, error) {
					fd, err := realOpenAt(ctx, parentFD, name, flags, mode)
					if err == nil && name == "binaries" {
						binariesFD = fd
					}
					return fd, err
				}
				fixture.authority.storeDeps.close = func(ctx context.Context, fd int) error {
					err := realClose(ctx, fd)
					if fd == binariesFD {
						return errors.Join(err, unix.EIO)
					}
					return err
				}
			},
		},
		"candidate assembly": {
			caller: installCallerArchiveForFaultTest,
			intent: trustedReleaseInstallAbsentOnly,
			mutate: func(_ *testing.T, fixture *trustedReleaseInstallFixture) {
				fixture.authority.storeDeps.candidateName = func(context.Context, releaseSlot) (string, error) {
					return "", unix.EIO
				}
			},
		},
		"installed-slot selection open": {
			caller:     installCallerArchiveForFaultTest,
			prepareOld: true,
			intent:     trustedReleaseInstallAllowReplacement,
			mutate: func(_ *testing.T, fixture *trustedReleaseInstallFixture) {
				realOpenAt := fixture.authority.storeDeps.openAt
				candidateNamed := false
				fixture.authority.storeDeps.candidateName = func(
					context.Context, releaseSlot,
				) (string, error) {
					candidateNamed = true
					prefix, err := trustedReleaseCandidatePrefix(fixture.store.slot)
					return prefix + "00000000000000000000000000000000.tmp", err
				}
				fixture.authority.storeDeps.openAt = func(
					ctx context.Context, parentFD int, name string, flags int, mode uint32,
				) (int, error) {
					if candidateNamed && name == fixture.store.slot.version {
						return -1, unix.EIO
					}
					return realOpenAt(ctx, parentFD, name, flags, mode)
				}
			},
		},
		"installed-slot selection checked close": {
			caller:     installCallerArchiveForFaultTest,
			prepareOld: true,
			intent:     trustedReleaseInstallAllowReplacement,
			mutate: func(_ *testing.T, fixture *trustedReleaseInstallFixture) {
				realOpenAt := fixture.authority.storeDeps.openAt
				realClose := fixture.authority.storeDeps.close
				selectionFD := -1
				candidateNamed := false
				failed := false
				fixture.authority.storeDeps.candidateName = func(
					context.Context, releaseSlot,
				) (string, error) {
					candidateNamed = true
					prefix, err := trustedReleaseCandidatePrefix(fixture.store.slot)
					return prefix + "00000000000000000000000000000000.tmp", err
				}
				fixture.authority.storeDeps.openAt = func(
					ctx context.Context, parentFD int, name string, flags int, mode uint32,
				) (int, error) {
					fd, err := realOpenAt(ctx, parentFD, name, flags, mode)
					if err == nil && candidateNamed && name == fixture.store.slot.version && selectionFD < 0 {
						selectionFD = fd
					}
					return fd, err
				}
				fixture.authority.storeDeps.close = func(ctx context.Context, fd int) error {
					err := realClose(ctx, fd)
					if fd == selectionFD && !failed {
						failed = true
						return errors.Join(err, unix.EIO)
					}
					return err
				}
			},
		},
		"absent publication": {
			caller: installCallerArchiveForFaultTest,
			intent: trustedReleaseInstallAbsentOnly,
			mutate: func(_ *testing.T, fixture *trustedReleaseInstallFixture) {
				fixture.authority.storeDeps.renameCandidateNoReplace = func(
					context.Context, int, string, string,
				) error {
					return unix.EIO
				}
			},
		},
		"replacement publication": {
			caller:     installCallerArchiveForFaultTest,
			prepareOld: true,
			intent:     trustedReleaseInstallAllowReplacement,
			mutate: func(_ *testing.T, fixture *trustedReleaseInstallFixture) {
				fixture.authority.storeDeps.exchangeCandidate = func(
					context.Context, int, string, string,
				) error {
					return unix.EIO
				}
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newTrustedReleaseInstallFixture(t)
			var oldManifest trustedReleaseManifest
			if tc.prepareOld {
				oldManifest = fixture.writeDifferentInstalled(t)
			}
			tc.mutate(t, fixture)

			caller := tc.caller(fixture)
			result, err := fixture.authority.install(t.Context(), fixture.store.slot, tc.intent, caller)
			require.Error(t, err)
			if diff := cmp.Diff(
				trustedReleaseInstallResult{},
				result,
				trustedReleaseInstallCmpOptions()...); diff != "" {
				t.Errorf("precommit failure result mismatch (-want +got):\n%s", diff)
			}
			if tc.prepareOld {
				assertInstalledTrustedReleaseMatchesManifest(t, fixture.store.slotPath, oldManifest)
			} else {
				assert.NoDirExists(t, fixture.store.slotPath)
			}
			if caller != nil {
				assert.Zero(t, fixture.archiveRequests)
			}
			assertNoTrustedReleaseCandidatesForInstallTest(t, fixture)
			if name != "slot lock" {
				assertPreparedReleaseSlotReacquirable(t, fixture.instances, fixture.store.slot)
			}
		})
	}
}

func TestReleaseAuthorityInstallUnchangedCandidateCleanupFailureRequiresRetry(t *testing.T) {
	fixture := newTrustedReleaseInstallFixture(t)
	fixture.writeInstalled(t, fixture.manifest, fixture.firecracker, fixture.jailer)
	realUnlink := fixture.authority.storeDeps.unlinkAt
	failed := false
	fixture.authority.storeDeps.unlinkAt = func(
		ctx context.Context, parentFD int, name string, flags int,
	) error {
		if !failed && name == trustedReleaseManifestLeaf {
			failed = true
			return unix.EIO
		}
		return realUnlink(ctx, parentFD, name, flags)
	}

	result, err := fixture.authority.install(
		t.Context(), fixture.store.slot, trustedReleaseInstallAbsentOnly, fixture.callerArchive(),
	)
	require.Error(t, err)
	if diff := cmp.Diff(trustedReleaseInstallResult{}, result, trustedReleaseInstallCmpOptions()...); diff != "" {
		t.Errorf("unchanged cleanup failure result mismatch (-want +got):\n%s", diff)
	}
	assertInstalledTrustedReleaseMatchesManifest(t, fixture.store.slotPath, fixture.manifest)
	require.Len(t, trustedReleaseCandidatePathsForInstallTest(t, fixture), 1)
	assertPreparedReleaseSlotReacquirable(t, fixture.instances, fixture.store.slot)
}

func TestReleaseAuthorityInstallRetainsCommittedResultAfterPublicationFailure(t *testing.T) {
	tests := map[string]struct {
		prepare       func(*testing.T, *trustedReleaseInstallFixture)
		intent        trustedReleaseInstallIntent
		wrapCommit    func(*trustedReleaseInstallFixture, func())
		wantOutcome   trustedReleaseInstallOutcome
		wantDetails   map[string]any
		wantCandidate bool
	}{
		"installed architecture sync": {
			prepare: func(*testing.T, *trustedReleaseInstallFixture) {},
			intent:  trustedReleaseInstallAbsentOnly,
			wrapCommit: func(fixture *trustedReleaseInstallFixture, committed func()) {
				realRename := fixture.authority.storeDeps.renameCandidateNoReplace
				fixture.authority.storeDeps.renameCandidateNoReplace = func(
					ctx context.Context, parentFD int, source string, target string,
				) error {
					err := realRename(ctx, parentFD, source, target)
					if err == nil {
						committed()
					}
					return err
				}
			},
			wantOutcome: trustedReleaseInstallInstalled,
			wantDetails: map[string]any{
				"release_installed":    true,
				"durability_uncertain": true,
			},
		},
		"replaced architecture sync": {
			prepare: func(t *testing.T, fixture *trustedReleaseInstallFixture) {
				fixture.writeDifferentInstalled(t)
			},
			intent: trustedReleaseInstallAllowReplacement,
			wrapCommit: func(fixture *trustedReleaseInstallFixture, committed func()) {
				realExchange := fixture.authority.storeDeps.exchangeCandidate
				fixture.authority.storeDeps.exchangeCandidate = func(
					ctx context.Context, parentFD int, source string, target string,
				) error {
					err := realExchange(ctx, parentFD, source, target)
					if err == nil {
						committed()
					}
					return err
				}
			},
			wantOutcome: trustedReleaseInstallReplaced,
			wantDetails: map[string]any{
				"release_replaced":         true,
				"durability_uncertain":     true,
				"retired_release_retained": true,
			},
			wantCandidate: true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newTrustedReleaseInstallFixture(t)
			tc.prepare(t, fixture)
			architectureFD := -1
			committed := false
			realOpenAt := fixture.authority.storeDeps.openAt
			fixture.authority.storeDeps.openAt = func(
				ctx context.Context, parentFD int, leaf string, flags int, mode uint32,
			) (int, error) {
				fd, err := realOpenAt(ctx, parentFD, leaf, flags, mode)
				if err == nil && leaf == fixture.store.slot.architecture {
					architectureFD = fd
				}
				return fd, err
			}
			tc.wrapCommit(fixture, func() { committed = true })
			realFsync := fixture.authority.storeDeps.fsync
			fixture.authority.storeDeps.fsync = func(ctx context.Context, fd int) error {
				if committed && fd == architectureFD {
					return unix.EIO
				}
				return realFsync(ctx, fd)
			}

			result, err := fixture.authority.install(
				t.Context(), fixture.store.slot, tc.intent, fixture.callerArchive(),
			)
			require.Error(t, err)
			want := trustedReleaseInstallResult{outcome: tc.wantOutcome, manifest: fixture.manifest}
			if diff := cmp.Diff(want, result, trustedReleaseInstallCmpOptions()...); diff != "" {
				t.Errorf("postcommit result mismatch (-want +got):\n%s", diff)
			}
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			if diff := cmp.Diff(tc.wantDetails, domainErr.Details); diff != "" {
				t.Errorf("postcommit details mismatch (-want +got):\n%s", diff)
			}
			assertInstalledTrustedReleaseMatchesArchive(t, fixture)
			if tc.wantCandidate {
				require.Len(t, trustedReleaseCandidatePathsForInstallTest(t, fixture), 1)
			} else {
				assertNoTrustedReleaseCandidatesForInstallTest(t, fixture)
			}
			assertPreparedReleaseSlotReacquirable(t, fixture.instances, fixture.store.slot)
		})
	}
}

func TestReleaseAuthorityInstallFinishesCommittedCleanupAfterCancellation(t *testing.T) {
	tests := map[string]struct {
		prepare     func(*testing.T, *trustedReleaseInstallFixture)
		intent      trustedReleaseInstallIntent
		wrapCommit  func(*trustedReleaseInstallFixture, context.CancelFunc)
		wantOutcome trustedReleaseInstallOutcome
	}{
		"installed": {
			prepare: func(*testing.T, *trustedReleaseInstallFixture) {},
			intent:  trustedReleaseInstallAbsentOnly,
			wrapCommit: func(fixture *trustedReleaseInstallFixture, cancel context.CancelFunc) {
				realRename := fixture.authority.storeDeps.renameCandidateNoReplace
				fixture.authority.storeDeps.renameCandidateNoReplace = func(
					ctx context.Context, parentFD int, source string, target string,
				) error {
					err := realRename(ctx, parentFD, source, target)
					if err == nil {
						cancel()
					}
					return err
				}
			},
			wantOutcome: trustedReleaseInstallInstalled,
		},
		"replaced": {
			prepare: func(t *testing.T, fixture *trustedReleaseInstallFixture) {
				fixture.writeDifferentInstalled(t)
			},
			intent: trustedReleaseInstallAllowReplacement,
			wrapCommit: func(fixture *trustedReleaseInstallFixture, cancel context.CancelFunc) {
				realExchange := fixture.authority.storeDeps.exchangeCandidate
				fixture.authority.storeDeps.exchangeCandidate = func(
					ctx context.Context, parentFD int, source string, target string,
				) error {
					err := realExchange(ctx, parentFD, source, target)
					if err == nil {
						cancel()
					}
					return err
				}
			},
			wantOutcome: trustedReleaseInstallReplaced,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newTrustedReleaseInstallFixture(t)
			tc.prepare(t, fixture)
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			tc.wrapCommit(fixture, cancel)

			result, err := fixture.authority.install(
				ctx, fixture.store.slot, tc.intent, fixture.callerArchive(),
			)
			require.NoError(t, err)
			want := trustedReleaseInstallResult{outcome: tc.wantOutcome, manifest: fixture.manifest}
			if diff := cmp.Diff(want, result, trustedReleaseInstallCmpOptions()...); diff != "" {
				t.Errorf("post-cancellation result mismatch (-want +got):\n%s", diff)
			}
			assertInstalledTrustedReleaseMatchesArchive(t, fixture)
			assertNoTrustedReleaseCandidatesForInstallTest(t, fixture)
			assertPreparedReleaseSlotReacquirable(t, fixture.instances, fixture.store.slot)
		})
	}
}

func TestReleaseAuthorityInstallSerializesSameSlotTransactions(t *testing.T) {
	fixture := newTrustedReleaseInstallFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	source, err := newTrustedReleaseSource(fixture.store.slot)
	require.NoError(t, err)
	checksumBody := hex.EncodeToString(fixture.manifest.archiveDigest[:]) + "  " + source.archiveName + "\n"
	fixture.authority.checksumAuthority.client.Transport = trustedReleaseRoundTripFunc(
		func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode:    http.StatusOK,
				Body:          io.NopCloser(strings.NewReader(checksumBody)),
				ContentLength: int64(len(checksumBody)),
				Header:        make(http.Header),
			}, nil
		},
	)

	firstCandidateEntered := make(chan struct{})
	releaseFirstCandidate := make(chan struct{})
	var candidateCalls atomic.Int32
	prefix, err := trustedReleaseCandidatePrefix(fixture.store.slot)
	require.NoError(t, err)
	fixture.authority.storeDeps.candidateName = func(ctx context.Context, _ releaseSlot) (string, error) {
		if candidateCalls.Add(1) == 1 {
			close(firstCandidateEntered)
			select {
			case <-releaseFirstCandidate:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		return prefix + "00000000000000000000000000000000.tmp", nil
	}

	secondLockAttempted := make(chan struct{})
	var secondLockOnce sync.Once
	var exclusiveAttempts atomic.Int32
	realFlock := fixture.instances.deps.flock
	fixture.instances.deps.flock = func(ctx context.Context, fd int, how int) error {
		if how == unix.LOCK_EX|unix.LOCK_NB && exclusiveAttempts.Add(1) == 2 {
			secondLockOnce.Do(func() { close(secondLockAttempted) })
		}
		return realFlock(ctx, fd, how)
	}

	type installResponse struct {
		result trustedReleaseInstallResult
		err    error
	}
	responses := make(chan installResponse, 2)
	install := func() {
		result, installErr := fixture.authority.install(
			ctx, fixture.store.slot, trustedReleaseInstallAbsentOnly, fixture.callerArchive(),
		)
		responses <- installResponse{result: result, err: installErr}
	}
	go install()
	requireInstallSignal(t, ctx, firstCandidateEntered, "first candidate entered while holding slot lock")
	go install()
	requireInstallSignal(t, ctx, secondLockAttempted, "second install attempted the held slot lock")
	assert.Equal(t, int32(1), candidateCalls.Load())
	close(releaseFirstCandidate)

	outcomes := make(map[trustedReleaseInstallOutcome]int, 2)
	for range 2 {
		response := <-responses
		require.NoError(t, response.err)
		if diff := cmp.Diff(
			fixture.manifest,
			response.result.manifest,
			trustedReleaseInstallCmpOptions()...); diff != "" {
			t.Errorf("serialized install manifest mismatch (-want +got):\n%s", diff)
		}
		outcomes[response.result.outcome]++
	}
	wantOutcomes := map[trustedReleaseInstallOutcome]int{
		trustedReleaseInstallInstalled: 1,
		trustedReleaseInstallUnchanged: 1,
	}
	if diff := cmp.Diff(wantOutcomes, outcomes); diff != "" {
		t.Errorf("serialized install outcomes mismatch (-want +got):\n%s", diff)
	}
	assert.Equal(t, int32(2), candidateCalls.Load())
	assertInstalledTrustedReleaseMatchesArchive(t, fixture)
	assertNoTrustedReleaseCandidatesForInstallTest(t, fixture)
	assertPreparedReleaseSlotReacquirable(t, fixture.instances, fixture.store.slot)
}

func TestReleaseAuthorityInstallOrdersLockAndDescriptorOwnership(t *testing.T) {
	fixture := newTrustedReleaseInstallFixture(t)
	events := make([]string, 0, 12)
	var eventMu sync.Mutex
	appendEvent := func(event string) {
		eventMu.Lock()
		events = append(events, event)
		eventMu.Unlock()
	}

	realChecksumTransport := fixture.authority.checksumAuthority.client.Transport
	fixture.authority.checksumAuthority.client.Transport = trustedReleaseRoundTripFunc(
		func(request *http.Request) (*http.Response, error) {
			appendEvent("checksum")
			return realChecksumTransport.RoundTrip(request)
		},
	)
	realOpen := fixture.authority.storeDeps.open
	fixture.authority.storeDeps.open = func(ctx context.Context, name string, flags int, mode uint32) (int, error) {
		appendEvent("store")
		return realOpen(ctx, name, flags, mode)
	}
	realOpenAt := fixture.authority.storeDeps.openAt
	realClose := fixture.authority.storeDeps.close
	archiveFD := -1
	binariesFD := -1
	architectureFD := -1
	candidateNamed := false
	locked := false
	fixture.authority.storeDeps.openAt = func(
		ctx context.Context, parentFD int, name string, flags int, mode uint32,
	) (int, error) {
		fd, err := realOpenAt(ctx, parentFD, name, flags, mode)
		if err == nil && flags == trustedReleaseArchiveStageFlags {
			archiveFD = fd
			appendEvent("archive-create")
		}
		if err == nil && name == "binaries" {
			binariesFD = fd
		}
		if err == nil && name == fixture.store.slot.architecture {
			architectureFD = fd
			appendEvent("architecture")
		}
		return fd, err
	}
	fixture.authority.storeDeps.close = func(ctx context.Context, fd int) error {
		if candidateNamed {
			assert.True(t, locked, "candidate and architecture cleanup must remain under the slot lock")
		}
		if fd == archiveFD {
			appendEvent("archive-close")
		}
		if fd == binariesFD {
			appendEvent("writer-close")
		}
		if fd == architectureFD {
			appendEvent("architecture-close")
		}
		return realClose(ctx, fd)
	}
	realFchmod := fixture.authority.storeDeps.fchmod
	finalizedCount := 0
	fixture.authority.storeDeps.fchmod = func(ctx context.Context, fd int, mode uint32) error {
		err := realFchmod(ctx, fd, mode)
		if err == nil && mode == trustedReleaseStoreExecutableMode {
			finalizedCount++
			if finalizedCount == 2 {
				appendEvent("finalized")
			}
		}
		return err
	}
	realFlock := fixture.instances.deps.flock
	fixture.instances.deps.flock = func(ctx context.Context, fd int, how int) error {
		err := realFlock(ctx, fd, how)
		if err == nil && how == unix.LOCK_EX|unix.LOCK_NB && !locked {
			locked = true
			appendEvent("slot-lock")
		}
		if err == nil && how == unix.LOCK_UN && locked {
			appendEvent("slot-unlock")
			locked = false
		}
		return err
	}
	fixture.authority.storeDeps.candidateName = func(context.Context, releaseSlot) (string, error) {
		assert.True(t, locked)
		candidateNamed = true
		appendEvent("candidate")
		prefix, err := trustedReleaseCandidatePrefix(fixture.store.slot)
		return prefix + "00000000000000000000000000000000.tmp", err
	}
	realRename := fixture.authority.storeDeps.renameCandidateNoReplace
	fixture.authority.storeDeps.renameCandidateNoReplace = func(
		ctx context.Context, parentFD int, source string, target string,
	) error {
		assert.True(t, locked)
		appendEvent("commit")
		return realRename(ctx, parentFD, source, target)
	}

	result, err := fixture.authority.install(
		t.Context(), fixture.store.slot, trustedReleaseInstallAbsentOnly, fixture.callerArchive(),
	)
	require.NoError(t, err)
	assert.Equal(t, trustedReleaseInstallInstalled, result.outcome)
	want := []string{
		"checksum", "store", "archive-create", "archive-close", "finalized", "slot-lock", "architecture",
		"writer-close", "candidate", "commit", "architecture-close", "slot-unlock",
	}
	if diff := cmp.Diff(want, events); diff != "" {
		t.Errorf("install ownership order mismatch (-want +got):\n%s", diff)
	}
	assertNoTrustedReleaseCandidatesForInstallTest(t, fixture)
	assertPreparedReleaseSlotReacquirable(t, fixture.instances, fixture.store.slot)
}

func TestReleaseAuthorityInstallBalancesStoreDescriptors(t *testing.T) {
	fixture := newTrustedReleaseInstallFixture(t)
	active := make(map[int]struct{})
	var activeMu sync.Mutex
	realOpen := fixture.authority.storeDeps.open
	realOpenAt := fixture.authority.storeDeps.openAt
	realClose := fixture.authority.storeDeps.close
	recordOpen := func(fd int, err error) {
		if err != nil {
			return
		}
		activeMu.Lock()
		defer activeMu.Unlock()
		if _, exists := active[fd]; exists {
			t.Errorf("descriptor %d reopened while still active", fd)
		}
		active[fd] = struct{}{}
	}
	fixture.authority.storeDeps.open = func(ctx context.Context, name string, flags int, mode uint32) (int, error) {
		fd, err := realOpen(ctx, name, flags, mode)
		recordOpen(fd, err)
		return fd, err
	}
	fixture.authority.storeDeps.openAt = func(
		ctx context.Context, parentFD int, name string, flags int, mode uint32,
	) (int, error) {
		fd, err := realOpenAt(ctx, parentFD, name, flags, mode)
		recordOpen(fd, err)
		return fd, err
	}
	fixture.authority.storeDeps.close = func(ctx context.Context, fd int) error {
		activeMu.Lock()
		if _, exists := active[fd]; !exists {
			t.Errorf("descriptor %d closed without one active open", fd)
		} else {
			delete(active, fd)
		}
		activeMu.Unlock()
		return realClose(ctx, fd)
	}

	result, err := fixture.authority.install(
		t.Context(), fixture.store.slot, trustedReleaseInstallAbsentOnly, fixture.callerArchive(),
	)
	require.NoError(t, err)
	assert.Equal(t, trustedReleaseInstallInstalled, result.outcome)
	activeMu.Lock()
	defer activeMu.Unlock()
	assert.Empty(t, active)
}

func installCallerArchiveForFaultTest(fixture *trustedReleaseInstallFixture) *trustedReleaseCallerArchive {
	return fixture.callerArchive()
}

func assertNoTrustedReleaseCandidatesForInstallTest(t *testing.T, fixture *trustedReleaseInstallFixture) {
	t.Helper()

	entries, err := os.ReadDir(fixture.store.architecturePath)
	require.NoError(t, err)
	prefix, err := trustedReleaseCandidatePrefix(fixture.store.slot)
	require.NoError(t, err)
	for _, entry := range entries {
		assert.False(t, strings.HasPrefix(entry.Name(), prefix))
	}
}

func trustedReleaseCandidatePathsForInstallTest(
	t *testing.T,
	fixture *trustedReleaseInstallFixture,
) []string {
	t.Helper()

	prefix, err := trustedReleaseCandidatePrefix(fixture.store.slot)
	require.NoError(t, err)
	matches, err := filepath.Glob(filepath.Join(fixture.store.architecturePath, prefix+"*"))
	require.NoError(t, err)
	return matches
}

func requireInstallSignal(t *testing.T, ctx context.Context, signal <-chan struct{}, description string) {
	t.Helper()

	select {
	case <-signal:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for %s: %v", description, ctx.Err())
	}
}
