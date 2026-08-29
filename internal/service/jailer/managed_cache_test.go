package jailer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

func TestParseManagedCacheLocator(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		raw        string
		want       []string
		wantErr    bool
		wantErrMsg string
	}{
		"absolute custom cache": {
			raw:  "/mnt/vm storage/mvmctl",
			want: []string{"mnt", "vm storage", "mvmctl"},
		},
		"empty":               {raw: "", wantErr: true, wantErrMsg: "absolute"},
		"root":                {raw: "/", wantErr: true, wantErrMsg: "root"},
		"relative":            {raw: "cache/mvmctl", wantErr: true, wantErrMsg: "absolute"},
		"dot component":       {raw: "/home/./mvmctl", wantErr: true, wantErrMsg: "canonical"},
		"dot dot component":   {raw: "/home/user/../mvmctl", wantErr: true, wantErrMsg: "canonical"},
		"duplicate separator": {raw: "/home//mvmctl", wantErr: true, wantErrMsg: "canonical"},
		"trailing separator":  {raw: "/home/mvmctl/", wantErr: true, wantErrMsg: "canonical"},
		"nul":                 {raw: "/home/mvmctl\x00escape", wantErr: true, wantErrMsg: "NUL"},
		"component exceeds limit": {
			raw:        "/" + strings.Repeat("a", unix.NAME_MAX+1),
			wantErr:    true,
			wantErrMsg: "component",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			locator, err := parseManagedCacheLocator(tc.raw)
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErrMsg)
				requireManagedCacheDomainErrorCode(t, err, errs.CodeValidationFailed)
				return
			}
			require.NoError(t, err)
			if diff := cmp.Diff(tc.want, locator.components); diff != "" {
				t.Errorf("managed cache components mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestParseManagedCacheLocatorRejectsPathAndDepthBounds(t *testing.T) {
	t.Parallel()

	overlong := "/" + strings.Repeat("a", unix.PathMax-1)
	_, err := parseManagedCacheLocator(overlong)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "length")

	deep := ""
	for range maxManagedCacheLocatorDepth + 1 {
		deep += "/a"
	}
	_, err = parseManagedCacheLocator(deep)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "depth")
}

func TestPinManagedCacheRetainsDescriptorIdentity(t *testing.T) {
	t.Parallel()

	fixture := prepareManagedCacheFixture(t)
	lease, err := pinManagedCache(
		context.Background(),
		fixture.deps,
		fixture.policy,
		fixture.caller,
		fixture.locator,
	)
	require.NoError(t, err)
	require.NotNil(t, lease)
	t.Cleanup(func() { require.NoError(t, lease.Release(context.Background())) })
	require.NotEmpty(t, lease.retained)
	assert.Equal(t, fixture.caller.uid, lease.ownerUID)
	assert.NotZero(t, lease.identity.inode)
	assert.NotZero(t, lease.identity.deviceMajor|lease.identity.deviceMinor)

	var stat unix.Statx_t
	require.NoError(t, unix.Statx(
		lease.cacheFD,
		"",
		managedCacheStatxFlags,
		managedCacheStatxMask,
		&stat,
	))
	assert.Equal(t, stat.Ino, lease.identity.inode)
	assert.Equal(t, stat.Dev_major, lease.identity.deviceMajor)
	assert.Equal(t, stat.Dev_minor, lease.identity.deviceMinor)
	switch {
	case stat.Mask&unix.STATX_MNT_ID_UNIQUE != 0:
		assert.Equal(t, stat.Mnt_id, lease.identity.mountID)
		assert.Equal(t, managedCacheMountIdentityUnique, lease.identity.mountIDKind)
	case stat.Mask&unix.STATX_MNT_ID != 0:
		assert.Equal(t, stat.Mnt_id, lease.identity.mountID)
		assert.Equal(t, managedCacheMountIdentityLegacy, lease.identity.mountIDKind)
	default:
		assert.Zero(t, lease.identity.mountID)
		assert.Equal(t, managedCacheMountIdentityUnavailable, lease.identity.mountIDKind)
	}

	repinned, err := repinManagedCache(
		context.Background(),
		fixture.deps,
		fixture.policy,
		fixture.caller,
		fixture.locator,
		lease.identity,
	)
	require.NoError(t, err)
	require.NotNil(t, repinned)
	t.Cleanup(func() { require.NoError(t, repinned.Release(context.Background())) })
	if diff := cmp.Diff(
		lease.identity,
		repinned.identity,
		cmpopts.EquateComparable(managedCacheIdentity{}),
	); diff != "" {
		t.Errorf("re-pinned cache identity mismatch (-want +got):\n%s", diff)
	}
}

func TestManagedCacheDescriptorSurvivesLocatorReplacement(t *testing.T) {
	t.Parallel()
	// Rationale: The invoking user can rename its cache path after root validates it. The retained descriptor
	// must keep referring to the originally inspected directory, while a later path-based re-pin fails closed.

	fixture := prepareManagedCacheFixture(t)
	lease, err := pinManagedCache(
		context.Background(),
		fixture.deps,
		fixture.policy,
		fixture.caller,
		fixture.locator,
	)
	require.NoError(t, err)
	require.NotNil(t, lease)
	t.Cleanup(func() { require.NoError(t, lease.Release(context.Background())) })

	detachedPath := fixture.cacheDir + ".detached"
	require.NoError(t, os.Rename(fixture.cacheDir, detachedPath))
	require.NoError(t, os.Mkdir(fixture.cacheDir, 0700))
	if os.Geteuid() == 0 {
		require.NoError(t, os.Chown(fixture.cacheDir, int(fixture.caller.uid), -1))
	}

	var pinnedStat unix.Stat_t
	require.NoError(t, unix.Fstat(lease.cacheFD, &pinnedStat))
	assert.Equal(t, fixture.cacheInode, pinnedStat.Ino)
	assert.Equal(t, fixture.cacheInode, lease.identity.inode)

	repinned, err := repinManagedCache(
		context.Background(),
		fixture.deps,
		fixture.policy,
		fixture.caller,
		fixture.locator,
		lease.identity,
	)
	require.Error(t, err)
	assert.Nil(t, repinned)
	assert.Contains(t, err.Error(), "identity")
}

func TestRepinManagedCacheResistsConcurrentRenameAndSymlinkReplacement(t *testing.T) {
	tests := map[string]func(*testing.T, *managedCacheFixture) (string, string){
		"caller-owned ancestor": func(t *testing.T, fixture *managedCacheFixture) (string, string) {
			attackerOwner := filepath.Join(fixture.root, "attacker-owner")
			require.NoError(t, os.Mkdir(attackerOwner, 0700))
			require.NoError(t, os.Mkdir(filepath.Join(attackerOwner, "mvmctl"), 0700))
			if os.Geteuid() == 0 {
				require.NoError(t, os.Chown(attackerOwner, int(fixture.caller.uid), -1))
				require.NoError(t, os.Chown(filepath.Join(attackerOwner, "mvmctl"), int(fixture.caller.uid), -1))
			}
			return fixture.ownerDir, attackerOwner
		},
		"cache root": func(t *testing.T, fixture *managedCacheFixture) (string, string) {
			attackerCache := filepath.Join(fixture.root, "attacker-cache")
			require.NoError(t, os.Mkdir(attackerCache, 0700))
			if os.Geteuid() == 0 {
				require.NoError(t, os.Chown(attackerCache, int(fixture.caller.uid), -1))
			}
			return fixture.cacheDir, attackerCache
		},
	}

	for name, prepareReplacement := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := prepareManagedCacheFixture(t)
			targetPath, attackerPath := prepareReplacement(t, &fixture)
			assertManagedCacheReplacementRace(t, fixture, targetPath, attackerPath)
		})
	}
}

func assertManagedCacheReplacementRace(
	t *testing.T,
	fixture managedCacheFixture,
	targetPath string,
	attackerPath string,
) {
	t.Helper()
	// Rationale: A malicious caller can race every caller-owned lookup. Re-pin may reject a transient path, but it
	// must never return a lease for the replacement directory or follow the replacement symlink.
	original, err := pinManagedCache(
		context.Background(),
		fixture.deps,
		fixture.policy,
		fixture.caller,
		fixture.locator,
	)
	require.NoError(t, err)
	require.NotNil(t, original)
	t.Cleanup(func() { require.NoError(t, original.Release(context.Background())) })

	detachedPath := targetPath + ".race-detached"
	firstReplacement := make(chan struct{})
	continueRace := make(chan struct{})
	stop := make(chan struct{})
	done := make(chan struct{})
	attackerErr := make(chan error, 1)
	go func() {
		defer close(done)
		first := true
		for {
			select {
			case <-stop:
				return
			default:
			}
			if renameErr := os.Rename(targetPath, detachedPath); renameErr != nil {
				attackerErr <- renameErr
				return
			}
			if symlinkErr := os.Symlink(attackerPath, targetPath); symlinkErr != nil {
				attackerErr <- symlinkErr
				return
			}
			if first {
				close(firstReplacement)
				select {
				case <-continueRace:
				case <-stop:
					return
				}
				first = false
			}
			if removeErr := os.Remove(targetPath); removeErr != nil {
				attackerErr <- removeErr
				return
			}
			if renameErr := os.Rename(detachedPath, targetPath); renameErr != nil {
				attackerErr <- renameErr
				return
			}
		}
	}()
	stopped := false
	stopRace := func() {
		if stopped {
			return
		}
		close(stop)
		<-done
		stopped = true
	}
	defer stopRace()
	<-firstReplacement

	repinned, err := repinManagedCache(
		context.Background(),
		fixture.deps,
		fixture.policy,
		fixture.caller,
		fixture.locator,
		original.identity,
	)
	require.Error(t, err)
	assert.Nil(t, repinned)
	close(continueRace)

	for range 256 {
		repinned, repinErr := repinManagedCache(
			context.Background(),
			fixture.deps,
			fixture.policy,
			fixture.caller,
			fixture.locator,
			original.identity,
		)
		if repinErr != nil {
			continue
		}
		require.NotNil(t, repinned)
		if diff := cmp.Diff(
			original.identity,
			repinned.identity,
			cmpopts.EquateComparable(managedCacheIdentity{}),
		); diff != "" {
			t.Errorf("race re-pin identity mismatch (-want +got):\n%s", diff)
		}
		require.NoError(t, repinned.Release(context.Background()))
	}
	stopRace()
	select {
	case err := <-attackerErr:
		require.NoError(t, err)
	default:
	}
}

func TestPinManagedCacheAcceptsUnavailableMountIdentity(t *testing.T) {
	t.Parallel()
	// Rationale: ADR-0016 records mount identity where the kernel provides it. Device and inode remain mandatory
	// on kernels or filesystems that omit STATX_MNT_ID, and both sides of a later re-pin must agree on that absence.

	fixture := prepareManagedCacheFixture(t)
	realStatx := fixture.deps.statx
	fixture.deps.statx = func(ctx context.Context, fd, flags, mask int, stat *unix.Statx_t) error {
		if err := realStatx(ctx, fd, flags, mask, stat); err != nil {
			return err
		}
		stat.Mask &^= unix.STATX_MNT_ID | unix.STATX_MNT_ID_UNIQUE
		stat.Mnt_id = 0
		return nil
	}
	lease, err := pinManagedCache(
		context.Background(),
		fixture.deps,
		fixture.policy,
		fixture.caller,
		fixture.locator,
	)
	require.NoError(t, err)
	require.NotNil(t, lease)
	t.Cleanup(func() { require.NoError(t, lease.Release(context.Background())) })
	assert.Zero(t, lease.identity.mountID)
	assert.Equal(t, managedCacheMountIdentityUnavailable, lease.identity.mountIDKind)

	repinned, err := repinManagedCache(
		context.Background(),
		fixture.deps,
		fixture.policy,
		fixture.caller,
		fixture.locator,
		lease.identity,
	)
	require.NoError(t, err)
	require.NotNil(t, repinned)
	t.Cleanup(func() { require.NoError(t, repinned.Release(context.Background())) })
}

func TestPinManagedCachePrefersUniqueMountIdentityAndFallsBackToLegacy(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		mutate   func(*unix.Statx_t)
		wantKind managedCacheMountIdentityKind
		wantID   uint64
	}{
		"unique preferred": {
			mutate: func(stat *unix.Statx_t) {
				stat.Mask |= unix.STATX_MNT_ID | unix.STATX_MNT_ID_UNIQUE
				stat.Mnt_id = 701
			},
			wantKind: managedCacheMountIdentityUnique,
			wantID:   701,
		},
		"legacy fallback": {
			mutate: func(stat *unix.Statx_t) {
				stat.Mask &^= unix.STATX_MNT_ID_UNIQUE
				stat.Mask |= unix.STATX_MNT_ID
				stat.Mnt_id = 702
			},
			wantKind: managedCacheMountIdentityLegacy,
			wantID:   702,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := prepareManagedCacheFixture(t)
			realStatx := fixture.deps.statx
			fixture.deps.statx = func(ctx context.Context, fd, flags, mask int, stat *unix.Statx_t) error {
				if err := realStatx(ctx, fd, flags, mask, stat); err != nil {
					return err
				}
				if stat.Ino == fixture.cacheInode {
					tc.mutate(stat)
				}
				return nil
			}

			lease, err := pinManagedCache(
				context.Background(),
				fixture.deps,
				fixture.policy,
				fixture.caller,
				fixture.locator,
			)
			require.NoError(t, err)
			require.NotNil(t, lease)
			t.Cleanup(func() { require.NoError(t, lease.Release(context.Background())) })
			assert.Equal(t, tc.wantKind, lease.identity.mountIDKind)
			assert.Equal(t, tc.wantID, lease.identity.mountID)
		})
	}
}

func TestRepinManagedCacheRejectsEveryIdentityMismatch(t *testing.T) {
	t.Parallel()

	fixture := prepareManagedCacheFixture(t)
	lease, err := pinManagedCache(
		context.Background(),
		fixture.deps,
		fixture.policy,
		fixture.caller,
		fixture.locator,
	)
	require.NoError(t, err)
	require.NotNil(t, lease)
	t.Cleanup(func() { require.NoError(t, lease.Release(context.Background())) })

	tests := map[string]func(managedCacheIdentity) managedCacheIdentity{
		"device major": func(identity managedCacheIdentity) managedCacheIdentity {
			identity.deviceMajor++
			return identity
		},
		"device minor": func(identity managedCacheIdentity) managedCacheIdentity {
			identity.deviceMinor++
			return identity
		},
		"inode": func(identity managedCacheIdentity) managedCacheIdentity {
			identity.inode++
			return identity
		},
		"mount": func(identity managedCacheIdentity) managedCacheIdentity {
			identity.mountID++
			return identity
		},
		"mount identity kind": func(identity managedCacheIdentity) managedCacheIdentity {
			identity.mountIDKind++
			return identity
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			repinned, repinErr := repinManagedCache(
				context.Background(),
				fixture.deps,
				fixture.policy,
				fixture.caller,
				fixture.locator,
				mutate(lease.identity),
			)
			require.Error(t, repinErr)
			assert.Nil(t, repinned)
			requireManagedCacheDomainErrorCode(t, repinErr, errs.CodeVMAtomicFailed)
			assert.Contains(t, repinErr.Error(), "identity")
		})
	}
}

func TestPinManagedCacheUsesOnlyConstrainedDescriptorResolution(t *testing.T) {
	t.Parallel()

	fixture := prepareManagedCacheFixture(t)
	realOpen := fixture.deps.open
	realOpenAt2 := fixture.deps.openAt2
	realStatx := fixture.deps.statx
	realStatfs := fixture.deps.statfs
	openedPaths := make([]string, 0, 1)
	seenComponents := make([]string, 0, len(fixture.locator.components))
	statxCalls := 0
	statfsCalls := 0
	fixture.deps.open = func(ctx context.Context, path string, flags int, mode uint32) (int, error) {
		openedPaths = append(openedPaths, path)
		return realOpen(ctx, path, flags, mode)
	}
	fixture.deps.openAt2 = func(
		ctx context.Context,
		parentFD int,
		name string,
		how *unix.OpenHow,
	) (int, error) {
		seenComponents = append(seenComponents, name)
		assert.Equal(t, uint64(managedCacheOpenFlags), how.Flags)
		assert.Equal(t, uint64(managedCacheResolveFlags), how.Resolve)
		assert.Zero(t, how.Resolve&unix.RESOLVE_NO_XDEV,
			"custom cache roots may reside on a separate filesystem")
		return realOpenAt2(ctx, parentFD, name, how)
	}
	fixture.deps.statx = func(ctx context.Context, fd, flags, mask int, stat *unix.Statx_t) error {
		statxCalls++
		assert.Equal(t, managedCacheStatxFlags, flags)
		assert.NotZero(t, flags&unix.AT_STATX_FORCE_SYNC)
		assert.Zero(t, flags&unix.AT_STATX_DONT_SYNC)
		assert.Equal(t, managedCacheStatxMask, mask)
		return realStatx(ctx, fd, flags, mask, stat)
	}
	fixture.deps.statfs = func(ctx context.Context, fd int, stat *unix.Statfs_t) error {
		statfsCalls++
		return realStatfs(ctx, fd, stat)
	}

	lease, err := pinManagedCache(
		context.Background(),
		fixture.deps,
		fixture.policy,
		fixture.caller,
		fixture.locator,
	)
	require.NoError(t, err)
	require.NotNil(t, lease)
	require.NoError(t, lease.Release(context.Background()))
	if diff := cmp.Diff([]string{fixture.policy.rootPath}, openedPaths); diff != "" {
		t.Errorf("pathname opens mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(fixture.locator.components, seenComponents); diff != "" {
		t.Errorf("descriptor-relative components mismatch (-want +got):\n%s", diff)
	}
	assert.Equal(t, len(fixture.locator.components)+1, statxCalls)
	assert.Equal(t, 1, statfsCalls, "only the resource-bearing cache mount requires an approved filesystem")
}

func TestPinManagedCacheRejectsForgedLocatorBeforeFilesystemAccess(t *testing.T) {
	t.Parallel()
	// Rationale: The typed locator is private but still constructible elsewhere in this package. The privileged
	// effect boundary must re-check its invariants instead of depending only on the wire parser's constructor.

	tests := map[string]managedCacheLocator{
		"empty":              {},
		"slash in component": {components: []string{"home/caller", "mvmctl"}},
		"dot component":      {components: []string{"home", "..", "mvmctl"}},
		"nul component":      {components: []string{"home", "caller\x00escape", "mvmctl"}},
	}

	for name, locator := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := prepareManagedCacheFixture(t)
			openCalled := false
			fixture.deps.open = func(context.Context, string, int, uint32) (int, error) {
				openCalled = true
				return -1, errors.New("unexpected open")
			}
			lease, err := pinManagedCache(
				context.Background(),
				fixture.deps,
				fixture.policy,
				fixture.caller,
				locator,
			)
			require.Error(t, err)
			assert.Nil(t, lease)
			requireManagedCacheDomainErrorCode(t, err, errs.CodeValidationFailed)
			assert.False(t, openCalled)
		})
	}
}

func TestPinManagedCacheRejectsUnsafeComponents(t *testing.T) {
	tests := map[string]func(*testing.T, *managedCacheFixture){
		"ancestor symlink": func(t *testing.T, fixture *managedCacheFixture) {
			require.NoError(t, os.RemoveAll(fixture.ownerDir))
			require.NoError(t, os.Symlink(fixture.cacheDir, fixture.ownerDir))
		},
		"cache symlink": func(t *testing.T, fixture *managedCacheFixture) {
			require.NoError(t, os.Remove(fixture.cacheDir))
			require.NoError(t, os.Symlink(fixture.ownerDir, fixture.cacheDir))
		},
		"cache is regular file": func(t *testing.T, fixture *managedCacheFixture) {
			require.NoError(t, os.Remove(fixture.cacheDir))
			require.NoError(t, os.WriteFile(fixture.cacheDir, []byte("not a directory"), 0600))
		},
		"ancestor group writable": func(t *testing.T, fixture *managedCacheFixture) {
			require.NoError(t, os.Chmod(fixture.ownerDir, 0770))
		},
		"cache world writable": func(t *testing.T, fixture *managedCacheFixture) {
			require.NoError(t, os.Chmod(fixture.cacheDir, 0707))
		},
		"cache not writable": func(t *testing.T, fixture *managedCacheFixture) {
			require.NoError(t, os.Chmod(fixture.cacheDir, 0500))
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := prepareManagedCacheFixture(t)
			mutate(t, &fixture)
			lease, err := pinManagedCache(
				context.Background(),
				fixture.deps,
				fixture.policy,
				fixture.caller,
				fixture.locator,
			)
			require.Error(t, err)
			assert.Nil(t, lease)
			requireManagedCacheDomainErrorCode(t, err, errs.CodeValidationFailed)
		})
	}
}

func TestPinManagedCacheRejectsForeignOwnerAndIncompleteIdentity(t *testing.T) {
	tests := map[string]func(*managedCacheFixture){
		"foreign final owner": func(fixture *managedCacheFixture) {
			realStatx := fixture.deps.statx
			fixture.deps.statx = func(ctx context.Context, fd, flags, mask int, stat *unix.Statx_t) error {
				if err := realStatx(ctx, fd, flags, mask, stat); err != nil {
					return err
				}
				if stat.Ino == fixture.cacheInode {
					stat.Uid = fixture.caller.uid + 1
				}
				return nil
			}
		},
		"foreign ancestor owner": func(fixture *managedCacheFixture) {
			realStatx := fixture.deps.statx
			fixture.deps.statx = func(ctx context.Context, fd, flags, mask int, stat *unix.Statx_t) error {
				if err := realStatx(ctx, fd, flags, mask, stat); err != nil {
					return err
				}
				if stat.Ino == fixture.ownerInode {
					stat.Uid = fixture.caller.uid + 1
				}
				return nil
			}
		},
		"missing inode identity": func(fixture *managedCacheFixture) {
			realStatx := fixture.deps.statx
			fixture.deps.statx = func(ctx context.Context, fd, flags, mask int, stat *unix.Statx_t) error {
				if err := realStatx(ctx, fd, flags, mask, stat); err != nil {
					return err
				}
				if stat.Ino == fixture.cacheInode {
					stat.Mask &^= unix.STATX_INO
				}
				return nil
			}
		},
		"automount topology": func(fixture *managedCacheFixture) {
			realStatx := fixture.deps.statx
			fixture.deps.statx = func(ctx context.Context, fd, flags, mask int, stat *unix.Statx_t) error {
				if err := realStatx(ctx, fd, flags, mask, stat); err != nil {
					return err
				}
				if stat.Ino == fixture.cacheInode {
					stat.Attributes_mask |= unix.STATX_ATTR_AUTOMOUNT
					stat.Attributes |= unix.STATX_ATTR_AUTOMOUNT
				}
				return nil
			}
		},
		"present zero unique mount identity": func(fixture *managedCacheFixture) {
			realStatx := fixture.deps.statx
			fixture.deps.statx = func(ctx context.Context, fd, flags, mask int, stat *unix.Statx_t) error {
				if err := realStatx(ctx, fd, flags, mask, stat); err != nil {
					return err
				}
				if stat.Ino == fixture.cacheInode {
					stat.Mask |= unix.STATX_MNT_ID_UNIQUE
					stat.Mnt_id = 0
				}
				return nil
			}
		},
		"unsupported filesystem topology": func(fixture *managedCacheFixture) {
			fixture.deps.statfs = func(context.Context, int, *unix.Statfs_t) error {
				return nil
			}
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := prepareManagedCacheFixture(t)
			mutate(&fixture)
			lease, err := pinManagedCache(
				context.Background(),
				fixture.deps,
				fixture.policy,
				fixture.caller,
				fixture.locator,
			)
			require.Error(t, err)
			assert.Nil(t, lease)
			requireManagedCacheDomainErrorCode(t, err, errs.CodeValidationFailed)
		})
	}
}

func TestPinManagedCachePreservesPrimaryErrorWhenCleanupFails(t *testing.T) {
	t.Parallel()
	// Rationale: A descriptor close failure is partial-state evidence, not permission to replace the primary
	// operation's stable DomainError identity.

	fixture := prepareManagedCacheFixture(t)
	realOpenAt2 := fixture.deps.openAt2
	realClose := fixture.deps.close
	openCalls := 0
	fixture.deps.openAt2 = func(
		ctx context.Context,
		parentFD int,
		name string,
		how *unix.OpenHow,
	) (int, error) {
		openCalls++
		if openCalls == 2 {
			return -1, errors.New("open component failed")
		}
		return realOpenAt2(ctx, parentFD, name, how)
	}
	fixture.deps.close = func(ctx context.Context, fd int) error {
		closeErr := realClose(ctx, fd)
		return errors.Join(closeErr, errors.New("close retained descriptor failed"))
	}

	lease, err := pinManagedCache(
		context.Background(),
		fixture.deps,
		fixture.policy,
		fixture.caller,
		fixture.locator,
	)
	require.Error(t, err)
	assert.Nil(t, lease)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeVMAtomicFailed, domainErr.Code)
	assert.Contains(t, domainErr.Message, "open managed cache component")
	assert.Contains(t, domainErr.Message, "close rejected managed cache descriptors")
	assert.Equal(t, true, domainErr.Details["managed_cache_descriptor_cleanup_failed"])
}

func TestManagedCacheLeaseClosesDescriptorsInReverseOrder(t *testing.T) {
	t.Parallel()

	fixture := prepareManagedCacheFixture(t)
	realOpen := fixture.deps.open
	realOpenAt2 := fixture.deps.openAt2
	realClose := fixture.deps.close
	opened := make([]int, 0, len(fixture.locator.components)+1)
	closed := make([]int, 0, len(fixture.locator.components)+1)
	fixture.deps.open = func(ctx context.Context, path string, flags int, mode uint32) (int, error) {
		fd, err := realOpen(ctx, path, flags, mode)
		if err == nil {
			opened = append(opened, fd)
		}
		return fd, err
	}
	fixture.deps.openAt2 = func(ctx context.Context, parentFD int, name string, how *unix.OpenHow) (int, error) {
		fd, err := realOpenAt2(ctx, parentFD, name, how)
		if err == nil {
			opened = append(opened, fd)
		}
		return fd, err
	}
	fixture.deps.close = func(ctx context.Context, fd int) error {
		closed = append(closed, fd)
		return realClose(ctx, fd)
	}
	lease, err := pinManagedCache(
		context.Background(),
		fixture.deps,
		fixture.policy,
		fixture.caller,
		fixture.locator,
	)
	require.NoError(t, err)
	require.NotNil(t, lease)
	expected := append([]int(nil), opened...)
	for left, right := 0, len(expected)-1; left < right; left, right = left+1, right-1 {
		expected[left], expected[right] = expected[right], expected[left]
	}

	require.NoError(t, lease.Release(context.Background()))
	if diff := cmp.Diff(expected, closed); diff != "" {
		t.Errorf("descriptor close order mismatch (-want +got):\n%s", diff)
	}
}

func TestPinManagedCacheCancellationAfterAcquisitionClosesDescriptors(t *testing.T) {
	t.Parallel()

	fixture := prepareManagedCacheFixture(t)
	realOpen := fixture.deps.open
	realOpenAt2 := fixture.deps.openAt2
	realClose := fixture.deps.close
	opened := make([]int, 0, 2)
	closed := make([]int, 0, 2)
	cleanupUncancelled := true
	ctx, cancel := context.WithCancel(context.Background())
	fixture.deps.open = func(ctx context.Context, path string, flags int, mode uint32) (int, error) {
		fd, err := realOpen(ctx, path, flags, mode)
		if err == nil {
			opened = append(opened, fd)
		}
		return fd, err
	}
	fixture.deps.openAt2 = func(ctx context.Context, parentFD int, name string, how *unix.OpenHow) (int, error) {
		fd, err := realOpenAt2(ctx, parentFD, name, how)
		if err == nil {
			opened = append(opened, fd)
			cancel()
		}
		return fd, err
	}
	fixture.deps.close = func(ctx context.Context, fd int) error {
		cleanupUncancelled = cleanupUncancelled && ctx.Err() == nil
		closed = append(closed, fd)
		return realClose(ctx, fd)
	}

	lease, err := pinManagedCache(ctx, fixture.deps, fixture.policy, fixture.caller, fixture.locator)
	require.Error(t, err)
	assert.Nil(t, lease)
	assert.ErrorIs(t, err, context.Canceled)
	expected := append([]int(nil), opened...)
	for left, right := 0, len(expected)-1; left < right; left, right = left+1, right-1 {
		expected[left], expected[right] = expected[right], expected[left]
	}
	if diff := cmp.Diff(expected, closed); diff != "" {
		t.Errorf("cancellation close order mismatch (-want +got):\n%s", diff)
	}
	assert.True(t, cleanupUncancelled)
}

func TestManagedCacheFilesystemPolicyIsClosed(t *testing.T) {
	t.Parallel()

	allowed := []int64{
		unix.EXT4_SUPER_MAGIC,
		unix.XFS_SUPER_MAGIC,
		unix.BTRFS_SUPER_MAGIC,
		unix.F2FS_SUPER_MAGIC,
		unix.BCACHEFS_SUPER_MAGIC,
		unix.TMPFS_MAGIC,
		managedCacheZFSSuperMagic,
	}
	for _, filesystemType := range allowed {
		assert.True(t, isSupportedManagedCacheFilesystem(filesystemType))
	}

	rejected := []int64{
		unix.FUSE_SUPER_MAGIC,
		unix.NFS_SUPER_MAGIC,
		unix.CIFS_SUPER_MAGIC,
		unix.OVERLAYFS_SUPER_MAGIC,
		unix.AUTOFS_SUPER_MAGIC,
	}
	for _, filesystemType := range rejected {
		assert.False(t, isSupportedManagedCacheFilesystem(filesystemType))
	}
}

func TestPinManagedCacheRequiresOpenat2(t *testing.T) {
	t.Parallel()

	fixture := prepareManagedCacheFixture(t)
	fixture.deps.openAt2 = func(context.Context, int, string, *unix.OpenHow) (int, error) {
		return -1, unix.ENOSYS
	}
	lease, err := pinManagedCache(
		context.Background(),
		fixture.deps,
		fixture.policy,
		fixture.caller,
		fixture.locator,
	)
	require.Error(t, err)
	assert.Nil(t, lease)
	requireManagedCacheDomainErrorCode(t, err, errs.CodeVMAtomicFailed)
	assert.ErrorIs(t, err, unix.ENOSYS)
}

func TestManagedCacheLeaseReleaseIsCheckedAndUncancelled(t *testing.T) {
	t.Parallel()

	type cleanupContextKey struct{}
	fixture := prepareManagedCacheFixture(t)
	realClose := fixture.deps.close
	contextObserved := false
	fixture.deps.close = func(ctx context.Context, fd int) error {
		if ctx.Value(cleanupContextKey{}) == "release" && ctx.Err() == nil {
			contextObserved = true
		}
		closeErr := realClose(ctx, fd)
		return errors.Join(closeErr, errors.New("release close failed"))
	}
	lease, err := pinManagedCache(
		context.Background(),
		fixture.deps,
		fixture.policy,
		fixture.caller,
		fixture.locator,
	)
	require.NoError(t, err)
	require.NotNil(t, lease)

	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), cleanupContextKey{}, "release"))
	cancel()
	err = lease.Release(ctx)
	require.Error(t, err)
	assert.True(t, contextObserved)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeVMAtomicFailed, domainErr.Code)
	assert.Equal(t, true, domainErr.Details["managed_cache_descriptor_cleanup_failed"])
	assert.Empty(t, lease.retained)
	assert.Equal(t, -1, lease.cacheFD)
}

func TestAppendManagedCacheCleanupErrorPreservesPrimaryDomainMetadata(t *testing.T) {
	t.Parallel()

	primary := errs.New(
		errs.CodeValidationFailed,
		"primary failure",
		errs.WithClass(errs.ClassConflict),
		errs.WithEntity(testVMID),
		errs.WithDetails(map[string]any{"record_registered": true}),
	)
	joined := appendManagedCacheCleanupError(primary, "close cache descriptors", errors.New("close failed"))
	domainErr := errs.AsDomainError(joined)
	require.NotNil(t, domainErr)
	assert.Same(t, primary, domainErr)
	assert.Equal(t, errs.CodeValidationFailed, domainErr.Code)
	assert.Equal(t, errs.ClassConflict, domainErr.Class)
	assert.Equal(t, testVMID, domainErr.Entity)
	assert.Equal(t, true, domainErr.Details["record_registered"])
	assert.Equal(t, true, domainErr.Details["managed_cache_descriptor_cleanup_failed"])
}

func TestPinManagedCacheHonorsPreCancelledContext(t *testing.T) {
	t.Parallel()

	fixture := prepareManagedCacheFixture(t)
	openCalled := false
	fixture.deps.open = func(context.Context, string, int, uint32) (int, error) {
		openCalled = true
		return -1, errors.New("unexpected open")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	lease, err := pinManagedCache(ctx, fixture.deps, fixture.policy, fixture.caller, fixture.locator)
	require.Error(t, err)
	assert.Nil(t, lease)
	assert.ErrorIs(t, err, context.Canceled)
	assert.False(t, openCalled)
}

type managedCacheFixture struct {
	deps       managedCacheDeps
	policy     managedCachePolicy
	caller     instanceCaller
	locator    managedCacheLocator
	root       string
	ownerDir   string
	cacheDir   string
	ownerInode uint64
	cacheInode uint64
}

func prepareManagedCacheFixture(t *testing.T) managedCacheFixture {
	t.Helper()

	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	homeDir := filepath.Join(root, "home")
	ownerDir := filepath.Join(homeDir, "caller")
	cacheDir := filepath.Join(ownerDir, "mvmctl")
	require.NoError(t, os.Mkdir(homeDir, 0755))
	require.NoError(t, os.Mkdir(ownerDir, 0700))
	require.NoError(t, os.Mkdir(cacheDir, 0700))

	callerUID := uint32(os.Geteuid())
	if callerUID == 0 {
		callerUID = testAuthorityUID
	}
	ownerInode := inodeForManagedCacheTestPath(t, ownerDir)
	cacheInode := inodeForManagedCacheTestPath(t, cacheDir)
	deps := realManagedCacheDeps()
	if callerUID != uint32(os.Geteuid()) {
		realStatx := deps.statx
		deps.statx = func(ctx context.Context, fd, flags, mask int, stat *unix.Statx_t) error {
			if err := realStatx(ctx, fd, flags, mask, stat); err != nil {
				return err
			}
			if stat.Ino == ownerInode || stat.Ino == cacheInode {
				stat.Uid = callerUID
			}
			return nil
		}
	}
	locator, err := parseManagedCacheLocator("/home/caller/mvmctl")
	require.NoError(t, err)
	return managedCacheFixture{
		deps:       deps,
		policy:     managedCachePolicy{rootPath: root, trustedUID: uint32(os.Geteuid())},
		caller:     instanceCaller{uid: callerUID},
		locator:    locator,
		root:       root,
		ownerDir:   ownerDir,
		cacheDir:   cacheDir,
		ownerInode: ownerInode,
		cacheInode: cacheInode,
	}
}

func inodeForManagedCacheTestPath(t *testing.T, path string) uint64 {
	t.Helper()

	var stat unix.Stat_t
	require.NoError(t, unix.Stat(path, &stat))
	return stat.Ino
}

func requireManagedCacheDomainErrorCode(t *testing.T, err error, code errs.Code) {
	t.Helper()

	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, code, domainErr.Code)
}
