package api

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/vm"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/service/jailer"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/errs"
)

type volumeAttachAuthority struct {
	result                jailer.VMVolumeResult
	observations          []jailer.VMVolumeObservation
	attachErr, observeErr error
}

func (authority *volumeAttachAuthority) AttachManagedVolume(
	_ context.Context, _ string, _ *model.VolumeItem,
) (jailer.VMVolumeResult, error) {
	return authority.result, authority.attachErr
}

func (authority *volumeAttachAuthority) ObserveVolume(
	_ context.Context, _ string,
) (jailer.VMVolumeObservation, error) {
	if authority.observeErr != nil {
		return jailer.VMVolumeObservation{}, authority.observeErr
	}
	if len(authority.observations) == 0 {
		return jailer.VMVolumeObservation{}, errs.New(errs.CodeInternal, "unexpected additional volume observation")
	}
	observed := authority.observations[0]
	authority.observations = authority.observations[1:]
	return observed, nil
}

type volumeReadyGuest struct {
	identity  model.PCIVolumeIdentity
	err       error
	forbidden bool
	cancel    context.CancelFunc
	calls     int
}

func (guest *volumeReadyGuest) EnsurePCIVolumeReady(ctx context.Context, identity model.PCIVolumeIdentity) error {
	guest.calls++
	if guest.forbidden {
		return errs.New(errs.CodeInternal, "guest readiness must not be attempted")
	}
	if identity != guest.identity {
		return errs.New(errs.CodeVolumeGuestNotReady, "wrong root-derived identity selected")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if guest.cancel != nil {
		guest.cancel()
	}
	return guest.err
}

func TestVolumeAttachCancellationAfterGuestAckRetainsBinding(t *testing.T) {
	// Rationale: Even an acknowledged device cannot turn a cancelled attach into successful completion.
	op, vmItem, vol, observed := volumeAttachFixture(t, model.VMStatusRunning)
	authority := &volumeAttachAuthority{
		result: jailer.VMVolumeResult{
			VMID: vmItem.ID, VolumeID: vol.ID, LaunchGeneration: observed.LaunchGeneration, Attached: true,
		},
		observations: []jailer.VMVolumeObservation{observed, observed},
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	guest := &volumeReadyGuest{
		identity: model.PCIVolumeIdentity{
			Serial:        observed.Serial,
			ExpectedBytes: observed.ExpectedBytes,
		},
		cancel: cancel,
	}
	err := op.attachVolumeToVM(ctx, vmItem, vol, authority, guest, nil)
	assert.Equal(t, 1, guest.calls)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	de := errs.AsDomainError(err)
	assert.Equal(t, errs.CodeVolumeGuestNotReady, de.Code)
	assert.Equal(t, true, de.Details["vmm_attached"])
	stored, repoErr := op.Repos.Volume.Get(t.Context(), vol.ID)
	require.NoError(t, repoErr)
	assert.Equal(t, model.VolumeStatusAttached, stored.Status)
	require.NotNil(t, stored.VMID)
	assert.Equal(t, vmItem.ID, *stored.VMID)
	storedVM, repoErr := op.Repos.VM.Get(t.Context(), vmItem.ID)
	require.NoError(t, repoErr)
	if diff := cmp.Diff([]string{"prior-volume", vol.ID}, []string(storedVM.VolumeIDs)); diff != "" {
		t.Errorf("cancelled attachment binding (-want +got):\n%s", diff)
	}
}

type volumeAttachFailingVMRepo struct {
	vm.Repository
	err error
}

func (repo *volumeAttachFailingVMRepo) Upsert(context.Context, *model.VMItem) error { return repo.err }

// volumeAttachFixture supplies distinct caller labels and root-derived guest identity.
func volumeAttachFixture(t *testing.T, status model.VMStatus) (
	*Operation, *model.VMItem, *model.VolumeItem, jailer.VMVolumeObservation,
) {
	t.Helper()
	vmRepo, volumeRepo := testutil.NewVMRepo(), testutil.NewVolumeRepo()
	vmItem := &model.VMItem{
		ID: "33333333333333333333333333333333", Name: "running-source", Status: status,
		VolumeIDs: []string{"prior-volume"},
	}
	vol := &model.VolumeItem{
		ID: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", Name: "named-data",
		SizeBytes: 4096, Format: model.VolumeFormatRaw, Status: model.VolumeStatusAvailable,
	}
	require.NoError(t, vmRepo.Upsert(t.Context(), vmItem))
	require.NoError(t, volumeRepo.Upsert(t.Context(), vol))
	op := &Operation{Repos: Repos{VM: vmRepo, Volume: volumeRepo}}
	observed := jailer.VMVolumeObservation{
		VMID: vmItem.ID, VolumeID: vol.ID, LaunchGeneration: 7, Attached: true,
		GuestDriveID: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		Serial:       "987654", ExpectedBytes: 4096, MountID: 29, SourceMajor: 1, SourceMinor: 2, SourceInode: 31,
	}
	return op, vmItem, vol, observed
}

func TestVolumeAttachCompletionRetainsBindingUntilExactReadiness(t *testing.T) {
	// Rationale: A root-attached drive must stay owned when the guest cannot use it or completion is deferred.
	for name, tc := range map[string]struct {
		status                                                                                                     model.VMStatus
		guestFailure, missingGuest, acquisitionFailure, changedGeneration, changedMount, initialGenerationMismatch bool
		observationFailure, shareable                                                                              bool
		wantError                                                                                                  bool
		wantGuestCalls                                                                                             int
	}{
		"guest readiness failure retains binding":          {guestFailure: true, wantError: true, wantGuestCalls: 1},
		"agent configuration absent":                       {missingGuest: true, wantError: true},
		"agent acquisition failed":                         {acquisitionFailure: true, wantError: true},
		"guest ready but launch generation changed":        {changedGeneration: true, wantError: true, wantGuestCalls: 1},
		"guest ready but mount identity changed":           {changedMount: true, wantError: true, wantGuestCalls: 1},
		"root generation changed before guest discovery":   {initialGenerationMismatch: true, wantError: true},
		"root identity observation failed":                 {observationFailure: true, wantError: true},
		"paused staging defers guest readiness":            {status: model.VMStatusPaused, wantError: true},
		"running exact guest and root readiness":           {wantGuestCalls: 1},
		"shareable read-only retains available projection": {shareable: true, wantGuestCalls: 1},
		"stopped configuration remains supported":          {status: model.VMStatusStopped},
	} {
		t.Run(name, func(t *testing.T) {
			status := tc.status
			if status == "" {
				status = model.VMStatusRunning
			}
			op, vmItem, vol, observed := volumeAttachFixture(t, status)
			if tc.shareable {
				vol.IsReadOnly, vol.IsShareable = true, true
			}
			fresh := observed
			if tc.changedGeneration {
				fresh.LaunchGeneration++
			}
			if tc.changedMount {
				fresh.MountID++
			}
			result := jailer.VMVolumeResult{
				VMID: vmItem.ID, VolumeID: vol.ID, LaunchGeneration: observed.LaunchGeneration, Attached: true,
			}
			if tc.initialGenerationMismatch {
				result.LaunchGeneration--
			}
			authority := &volumeAttachAuthority{
				result: result, observations: []jailer.VMVolumeObservation{observed, fresh},
			}
			if tc.observationFailure {
				authority.observeErr = errors.New("root observation unavailable")
			}
			guestProbe := &volumeReadyGuest{
				identity:  model.PCIVolumeIdentity{Serial: observed.Serial, ExpectedBytes: observed.ExpectedBytes},
				forbidden: status != model.VMStatusRunning || tc.initialGenerationMismatch || tc.observationFailure,
			}
			var guest pciVolumeReadinessClient = guestProbe
			if tc.guestFailure {
				guestProbe.err = errs.New(
					errs.CodeVolumeGuestNotReady,
					"selected block device is absent",
				)
			}
			if tc.missingGuest {
				guest = nil
			}
			var guestErr error
			if tc.acquisitionFailure {
				guestErr = errors.New("vsock configuration read failed")
			}
			err := op.attachVolumeToVM(t.Context(), vmItem, vol, authority, guest, guestErr)
			assert.Equal(t, tc.wantGuestCalls, guestProbe.calls, "guest contact follows readiness admission")
			if tc.wantError {
				require.Error(t, err)
				de := errs.AsDomainError(err)
				require.NotNil(t, de)
				assert.Equal(t, errs.CodeVolumeGuestNotReady, de.Code)
				assert.Equal(t, errs.ClassRetryable, de.Class)
				assert.Equal(t, vol.ID, de.Entity)
				want := map[string]any{
					"vm_id": vmItem.ID, "vmm_attached": true, "guest_ready": false,
					"readiness_deferred": status == model.VMStatusPaused,
				}
				if diff := cmp.Diff(want, de.Details); diff != "" {
					t.Errorf("partial completion details (-want +got):\n%s", diff)
				}
			} else {
				assert.NoError(t, err)
			}
			stored, repoErr := op.Repos.Volume.Get(t.Context(), vol.ID)
			require.NoError(t, repoErr)
			wantVol := *vol
			if !tc.shareable {
				wantVol.Status, wantVol.VMID = model.VolumeStatusAttached, &vmItem.ID
			}
			if diff := cmp.Diff(&wantVol, stored); diff != "" {
				t.Errorf("configured volume ownership (-want +got):\n%s", diff)
			}
			storedVM, repoErr := op.Repos.VM.Get(t.Context(), vmItem.ID)
			require.NoError(t, repoErr)
			wantVM := *vmItem
			wantVM.VolumeIDs = []string{"prior-volume", vol.ID}
			if diff := cmp.Diff(&wantVM, storedVM); diff != "" {
				t.Errorf("configured VM volume set (-want +got):\n%s", diff)
			}
		})
	}
}

func TestVolumeAttachRootFailureDistinguishesAppliedFromUnknown(t *testing.T) {
	for name, tc := range map[string]struct{ unknown, absent bool }{
		"unknown root outcome":               {unknown: true},
		"root observation proves absence":    {absent: true},
		"root observation proves attachment": {},
	} {
		t.Run(name, func(t *testing.T) {
			op, vmItem, vol, observed := volumeAttachFixture(t, model.VMStatusRunning)
			rootErr := errors.New("root transport closed before acknowledgment")
			authority := &volumeAttachAuthority{attachErr: rootErr}
			if tc.unknown {
				authority.observeErr = errors.New("root transition remains unobservable")
			} else if tc.absent {
				authority.observations = []jailer.VMVolumeObservation{{
					VMID: vmItem.ID, VolumeID: vol.ID, LaunchGeneration: observed.LaunchGeneration,
				}}
			} else {
				authority.observations = []jailer.VMVolumeObservation{observed}
			}
			guest := &volumeReadyGuest{forbidden: true}
			err := op.attachVolumeToVM(t.Context(), vmItem, vol, authority, guest, nil)
			assert.Zero(t, guest.calls, "an unacknowledged root effect must not contact the guest")
			require.Error(t, err)
			assert.ErrorIs(t, err, rootErr)
			if !tc.absent {
				de := errs.AsDomainError(err)
				require.NotNil(t, de)
				assert.Equal(t, errs.CodeVolumeGuestNotReady, de.Code)
				if tc.unknown {
					assert.Equal(t, true, de.Details["vmm_outcome_unknown"])
					_, claimed := de.Details["vmm_attached"]
					assert.False(t, claimed, "unknown root fate cannot be claimed attached")
				} else {
					assert.Equal(t, true, de.Details["vmm_attached"])
				}
			}
			stored, repoErr := op.Repos.Volume.Get(t.Context(), vol.ID)
			require.NoError(t, repoErr)
			wantVol := *vol
			wantVM := *vmItem
			if !tc.unknown && !tc.absent {
				wantVol.Status, wantVol.VMID = model.VolumeStatusAttached, &vmItem.ID
				wantVM.VolumeIDs = []string{"prior-volume", vol.ID}
			}
			if diff := cmp.Diff(&wantVol, stored); diff != "" {
				t.Errorf("root-fate volume projection (-want +got):\n%s", diff)
			}
			storedVM, repoErr := op.Repos.VM.Get(t.Context(), vmItem.ID)
			require.NoError(t, repoErr)
			if diff := cmp.Diff(&wantVM, storedVM); diff != "" {
				t.Errorf("root-fate VM projection (-want +got):\n%s", diff)
			}
		})
	}
}

func TestVolumeAttachProjectionFailureRetryRepairsWithoutDuplicateBinding(t *testing.T) {
	// Rationale: A root effect survives a failed second projection; retry must repair rather than roll it back.
	op, vmItem, vol, observed := volumeAttachFixture(t, model.VMStatusRunning)
	vmRepo := op.Repos.VM
	projectionErr := errors.New("VM projection unavailable")
	op.Repos.VM = &volumeAttachFailingVMRepo{Repository: vmRepo, err: projectionErr}
	result := jailer.VMVolumeResult{
		VMID: vmItem.ID, VolumeID: vol.ID, LaunchGeneration: observed.LaunchGeneration, Attached: true,
	}
	authority := &volumeAttachAuthority{result: result}
	guest := &volumeReadyGuest{forbidden: true}
	err := op.attachVolumeToVM(t.Context(), vmItem, vol, authority, guest, nil)
	assert.Zero(t, guest.calls, "failed projection must not contact the guest")
	require.Error(t, err)
	assert.ErrorIs(t, err, projectionErr)
	de := errs.AsDomainError(err)
	assert.Equal(t, errs.CodeDatabaseError, de.Code)
	assert.Equal(t, true, de.Details["vmm_attached"])
	assert.Equal(t, true, de.Details["projection_failed"])
	stored, repoErr := op.Repos.Volume.Get(t.Context(), vol.ID)
	require.NoError(t, repoErr)
	assert.Equal(t, model.VolumeStatusAttached, stored.Status)
	require.NotNil(t, stored.VMID)
	assert.Equal(t, vmItem.ID, *stored.VMID)
	storedVM, repoErr := vmRepo.Get(t.Context(), vmItem.ID)
	require.NoError(t, repoErr)
	if diff := cmp.Diff([]string{"prior-volume"}, []string(storedVM.VolumeIDs)); diff != "" {
		t.Errorf("failed VM projection must not change durable state (-want +got):\n%s", diff)
	}
	op.Repos.VM = vmRepo
	for _, attempt := range []string{"repair", "already complete"} {
		t.Run(attempt, func(t *testing.T) {
			currentVM, err := vmRepo.Get(t.Context(), vmItem.ID)
			require.NoError(t, err)
			currentVol, err := op.Repos.Volume.Get(t.Context(), vol.ID)
			require.NoError(t, err)
			authority.observations = []jailer.VMVolumeObservation{observed, observed}
			guest := &volumeReadyGuest{identity: model.PCIVolumeIdentity{Serial: observed.Serial, ExpectedBytes: 4096}}
			assert.NoError(t, op.attachVolumeToVM(t.Context(), currentVM, currentVol, authority, guest, nil))
			assert.Equal(t, 1, guest.calls)
			got, err := vmRepo.Get(t.Context(), vmItem.ID)
			require.NoError(t, err)
			if diff := cmp.Diff([]string{"prior-volume", vol.ID}, []string(got.VolumeIDs)); diff != "" {
				t.Errorf("retry VM binding (-want +got):\n%s", diff)
			}
		})
	}
}
