package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corevm "mvmctl/internal/core/vm"
	"mvmctl/internal/core/vsock"
	"mvmctl/internal/infra"
	"mvmctl/internal/lib/logging"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/system"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

type relaunchVsockRepo struct {
	vsock.Repository
	item     *model.VsockConfigItem
	err      error
	getCalls int
}

const (
	relaunchTestVMID     = "11111111111111111111111111111111"
	relaunchTestVolumeID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	relaunchMissingID    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func (r *relaunchVsockRepo) GetByVMID(_ context.Context, _ string) (*model.VsockConfigItem, error) {
	r.getCalls++
	return r.item, r.err
}

func TestLoadVMRelaunchState(t *testing.T) {
	t.Run("row is cloned and normalized", func(t *testing.T) {
		started := time.Unix(123, 0)
		row := &model.VsockConfigItem{
			ID: "vsock-1", VmID: relaunchTestVMID, GuestCID: model.MinVsockGuestCID,
			UDSPath: "/database-selected.sock", Port: 1024, Token: "secret",
			AgentVersion: "1.2.3", Upgrading: true, UpgradeStartedAt: &started,
		}
		repo := &relaunchVsockRepo{Repository: testutil.NewVsockRepo(), item: row}
		cacheDir := t.TempDir()
		op := &Operation{CacheDir: cacheDir, Repos: Repos{Vsock: repo}}
		vmItem := &model.VMItem{ID: relaunchTestVMID, Name: "example"}

		state, err := op.loadVMRelaunchState(t.Context(), vmItem)
		require.NoError(t, err)
		require.NotNil(t, state)
		assert.Same(t, vmItem, state.vm)
		want := *row
		want.UDSPath = filepath.Join("/run/mvmctl/runtime", strconv.Itoa(os.Geteuid()),
			vmItem.ID, infra.VMVsockSocketFilename)
		if diff := cmp.Diff(&want, state.vsock); diff != "" {
			t.Errorf("relaunch vsock state mismatch (-want +got):\n%s", diff)
		}

		state.vsock.UDSPath = "/mutated"
		*state.vsock.UpgradeStartedAt = time.Unix(456, 0)
		assert.Equal(t, "/database-selected.sock", row.UDSPath)
		assert.Equal(t, time.Unix(123, 0), *row.UpgradeStartedAt)
	})

	t.Run("valid absence", func(t *testing.T) {
		repo := &relaunchVsockRepo{Repository: testutil.NewVsockRepo()}
		vmItem := &model.VMItem{ID: "vm-absent"}
		state, err := (&Operation{Repos: Repos{Vsock: repo}}).loadVMRelaunchState(t.Context(), vmItem)
		require.NoError(t, err)
		require.NotNil(t, state)
		assert.Same(t, vmItem, state.vm)
		assert.Nil(t, state.vsock)
	})

	t.Run("valid CID boundaries", func(t *testing.T) {
		for name, cid := range map[string]int{
			"minimum": model.MinVsockGuestCID,
			"maximum": int(model.MaxVsockGuestCID),
		} {
			t.Run(name, func(t *testing.T) {
				repo := &relaunchVsockRepo{
					Repository: testutil.NewVsockRepo(),
					item:       &model.VsockConfigItem{VmID: "vm-boundary", GuestCID: cid},
				}
				state, err := (&Operation{CacheDir: t.TempDir(), Repos: Repos{Vsock: repo}}).
					loadVMRelaunchState(t.Context(), &model.VMItem{ID: "vm-boundary"})
				require.NoError(t, err)
				require.NotNil(t, state.vsock)
				assert.Equal(t, cid, state.vsock.GuestCID)
			})
		}
	})

	t.Run("repository failure preserves cause", func(t *testing.T) {
		cause := errors.New("database unavailable")
		repo := &relaunchVsockRepo{Repository: testutil.NewVsockRepo(), err: cause}
		state, err := (&Operation{Repos: Repos{Vsock: repo}}).loadVMRelaunchState(
			t.Context(), &model.VMItem{ID: "vm-error"},
		)
		require.Error(t, err)
		assert.Nil(t, state)
		assert.ErrorIs(t, err, cause)
		assert.Equal(t, errs.CodeDatabaseError, errs.AsDomainError(err).Code)
	})

	for name, row := range map[string]*model.VsockConfigItem{
		"VM mismatch":       {VmID: "other-vm", GuestCID: model.MinVsockGuestCID},
		"reserved CID":      {VmID: "vm-invalid", GuestCID: model.MinVsockGuestCID - 1},
		"CID above maximum": {VmID: "vm-invalid", GuestCID: int(model.MaxVsockGuestCID) + 1},
	} {
		t.Run(name, func(t *testing.T) {
			repo := &relaunchVsockRepo{Repository: testutil.NewVsockRepo(), item: row}
			state, err := (&Operation{Repos: Repos{Vsock: repo}}).loadVMRelaunchState(
				t.Context(), &model.VMItem{ID: "vm-invalid"},
			)
			require.Error(t, err)
			assert.Nil(t, state)
			assert.Equal(t, errs.CodeDatabaseError, errs.AsDomainError(err).Code)
		})
	}
}

// Rationale: reboot must load persisted relaunch state before stopping a running VM.
// A failed lookup must leave the VM running instead of stopping it without a relaunch plan.
func TestVMRebootLoadsRelaunchStateBeforeStop(t *testing.T) {
	vmRepo := testutil.NewVMRepo()
	require.NoError(t, vmRepo.Upsert(t.Context(), &model.VMItem{
		ID: relaunchTestVMID, Name: "vm-relaunch", Status: model.VMStatusRunning,
		CgroupLimits: model.VMCgroupLimits{PolicyVersion: 1},
	}))
	cause := errors.New("vsock lookup failed")
	vsockRepo := &relaunchVsockRepo{Repository: testutil.NewVsockRepo(), err: cause}
	op := &Operation{Repos: Repos{VM: vmRepo, Vsock: vsockRepo}}

	result := op.VMReboot(t.Context(), inputs.VMInput{Identifiers: []string{relaunchTestVMID}})
	require.Len(t, result.Items, 1)
	require.Error(t, result.Items[0].Exception)
	assert.ErrorIs(t, result.Items[0].Exception, cause)
	assert.Equal(t, errs.CodeDatabaseError, errs.AsDomainError(result.Items[0].Exception).Code)
	assert.Equal(t, 1, vsockRepo.getCalls)
	persisted, getErr := vmRepo.Get(t.Context(), relaunchTestVMID)
	require.NoError(t, getErr)
	require.NotNil(t, persisted)
	assert.Equal(t, model.VMStatusRunning, persisted.Status)
}

// Rationale: a corrupt CID must abort reboot before the running VM is stopped.
func TestVMRebootRejectsCorruptRelaunchCIDBeforeStop(t *testing.T) {
	vmRepo := testutil.NewVMRepo()
	require.NoError(t, vmRepo.Upsert(t.Context(), &model.VMItem{
		ID: relaunchTestVMID, Name: "vm-corrupt", Status: model.VMStatusRunning,
		CgroupLimits: model.VMCgroupLimits{PolicyVersion: 1},
	}))
	vsockRepo := &relaunchVsockRepo{
		Repository: testutil.NewVsockRepo(),
		item: &model.VsockConfigItem{
			VmID: relaunchTestVMID, GuestCID: model.MinVsockGuestCID - 1,
		},
	}
	op := &Operation{CacheDir: t.TempDir(), Repos: Repos{VM: vmRepo, Vsock: vsockRepo}}

	result := op.VMReboot(t.Context(), inputs.VMInput{Identifiers: []string{relaunchTestVMID}})
	require.Len(t, result.Items, 1)
	require.Error(t, result.Items[0].Exception)
	assert.Equal(t, errs.CodeDatabaseError, errs.AsDomainError(result.Items[0].Exception).Code)
	assert.Equal(t, 1, vsockRepo.getCalls)
	persisted, getErr := vmRepo.Get(t.Context(), relaunchTestVMID)
	require.NoError(t, getErr)
	require.NotNil(t, persisted)
	assert.Equal(t, model.VMStatusRunning, persisted.Status)
}

// Rationale: persisted VolumeIDs must all resolve before reboot stops the VM.
// Enriched volume relations are not authority for raw-only attachment.
func TestVMRebootRejectsUnsupportedOrMissingVolumesBeforeStop(t *testing.T) {
	for name, tc := range map[string]struct {
		format model.VolumeFormat
		ids    []string
		code   errs.Code
	}{
		"qcow2":            {model.VolumeFormatQCOW2, []string{relaunchTestVolumeID}, errs.CodeValidationFailed},
		"unknown":          {"unknown", []string{relaunchTestVolumeID}, errs.CodeValidationFailed},
		"empty format":     {"", []string{relaunchTestVolumeID}, errs.CodeValidationFailed},
		"missing":          {model.VolumeFormatRaw, []string{relaunchMissingID}, errs.CodeVolumeNotFound},
		"partial":          {model.VolumeFormatRaw, []string{relaunchTestVolumeID, relaunchMissingID}, errs.CodeVolumeNotFound},
		"name collision":   {model.VolumeFormatRaw, []string{"data"}, errs.CodeVolumeNotFound},
		"prefix collision": {model.VolumeFormatRaw, []string{"aaaaaaaa"}, errs.CodeVolumeNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			volumeRepo := testutil.NewVolumeRepo()
			require.NoError(t, volumeRepo.Upsert(t.Context(), &model.VolumeItem{
				ID: relaunchTestVolumeID, Name: "data", Format: tc.format,
			}))
			original := model.VMItem{
				ID: relaunchTestVMID, Name: "vm-volumes", Status: model.VMStatusRunning,
				VolumeIDs: tc.ids, CgroupLimits: model.VMCgroupLimits{PolicyVersion: 1},
				Volumes: []*model.VolumeItem{{ID: "stale", Format: model.VolumeFormatRaw}},
			}
			vmItem := original
			vmRepo := testutil.NewVMRepo()
			require.NoError(t, vmRepo.Upsert(t.Context(), &vmItem))
			runner := &testutil.FakeRunner{}
			old := system.DefaultRunner
			system.DefaultRunner = runner
			t.Cleanup(func() { system.DefaultRunner = old })
			op := &Operation{
				Repos:    Repos{VM: vmRepo, Volume: volumeRepo, Vsock: testutil.NewVsockRepo()},
				AuditLog: &logging.AuditLog{},
			}
			input := inputs.VMInput{Identifiers: []string{relaunchTestVMID}}
			result := op.VMReboot(t.Context(), input)
			require.Len(t, result.Items, 1)
			require.Error(t, result.Items[0].Exception)
			assert.Equal(t, tc.code, errs.AsDomainError(result.Items[0].Exception).Code)
			assert.Equal(t, "error", result.Items[0].Status)
			assert.Empty(t, runner.Calls)
			stored, err := vmRepo.Get(t.Context(), original.ID)
			require.NoError(t, err)
			testutil.AssertDiff(t, &original, stored)
		})
	}
}

// Rationale: relaunch reads exact persisted raw volumes, not potentially stale enriched relations.
// Config construction must use those resolved records for guest-visible drive options.
func TestLoadVMRelaunchStateCarriesResolvedRawVolumesToConfig(t *testing.T) {
	volumeRepo := testutil.NewVolumeRepo()
	raw := &model.VolumeItem{
		ID: relaunchTestVolumeID, Name: "data", Format: model.VolumeFormatRaw,
		Path:       "/managed/" + relaunchTestVolumeID + ".raw",
		IsReadOnly: true, IsShareable: true, CacheType: model.CacheTypeWriteback,
	}
	require.NoError(t, volumeRepo.Upsert(t.Context(), raw))
	vmItem := &model.VMItem{
		ID: relaunchTestVMID, NetworkPlacement: model.VMNetworkPlacementManaged,
		VolumeIDs: []string{relaunchTestVolumeID},
		Volumes:   []*model.VolumeItem{{ID: "stale", Format: model.VolumeFormatQCOW2}},
		Image:     &model.ImageItem{}, Kernel: &model.KernelItem{}, Binary: &model.BinaryItem{},
		Network: &model.NetworkItem{},
	}
	op := &Operation{Repos: Repos{Volume: volumeRepo, Vsock: testutil.NewVsockRepo()}}
	state, err := op.loadVMRelaunchState(t.Context(), vmItem)
	require.NoError(t, err)
	require.NotNil(t, state)
	testutil.AssertDiff(t, []*model.VolumeItem{raw}, state.volumes)
	config := buildRespawnFirecrackerConfig(state, &model.BinaryPair{
		Firecracker: &model.BinaryItem{}, Jailer: &model.BinaryItem{},
	}, t.TempDir(), false, "", "Info")
	testutil.AssertDiff(t, []model.DriveConfig{{
		DriveID: relaunchTestVolumeID, PathOnHost: "/managed/" + relaunchTestVolumeID + ".raw",
		IsReadOnly: true, CacheType: model.CacheTypeWriteback, IOEngine: "Sync",
	}}, config.ExtraDrives)
}

// Rationale: relaunch reconstructs the admitted placement from the VM row;
// an absent network must not acquire a managed NIC, TAP, or kernel IP argument.
func TestBuildRespawnFirecrackerConfigRetainsNetworklessPlacement(t *testing.T) {
	vmItem := &model.VMItem{
		ID: relaunchTestVMID, NetworkPlacement: model.VMNetworkPlacementNetworkless,
		Binary: &model.BinaryItem{}, Kernel: &model.KernelItem{},
		BootArgs: "console=ttyS0", VCPUCount: 1, MemSizeMiB: 256,
	}
	state := &vmRelaunchState{vm: vmItem, vsock: &model.VsockConfigItem{GuestCID: 42}}
	config := buildRespawnFirecrackerConfig(state, &model.BinaryPair{
		Firecracker: &model.BinaryItem{Version: "1.16.1", Architecture: "x86_64"},
		Jailer:      &model.BinaryItem{},
	}, t.TempDir(), false, "", "Info")
	require.Equal(t, model.VMNetworkPlacementNetworkless, config.NetworkPlacement)
	require.Empty(t, config.NetworkID)
	require.Empty(t, config.GuestIP)
	require.Empty(t, config.GuestMAC)
	require.Empty(t, config.TapName)
	require.NotNil(t, config.VsockGuestCID)
	require.Equal(t, 42, *config.VsockGuestCID)

	generated, err := corevm.NewFirecrackerSpawner(config).Generate()
	require.NoError(t, err)
	assert.Empty(t, generated.NetworkInterfaces)
	assert.NotContains(t, generated.BootSource.BootArgs, "ip=")
	assert.NotContains(t, generated.BootSource.BootArgs, "ds=")
	require.NotNil(t, generated.Vsock)
	assert.Equal(t, 42, generated.Vsock.GuestCID)
}

func TestVMRespawnRejectsInvalidPlacementBeforeEffects(t *testing.T) {
	for _, tc := range []struct {
		name string
		vm   model.VMItem
		want string
	}{
		{name: "unknown placement", vm: model.VMItem{NetworkPlacement: "other"}, want: "network placement"},
		{name: "managed without network", vm: model.VMItem{NetworkPlacement: model.VMNetworkPlacementManaged}, want: "network info"},
		{name: "networkless with endpoint", vm: model.VMItem{
			NetworkPlacement: model.VMNetworkPlacementNetworkless, IPv4: "10.0.0.2",
		}, want: "managed network state"},
		{name: "networkless with NoCloud NET", vm: model.VMItem{
			NetworkPlacement: model.VMNetworkPlacementNetworkless, CloudInitMode: string(model.CloudInitModeNET),
		}, want: "managed network state"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := (&Operation{}).vmRespawnFirecracker(t.Context(), &vmRelaunchState{vm: &tc.vm}, false, "")
			require.ErrorContains(t, err, tc.want)
		})
	}
}

// Rationale: reboot must validate persisted placement before it stops a running VM.
func TestVMRebootRejectsNetworklessEndpointBeforeStop(t *testing.T) {
	vmRepo := testutil.NewVMRepo()
	vmItem := &model.VMItem{
		ID: relaunchTestVMID, Name: "networkless-invalid", Status: model.VMStatusRunning,
		NetworkPlacement: model.VMNetworkPlacementNetworkless, IPv4: "10.0.0.2",
	}
	require.NoError(t, vmRepo.Upsert(t.Context(), vmItem))
	op := &Operation{Repos: Repos{VM: vmRepo, Vsock: testutil.NewVsockRepo()}}
	result := op.VMReboot(t.Context(), inputs.VMInput{Identifiers: []string{relaunchTestVMID}})
	require.Len(t, result.Items, 1)
	require.ErrorContains(t, result.Items[0].Exception, "managed network state")
	persisted, err := vmRepo.Get(t.Context(), relaunchTestVMID)
	require.NoError(t, err)
	assert.Equal(t, model.VMStatusRunning, persisted.Status)
}
