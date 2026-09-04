package jailer

import (
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

	"mvmctl/internal/lib/model"
)

func TestPrepareManagedVMCgroupForLaunchRejectsInvalidLimitsBeforeEffects(t *testing.T) {
	t.Parallel()

	valid := testManagedVMCgroupLimits()
	tests := map[string]func(*model.VMCgroupLimits){
		"policy zero":      func(limits *model.VMCgroupLimits) { limits.PolicyVersion = 0 },
		"policy two":       func(limits *model.VMCgroupLimits) { limits.PolicyVersion = 2 },
		"quota negative":   func(limits *model.VMCgroupLimits) { limits.CPUQuotaMicros = -1 },
		"quota zero":       func(limits *model.VMCgroupLimits) { limits.CPUQuotaMicros = 0 },
		"period negative":  func(limits *model.VMCgroupLimits) { limits.CPUPeriodMicros = -1 },
		"period zero":      func(limits *model.VMCgroupLimits) { limits.CPUPeriodMicros = 0 },
		"weight negative":  func(limits *model.VMCgroupLimits) { limits.CPUWeight = -1 },
		"weight zero":      func(limits *model.VMCgroupLimits) { limits.CPUWeight = 0 },
		"weight above max": func(limits *model.VMCgroupLimits) { limits.CPUWeight = 10001 },
		"memory high negative": func(limits *model.VMCgroupLimits) {
			limits.MemoryHighBytes = -1
		},
		"memory high zero": func(limits *model.VMCgroupLimits) { limits.MemoryHighBytes = 0 },
		"memory max zero":  func(limits *model.VMCgroupLimits) { limits.MemoryMaxBytes = 0 },
		"high above max":   func(limits *model.VMCgroupLimits) { limits.MemoryHighBytes = limits.MemoryMaxBytes + 1 },
		"negative swap":    func(limits *model.VMCgroupLimits) { limits.SwapMaxBytes = -1 },
		"process limit negative": func(limits *model.VMCgroupLimits) {
			limits.PIDsMax = -1
		},
		"process limit zero": func(limits *model.VMCgroupLimits) { limits.PIDsMax = 0 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newManagedVMCgroupTestFixture(t)
			deps := fixture.deps()
			effects := 0
			deps.openRoot = func(context.Context) (int, error) { effects++; return -1, unix.EIO }
			deps.mkdirAt = func(context.Context, int, string, uint32) error { effects++; return unix.EIO }
			deps.write = func(context.Context, int, []byte) (int, error) { effects++; return 0, unix.EIO }
			limits := valid
			mutate(&limits)

			prepared, err := prepareManagedVMCgroupForLaunch(t.Context(), deps, testVMID, limits)
			assert.Nil(t, prepared)
			requireManagedVMCgroupAtomicError(t, err)
			assert.Zero(t, effects)
		})
	}
}

func TestManagedVMCgroupLimitIntentIsCopiedTransferredAndReadBeforeMembership(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	limits := testManagedVMCgroupLimits()
	wantLimits := limits
	prepared, err := prepareManagedVMCgroupForLaunch(t.Context(), fixture.launchDeps(), testVMID, limits)
	require.NoError(t, err)
	assert.True(t, prepared.state.hasLimitIntent)
	if diff := cmp.Diff(wantLimits, prepared.state.limits); diff != "" {
		t.Errorf("prepared limit intent mismatch (-want +got):\n%s", diff)
	}
	limits.CPUWeight++
	limits.MemoryMaxBytes++
	if diff := cmp.Diff(wantLimits, prepared.state.limits); diff != "" {
		t.Errorf("mutated caller limit intent changed preparation (-want +got):\n%s", diff)
	}
	require.NoError(t, prepared.MarkJailerHandoff())
	fixture.addVMLeafWithLimits(testVMID, wantLimits)

	var opened []string
	realOpenAt2 := prepared.state.deps.openAt2
	prepared.state.deps.openAt2 = func(
		ctx context.Context,
		fd int,
		name string,
		how *unix.OpenHow,
	) (int, error) {
		opened = append(opened, name)
		return realOpenAt2(ctx, fd, name, how)
	}
	lease, err := prepared.Bind(
		t.Context(),
		processTestLocator(),
		processTestCgroupObservation(),
		&managedVMCgroupTestProcessVerifier{},
	)
	require.NoError(t, err)
	require.NotNil(t, lease)
	assert.True(t, lease.state.hasLimits)
	if diff := cmp.Diff(wantLimits, lease.state.limits); diff != "" {
		t.Errorf("bound limit intent mismatch (-want +got):\n%s", diff)
	}
	wantOpened := []string{
		testVMID,
		"cpu.max",
		"cpu.weight",
		"memory.high",
		"memory.max",
		"memory.swap.max",
		"pids.max",
		"cgroup.procs",
	}
	if diff := cmp.Diff(wantOpened, opened); diff != "" {
		t.Errorf("limit and membership open order mismatch (-want +got):\n%s", diff)
	}
	fixture.setMembership(testVMID, "")
	require.NoError(t, lease.Remove(t.Context()))
	require.NoError(t, lease.Release(t.Context()))
}

func TestManagedVMCgroupLimitReadbackRejectsEveryMismatchAndMalformedControl(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		control string
		raw     string
	}{
		"cpu max mismatch":        {control: "cpu.max", raw: "200001 100000\n"},
		"cpu max malformed":       {control: "cpu.max", raw: "max 100000\n"},
		"cpu weight mismatch":     {control: "cpu.weight", raw: "101\n"},
		"cpu weight malformed":    {control: "cpu.weight", raw: "0100\n"},
		"memory high mismatch":    {control: "memory.high", raw: "536870913\n"},
		"memory high malformed":   {control: "memory.high", raw: "max\n"},
		"memory max mismatch":     {control: "memory.max", raw: "671088641\n"},
		"memory max malformed":    {control: "memory.max", raw: "-1\n"},
		"swap max mismatch":       {control: "memory.swap.max", raw: "1\n"},
		"swap max malformed":      {control: "memory.swap.max", raw: "+0\n"},
		"process limit mismatch":  {control: "pids.max", raw: "129\n"},
		"process limit malformed": {control: "pids.max", raw: "128 129\n"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture, prepared, limits := prepareManagedVMCgroupLimitTestBind(t)
			require.NoError(t, os.WriteFile(
				filepath.Join(fixture.vmLeafPath(testVMID), tc.control),
				[]byte(tc.raw),
				0o644,
			))
			membershipOpens := 0
			realOpenAt2 := prepared.state.deps.openAt2
			prepared.state.deps.openAt2 = func(
				ctx context.Context,
				fd int,
				child string,
				how *unix.OpenHow,
			) (int, error) {
				if child == "cgroup.procs" {
					membershipOpens++
				}
				return realOpenAt2(ctx, fd, child, how)
			}
			verifier := &managedVMCgroupTestProcessVerifier{}

			lease, err := prepared.Bind(
				t.Context(),
				processTestLocator(),
				processTestCgroupObservation(),
				verifier,
			)
			requireManagedVMCgroupAtomicError(t, err)
			require.NotNil(t, lease)
			if diff := cmp.Diff(limits, lease.state.limits); diff != "" {
				t.Errorf("retained limit intent mismatch (-want +got):\n%s", diff)
			}
			assert.Zero(t, membershipOpens)
			assert.Zero(t, verifier.beforeCalls)
			assert.Zero(t, verifier.afterCalls)
			fixture.setMembership(testVMID, "")
			require.NoError(t, lease.Remove(t.Context()))
			require.NoError(t, lease.Release(t.Context()))
		})
	}
}

func TestManagedVMCgroupLimitReadbackFailsClosedOnControlErrors(t *testing.T) {
	t.Parallel()

	openErr := errors.New("injected limit open failure")
	readErr := errors.New("injected limit read failure")
	closeErr := errors.New("injected limit close failure")
	tests := map[string]struct {
		cause     error
		configure func(*testing.T, *managedVMCgroupDeps)
	}{
		"open": {
			cause: openErr,
			configure: func(_ *testing.T, deps *managedVMCgroupDeps) {
				realOpenAt2 := deps.openAt2
				deps.openAt2 = func(ctx context.Context, fd int, name string, how *unix.OpenHow) (int, error) {
					if name == "cpu.max" {
						return -1, openErr
					}
					return realOpenAt2(ctx, fd, name, how)
				}
			},
		},
		"metadata": {
			configure: func(_ *testing.T, deps *managedVMCgroupDeps) {
				controlFD := -1
				realOpenAt2 := deps.openAt2
				deps.openAt2 = func(ctx context.Context, fd int, name string, how *unix.OpenHow) (int, error) {
					opened, err := realOpenAt2(ctx, fd, name, how)
					if err == nil && name == "cpu.max" {
						controlFD = opened
					}
					return opened, err
				}
				realStatx := deps.statx
				deps.statx = func(ctx context.Context, fd int, stat *unix.Statx_t) error {
					if err := realStatx(ctx, fd, stat); err != nil {
						return err
					}
					if fd == controlFD {
						stat.Gid++
					}
					return nil
				}
			},
		},
		"mount": {
			configure: func(_ *testing.T, deps *managedVMCgroupDeps) {
				controlFD := -1
				realOpenAt2 := deps.openAt2
				deps.openAt2 = func(ctx context.Context, fd int, name string, how *unix.OpenHow) (int, error) {
					opened, err := realOpenAt2(ctx, fd, name, how)
					if err == nil && name == "cpu.max" {
						controlFD = opened
					}
					return opened, err
				}
				realStatx := deps.statx
				deps.statx = func(ctx context.Context, fd int, stat *unix.Statx_t) error {
					if err := realStatx(ctx, fd, stat); err != nil {
						return err
					}
					if fd == controlFD {
						stat.Mnt_id++
					}
					return nil
				}
			},
		},
		"read": {
			cause: readErr,
			configure: func(t *testing.T, deps *managedVMCgroupDeps) {
				realPread := deps.pread
				deps.pread = func(ctx context.Context, fd int, buffer []byte, offset int64) (int, error) {
					target, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
					require.NoError(t, err)
					if filepath.Base(target) == "cpu.max" {
						return 0, readErr
					}
					return realPread(ctx, fd, buffer, offset)
				}
			},
		},
		"close": {
			cause: closeErr,
			configure: func(t *testing.T, deps *managedVMCgroupDeps) {
				realClose := deps.close
				deps.close = func(ctx context.Context, fd int) error {
					target, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
					require.NoError(t, err)
					base := filepath.Base(target)
					err = realClose(ctx, fd)
					if base == "cpu.max" {
						return errors.Join(err, closeErr)
					}
					return err
				}
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture, prepared, _ := prepareManagedVMCgroupLimitTestBind(t)
			cleanDeps := prepared.state.deps
			tc.configure(t, &prepared.state.deps)
			membershipOpens := 0
			configuredOpenAt2 := prepared.state.deps.openAt2
			prepared.state.deps.openAt2 = func(
				ctx context.Context,
				fd int,
				name string,
				how *unix.OpenHow,
			) (int, error) {
				if name == "cgroup.procs" {
					membershipOpens++
				}
				return configuredOpenAt2(ctx, fd, name, how)
			}
			verifier := &managedVMCgroupTestProcessVerifier{}

			lease, err := prepared.Bind(
				t.Context(),
				processTestLocator(),
				processTestCgroupObservation(),
				verifier,
			)
			if tc.cause != nil {
				require.ErrorIs(t, err, tc.cause)
			}
			requireManagedVMCgroupAtomicError(t, err)
			require.NotNil(t, lease)
			assert.Zero(t, membershipOpens)
			assert.Zero(t, verifier.beforeCalls)
			assert.Zero(t, verifier.afterCalls)
			lease.state.deps = cleanDeps
			fixture.setMembership(testVMID, "")
			require.NoError(t, lease.Remove(t.Context()))
			require.NoError(t, lease.Release(t.Context()))
		})
	}
}

// Rationale: abort cleanup must preserve the launch's positive typed limit intent even though it does not verify
// Jailer-written values after the retained process has been terminated and reaped.
func TestPreparedManagedVMCgroupBindForCleanupTransfersPositiveLimitIntent(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	wantLimits := testManagedVMCgroupLimits()
	prepared, err := prepareManagedVMCgroupForLaunch(
		t.Context(), fixture.launchDeps(), testVMID, wantLimits,
	)
	require.NoError(t, err)
	require.NoError(t, prepared.MarkJailerHandoff())
	fixture.addVMLeaf(testVMID)
	fixture.setMembership(testVMID, "")

	lease, present, err := prepared.BindForCleanup(t.Context())
	require.NoError(t, err)
	require.NotNil(t, lease)
	assert.True(t, present)
	assert.True(t, lease.state.hasLimits)
	if diff := cmp.Diff(wantLimits, lease.state.limits); diff != "" {
		t.Errorf("cleanup limit intent mismatch (-want +got):\n%s", diff)
	}
	require.NoError(t, lease.Remove(t.Context()))
	require.NoError(t, lease.Release(t.Context()))
}

func TestManagedVMCgroupLimitReadbackRetriesOnSamePinnedLease(t *testing.T) {
	t.Parallel()

	fixture, prepared, limits := prepareManagedVMCgroupLimitTestBind(t)
	pidsPath := filepath.Join(fixture.vmLeafPath(testVMID), "pids.max")
	require.NoError(t, os.WriteFile(pidsPath, []byte("129\n"), 0o644))
	leafOpens := 0
	limitOpens := make(map[string]int)
	realOpenAt2 := prepared.state.deps.openAt2
	prepared.state.deps.openAt2 = func(
		ctx context.Context,
		fd int,
		name string,
		how *unix.OpenHow,
	) (int, error) {
		if name == testVMID {
			leafOpens++
		}
		if isManagedVMCgroupLimitControlTestName(name) {
			limitOpens[name]++
		}
		return realOpenAt2(ctx, fd, name, how)
	}
	verifier := &managedVMCgroupTestProcessVerifier{}
	lease, err := prepared.Bind(
		t.Context(), processTestLocator(), processTestCgroupObservation(), verifier,
	)
	requireManagedVMCgroupAtomicError(t, err)
	require.NotNil(t, lease)
	require.NoError(t, os.WriteFile(pidsPath, encodeManagedVMCgroupTestFiniteUint(limits.PIDsMax), 0o644))
	require.NoError(t, lease.VerifyMembership(
		t.Context(), processTestLocator(), processTestCgroupObservation(), verifier,
	))
	assert.Equal(t, 1, leafOpens)
	wantLimitOpens := map[string]int{
		"cpu.max": 2, "cpu.weight": 2, "memory.high": 2, "memory.max": 2, "memory.swap.max": 2, "pids.max": 2,
	}
	if diff := cmp.Diff(wantLimitOpens, limitOpens); diff != "" {
		t.Errorf("limit retry opens mismatch (-want +got):\n%s", diff)
	}
	assert.Equal(t, 1, verifier.beforeCalls)
	fixture.setMembership(testVMID, "")
	require.NoError(t, lease.Remove(t.Context()))
	require.NoError(t, lease.Release(t.Context()))
}

func TestFreshRecoveryCleanupManagedVMCgroupCarriesNoLimitIntent(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	prepared, err := prepareFreshRecoveryManagedVMCgroup(t.Context(), fixture.deps(), testVMID)
	require.NoError(t, err)
	assert.False(t, prepared.state.hasLimitIntent)
	require.NoError(t, prepared.MarkJailerHandoff())
	fixture.addVMLeaf(testVMID)
	fixture.setMembership(testVMID, "")
	lease, present, err := prepared.BindForCleanup(t.Context())
	require.NoError(t, err)
	require.NotNil(t, lease)
	assert.True(t, present)
	assert.False(t, lease.state.hasLimits)
	require.NoError(t, lease.Remove(t.Context()))
	require.NoError(t, lease.Release(t.Context()))
}

func testManagedVMCgroupLimits() model.VMCgroupLimits {
	return model.VMCgroupLimits{
		PolicyVersion: 1, CPUQuotaMicros: 200000, CPUPeriodMicros: 100000, CPUWeight: 100,
		MemoryHighBytes: 512 << 20, MemoryMaxBytes: 640 << 20, SwapMaxBytes: 0, PIDsMax: 128,
	}
}

func (fixture *managedVMCgroupTestFixture) addVMLeafWithLimits(vmID string, limits model.VMCgroupLimits) {
	fixture.t.Helper()
	fixture.addVMLeaf(vmID)
	leafPath := fixture.vmLeafPath(vmID)
	controls := map[string][]byte{
		"cpu.max":         encodeManagedVMCgroupTestCPUMax(limits.CPUQuotaMicros, limits.CPUPeriodMicros),
		"cpu.weight":      encodeManagedVMCgroupTestFiniteUint(limits.CPUWeight),
		"memory.high":     encodeManagedVMCgroupTestFiniteUint(limits.MemoryHighBytes),
		"memory.max":      encodeManagedVMCgroupTestFiniteUint(limits.MemoryMaxBytes),
		"memory.swap.max": encodeManagedVMCgroupTestFiniteUint(limits.SwapMaxBytes),
		"pids.max":        encodeManagedVMCgroupTestFiniteUint(limits.PIDsMax),
	}
	for name, raw := range controls {
		path := filepath.Join(leafPath, name)
		require.NoError(fixture.t, os.WriteFile(path, raw, 0o644))
		require.NoError(fixture.t, os.Chmod(path, 0o644))
	}
}

func encodeManagedVMCgroupTestFiniteUint(value int64) []byte {
	return []byte(strconv.FormatInt(value, 10) + "\n")
}

func encodeManagedVMCgroupTestCPUMax(quotaMicros, periodMicros int64) []byte {
	return []byte(strconv.FormatInt(quotaMicros, 10) + " " + strconv.FormatInt(periodMicros, 10) + "\n")
}

func prepareManagedVMCgroupLimitTestBind(
	t *testing.T,
) (*managedVMCgroupTestFixture, *preparedManagedVMCgroup, model.VMCgroupLimits) {
	t.Helper()
	fixture := newManagedVMCgroupTestFixture(t)
	limits := testManagedVMCgroupLimits()
	prepared, err := prepareManagedVMCgroupForLaunch(t.Context(), fixture.launchDeps(), testVMID, limits)
	require.NoError(t, err)
	require.NoError(t, prepared.MarkJailerHandoff())
	fixture.addVMLeafWithLimits(testVMID, limits)
	return fixture, prepared, limits
}

func isManagedVMCgroupLimitControlTestName(name string) bool {
	switch name {
	case "cpu.max", "cpu.weight", "memory.high", "memory.max", "memory.swap.max", "pids.max":
		return true
	default:
		return false
	}
}
