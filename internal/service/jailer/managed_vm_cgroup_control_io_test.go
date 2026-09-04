package jailer

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

// Rationale: every fixed cgroup pseudo-file has an independent metadata and write-authority contract. A neighboring
// spec must not accidentally lend its basename, mode, or writable bit to cgroup.subtree_control or pids.max.
func TestManagedVMCgroupControlSpec(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		control managedVMCgroupControlFile
		want    managedVMCgroupControlFileSpec
	}{
		"controllers": {
			control: managedVMCgroupControllersControl,
			want:    managedVMCgroupControlFileSpec{name: "cgroup.controllers", mode: 0o444, writable: false},
		},
		"subtree control": {
			control: managedVMCgroupSubtreeControl,
			want:    managedVMCgroupControlFileSpec{name: "cgroup.subtree_control", mode: 0o644, writable: true},
		},
		"CPU maximum": {
			control: managedVMCgroupCPUMaxControl,
			want:    managedVMCgroupControlFileSpec{name: "cpu.max", mode: 0o644, writable: true},
		},
		"CPU weight": {
			control: managedVMCgroupCPUWeightControl,
			want:    managedVMCgroupControlFileSpec{name: "cpu.weight", mode: 0o644, writable: true},
		},
		"memory high": {
			control: managedVMCgroupMemoryHighControl,
			want:    managedVMCgroupControlFileSpec{name: "memory.high", mode: 0o644, writable: true},
		},
		"memory maximum": {
			control: managedVMCgroupMemoryMaxControl,
			want:    managedVMCgroupControlFileSpec{name: "memory.max", mode: 0o644, writable: true},
		},
		"swap maximum": {
			control: managedVMCgroupMemorySwapMaxControl,
			want:    managedVMCgroupControlFileSpec{name: "memory.swap.max", mode: 0o644, writable: true},
		},
		"process maximum": {
			control: managedVMCgroupPIDsMaxControl,
			want:    managedVMCgroupControlFileSpec{name: "pids.max", mode: 0o644, writable: true},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := managedVMCgroupControlSpec(tc.control)
			require.NoError(t, err)
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(managedVMCgroupControlFileSpec{})); diff != "" {
				t.Errorf("control spec mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestParseManagedVMCgroupControllerSet(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		raw     string
		want    managedVMCgroupControllerSet
		wantErr bool
	}{
		"duplicate required controller": {raw: "cpu memory cpu\n", wantErr: true},
		"duplicate other controller":    {raw: "cpu io io memory pids\n", wantErr: true},
		"empty token stream":            {raw: " \t\n", want: 0},
		"signed token":                  {raw: "+cpu memory pids\n", wantErr: true},
		"punctuated token":              {raw: "cpu memory pids,\n", wantErr: true},
		"uppercase token":               {raw: "CPU memory pids\n", wantErr: true},
		"leading digit token":           {raw: "2cpu memory pids\n", wantErr: true},
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
	pidsMaxPath := filepath.Join(fixture.vmLeafPath(testVMID), "pids.max")
	require.NoError(t, os.WriteFile(pidsMaxPath, []byte("128\n"), 0o644))

	deps, leafFD, leaf := openManagedVMCgroupControlTestLeaf(t, fixture)
	type openCall struct {
		Name    string
		Flags   uint64
		Resolve uint64
	}
	var opened []openCall
	closed := 0
	realOpenAt2 := deps.openAt2
	deps.openAt2 = func(ctx context.Context, parentFD int, name string, how *unix.OpenHow) (int, error) {
		opened = append(opened, openCall{Name: name, Flags: how.Flags, Resolve: how.Resolve})
		return realOpenAt2(ctx, parentFD, name, how)
	}
	realClose := deps.close
	deps.close = func(ctx context.Context, fd int) error {
		closed++
		return realClose(ctx, fd)
	}
	mutationCalls := 0
	deps.mkdirAt = func(context.Context, int, string, uint32) error { mutationCalls++; return nil }
	deps.fchown = func(context.Context, int, int, int) error { mutationCalls++; return nil }
	deps.fchmod = func(context.Context, int, uint32) error { mutationCalls++; return nil }
	deps.unlinkAt = func(context.Context, int, string, int) error { mutationCalls++; return nil }

	controllers, err := readManagedVMCgroupControllers(t.Context(), deps, leafFD, leaf)
	require.NoError(t, err)
	wantControllers := managedVMCgroupControllerCPU |
		managedVMCgroupControllerMemory |
		managedVMCgroupControllerPIDs
	if diff := cmp.Diff(wantControllers, controllers); diff != "" {
		t.Errorf("read control mismatch (-want +got):\n%s", diff)
	}
	pidsMax, err := readManagedVMCgroupPIDsMax(t.Context(), deps, leafFD, leaf)
	require.NoError(t, err)
	assert.Equal(t, uint64(128), pidsMax)
	require.NoError(t, writeManagedVMCgroupCPUMax(
		t.Context(), deps, leafFD, leaf, managedVMCgroupCPUMax{quotaMicros: 50000, periodMicros: 100000},
	))
	written, err := os.ReadFile(cpuMaxPath)
	require.NoError(t, err)
	if diff := cmp.Diff([]byte("50000 100000\n"), written); diff != "" {
		t.Errorf("written control mismatch (-want +got):\n%s", diff)
	}
	wantOpened := []openCall{
		{
			Name:  "cgroup.controllers",
			Flags: uint64(unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC | unix.O_NOFOLLOW),
			Resolve: uint64(
				unix.RESOLVE_BENEATH |
					unix.RESOLVE_NO_SYMLINKS |
					unix.RESOLVE_NO_MAGICLINKS |
					unix.RESOLVE_NO_XDEV,
			),
		},
		{
			Name:  "pids.max",
			Flags: uint64(unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC | unix.O_NOFOLLOW),
			Resolve: uint64(
				unix.RESOLVE_BENEATH |
					unix.RESOLVE_NO_SYMLINKS |
					unix.RESOLVE_NO_MAGICLINKS |
					unix.RESOLVE_NO_XDEV,
			),
		},
		{
			Name:  "cpu.max",
			Flags: uint64(unix.O_WRONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW),
			Resolve: uint64(
				unix.RESOLVE_BENEATH |
					unix.RESOLVE_NO_SYMLINKS |
					unix.RESOLVE_NO_MAGICLINKS |
					unix.RESOLVE_NO_XDEV,
			),
		},
	}
	if diff := cmp.Diff(wantOpened, opened); diff != "" {
		t.Errorf("descriptor-relative control opens mismatch (-want +got):\n%s", diff)
	}
	assert.Equal(t, 3, closed)
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
		"wrong group": {
			prepare: createManagedVMCgroupControlTestFile,
			configure: func(_ *testing.T, deps *managedVMCgroupDeps, leafFD int) {
				realStatx := deps.statx
				deps.statx = func(ctx context.Context, fd int, stat *unix.Statx_t) error {
					if err := realStatx(ctx, fd, stat); err != nil {
						return err
					}
					if fd != leafFD {
						stat.Gid++
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
		"wrong device major": {
			prepare: createManagedVMCgroupControlTestFile,
			configure: func(_ *testing.T, deps *managedVMCgroupDeps, leafFD int) {
				realStatx := deps.statx
				deps.statx = func(ctx context.Context, fd int, stat *unix.Statx_t) error {
					if err := realStatx(ctx, fd, stat); err != nil {
						return err
					}
					if fd != leafFD {
						stat.Dev_major++
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

			_, err := readManagedVMCgroupMemoryMax(t.Context(), deps, leafFD, leaf)
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

	_, err := readManagedVMCgroupMemoryMax(t.Context(), deps, leafFD, leaf)
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

	_, err := readManagedVMCgroupMemoryMax(t.Context(), deps, leafFD, leaf)
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

	got, err := readManagedVMCgroupMemoryMax(t.Context(), deps, leafFD, leaf)
	require.NoError(t, err)
	assert.Equal(t, uint64(1024), got)
	replacement, err := os.ReadFile(path)
	require.NoError(t, err)
	if diff := cmp.Diff([]byte("replacement\n"), replacement); diff != "" {
		t.Errorf("replacement file was mutated (-want +got):\n%s", diff)
	}
}

// Rationale: the typed write must target only the regular file proven through its retained descriptor. Replacing the
// same basename after open must not redirect the semantic write to the new object.
func TestWriteManagedVMCgroupControlRetainsOpenedFileAcrossNameReplacement(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	fixture.addVMLeaf(testVMID)
	path := filepath.Join(fixture.vmLeafPath(testVMID), "cpu.max")
	replacement := []byte("replacement remains\n")
	require.NoError(t, os.WriteFile(path, []byte("old detached value\n"), 0o644))
	deps, leafFD, leaf := openManagedVMCgroupControlTestLeaf(t, fixture)
	realOpenAt2 := deps.openAt2
	deps.openAt2 = func(ctx context.Context, parentFD int, name string, how *unix.OpenHow) (int, error) {
		fd, err := realOpenAt2(ctx, parentFD, name, how)
		if err != nil {
			return fd, err
		}
		detached := path + ".detached"
		require.NoError(t, os.Rename(path, detached))
		require.NoError(t, os.WriteFile(path, replacement, 0o644))
		return fd, nil
	}

	err := writeManagedVMCgroupCPUMax(
		t.Context(),
		deps,
		leafFD,
		leaf,
		managedVMCgroupCPUMax{quotaMicros: 50000, periodMicros: 100000},
	)
	require.NoError(t, err)
	detached, err := os.ReadFile(path + ".detached")
	require.NoError(t, err)
	if diff := cmp.Diff([]byte("50000 100000\nvalue\n"), detached); diff != "" {
		t.Errorf("detached control mismatch (-want +got):\n%s", diff)
	}
	gotReplacement, err := os.ReadFile(path)
	require.NoError(t, err)
	if diff := cmp.Diff(replacement, gotReplacement); diff != "" {
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

			got, err := readManagedVMCgroupControlBytes(
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
		"partial EINTR": {
			pread: func(context.Context, int, []byte, int64) (int, error) { return 1, unix.EINTR },
			cause: unix.EINTR,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newManagedVMCgroupTestFixture(t)
			fixture.addVMLeaf(testVMID)
			createManagedVMCgroupControlTestFile(t, filepath.Join(fixture.vmLeafPath(testVMID), "memory.max"))
			deps, leafFD, leaf := openManagedVMCgroupControlTestLeaf(t, fixture)
			deps.pread = tc.pread

			_, err := readManagedVMCgroupMemoryMax(t.Context(), deps, leafFD, leaf)
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

		got, err := readManagedVMCgroupMemoryMax(t.Context(), deps, leafFD, leaf)
		require.NoError(t, err)
		assert.Equal(t, uint64(1024), got)
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

		_, err := readManagedVMCgroupMemoryMax(ctx, deps, leafFD, leaf)
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
		"partial EINTR": {
			write:     func(context.Context, int, []byte) (int, error) { return 1, unix.EINTR },
			cause:     unix.EINTR,
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

			err := writeManagedVMCgroupCPUMax(
				t.Context(),
				deps,
				leafFD,
				leaf,
				managedVMCgroupCPUMax{quotaMicros: 50000, periodMicros: 100000},
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

		err := writeManagedVMCgroupCPUMax(
			t.Context(),
			deps,
			leafFD,
			leaf,
			managedVMCgroupCPUMax{quotaMicros: 50000, periodMicros: 100000},
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

		err := writeManagedVMCgroupCPUMax(
			ctx,
			deps,
			leafFD,
			leaf,
			managedVMCgroupCPUMax{quotaMicros: 50000, periodMicros: 100000},
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
				_, err := readManagedVMCgroupMemoryMax(ctx, deps, leafFD, leaf)
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
				return writeManagedVMCgroupCPUMax(
					ctx,
					deps,
					leafFD,
					leaf,
					managedVMCgroupCPUMax{quotaMicros: 50000, periodMicros: 100000},
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

func TestReadManagedVMCgroupControlReturnsSuccessfulOperationCloseError(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	fixture.addVMLeaf(testVMID)
	createManagedVMCgroupControlTestFile(t, filepath.Join(fixture.vmLeafPath(testVMID), "memory.max"))
	deps, leafFD, leaf := openManagedVMCgroupControlTestLeaf(t, fixture)
	closeErr := errors.New("injected successful control close failure")
	realClose := deps.close
	deps.close = func(ctx context.Context, fd int) error {
		return errors.Join(realClose(ctx, fd), closeErr)
	}

	_, err := readManagedVMCgroupMemoryMax(t.Context(), deps, leafFD, leaf)
	require.ErrorIs(t, err, closeErr)
	requireManagedVMCgroupAtomicError(t, err)
}

// Rationale: a cgroup pseudo-file write is incomplete until its descriptor closes successfully. The write cause must
// remain primary when both the write and close fail, while a close failure after a full write must still be reported.
func TestWriteManagedVMCgroupControlPreservesWriteAndCloseErrors(t *testing.T) {
	t.Parallel()

	writeErr := errors.New("injected subtree-control write failure")
	closeErr := errors.New("injected subtree-control close failure")
	tests := map[string]struct {
		writeErr error
	}{
		"successful write and failed close": {},
		"failed write and failed close":     {writeErr: writeErr},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newManagedVMCgroupTestFixture(t)
			fixture.addVMLeaf(testVMID)
			path := filepath.Join(fixture.vmLeafPath(testVMID), "cgroup.subtree_control")
			require.NoError(t, os.WriteFile(path, nil, 0o644))
			deps, leafFD, leaf := openManagedVMCgroupControlTestLeaf(t, fixture)
			realWrite := deps.write
			writeCalls := 0
			deps.write = func(ctx context.Context, fd int, value []byte) (int, error) {
				writeCalls++
				if tc.writeErr != nil {
					return 0, tc.writeErr
				}
				return realWrite(ctx, fd, value)
			}
			realClose := deps.close
			deps.close = func(ctx context.Context, fd int) error {
				return errors.Join(realClose(ctx, fd), closeErr)
			}

			err := addManagedVMCgroupSubtreeControllers(
				t.Context(), deps, leafFD, leaf, managedVMCgroupControllerCPU,
			)
			if tc.writeErr != nil {
				require.ErrorIs(t, err, tc.writeErr)
			}
			require.ErrorIs(t, err, closeErr)
			requireManagedVMCgroupAtomicError(t, err)
			assert.Equal(t, 1, writeCalls)
			if tc.writeErr == nil {
				written, readErr := os.ReadFile(path)
				require.NoError(t, readErr)
				if diff := cmp.Diff([]byte("+cpu\n"), written); diff != "" {
					t.Errorf("subtree-control write mismatch (-want +got):\n%s", diff)
				}
			}
		})
	}
}

func TestWriteManagedVMCgroupControlBytesEnforcesExactCap(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		size    int
		wantErr bool
	}{
		"exact cap":      {size: managedVMCgroupControlMaxBytes},
		"one beyond cap": {size: managedVMCgroupControlMaxBytes + 1, wantErr: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newManagedVMCgroupTestFixture(t)
			fixture.addVMLeaf(testVMID)
			createManagedVMCgroupControlTestFile(t, filepath.Join(fixture.vmLeafPath(testVMID), "cpu.max"))
			deps, leafFD, leaf := openManagedVMCgroupControlTestLeaf(t, fixture)
			writeCalls := 0
			deps.write = func(_ context.Context, _ int, value []byte) (int, error) {
				writeCalls++
				return len(value), nil
			}

			err := writeManagedVMCgroupControlBytes(
				t.Context(),
				deps,
				leafFD,
				leaf,
				managedVMCgroupCPUMaxControl,
				make([]byte, tc.size),
			)
			if tc.wantErr {
				requireManagedVMCgroupAtomicError(t, err)
				assert.Zero(t, writeCalls)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, 1, writeCalls)
		})
	}
}

func TestManagedVMCgroupNamedControlOperationsRejectAlreadyCancelledContextBeforeIO(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	fixture.addVMLeaf(testVMID)
	deps, leafFD, leaf := openManagedVMCgroupControlTestLeaf(t, fixture)
	openCalls := 0
	preadCalls := 0
	writeCalls := 0
	deps.openAt2 = func(context.Context, int, string, *unix.OpenHow) (int, error) { openCalls++; return -1, nil }
	deps.pread = func(context.Context, int, []byte, int64) (int, error) { preadCalls++; return 0, nil }
	deps.write = func(context.Context, int, []byte) (int, error) { writeCalls++; return 0, nil }
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, readErr := readManagedVMCgroupMemoryMax(ctx, deps, leafFD, leaf)
	require.ErrorIs(t, readErr, context.Canceled)
	writeErr := writeManagedVMCgroupCPUMax(
		ctx,
		deps,
		leafFD,
		leaf,
		managedVMCgroupCPUMax{quotaMicros: 50000, periodMicros: 100000},
	)
	require.ErrorIs(t, writeErr, context.Canceled)
	assert.Zero(t, openCalls)
	assert.Zero(t, preadCalls)
	assert.Zero(t, writeCalls)
}

func TestPrepareManagedVMCgroupForLaunchEnsuresRootThenParentControllers(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	parentPath := createManagedVMCgroupControllerHierarchy(
		t,
		fixture,
		"io cpu memory pids\n",
		"cpu\n",
		"pids memory cpu rdma\n",
		"memory\n",
	)
	deps := fixture.deps()
	var writes []managedVMCgroupControllerWriteCall
	deps.write = simulatedManagedVMCgroupControllerWrite(t, fixture.root, parentPath, &writes)

	authority, _ := newTestInstanceAuthority(t)
	authority.cgroupDeps = deps
	launch, err := registerTestLaunch(
		t,
		t.Context(),
		authority,
		testInstanceCaller(),
		testLaunchIntent(false),
	)
	require.NoError(t, err)
	err = launch.prepareManagedVMCgroup(t.Context(), testManagedVMCgroupLimits())
	require.NoError(t, err)
	wantWrites := []managedVMCgroupControllerWriteCall{
		{directory: "root", value: "+memory +pids\n"},
		{directory: "parent", value: "+cpu +pids\n"},
	}
	if diff := cmp.Diff(wantWrites, writes, cmp.AllowUnexported(managedVMCgroupControllerWriteCall{})); diff != "" {
		t.Errorf("controller enablement mismatch (-want +got):\n%s", diff)
	}
	cleanup, err := launch.Abort(t.Context())
	require.NoError(t, err)
	require.NoError(t, cleanup.Complete(t.Context()))
	require.NoError(t, cleanup.Release(t.Context()))

	prepared, err := prepareManagedVMCgroupForLaunch(t.Context(), deps, testVMID, testManagedVMCgroupLimits())
	require.NoError(t, err)
	require.NoError(t, prepared.Release(t.Context()))
	if diff := cmp.Diff(wantWrites, writes, cmp.AllowUnexported(managedVMCgroupControllerWriteCall{})); diff != "" {
		t.Errorf("retry repeated additive controller writes (-want +got):\n%s", diff)
	}
}

func TestPrepareManagedVMCgroupForLaunchFailsClosedOnControllerAvailabilityAndReadback(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		rootControllers string
		rootSubtree     string
		write           func(context.Context, int, []byte) (int, error)
		wantWrites      int
	}{
		"required controller missing": {
			rootControllers: "cpu memory\n",
			rootSubtree:     "cpu memory\n",
		},
		"enablement absent from readback": {
			rootControllers: "cpu memory pids\n",
			rootSubtree:     "",
			write: func(_ context.Context, _ int, value []byte) (int, error) {
				return len(value), nil
			},
			wantWrites: 1,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newManagedVMCgroupTestFixture(t)
			createManagedVMCgroupControllerHierarchy(
				t,
				fixture,
				tc.rootControllers,
				tc.rootSubtree,
				"cpu memory pids\n",
				"cpu memory pids\n",
			)
			deps := fixture.deps()
			writeCalls := 0
			deps.write = func(ctx context.Context, fd int, value []byte) (int, error) {
				writeCalls++
				if tc.write == nil {
					t.Fatal("unexpected controller write")
				}
				return tc.write(ctx, fd, value)
			}

			prepared, err := prepareManagedVMCgroupForLaunch(
				t.Context(), deps, testVMID, testManagedVMCgroupLimits(),
			)
			assert.Nil(t, prepared)
			requireManagedVMCgroupAtomicError(t, err)
			assert.Equal(t, tc.wantWrites, writeCalls)
		})
	}
}

// Rationale: successful additive mutation is not readiness evidence. The parent subtree-control must be read back
// through a fresh descriptor, and an I/O failure there must stop launch before the leaf-absence capability is returned.
func TestPrepareManagedVMCgroupForLaunchRejectsParentControllerReadbackFailureAfterWrite(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	parentPath := createManagedVMCgroupControllerHierarchy(
		t,
		fixture,
		"cpu memory pids\n",
		"cpu memory pids\n",
		"cpu memory pids\n",
		"cpu memory\n",
	)
	deps := fixture.deps()
	readbackErr := errors.New("injected parent subtree-control readback failure")
	parentSubtreeOpens := 0
	readbackFD := -1
	realOpenAt2 := deps.openAt2
	deps.openAt2 = func(ctx context.Context, fd int, name string, how *unix.OpenHow) (int, error) {
		opened, err := realOpenAt2(ctx, fd, name, how)
		if err != nil || name != "cgroup.subtree_control" {
			return opened, err
		}
		parent, readlinkErr := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
		require.NoError(t, readlinkErr)
		if parent == parentPath {
			parentSubtreeOpens++
			if parentSubtreeOpens == 2 {
				readbackFD = opened
			}
		}
		return opened, nil
	}
	realPread := deps.pread
	deps.pread = func(ctx context.Context, fd int, buffer []byte, offset int64) (int, error) {
		if fd == readbackFD {
			return 0, readbackErr
		}
		return realPread(ctx, fd, buffer, offset)
	}
	var writes []managedVMCgroupControllerWriteCall
	deps.write = simulatedManagedVMCgroupControllerWrite(t, fixture.root, parentPath, &writes)

	prepared, err := prepareManagedVMCgroupForLaunch(
		t.Context(), deps, testVMID, testManagedVMCgroupLimits(),
	)
	assert.Nil(t, prepared)
	require.ErrorIs(t, err, readbackErr)
	requireManagedVMCgroupAtomicError(t, err)
	wantWrites := []managedVMCgroupControllerWriteCall{{directory: "parent", value: "+pids\n"}}
	if diff := cmp.Diff(wantWrites, writes, cmp.AllowUnexported(managedVMCgroupControllerWriteCall{})); diff != "" {
		t.Errorf("controller writes mismatch (-want +got):\n%s", diff)
	}
}

// Rationale: controller enablement is additive shared state. If root convergence succeeds before parent readiness
// fails, retry must retain that root result and converge the parent without replaying the root write.
func TestPrepareManagedVMCgroupForLaunchRetryDoesNotRepeatConvergedRootWrite(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	parentPath := createManagedVMCgroupControllerHierarchy(
		t,
		fixture,
		"cpu memory pids\n",
		"cpu\n",
		"cpu memory\n",
		"cpu memory\n",
	)
	deps := fixture.deps()
	var writes []managedVMCgroupControllerWriteCall
	deps.write = simulatedManagedVMCgroupControllerWrite(t, fixture.root, parentPath, &writes)

	prepared, err := prepareManagedVMCgroupForLaunch(
		t.Context(), deps, testVMID, testManagedVMCgroupLimits(),
	)
	assert.Nil(t, prepared)
	requireManagedVMCgroupAtomicError(t, err)
	parentControllersPath := filepath.Join(parentPath, "cgroup.controllers")
	require.NoError(t, os.Chmod(parentControllersPath, 0o644))
	require.NoError(t, os.WriteFile(
		parentControllersPath,
		[]byte("cpu memory pids\n"),
		0o444,
	))
	require.NoError(t, os.Chmod(parentControllersPath, 0o444))

	prepared, err = prepareManagedVMCgroupForLaunch(
		t.Context(), deps, testVMID, testManagedVMCgroupLimits(),
	)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	require.NoError(t, prepared.Release(t.Context()))
	wantWrites := []managedVMCgroupControllerWriteCall{
		{directory: "root", value: "+memory +pids\n"},
		{directory: "parent", value: "+pids\n"},
	}
	if diff := cmp.Diff(wantWrites, writes, cmp.AllowUnexported(managedVMCgroupControllerWriteCall{})); diff != "" {
		t.Errorf("controller retry writes mismatch (-want +got):\n%s", diff)
	}
}

// Rationale: subtree controller writes are shared additive effects. A later parent failure must not attempt to
// compensate by disabling controllers that this launch just made ready at the root.
func TestPrepareManagedVMCgroupForLaunchDoesNotRollbackRootControllerEnablement(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	parentPath := createManagedVMCgroupControllerHierarchy(
		t,
		fixture,
		"cpu memory pids\n",
		"cpu\n",
		"cpu memory\n",
		"cpu memory\n",
	)
	deps := fixture.deps()
	var writes []managedVMCgroupControllerWriteCall
	deps.write = simulatedManagedVMCgroupControllerWrite(t, fixture.root, parentPath, &writes)

	prepared, err := prepareManagedVMCgroupForLaunch(t.Context(), deps, testVMID, testManagedVMCgroupLimits())
	assert.Nil(t, prepared)
	requireManagedVMCgroupAtomicError(t, err)
	wantWrites := []managedVMCgroupControllerWriteCall{{directory: "root", value: "+memory +pids\n"}}
	if diff := cmp.Diff(wantWrites, writes, cmp.AllowUnexported(managedVMCgroupControllerWriteCall{})); diff != "" {
		t.Errorf("controller writes mismatch (-want +got):\n%s", diff)
	}
	rootSubtree, readErr := os.ReadFile(filepath.Join(fixture.root, "cgroup.subtree_control"))
	require.NoError(t, readErr)
	if diff := cmp.Diff([]byte("cpu memory pids\n"), rootSubtree); diff != "" {
		t.Errorf("root enablement was rolled back (-want +got):\n%s", diff)
	}
}

// Rationale: a controller failure after the parent was created may leave that shared additive object in place, but
// every locally retained descriptor must still close in reverse order using cancellation-detached context.
func TestPrepareManagedVMCgroupForLaunchClosesCreatedParentAfterCancelledControllerRead(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	createManagedVMCgroupControllerFiles(t, fixture.root, "cpu memory pids\n", "cpu memory pids\n")
	parentPath := filepath.Join(fixture.root, "mvmctl")
	deps := fixture.deps()
	realMkdirAt := deps.mkdirAt
	deps.mkdirAt = func(ctx context.Context, rootFD int, name string, mode uint32) error {
		if err := realMkdirAt(ctx, rootFD, name, mode); err != nil {
			return err
		}
		createManagedVMCgroupControllerFiles(t, parentPath, "cpu memory pids\n", "cpu memory pids\n")
		return nil
	}
	rootFD := -1
	realOpenRoot := deps.openRoot
	deps.openRoot = func(ctx context.Context) (int, error) {
		fd, err := realOpenRoot(ctx)
		if err == nil {
			rootFD = fd
		}
		return fd, err
	}
	parentFD := -1
	realOpenAt2 := deps.openAt2
	deps.openAt2 = func(ctx context.Context, fd int, name string, how *unix.OpenHow) (int, error) {
		opened, err := realOpenAt2(ctx, fd, name, how)
		if err == nil && name == "mvmctl" {
			parentFD = opened
		}
		return opened, err
	}
	ctx, cancel := context.WithCancel(context.WithValue(
		t.Context(), managedVMCgroupControlContextKey{}, "retained",
	))
	realPread := deps.pread
	deps.pread = func(readCtx context.Context, fd int, buffer []byte, offset int64) (int, error) {
		target, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
		require.NoError(t, err)
		count, readErr := realPread(readCtx, fd, buffer, offset)
		if target == filepath.Join(parentPath, "cgroup.controllers") {
			cancel()
		}
		return count, readErr
	}
	var retainedCloseOrder []string
	realClose := deps.close
	deps.close = func(closeCtx context.Context, fd int) error {
		assert.NoError(t, closeCtx.Err())
		assert.Equal(t, "retained", closeCtx.Value(managedVMCgroupControlContextKey{}))
		switch fd {
		case parentFD:
			retainedCloseOrder = append(retainedCloseOrder, "parent")
		case rootFD:
			retainedCloseOrder = append(retainedCloseOrder, "root")
		}
		return realClose(closeCtx, fd)
	}

	prepared, err := prepareManagedVMCgroupForLaunch(ctx, deps, testVMID, testManagedVMCgroupLimits())
	assert.Nil(t, prepared)
	require.ErrorIs(t, err, context.Canceled)
	if diff := cmp.Diff([]string{"parent", "root"}, retainedCloseOrder); diff != "" {
		t.Errorf("retained descriptor close order mismatch (-want +got):\n%s", diff)
	}
	info, statErr := os.Stat(parentPath)
	require.NoError(t, statErr)
	assert.True(t, info.IsDir())
}

func TestPrepareFreshRecoveryManagedVMCgroupDoesNotTouchControllerControls(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	deps := fixture.deps()
	var opened []string
	realOpenAt2 := deps.openAt2
	deps.openAt2 = func(ctx context.Context, fd int, name string, how *unix.OpenHow) (int, error) {
		opened = append(opened, name)
		return realOpenAt2(ctx, fd, name, how)
	}
	writeCalls := 0
	deps.write = func(context.Context, int, []byte) (int, error) {
		writeCalls++
		return 0, errors.New("unexpected controller write")
	}

	prepared, err := prepareFreshRecoveryManagedVMCgroup(t.Context(), deps, testVMID)
	require.NoError(t, err)
	require.NoError(t, prepared.Release(t.Context()))
	wantOpened := []string{"mvmctl", "mvmctl", testVMID}
	if diff := cmp.Diff(wantOpened, opened); diff != "" {
		t.Errorf("fresh recovery opens mismatch (-want +got):\n%s", diff)
	}
	assert.Zero(t, writeCalls)
}

type managedVMCgroupControlContextKey struct{}

type managedVMCgroupControllerWriteCall struct {
	directory string
	value     string
}

func createManagedVMCgroupControllerHierarchy(
	t *testing.T,
	fixture *managedVMCgroupTestFixture,
	rootControllers string,
	rootSubtree string,
	parentControllers string,
	parentSubtree string,
) string {
	t.Helper()
	createManagedVMCgroupControllerFiles(t, fixture.root, rootControllers, rootSubtree)
	parentPath := filepath.Join(fixture.root, "mvmctl")
	require.NoError(t, os.Mkdir(parentPath, os.FileMode(managedVMCgroupParentMode)))
	require.NoError(t, os.Chmod(parentPath, os.FileMode(managedVMCgroupParentMode)))
	createManagedVMCgroupControllerFiles(t, parentPath, parentControllers, parentSubtree)
	return parentPath
}

func createManagedVMCgroupControllerFiles(t *testing.T, directory, controllers, subtree string) {
	t.Helper()
	controllersPath := filepath.Join(directory, "cgroup.controllers")
	require.NoError(t, os.WriteFile(controllersPath, []byte(controllers), 0o444))
	require.NoError(t, os.Chmod(controllersPath, 0o444))
	subtreePath := filepath.Join(directory, "cgroup.subtree_control")
	require.NoError(t, os.WriteFile(subtreePath, []byte(subtree), 0o644))
	require.NoError(t, os.Chmod(subtreePath, 0o644))
}

func simulatedManagedVMCgroupControllerWrite(
	t *testing.T,
	rootPath string,
	parentPath string,
	writes *[]managedVMCgroupControllerWriteCall,
) func(context.Context, int, []byte) (int, error) {
	t.Helper()
	return func(_ context.Context, fd int, value []byte) (int, error) {
		target, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
		require.NoError(t, err)
		directory := filepath.Dir(target)
		label := ""
		switch directory {
		case rootPath:
			label = "root"
		case parentPath:
			label = "parent"
		default:
			t.Fatalf("write targeted unexpected controller directory %q", directory)
		}
		*writes = append(*writes, managedVMCgroupControllerWriteCall{directory: label, value: string(value)})
		require.NoError(t, os.WriteFile(target, []byte("cpu memory pids\n"), 0o644))
		return len(value), nil
	}
}

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
