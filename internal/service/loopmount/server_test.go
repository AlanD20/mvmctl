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

	"mvmctl/pkg/errs"
)

func TestServerProvisionVMReturnsValidatedResultAndReleasesAuthorityInOrder(t *testing.T) {
	t.Parallel()

	request := testVMProvisionRequest()
	want := VMProvisionResult{
		VMID:         request.VMID,
		Filesystem:   request.Filesystem,
		RootfsBytes:  request.ExpectedRootfsBytes,
		FilesWritten: 3,
		CommandsRun:  2,
	}
	events := &serverEventLog{}
	fixture := newRootfsAuthorityFixture(t, []string{"srv", "cache", "vms", request.VMID, "rootfs.img"})
	rootfsAuthority := fixture.deps()
	closeRootfs := rootfsAuthority.close
	rootfsAuthority.close = func(ctx context.Context, fd int) error {
		events.append("rootfs")
		return closeRootfs(ctx, fd)
	}
	effectLease := &fakeVMProvisionEffectLease{events: events}
	effects := &fakeServerRootfsEffects{
		provisionOutcome: vmProvisionEffectOutcome{result: want, lease: effectLease},
	}
	locks := &fakeServerResourceLocks{
		lease: &fakeServerResourceLockLease{events: events},
	}
	server := &Server{rootfsAuthority: rootfsAuthority, effects: effects, locks: locks}

	got, err := server.ProvisionVM(t.Context(), testRootfsCaller(), request)
	require.NoError(t, err)

	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ProvisionVM() result mismatch (-want +got):\n%s", diff)
	}
	wantEvents := []string{"effect", "rootfs", "rootfs", "rootfs", "rootfs", "rootfs", "rootfs", "lock"}
	if diff := cmp.Diff(wantEvents, events.snapshot()); diff != "" {
		t.Errorf("ProvisionVM() cleanup order mismatch (-want +got):\n%s", diff)
	}
}

func TestServerImageOperationsUseActionSpecificAccessAndOutcomes(t *testing.T) {
	t.Parallel()

	t.Run("inspection retains read-only authority", func(t *testing.T) {
		request := testImageInspectRequest()
		want := ImageInspectResult{ImageID: request.ImageID, OSType: "ubuntu"}
		events := &serverEventLog{}
		fixture := newRootfsAuthorityFixture(
			t,
			[]string{"srv", "cache", "images", "staging", request.ImageID, "rootfs.img"},
		)
		rootfsAuthority := fixture.deps()
		closeRootfs := rootfsAuthority.close
		rootfsAuthority.close = func(ctx context.Context, fd int) error {
			events.append("rootfs")
			return closeRootfs(ctx, fd)
		}
		effects := &fakeServerRootfsEffects{
			inspectOutcome: imageInspectEffectOutcome{
				result: want,
				lease:  &fakeImageInspectEffectLease{events: events},
			},
		}
		locks := &fakeServerResourceLocks{lease: &fakeServerResourceLockLease{events: events}}
		server := &Server{rootfsAuthority: rootfsAuthority, effects: effects, locks: locks}

		got, err := server.InspectImage(t.Context(), testRootfsCaller(), request)
		require.NoError(t, err)

		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("InspectImage() result mismatch (-want +got):\n%s", diff)
		}
		assert.Equal(t, rootfsReadOnly, effects.inspectAccess)
		assert.Equal(t, rootfsImageInspectSelection, locks.selection.kind)
		assert.Equal(t, request.ImageID, locks.selection.resourceID)
		assert.Equal(t, request.ExpectedImageBytes, locks.selection.expectedBytes)
		if diff := cmp.Diff(testRootfsCaller(), locks.caller); diff != "" {
			t.Errorf("InspectImage() lock caller mismatch (-want +got):\n%s", diff)
		}
		wantEvents := []string{
			"effect", "rootfs", "rootfs", "rootfs", "rootfs", "rootfs", "rootfs", "rootfs", "lock",
		}
		if diff := cmp.Diff(wantEvents, events.snapshot()); diff != "" {
			t.Errorf("InspectImage() cleanup order mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("optimization retains read-write authority", func(t *testing.T) {
		request := testImageOptimizeRequest()
		want := ImageOptimizeResult{
			ImageID: request.ImageID, Filesystem: request.Filesystem, ImageBytes: request.ExpectedImageBytes,
		}
		events := &serverEventLog{}
		fixture := newRootfsAuthorityFixture(
			t,
			[]string{"srv", "cache", "images", "staging", request.ImageID, "rootfs.img"},
		)
		rootfsAuthority := fixture.deps()
		closeRootfs := rootfsAuthority.close
		rootfsAuthority.close = func(ctx context.Context, fd int) error {
			events.append("rootfs")
			return closeRootfs(ctx, fd)
		}
		effects := &fakeServerRootfsEffects{
			optimizeOutcome: imageOptimizeEffectOutcome{
				result: want,
				lease:  &fakeImageOptimizeEffectLease{events: events},
			},
		}
		locks := &fakeServerResourceLocks{lease: &fakeServerResourceLockLease{events: events}}
		server := &Server{rootfsAuthority: rootfsAuthority, effects: effects, locks: locks}

		got, err := server.OptimizeImage(t.Context(), testRootfsCaller(), request)
		require.NoError(t, err)

		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("OptimizeImage() result mismatch (-want +got):\n%s", diff)
		}
		assert.Equal(t, rootfsReadWrite, effects.optimizeAccess)
		assert.Equal(t, rootfsImageOptimizeSelection, locks.selection.kind)
		wantEvents := []string{
			"effect", "rootfs", "rootfs", "rootfs", "rootfs", "rootfs", "rootfs", "rootfs", "lock",
		}
		if diff := cmp.Diff(wantEvents, events.snapshot()); diff != "" {
			t.Errorf("OptimizeImage() cleanup order mismatch (-want +got):\n%s", diff)
		}
	})
}

// Rationale: An effect result is untrusted until it is bound back to the admitted request. Cleanup must still run
// after the server rejects a semantically invalid result.
func TestServerRejectsInvalidEffectResultBeforePublishingIt(t *testing.T) {
	t.Parallel()

	request := testVMProvisionRequest()
	events := &serverEventLog{}
	fixture := newRootfsAuthorityFixture(t, []string{"srv", "cache", "vms", request.VMID, "rootfs.img"})
	rootfsAuthority := fixture.deps()
	closeRootfs := rootfsAuthority.close
	rootfsAuthority.close = func(ctx context.Context, fd int) error {
		events.append("rootfs")
		return closeRootfs(ctx, fd)
	}
	effects := &fakeServerRootfsEffects{
		provisionOutcome: vmProvisionEffectOutcome{
			result: VMProvisionResult{
				VMID: "ffffffffffffffffffffffffffffffff", Filesystem: request.Filesystem,
				RootfsBytes: request.ExpectedRootfsBytes,
			},
			lease: &fakeVMProvisionEffectLease{events: events},
		},
	}
	locks := &fakeServerResourceLocks{lease: &fakeServerResourceLockLease{events: events}}
	server := &Server{rootfsAuthority: rootfsAuthority, effects: effects, locks: locks}

	got, err := server.ProvisionVM(t.Context(), testRootfsCaller(), request)
	require.Error(t, err)

	if diff := cmp.Diff(VMProvisionResult{}, got); diff != "" {
		t.Errorf("invalid effect result must not be published (-want +got):\n%s", diff)
	}
	assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
	wantEvents := []string{"effect", "rootfs", "rootfs", "rootfs", "rootfs", "rootfs", "rootfs", "lock"}
	if diff := cmp.Diff(wantEvents, events.snapshot()); diff != "" {
		t.Errorf("invalid-result cleanup order mismatch (-want +got):\n%s", diff)
	}
}

// Rationale: Once effects begin, their DomainError is the authoritative failure. Later cleanup diagnostics may add
// bounded partial-state evidence but must not erase its classification, entity, message, operation, or details.
func TestServerPreservesPrimaryDomainErrorAcrossCancellationAndCleanupFailures(t *testing.T) {
	t.Parallel()

	request := testVMProvisionRequest()
	events := &serverEventLog{}
	fixture := newRootfsAuthorityFixture(t, []string{"srv", "cache", "vms", request.VMID, "rootfs.img"})
	rootfsAuthority := fixture.deps()
	closeRootfs := rootfsAuthority.close
	rootfsAuthority.close = func(ctx context.Context, fd int) error {
		events.append("rootfs")
		return closeRootfs(ctx, fd)
	}
	longCause := errors.New(strings.Repeat("cleanup-failure-", 80))
	fixture.closeError[fixture.rootFD+5] = longCause
	primary := errs.New(
		errs.CodeImageError,
		"primary effect failure",
		errs.WithClass(errs.ClassRetryable),
		errs.WithEntity(request.VMID),
		errs.WithDetails(map[string]any{"original": "retained"}),
	)
	ctx, cancel := context.WithCancel(context.Background())
	effectLease := &fakeVMProvisionEffectLease{events: events, err: longCause}
	lockLease := &fakeServerResourceLockLease{events: events, err: longCause}
	effects := &fakeServerRootfsEffects{
		provisionOutcome: vmProvisionEffectOutcome{
			result: VMProvisionResult{},
			lease:  effectLease,
		},
		provisionErr: primary,
		cancel:       cancel,
	}
	locks := &fakeServerResourceLocks{lease: lockLease}
	server := &Server{rootfsAuthority: rootfsAuthority, effects: effects, locks: locks}

	_, err := server.ProvisionVM(ctx, testRootfsCaller(), request)
	require.Error(t, err)

	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, primary.Code, domainErr.Code)
	assert.Equal(t, primary.Class, domainErr.Class)
	assert.Equal(t, primary.Entity, domainErr.Entity)
	assert.Equal(t, primary.Message, domainErr.Message)
	assert.Equal(t, primary.Op, domainErr.Op)
	assert.Equal(t, "retained", domainErr.Details["original"])
	for _, annotation := range []string{"effect_cleanup", "rootfs_release", "resource_lock_release"} {
		assert.Equal(t, true, domainErr.Details["loopmount_"+annotation+"_failed"])
		detail, ok := domainErr.Details["loopmount_"+annotation+"_detail"].(string)
		require.True(t, ok)
		assert.LessOrEqual(t, len(detail), maxServerCleanupDetailBytes)
	}
	if diff := cmp.Diff(map[string]any{"original": "retained"}, primary.Details); diff != "" {
		t.Errorf("primary error details mutated (-want +got):\n%s", diff)
	}
	assert.NoError(t, effectLease.contextErr)
	assert.NoError(t, lockLease.contextErr)
	for _, closeCall := range fixture.closeCalls {
		assert.NoError(t, closeCall.contextErr)
	}
	wantEvents := []string{"effect", "rootfs", "rootfs", "rootfs", "rootfs", "rootfs", "rootfs", "lock"}
	if diff := cmp.Diff(wantEvents, events.snapshot()); diff != "" {
		t.Errorf("failed-operation cleanup order mismatch (-want +got):\n%s", diff)
	}
}

// Rationale: A missing action-specific cleanup lease means the server cannot prove rollback. It must fail closed while
// still releasing rootfs and resource-lock authority.
func TestServerFailsClosedWithoutEffectCleanupAuthority(t *testing.T) {
	t.Parallel()

	request := testImageInspectRequest()
	events := &serverEventLog{}
	fixture := newRootfsAuthorityFixture(
		t,
		[]string{"srv", "cache", "images", "staging", request.ImageID, "rootfs.img"},
	)
	rootfsAuthority := fixture.deps()
	closeRootfs := rootfsAuthority.close
	rootfsAuthority.close = func(ctx context.Context, fd int) error {
		events.append("rootfs")
		return closeRootfs(ctx, fd)
	}
	effects := &fakeServerRootfsEffects{
		inspectOutcome: imageInspectEffectOutcome{
			result: ImageInspectResult{ImageID: request.ImageID, OSType: "ubuntu"},
		},
	}
	locks := &fakeServerResourceLocks{lease: &fakeServerResourceLockLease{events: events}}
	server := &Server{rootfsAuthority: rootfsAuthority, effects: effects, locks: locks}

	_, err := server.InspectImage(t.Context(), testRootfsCaller(), request)
	require.Error(t, err)

	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeLoopMountError, domainErr.Code)
	assert.Equal(t, true, domainErr.Details["loopmount_effect_cleanup_failed"])
	wantEvents := []string{"rootfs", "rootfs", "rootfs", "rootfs", "rootfs", "rootfs", "rootfs", "lock"}
	if diff := cmp.Diff(wantEvents, events.snapshot()); diff != "" {
		t.Errorf("missing-effect-authority cleanup order mismatch (-want +got):\n%s", diff)
	}
}

func TestServerRejectsInvalidRequestBeforeAcquiringAuthority(t *testing.T) {
	t.Parallel()

	request := testVMProvisionRequest()
	request.VMID = "invalid"
	fixture := newRootfsAuthorityFixture(t, []string{"srv", "cache", "vms", testVMID, "rootfs.img"})
	locks := &fakeServerResourceLocks{lease: &fakeServerResourceLockLease{events: &serverEventLog{}}}
	server := &Server{
		rootfsAuthority: fixture.deps(),
		effects:         &fakeServerRootfsEffects{},
		locks:           locks,
	}

	_, err := server.ProvisionVM(t.Context(), testRootfsCaller(), request)
	require.Error(t, err)

	assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
	assert.Equal(t, 0, locks.acquireCount)
	assert.Empty(t, fixture.openCalls)
}

type serverEventLog struct {
	mu     sync.Mutex
	events []string
}

func (log *serverEventLog) append(event string) {
	log.mu.Lock()
	defer log.mu.Unlock()
	log.events = append(log.events, event)
}

func (log *serverEventLog) snapshot() []string {
	log.mu.Lock()
	defer log.mu.Unlock()
	return append([]string(nil), log.events...)
}

type fakeServerRootfsEffects struct {
	provisionOutcome vmProvisionEffectOutcome
	inspectOutcome   imageInspectEffectOutcome
	optimizeOutcome  imageOptimizeEffectOutcome
	provisionErr     error
	inspectErr       error
	optimizeErr      error
	cancel           context.CancelFunc
	provisionAccess  rootfsAccess
	inspectAccess    rootfsAccess
	optimizeAccess   rootfsAccess
}

func (effects *fakeServerRootfsEffects) ProvisionVM(
	_ context.Context,
	lease *rootfsLease,
	_ VMProvisionRequest,
) (vmProvisionEffectOutcome, error) {
	effects.provisionAccess = lease.access
	if effects.cancel != nil {
		effects.cancel()
	}
	return effects.provisionOutcome, effects.provisionErr
}

func (effects *fakeServerRootfsEffects) InspectImage(
	_ context.Context,
	lease *rootfsLease,
	_ ImageInspectRequest,
) (imageInspectEffectOutcome, error) {
	effects.inspectAccess = lease.access
	return effects.inspectOutcome, effects.inspectErr
}

func (effects *fakeServerRootfsEffects) OptimizeImage(
	_ context.Context,
	lease *rootfsLease,
	_ ImageOptimizeRequest,
) (imageOptimizeEffectOutcome, error) {
	effects.optimizeAccess = lease.access
	return effects.optimizeOutcome, effects.optimizeErr
}

type fakeVMProvisionEffectLease struct {
	events     *serverEventLog
	err        error
	contextErr error
}

func (lease *fakeVMProvisionEffectLease) CleanupProvisionVM(ctx context.Context) error {
	lease.events.append("effect")
	lease.contextErr = ctx.Err()
	return lease.err
}

type fakeImageInspectEffectLease struct {
	events *serverEventLog
}

func (lease *fakeImageInspectEffectLease) CleanupInspectImage(context.Context) error {
	lease.events.append("effect")
	return nil
}

type fakeImageOptimizeEffectLease struct {
	events *serverEventLog
}

func (lease *fakeImageOptimizeEffectLease) CleanupOptimizeImage(context.Context) error {
	lease.events.append("effect")
	return nil
}

type fakeServerResourceLocks struct {
	lease        rootfsResourceLockLease
	acquireErr   error
	acquireCount int
	caller       Caller
	selection    rootfsSelection
}

func (locks *fakeServerResourceLocks) Acquire(
	_ context.Context,
	caller Caller,
	selection rootfsSelection,
) (rootfsResourceLockLease, error) {
	locks.acquireCount++
	locks.caller = caller
	locks.selection = selection
	return locks.lease, locks.acquireErr
}

type fakeServerResourceLockLease struct {
	events     *serverEventLog
	err        error
	contextErr error
}

func (lease *fakeServerResourceLockLease) Release(ctx context.Context) error {
	lease.events.append("lock")
	lease.contextErr = ctx.Err()
	return lease.err
}
