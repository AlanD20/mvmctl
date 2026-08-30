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
		provisionOutcome: vmProvisionEffectOutcome{
			state:        vmProvisionEffectStateModified,
			filesystem:   want.Filesystem,
			rootfsBytes:  want.RootfsBytes,
			filesWritten: want.FilesWritten,
			commandsRun:  want.CommandsRun,
			lease:        effectLease,
		},
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
	assert.Equal(t, request.ExpectedRootfsBytes, effects.provisionAdmittedBytes)
	assert.Equal(t, request.Filesystem, effects.provisionFilesystem)
	if diff := cmp.Diff(request.Plan, effects.provisionPlan); diff != "" {
		t.Errorf("ProvisionVM() plan mismatch (-want +got):\n%s", diff)
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
				state:  imageInspectEffectStateUnchanged,
				osType: want.OSType,
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
		assert.Equal(t, request.Filesystem, effects.inspectFilesystem)
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
		optimizeEffects := &fakeServerImageOptimizeEffects{
			optimizeOutcome: imageOptimizeEffectOutcome{
				state:      imageOptimizeEffectStateModified,
				filesystem: want.Filesystem,
				imageBytes: want.ImageBytes,
				lease:      &fakeImageOptimizeEffectLease{events: events},
			},
		}
		locks := &fakeServerResourceLocks{lease: &fakeServerResourceLockLease{events: events}}
		server := &Server{rootfsAuthority: rootfsAuthority, optimizeEffects: optimizeEffects, locks: locks}

		got, err := server.OptimizeImage(t.Context(), testRootfsCaller(), request)
		require.NoError(t, err)

		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("OptimizeImage() result mismatch (-want +got):\n%s", diff)
		}
		assert.Equal(t, rootfsReadWrite, optimizeEffects.optimizeAccess)
		assert.Equal(t, request.Filesystem, optimizeEffects.optimizeFilesystem)
		if diff := cmp.Diff(request.Plan, optimizeEffects.optimizePlan); diff != "" {
			t.Errorf("OptimizeImage() plan mismatch (-want +got):\n%s", diff)
		}
		assert.Equal(t, rootfsImageOptimizeSelection, locks.selection.kind)
		wantEvents := []string{
			"effect", "rootfs", "rootfs", "rootfs", "rootfs", "rootfs", "rootfs", "rootfs", "lock",
		}
		if diff := cmp.Diff(wantEvents, events.snapshot()); diff != "" {
			t.Errorf("OptimizeImage() cleanup order mismatch (-want +got):\n%s", diff)
		}
	})
}

// Rationale: Production-capable provisioning/inspection and deferred image optimization are separate privileges.
// Missing one effect seam must fail only its named action before lock or rootfs authority is acquired.
func TestServerRejectsMissingActionEffectDependencyBeforeAuthority(t *testing.T) {
	t.Parallel()

	t.Run("VM provision requires production-capable effects", func(t *testing.T) {
		fixture := newRootfsAuthorityFixture(t, []string{"srv", "cache", "vms", testVMID, "rootfs.img"})
		locks := &fakeServerResourceLocks{lease: &fakeServerResourceLockLease{events: &serverEventLog{}}}
		server := &Server{
			rootfsAuthority: fixture.deps(), optimizeEffects: &fakeServerImageOptimizeEffects{}, locks: locks,
		}

		got, err := server.ProvisionVM(t.Context(), testRootfsCaller(), testVMProvisionRequest())
		require.Error(t, err)

		if diff := cmp.Diff(VMProvisionResult{}, got); diff != "" {
			t.Errorf("missing provision effects published a result (-want +got):\n%s", diff)
		}
		assert.Equal(t, errs.CodeLoopMountError, errs.AsDomainError(err).Code)
		assert.Equal(t, 0, locks.acquireCount)
		assert.Empty(t, fixture.openCalls)
	})

	t.Run("image inspection requires production-capable effects", func(t *testing.T) {
		fixture := newRootfsAuthorityFixture(t, []string{"srv", "cache", "vms", testVMID, "rootfs.img"})
		locks := &fakeServerResourceLocks{lease: &fakeServerResourceLockLease{events: &serverEventLog{}}}
		server := &Server{
			rootfsAuthority: fixture.deps(), optimizeEffects: &fakeServerImageOptimizeEffects{}, locks: locks,
		}

		got, err := server.InspectImage(t.Context(), testRootfsCaller(), testImageInspectRequest())
		require.Error(t, err)

		if diff := cmp.Diff(ImageInspectResult{}, got); diff != "" {
			t.Errorf("missing inspection effects published a result (-want +got):\n%s", diff)
		}
		assert.Equal(t, errs.CodeLoopMountError, errs.AsDomainError(err).Code)
		assert.Equal(t, 0, locks.acquireCount)
		assert.Empty(t, fixture.openCalls)
	})

	t.Run("image optimization requires only its deferred effect", func(t *testing.T) {
		fixture := newRootfsAuthorityFixture(t, []string{"srv", "cache", "vms", testVMID, "rootfs.img"})
		locks := &fakeServerResourceLocks{lease: &fakeServerResourceLockLease{events: &serverEventLog{}}}
		server := &Server{rootfsAuthority: fixture.deps(), effects: &fakeServerRootfsEffects{}, locks: locks}

		got, err := server.OptimizeImage(t.Context(), testRootfsCaller(), testImageOptimizeRequest())
		require.Error(t, err)

		if diff := cmp.Diff(ImageOptimizeResult{}, got); diff != "" {
			t.Errorf("missing optimization effects published a result (-want +got):\n%s", diff)
		}
		assert.Equal(t, errs.CodeLoopMountError, errs.AsDomainError(err).Code)
		assert.Equal(t, 0, locks.acquireCount)
		assert.Empty(t, fixture.openCalls)
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
			state:       vmProvisionEffectStateModified,
			filesystem:  FilesystemExt3,
			rootfsBytes: request.ExpectedRootfsBytes,
			lease:       &fakeVMProvisionEffectLease{events: events},
		},
	}
	locks := &fakeServerResourceLocks{lease: &fakeServerResourceLockLease{events: events}}
	server := &Server{rootfsAuthority: rootfsAuthority, effects: effects, locks: locks}

	got, err := server.ProvisionVM(t.Context(), testRootfsCaller(), request)
	require.Error(t, err)

	if diff := cmp.Diff(VMProvisionResult{}, got); diff != "" {
		t.Errorf("invalid effect result must not be published (-want +got):\n%s", diff)
	}
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeValidationFailed, domainErr.Code)
	assertServerEffectEvidence(t, domainErr, rootfsEffectEvidence{rootfsModified: true})
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
	effectCleanupCause := errors.New(strings.Repeat("effect-cleanup-failure-", 40))
	rootfsCleanupCause := errors.New(strings.Repeat("rootfs-cleanup-failure-", 40))
	lockCleanupCause := errors.New(strings.Repeat("lock-cleanup-failure-", 40))
	fixture.closeError[fixture.rootFD+5] = rootfsCleanupCause
	primary := errs.New(
		errs.CodeImageError,
		"primary effect failure",
		errs.WithClass(errs.ClassRetryable),
		errs.WithEntity(request.VMID),
		errs.WithDetails(map[string]any{"original": "retained"}),
	)
	ctx, cancel := context.WithCancel(context.WithValue(
		context.Background(),
		rootfsCleanupContextKey{},
		"server-cleanup",
	))
	effectLease := &fakeVMProvisionEffectLease{events: events, err: effectCleanupCause}
	lockLease := &fakeServerResourceLockLease{events: events, err: lockCleanupCause}
	effects := &fakeServerRootfsEffects{
		provisionOutcome: vmProvisionEffectOutcome{
			state: vmProvisionEffectStateOutcomeUnknown,
			lease: effectLease,
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
	assert.ErrorIs(t, err, effectCleanupCause)
	assert.ErrorIs(t, err, rootfsCleanupCause)
	assert.ErrorIs(t, err, lockCleanupCause)
	assertServerEffectEvidence(t, domainErr, rootfsEffectEvidence{
		rootfsModified: true, durabilityUncertain: true, outcomeUnknown: true,
	})
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
	assert.Equal(t, "server-cleanup", effectLease.contextMark)
	assert.NoError(t, lockLease.contextErr)
	assert.Equal(t, "server-cleanup", lockLease.contextMark)
	for _, closeCall := range fixture.closeCalls {
		assert.NoError(t, closeCall.contextErr)
		assert.Equal(t, "server-cleanup", closeCall.contextMark)
	}
	wantEvents := []string{"effect", "rootfs", "rootfs", "rootfs", "rootfs", "rootfs", "rootfs", "lock"}
	if diff := cmp.Diff(wantEvents, events.snapshot()); diff != "" {
		t.Errorf("failed-operation cleanup order mismatch (-want +got):\n%s", diff)
	}
}

// Rationale: An effect error is incomplete without receiver-validated mutation evidence. Plain errors are normalized,
// while DomainError identity is retained; neither path may accept effect-supplied booleans or generic detail maps.
func TestServerEffectErrorsCarryClosedMutationEvidence(t *testing.T) {
	t.Parallel()

	t.Run("plain VM error after modification", func(t *testing.T) {
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
				state: vmProvisionEffectStateModified,
				lease: &fakeVMProvisionEffectLease{events: events},
			},
			provisionErr: errors.New("plain effect failure"),
		}
		server := &Server{
			rootfsAuthority: rootfsAuthority,
			effects:         effects,
			locks: &fakeServerResourceLocks{
				lease: &fakeServerResourceLockLease{events: events},
			},
		}

		_, err := server.ProvisionVM(t.Context(), testRootfsCaller(), request)
		require.Error(t, err)

		domainErr := errs.AsDomainError(err)
		require.NotNil(t, domainErr)
		assert.Equal(t, errs.CodeLoopMountError, domainErr.Code)
		assertServerEffectEvidence(t, domainErr, rootfsEffectEvidence{rootfsModified: true})
		wantEvents := []string{"effect", "rootfs", "rootfs", "rootfs", "rootfs", "rootfs", "rootfs", "lock"}
		if diff := cmp.Diff(wantEvents, events.snapshot()); diff != "" {
			t.Errorf("plain-error cleanup order mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("DomainError after durable image replacement uncertainty", func(t *testing.T) {
		request := testImageOptimizeRequest()
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
		primary := errs.New(
			errs.CodeImageError,
			"replacement durability failed",
			errs.WithClass(errs.ClassRetryable),
			errs.WithEntity(request.ImageID),
			errs.WithDetails(map[string]any{"original": "retained"}),
		)
		optimizeEffects := &fakeServerImageOptimizeEffects{
			optimizeOutcome: imageOptimizeEffectOutcome{
				state: imageOptimizeEffectStateImageReplacedDurabilityUncertain,
				lease: &fakeImageOptimizeEffectLease{events: events},
			},
			optimizeErr: primary,
		}
		server := &Server{
			rootfsAuthority: rootfsAuthority,
			optimizeEffects: optimizeEffects,
			locks: &fakeServerResourceLocks{
				lease: &fakeServerResourceLockLease{events: events},
			},
		}

		_, err := server.OptimizeImage(t.Context(), testRootfsCaller(), request)
		require.Error(t, err)

		domainErr := errs.AsDomainError(err)
		require.NotNil(t, domainErr)
		assert.Equal(t, primary.Code, domainErr.Code)
		assert.Equal(t, primary.Class, domainErr.Class)
		assert.Equal(t, primary.Entity, domainErr.Entity)
		assert.Equal(t, primary.Message, domainErr.Message)
		assert.Equal(t, "retained", domainErr.Details["original"])
		assertServerEffectEvidence(t, domainErr, rootfsEffectEvidence{
			rootfsModified: true, imageReplaced: true, durabilityUncertain: true,
		})
		wantEvents := []string{
			"effect", "rootfs", "rootfs", "rootfs", "rootfs", "rootfs", "rootfs", "rootfs", "lock",
		}
		if diff := cmp.Diff(wantEvents, events.snapshot()); diff != "" {
			t.Errorf("replacement-error cleanup order mismatch (-want +got):\n%s", diff)
		}
	})
}

// Rationale: Unknown or missing effect state cannot authorize a success response even when result fields look valid.
// The server must fail closed and expose conservative evidence derived from the action-specific closed state.
func TestServerRejectsUnknownOrMissingEffectStateOnSuccess(t *testing.T) {
	t.Parallel()

	t.Run("VM outcome unknown", func(t *testing.T) {
		request := testVMProvisionRequest()
		events := &serverEventLog{}
		fixture := newRootfsAuthorityFixture(t, []string{"srv", "cache", "vms", request.VMID, "rootfs.img"})
		effects := &fakeServerRootfsEffects{
			provisionOutcome: vmProvisionEffectOutcome{
				state: vmProvisionEffectStateOutcomeUnknown,
				lease: &fakeVMProvisionEffectLease{events: events},
			},
		}
		server := &Server{
			rootfsAuthority: fixture.deps(),
			effects:         effects,
			locks: &fakeServerResourceLocks{
				lease: &fakeServerResourceLockLease{events: events},
			},
		}

		_, err := server.ProvisionVM(t.Context(), testRootfsCaller(), request)
		require.Error(t, err)

		domainErr := errs.AsDomainError(err)
		require.NotNil(t, domainErr)
		assert.Equal(t, errs.CodeLoopMountError, domainErr.Code)
		assertServerEffectEvidence(t, domainErr, rootfsEffectEvidence{
			rootfsModified: true, durabilityUncertain: true, outcomeUnknown: true,
		})
	})

	t.Run("inspection state missing", func(t *testing.T) {
		request := testImageInspectRequest()
		events := &serverEventLog{}
		fixture := newRootfsAuthorityFixture(
			t,
			[]string{"srv", "cache", "images", "staging", request.ImageID, "rootfs.img"},
		)
		effects := &fakeServerRootfsEffects{
			inspectOutcome: imageInspectEffectOutcome{
				osType: "ubuntu",
				lease:  &fakeImageInspectEffectLease{events: events},
			},
		}
		server := &Server{
			rootfsAuthority: fixture.deps(),
			effects:         effects,
			locks: &fakeServerResourceLocks{
				lease: &fakeServerResourceLockLease{events: events},
			},
		}

		_, err := server.InspectImage(t.Context(), testRootfsCaller(), request)
		require.Error(t, err)

		domainErr := errs.AsDomainError(err)
		require.NotNil(t, domainErr)
		assert.Equal(t, true, domainErr.Details["loopmount_effect_state_invalid"])
		assertServerEffectEvidence(t, domainErr, rootfsEffectEvidence{outcomeUnknown: true})
	})
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
			state:  imageInspectEffectStateUnchanged,
			osType: "ubuntu",
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
	assertServerEffectEvidence(t, domainErr, rootfsEffectEvidence{})
	wantEvents := []string{"rootfs", "rootfs", "rootfs", "rootfs", "rootfs", "rootfs", "rootfs", "lock"}
	if diff := cmp.Diff(wantEvents, events.snapshot()); diff != "" {
		t.Errorf("missing-effect-authority cleanup order mismatch (-want +got):\n%s", diff)
	}
}

// Rationale: A zero effect outcome carries neither an admissible state nor cleanup authority. Every action must reject
// it conservatively, publish no result, and still release the admitted rootfs and resource lock.
func TestServerRejectsZeroEffectOutcomes(t *testing.T) {
	t.Parallel()

	vmEffectErr := errors.New("zero VM effect outcome")
	tests := []struct {
		name             string
		kind             rootfsSelectionKind
		effectErr        error
		wantEvidence     rootfsEffectEvidence
		wantRootfsCloses int
	}{
		{
			name: "VM provision without effect error", kind: rootfsVMProvisionSelection,
			wantEvidence: rootfsEffectEvidence{
				rootfsModified: true, durabilityUncertain: true, outcomeUnknown: true,
			},
			wantRootfsCloses: 6,
		},
		{
			name: "VM provision with effect error", kind: rootfsVMProvisionSelection, effectErr: vmEffectErr,
			wantEvidence: rootfsEffectEvidence{
				rootfsModified: true, durabilityUncertain: true, outcomeUnknown: true,
			},
			wantRootfsCloses: 6,
		},
		{
			name: "image inspection without effect error", kind: rootfsImageInspectSelection,
			wantEvidence: rootfsEffectEvidence{outcomeUnknown: true}, wantRootfsCloses: 7,
		},
		{
			name: "image optimization without effect error", kind: rootfsImageOptimizeSelection,
			wantEvidence: rootfsEffectEvidence{
				rootfsModified: true, imageReplaced: true, durabilityUncertain: true, outcomeUnknown: true,
			},
			wantRootfsCloses: 7,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			events := &serverEventLog{}
			components := []string{"srv", "cache", "vms", testVMID, "rootfs.img"}
			if tc.kind != rootfsVMProvisionSelection {
				components = []string{"srv", "cache", "images", "staging", testImageID, "rootfs.img"}
			}
			fixture := newRootfsAuthorityFixture(t, components)
			rootfsAuthority := fixture.deps()
			closeRootfs := rootfsAuthority.close
			rootfsAuthority.close = func(ctx context.Context, fd int) error {
				events.append("rootfs")
				return closeRootfs(ctx, fd)
			}
			effects := &fakeServerRootfsEffects{}
			optimizeEffects := &fakeServerImageOptimizeEffects{}
			switch tc.kind {
			case rootfsVMProvisionSelection:
				effects.provisionErr = tc.effectErr
			case rootfsImageInspectSelection:
				effects.inspectErr = tc.effectErr
			case rootfsImageOptimizeSelection:
				optimizeEffects.optimizeErr = tc.effectErr
			default:
				t.Fatalf("unhandled test action %d", tc.kind)
			}
			lockLease := &fakeServerResourceLockLease{events: events}
			server := &Server{
				rootfsAuthority: rootfsAuthority,
				effects:         effects,
				optimizeEffects: optimizeEffects,
				locks:           &fakeServerResourceLocks{lease: lockLease},
			}

			var operationErr error
			switch tc.kind {
			case rootfsVMProvisionSelection:
				got, err := server.ProvisionVM(t.Context(), testRootfsCaller(), testVMProvisionRequest())
				operationErr = err
				if diff := cmp.Diff(VMProvisionResult{}, got); diff != "" {
					t.Errorf("zero VM outcome published a result (-want +got):\n%s", diff)
				}
				assert.Equal(t, 1, effects.provisionCallCount)
			case rootfsImageInspectSelection:
				got, err := server.InspectImage(t.Context(), testRootfsCaller(), testImageInspectRequest())
				operationErr = err
				if diff := cmp.Diff(ImageInspectResult{}, got); diff != "" {
					t.Errorf("zero inspection outcome published a result (-want +got):\n%s", diff)
				}
				assert.Equal(t, 1, effects.inspectCallCount)
			case rootfsImageOptimizeSelection:
				got, err := server.OptimizeImage(t.Context(), testRootfsCaller(), testImageOptimizeRequest())
				operationErr = err
				if diff := cmp.Diff(ImageOptimizeResult{}, got); diff != "" {
					t.Errorf("zero optimization outcome published a result (-want +got):\n%s", diff)
				}
				assert.Equal(t, 1, optimizeEffects.optimizeCallCount)
			}
			require.Error(t, operationErr)
			if tc.effectErr != nil {
				assert.ErrorIs(t, operationErr, tc.effectErr)
			}
			domainErr := errs.AsDomainError(operationErr)
			require.NotNil(t, domainErr)
			assert.Equal(t, true, domainErr.Details["loopmount_effect_state_invalid"])
			assert.Equal(t, true, domainErr.Details["loopmount_effect_cleanup_failed"])
			assertServerEffectEvidence(t, domainErr, tc.wantEvidence)
			assert.Len(t, fixture.closeCalls, tc.wantRootfsCloses)
			assert.Equal(t, 1, lockLease.releaseCount)
			wantEvents := make([]string, tc.wantRootfsCloses+1)
			for index := range tc.wantRootfsCloses {
				wantEvents[index] = "rootfs"
			}
			wantEvents[len(wantEvents)-1] = "lock"
			if diff := cmp.Diff(wantEvents, events.snapshot()); diff != "" {
				t.Errorf("zero-outcome cleanup order mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// Rationale: A lock provider returning neither a lease nor an error has failed to establish exclusion. The server must
// reject that missing authority before opening the rootfs or invoking a privileged effect.
func TestServerRejectsMissingResourceLockAuthorityBeforeAdmission(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		kind rootfsSelectionKind
	}{
		{name: "VM provision", kind: rootfsVMProvisionSelection},
		{name: "image inspection", kind: rootfsImageInspectSelection},
		{name: "image optimization", kind: rootfsImageOptimizeSelection},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRootfsAuthorityFixture(t, []string{"srv", "cache", "vms", testVMID, "rootfs.img"})
			effects := &fakeServerRootfsEffects{}
			optimizeEffects := &fakeServerImageOptimizeEffects{}
			locks := &fakeServerResourceLocks{}
			server := &Server{
				rootfsAuthority: fixture.deps(), effects: effects, optimizeEffects: optimizeEffects, locks: locks,
			}

			var operationErr error
			switch test.kind {
			case rootfsVMProvisionSelection:
				got, err := server.ProvisionVM(t.Context(), testRootfsCaller(), testVMProvisionRequest())
				operationErr = err
				if diff := cmp.Diff(VMProvisionResult{}, got); diff != "" {
					t.Errorf("missing VM lock published a result (-want +got):\n%s", diff)
				}
			case rootfsImageInspectSelection:
				got, err := server.InspectImage(t.Context(), testRootfsCaller(), testImageInspectRequest())
				operationErr = err
				if diff := cmp.Diff(ImageInspectResult{}, got); diff != "" {
					t.Errorf("missing inspection lock published a result (-want +got):\n%s", diff)
				}
			case rootfsImageOptimizeSelection:
				got, err := server.OptimizeImage(t.Context(), testRootfsCaller(), testImageOptimizeRequest())
				operationErr = err
				if diff := cmp.Diff(ImageOptimizeResult{}, got); diff != "" {
					t.Errorf("missing optimization lock published a result (-want +got):\n%s", diff)
				}
			default:
				t.Fatalf("unhandled test action %d", test.kind)
			}
			require.Error(t, operationErr)
			assert.Equal(t, errs.CodeLoopMountError, errs.AsDomainError(operationErr).Code)
			assert.Equal(t, 1, locks.acquireCount)
			assert.Equal(t, test.kind, locks.selection.kind)
			assert.Empty(t, fixture.openCalls)
			assert.Equal(t, 0, effects.provisionCallCount)
			assert.Equal(t, 0, effects.inspectCallCount)
			assert.Equal(t, 0, optimizeEffects.optimizeCallCount)
		})
	}
}

// Rationale: Lock acquisition may return both a lease and an error after partially acquiring authority. Every action
// must retain and release that lease exactly once before returning the unchanged acquisition error identity.
func TestServerReleasesPartialLockAcquisitionForEveryAction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		kind         rootfsSelectionKind
		cleanupFails bool
	}{
		{name: "VM provision", kind: rootfsVMProvisionSelection},
		{name: "image inspect", kind: rootfsImageInspectSelection, cleanupFails: true},
		{name: "image optimize", kind: rootfsImageOptimizeSelection},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newRootfsAuthorityFixture(t, []string{"srv", "cache", "vms", testVMID, "rootfs.img"})
			events := &serverEventLog{}
			longCause := errors.New(strings.Repeat("lock-release-failure-", 40))
			var releaseErr error
			if tc.cleanupFails {
				releaseErr = longCause
			}
			primary := errs.New(
				errs.CodeImageError,
				"lock acquisition failed",
				errs.WithClass(errs.ClassRetryable),
				errs.WithEntity(tc.name),
				errs.WithDetails(map[string]any{"original": tc.name}),
			)
			ctx, cancel := context.WithCancel(context.Background())
			lockLease := &fakeServerResourceLockLease{events: events, err: releaseErr}
			locks := &fakeServerResourceLocks{lease: lockLease, acquireErr: primary, cancel: cancel}
			server := &Server{
				rootfsAuthority: fixture.deps(),
				effects:         &fakeServerRootfsEffects{},
				optimizeEffects: &fakeServerImageOptimizeEffects{},
				locks:           locks,
			}

			var err error
			switch tc.kind {
			case rootfsVMProvisionSelection:
				_, err = server.ProvisionVM(ctx, testRootfsCaller(), testVMProvisionRequest())
			case rootfsImageInspectSelection:
				_, err = server.InspectImage(ctx, testRootfsCaller(), testImageInspectRequest())
			case rootfsImageOptimizeSelection:
				_, err = server.OptimizeImage(ctx, testRootfsCaller(), testImageOptimizeRequest())
			default:
				t.Fatalf("unhandled test action %d", tc.kind)
			}
			require.Error(t, err)

			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, primary.Code, domainErr.Code)
			assert.Equal(t, primary.Class, domainErr.Class)
			assert.Equal(t, primary.Entity, domainErr.Entity)
			assert.Equal(t, primary.Message, domainErr.Message)
			assert.Equal(t, primary.Op, domainErr.Op)
			assert.Equal(t, tc.name, domainErr.Details["original"])
			if tc.cleanupFails {
				assert.Equal(t, true, domainErr.Details["loopmount_resource_lock_release_failed"])
				detail, ok := domainErr.Details["loopmount_resource_lock_release_detail"].(string)
				require.True(t, ok)
				assert.LessOrEqual(t, len(detail), maxServerCleanupDetailBytes)
			} else {
				assert.Same(t, primary, domainErr)
				assert.NotContains(t, domainErr.Details, "loopmount_resource_lock_release_failed")
			}
			assert.Equal(t, 1, lockLease.releaseCount)
			assert.NoError(t, lockLease.contextErr)
			assert.Equal(t, tc.kind, locks.selection.kind)
			assert.Empty(t, fixture.openCalls)
		})
	}
}

// Rationale: Grow replay authority is meaningful only if the effect receives the exact admitted size. The server must
// pass old and target sizes through the lease and reject an unrelated third size before any effect begins.
func TestServerVMGrowReplayPassesActualAdmittedSizeToEffect(t *testing.T) {
	t.Parallel()

	const (
		targetBytes = 256 << 20
		thirdBytes  = 192 << 20
	)
	tests := map[string]struct {
		actualBytes uint64
		state       vmProvisionEffectState
		wantErr     bool
	}{
		"old size converges through effect": {
			actualBytes: testExpectedRootfsBytes,
			state:       vmProvisionEffectStateModified,
		},
		"target retry is already converged": {actualBytes: targetBytes, state: vmProvisionEffectStateUnchanged},
		"third size never reaches effect": {
			actualBytes: thirdBytes,
			state:       vmProvisionEffectStateModified,
			wantErr:     true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			request := testVMProvisionRequest()
			request.Plan.ResizeBytes = targetBytes
			components := []string{"srv", "cache", "vms", request.VMID, "rootfs.img"}
			fixture := newRootfsAuthorityFixture(t, components)
			rootfsFD := fixture.rootFD + len(components)
			fixture.stats[rootfsFD][0].Size = tc.actualBytes
			fixture.stats[rootfsFD][1].Size = tc.actualBytes
			events := &serverEventLog{}
			effects := &fakeServerRootfsEffects{
				provisionOutcome: vmProvisionEffectOutcome{
					state:       tc.state,
					filesystem:  request.Filesystem,
					rootfsBytes: targetBytes,
					lease:       &fakeVMProvisionEffectLease{events: events},
				},
			}
			server := &Server{
				rootfsAuthority: fixture.deps(),
				effects:         effects,
				locks: &fakeServerResourceLocks{
					lease: &fakeServerResourceLockLease{events: events},
				},
			}

			got, err := server.ProvisionVM(t.Context(), testRootfsCaller(), request)
			if tc.wantErr {
				require.Error(t, err)
				assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
				assert.Equal(t, 0, effects.provisionCallCount)
				return
			}
			require.NoError(t, err)
			want := VMProvisionResult{
				VMID: request.VMID, Filesystem: request.Filesystem, RootfsBytes: targetBytes,
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("grow replay result mismatch (-want +got):\n%s", diff)
			}
			assert.Equal(t, 1, effects.provisionCallCount)
			assert.Equal(t, tc.actualBytes, effects.provisionAdmittedBytes)
			assert.Equal(t, request.Filesystem, effects.provisionFilesystem)
			if diff := cmp.Diff(request.Plan, effects.provisionPlan); diff != "" {
				t.Errorf("grow replay plan mismatch (-want +got):\n%s", diff)
			}
		})
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

// assertServerEffectEvidence checks the mandatory receiver-derived partial-state contract on an error.
func assertServerEffectEvidence(t *testing.T, domainErr *errs.DomainError, want rootfsEffectEvidence) {
	t.Helper()

	rootfsModified, ok := domainErr.Details["rootfs_modified"].(bool)
	require.True(t, ok)
	imageReplaced, ok := domainErr.Details["image_replaced"].(bool)
	require.True(t, ok)
	durabilityUncertain, ok := domainErr.Details["durability_uncertain"].(bool)
	require.True(t, ok)
	outcomeUnknown, ok := domainErr.Details["outcome_unknown"].(bool)
	require.True(t, ok)
	assert.Equal(t, want.rootfsModified, rootfsModified)
	assert.Equal(t, want.imageReplaced, imageReplaced)
	assert.Equal(t, want.durabilityUncertain, durabilityUncertain)
	assert.Equal(t, want.outcomeUnknown, outcomeUnknown)
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
	provisionOutcome       vmProvisionEffectOutcome
	inspectOutcome         imageInspectEffectOutcome
	provisionErr           error
	inspectErr             error
	cancel                 context.CancelFunc
	provisionAccess        rootfsAccess
	inspectAccess          rootfsAccess
	provisionFilesystem    Filesystem
	provisionPlan          VMProvisionPlan
	inspectFilesystem      Filesystem
	provisionAdmittedBytes uint64
	provisionCallCount     int
	inspectCallCount       int
}

func (effects *fakeServerRootfsEffects) ProvisionVM(
	_ context.Context,
	lease *rootfsLease,
	filesystem Filesystem,
	plan VMProvisionPlan,
) (vmProvisionEffectOutcome, error) {
	effects.provisionCallCount++
	effects.provisionAccess = lease.access
	effects.provisionAdmittedBytes = lease.admittedBytes
	effects.provisionFilesystem = filesystem
	effects.provisionPlan = plan
	if effects.cancel != nil {
		effects.cancel()
	}
	return effects.provisionOutcome, effects.provisionErr
}

func (effects *fakeServerRootfsEffects) InspectImage(
	_ context.Context,
	lease *rootfsLease,
	filesystem Filesystem,
) (imageInspectEffectOutcome, error) {
	effects.inspectCallCount++
	effects.inspectAccess = lease.access
	effects.inspectFilesystem = filesystem
	return effects.inspectOutcome, effects.inspectErr
}

type fakeServerImageOptimizeEffects struct {
	optimizeOutcome    imageOptimizeEffectOutcome
	optimizeErr        error
	optimizeAccess     rootfsAccess
	optimizeFilesystem Filesystem
	optimizePlan       ImageOptimizePlan
	optimizeCallCount  int
}

func (effects *fakeServerImageOptimizeEffects) OptimizeImage(
	_ context.Context,
	lease *rootfsLease,
	filesystem Filesystem,
	plan ImageOptimizePlan,
) (imageOptimizeEffectOutcome, error) {
	effects.optimizeCallCount++
	effects.optimizeAccess = lease.access
	effects.optimizeFilesystem = filesystem
	effects.optimizePlan = plan
	return effects.optimizeOutcome, effects.optimizeErr
}

type fakeVMProvisionEffectLease struct {
	events      *serverEventLog
	err         error
	contextErr  error
	contextMark string
}

func (lease *fakeVMProvisionEffectLease) CleanupProvisionVM(ctx context.Context) error {
	lease.events.append("effect")
	lease.contextErr = ctx.Err()
	lease.contextMark, _ = ctx.Value(rootfsCleanupContextKey{}).(string)
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
	cancel       context.CancelFunc
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
	if locks.cancel != nil {
		locks.cancel()
	}
	return locks.lease, locks.acquireErr
}

type fakeServerResourceLockLease struct {
	events       *serverEventLog
	err          error
	contextErr   error
	contextMark  string
	releaseCount int
}

func (lease *fakeServerResourceLockLease) Release(ctx context.Context) error {
	lease.events.append("lock")
	lease.contextErr = ctx.Err()
	lease.contextMark, _ = ctx.Value(rootfsCleanupContextKey{}).(string)
	lease.releaseCount++
	return lease.err
}
