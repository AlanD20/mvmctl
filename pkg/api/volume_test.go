package api_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/volume"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/ptr"
	"mvmctl/internal/lib/logging"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/system"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

const (
	volumeTestVMID     = "33333333333333333333333333333333"
	volumeTestVolumeID = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

func TestVolumeCreateUsesImmutableIDForManagedPath(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv("MVM_CACHE_DIR", cacheDir)

	tests := map[string]struct {
		name      string
		readOnly  bool
		shareable bool
		writeback bool
	}{
		"raw_default": {
			name: "customer-backups",
		},
		"raw_options": {
			name: "database-volume", readOnly: true, shareable: true, writeback: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			repo := testutil.NewVolumeRepo()
			fakeRunner := &testutil.FakeRunner{}
			originalRunner := system.DefaultRunner
			system.DefaultRunner = fakeRunner
			t.Cleanup(func() { system.DefaultRunner = originalRunner })

			op := &api.Operation{
				Repos:    api.Repos{Volume: repo},
				Services: api.Services{Volume: volume.NewService(repo)},
				AuditLog: &logging.AuditLog{},
			}
			created, err := op.VolumeCreate(context.Background(), inputs.VolumeCreateInput{
				Name: tc.name, Size: "1MB", Writeback: &tc.writeback,
				ReadOnly: &tc.readOnly, Shareable: &tc.shareable,
			})
			require.NoError(t, err)
			require.NotNil(t, created)
			assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{64}$`), created.ID)

			wantPath := filepath.Join(cacheDir, "volumes", created.ID+".raw")
			assert.Equal(t, wantPath, created.Path)
			assert.NotContains(t, filepath.Base(created.Path), tc.name)

			stored, repoErr := repo.Get(context.Background(), created.ID)
			require.NoError(t, repoErr)
			require.NotNil(t, stored)
			assert.Equal(t, wantPath, stored.Path)
			assert.Equal(t, model.VolumeFormatRaw, stored.Format)
			assert.Equal(t, int64(1024*1024), stored.SizeBytes)
			assert.Equal(t, tc.readOnly, stored.IsReadOnly)
			assert.Equal(t, tc.shareable, stored.IsShareable)
			wantCache := model.CacheTypeUnsafe
			if tc.writeback {
				wantCache = model.CacheTypeWriteback
			}
			assert.Equal(t, wantCache, stored.CacheType)

			require.Len(t, fakeRunner.Calls, 1)
			require.Len(t, fakeRunner.Calls[0].Args, 4)
			assert.Equal(t, "fallocate", fakeRunner.Calls[0].Args[0])
			assert.Equal(t, wantPath, fakeRunner.Calls[0].Args[3])
		})
	}
}

func TestVolumeCreateRejectsPathLikeDisplayNamesBeforeDiskCreation(t *testing.T) {
	tests := []string{"../escape", "nested/escape"}
	for _, displayName := range tests {
		t.Run(displayName, func(t *testing.T) {
			repo := testutil.NewVolumeRepo()
			fakeRunner := &testutil.FakeRunner{}
			originalRunner := system.DefaultRunner
			system.DefaultRunner = fakeRunner
			t.Cleanup(func() { system.DefaultRunner = originalRunner })

			op := &api.Operation{
				Repos:    api.Repos{Volume: repo},
				Services: api.Services{Volume: volume.NewService(repo)},
				AuditLog: &logging.AuditLog{},
			}
			writeback := false

			created, err := op.VolumeCreate(context.Background(), inputs.VolumeCreateInput{
				Name: displayName, Size: "1MB", Writeback: &writeback,
			})
			require.Error(t, err)
			assert.Nil(t, created)
			assert.Empty(t, fakeRunner.Calls)
		})
	}
}

func TestVolumeOperationsRejectUnsupportedOrMissingBeforeEffects(t *testing.T) {
	for name, tc := range map[string]struct {
		format      model.VolumeFormat
		identifiers []string
		code        errs.Code
	}{
		"qcow2":        {model.VolumeFormatQCOW2, []string{"data"}, errs.CodeValidationFailed},
		"unknown":      {"unknown", []string{"data"}, errs.CodeValidationFailed},
		"empty format": {"", []string{"data"}, errs.CodeValidationFailed},
		"missing":      {model.VolumeFormatRaw, []string{"missing"}, errs.CodeVolumeNotFound},
		"partial":      {model.VolumeFormatRaw, []string{"data", "missing"}, errs.CodeVolumeNotFound},
	} {
		for _, operation := range []string{"attach", "hotplug", "resize"} {
			if operation == "resize" && len(tc.identifiers) > 1 {
				continue // Resize accepts only one identifier.
			}
			t.Run(operation+"/"+name, func(t *testing.T) {
				repo := testutil.NewVolumeRepo()
				vmRepo := testutil.NewVMRepo()
				path := filepath.Join(t.TempDir(), "data.disk")
				require.NoError(t, os.WriteFile(path, []byte("preserve data"), 0600))
				originalVolume := model.VolumeItem{
					ID: volumeTestVolumeID, Name: "data", Format: tc.format, Path: path,
					SizeBytes: 13, Status: model.VolumeStatusAvailable,
				}
				vol := originalVolume
				require.NoError(t, repo.Upsert(t.Context(), &vol))
				originalVM := model.VMItem{ID: volumeTestVMID, Name: "vm-test", Status: model.VMStatusStopped}
				if operation == "hotplug" {
					originalVM.Status = model.VMStatusRunning
					originalVM.PCIEnabled = true
				}
				vmItem := originalVM
				require.NoError(t, vmRepo.Upsert(t.Context(), &vmItem))
				runner := &testutil.FakeRunner{}
				old := system.DefaultRunner
				system.DefaultRunner = runner
				t.Cleanup(func() { system.DefaultRunner = old })
				op := &api.Operation{
					Repos:    api.Repos{Volume: repo, VM: vmRepo},
					Services: api.Services{Volume: volume.NewService(repo)},
					AuditLog: &logging.AuditLog{},
				}
				var err error
				if operation == "resize" {
					err = op.VolumeResize(t.Context(), inputs.VolumeCreateInput{Name: tc.identifiers[0], Size: "2MB"})
				} else {
					err = op.VolumeAttach(t.Context(), inputs.VolumeInput{
						Identifiers: tc.identifiers, VMIdentifier: "vm-test",
					})
				}
				require.Error(t, err)
				assert.Equal(t, tc.code, errs.AsDomainError(err).Code)
				if tc.code == errs.CodeValidationFailed {
					assert.Equal(t, errs.ClassValidation, errs.AsDomainError(err).Class)
				}
				assert.Empty(t, runner.Calls)
				storedVolume, err := repo.Get(t.Context(), vol.ID)
				require.NoError(t, err)
				testutil.AssertDiff(t, &originalVolume, storedVolume)
				storedVM, err := vmRepo.Get(t.Context(), vmItem.ID)
				require.NoError(t, err)
				testutil.AssertDiff(t, &originalVM, storedVM)
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				assert.Equal(t, "preserve data", string(data))
			})
		}
	}
}

// Rationale: VM creation must reject the whole request before creating networking,
// VM-specific files, or repository rows, even if some requested volumes resolved.
func TestVMCreateRejectsUnsupportedOrMissingVolumesBeforeEffects(t *testing.T) {
	t.Setenv("MVM_CACHE_DIR", t.TempDir())
	cacheDir, err := infra.GetCacheDir()
	require.NoError(t, err)
	for name, tc := range map[string]struct {
		format      model.VolumeFormat
		identifiers []string
		code        errs.Code
	}{
		"qcow2":   {model.VolumeFormatQCOW2, []string{"data"}, errs.CodeValidationFailed},
		"unknown": {"unknown", []string{"data"}, errs.CodeValidationFailed},
		"missing": {model.VolumeFormatRaw, []string{"missing"}, errs.CodeVolumeNotFound},
		"partial": {model.VolumeFormatRaw, []string{"data", "missing"}, errs.CodeVolumeNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			runner := &testutil.FakeRunner{}
			oldRunner, oldOS := system.DefaultRunner, system.DefaultOS
			system.DefaultRunner, system.DefaultOS = runner, &testutil.FakeOS{GeteuidVal: 0}
			t.Cleanup(func() { system.DefaultRunner, system.DefaultOS = oldRunner, oldOS })
			volumeRepo := testutil.NewVolumeRepo()
			vmRepo := testutil.NewVMRepo()
			imageRepo := testutil.NewImageRepo()
			kernelRepo := testutil.NewKernelRepo()
			networkRepo := testutil.NewNetworkRepo()
			binaryRepo := testutil.NewBinaryRepo()
			require.NoError(t, imageRepo.Upsert(t.Context(), &model.ImageItem{
				ID: "image-1", Name: "test-image", IsPresent: true,
			}))
			require.NoError(t, kernelRepo.Upsert(t.Context(), &model.KernelItem{ID: "kernel-1", IsPresent: true}))
			require.NoError(t, networkRepo.Upsert(t.Context(), &model.NetworkItem{ID: "network-1", IsPresent: true}))
			require.NoError(t, binaryRepo.Upsert(t.Context(), &model.BinaryItem{
				ID: "binary-1", Type: "firecracker", Architecture: system.RuntimeArch(), IsPresent: true,
			}))
			originalVolume := model.VolumeItem{
				ID: volumeTestVolumeID, Name: "data", Format: tc.format, Status: model.VolumeStatusAvailable,
			}
			vol := originalVolume
			require.NoError(t, volumeRepo.Upsert(t.Context(), &vol))
			op := &api.Operation{Repos: api.Repos{
				VM: vmRepo, Volume: volumeRepo, Image: imageRepo, Kernel: kernelRepo,
				Network: networkRepo, Binary: binaryRepo, Key: testutil.NewKeyRepo(),
			}}
			_, err := op.VMCreate(t.Context(), inputs.VMCreateInput{
				Name: "test-vm", ImageID: ptr.Ptr("test-image"), KernelID: ptr.Ptr("kernel-1"),
				NetworkID: ptr.Ptr("network-1"), BinaryID: ptr.Ptr("binary-1"), Volumes: tc.identifiers,
			}, nil)
			require.Error(t, err)
			assert.Equal(t, tc.code, errs.AsDomainError(err).Code)
			assert.Empty(t, runner.Calls)
			// The existing path getter creates the shared vms parent during resolution.
			entries, err := os.ReadDir(filepath.Join(cacheDir, "vms"))
			require.NoError(t, err)
			assert.Empty(t, entries, "no VM-specific artifacts may be created")
			vms, err := vmRepo.ListAll(t.Context())
			require.NoError(t, err)
			assert.Empty(t, vms)
			stored, err := volumeRepo.Get(t.Context(), vol.ID)
			require.NoError(t, err)
			testutil.AssertDiff(t, &originalVolume, stored)
		})
	}
}
