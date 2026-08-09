package jailer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
)

const (
	testVMID       = "0123456789abcdef0123456789abcdef"
	testResourceID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func TestLoadManifest_AcceptsExactManagedResources(t *testing.T) {
	fixture := newManagedFixture(t)
	got, err := loadManifest(testVMID, fixture.vmDir)
	require.NoError(t, err)
	require.NotNil(t, got)
	if diff := cmp.Diff(&fixture.manifest, got); diff != "" {
		t.Errorf("validated manifest mismatch (-want +got):\n%s", diff)
	}
}

// Rationale: The privileged service must not treat an attacker-controlled path
// as managed merely because its immediate parent has a managed collection name.
func TestLoadManifest_RejectsArbitraryCollectionPaths(t *testing.T) {
	tests := map[string]struct {
		mutate  func(*testing.T, *LaunchManifest)
		wantErr string
	}{
		"kernel_from_arbitrary_collection": {
			mutate: func(t *testing.T, manifest *LaunchManifest) {
				path := filepath.Join(t.TempDir(), "kernels", "vmlinux")
				require.NoError(t, os.Mkdir(filepath.Dir(path), 0700))
				require.NoError(t, os.WriteFile(path, []byte("kernel"), 0600))
				manifest.KernelPath = path
			},
			wantErr: "invalid managed kernel path",
		},
		"snapshot_from_arbitrary_collection": {
			mutate: func(t *testing.T, manifest *LaunchManifest) {
				path := filepath.Join(t.TempDir(), "snapshots", testResourceID)
				require.NoError(t, os.MkdirAll(path, 0700))
				manifest.SnapshotDir = path
			},
			wantErr: "outside the managed snapshots root",
		},
		"volume_from_arbitrary_collection": {
			mutate: func(t *testing.T, manifest *LaunchManifest) {
				path := filepath.Join(t.TempDir(), "volumes", testResourceID)
				require.NoError(t, os.Mkdir(filepath.Dir(path), 0700))
				require.NoError(t, os.WriteFile(path, []byte("volume"), 0600))
				manifest.Volumes = []VolumeMount{{DriveID: testResourceID, HostPath: path}}
			},
			wantErr: "invalid managed volume path",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newManagedFixture(t)
			tc.mutate(t, &fixture.manifest)
			fixture.writeManifest(t)

			got, err := loadManifest(testVMID, fixture.vmDir)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.Nil(t, got)
		})
	}
}

// Rationale: Symlink substitution and group/other-writable directories would
// let the invoking user swap privileged mount sources after validation.
func TestManagedPathValidationRejectsUnsafeFilesystemState(t *testing.T) {
	uid := uint32(os.Getuid())
	tests := map[string]func(*testing.T) error{
		"symlink_file": func(t *testing.T) error {
			root := t.TempDir()
			target := filepath.Join(root, "target")
			require.NoError(t, os.WriteFile(target, []byte("target"), 0600))
			link := filepath.Join(root, "link")
			require.NoError(t, os.Symlink(target, link))
			return validateManagedFile(link, root, uid, true)
		},
		"other_writable_root": func(t *testing.T) error {
			root := t.TempDir()
			require.NoError(t, os.Chmod(root, 0777))
			return validateManagedDirectory(root, uid)
		},
	}
	for name, validate := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Error(t, validate(t))
		})
	}
}

// Rationale: Identity values cross a sudo boundary; root, missing, negative,
// and non-numeric identities must never become Jailer --uid/--gid arguments.
func TestInvokingIdentityRejectsMalformedValues(t *testing.T) {
	tests := map[string]struct {
		uid string
		gid string
	}{
		"missing":     {},
		"root_uid":    {uid: "0", gid: "1000"},
		"root_gid":    {uid: "1000", gid: "0"},
		"negative":    {uid: "-1", gid: "1000"},
		"non_numeric": {uid: "user", gid: "group"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Setenv("SUDO_UID", tc.uid)
			t.Setenv("SUDO_GID", tc.gid)
			uid, gid, err := invokingIdentity()
			assert.Error(t, err)
			assert.Zero(t, uid)
			assert.Zero(t, gid)
		})
	}
}

// Rationale: Privileged cleanup and volume operations must reject identifiers
// that could alter their fixed mount targets before issuing any syscall.
func TestPrivilegedTargetValidationRejectsArbitraryIdentifiers(t *testing.T) {
	tests := map[string]func() error{
		"cleanup_path_traversal": func() error { return cleanup("../../etc") },
		"remove_volume_bad_vm":   func() error { return removeVolume("../../etc", testResourceID) },
		"remove_volume_bad_drive": func() error {
			return removeVolume(testVMID, "../../rootfs")
		},
	}
	for name, operation := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Error(t, operation())
		})
	}
}

// Rationale: The wire entry point must reject every action when invoked without
// its required root boundary, including unknown actions and path-bearing actions.
func TestRunRejectsUnprivilegedWireOperations(t *testing.T) {
	tests := map[string]Config{
		"unknown_action":        {Action: "exec-arbitrary"},
		"launch_arbitrary_path": {Action: "launch", VMID: testVMID, VMDir: "/tmp/attacker"},
		"expose_arbitrary_path": {
			Action: "expose-volume", VMID: testVMID, VMDir: "/tmp/attacker", DriveID: testResourceID,
			HostPath: "/etc/shadow",
		},
	}
	for name, cfg := range tests {
		t.Run(name, func(t *testing.T) {
			err := Run(context.Background(), cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "requires root")
		})
	}
}

func TestJailRootIsDeterministic(t *testing.T) {
	want := filepath.Join(infra.JailerChrootBase, "firecracker", testVMID, "root")
	first := jailRoot(testVMID)
	second := jailRoot(testVMID)
	assert.Equal(t, want, first)
	assert.Equal(t, first, second)
}

func TestValidateCgroupLimitsRejectsAbsentAndInvalidEnvelopes(t *testing.T) {
	valid := model.NewVMCgroupLimits(2, 512, model.VMCgroupPolicy{
		VMMHeadroomMiB: 128, CPUWeight: 100, PIDsMax: 256, SwapMaxBytes: 0,
	})
	tests := map[string]struct {
		mutate func(*model.VMCgroupLimits)
		valid  bool
	}{
		"valid":                {valid: true},
		"absent":               {mutate: func(l *model.VMCgroupLimits) { *l = model.VMCgroupLimits{} }},
		"zero_cpu_quota":       {mutate: func(l *model.VMCgroupLimits) { l.CPUQuotaMicros = 0 }},
		"zero_cpu_period":      {mutate: func(l *model.VMCgroupLimits) { l.CPUPeriodMicros = 0 }},
		"weight_below_minimum": {mutate: func(l *model.VMCgroupLimits) { l.CPUWeight = 0 }},
		"weight_above_maximum": {mutate: func(l *model.VMCgroupLimits) { l.CPUWeight = 10001 }},
		"zero_memory_high":     {mutate: func(l *model.VMCgroupLimits) { l.MemoryHighBytes = 0 }},
		"high_above_max":       {mutate: func(l *model.VMCgroupLimits) { l.MemoryHighBytes = l.MemoryMaxBytes + 1 }},
		"negative_swap":        {mutate: func(l *model.VMCgroupLimits) { l.SwapMaxBytes = -1 }},
		"zero_pid_cap":         {mutate: func(l *model.VMCgroupLimits) { l.PIDsMax = 0 }},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			limits := valid
			if tc.mutate != nil {
				tc.mutate(&limits)
			}
			err := validateCgroupLimits(limits)
			if tc.valid {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
		})
	}
}

func TestEnsureCgroupEmptyRefusesPopulatedAndAcceptsAbsentState(t *testing.T) {
	path := t.TempDir()
	base := filepath.Join(infra.CgroupV2Root, infra.JailerCgroupParent, infra.JailerCgroupExecutable)
	vmID, err := filepath.Rel(base, path)
	require.NoError(t, err)
	assert.Equal(t, filepath.Clean(path), cgroupPath(vmID))

	require.NoError(t, os.WriteFile(filepath.Join(path, "cgroup.procs"), []byte("123\n"), 0600))
	err = ensureCgroupEmpty(vmID)
	require.ErrorContains(t, err, "contains live processes")

	require.NoError(t, os.Remove(filepath.Join(path, "cgroup.procs")))
	require.NoError(t, ensureCgroupEmpty(vmID))
}

// Rationale: User-owned or symlinked executables must never satisfy the
// root-owned, non-user-writable trusted executable contract.
func TestValidateTrustedExecutableRejectsUntrustedPaths(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "firecracker")
	require.NoError(t, os.WriteFile(executable, []byte("binary"), 0755))
	assert.Error(t, validateTrustedExecutable(executable))
	link := filepath.Join(root, "jailer")
	require.NoError(t, os.Symlink(executable, link))
	assert.Error(t, validateTrustedExecutable(link))
}

// Rationale: Every trusted path component is traversed by a root process, so
// symlinked, user-owned, or group/other-writable directories must fail closed.
func TestValidateTrustedDirectoryRejectsUnsafeComponents(t *testing.T) {
	tests := map[string]func(*testing.T) string{
		"symlink": func(t *testing.T) string {
			target := t.TempDir()
			link := filepath.Join(t.TempDir(), "trusted-link")
			require.NoError(t, os.Symlink(target, link))
			return link
		},
		"user_owned": func(t *testing.T) string {
			return t.TempDir()
		},
		"other_writable": func(t *testing.T) string {
			path := t.TempDir()
			require.NoError(t, os.Chmod(path, 0777))
			return path
		},
	}
	for name, setup := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Error(t, validateTrustedDirectory(setup(t)))
		})
	}
}

type managedFixture struct {
	vmDir    string
	manifest LaunchManifest
}

// newManagedFixture creates the exact per-user cache layout accepted by the
// privileged manifest validator without requiring root, mounts, or subprocesses.
func newManagedFixture(t *testing.T) *managedFixture {
	t.Helper()
	uid := os.Getuid()
	gid := os.Getgid()
	require.Positive(t, uid)
	require.Positive(t, gid)
	t.Setenv("SUDO_UID", strconv.Itoa(uid))
	t.Setenv("SUDO_GID", strconv.Itoa(gid))
	cacheRoot := t.TempDir()
	vmsRoot := filepath.Join(cacheRoot, "vms")
	vmDir := filepath.Join(vmsRoot, testVMID)
	kernelsRoot := filepath.Join(cacheRoot, "kernels")
	volumesRoot := filepath.Join(cacheRoot, "volumes")
	snapshotsRoot := filepath.Join(cacheRoot, "snapshots")
	for _, path := range []string{vmsRoot, vmDir, kernelsRoot, volumesRoot, snapshotsRoot} {
		require.NoError(t, os.Mkdir(path, 0700))
	}
	snapshotDir := filepath.Join(snapshotsRoot, testResourceID)
	require.NoError(t, os.Mkdir(snapshotDir, 0700))
	files := map[string]string{
		filepath.Join(vmDir, "config.json"):      "{}",
		filepath.Join(vmDir, "rootfs.ext4"):      "rootfs",
		filepath.Join(vmDir, "cloud-init.iso"):   "iso",
		filepath.Join(kernelsRoot, "vmlinux"):    "kernel",
		filepath.Join(volumesRoot, "volume.raw"): "volume",
	}
	for path, content := range files {
		require.NoError(t, os.WriteFile(path, []byte(content), 0600))
	}
	fixture := &managedFixture{vmDir: vmDir}
	fixture.manifest = LaunchManifest{
		VMID: testVMID, VMDir: vmDir, Version: "1.16.0",
		ConfigPath: filepath.Join(vmDir, "config.json"), PIDPath: filepath.Join(vmDir, "firecracker.pid"),
		KernelPath: filepath.Join(kernelsRoot, "vmlinux"), RootfsPath: filepath.Join(vmDir, "rootfs.ext4"),
		ISOPath: filepath.Join(vmDir, "cloud-init.iso"), SnapshotDir: snapshotDir,
		Volumes:   []VolumeMount{{DriveID: testResourceID, HostPath: filepath.Join(volumesRoot, "volume.raw")}},
		APISocket: filepath.Join(vmDir, "firecracker.socket"),
		CgroupLimits: func() *model.VMCgroupLimits {
			limits := model.NewVMCgroupLimits(2, 512, model.VMCgroupPolicy{
				VMMHeadroomMiB: 128, CPUWeight: 100, PIDsMax: 256, SwapMaxBytes: 0,
			})
			return &limits
		}(),
	}
	fixture.writeManifest(t)
	return fixture
}

func (f *managedFixture) writeManifest(t *testing.T) {
	t.Helper()
	data, err := json.Marshal(f.manifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(f.vmDir, infra.JailerManifestFilename), data, 0600))
}
