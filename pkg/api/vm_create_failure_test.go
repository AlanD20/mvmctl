package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/binary"
	"mvmctl/internal/core/config"
	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/system"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

// Rationale: a real builder failure creates no row, so the attempted canonical
// selector must survive the public API error rather than depend on SQL or logs.
// The incomplete image fails before any provisioning or privileged launch.
func TestVMCreateFailureRetainsAttemptedSelectorsWithoutRows(t *testing.T) {
	if os.Getenv("MVM_VM_CREATE_FAILURE_TEST_CHILD") != "1" {
		// GetCacheDir is once-cached; use the existing API test child-process
		// pattern rather than resetting production state or sharing another test's cache.
		command := exec.CommandContext(t.Context(), os.Args[0],
			"-test.run=^TestVMCreateFailureRetainsAttemptedSelectorsWithoutRows$")
		command.Env = append(os.Environ(), "MVM_VM_CREATE_FAILURE_TEST_CHILD=1", "MVM_CACHE_DIR="+t.TempDir())
		output, err := command.CombinedOutput()
		require.NoError(t, err, string(output))
		return
	}
	cacheDir, err := infra.GetCacheDir()
	require.NoError(t, err)
	for _, tc := range []struct {
		name   string
		vmName string
		count  int
		atomic bool
	}{
		{name: "single", vmName: "failed-one", count: 1},
		{name: "all failed", vmName: "failed-many", count: 2},
		{name: "atomic all failed", vmName: "failed-atomic", count: 2, atomic: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kernelPath := filepath.Join(t.TempDir(), "kernel")
			require.NoError(t, os.WriteFile(kernelPath, []byte("kernel"), 0600))
			vmRepo := testutil.NewVMRepo()
			imageRepo := testutil.NewImageRepo()
			kernelRepo := testutil.NewKernelRepo()
			binaryRepo := testutil.NewBinaryRepo()
			require.NoError(t, imageRepo.Upsert(t.Context(), &model.ImageItem{
				ID: "incomplete-image", IsPresent: true, IsDefault: true, MinRootfsSizeMiB: 16,
				FSUUID: "image-fs", // Deliberately missing FSType: the real clone owner rejects it.
			}))
			require.NoError(t, kernelRepo.Upsert(t.Context(), &model.KernelItem{
				ID: "kernel", IsPresent: true, IsDefault: true, Path: kernelPath,
			}))
			arch := system.RuntimeArch()
			for _, kind := range []string{"firecracker", "jailer"} {
				version := infra.QualifiedVMLaunchFirecrackerVersion
				require.NoError(t, binaryRepo.Upsert(t.Context(), &model.BinaryItem{
					ID: kind, Type: kind, Architecture: arch, Version: version, IsPresent: true,
					IsDefault: true, Path: filepath.Join(infra.TrustedBinaryRoot, arch, version, kind),
				}))
			}
			op := &Operation{
				CacheDir: cacheDir,
				Repos: Repos{
					VM: vmRepo, Image: imageRepo, Kernel: kernelRepo, Binary: binaryRepo,
					Network: testutil.NewNetworkRepo(), Key: testutil.NewKeyRepo(),
					Volume: testutil.NewVolumeRepo(), Lease: testutil.NewLeaseRepo(),
				},
				Services: Services{
					Config: config.NewService(testutil.NewConfigRepo(), config.NewConstraintRegistry()),
					Binary: binary.NewService(binaryRepo, arch),
				},
			}
			created, err := op.VMCreate(t.Context(), inputs.VMCreateInput{
				Name: tc.vmName, NoNetwork: true, Count: &tc.count, Atomic: tc.atomic, SkipCleanup: true,
			}, nil)
			require.ErrorContains(t, err, "fsType is required")
			require.Nil(t, created)
			domain := errs.AsDomainError(err)
			require.NotNil(t, domain)
			require.Equal(t, errs.CodeVMCreateFailure, domain.Code)
			require.Equal(t, errs.ClassInternal, domain.Class)
			require.Equal(t, "vm", domain.Op)
			require.Equal(t, []string{}, domain.Details["created_vm_ids"])
			encoded, err := json.Marshal(domain.Details["failed_vms"])
			require.NoError(t, err)
			var failures []struct {
				VMID string `json:"vm_id"`
				Name string `json:"name"`
			}
			require.NoError(t, json.Unmarshal(encoded, &failures))
			require.Len(t, failures, tc.count)
			for idx, failure := range failures {
				wantName := tc.vmName
				if idx > 0 {
					wantName = fmt.Sprintf("%s-%d", tc.vmName, idx+1)
				}
				require.Equal(t, wantName, failure.Name)
				require.Regexp(t, `^[0-9a-f]{32}$`, failure.VMID)
				info, statErr := os.Stat(filepath.Join(cacheDir, "vms", failure.VMID))
				require.NoError(t, statErr, "selector must identify the directory actually attempted")
				require.True(t, info.IsDir())
			}
			if tc.count == 1 {
				require.Equal(t, failures[0].VMID, domain.Entity)
			} else {
				require.Empty(t, domain.Entity)
			}
			rows, err := vmRepo.ListAll(t.Context())
			require.NoError(t, err)
			require.Empty(t, rows)
		})
	}
}

// Rationale: concurrent completion order, atomic cancellation and joined recovery
// errors interact. Collection must drain after cancellation, retain real causes,
// and expose successes without silently discarding non-atomic failures.
// Completed items are collector inputs, not evidence of a privileged launch.
func TestCollectVMCreateResultsPreservesMixedFailuresAndRecovery(t *testing.T) {
	for _, atomic := range []bool{false, true} {
		t.Run(fmt.Sprintf("atomic=%t", atomic), func(t *testing.T) {
			reader, writer, err := os.Pipe()
			require.NoError(t, err)
			require.NoError(t, reader.Close())
			t.Cleanup(func() { _ = writer.Close() })
			_, pipeErr := writer.Write([]byte("launch request"))
			require.ErrorIs(t, pipeErr, syscall.EPIPE)
			cleanupErr := errors.New("recovery unavailable")
			primary := errs.WrapMsg(errs.CodeProcessError, "launch write failed", pipeErr,
				errs.WithClass(errs.ClassInternal), errs.WithEntity("attempt-2"),
				errs.WithDetails(map[string]any{"blocked_launch_process_cleanup_failed": true}),
			)
			recovery := errs.WrapMsg(errs.CodeVMCgroupCleanupFailed, "retain pending root state", cleanupErr,
				errs.WithClass(errs.ClassConflict), errs.WithEntity("root-attempt-2"),
				errs.WithDetails(map[string]any{"generation": 1}),
			)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			results := make(chan vmCreateResult)
			go func() {
				results <- vmCreateResult{idx: 1, vmID: "attempt-2", err: errors.Join(primary, recovery)}
				if atomic {
					<-ctx.Done() // The collector must cancel before waiting for the next result.
				}
				results <- vmCreateResult{idx: 2, vmID: "attempt-3", vm: &model.VMItem{ID: "attempt-3"}}
				results <- vmCreateResult{idx: 0, vmID: "attempt-1", vm: &model.VMItem{ID: "attempt-1"}}
			}()
			outcome := collectVMCreateResults([]string{"one", "two", "three"}, results, atomic, cancel)
			if atomic {
				require.ErrorIs(t, ctx.Err(), context.Canceled, "must cancel without waiting for timeout")
			}
			code := errs.CodeVMCreateFailure
			if atomic {
				code = errs.CodeVMAtomicFailed
			}
			domain := outcome.failure(code, "batch failed")
			require.ErrorIs(t, domain, pipeErr)
			require.ErrorIs(t, domain, cleanupErr)
			require.Equal(t, "attempt-2", domain.Entity)
			require.Equal(t, primary, errs.AsDomainError(domain.Err))
			wantVMs := []*model.VMItem{{ID: "attempt-1"}, {ID: "attempt-3"}}
			if diff := cmp.Diff(wantVMs, outcome.createdVMs); diff != "" {
				t.Errorf("completed items (-want +got):\n%s", diff)
			}
			encoded, err := json.Marshal(domain.Details)
			require.NoError(t, err)
			var got map[string]any
			require.NoError(t, json.Unmarshal(encoded, &got))
			want := map[string]any{
				"created_vm_ids": []any{"attempt-1", "attempt-3"},
				"failed_vms": []any{map[string]any{
					"vm_id": "attempt-2", "name": "two", "errors": []any{
						map[string]any{
							"code": "process.error", "class": float64(4), "message": "launch write failed",
							"op": "process", "entity": "attempt-2",
							"details": map[string]any{"blocked_launch_process_cleanup_failed": true},
						},
						map[string]any{
							"code":    "vm.cgroup.cleanup_failed",
							"class":   float64(2),
							"message": "retain pending root state",
							"op":      "vm",
							"entity":  "root-attempt-2",
							"details": map[string]any{"generation": float64(1)},
						},
					},
				}},
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("recovery diagnostics (-want +got):\n%s", diff)
			}
		})
	}
}
