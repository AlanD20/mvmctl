package jailer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// Rationale: The privileged boundary must reject a freshly observed out-of-range size before reading caller bytes or
// allocating the root-owned capability.
func TestPrepareManagedFirecrackerConfigRejectsSizeBeforeReadOrMemfd(t *testing.T) {
	tests := map[string]uint64{
		"empty":          0,
		"above maximum":  managedVMConfigMaxBytes + 1,
		"maximum uint64": ^uint64(0),
	}
	for name, size := range tests {
		t.Run(name, func(t *testing.T) {
			deps, sourceFD := managedFirecrackerConfigTestSource(t, []byte("{}"))
			readCalls := 0
			memfdCalls := 0
			deps.statx = func(_ context.Context, _ int, _ int, mask int, stat *unix.Statx_t) error {
				*stat = unix.Statx_t{
					Mask: uint32(mask), Mode: unix.S_IFREG | 0400, Uid: uint32(os.Geteuid()), Gid: uint32(os.Getegid()),
					Nlink: 1, Ino: 1, Size: size, Dev_major: 1,
				}
				return nil
			}
			deps.pread = func(context.Context, int, []byte, int64) (int, error) {
				readCalls++
				return 0, nil
			}
			deps.memfdCreate = func(context.Context, string, int) (int, error) {
				memfdCalls++
				return -1, errors.New("must not create")
			}
			closes := observeManagedFirecrackerConfigCloses(&deps)

			prepared, err := prepareManagedFirecrackerConfig(
				context.Background(),
				deps,
				sourceFD,
				testVMFirecrackerLaunchIntent(cloudInitAbsent),
			)
			require.Error(t, err)
			assert.Nil(t, prepared)
			assert.Zero(t, readCalls)
			assert.Zero(t, memfdCalls)
			assert.Zero(t, closes.memfdCreateCalls)
			assert.Equal(
				t,
				[]managedFirecrackerConfigClosePhase{managedFirecrackerSourceClosePhase},
				closes.closePhases,
			)
		})
	}
}

// Rationale: One byte is the exact lower size boundary. Size admission must not silently require valid JSON.
func TestReadStableManagedFirecrackerConfigAcceptsOneByteLowerBound(t *testing.T) {
	deps, sourceFD := managedFirecrackerConfigTestSource(t, []byte("{"))
	t.Cleanup(func() { require.NoError(t, deps.close(context.Background(), sourceFD)) })

	got, err := readStableManagedFirecrackerConfig(context.Background(), deps, sourceFD)
	require.NoError(t, err)
	if diff := cmp.Diff([]byte("{"), got); diff != "" {
		t.Errorf("one-byte configuration mismatch (-want +got):\n%s", diff)
	}
}

// Rationale: Positioned reads may legally be partial, while impossible counts or no progress must fail closed.
func TestPrepareManagedFirecrackerConfigHandlesSourceReadCounts(t *testing.T) {
	tests := []struct {
		name     string
		inject   func(managedFirecrackerConfigDeps, int) func(context.Context, int, []byte, int64) (int, error)
		wantCode errs.Code
	}{
		{
			name: "partial reads complete",
			inject: func(real managedFirecrackerConfigDeps, sourceFD int) func(
				context.Context,
				int,
				[]byte,
				int64,
			) (int, error) {
				return func(ctx context.Context, fd int, value []byte, offset int64) (int, error) {
					if fd == sourceFD && len(value) > 1 {
						value = value[:1]
					}
					return real.pread(ctx, fd, value, offset)
				}
			},
		},
		{
			name: "zero read",
			inject: func(_ managedFirecrackerConfigDeps, sourceFD int) func(
				context.Context,
				int,
				[]byte,
				int64,
			) (int, error) {
				return func(_ context.Context, fd int, _ []byte, offset int64) (int, error) {
					if fd == sourceFD && offset == 0 {
						return 0, nil
					}
					return 0, nil
				}
			},
			wantCode: errs.CodeValidationFailed,
		},
		{
			name: "negative read",
			inject: func(real managedFirecrackerConfigDeps, sourceFD int) func(
				context.Context,
				int,
				[]byte,
				int64,
			) (int, error) {
				return func(ctx context.Context, fd int, value []byte, offset int64) (int, error) {
					if fd == sourceFD && offset == 0 {
						return -1, nil
					}
					return real.pread(ctx, fd, value, offset)
				}
			},
			wantCode: errs.CodeVMAtomicFailed,
		},
		{
			name: "oversized read",
			inject: func(real managedFirecrackerConfigDeps, sourceFD int) func(
				context.Context,
				int,
				[]byte,
				int64,
			) (int, error) {
				return func(ctx context.Context, fd int, value []byte, offset int64) (int, error) {
					if fd == sourceFD && offset == 0 {
						return len(value) + 1, nil
					}
					return real.pread(ctx, fd, value, offset)
				}
			},
			wantCode: errs.CodeVMAtomicFailed,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			intent := testVMFirecrackerLaunchIntent(cloudInitAbsent)
			raw := mustMarshalManagedFirecrackerConfig(t, buildManagedFirecrackerConfig(intent))
			deps, sourceFD := managedFirecrackerConfigTestSource(t, raw)
			prepareManagedFirecrackerRootDepsForUnprivilegedTest(&deps)
			real := deps
			deps.pread = tc.inject(real, sourceFD)
			closes := observeManagedFirecrackerConfigCloses(&deps)

			prepared, err := prepareManagedFirecrackerConfig(context.Background(), deps, sourceFD, intent)
			if tc.wantCode != "" {
				assert.Nil(t, prepared)
				requireManagedVMResourceDomainErrorCode(t, err, tc.wantCode)
				assert.Zero(t, closes.memfdCreateCalls)
				assert.Equal(
					t,
					[]managedFirecrackerConfigClosePhase{managedFirecrackerSourceClosePhase},
					closes.closePhases,
				)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, prepared)
			assert.True(t, closes.sourceClosedBeforeMemfd)
			assert.Equal(t, 1, closes.memfdCreateCalls)
			assert.Equal(
				t,
				[]managedFirecrackerConfigClosePhase{managedFirecrackerSourceClosePhase},
				closes.closePhases,
			)
			require.NoError(t, prepared.Release(context.Background()))
			assert.Equal(
				t,
				[]managedFirecrackerConfigClosePhase{
					managedFirecrackerSourceClosePhase,
					managedFirecrackerPreparedClosePhase,
				},
				closes.closePhases,
			)
		})
	}
}

// Rationale: A stable initial size cannot authorize bytes added at EOF or bytes missing before the declared end.
func TestPrepareManagedFirecrackerConfigRejectsSourceEOFGrowthAndTruncation(t *testing.T) {
	tests := []struct {
		name     string
		inject   func(managedFirecrackerConfigDeps, int, int) func(context.Context, int, []byte, int64) (int, error)
		wantCode errs.Code
	}{
		{
			name: "growth at EOF",
			inject: func(real managedFirecrackerConfigDeps, sourceFD, rawBytes int) func(
				context.Context,
				int,
				[]byte,
				int64,
			) (int, error) {
				return func(ctx context.Context, fd int, value []byte, offset int64) (int, error) {
					if fd == sourceFD && offset == int64(rawBytes) {
						value[0] = 'x'
						return 1, nil
					}
					return real.pread(ctx, fd, value, offset)
				}
			},
			wantCode: errs.CodeValidationFailed,
		},
		{
			name: "truncation before EOF",
			inject: func(real managedFirecrackerConfigDeps, sourceFD, _ int) func(
				context.Context,
				int,
				[]byte,
				int64,
			) (int, error) {
				return func(ctx context.Context, fd int, value []byte, offset int64) (int, error) {
					if fd == sourceFD && offset == 0 {
						return real.pread(ctx, fd, value[:len(value)-1], offset)
					}
					if fd == sourceFD {
						return 0, nil
					}
					return real.pread(ctx, fd, value, offset)
				}
			},
			wantCode: errs.CodeValidationFailed,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			intent := testVMFirecrackerLaunchIntent(cloudInitAbsent)
			raw := mustMarshalManagedFirecrackerConfig(t, buildManagedFirecrackerConfig(intent))
			deps, sourceFD := managedFirecrackerConfigTestSource(t, raw)
			real := deps
			deps.pread = tc.inject(real, sourceFD, len(raw))
			closes := observeManagedFirecrackerConfigCloses(&deps)

			prepared, err := prepareManagedFirecrackerConfig(context.Background(), deps, sourceFD, intent)
			assert.Nil(t, prepared)
			requireManagedVMResourceDomainErrorCode(t, err, tc.wantCode)
			assert.Zero(t, closes.memfdCreateCalls)
			assert.Equal(
				t,
				[]managedFirecrackerConfigClosePhase{managedFirecrackerSourceClosePhase},
				closes.closePhases,
			)
		})
	}
}

// Rationale: Cancellation during a partial source read must stop before memfd creation and still consume the source.
func TestPrepareManagedFirecrackerConfigCancelsDuringSourceRead(t *testing.T) {
	intent := testVMFirecrackerLaunchIntent(cloudInitAbsent)
	raw := mustMarshalManagedFirecrackerConfig(t, buildManagedFirecrackerConfig(intent))
	deps, sourceFD := managedFirecrackerConfigTestSource(t, raw)
	realPread := deps.pread
	ctx, cancel := context.WithCancel(context.Background())
	deps.pread = func(readCtx context.Context, fd int, value []byte, offset int64) (int, error) {
		if fd == sourceFD && offset == 0 {
			count, err := realPread(readCtx, fd, value[:1], offset)
			cancel()
			return count, err
		}
		return realPread(readCtx, fd, value, offset)
	}
	memfdCalls := 0
	deps.memfdCreate = func(context.Context, string, int) (int, error) {
		memfdCalls++
		return -1, errors.New("must not create")
	}
	closes := observeManagedFirecrackerConfigCloses(&deps)

	prepared, err := prepareManagedFirecrackerConfig(ctx, deps, sourceFD, intent)
	assert.Nil(t, prepared)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, memfdCalls)
	assert.Zero(t, closes.memfdCreateCalls)
	assert.Equal(
		t,
		[]managedFirecrackerConfigClosePhase{managedFirecrackerSourceClosePhase},
		closes.closePhases,
	)
}

// Rationale: Canonical memfd writes must tolerate partial progress and reject impossible counts or stalled writes.
func TestPrepareManagedFirecrackerConfigHandlesCanonicalWriteCounts(t *testing.T) {
	tests := []struct {
		name     string
		inject   func(managedFirecrackerConfigDeps) func(context.Context, int, []byte, int64) (int, error)
		wantCode errs.Code
	}{
		{
			name: "partial writes complete",
			inject: func(real managedFirecrackerConfigDeps) func(
				context.Context,
				int,
				[]byte,
				int64,
			) (int, error) {
				return func(ctx context.Context, fd int, value []byte, offset int64) (int, error) {
					if len(value) > 1 {
						value = value[:1]
					}
					return real.pwrite(ctx, fd, value, offset)
				}
			},
		},
		{
			name: "zero write",
			inject: func(managedFirecrackerConfigDeps) func(
				context.Context,
				int,
				[]byte,
				int64,
			) (int, error) {
				return func(context.Context, int, []byte, int64) (int, error) { return 0, nil }
			},
			wantCode: errs.CodeVMAtomicFailed,
		},
		{
			name: "negative write",
			inject: func(managedFirecrackerConfigDeps) func(
				context.Context,
				int,
				[]byte,
				int64,
			) (int, error) {
				return func(context.Context, int, []byte, int64) (int, error) { return -1, nil }
			},
			wantCode: errs.CodeVMAtomicFailed,
		},
		{
			name: "oversized write",
			inject: func(managedFirecrackerConfigDeps) func(
				context.Context,
				int,
				[]byte,
				int64,
			) (int, error) {
				return func(_ context.Context, _ int, value []byte, _ int64) (int, error) {
					return len(value) + 1, nil
				}
			},
			wantCode: errs.CodeVMAtomicFailed,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			intent := testVMFirecrackerLaunchIntent(cloudInitAbsent)
			raw := mustMarshalManagedFirecrackerConfig(t, buildManagedFirecrackerConfig(intent))
			deps, sourceFD := managedFirecrackerConfigTestSource(t, raw)
			prepareManagedFirecrackerRootDepsForUnprivilegedTest(&deps)
			real := deps
			deps.pwrite = tc.inject(real)
			closes := observeManagedFirecrackerConfigCloses(&deps)

			prepared, err := prepareManagedFirecrackerConfig(context.Background(), deps, sourceFD, intent)
			if tc.wantCode != "" {
				assert.Nil(t, prepared)
				requireManagedVMResourceDomainErrorCode(t, err, tc.wantCode)
				assert.True(t, closes.sourceClosedBeforeMemfd)
				assert.Equal(t, 1, closes.memfdCreateCalls)
				assert.Equal(
					t,
					[]managedFirecrackerConfigClosePhase{
						managedFirecrackerSourceClosePhase,
						managedFirecrackerPreparedClosePhase,
					},
					closes.closePhases,
				)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, prepared)
			assert.True(t, closes.sourceClosedBeforeMemfd)
			assert.Equal(t, 1, closes.memfdCreateCalls)
			assert.Equal(
				t,
				[]managedFirecrackerConfigClosePhase{managedFirecrackerSourceClosePhase},
				closes.closePhases,
			)
			require.NoError(t, prepared.Release(context.Background()))
			assert.Equal(
				t,
				[]managedFirecrackerConfigClosePhase{
					managedFirecrackerSourceClosePhase,
					managedFirecrackerPreparedClosePhase,
				},
				closes.closePhases,
			)
		})
	}
}

// Rationale: Cancellation after partial canonical output must close the rejected memfd and retain no capability.
func TestPrepareManagedFirecrackerConfigCancelsDuringCanonicalWrite(t *testing.T) {
	intent := testVMFirecrackerLaunchIntent(cloudInitAbsent)
	raw := mustMarshalManagedFirecrackerConfig(t, buildManagedFirecrackerConfig(intent))
	deps, sourceFD := managedFirecrackerConfigTestSource(t, raw)
	prepareManagedFirecrackerRootDepsForUnprivilegedTest(&deps)
	realPwrite := deps.pwrite
	ctx, cancel := context.WithCancel(context.Background())
	deps.pwrite = func(writeCtx context.Context, fd int, value []byte, offset int64) (int, error) {
		count, err := realPwrite(writeCtx, fd, value[:1], offset)
		cancel()
		return count, err
	}
	closes := observeManagedFirecrackerConfigCloses(&deps)

	prepared, err := prepareManagedFirecrackerConfig(ctx, deps, sourceFD, intent)
	assert.Nil(t, prepared)
	assert.ErrorIs(t, err, context.Canceled)
	assert.True(t, closes.sourceClosedBeforeMemfd)
	assert.Equal(t, 1, closes.memfdCreateCalls)
	assert.Equal(
		t,
		[]managedFirecrackerConfigClosePhase{
			managedFirecrackerSourceClosePhase,
			managedFirecrackerPreparedClosePhase,
		},
		closes.closePhases,
	)
}

// Rationale: The post-seal readback is part of authority admission. Cancellation after partial readback must reject
// and close the sealed descriptor instead of publishing a capability whose verification did not finish.
func TestPrepareManagedFirecrackerConfigCancelsDuringCanonicalReadback(t *testing.T) {
	intent := testVMFirecrackerLaunchIntent(cloudInitAbsent)
	raw := mustMarshalManagedFirecrackerConfig(t, buildManagedFirecrackerConfig(intent))
	deps, sourceFD := managedFirecrackerConfigTestSource(t, raw)
	prepareManagedFirecrackerRootDepsForUnprivilegedTest(&deps)
	ctx, cancel := context.WithCancel(context.Background())
	createdFD := -1
	realCreate := deps.memfdCreate
	deps.memfdCreate = func(ctx context.Context, name string, flags int) (int, error) {
		fd, err := realCreate(ctx, name, flags)
		createdFD = fd
		return fd, err
	}
	realPread := deps.pread
	deps.pread = func(ctx context.Context, fd int, value []byte, offset int64) (int, error) {
		if fd == createdFD && offset == 0 {
			count, err := realPread(ctx, fd, value[:1], offset)
			cancel()
			return count, err
		}
		return realPread(ctx, fd, value, offset)
	}
	closes := observeManagedFirecrackerConfigCloses(&deps)

	prepared, err := prepareManagedFirecrackerConfig(ctx, deps, sourceFD, intent)
	assert.Nil(t, prepared)
	assert.ErrorIs(t, err, context.Canceled)
	assert.True(t, closes.sourceClosedBeforeMemfd)
	wantClosePhases := []managedFirecrackerConfigClosePhase{
		managedFirecrackerSourceClosePhase,
		managedFirecrackerPreparedClosePhase,
	}
	if diff := cmp.Diff(wantClosePhases, closes.closePhases); diff != "" {
		t.Errorf("configuration cleanup phases mismatch (-want +got):\n%s", diff)
	}
}

// Rationale: Every syscall and readback stage constructing root authority must fail closed and release its memfd.
func TestPrepareManagedFirecrackerConfigRejectsIndividualMemfdFaults(t *testing.T) {
	tests := []struct {
		name              string
		wantPreparedClose bool
		inject            func(*managedFirecrackerConfigDeps, *int, int, error)
	}{
		{
			name: "memfd create",
			inject: func(deps *managedFirecrackerConfigDeps, _ *int, _ int, cause error) {
				deps.memfdCreate = func(context.Context, string, int) (int, error) { return -1, cause }
			},
		},
		{
			name:              "initial fstat",
			wantPreparedClose: true,
			inject: func(deps *managedFirecrackerConfigDeps, _ *int, _ int, cause error) {
				deps.fstat = func(context.Context, int, *unix.Stat_t) error { return cause }
			},
		},
		{
			name:              "fchown",
			wantPreparedClose: true,
			inject: func(deps *managedFirecrackerConfigDeps, _ *int, _ int, cause error) {
				deps.fchown = func(context.Context, int, int, int) error { return cause }
			},
		},
		{
			name:              "fchmod",
			wantPreparedClose: true,
			inject: func(deps *managedFirecrackerConfigDeps, _ *int, _ int, cause error) {
				deps.fchmod = func(context.Context, int, uint32) error { return cause }
			},
		},
		{
			name:              "final fstat before readback",
			wantPreparedClose: true,
			inject: func(deps *managedFirecrackerConfigDeps, _ *int, _ int, cause error) {
				realFstat := deps.fstat
				calls := 0
				deps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
					calls++
					if calls == 2 {
						return cause
					}
					return realFstat(ctx, fd, stat)
				}
			},
		},
		{
			name:              "descriptor flags",
			wantPreparedClose: true,
			inject: func(deps *managedFirecrackerConfigDeps, _ *int, _ int, cause error) {
				deps.getFDFlags = func(context.Context, int) (int, error) { return 0, cause }
			},
		},
		{
			name:              "descriptor seek",
			wantPreparedClose: true,
			inject: func(deps *managedFirecrackerConfigDeps, _ *int, _ int, cause error) {
				deps.seek = func(context.Context, int, int64, int) (int64, error) { return 0, cause }
			},
		},
		{
			name:              "seal inspection",
			wantPreparedClose: true,
			inject: func(deps *managedFirecrackerConfigDeps, _ *int, _ int, cause error) {
				deps.getSeals = func(context.Context, int) (int, error) { return 0, cause }
			},
		},
		{
			name:              "readback",
			wantPreparedClose: true,
			inject: func(deps *managedFirecrackerConfigDeps, createdFD *int, _ int, cause error) {
				realPread := deps.pread
				deps.pread = func(ctx context.Context, fd int, value []byte, offset int64) (int, error) {
					if fd == *createdFD && offset == 0 {
						return 0, cause
					}
					return realPread(ctx, fd, value, offset)
				}
			},
		},
		{
			name:              "readback probe",
			wantPreparedClose: true,
			inject: func(deps *managedFirecrackerConfigDeps, createdFD *int, canonicalBytes int, cause error) {
				realPread := deps.pread
				deps.pread = func(ctx context.Context, fd int, value []byte, offset int64) (int, error) {
					if fd == *createdFD && offset == int64(canonicalBytes) {
						return 0, cause
					}
					return realPread(ctx, fd, value, offset)
				}
			},
		},
		{
			name:              "final fstat after readback",
			wantPreparedClose: true,
			inject: func(deps *managedFirecrackerConfigDeps, _ *int, _ int, cause error) {
				realFstat := deps.fstat
				calls := 0
				deps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
					calls++
					if calls == 3 {
						return cause
					}
					return realFstat(ctx, fd, stat)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			intent := testVMFirecrackerLaunchIntent(cloudInitAbsent)
			canonical := mustMarshalManagedFirecrackerConfig(t, buildManagedFirecrackerConfig(intent))
			deps, sourceFD := managedFirecrackerConfigTestSource(t, canonical)
			prepareManagedFirecrackerRootDepsForUnprivilegedTest(&deps)
			createdFD := -1
			realCreate := deps.memfdCreate
			deps.memfdCreate = func(ctx context.Context, name string, flags int) (int, error) {
				fd, err := realCreate(ctx, name, flags)
				createdFD = fd
				return fd, err
			}
			cause := errors.New("injected " + tc.name + " failure")
			tc.inject(&deps, &createdFD, len(canonical), cause)
			closes := observeManagedFirecrackerConfigCloses(&deps)

			prepared, err := prepareManagedFirecrackerConfig(context.Background(), deps, sourceFD, intent)
			assert.Nil(t, prepared)
			requireManagedVMResourceDomainErrorCode(t, err, errs.CodeVMAtomicFailed)
			assert.ErrorIs(t, err, cause)
			assert.True(t, closes.sourceClosedBeforeMemfd)
			assert.Equal(t, 1, closes.memfdCreateCalls)
			wantClosePhases := []managedFirecrackerConfigClosePhase{managedFirecrackerSourceClosePhase}
			if tc.wantPreparedClose {
				wantClosePhases = append(wantClosePhases, managedFirecrackerPreparedClosePhase)
			}
			assert.Equal(t, wantClosePhases, closes.closePhases)
		})
	}
}

// Rationale: Caller JSON is admission evidence only; every launch-affecting field must match the root-built intent.
func TestPrepareManagedFirecrackerConfigRejectsEveryIntentMismatch(t *testing.T) {
	baseline := testVMFirecrackerLaunchIntent(cloudInitPresent)
	raw := mustMarshalManagedFirecrackerConfig(t, buildManagedFirecrackerConfig(baseline))
	mutations := map[string]func(*vmFirecrackerLaunchIntent){
		"boot arguments": func(intent *vmFirecrackerLaunchIntent) { intent.bootArgs += " forged=1" },
		"VCPU count":     func(intent *vmFirecrackerLaunchIntent) { intent.vcpuCount++ },
		"memory":         func(intent *vmFirecrackerLaunchIntent) { intent.memSizeMiB++ },
		"SMT":            func(intent *vmFirecrackerLaunchIntent) { intent.smt = !intent.smt },
		"dirty tracking": func(intent *vmFirecrackerLaunchIntent) {
			intent.trackDirtyPages = !intent.trackDirtyPages
		},
		"guest MAC": func(intent *vmFirecrackerLaunchIntent) { intent.guestMAC = "06:00:00:00:00:02" },
		"TAP identity": func(intent *vmFirecrackerLaunchIntent) {
			intent.tapIdentity = vmFirecrackerTapIdentity{name: "mvtap876543210"}
		},
		"rootfs cache": func(intent *vmFirecrackerLaunchIntent) { intent.rootfsCacheType = model.CacheTypeUnsafe },
		"cloud-init":   func(intent *vmFirecrackerLaunchIntent) { intent.cloudInit = cloudInitAbsent },
		"logger presence": func(intent *vmFirecrackerLaunchIntent) {
			intent.enableLogging = false
			intent.logLevel = ""
		},
		"logger level":     func(intent *vmFirecrackerLaunchIntent) { intent.logLevel = "Debug" },
		"metrics presence": func(intent *vmFirecrackerLaunchIntent) { intent.enableMetrics = false },
		"CPU config":       func(intent *vmFirecrackerLaunchIntent) { intent.cpuConfig = nil },
		"vsock CID":        func(intent *vmFirecrackerLaunchIntent) { *intent.vsockCID++ },
		"vsock presence":   func(intent *vmFirecrackerLaunchIntent) { intent.vsockCID = nil },
	}

	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			intent := cloneVMFirecrackerLaunchIntent(baseline)
			mutate(&intent)
			deps, sourceFD := managedFirecrackerConfigTestSource(t, raw)
			memfdCalls := 0
			deps.memfdCreate = func(context.Context, string, int) (int, error) {
				memfdCalls++
				return -1, errors.New("must not create")
			}
			closes := observeManagedFirecrackerConfigCloses(&deps)

			prepared, err := prepareManagedFirecrackerConfig(context.Background(), deps, sourceFD, intent)
			require.Error(t, err)
			assert.Nil(t, prepared)
			requireManagedVMResourceDomainErrorCode(t, err, errs.CodeValidationFailed)
			assert.Zero(t, memfdCalls)
			assert.Zero(t, closes.memfdCreateCalls)
			assert.Equal(
				t,
				[]managedFirecrackerConfigClosePhase{managedFirecrackerSourceClosePhase},
				closes.closePhases,
			)
		})
	}
}

// Rationale: Package-private construction must not let malformed typed intent cross the privileged effect boundary.
func TestValidateVMFirecrackerLaunchIntentRejectsForgedValues(t *testing.T) {
	tests := map[string]func(*vmFirecrackerLaunchIntent){
		"empty boot arguments": func(intent *vmFirecrackerLaunchIntent) { intent.bootArgs = "" },
		"NUL boot argument":    func(intent *vmFirecrackerLaunchIntent) { intent.bootArgs += "\x00" },
		"VCPU below minimum":   func(intent *vmFirecrackerLaunchIntent) { intent.vcpuCount = infra.VCPUMin - 1 },
		"VCPU above maximum":   func(intent *vmFirecrackerLaunchIntent) { intent.vcpuCount = infra.VCPUMax + 1 },
		"memory below minimum": func(intent *vmFirecrackerLaunchIntent) { intent.memSizeMiB = infra.MemMinMB - 1 },
		"memory above maximum": func(intent *vmFirecrackerLaunchIntent) { intent.memSizeMiB = infra.MemMaxMB + 1 },
		"noncanonical MAC":     func(intent *vmFirecrackerLaunchIntent) { intent.guestMAC = "06-00-00-00-00-01" },
		"invalid TAP": func(intent *vmFirecrackerLaunchIntent) {
			intent.tapIdentity = vmFirecrackerTapIdentity{name: "tap/escape"}
		},
		"invalid cache":      func(intent *vmFirecrackerLaunchIntent) { intent.rootfsCacheType = "forged" },
		"invalid cloud-init": func(intent *vmFirecrackerLaunchIntent) { intent.cloudInit = 0 },
		"invalid log level":  func(intent *vmFirecrackerLaunchIntent) { intent.logLevel = "Trace" },
		"disabled logger data": func(intent *vmFirecrackerLaunchIntent) {
			intent.enableLogging = false
		},
		"incomplete CPU config": func(intent *vmFirecrackerLaunchIntent) {
			intent.cpuConfig.KvmCapabilities = nil
		},
		"reserved vsock CID": func(intent *vmFirecrackerLaunchIntent) { *intent.vsockCID = 2 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			intent := testVMFirecrackerLaunchIntent(cloudInitAbsent)
			mutate(&intent)
			err := validateVMFirecrackerLaunchIntent(intent)
			requireManagedVMResourceDomainErrorCode(t, err, errs.CodeValidationFailed)
		})
	}
}

func TestBuildManagedFirecrackerConfigDerivesCompleteClosedConfiguration(t *testing.T) {
	vsockCID := uint32(42)
	intent := vmFirecrackerLaunchIntent{
		networkPlacement: model.VMNetworkPlacementManaged,
		bootArgs:         "console=ttyS0 reboot=k panic=1",
		vcpuCount:        2,
		memSizeMiB:       512,
		smt:              true,
		trackDirtyPages:  true,
		guestMAC:         "06:00:00:00:00:01",
		tapIdentity:      vmFirecrackerTapIdentity{name: "mvtap012345678"},
		rootfsCacheType:  model.CacheTypeWriteback,
		cloudInit:        cloudInitPresent,
		enableLogging:    true,
		logLevel:         "Info",
		enableMetrics:    true,
		cpuConfig: &model.CpuConfig{
			KvmCapabilities: []string{"KVM_CAP_ONE_REG"},
		},
		vsockCID: &vsockCID,
	}

	// Contract literals are the only paths and fixed device values the receiver may authorize in this base slice.
	want := managedFirecrackerConfig{
		BootSource: managedFirecrackerBootSource{
			BootArgs: "console=ttyS0 reboot=k panic=1", KernelImagePath: "/kernel",
		},
		Drives: []managedFirecrackerDrive{
			{
				DriveID: "rootfs", PathOnHost: "/rootfs", IsRootDevice: true, IsReadOnly: false,
				CacheType: model.CacheTypeWriteback, IOEngine: "Sync",
			},
			{
				DriveID: "cloud-init", PathOnHost: "/cloud-init.iso", IsRootDevice: false, IsReadOnly: true,
				CacheType: model.CacheTypeUnsafe, IOEngine: "Sync",
			},
		},
		NetworkInterfaces: []managedFirecrackerNetworkInterface{{
			IfaceID: "eth0", GuestMAC: "06:00:00:00:00:01", HostDevName: "mvtap012345678",
		}},
		MachineConfig: managedFirecrackerMachineConfig{
			VCPUCount: 2, MemSizeMiB: 512, SMT: true, TrackDirtyPages: true,
		},
		Logger: &managedFirecrackerLogger{
			LogPath: "/run/mvm/firecracker.log", Level: "Info", ShowLevel: true, ShowLogOrigin: true,
		},
		Metrics:   &managedFirecrackerMetrics{MetricsPath: "/run/mvm/firecracker.metrics"},
		CPUConfig: &model.CpuConfig{KvmCapabilities: []string{"KVM_CAP_ONE_REG"}},
		Vsock:     &managedFirecrackerVsock{GuestCID: 42, UDSPath: "/vmm-runtime/vsock.sock"},
	}

	if diff := cmp.Diff(want, buildManagedFirecrackerConfig(intent)); diff != "" {
		t.Errorf("closed Firecracker configuration mismatch (-want +got):\n%s", diff)
	}
}

func TestNetworklessFirecrackerConfigRetainsVsockWithoutNIC(t *testing.T) {
	intent := testVMFirecrackerLaunchIntent(cloudInitAbsent)
	intent.networkPlacement = model.VMNetworkPlacementNetworkless
	intent.guestMAC = ""
	intent.tapIdentity = vmFirecrackerTapIdentity{}

	raw, err := marshalVMFirecrackerLaunchIntent(intent)
	require.NoError(t, err)
	config, err := decodeManagedFirecrackerConfig(raw)
	require.NoError(t, err)
	if diff := cmp.Diff([]managedFirecrackerNetworkInterface{}, config.NetworkInterfaces); diff != "" {
		t.Errorf("networkless NIC inventory mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(
		&managedFirecrackerVsock{GuestCID: 42, UDSPath: "/vmm-runtime/vsock.sock"},
		config.Vsock,
	); diff != "" {
		t.Errorf("networkless vsock mismatch (-want +got):\n%s", diff)
	}
	assert.Equal(t, "console=ttyS0 reboot=k panic=1", config.BootSource.BootArgs)
}

func TestNetworklessFirecrackerIntentRejectsManagedEndpointAndBootArgs(t *testing.T) {
	tests := map[string]func(*vmFirecrackerLaunchIntent){
		"guest MAC": func(intent *vmFirecrackerLaunchIntent) { intent.guestMAC = "06:00:00:00:00:01" },
		"TAP": func(intent *vmFirecrackerLaunchIntent) {
			intent.tapIdentity = vmFirecrackerTapIdentity{name: "mvtap012345678"}
		},
		"IP boot argument": func(intent *vmFirecrackerLaunchIntent) { intent.bootArgs += " ip=dhcp" },
		"NoCloud NET boot argument": func(intent *vmFirecrackerLaunchIntent) {
			intent.bootArgs += " ds=nocloud-net;s=http://169.254.0.1/"
		},
		"omitted placement": func(intent *vmFirecrackerLaunchIntent) { intent.networkPlacement = "" },
		"unknown placement": func(intent *vmFirecrackerLaunchIntent) { intent.networkPlacement = "untrusted" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			intent := testVMFirecrackerLaunchIntent(cloudInitAbsent)
			intent.networkPlacement = model.VMNetworkPlacementNetworkless
			intent.guestMAC = ""
			intent.tapIdentity = vmFirecrackerTapIdentity{}
			mutate(&intent)
			_, err := marshalVMFirecrackerLaunchIntent(intent)
			requireManagedVMResourceDomainErrorCode(t, err, errs.CodeValidationFailed)
		})
	}
}

// Rationale: Persistent output leaves are mounted directly below /run/mvm, while mutable runtime sockets belong to
// the separately pinned ephemeral directory mounted at /vmm-runtime. Collapsing those paths breaks that boundary.
func TestBuildManagedFirecrackerConfigSeparatesPersistentOutputsFromRuntimeSockets(t *testing.T) {
	config := buildManagedFirecrackerConfig(testVMFirecrackerLaunchIntent(cloudInitAbsent))

	want := struct {
		Log     string
		Metrics string
		Vsock   string
	}{
		Log:     "/run/mvm/firecracker.log",
		Metrics: "/run/mvm/firecracker.metrics",
		Vsock:   "/vmm-runtime/vsock.sock",
	}
	got := struct {
		Log     string
		Metrics string
		Vsock   string
	}{
		Log:     config.Logger.LogPath,
		Metrics: config.Metrics.MetricsPath,
		Vsock:   config.Vsock.UDSPath,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("persistent and ephemeral Firecracker path contract mismatch (-want +got):\n%s", diff)
	}
}

func TestValidateVMFirecrackerLaunchIntentAcceptsAbsentOptionalSections(t *testing.T) {
	intent := testVMFirecrackerLaunchIntent(cloudInitAbsent)
	intent.rootfsCacheType = model.CacheTypeUnsafe
	intent.enableLogging = false
	intent.logLevel = ""
	intent.enableMetrics = false
	intent.cpuConfig = nil
	intent.vsockCID = nil
	assert.NoError(t, validateVMFirecrackerLaunchIntent(intent))
}

// Rationale: A caller-controlled file that changes identity or metadata during admission cannot authorize launch.
func TestPrepareManagedFirecrackerConfigRequiresStableSourceMetadata(t *testing.T) {
	mutations := map[string]func(*unix.Statx_t){
		"device": func(stat *unix.Statx_t) { stat.Dev_minor++ },
		"inode":  func(stat *unix.Statx_t) { stat.Ino++ },
		"mode":   func(stat *unix.Statx_t) { stat.Mode ^= 0100 },
		"owner":  func(stat *unix.Statx_t) { stat.Uid++ },
		"group":  func(stat *unix.Statx_t) { stat.Gid++ },
		"links":  func(stat *unix.Statx_t) { stat.Nlink++ },
		"size":   func(stat *unix.Statx_t) { stat.Size++ },
		"mtime":  func(stat *unix.Statx_t) { stat.Mtime.Nsec++ },
		"ctime":  func(stat *unix.Statx_t) { stat.Ctime.Nsec++ },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			intent := testVMFirecrackerLaunchIntent(cloudInitAbsent)
			raw := mustMarshalManagedFirecrackerConfig(t, buildManagedFirecrackerConfig(intent))
			deps, sourceFD := managedFirecrackerConfigTestSource(t, raw)
			realStatx := deps.statx
			statCalls := 0
			deps.statx = func(ctx context.Context, fd, flags, mask int, stat *unix.Statx_t) error {
				if err := realStatx(ctx, fd, flags, mask, stat); err != nil {
					return err
				}
				statCalls++
				if statCalls == 2 {
					mutate(stat)
				}
				return nil
			}
			memfdCalls := 0
			deps.memfdCreate = func(context.Context, string, int) (int, error) {
				memfdCalls++
				return -1, errors.New("must not create")
			}
			closes := observeManagedFirecrackerConfigCloses(&deps)

			prepared, err := prepareManagedFirecrackerConfig(context.Background(), deps, sourceFD, intent)
			require.Error(t, err)
			assert.Nil(t, prepared)
			assert.Contains(t, err.Error(), "changed while being read")
			assert.Equal(t, 2, statCalls)
			assert.Zero(t, memfdCalls)
			assert.Zero(t, closes.memfdCreateCalls)
			assert.Equal(
				t,
				[]managedFirecrackerConfigClosePhase{managedFirecrackerSourceClosePhase},
				closes.closePhases,
			)
		})
	}
}

// Rationale: Closed JSON decoding and complete semantic equality prevent omitted, duplicate, or extra authority.
func TestPrepareManagedFirecrackerConfigRejectsStrictJSONAndIntentMismatches(t *testing.T) {
	intent := testVMFirecrackerLaunchIntent(cloudInitPresent)
	expected := buildManagedFirecrackerConfig(intent)
	valid := mustMarshalManagedFirecrackerConfig(t, expected)

	tests := map[string][]byte{
		"case-conflicting field": bytes.Replace(
			valid,
			[]byte(`"smt":false`),
			[]byte(`"smt":false,"SMT":false`),
			1,
		),
		"unknown field": bytes.Replace(
			valid,
			[]byte(`{"boot-source":{`),
			[]byte(`{"unknown":true,"boot-source":{`),
			1,
		),
		"missing required field": bytes.Replace(valid, []byte(`"smt":false,`), nil, 1),
		"null required field":    bytes.Replace(valid, []byte(`"smt":false`), []byte(`"smt":null`), 1),
		"null optional field": bytes.Replace(
			valid,
			[]byte(`"kernel_image_path"`),
			[]byte(`"initrd_path":null,"kernel_image_path"`),
			1,
		),
		"duplicate field": bytes.Replace(valid, []byte(`"smt":false`), []byte(`"smt":false,"smt":false`), 1),
		"nested collection element": bytes.Replace(
			valid,
			[]byte(`"KVM_CAP_ONE_REG"`),
			[]byte(`["KVM_CAP_ONE_REG"]`),
			1,
		),
		"trailing JSON": append(bytes.Clone(valid), []byte(` {}`)...),
	}

	mismatches := map[string]func(*managedFirecrackerConfig){
		"boot arguments":  func(config *managedFirecrackerConfig) { config.BootSource.BootArgs += " forged=1" },
		"kernel path":     func(config *managedFirecrackerConfig) { config.BootSource.KernelImagePath = "/other" },
		"rootfs cache":    func(config *managedFirecrackerConfig) { config.Drives[0].CacheType = model.CacheTypeUnsafe },
		"cloud-init path": func(config *managedFirecrackerConfig) { config.Drives[1].PathOnHost = "/other" },
		"network interface ID": func(config *managedFirecrackerConfig) {
			config.NetworkInterfaces[0].IfaceID = "eth1"
		},
		"guest MAC": func(config *managedFirecrackerConfig) {
			config.NetworkInterfaces[0].GuestMAC = "06:00:00:00:00:02"
		},
		"TAP identity": func(config *managedFirecrackerConfig) {
			config.NetworkInterfaces[0].HostDevName = "mvtap-other"
		},
		"VCPU count":     func(config *managedFirecrackerConfig) { config.MachineConfig.VCPUCount++ },
		"memory":         func(config *managedFirecrackerConfig) { config.MachineConfig.MemSizeMiB++ },
		"SMT":            func(config *managedFirecrackerConfig) { config.MachineConfig.SMT = true },
		"dirty tracking": func(config *managedFirecrackerConfig) { config.MachineConfig.TrackDirtyPages = true },
		"CPU template": func(config *managedFirecrackerConfig) {
			value := "T2"
			config.MachineConfig.CPUTemplate = &value
		},
		"logger path":       func(config *managedFirecrackerConfig) { config.Logger.LogPath = "/other" },
		"logger level":      func(config *managedFirecrackerConfig) { config.Logger.Level = "Debug" },
		"logger show level": func(config *managedFirecrackerConfig) { config.Logger.ShowLevel = false },
		"logger origin":     func(config *managedFirecrackerConfig) { config.Logger.ShowLogOrigin = false },
		"metrics path":      func(config *managedFirecrackerConfig) { config.Metrics.MetricsPath = "/other" },
		"CPU config": func(config *managedFirecrackerConfig) {
			config.CPUConfig.KvmCapabilities = append(config.CPUConfig.KvmCapabilities, "forged")
		},
		"logger presence":  func(config *managedFirecrackerConfig) { config.Logger = nil },
		"metrics presence": func(config *managedFirecrackerConfig) { config.Metrics = nil },
		"CPU presence":     func(config *managedFirecrackerConfig) { config.CPUConfig = nil },
		"vsock CID":        func(config *managedFirecrackerConfig) { config.Vsock.GuestCID++ },
		"vsock path":       func(config *managedFirecrackerConfig) { config.Vsock.UDSPath = "/other" },
		"vsock presence":   func(config *managedFirecrackerConfig) { config.Vsock = nil },
	}
	for name, mutate := range mismatches {
		config := cloneManagedFirecrackerConfigForTest(t, expected)
		mutate(&config)
		tests[name] = mustMarshalManagedFirecrackerConfig(t, config)
	}

	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			deps, sourceFD := managedFirecrackerConfigTestSource(t, raw)
			memfdCalls := 0
			deps.memfdCreate = func(context.Context, string, int) (int, error) {
				memfdCalls++
				return -1, errors.New("must not create")
			}
			closes := observeManagedFirecrackerConfigCloses(&deps)

			prepared, err := prepareManagedFirecrackerConfig(context.Background(), deps, sourceFD, intent)
			require.Error(t, err)
			assert.Nil(t, prepared)
			requireManagedVMResourceDomainErrorCode(t, err, errs.CodeValidationFailed)
			assert.Zero(t, memfdCalls)
			assert.Zero(t, closes.memfdCreateCalls)
			assert.Equal(
				t,
				[]managedFirecrackerConfigClosePhase{managedFirecrackerSourceClosePhase},
				closes.closePhases,
			)
		})
	}
}

// Rationale: encoding/json replaces malformed UTF-8 with U+FFFD. Without an explicit byte-level check, malformed
// caller bytes can become semantically equal to a root intent that legitimately contains the replacement character.
func TestPrepareManagedFirecrackerConfigRejectsMalformedUTF8BeforeMemfd(t *testing.T) {
	intent := testVMFirecrackerLaunchIntent(cloudInitAbsent)
	intent.bootArgs = "console=ttyS0 replacement=\uFFFD"
	valid := mustMarshalManagedFirecrackerConfig(t, buildManagedFirecrackerConfig(intent))
	malformed := bytes.Replace(valid, []byte("\uFFFD"), []byte{0xff}, 1)
	require.False(t, bytes.Equal(valid, malformed))

	deps, sourceFD := managedFirecrackerConfigTestSource(t, malformed)
	memfdCalls := 0
	deps.memfdCreate = func(context.Context, string, int) (int, error) {
		memfdCalls++
		return -1, errors.New("must not create")
	}

	prepared, err := prepareManagedFirecrackerConfig(context.Background(), deps, sourceFD, intent)
	assert.Nil(t, prepared)
	requireManagedVMResourceDomainErrorCode(t, err, errs.CodeValidationFailed)
	assert.Zero(t, memfdCalls)
}

func TestPrepareManagedFirecrackerConfigCreatesSealedCanonicalCapability(t *testing.T) {
	intent := testVMFirecrackerLaunchIntent(cloudInitPresent)
	expected := buildManagedFirecrackerConfig(intent)
	canonical := mustMarshalManagedFirecrackerConfig(t, expected)
	formatted := new(bytes.Buffer)
	require.NoError(t, json.Indent(formatted, canonical, "", "  "))

	deps, sourceFD := managedFirecrackerConfigTestSource(t, formatted.Bytes())
	prepareManagedFirecrackerRootDepsForUnprivilegedTest(&deps)
	var sourceStat unix.Stat_t
	require.NoError(t, unix.Fstat(sourceFD, &sourceStat))
	var observedMemfdName string
	var observedMemfdFlags int
	var observedUID int
	var observedGID int
	var observedMode uint32
	var observedSeals int
	realFchown := deps.fchown
	deps.fchown = func(ctx context.Context, fd, uid, gid int) error {
		observedUID = uid
		observedGID = gid
		return realFchown(ctx, fd, uid, gid)
	}
	realFchmod := deps.fchmod
	deps.fchmod = func(ctx context.Context, fd int, mode uint32) error {
		observedMode = mode
		return realFchmod(ctx, fd, mode)
	}
	realAddSeals := deps.addSeals
	deps.addSeals = func(ctx context.Context, fd, seals int) error {
		observedSeals = seals
		return realAddSeals(ctx, fd, seals)
	}

	// CRITICAL: Occupying the released source descriptor during memfd creation prevents Linux from reusing its numeric
	// value. This lets the test prove both ownership transfer and object separation through independent kernel state.
	realMemfdCreate := deps.memfdCreate
	deps.memfdCreate = func(ctx context.Context, name string, flags int) (int, error) {
		observedMemfdName = name
		observedMemfdFlags = flags
		guardFD, openErr := unix.Open("/dev/null", unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if openErr != nil {
			return -1, openErr
		}
		if guardFD != sourceFD {
			return -1, errors.Join(
				errors.New("source descriptor remained open before memfd creation"),
				unix.Close(guardFD),
			)
		}
		memfdFD, createErr := realMemfdCreate(ctx, name, flags)
		closeErr := unix.Close(guardFD)
		if createErr != nil {
			return -1, errors.Join(createErr, closeErr)
		}
		if closeErr != nil {
			return -1, errors.Join(closeErr, unix.Close(memfdFD))
		}
		return memfdFD, nil
	}
	closes := observeManagedFirecrackerConfigCloses(&deps)
	prepared, err := prepareManagedFirecrackerConfig(context.Background(), deps, sourceFD, intent)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	t.Cleanup(func() { require.NoError(t, prepared.Release(context.Background())) })
	assert.True(t, closes.sourceClosedBeforeMemfd)
	assert.Equal(t, 1, closes.memfdCreateCalls)
	assert.Equal(t, managedVMConfigMemfdName, observedMemfdName)
	assert.Equal(t, managedVMConfigMemfdFlags, observedMemfdFlags)
	assert.Zero(t, observedUID)
	assert.Zero(t, observedGID)
	assert.Equal(t, managedVMConfigMemfdMode, observedMode)
	assert.Equal(t, managedVMConfigRequiredSeals, observedSeals)
	assert.Equal(
		t,
		[]managedFirecrackerConfigClosePhase{managedFirecrackerSourceClosePhase},
		closes.closePhases,
	)
	var closedSourceStat unix.Stat_t
	assert.ErrorIs(t, unix.Fstat(sourceFD, &closedSourceStat), unix.EBADF)

	var stat unix.Stat_t
	require.NoError(t, unix.Fstat(prepared.state.fd, &stat))
	assert.False(
		t,
		sourceStat.Dev == stat.Dev && sourceStat.Ino == stat.Ino,
		"prepared configuration must not retain the caller source object",
	)
	assert.Equal(t, uint32(unix.S_IFREG)|managedVMConfigMemfdMode, stat.Mode&(unix.S_IFMT|07777))
	assert.Zero(t, stat.Nlink)
	assert.Equal(t, int64(len(canonical)), stat.Size)
	flags, err := unix.FcntlInt(uintptr(prepared.state.fd), unix.F_GETFD, 0)
	require.NoError(t, err)
	assert.Equal(t, unix.FD_CLOEXEC, flags)
	offset, err := unix.Seek(prepared.state.fd, 0, unix.SEEK_CUR)
	require.NoError(t, err)
	assert.Zero(t, offset)
	seals, err := unix.FcntlInt(uintptr(prepared.state.fd), unix.F_GET_SEALS, 0)
	require.NoError(t, err)
	assert.Equal(t, managedVMConfigRequiredSeals, seals&managedVMConfigRequiredSeals)

	got := make([]byte, len(canonical))
	count, err := unix.Pread(prepared.state.fd, got, 0)
	require.NoError(t, err)
	assert.Equal(t, len(canonical), count)
	if diff := cmp.Diff(canonical, got); diff != "" {
		t.Errorf("prepared Firecracker configuration mismatch (-want +got):\n%s", diff)
	}

	duplicate, err := unix.Dup(prepared.state.fd)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, unix.Close(duplicate)) })
	_, err = unix.Pwrite(duplicate, []byte("x"), 0)
	assert.Error(t, err)
	assert.Error(t, unix.Ftruncate(duplicate, int64(len(canonical)+1)))
	_, err = unix.Mmap(duplicate, 0, len(canonical), unix.PROT_WRITE, unix.MAP_SHARED)
	assert.Error(t, err)
	require.NoError(t, prepared.Release(context.Background()))
	assert.Equal(
		t,
		[]managedFirecrackerConfigClosePhase{
			managedFirecrackerSourceClosePhase,
			managedFirecrackerPreparedClosePhase,
		},
		closes.closePhases,
	)
}

// Rationale: Once admitted, the retained capability must contain only root-built bytes detached from caller storage.
func TestPrepareManagedFirecrackerConfigIsolatesCallerMutationAndReplacement(t *testing.T) {
	intent := testVMFirecrackerLaunchIntent(cloudInitAbsent)
	canonical := mustMarshalManagedFirecrackerConfig(t, buildManagedFirecrackerConfig(intent))
	directory := t.TempDir()
	path := filepath.Join(directory, "firecracker.json")
	require.NoError(t, os.WriteFile(path, canonical, 0600))
	sourceFD, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	require.NoError(t, err)
	deps := realManagedFirecrackerConfigDeps()
	prepareManagedFirecrackerRootDepsForUnprivilegedTest(&deps)

	prepared, err := prepareManagedFirecrackerConfig(context.Background(), deps, sourceFD, intent)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	t.Cleanup(func() { require.NoError(t, prepared.Release(context.Background())) })

	detached := path + ".detached"
	require.NoError(t, os.Rename(path, detached))
	require.NoError(t, os.WriteFile(path, []byte(`{"forged":true}`), 0600))
	require.NoError(t, os.WriteFile(detached, []byte(`{"also":"forged"}`), 0600))
	got := make([]byte, len(canonical))
	count, err := unix.Pread(prepared.state.fd, got, 0)
	require.NoError(t, err)
	assert.Equal(t, len(canonical), count)
	if diff := cmp.Diff(canonical, got); diff != "" {
		t.Errorf("caller mutation changed prepared configuration (-want +got):\n%s", diff)
	}
}

// Rationale: Cleanup failure is security-relevant evidence but must not erase the primary construction failure.
func TestPrepareManagedFirecrackerConfigPreservesPrimaryErrorAndCheckedCleanup(t *testing.T) {
	intent := testVMFirecrackerLaunchIntent(cloudInitAbsent)
	raw := mustMarshalManagedFirecrackerConfig(t, buildManagedFirecrackerConfig(intent))
	deps, sourceFD := managedFirecrackerConfigTestSource(t, raw)
	prepareManagedFirecrackerRootDepsForUnprivilegedTest(&deps)
	primary := errors.New("injected positioned write failure")
	deps.pwrite = func(context.Context, int, []byte, int64) (int, error) { return 0, primary }
	realClose := deps.close
	closeCalls := 0
	deps.close = func(ctx context.Context, fd int) error {
		closeCalls++
		if closeCalls == 2 {
			return errors.Join(realClose(ctx, fd), errors.New("prepared close failed"))
		}
		return realClose(ctx, fd)
	}

	prepared, err := prepareManagedFirecrackerConfig(context.Background(), deps, sourceFD, intent)
	require.Error(t, err)
	assert.Nil(t, prepared)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeVMAtomicFailed, domainErr.Code)
	assert.ErrorIs(t, err, primary)
	assert.Contains(t, err.Error(), "prepared close failed")
	assert.Equal(t, true, domainErr.Details["managed_vm_resource_descriptor_cleanup_failed"])
	assert.Equal(t, 2, closeCalls)
}

// Rationale: The root-built capability is not retainable unless immutable seals are both installed and verified.
func TestPrepareManagedFirecrackerConfigRejectsSealFaults(t *testing.T) {
	tests := map[string]func(*managedFirecrackerConfigDeps){
		"seal syscall": func(deps *managedFirecrackerConfigDeps) {
			deps.addSeals = func(context.Context, int, int) error { return unix.EPERM }
		},
		"seal inspection": func(deps *managedFirecrackerConfigDeps) {
			deps.getSeals = func(context.Context, int) (int, error) { return 0, unix.EIO }
		},
		"missing required seal": func(deps *managedFirecrackerConfigDeps) {
			deps.getSeals = func(context.Context, int) (int, error) {
				return managedVMConfigRequiredSeals &^ unix.F_SEAL_FUTURE_WRITE, nil
			}
		},
	}
	for name, inject := range tests {
		t.Run(name, func(t *testing.T) {
			intent := testVMFirecrackerLaunchIntent(cloudInitAbsent)
			raw := mustMarshalManagedFirecrackerConfig(t, buildManagedFirecrackerConfig(intent))
			deps, sourceFD := managedFirecrackerConfigTestSource(t, raw)
			prepareManagedFirecrackerRootDepsForUnprivilegedTest(&deps)
			inject(&deps)

			prepared, err := prepareManagedFirecrackerConfig(context.Background(), deps, sourceFD, intent)
			require.Error(t, err)
			assert.Nil(t, prepared)
			requireManagedVMResourceDomainErrorCode(t, err, errs.CodeVMAtomicFailed)
		})
	}
}

// Rationale: vm.memfd_noexec=1 implicitly adds F_SEAL_EXEC without weakening the required immutability seals.
// Requesting the same kernel behavior directly proves admission without changing host or namespace sysctls.
// See https://cdn.kernel.org/doc/html/latest/userspace-api/mfd_noexec.html.
func TestPrepareManagedFirecrackerConfigAcceptsKernelNoexecSeal(t *testing.T) {
	intent := testVMFirecrackerLaunchIntent(cloudInitAbsent)
	raw := mustMarshalManagedFirecrackerConfig(t, buildManagedFirecrackerConfig(intent))
	deps, sourceFD := managedFirecrackerConfigTestSource(t, raw)
	prepareManagedFirecrackerRootDepsForUnprivilegedTest(&deps)
	realMemfdCreate := deps.memfdCreate
	deps.memfdCreate = func(ctx context.Context, name string, flags int) (int, error) {
		return realMemfdCreate(ctx, name, flags|unix.MFD_NOEXEC_SEAL)
	}

	prepared, err := prepareManagedFirecrackerConfig(context.Background(), deps, sourceFD, intent)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	t.Cleanup(func() { require.NoError(t, prepared.Release(context.Background())) })
	seals, err := unix.FcntlInt(uintptr(prepared.state.fd), unix.F_GET_SEALS, 0)
	require.NoError(t, err)
	assert.Equal(t, managedVMConfigRequiredSeals, seals&managedVMConfigRequiredSeals)
	assert.Equal(t, unix.F_SEAL_EXEC, seals&unix.F_SEAL_EXEC)
}

// Rationale: The private capability permits exactly close-on-exec. Accepting an unknown descriptor flag would silently
// broaden the authority contract when Linux adds another flag or a dependency returns an impossible state.
func TestPrepareManagedFirecrackerConfigRejectsUnexpectedDescriptorFlags(t *testing.T) {
	intent := testVMFirecrackerLaunchIntent(cloudInitAbsent)
	raw := mustMarshalManagedFirecrackerConfig(t, buildManagedFirecrackerConfig(intent))
	deps, sourceFD := managedFirecrackerConfigTestSource(t, raw)
	prepareManagedFirecrackerRootDepsForUnprivilegedTest(&deps)
	deps.getFDFlags = func(context.Context, int) (int, error) {
		return unix.FD_CLOEXEC | 1<<20, nil
	}

	prepared, err := prepareManagedFirecrackerConfig(context.Background(), deps, sourceFD, intent)
	assert.Nil(t, prepared)
	requireManagedVMResourceDomainErrorCode(t, err, errs.CodeVMAtomicFailed)
}

// Rationale: Seals alone are insufficient; the retained anonymous file must also have exact safe metadata.
func TestPrepareManagedFirecrackerConfigRejectsUnsafeCanonicalMemfdMetadata(t *testing.T) {
	mutations := map[string]func(*unix.Stat_t){
		"non-regular type": func(stat *unix.Stat_t) { stat.Mode = unix.S_IFIFO | managedVMConfigMemfdMode },
		"non-root owner":   func(stat *unix.Stat_t) { stat.Uid = 1 },
		"non-root group":   func(stat *unix.Stat_t) { stat.Gid = 1 },
		"writable mode":    func(stat *unix.Stat_t) { stat.Mode = stat.Mode&^07777 | 0644 },
		"linked object":    func(stat *unix.Stat_t) { stat.Nlink = 1 },
		"wrong size":       func(stat *unix.Stat_t) { stat.Size++ },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			intent := testVMFirecrackerLaunchIntent(cloudInitAbsent)
			raw := mustMarshalManagedFirecrackerConfig(t, buildManagedFirecrackerConfig(intent))
			deps, sourceFD := managedFirecrackerConfigTestSource(t, raw)
			prepareManagedFirecrackerRootDepsForUnprivilegedTest(&deps)
			realFstat := deps.fstat
			deps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
				if err := realFstat(ctx, fd, stat); err != nil {
					return err
				}
				if stat.Nlink == 0 && stat.Size > 0 {
					mutate(stat)
				}
				return nil
			}

			prepared, err := prepareManagedFirecrackerConfig(context.Background(), deps, sourceFD, intent)
			require.Error(t, err)
			assert.Nil(t, prepared)
			assert.Contains(t, err.Error(), "unsafe final metadata")
		})
	}
}

// Rationale: Root authority must not be constructed until ownership of the caller source has been conclusively ended.
func TestPrepareManagedFirecrackerConfigStopsBeforeMemfdWhenSourceCloseFails(t *testing.T) {
	intent := testVMFirecrackerLaunchIntent(cloudInitAbsent)
	raw := mustMarshalManagedFirecrackerConfig(t, buildManagedFirecrackerConfig(intent))
	deps, sourceFD := managedFirecrackerConfigTestSource(t, raw)
	realClose := deps.close
	closeErr := errors.New("injected source close failure")
	deps.close = func(ctx context.Context, fd int) error {
		return errors.Join(realClose(ctx, fd), closeErr)
	}
	memfdCalls := 0
	deps.memfdCreate = func(context.Context, string, int) (int, error) {
		memfdCalls++
		return -1, errors.New("must not create")
	}
	closes := observeManagedFirecrackerConfigCloses(&deps)

	prepared, err := prepareManagedFirecrackerConfig(context.Background(), deps, sourceFD, intent)
	require.Error(t, err)
	assert.Nil(t, prepared)
	assert.Zero(t, memfdCalls)
	assert.Zero(t, closes.memfdCreateCalls)
	assert.Equal(
		t,
		[]managedFirecrackerConfigClosePhase{managedFirecrackerSourceClosePhase},
		closes.closePhases,
	)
	assert.ErrorIs(t, err, closeErr)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, true, domainErr.Details["managed_vm_resource_descriptor_cleanup_failed"])
}

func testVMFirecrackerLaunchIntent(cloudInit cloudInitPresence) vmFirecrackerLaunchIntent {
	vsockCID := uint32(42)
	return vmFirecrackerLaunchIntent{
		networkPlacement: model.VMNetworkPlacementManaged,
		bootArgs:         "console=ttyS0 reboot=k panic=1",
		vcpuCount:        2,
		memSizeMiB:       512,
		guestMAC:         "06:00:00:00:00:01",
		tapIdentity:      vmFirecrackerTapIdentity{name: "mvtap012345678"},
		rootfsCacheType:  model.CacheTypeWriteback,
		cloudInit:        cloudInit,
		enableLogging:    true,
		logLevel:         "Info",
		enableMetrics:    true,
		cpuConfig: &model.CpuConfig{
			KvmCapabilities: []string{"KVM_CAP_ONE_REG"},
			CpuidModifiers: []model.CpuidLeafModifier{{
				Leaf: "0x1", Subleaf: "0x0", Flags: 0,
				Modifiers: []model.CpuidRegisterModifier{{Register: "eax", Bitmap: "0b1"}},
			}},
		},
		vsockCID: &vsockCID,
	}
}

func managedFirecrackerConfigTestSource(t *testing.T, raw []byte) (managedFirecrackerConfigDeps, int) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "firecracker.json")
	require.NoError(t, os.WriteFile(path, raw, 0600))
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	require.NoError(t, err)
	return realManagedFirecrackerConfigDeps(), fd
}

type managedFirecrackerConfigClosePhase string

const (
	managedFirecrackerSourceClosePhase   managedFirecrackerConfigClosePhase = "source"
	managedFirecrackerPreparedClosePhase managedFirecrackerConfigClosePhase = "prepared"
)

type managedFirecrackerConfigCloseObservation struct {
	closePhases             []managedFirecrackerConfigClosePhase
	memfdCreateCalls        int
	sourceClosedBeforeMemfd bool
}

// observeManagedFirecrackerConfigCloses wraps close and memfdCreate to record descriptor-generation phases, keeping
// test evidence valid when Linux reuses a numeric file descriptor.
func observeManagedFirecrackerConfigCloses(
	deps *managedFirecrackerConfigDeps,
) *managedFirecrackerConfigCloseObservation {
	realClose := deps.close
	realMemfdCreate := deps.memfdCreate
	observation := &managedFirecrackerConfigCloseObservation{}
	deps.close = func(ctx context.Context, fd int) error {
		phase := managedFirecrackerSourceClosePhase
		if observation.memfdCreateCalls > 0 {
			phase = managedFirecrackerPreparedClosePhase
		}
		observation.closePhases = append(observation.closePhases, phase)
		return realClose(ctx, fd)
	}
	deps.memfdCreate = func(ctx context.Context, name string, flags int) (int, error) {
		observation.sourceClosedBeforeMemfd = len(observation.closePhases) == 1 &&
			observation.closePhases[0] == managedFirecrackerSourceClosePhase
		observation.memfdCreateCalls++
		return realMemfdCreate(ctx, name, flags)
	}
	return observation
}

// prepareManagedFirecrackerRootDepsForUnprivilegedTest preserves real memfd behavior while emulating the privileged
// owner transition: fstat reports anonymous memfds as root-owned, and unprivileged processes substitute a successful
// fchown because they cannot perform the production UID/GID transition themselves.
func prepareManagedFirecrackerRootDepsForUnprivilegedTest(deps *managedFirecrackerConfigDeps) {
	realFstat := deps.fstat
	deps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
		if err := realFstat(ctx, fd, stat); err != nil {
			return err
		}
		if stat.Nlink == 0 {
			stat.Uid = 0
			stat.Gid = 0
		}
		return nil
	}
	if os.Geteuid() != 0 || os.Getegid() != 0 {
		deps.fchown = func(context.Context, int, int, int) error { return nil }
	}
}

func mustMarshalManagedFirecrackerConfig(t *testing.T, config managedFirecrackerConfig) []byte {
	t.Helper()

	raw, err := json.Marshal(config)
	require.NoError(t, err)
	return raw
}

func cloneManagedFirecrackerConfigForTest(
	t *testing.T,
	config managedFirecrackerConfig,
) managedFirecrackerConfig {
	t.Helper()

	raw := mustMarshalManagedFirecrackerConfig(t, config)
	var cloned managedFirecrackerConfig
	require.NoError(t, json.Unmarshal(raw, &cloned))
	return cloned
}

// Rationale: Copied capability handles must share one checked close result; otherwise a stale copy can close a reused
// descriptor, and concurrent cleanup can race or hide the first close failure.
func TestPreparedFirecrackerConfigReleaseIsSharedAndIdempotent(t *testing.T) {
	var mu sync.Mutex
	closeCalls := 0
	closeErr := io.ErrClosedPipe
	prepared := &preparedFirecrackerConfig{state: &preparedFirecrackerConfigState{
		fd: 91,
		close: func(ctx context.Context, fd int) error {
			assert.NoError(t, ctx.Err())
			assert.Equal(t, 91, fd)
			mu.Lock()
			closeCalls++
			mu.Unlock()
			return closeErr
		},
	}}
	copied := *prepared
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	results := make(chan error, 2)
	var releaseWG sync.WaitGroup
	releaseWG.Add(2)
	go func() {
		defer releaseWG.Done()
		results <- prepared.Release(ctx)
	}()
	go func() {
		defer releaseWG.Done()
		results <- copied.Release(context.Background())
	}()
	releaseWG.Wait()
	close(results)

	for err := range results {
		assert.ErrorIs(t, err, closeErr)
	}
	mu.Lock()
	assert.Equal(t, 1, closeCalls)
	mu.Unlock()
	assert.ErrorIs(t, prepared.Release(context.Background()), closeErr)
	assert.ErrorIs(t, copied.Release(context.Background()), closeErr)
}
