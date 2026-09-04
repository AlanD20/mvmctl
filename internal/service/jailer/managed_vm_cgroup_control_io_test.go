package jailer

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

func TestParseManagedVMCgroupControllerSet(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		raw     string
		want    managedVMCgroupControllerSet
		wantErr bool
	}{
		"duplicate required controller": {raw: "cpu memory cpu\n", wantErr: true},
		"duplicate other controller":    {raw: "cpu io io memory pids\n", wantErr: true},
		"empty token stream":            {raw: " \t\n", wantErr: true},
		"signed token":                  {raw: "+cpu memory pids\n", wantErr: true},
		"punctuated token":              {raw: "cpu memory pids,\n", wantErr: true},
		"required plus other controllers": {
			raw:  "cpuset cpu io memory hugetlb pids rdma misc dmem\n",
			want: managedVMCgroupControllerCPU | managedVMCgroupControllerMemory | managedVMCgroupControllerPIDs,
		},
		"arbitrary ASCII whitespace": {
			raw:  "\tcpu\n memory  pids \r\n",
			want: managedVMCgroupControllerCPU | managedVMCgroupControllerMemory | managedVMCgroupControllerPIDs,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := parseManagedVMCgroupControllerSet([]byte(tc.raw))
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("controller set mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// Rationale: merely parsing a valid host token set is insufficient; launch policy requires all three fixed
// controllers and must report a missing subset before any subtree-control write is attempted.
func TestRequireManagedVMCgroupControllers(t *testing.T) {
	t.Parallel()

	required := managedVMCgroupControllerCPU | managedVMCgroupControllerMemory | managedVMCgroupControllerPIDs
	tests := map[string]struct {
		available managedVMCgroupControllerSet
		wantErr   bool
	}{
		"missing cpu":    {available: managedVMCgroupControllerMemory | managedVMCgroupControllerPIDs, wantErr: true},
		"missing memory": {available: managedVMCgroupControllerCPU | managedVMCgroupControllerPIDs, wantErr: true},
		"missing pids":   {available: managedVMCgroupControllerCPU | managedVMCgroupControllerMemory, wantErr: true},
		"all required":   {available: required},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			err := requireManagedVMCgroupControllers(tc.available, required)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestFormatManagedVMCgroupControllerEnablement(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		controllers managedVMCgroupControllerSet
		want        []byte
		wantErr     bool
	}{
		"empty":       {wantErr: true},
		"unknown bit": {controllers: managedVMCgroupControllerSet(1 << 7), wantErr: true},
		"fixed order": {
			controllers: managedVMCgroupControllerPIDs | managedVMCgroupControllerCPU,
			want:        []byte("+cpu +pids\n"),
		},
		"all required": {
			controllers: managedVMCgroupControllerCPU | managedVMCgroupControllerMemory | managedVMCgroupControllerPIDs,
			want:        []byte("+cpu +memory +pids\n"),
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := formatManagedVMCgroupControllerEnablement(tc.controllers)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("controller enablement mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestParseManagedVMCgroupFiniteUint(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		raw     string
		want    uint64
		wantErr bool
	}{
		"max keyword":     {raw: "max\n", wantErr: true},
		"negative":        {raw: "-1\n", wantErr: true},
		"positive sign":   {raw: "+1\n", wantErr: true},
		"leading zero":    {raw: "01\n", wantErr: true},
		"missing newline": {raw: "1", wantErr: true},
		"extra field":     {raw: "1 2\n", wantErr: true},
		"trailing space":  {raw: "1 \n", wantErr: true},
		"overflow":        {raw: "18446744073709551616\n", wantErr: true},
		"zero":            {raw: "0\n", want: 0},
		"maximum uint64":  {raw: "18446744073709551615\n", want: math.MaxUint64},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := parseManagedVMCgroupFiniteUint([]byte(tc.raw))
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestFormatManagedVMCgroupFiniteUint(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		value uint64
		want  []byte
	}{
		"zero":           {value: 0, want: []byte("0\n")},
		"maximum uint64": {value: math.MaxUint64, want: []byte("18446744073709551615\n")},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, formatManagedVMCgroupFiniteUint(tc.value)); diff != "" {
				t.Errorf("finite control value mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestParseManagedVMCgroupCPUMax(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		raw     string
		want    managedVMCgroupCPUMax
		wantErr bool
	}{
		"max quota":        {raw: "max 100000\n", wantErr: true},
		"signed quota":     {raw: "+50000 100000\n", wantErr: true},
		"zero quota":       {raw: "0 100000\n", wantErr: true},
		"zero period":      {raw: "50000 0\n", wantErr: true},
		"leading zero":     {raw: "050000 100000\n", wantErr: true},
		"extra field":      {raw: "50000 100000 2\n", wantErr: true},
		"noncanonical gap": {raw: "50000  100000\n", wantErr: true},
		"missing newline":  {raw: "50000 100000", wantErr: true},
		"overflow":         {raw: "18446744073709551616 100000\n", wantErr: true},
		"finite value": {
			raw:  "50000 100000\n",
			want: managedVMCgroupCPUMax{quotaMicros: 50000, periodMicros: 100000},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := parseManagedVMCgroupCPUMax([]byte(tc.raw))
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(managedVMCgroupCPUMax{})); diff != "" {
				t.Errorf("cpu.max mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestFormatManagedVMCgroupCPUMax(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		value   managedVMCgroupCPUMax
		want    []byte
		wantErr bool
	}{
		"zero quota":  {value: managedVMCgroupCPUMax{periodMicros: 100000}, wantErr: true},
		"zero period": {value: managedVMCgroupCPUMax{quotaMicros: 50000}, wantErr: true},
		"finite value": {
			value: managedVMCgroupCPUMax{quotaMicros: 50000, periodMicros: 100000},
			want:  []byte("50000 100000\n"),
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := formatManagedVMCgroupCPUMax(tc.value)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("cpu.max format mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestReadAndWriteManagedVMCgroupControlUseFixedDescriptorRelativeFiles(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	fixture.addVMLeaf(testVMID)
	controllersPath := filepath.Join(fixture.vmLeafPath(testVMID), "cgroup.controllers")
	require.NoError(t, os.WriteFile(controllersPath, []byte("cpu memory pids\n"), 0o444))
	cpuMaxPath := filepath.Join(fixture.vmLeafPath(testVMID), "cpu.max")
	require.NoError(t, os.WriteFile(cpuMaxPath, nil, 0o644))

	deps, leafFD, leaf := openManagedVMCgroupControlTestLeaf(t, fixture)
	type openCall struct {
		Name    string
		Flags   uint64
		Resolve uint64
	}
	var opened []openCall
	realOpenAt2 := deps.openAt2
	deps.openAt2 = func(ctx context.Context, parentFD int, name string, how *unix.OpenHow) (int, error) {
		opened = append(opened, openCall{Name: name, Flags: how.Flags, Resolve: how.Resolve})
		return realOpenAt2(ctx, parentFD, name, how)
	}
	mutationCalls := 0
	deps.mkdirAt = func(context.Context, int, string, uint32) error { mutationCalls++; return nil }
	deps.fchown = func(context.Context, int, int, int) error { mutationCalls++; return nil }
	deps.fchmod = func(context.Context, int, uint32) error { mutationCalls++; return nil }
	deps.unlinkAt = func(context.Context, int, string, int) error { mutationCalls++; return nil }

	controllers, err := readManagedVMCgroupControl(
		t.Context(), deps, leafFD, leaf, managedVMCgroupControllersControl,
	)
	require.NoError(t, err)
	if diff := cmp.Diff([]byte("cpu memory pids\n"), controllers); diff != "" {
		t.Errorf("read control mismatch (-want +got):\n%s", diff)
	}
	require.NoError(t, writeManagedVMCgroupControl(
		t.Context(), deps, leafFD, leaf, managedVMCgroupCPUMaxControl, []byte("50000 100000\n"),
	))
	written, err := os.ReadFile(cpuMaxPath)
	require.NoError(t, err)
	if diff := cmp.Diff([]byte("50000 100000\n"), written); diff != "" {
		t.Errorf("written control mismatch (-want +got):\n%s", diff)
	}
	wantOpened := []openCall{
		{
			Name:    "cgroup.controllers",
			Flags:   uint64(unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC | unix.O_NOFOLLOW),
			Resolve: uint64(managedVMCgroupResolveFlags),
		},
		{
			Name:    "cpu.max",
			Flags:   uint64(unix.O_WRONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW),
			Resolve: uint64(managedVMCgroupResolveFlags),
		},
	}
	if diff := cmp.Diff(wantOpened, opened); diff != "" {
		t.Errorf("descriptor-relative control opens mismatch (-want +got):\n%s", diff)
	}
	assert.Zero(t, mutationCalls)
}

// Rationale: a fixed control basename is not sufficient authority. Every opened pseudo-file must independently be an
// exact root-owned regular file on the retained directory's mount and device before any bytes are consumed.
func TestReadManagedVMCgroupControlRejectsUnsafeFile(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		prepare   func(*testing.T, string)
		configure func(*testing.T, *managedVMCgroupDeps, int)
	}{
		"symlink": {prepare: func(t *testing.T, path string) {
			require.NoError(t, os.Symlink("cgroup.procs", path))
		}},
		"directory": {prepare: func(t *testing.T, path string) {
			require.NoError(t, os.Mkdir(path, 0o644))
		}},
		"wrong mode": {prepare: func(t *testing.T, path string) {
			require.NoError(t, os.WriteFile(path, []byte("1024\n"), 0o600))
		}},
		"wrong owner": {
			prepare: createManagedVMCgroupControlTestFile,
			configure: func(_ *testing.T, deps *managedVMCgroupDeps, leafFD int) {
				realStatx := deps.statx
				deps.statx = func(ctx context.Context, fd int, stat *unix.Statx_t) error {
					if err := realStatx(ctx, fd, stat); err != nil {
						return err
					}
					if fd != leafFD {
						stat.Uid++
					}
					return nil
				}
			},
		},
		"wrong mount": {
			prepare: createManagedVMCgroupControlTestFile,
			configure: func(_ *testing.T, deps *managedVMCgroupDeps, leafFD int) {
				realStatx := deps.statx
				deps.statx = func(ctx context.Context, fd int, stat *unix.Statx_t) error {
					if err := realStatx(ctx, fd, stat); err != nil {
						return err
					}
					if fd != leafFD {
						stat.Mnt_id++
					}
					return nil
				}
			},
		},
		"wrong device": {
			prepare: createManagedVMCgroupControlTestFile,
			configure: func(_ *testing.T, deps *managedVMCgroupDeps, leafFD int) {
				realStatx := deps.statx
				deps.statx = func(ctx context.Context, fd int, stat *unix.Statx_t) error {
					if err := realStatx(ctx, fd, stat); err != nil {
						return err
					}
					if fd != leafFD {
						stat.Dev_minor++
					}
					return nil
				}
			},
		},
		"stat failure": {
			prepare: createManagedVMCgroupControlTestFile,
			configure: func(_ *testing.T, deps *managedVMCgroupDeps, leafFD int) {
				realStatx := deps.statx
				deps.statx = func(ctx context.Context, fd int, stat *unix.Statx_t) error {
					if fd != leafFD {
						return unix.EIO
					}
					return realStatx(ctx, fd, stat)
				}
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newManagedVMCgroupTestFixture(t)
			fixture.addVMLeaf(testVMID)
			path := filepath.Join(fixture.vmLeafPath(testVMID), "memory.max")
			tc.prepare(t, path)
			deps, leafFD, leaf := openManagedVMCgroupControlTestLeaf(t, fixture)
			if tc.configure != nil {
				tc.configure(t, &deps, leafFD)
			}

			_, err := readManagedVMCgroupControl(
				t.Context(), deps, leafFD, leaf, managedVMCgroupMemoryMaxControl,
			)
			requireManagedVMCgroupAtomicError(t, err)
		})
	}
}

func TestReadManagedVMCgroupControlFailsClosedOnOpen(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	fixture.addVMLeaf(testVMID)
	deps, leafFD, leaf := openManagedVMCgroupControlTestLeaf(t, fixture)
	deps.openAt2 = func(context.Context, int, string, *unix.OpenHow) (int, error) { return -1, unix.EIO }

	_, err := readManagedVMCgroupControl(t.Context(), deps, leafFD, leaf, managedVMCgroupMemoryMaxControl)
	require.ErrorIs(t, err, unix.EIO)
	requireManagedVMCgroupAtomicError(t, err)
}

func TestReadManagedVMCgroupControlPreservesInspectionAndCloseErrors(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	fixture.addVMLeaf(testVMID)
	createManagedVMCgroupControlTestFile(t, filepath.Join(fixture.vmLeafPath(testVMID), "memory.max"))
	deps, leafFD, leaf := openManagedVMCgroupControlTestLeaf(t, fixture)
	statErr := errors.New("injected control stat failure")
	closeErr := errors.New("injected rejected-control close failure")
	realStatx := deps.statx
	deps.statx = func(ctx context.Context, fd int, stat *unix.Statx_t) error {
		if fd != leafFD {
			return statErr
		}
		return realStatx(ctx, fd, stat)
	}
	realClose := deps.close
	closed := 0
	deps.close = func(ctx context.Context, fd int) error {
		closed++
		return errors.Join(realClose(ctx, fd), closeErr)
	}

	_, err := readManagedVMCgroupControl(t.Context(), deps, leafFD, leaf, managedVMCgroupMemoryMaxControl)
	require.ErrorIs(t, err, statErr)
	require.ErrorIs(t, err, closeErr)
	requireManagedVMCgroupAtomicError(t, err)
	assert.Equal(t, 1, closed)
}

// Rationale: after openat2 returns, all inspection and I/O must stay on that descriptor. A same-name replacement in
// the test filesystem must remain untouched and cannot redirect the read through a reconstructed path.
func TestReadManagedVMCgroupControlRetainsOpenedFileAcrossNameReplacement(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	fixture.addVMLeaf(testVMID)
	path := filepath.Join(fixture.vmLeafPath(testVMID), "memory.max")
	require.NoError(t, os.WriteFile(path, []byte("1024\n"), 0o644))
	deps, leafFD, leaf := openManagedVMCgroupControlTestLeaf(t, fixture)
	realOpenAt2 := deps.openAt2
	deps.openAt2 = func(ctx context.Context, parentFD int, name string, how *unix.OpenHow) (int, error) {
		fd, err := realOpenAt2(ctx, parentFD, name, how)
		if err != nil {
			return fd, err
		}
		detached := path + ".detached"
		require.NoError(t, os.Rename(path, detached))
		require.NoError(t, os.WriteFile(path, []byte("replacement\n"), 0o644))
		return fd, nil
	}

	got, err := readManagedVMCgroupControl(t.Context(), deps, leafFD, leaf, managedVMCgroupMemoryMaxControl)
	require.NoError(t, err)
	if diff := cmp.Diff([]byte("1024\n"), got); diff != "" {
		t.Errorf("descriptor-pinned replacement read mismatch (-want +got):\n%s", diff)
	}
	replacement, err := os.ReadFile(path)
	require.NoError(t, err)
	if diff := cmp.Diff([]byte("replacement\n"), replacement); diff != "" {
		t.Errorf("replacement file was mutated (-want +got):\n%s", diff)
	}
}

func TestReadManagedVMCgroupControlEnforcesBoundedWholeRead(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		raw     []byte
		want    []byte
		wantErr bool
	}{
		"empty EOF": {raw: []byte{}, want: []byte{}},
		"exact cap": {
			raw:  make([]byte, managedVMCgroupControlMaxBytes),
			want: make([]byte, managedVMCgroupControlMaxBytes),
		},
		"one beyond cap": {raw: make([]byte, managedVMCgroupControlMaxBytes+1), wantErr: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newManagedVMCgroupTestFixture(t)
			fixture.addVMLeaf(testVMID)
			createManagedVMCgroupControlTestFile(t, filepath.Join(fixture.vmLeafPath(testVMID), "memory.max"))
			deps, leafFD, leaf := openManagedVMCgroupControlTestLeaf(t, fixture)
			deps.pread = func(_ context.Context, _ int, buffer []byte, offset int64) (int, error) {
				if offset >= int64(len(tc.raw)) {
					return 0, nil
				}
				return copy(buffer, tc.raw[offset:]), nil
			}

			got, err := readManagedVMCgroupControl(
				t.Context(), deps, leafFD, leaf, managedVMCgroupMemoryMaxControl,
			)
			if tc.wantErr {
				requireManagedVMCgroupAtomicError(t, err)
				return
			}
			require.NoError(t, err)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("bounded control read mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// Rationale: the positioned-read dependency is a syscall seam and may report impossible counts. Treating one as a
// slice bound or progress signal could panic, truncate authority evidence, or spin indefinitely.
func TestReadManagedVMCgroupControlRejectsReadFailuresAndInvalidCounts(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		pread func(context.Context, int, []byte, int64) (int, error)
		cause error
	}{
		"read failure": {
			pread: func(context.Context, int, []byte, int64) (int, error) { return 0, unix.EIO },
			cause: unix.EIO,
		},
		"negative count": {
			pread: func(context.Context, int, []byte, int64) (int, error) { return -1, nil },
		},
		"over buffer count": {
			pread: func(_ context.Context, _ int, buffer []byte, _ int64) (int, error) {
				return len(buffer) + 1, nil
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newManagedVMCgroupTestFixture(t)
			fixture.addVMLeaf(testVMID)
			createManagedVMCgroupControlTestFile(t, filepath.Join(fixture.vmLeafPath(testVMID), "memory.max"))
			deps, leafFD, leaf := openManagedVMCgroupControlTestLeaf(t, fixture)
			deps.pread = tc.pread

			_, err := readManagedVMCgroupControl(
				t.Context(), deps, leafFD, leaf, managedVMCgroupMemoryMaxControl,
			)
			if tc.cause != nil {
				require.ErrorIs(t, err, tc.cause)
			}
			requireManagedVMCgroupAtomicError(t, err)
		})
	}
}

func TestReadManagedVMCgroupControlRetriesEINTRAndObservesCancellation(t *testing.T) {
	t.Parallel()

	t.Run("retries EINTR", func(t *testing.T) {
		fixture := newManagedVMCgroupTestFixture(t)
		fixture.addVMLeaf(testVMID)
		createManagedVMCgroupControlTestFile(t, filepath.Join(fixture.vmLeafPath(testVMID), "memory.max"))
		deps, leafFD, leaf := openManagedVMCgroupControlTestLeaf(t, fixture)
		raw := []byte("1024\n")
		calls := 0
		deps.pread = func(_ context.Context, _ int, buffer []byte, offset int64) (int, error) {
			calls++
			if calls == 1 {
				return 0, unix.EINTR
			}
			if offset >= int64(len(raw)) {
				return 0, nil
			}
			return copy(buffer, raw[offset:]), nil
		}

		got, err := readManagedVMCgroupControl(
			t.Context(), deps, leafFD, leaf, managedVMCgroupMemoryMaxControl,
		)
		require.NoError(t, err)
		if diff := cmp.Diff(raw, got); diff != "" {
			t.Errorf("EINTR retry read mismatch (-want +got):\n%s", diff)
		}
		assert.Equal(t, 3, calls)
	})

	t.Run("checks cancellation after EINTR", func(t *testing.T) {
		fixture := newManagedVMCgroupTestFixture(t)
		fixture.addVMLeaf(testVMID)
		createManagedVMCgroupControlTestFile(t, filepath.Join(fixture.vmLeafPath(testVMID), "memory.max"))
		deps, leafFD, leaf := openManagedVMCgroupControlTestLeaf(t, fixture)
		ctx, cancel := context.WithCancel(t.Context())
		calls := 0
		deps.pread = func(context.Context, int, []byte, int64) (int, error) {
			calls++
			cancel()
			return 0, unix.EINTR
		}

		_, err := readManagedVMCgroupControl(ctx, deps, leafFD, leaf, managedVMCgroupMemoryMaxControl)
		require.ErrorIs(t, err, context.Canceled)
		requireManagedVMCgroupAtomicError(t, err)
		assert.Equal(t, 1, calls)
	})
}

func TestWriteManagedVMCgroupControlRejectsFailuresAndNonFullWrites(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		write     func(context.Context, int, []byte) (int, error)
		cause     error
		wantCalls int
	}{
		"write failure": {
			write: func(context.Context, int, []byte) (int, error) { return 0, unix.EIO },
			cause: unix.EIO, wantCalls: 1,
		},
		"negative count": {
			write:     func(context.Context, int, []byte) (int, error) { return -1, nil },
			wantCalls: 1,
		},
		"over payload count": {
			write:     func(_ context.Context, _ int, value []byte) (int, error) { return len(value) + 1, nil },
			wantCalls: 1,
		},
		"zero write": {
			write:     func(context.Context, int, []byte) (int, error) { return 0, nil },
			wantCalls: 1,
		},
		"short successful write": {
			write:     func(context.Context, int, []byte) (int, error) { return 1, nil },
			wantCalls: 1,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newManagedVMCgroupTestFixture(t)
			fixture.addVMLeaf(testVMID)
			createManagedVMCgroupControlTestFile(t, filepath.Join(fixture.vmLeafPath(testVMID), "cpu.max"))
			deps, leafFD, leaf := openManagedVMCgroupControlTestLeaf(t, fixture)
			calls := 0
			deps.write = func(ctx context.Context, fd int, value []byte) (int, error) {
				calls++
				return tc.write(ctx, fd, value)
			}

			err := writeManagedVMCgroupControl(
				t.Context(), deps, leafFD, leaf, managedVMCgroupCPUMaxControl, []byte("50000 100000\n"),
			)
			if tc.cause != nil {
				require.ErrorIs(t, err, tc.cause)
			}
			requireManagedVMCgroupAtomicError(t, err)
			assert.Equal(t, tc.wantCalls, calls)
		})
	}
}

func TestWriteManagedVMCgroupControlRetriesEINTRAndObservesCancellation(t *testing.T) {
	t.Parallel()

	t.Run("retries EINTR before one full write", func(t *testing.T) {
		fixture := newManagedVMCgroupTestFixture(t)
		fixture.addVMLeaf(testVMID)
		path := filepath.Join(fixture.vmLeafPath(testVMID), "cpu.max")
		createManagedVMCgroupControlTestFile(t, path)
		deps, leafFD, leaf := openManagedVMCgroupControlTestLeaf(t, fixture)
		realWrite := deps.write
		calls := 0
		deps.write = func(ctx context.Context, fd int, value []byte) (int, error) {
			calls++
			if calls == 1 {
				return 0, unix.EINTR
			}
			return realWrite(ctx, fd, value)
		}

		err := writeManagedVMCgroupControl(
			t.Context(), deps, leafFD, leaf, managedVMCgroupCPUMaxControl, []byte("50000 100000\n"),
		)
		require.NoError(t, err)
		assert.Equal(t, 2, calls)
		got, err := os.ReadFile(path)
		require.NoError(t, err)
		if diff := cmp.Diff([]byte("50000 100000\n"), got); diff != "" {
			t.Errorf("EINTR write mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("checks cancellation after EINTR", func(t *testing.T) {
		fixture := newManagedVMCgroupTestFixture(t)
		fixture.addVMLeaf(testVMID)
		createManagedVMCgroupControlTestFile(t, filepath.Join(fixture.vmLeafPath(testVMID), "cpu.max"))
		deps, leafFD, leaf := openManagedVMCgroupControlTestLeaf(t, fixture)
		ctx, cancel := context.WithCancel(t.Context())
		calls := 0
		deps.write = func(context.Context, int, []byte) (int, error) {
			calls++
			cancel()
			return 0, unix.EINTR
		}

		err := writeManagedVMCgroupControl(
			ctx, deps, leafFD, leaf, managedVMCgroupCPUMaxControl, []byte("50000 100000\n"),
		)
		require.ErrorIs(t, err, context.Canceled)
		requireManagedVMCgroupAtomicError(t, err)
		assert.Equal(t, 1, calls)
	})
}

// Rationale: the production dependency itself must not enter write(2) after cancellation. An invalid descriptor
// distinguishes the required context error from an accidental EBADF syscall result.
func TestRealManagedVMCgroupWriteChecksContextBeforeSyscall(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	count, err := realManagedVMCgroupDeps().write(ctx, -1, []byte("value"))
	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, count)
}

// Rationale: closing the pseudo-file is part of the checked operation even after cancellation or a primary syscall
// failure. Both causes must remain discoverable, and cleanup must receive the caller's context values without its
// cancellation.
func TestManagedVMCgroupControlIOPreservesCloseErrorsWithoutCancellation(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		controlName string
		operation   func(context.Context, context.CancelFunc, managedVMCgroupDeps, int, managedVMCgroupIdentity) error
	}{
		"read primary and close errors": {
			controlName: "memory.max",
			operation: func(
				ctx context.Context,
				cancel context.CancelFunc,
				deps managedVMCgroupDeps,
				leafFD int,
				leaf managedVMCgroupIdentity,
			) error {
				deps.pread = func(context.Context, int, []byte, int64) (int, error) {
					cancel()
					return 0, unix.EIO
				}
				_, err := readManagedVMCgroupControl(
					ctx, deps, leafFD, leaf, managedVMCgroupMemoryMaxControl,
				)
				return err
			},
		},
		"write primary and close errors": {
			controlName: "cpu.max",
			operation: func(
				ctx context.Context,
				cancel context.CancelFunc,
				deps managedVMCgroupDeps,
				leafFD int,
				leaf managedVMCgroupIdentity,
			) error {
				deps.write = func(context.Context, int, []byte) (int, error) {
					cancel()
					return 0, unix.EIO
				}
				return writeManagedVMCgroupControl(
					ctx, deps, leafFD, leaf, managedVMCgroupCPUMaxControl, []byte("50000 100000\n"),
				)
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newManagedVMCgroupTestFixture(t)
			fixture.addVMLeaf(testVMID)
			createManagedVMCgroupControlTestFile(t, filepath.Join(fixture.vmLeafPath(testVMID), tc.controlName))
			deps, leafFD, leaf := openManagedVMCgroupControlTestLeaf(t, fixture)
			ctx, cancel := context.WithCancel(context.WithValue(
				t.Context(), managedVMCgroupControlContextKey{}, "retained",
			))
			closeErr := errors.New("injected control close failure")
			realClose := deps.close
			closed := 0
			deps.close = func(closeCtx context.Context, fd int) error {
				closed++
				assert.NoError(t, closeCtx.Err())
				assert.Equal(t, "retained", closeCtx.Value(managedVMCgroupControlContextKey{}))
				err := realClose(closeCtx, fd)
				return errors.Join(err, closeErr)
			}
			err := tc.operation(ctx, cancel, deps, leafFD, leaf)
			require.ErrorIs(t, err, unix.EIO)
			require.ErrorIs(t, err, closeErr)
			requireManagedVMCgroupAtomicError(t, err)
			assert.Equal(t, 1, closed)
			require.ErrorIs(t, ctx.Err(), context.Canceled)
		})
	}
}

func TestManagedVMCgroupControlIORejectsInvalidControlOrWritePayload(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	fixture.addVMLeaf(testVMID)
	deps, leafFD, leaf := openManagedVMCgroupControlTestLeaf(t, fixture)
	openCalls := 0
	deps.openAt2 = func(context.Context, int, string, *unix.OpenHow) (int, error) {
		openCalls++
		return -1, errors.New("unexpected open")
	}

	_, err := readManagedVMCgroupControl(
		t.Context(), deps, leafFD, leaf, managedVMCgroupControlFile(255),
	)
	requireManagedVMCgroupAtomicError(t, err)
	err = writeManagedVMCgroupControl(
		t.Context(), deps, leafFD, leaf, managedVMCgroupControllersControl, []byte("cpu\n"),
	)
	requireManagedVMCgroupAtomicError(t, err)
	err = writeManagedVMCgroupControl(
		t.Context(), deps, leafFD, leaf, managedVMCgroupCPUMaxControl, nil,
	)
	requireManagedVMCgroupAtomicError(t, err)
	assert.Zero(t, openCalls)
}

type managedVMCgroupControlContextKey struct{}

func createManagedVMCgroupControlTestFile(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte("1024\n"), 0o644))
}

func openManagedVMCgroupControlTestLeaf(
	t *testing.T,
	fixture *managedVMCgroupTestFixture,
) (managedVMCgroupDeps, int, managedVMCgroupIdentity) {
	t.Helper()
	deps := fixture.deps()
	leafFD, err := unix.Open(fixture.vmLeafPath(testVMID), managedVMCgroupDirectoryFlags, 0)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, unix.Close(leafFD)) })
	leaf, err := inspectManagedVMCgroupDirectory(
		t.Context(), deps, leafFD, "test managed VM cgroup", managedVMCgroupLeafMode,
	)
	require.NoError(t, err)
	return deps, leafFD, leaf
}

func requireManagedVMCgroupAtomicError(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
}
