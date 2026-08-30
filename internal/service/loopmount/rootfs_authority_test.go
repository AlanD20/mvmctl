package loopmount

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

const (
	testRootfsCallerUID     = 1001
	testRootfsCallerGID     = 1002
	testExpectedRootfsBytes = 128 << 20
)

func TestParseRootfsCacheLocatorRejectsUnsafeValues(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"empty":              "",
		"relative":           "srv/cache",
		"filesystem root":    "/",
		"NUL":                "/srv/cache\x00attacker",
		"dot component":      "/srv/./cache",
		"parent component":   "/srv/../cache",
		"empty component":    "/srv//cache",
		"trailing separator": "/srv/cache/",
		"name too long":      "/" + strings.Repeat("a", unix.NAME_MAX+1),
		"too deep":           "/" + strings.Repeat("a/", maxRootfsCacheLocatorDepth) + "cache",
		"path too long":      "/" + strings.Repeat("a/", unix.PathMax/2),
	}

	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			locator, err := parseRootfsCacheLocator(raw)

			require.Error(t, err)
			assert.Empty(t, locator.components)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, errs.CodeValidationFailed, domainErr.Code)
		})
	}
}

func TestParseRootfsCacheLocatorPreservesCanonicalComponents(t *testing.T) {
	t.Parallel()

	locator, err := parseRootfsCacheLocator("/srv/mvm/cache")
	require.NoError(t, err)

	want := []string{"srv", "mvm", "cache"}
	if diff := cmp.Diff(want, locator.components); diff != "" {
		t.Errorf("cache locator components mismatch (-want +got):\n%s", diff)
	}
}

func TestValidateRootfsCallerRequiresAuthenticatedNonRootIdentity(t *testing.T) {
	t.Parallel()

	tests := map[string]Caller{
		"zero UID": {UID: 0, GID: testRootfsCallerGID},
		"zero GID": {UID: testRootfsCallerUID, GID: 0},
	}
	for name, caller := range tests {
		t.Run(name, func(t *testing.T) {
			err := validateRootfsCaller(caller)

			require.Error(t, err)
			assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
		})
	}

	require.NoError(t, validateRootfsCaller(Caller{UID: testRootfsCallerUID, GID: testRootfsCallerGID}))
}

func TestRootfsAuthorityUsesFixedSelectionAndAccess(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		admit          func(context.Context, rootfsAuthorityDeps) (*rootfsLease, error)
		wantComponents []string
		wantLeafFlags  int
		wantAccess     rootfsAccess
		wantStagingFD  bool
	}{
		"VM provision is read-write": {
			admit: func(ctx context.Context, deps rootfsAuthorityDeps) (*rootfsLease, error) {
				return admitVMProvisionRootfs(ctx, deps, testRootfsCaller(), testVMProvisionRequest())
			},
			wantComponents: []string{"srv", "cache", "vms", testVMID, "rootfs.img"},
			wantLeafFlags:  rootfsReadWriteOpenFlags,
			wantAccess:     rootfsReadWrite,
		},
		"image inspect is read-only": {
			admit: func(ctx context.Context, deps rootfsAuthorityDeps) (*rootfsLease, error) {
				return admitImageInspectRootfs(ctx, deps, testRootfsCaller(), testImageInspectRequest())
			},
			wantComponents: []string{"srv", "cache", "images", "staging", testImageID, "rootfs.img"},
			wantLeafFlags:  rootfsReadOnlyOpenFlags,
			wantAccess:     rootfsReadOnly,
			wantStagingFD:  true,
		},
		"image optimize is read-write": {
			admit: func(ctx context.Context, deps rootfsAuthorityDeps) (*rootfsLease, error) {
				return admitImageOptimizeRootfs(ctx, deps, testRootfsCaller(), testImageOptimizeRequest())
			},
			wantComponents: []string{"srv", "cache", "images", "staging", testImageID, "rootfs.img"},
			wantLeafFlags:  rootfsReadWriteOpenFlags,
			wantAccess:     rootfsReadWrite,
			wantStagingFD:  true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newRootfsAuthorityFixture(t, tc.wantComponents)
			lease, err := tc.admit(t.Context(), fixture.deps())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, lease.Release(context.Background())) })

			assert.Equal(t, rootfsRootOpenFlags, fixture.rootFlags)
			assert.Equal(t, fixture.rootFD+2, fixture.statfsFD)
			require.Len(t, fixture.openCalls, len(tc.wantComponents))
			wantParentFD := fixture.rootFD
			for index, call := range fixture.openCalls {
				assert.Equal(t, wantParentFD, call.parentFD)
				assert.Equal(t, tc.wantComponents[index], call.component)
				wantFlags := uint64(rootfsDirectoryOpenFlags)
				if index == len(fixture.openCalls)-1 {
					wantFlags = uint64(tc.wantLeafFlags)
				}
				assert.Equal(t, wantFlags, call.how.Flags)
				if index < 2 {
					assert.Equal(t, uint64(rootfsCacheResolveFlags), call.how.Resolve)
				} else {
					assert.Equal(t, uint64(rootfsDescendantResolveFlags), call.how.Resolve)
				}
				wantParentFD = call.fd
			}
			assert.Equal(t, tc.wantAccess, lease.access)
			assert.Equal(t, fixture.openCalls[len(fixture.openCalls)-1].fd, lease.rootfsFD)
			if tc.wantStagingFD {
				assert.Equal(t, fixture.openCalls[len(fixture.openCalls)-2].fd, lease.stagingFD)
			} else {
				assert.Equal(t, -1, lease.stagingFD)
			}
		})
	}
}

// Rationale: A grow may have completed before response delivery. Retry admission must accept the stable old or exact
// target size so the effect can converge, while rejecting every unrelated size as ambiguous authority.
func TestVMProvisionRootfsAdmissionAcceptsOnlyGrowReplaySizes(t *testing.T) {
	t.Parallel()

	const (
		targetBytes = 256 << 20
		thirdBytes  = 192 << 20
	)
	tests := map[string]struct {
		actualBytes uint64
		wantErr     bool
	}{
		"old size admits first attempt":     {actualBytes: testExpectedRootfsBytes},
		"target size admits replay":         {actualBytes: targetBytes},
		"unrelated third size fails closed": {actualBytes: thirdBytes, wantErr: true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			request := testVMProvisionRequest()
			request.Plan.ResizeBytes = targetBytes
			components := []string{"srv", "cache", "vms", request.VMID, "rootfs.img"}
			fixture := newRootfsAuthorityFixture(t, components)
			rootfsFD := fixture.rootFD + len(components)
			fixture.stats[rootfsFD] = []unix.Statx_t{
				rootfsFileStat(testRootfsCallerUID, 0600, tc.actualBytes, 99),
				rootfsFileStat(testRootfsCallerUID, 0600, tc.actualBytes, 99),
			}

			lease, err := admitVMProvisionRootfs(t.Context(), fixture.deps(), testRootfsCaller(), request)
			if tc.wantErr {
				require.Error(t, err)
				assert.Nil(t, lease)
				assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
				return
			}
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, lease.Release(context.Background())) })
			assert.Equal(t, tc.actualBytes, lease.admittedBytes)
		})
	}
}

func TestRootfsCacheAdmissionEnforcesOwnershipModesAndTopology(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		mutate   func(*rootfsAuthorityFixture)
		wantCode errs.Code
	}{
		"filesystem root must be root-owned": {
			mutate: func(fixture *rootfsAuthorityFixture) {
				fixture.stats[fixture.rootFD][0].Uid = testRootfsCallerUID
			},
			wantCode: errs.CodeValidationFailed,
		},
		"filesystem root cannot be group writable": {
			mutate: func(fixture *rootfsAuthorityFixture) {
				fixture.stats[fixture.rootFD][0].Mode |= 0020
			},
			wantCode: errs.CodeValidationFailed,
		},
		"ancestor cannot have foreign owner": {
			mutate:   func(fixture *rootfsAuthorityFixture) { fixture.stats[11][0].Uid = 7777 },
			wantCode: errs.CodeValidationFailed,
		},
		"ancestor cannot be world writable": {
			mutate:   func(fixture *rootfsAuthorityFixture) { fixture.stats[11][0].Mode |= 0002 },
			wantCode: errs.CodeValidationFailed,
		},
		"cache must be caller UID owned": {
			mutate:   func(fixture *rootfsAuthorityFixture) { fixture.stats[12][0].Uid = 7777 },
			wantCode: errs.CodeValidationFailed,
		},
		"caller GID cannot override UID ownership": {
			mutate: func(fixture *rootfsAuthorityFixture) {
				fixture.stats[12][0].Uid = 7777
				fixture.stats[12][0].Gid = testRootfsCallerGID
			},
			wantCode: errs.CodeValidationFailed,
		},
		"cache requires owner traversal": {
			mutate:   func(fixture *rootfsAuthorityFixture) { fixture.stats[12][0].Mode = unix.S_IFDIR | 0600 },
			wantCode: errs.CodeValidationFailed,
		},
		"cache cannot be group writable": {
			mutate:   func(fixture *rootfsAuthorityFixture) { fixture.stats[12][0].Mode |= 0020 },
			wantCode: errs.CodeValidationFailed,
		},
		"cache cannot be automount": {
			mutate: func(fixture *rootfsAuthorityFixture) {
				fixture.stats[12][0].Attributes_mask = unix.STATX_ATTR_AUTOMOUNT
				fixture.stats[12][0].Attributes = unix.STATX_ATTR_AUTOMOUNT
			},
			wantCode: errs.CodeValidationFailed,
		},
		"cache must be a directory": {
			mutate: func(fixture *rootfsAuthorityFixture) {
				fixture.stats[12][0].Mode = unix.S_IFREG | 0700
			},
			wantCode: errs.CodeValidationFailed,
		},
		"cache identity requires an inode": {
			mutate:   func(fixture *rootfsAuthorityFixture) { fixture.stats[12][0].Ino = 0 },
			wantCode: errs.CodeValidationFailed,
		},
		"advertised mount identity cannot be zero": {
			mutate:   func(fixture *rootfsAuthorityFixture) { fixture.stats[12][0].Mnt_id = 0 },
			wantCode: errs.CodeValidationFailed,
		},
		"stacked filesystem is rejected": {
			mutate:   func(fixture *rootfsAuthorityFixture) { fixture.filesystem = unix.OVERLAYFS_SUPER_MAGIC },
			wantCode: errs.CodeValidationFailed,
		},
		"filesystem inspection failure is atomic": {
			mutate:   func(fixture *rootfsAuthorityFixture) { fixture.statfsError = unix.EIO },
			wantCode: errs.CodeLoopMountError,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newRootfsAuthorityFixture(t, []string{"srv", "cache", "vms", testVMID, "rootfs.img"})
			tc.mutate(fixture)

			lease, err := admitVMProvisionRootfs(
				t.Context(),
				fixture.deps(),
				testRootfsCaller(),
				testVMProvisionRequest(),
			)

			require.Error(t, err)
			assert.Nil(t, lease)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, tc.wantCode, domainErr.Code)
		})
	}
}

func TestRootfsCacheFilesystemClosedSet(t *testing.T) {
	t.Parallel()

	allowed := []int64{
		unix.EXT4_SUPER_MAGIC,
		unix.XFS_SUPER_MAGIC,
		unix.BTRFS_SUPER_MAGIC,
		unix.F2FS_SUPER_MAGIC,
		unix.BCACHEFS_SUPER_MAGIC,
		unix.TMPFS_MAGIC,
		rootfsZFSSuperMagic,
	}
	for _, filesystem := range allowed {
		assert.True(t, isSupportedRootfsCacheFilesystem(filesystem), "filesystem magic %#x", filesystem)
	}
	assert.False(t, isSupportedRootfsCacheFilesystem(unix.OVERLAYFS_SUPER_MAGIC))
	assert.False(t, isSupportedRootfsCacheFilesystem(unix.NFS_SUPER_MAGIC))
}

func TestRootfsCacheRetainsTypedMountIdentity(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		mask      uint32
		mountID   uint64
		wantMount rootfsMountIdentity
	}{
		"unavailable": {
			mask:      rootfsStatxRequiredDirectory,
			wantMount: rootfsMountIdentity{kind: rootfsMountIdentityUnavailable},
		},
		"legacy": {
			mask:      rootfsStatxRequiredDirectory | unix.STATX_MNT_ID,
			mountID:   41,
			wantMount: rootfsMountIdentity{kind: rootfsMountIdentityLegacy, value: 41},
		},
		"unique": {
			mask:      rootfsStatxRequiredDirectory | unix.STATX_MNT_ID | unix.STATX_MNT_ID_UNIQUE,
			mountID:   73,
			wantMount: rootfsMountIdentity{kind: rootfsMountIdentityUnique, value: 73},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newRootfsAuthorityFixture(t, []string{"srv", "cache", "vms", testVMID, "rootfs.img"})
			fixture.stats[12][0].Mask = tc.mask
			fixture.stats[12][0].Mnt_id = tc.mountID

			lease, err := admitVMProvisionRootfs(
				t.Context(),
				fixture.deps(),
				testRootfsCaller(),
				testVMProvisionRequest(),
			)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, lease.Release(context.Background())) })

			if diff := cmp.Diff(
				tc.wantMount,
				lease.cacheIdentity.mount,
				cmp.AllowUnexported(rootfsMountIdentity{}),
			); diff != "" {
				t.Errorf("mount identity mismatch (-want +got):\n%s", diff)
			}
			assert.Equal(t, uint32(8), lease.cacheIdentity.deviceMajor)
			assert.Equal(t, uint32(1), lease.cacheIdentity.deviceMinor)
			assert.Equal(t, uint64(3), lease.cacheIdentity.inode)
		})
	}
}

func TestRootfsAuthorityRejectsUnsafeDescendantsAndLeaves(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*rootfsAuthorityFixture){
		"descendant symlink": func(fixture *rootfsAuthorityFixture) { fixture.openError[2] = unix.ELOOP },
		"descendant wrong type": func(fixture *rootfsAuthorityFixture) {
			fixture.stats[13][0].Mode = unix.S_IFREG | 0700
		},
		"descendant wrong owner": func(fixture *rootfsAuthorityFixture) { fixture.stats[13][0].Uid = 7777 },
		"descendant missing owner traversal": func(fixture *rootfsAuthorityFixture) {
			fixture.stats[13][0].Mode = unix.S_IFDIR | 0600
		},
		"descendant group writable": func(fixture *rootfsAuthorityFixture) { fixture.stats[13][0].Mode |= 0020 },
		"descendant special mode":   func(fixture *rootfsAuthorityFixture) { fixture.stats[13][0].Mode |= unix.S_ISVTX },
		"descendant automount": func(fixture *rootfsAuthorityFixture) {
			fixture.stats[13][0].Attributes_mask = unix.STATX_ATTR_AUTOMOUNT
			fixture.stats[13][0].Attributes = unix.STATX_ATTR_AUTOMOUNT
		},
		"leaf symlink": func(fixture *rootfsAuthorityFixture) { fixture.openError[4] = unix.ELOOP },
		"leaf wrong type": func(fixture *rootfsAuthorityFixture) {
			fixture.stats[15][0].Mode = unix.S_IFIFO | 0600
			fixture.stats[15][1].Mode = unix.S_IFIFO | 0600
		},
		"leaf wrong owner": func(fixture *rootfsAuthorityFixture) {
			fixture.stats[15][0].Uid = 7777
			fixture.stats[15][1].Uid = 7777
		},
		"leaf owner read missing": func(fixture *rootfsAuthorityFixture) {
			fixture.stats[15][0].Mode = unix.S_IFREG | 0200
			fixture.stats[15][1].Mode = unix.S_IFREG | 0200
		},
		"leaf owner write missing": func(fixture *rootfsAuthorityFixture) {
			fixture.stats[15][0].Mode = unix.S_IFREG | 0400
			fixture.stats[15][1].Mode = unix.S_IFREG | 0400
		},
		"leaf group writable": func(fixture *rootfsAuthorityFixture) {
			fixture.stats[15][0].Mode |= 0020
			fixture.stats[15][1].Mode |= 0020
		},
		"leaf executable": func(fixture *rootfsAuthorityFixture) {
			fixture.stats[15][0].Mode |= 0100
			fixture.stats[15][1].Mode |= 0100
		},
		"leaf special mode": func(fixture *rootfsAuthorityFixture) {
			fixture.stats[15][0].Mode |= unix.S_ISUID
			fixture.stats[15][1].Mode |= unix.S_ISUID
		},
		"leaf hardlink": func(fixture *rootfsAuthorityFixture) {
			fixture.stats[15][0].Nlink = 2
			fixture.stats[15][1].Nlink = 2
		},
		"leaf below hard minimum": func(fixture *rootfsAuthorityFixture) {
			fixture.stats[15][0].Size = minimumRootfsBytes - 1
			fixture.stats[15][1].Size = minimumRootfsBytes - 1
		},
		"leaf above hard maximum": func(fixture *rootfsAuthorityFixture) {
			fixture.stats[15][0].Size = maximumRootfsBytes + 1
			fixture.stats[15][1].Size = maximumRootfsBytes + 1
		},
		"leaf differs from expected size": func(fixture *rootfsAuthorityFixture) {
			fixture.stats[15][0].Size++
			fixture.stats[15][1].Size++
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newRootfsAuthorityFixture(t, []string{"srv", "cache", "vms", testVMID, "rootfs.img"})
			mutate(fixture)

			lease, err := admitVMProvisionRootfs(
				t.Context(),
				fixture.deps(),
				testRootfsCaller(),
				testVMProvisionRequest(),
			)

			require.Error(t, err)
			assert.Nil(t, lease)
			assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
		})
	}
}

func TestRootfsAuthorityRejectsEveryUnstableLeafIdentityField(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*unix.Statx_t){
		"device": func(stat *unix.Statx_t) { stat.Dev_minor++ },
		"inode":  func(stat *unix.Statx_t) { stat.Ino++ },
		"type":   func(stat *unix.Statx_t) { stat.Mode = unix.S_IFIFO | 0600 },
		"mode":   func(stat *unix.Statx_t) { stat.Mode = unix.S_IFREG | 0640 },
		"UID":    func(stat *unix.Statx_t) { stat.Uid++ },
		"GID":    func(stat *unix.Statx_t) { stat.Gid++ },
		"links":  func(stat *unix.Statx_t) { stat.Nlink++ },
		"size":   func(stat *unix.Statx_t) { stat.Size++ },
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newRootfsAuthorityFixture(t, []string{"srv", "cache", "vms", testVMID, "rootfs.img"})
			mutate(&fixture.stats[15][1])

			lease, err := admitVMProvisionRootfs(
				t.Context(),
				fixture.deps(),
				testRootfsCaller(),
				testVMProvisionRequest(),
			)

			require.Error(t, err)
			assert.Nil(t, lease)
			assert.Contains(t, err.Error(), "metadata changed")
		})
	}
}

func TestRootfsAuthorityAllowsGroupAndWorldReadButDoesNotUseGIDAsOwner(t *testing.T) {
	t.Parallel()

	fixture := newRootfsAuthorityFixture(t, []string{"srv", "cache", "vms", testVMID, "rootfs.img"})
	fixture.stats[15][0].Mode = unix.S_IFREG | 0644
	fixture.stats[15][1].Mode = unix.S_IFREG | 0644
	fixture.stats[15][0].Gid = 4242
	fixture.stats[15][1].Gid = 4242

	lease, err := admitVMProvisionRootfs(t.Context(), fixture.deps(), testRootfsCaller(), testVMProvisionRequest())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lease.Release(context.Background())) })

	assert.Equal(t, uint16(unix.S_IFREG|0644), lease.metadata.mode)
	assert.Equal(t, uint32(testRootfsCallerUID), lease.metadata.uid)
	assert.Equal(t, uint32(4242), lease.metadata.gid)
}

func TestRootfsAuthorityAcceptsExactHardSizeBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		components []string
		size       uint64
		admit      func(context.Context, rootfsAuthorityDeps, uint64) (*rootfsLease, error)
	}{
		{
			name:       "VM minimum",
			components: []string{"srv", "cache", "vms", testVMID, "rootfs.img"},
			size:       minimumRootfsBytes,
			admit: func(ctx context.Context, deps rootfsAuthorityDeps, size uint64) (*rootfsLease, error) {
				request := testVMProvisionRequest()
				request.ExpectedRootfsBytes = size
				return admitVMProvisionRootfs(ctx, deps, testRootfsCaller(), request)
			},
		},
		{
			name:       "VM maximum",
			components: []string{"srv", "cache", "vms", testVMID, "rootfs.img"},
			size:       maximumRootfsBytes,
			admit: func(ctx context.Context, deps rootfsAuthorityDeps, size uint64) (*rootfsLease, error) {
				request := testVMProvisionRequest()
				request.ExpectedRootfsBytes = size
				return admitVMProvisionRootfs(ctx, deps, testRootfsCaller(), request)
			},
		},
		{
			name:       "image minimum",
			components: []string{"srv", "cache", "images", "staging", testImageID, "rootfs.img"},
			size:       minimumImageBytes,
			admit: func(ctx context.Context, deps rootfsAuthorityDeps, size uint64) (*rootfsLease, error) {
				request := testImageInspectRequest()
				request.ExpectedImageBytes = size
				return admitImageInspectRootfs(ctx, deps, testRootfsCaller(), request)
			},
		},
		{
			name:       "image maximum",
			components: []string{"srv", "cache", "images", "staging", testImageID, "rootfs.img"},
			size:       maximumImageBytes,
			admit: func(ctx context.Context, deps rootfsAuthorityDeps, size uint64) (*rootfsLease, error) {
				request := testImageInspectRequest()
				request.ExpectedImageBytes = size
				return admitImageInspectRootfs(ctx, deps, testRootfsCaller(), request)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newRootfsAuthorityFixture(t, tc.components)
			leafFD := fixture.rootFD + len(tc.components)
			fixture.stats[leafFD][0].Size = tc.size
			fixture.stats[leafFD][1].Size = tc.size

			lease, err := tc.admit(t.Context(), fixture.deps(), tc.size)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, lease.Release(context.Background())) })
			assert.Equal(t, tc.size, lease.metadata.size)
		})
	}
}

// Rationale: Release runs after a privileged effect or partial admission. Cancellation must not retain any descriptor,
// and the reverse order is required so the exact leaf and its parents disappear from authority in dependency order.
func TestRootfsLeaseReleaseIsCheckedReverseIdempotentAndCancellationIndependent(t *testing.T) {
	fixture := newRootfsAuthorityFixture(t, []string{"srv", "cache", "vms", testVMID, "rootfs.img"})
	lease, err := admitVMProvisionRootfs(t.Context(), fixture.deps(), testRootfsCaller(), testVMProvisionRequest())
	require.NoError(t, err)

	fixture.closeError[15] = errors.New(strings.Repeat("leaf-close-failure", 80))
	fixture.closeError[10] = errors.New("root-close-failure")
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), rootfsCleanupContextKey{}, "release"))
	cancel()

	firstErr := lease.Release(ctx)
	require.Error(t, firstErr)
	secondErr := lease.Release(context.Background())
	assert.Same(t, firstErr, secondErr)

	wantFDs := []int{15, 14, 13, 12, 11, 10}
	gotFDs := make([]int, 0, len(fixture.closeCalls))
	for _, call := range fixture.closeCalls {
		gotFDs = append(gotFDs, call.fd)
		assert.NoError(t, call.contextErr)
		assert.Equal(t, "release", call.contextMark)
	}
	if diff := cmp.Diff(wantFDs, gotFDs); diff != "" {
		t.Errorf("descriptor close order mismatch (-want +got):\n%s", diff)
	}
	domainErr := errs.AsDomainError(firstErr)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeLoopMountError, domainErr.Code)
	detail, ok := domainErr.Details["loopmount_rootfs_descriptor_cleanup_detail"].(string)
	require.True(t, ok)
	assert.LessOrEqual(t, len(detail), maxRootfsCleanupDetailBytes)
	assert.True(t, domainErr.Details["loopmount_rootfs_descriptor_cleanup_failed"].(bool))
	assert.Empty(t, lease.retained)
	assert.Equal(t, -1, lease.rootfsFD)
}

func TestRootfsLeaseConcurrentReleaseClosesDescriptorsOnce(t *testing.T) {
	fixture := newRootfsAuthorityFixture(t, []string{"srv", "cache", "vms", testVMID, "rootfs.img"})
	lease, err := admitVMProvisionRootfs(t.Context(), fixture.deps(), testRootfsCaller(), testVMProvisionRequest())
	require.NoError(t, err)

	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			assert.NoError(t, lease.Release(context.Background()))
		}()
	}
	wait.Wait()
	assert.Len(t, fixture.closeCalls, 6)
}

// Rationale: Cancellation can arrive after the leaf is open but before admission finishes. Failure cleanup must still
// close the leaf and every retained parent in reverse order with the original context values available to diagnostics.
func TestCancelledRootfsAdmissionClosesPartialAuthorityInReverse(t *testing.T) {
	fixture := newRootfsAuthorityFixture(t, []string{"srv", "cache", "vms", testVMID, "rootfs.img"})
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), rootfsCleanupContextKey{}, "admission"))
	fixture.statxHook = func(fd, call int) {
		if fd == 15 && call == 0 {
			cancel()
		}
	}

	lease, err := admitVMProvisionRootfs(ctx, fixture.deps(), testRootfsCaller(), testVMProvisionRequest())

	require.Error(t, err)
	assert.Nil(t, lease)
	assert.ErrorIs(t, err, context.Canceled)
	wantFDs := []int{15, 14, 13, 12, 11, 10}
	gotFDs := make([]int, 0, len(fixture.closeCalls))
	for _, call := range fixture.closeCalls {
		gotFDs = append(gotFDs, call.fd)
		assert.NoError(t, call.contextErr)
		assert.Equal(t, "admission", call.contextMark)
	}
	if diff := cmp.Diff(wantFDs, gotFDs); diff != "" {
		t.Errorf("rejected descriptor close order mismatch (-want +got):\n%s", diff)
	}
}

// Rationale: cleanup diagnostics supplement the authoritative admission error. They must not mutate or reclassify the
// original DomainError, and a later cleanup failure must not overwrite the first bounded descriptor-cleanup detail.
func TestAppendRootfsCleanupErrorPreservesPrimaryDomainError(t *testing.T) {
	t.Parallel()

	type errorMetadata struct {
		Code    errs.Code
		Message string
		Op      string
		Entity  string
		Class   errs.Class
	}
	metadata := func(domainErr *errs.DomainError) errorMetadata {
		return errorMetadata{
			Code: domainErr.Code, Message: domainErr.Message, Op: domainErr.Op,
			Entity: domainErr.Entity, Class: domainErr.Class,
		}
	}

	primaryCause := errors.New("primary cause")
	primaryDetails := map[string]any{"admission_stage": "leaf"}
	primary := errs.WrapMsg(
		errs.CodeVMAtomicFailed,
		"primary failure",
		primaryCause,
		errs.WithClass(errs.ClassConflict),
		errs.WithEntity(testVMID),
		errs.WithDetails(primaryDetails),
	)
	wantMetadata := metadata(primary)
	outerWrapper := fmt.Errorf("outer rootfs admission wrapper: %w", primary)
	siblingCause := errors.New("sibling admission cause")
	primaryChain := errors.Join(outerWrapper, siblingCause)
	firstCleanupCause := errors.New(strings.Repeat("cleanup", maxRootfsCleanupDetailBytes))
	first := appendRootfsCleanupError(
		primaryChain,
		"close rejected descriptors",
		firstCleanupCause,
	)

	firstDomain := errs.AsDomainError(first)
	require.NotNil(t, firstDomain)
	require.NotSame(t, primary, firstDomain)

	if diff := cmp.Diff(wantMetadata, metadata(firstDomain)); diff != "" {
		t.Errorf("primary DomainError metadata changed (-want +got):\n%s", diff)
	}
	assert.ErrorIs(t, first, primaryChain)
	assert.ErrorIs(t, first, outerWrapper)
	assert.ErrorIs(t, first, primary)
	assert.ErrorIs(t, first, primaryCause)
	assert.ErrorIs(t, first, siblingCause)
	assert.ErrorIs(t, first, firstCleanupCause)
	firstDetail, ok := firstDomain.Details[rootfsCleanupDetailKey].(string)
	require.True(t, ok)
	unboundedFirstDetail := "close rejected descriptors: " + firstCleanupCause.Error()
	wantFirstDetail := unboundedFirstDetail[:maxRootfsCleanupDetailBytes-3] + "..."
	assert.Equal(t, wantFirstDetail, firstDetail)
	wantFirstDetails := map[string]any{
		"admission_stage":      "leaf",
		rootfsCleanupFailedKey: true,
		rootfsCleanupDetailKey: wantFirstDetail,
	}
	if diff := cmp.Diff(wantFirstDetails, firstDomain.Details); diff != "" {
		t.Errorf("cleanup evidence mismatch (-want +got):\n%s", diff)
	}

	secondCleanupCause := errors.New("close cache root failed")
	second := appendRootfsCleanupError(first, "close cache root", secondCleanupCause)
	secondDomain := errs.AsDomainError(second)
	require.NotNil(t, secondDomain)
	require.NotSame(t, firstDomain, secondDomain)
	assert.ErrorIs(t, second, first)
	assert.ErrorIs(t, second, primaryChain)
	assert.ErrorIs(t, second, outerWrapper)
	assert.ErrorIs(t, second, primary)
	assert.ErrorIs(t, second, primaryCause)
	assert.ErrorIs(t, second, siblingCause)
	assert.ErrorIs(t, second, firstCleanupCause)
	assert.ErrorIs(t, second, secondCleanupCause)
	if diff := cmp.Diff(wantMetadata, metadata(secondDomain)); diff != "" {
		t.Errorf("repeated cleanup changed primary DomainError metadata (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(wantFirstDetails, secondDomain.Details); diff != "" {
		t.Errorf("existing cleanup evidence overwritten (-want +got):\n%s", diff)
	}

	wantPrimaryDetails := map[string]any{"admission_stage": "leaf"}
	if diff := cmp.Diff(wantPrimaryDetails, primary.Details); diff != "" {
		t.Errorf("original primary details mutated (-want +got):\n%s", diff)
	}
	assert.Same(t, primaryCause, primary.Err)
	if diff := cmp.Diff(wantMetadata, metadata(primary)); diff != "" {
		t.Errorf("original primary metadata mutated (-want +got):\n%s", diff)
	}
}

// Rationale: callers may already own the fixed detail keys. Cleanup annotation must preserve those values instead of
// silently replacing evidence from an earlier boundary.
func TestAppendRootfsCleanupErrorPreservesExistingDetailKeys(t *testing.T) {
	t.Parallel()

	primaryCause := errors.New("primary cause")
	primaryDetails := map[string]any{
		"admission_stage":      "leaf",
		rootfsCleanupFailedKey: true,
		rootfsCleanupDetailKey: "earlier cleanup detail",
	}
	wantDetails := map[string]any{
		"admission_stage":      "leaf",
		rootfsCleanupFailedKey: true,
		rootfsCleanupDetailKey: "earlier cleanup detail",
	}
	primary := errs.WrapMsg(
		errs.CodeVMAtomicFailed,
		"primary failure",
		primaryCause,
		errs.WithDetails(primaryDetails),
	)
	cleanupCause := errors.New("later cleanup cause")
	joined := appendRootfsCleanupError(primary, "later cleanup", cleanupCause)

	domainErr := errs.AsDomainError(joined)
	require.NotNil(t, domainErr)
	require.NotSame(t, primary, domainErr)
	assert.ErrorIs(t, joined, primaryCause)
	assert.ErrorIs(t, joined, cleanupCause)
	if diff := cmp.Diff(wantDetails, domainErr.Details); diff != "" {
		t.Errorf("existing cleanup evidence changed (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(wantDetails, primary.Details); diff != "" {
		t.Errorf("original primary details mutated (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(wantDetails, primaryDetails); diff != "" {
		t.Errorf("caller-owned primary detail map mutated (-want +got):\n%s", diff)
	}
}

// Rationale: cleanup annotation must preserve the complete error chain even when no DomainError exists to clone.
func TestAppendRootfsCleanupErrorPreservesNonDomainPrimaryChain(t *testing.T) {
	t.Parallel()

	primaryCause := errors.New("primary read cause")
	primary := fmt.Errorf("read admitted rootfs: %w", primaryCause)
	cleanupCause := errors.New("close admitted rootfs failed")
	joined := appendRootfsCleanupError(primary, "close admitted rootfs", cleanupCause)

	domainErr := errs.AsDomainError(joined)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeLoopMountError, domainErr.Code)
	assert.Equal(t, "close admitted rootfs: close admitted rootfs failed", domainErr.Message)
	assert.ErrorIs(t, joined, primary)
	assert.ErrorIs(t, joined, primaryCause)
	assert.ErrorIs(t, joined, cleanupCause)
	wantDetails := map[string]any{
		rootfsCleanupFailedKey: true,
		rootfsCleanupDetailKey: "close admitted rootfs: close admitted rootfs failed",
	}
	if diff := cmp.Diff(wantDetails, domainErr.Details); diff != "" {
		t.Errorf("non-domain cleanup evidence mismatch (-want +got):\n%s", diff)
	}
}

type rootfsOpenCall struct {
	parentFD  int
	component string
	how       unix.OpenHow
	fd        int
}

type rootfsAuthorityFixture struct {
	t           *testing.T
	rootFD      int
	rootFlags   int
	openCalls   []rootfsOpenCall
	stats       map[int][]unix.Statx_t
	statCalls   map[int]int
	statxHook   func(int, int)
	filesystem  int64
	statfsFD    int
	statfsError error
	closeCalls  []rootfsCloseCall
	openError   map[int]error
	closeError  map[int]error
}

type rootfsCloseCall struct {
	fd          int
	contextErr  error
	contextMark string
}

func newRootfsAuthorityFixture(t *testing.T, components []string) *rootfsAuthorityFixture {
	t.Helper()

	fixture := &rootfsAuthorityFixture{
		t:          t,
		rootFD:     10,
		stats:      make(map[int][]unix.Statx_t),
		statCalls:  make(map[int]int),
		filesystem: unix.EXT4_SUPER_MAGIC,
		openError:  make(map[int]error),
		closeError: make(map[int]error),
	}
	fixture.stats[fixture.rootFD] = []unix.Statx_t{rootfsDirectoryStat(0, 0755, 1)}
	for index := range components {
		fd := fixture.rootFD + index + 1
		uid := uint32(testRootfsCallerUID)
		mode := uint16(0700)
		if index == 0 {
			uid = 0
			mode = 0755
		}
		if index == len(components)-1 {
			fixture.stats[fd] = []unix.Statx_t{
				rootfsFileStat(uid, 0600, uint64(testExpectedRootfsBytes), uint64(index+2)),
				rootfsFileStat(uid, 0600, uint64(testExpectedRootfsBytes), uint64(index+2)),
			}
			continue
		}
		fixture.stats[fd] = []unix.Statx_t{rootfsDirectoryStat(uid, mode, uint64(index+2))}
	}
	return fixture
}

func (fixture *rootfsAuthorityFixture) deps() rootfsAuthorityDeps {
	return rootfsAuthorityDeps{
		openRoot: func(_ context.Context, path string, flags int, mode uint32) (int, error) {
			assert.Equal(fixture.t, "/", path)
			assert.Equal(fixture.t, uint32(0), mode)
			fixture.rootFlags = flags
			return fixture.rootFD, nil
		},
		openAt2: func(_ context.Context, parentFD int, component string, how *unix.OpenHow) (int, error) {
			index := len(fixture.openCalls)
			if err := fixture.openError[index]; err != nil {
				return -1, err
			}
			fd := fixture.rootFD + index + 1
			fixture.openCalls = append(fixture.openCalls, rootfsOpenCall{
				parentFD:  parentFD,
				component: component,
				how:       *how,
				fd:        fd,
			})
			return fd, nil
		},
		statx: func(_ context.Context, fd, flags, mask int, stat *unix.Statx_t) error {
			assert.Equal(fixture.t, rootfsStatxFlags, flags)
			assert.Equal(fixture.t, rootfsStatxMask, mask)
			values := fixture.stats[fd]
			require.NotEmpty(fixture.t, values)
			index := fixture.statCalls[fd]
			if index >= len(values) {
				index = len(values) - 1
			}
			*stat = values[index]
			fixture.statCalls[fd]++
			if fixture.statxHook != nil {
				fixture.statxHook(fd, fixture.statCalls[fd]-1)
			}
			return nil
		},
		statfs: func(_ context.Context, fd int, stat *unix.Statfs_t) error {
			fixture.statfsFD = fd
			if fixture.statfsError != nil {
				return fixture.statfsError
			}
			stat.Type = fixture.filesystem
			return nil
		},
		close: func(ctx context.Context, fd int) error {
			mark, _ := ctx.Value(rootfsCleanupContextKey{}).(string)
			fixture.closeCalls = append(fixture.closeCalls, rootfsCloseCall{
				fd: fd, contextErr: ctx.Err(), contextMark: mark,
			})
			return fixture.closeError[fd]
		},
	}
}

func rootfsDirectoryStat(uid uint32, permissions uint16, inode uint64) unix.Statx_t {
	return unix.Statx_t{
		Mask:      rootfsStatxRequiredDirectory | unix.STATX_MNT_ID_UNIQUE,
		Mode:      unix.S_IFDIR | permissions,
		Uid:       uid,
		Gid:       9000,
		Ino:       inode,
		Dev_major: 8,
		Dev_minor: 1,
		Mnt_id:    77,
	}
}

func rootfsFileStat(uid uint32, permissions uint16, size, inode uint64) unix.Statx_t {
	return unix.Statx_t{
		Mask:      rootfsStatxRequiredFile,
		Mode:      unix.S_IFREG | permissions,
		Uid:       uid,
		Gid:       9000,
		Nlink:     1,
		Ino:       inode,
		Size:      size,
		Dev_major: 8,
		Dev_minor: 1,
	}
}

func testRootfsCaller() Caller {
	return Caller{UID: testRootfsCallerUID, GID: testRootfsCallerGID}
}

func testVMProvisionRequest() VMProvisionRequest {
	return VMProvisionRequest{
		CacheLocator:        "/srv/cache",
		VMID:                testVMID,
		ExpectedRootfsBytes: testExpectedRootfsBytes,
		Filesystem:          FilesystemExt4,
		Plan:                VMProvisionPlan{FixFstab: true},
	}
}

func testImageInspectRequest() ImageInspectRequest {
	return ImageInspectRequest{
		CacheLocator:       "/srv/cache",
		ImageID:            testImageID,
		ExpectedImageBytes: testExpectedRootfsBytes,
		Filesystem:         FilesystemExt4,
	}
}

func testImageOptimizeRequest() ImageOptimizeRequest {
	return ImageOptimizeRequest{
		CacheLocator:       "/srv/cache",
		ImageID:            testImageID,
		ExpectedImageBytes: testExpectedRootfsBytes,
		Filesystem:         FilesystemExt4,
		Plan:               ImageOptimizePlan{FixFstab: true},
	}
}

type rootfsCleanupContextKey struct{}
