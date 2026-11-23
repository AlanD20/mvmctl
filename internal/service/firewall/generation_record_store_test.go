package firewall

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

func TestGenerationRecordStoreCreatesStrictTreeAndExclusiveRecord(t *testing.T) {
	t.Parallel()

	root := prepareGenerationRecordTestRoot(t)
	store, err := openGenerationRecordStore(
		t.Context(),
		realGenerationRecordStoreDeps(),
		testGenerationRecordStorePolicy(root),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Release(context.Background())) })

	_, _, found, err := store.read(t.Context())
	require.NoError(t, err)
	assert.False(t, found)
	record := testCommittedGenerationRecord(t)
	createdProof, err := store.create(t.Context(), record)
	require.NoError(t, err)
	assert.NotZero(t, createdProof.device)
	assert.NotZero(t, createdProof.inode)

	got, gotProof, found, err := store.read(t.Context())
	require.NoError(t, err)
	assert.True(t, found)
	if diff := cmp.Diff(record, got, generationRecordCmpOptions...); diff != "" {
		t.Errorf("stored generation record mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(createdProof, gotProof, cmp.AllowUnexported(generationRecordFileEvidence{})); diff != "" {
		t.Errorf("generation record evidence mismatch (-want +got):\n%s", diff)
	}
	assertGenerationRecordExactMode(t, filepath.Join(root, "var/lib/mvmctl"), 0700)
	assertGenerationRecordExactMode(t, filepath.Join(root, "var/lib/mvmctl/firewall"), 0700)
	assertGenerationRecordExactMode(t, generationRecordTestPath(root), 0600)
}

// Rationale: /var/lib/mvmctl may intentionally start on a different local filesystem. Traversal may cross mounts
// while pinning that managed root, but every descendant must remain on the pinned managed filesystem.
func TestGenerationRecordStoreAllowsManagedRootMountCrossingAndRejectsDescendantCrossing(t *testing.T) {
	t.Parallel()

	t.Run("ancestor and managed root crossings are allowed", func(t *testing.T) {
		t.Parallel()
		root := prepareGenerationRecordTestRoot(t)
		deps := realGenerationRecordStoreDeps()
		realOpenAt2 := deps.openAt2
		var protectedDescendant atomic.Bool
		deps.openAt2 = func(ctx context.Context, parentFD int, name string, how *unix.OpenHow) (int, error) {
			if name != "firewall" && how.Resolve&unix.RESOLVE_NO_XDEV != 0 {
				return -1, unix.EXDEV
			}
			if name == "firewall" {
				protectedDescendant.Store(how.Resolve&unix.RESOLVE_NO_XDEV != 0)
			}
			return realOpenAt2(ctx, parentFD, name, how)
		}

		store, err := openGenerationRecordStore(t.Context(), deps, testGenerationRecordStorePolicy(root))
		require.NoError(t, err)
		require.NoError(t, store.Release(t.Context()))
		assert.True(t, protectedDescendant.Load())
	})

	t.Run("crossing below managed root is rejected", func(t *testing.T) {
		t.Parallel()
		root := prepareGenerationRecordTestRoot(t)
		deps := realGenerationRecordStoreDeps()
		realOpenAt2 := deps.openAt2
		deps.openAt2 = func(ctx context.Context, parentFD int, name string, how *unix.OpenHow) (int, error) {
			if name == "firewall" && how.Resolve&unix.RESOLVE_NO_XDEV != 0 {
				return -1, unix.EXDEV
			}
			return realOpenAt2(ctx, parentFD, name, how)
		}

		store, err := openGenerationRecordStore(t.Context(), deps, testGenerationRecordStorePolicy(root))
		assert.Nil(t, store)
		require.ErrorIs(t, err, unix.EXDEV)
		_, statErr := os.Lstat(filepath.Join(root, "var/lib/mvmctl/firewall"))
		assert.ErrorIs(t, statErr, os.ErrNotExist)
	})
}

func TestGenerationRecordStoreRejectsUnsafeRecordWithoutRepair(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*testing.T, string){
		"symlink": func(t *testing.T, recordPath string) {
			require.NoError(t, os.Symlink("attacker", recordPath))
		},
		"wrong mode": func(t *testing.T, recordPath string) {
			writeGenerationRecordFixture(t, recordPath, testCommittedGenerationRecord(t))
			require.NoError(t, os.Chmod(recordPath, 0644))
		},
		"hard link": func(t *testing.T, recordPath string) {
			source := filepath.Join(filepath.Dir(recordPath), "foreign")
			writeGenerationRecordFixture(t, source, testCommittedGenerationRecord(t))
			require.NoError(t, os.Link(source, recordPath))
		},
		"corrupt JSON": func(t *testing.T, recordPath string) {
			require.NoError(t, os.WriteFile(recordPath, []byte(`{"schema_version":1}`), 0600))
		},
		"oversized": func(t *testing.T, recordPath string) {
			require.NoError(t, os.WriteFile(recordPath, make([]byte, maxGenerationRecordBytes+1), 0600))
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := prepareGenerationRecordTestRoot(t)
			store := openTestGenerationRecordStore(t, root)
			t.Cleanup(func() { require.NoError(t, store.Release(context.Background())) })
			recordPath := generationRecordTestPath(root)
			mutate(t, recordPath)

			_, _, _, err := store.read(t.Context())
			require.Error(t, err)
			if name == "wrong mode" {
				info, statErr := os.Lstat(recordPath)
				require.NoError(t, statErr)
				assert.Equal(t, os.FileMode(0644), info.Mode().Perm())
			}
		})
	}
}

func TestGenerationRecordStoreRejectsWrongRecordOwnerWithoutRepair(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*unix.Stat_t){
		"wrong UID": func(stat *unix.Stat_t) { stat.Uid++ },
		"wrong GID": func(stat *unix.Stat_t) { stat.Gid++ },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := prepareGenerationRecordTestRoot(t)
			seed := openTestGenerationRecordStore(t, root)
			_, err := seed.create(t.Context(), testCommittedGenerationRecord(t))
			require.NoError(t, err)
			require.NoError(t, seed.Release(t.Context()))

			deps := realGenerationRecordStoreDeps()
			realFstat := deps.fstat
			deps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
				if err := realFstat(ctx, fd, stat); err != nil {
					return err
				}
				if stat.Mode&unix.S_IFMT == unix.S_IFREG && stat.Size > 0 {
					mutate(stat)
				}
				return nil
			}
			store, err := openGenerationRecordStore(t.Context(), deps, testGenerationRecordStorePolicy(root))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Release(context.Background())) })

			_, _, _, err = store.read(t.Context())
			require.Error(t, err)
			assert.ErrorContains(t, err, "unexpected owner or mode")
			assertGenerationAuthorityErrorMetadata(t, err)
		})
	}
}

func TestGenerationRecordStoreRejectsWrongManagedDirectoryOwnerWithoutRepair(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*unix.Stat_t){
		"wrong UID": func(stat *unix.Stat_t) { stat.Uid++ },
		"wrong GID": func(stat *unix.Stat_t) { stat.Gid++ },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := prepareGenerationRecordTestRoot(t)
			managedRoot := filepath.Join(root, "var/lib/mvmctl")
			require.NoError(t, os.MkdirAll(filepath.Join(managedRoot, "firewall"), 0700))
			deps := realGenerationRecordStoreDeps()
			realFstat := deps.fstat
			var directories atomic.Int32
			deps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
				if err := realFstat(ctx, fd, stat); err != nil {
					return err
				}
				if stat.Mode&unix.S_IFMT == unix.S_IFDIR && directories.Add(1) == 5 {
					mutate(stat)
				}
				return nil
			}

			store, err := openGenerationRecordStore(t.Context(), deps, testGenerationRecordStorePolicy(root))
			assert.Nil(t, store)
			require.Error(t, err)
			assertGenerationAuthorityErrorMetadata(t, err)
			assertGenerationRecordExactMode(t, managedRoot, 0700)
		})
	}
}

func TestGenerationRecordStoreRejectsUnsafeManagedDirectoryWithoutRepair(t *testing.T) {
	t.Parallel()

	root := prepareGenerationRecordTestRoot(t)
	managedPath := filepath.Join(root, "var/lib/mvmctl/firewall")
	require.NoError(t, os.MkdirAll(managedPath, 0755))

	store, err := openGenerationRecordStore(
		t.Context(),
		realGenerationRecordStoreDeps(),
		testGenerationRecordStorePolicy(root),
	)
	assert.Nil(t, store)
	require.Error(t, err)
	info, statErr := os.Lstat(managedPath)
	require.NoError(t, statErr)
	assert.Equal(t, os.FileMode(0755), info.Mode().Perm())
}

// Rationale: private firewall metadata must coexist with guest markers beneath a safe shared state base.
func TestGenerationRecordStoreSharedStateBaseAndPrivateDirectories(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		baseMode   os.FileMode
		wrongOwner bool
		wantErr    bool
	}{
		"writable shared base":    {baseMode: 0775, wantErr: true},
		"unexpected shared owner": {baseMode: 0755, wrongOwner: true, wantErr: true},
		"existing shared base":    {baseMode: 0755},
		"missing shared base":     {},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := prepareGenerationRecordTestRoot(t)
			base := filepath.Join(root, "var/lib/mvmctl")
			private := filepath.Join(base, "firewall")
			var before unix.Stat_t
			if tc.baseMode != 0 {
				require.NoError(t, os.Mkdir(base, tc.baseMode))
				require.NoError(t, os.Chmod(base, tc.baseMode))
				require.NoError(t, unix.Lstat(base, &before))
			}
			deps := realGenerationRecordStoreDeps()
			realFstat := deps.fstat
			deps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
				if err := realFstat(ctx, fd, stat); err != nil {
					return err
				}
				if tc.wrongOwner && stat.Ino == before.Ino && stat.Dev == before.Dev {
					stat.Uid++
				}
				return nil
			}
			store, err := openGenerationRecordStore(t.Context(), deps, testGenerationRecordStorePolicy(root))
			if tc.wantErr {
				require.Error(t, err)
				assert.Nil(t, store)
				assert.NoDirExists(t, private)
			} else {
				require.NoError(t, err)
				require.NoError(t, store.Release(t.Context()))
				assertGenerationRecordExactMode(t, private, 0700)
			}
			var after unix.Stat_t
			require.NoError(t, unix.Lstat(base, &after))
			if tc.baseMode != 0 {
				assert.Equal(t, before.Ino, after.Ino)
				assert.Equal(t, before.Mode, after.Mode)
				assert.Equal(t, before.Uid, after.Uid)
				assert.Equal(t, before.Gid, after.Gid)
			} else {
				assert.Equal(t, uint32(0700), after.Mode&07777)
				assert.Equal(t, uint32(os.Geteuid()), after.Uid)
				assert.Equal(t, uint32(os.Getegid()), after.Gid)
			}
		})
	}
}

func TestHostResetRecordStorePreservesSharedBaseAndAbsence(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"missing base", "missing private", "existing private", "unsafe private"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			root := prepareGenerationRecordTestRoot(t)
			base := filepath.Join(root, "var/lib/mvmctl")
			private := filepath.Join(base, "firewall")
			if state != "missing base" {
				require.NoError(t, os.Mkdir(base, 0755))
			}
			if state == "existing private" {
				require.NoError(t, os.Mkdir(private, 0700))
			}
			if state == "unsafe private" {
				require.NoError(t, os.Mkdir(private, 0755))
			}
			store, absence, err := openHostResetRecordStoreWithDeps(
				t.Context(), realGenerationRecordStoreDeps(), testGenerationRecordStorePolicy(root),
			)
			if state == "unsafe private" {
				require.Error(t, err)
				assert.Nil(t, store)
				assert.Nil(t, absence)
				assertGenerationRecordExactMode(t, private, 0755)
				return
			}
			require.NoError(t, err)
			if state == "existing private" {
				require.NotNil(t, store)
				assert.Nil(t, absence)
				require.NoError(t, store.Release(t.Context()))
			} else {
				assert.Nil(t, store)
				require.NotNil(t, absence)
				wantName := "firewall"
				if state == "missing base" {
					wantName = "mvmctl"
				}
				assert.Equal(t, wantName, absence.name)
				require.NoError(t, closeGenerationRecordFDs(t.Context(), absence.deps, absence.retained))
				assert.NoDirExists(t, private)
			}
			if state == "missing base" {
				assert.NoDirExists(t, base)
			} else {
				assertGenerationRecordExactMode(t, base, 0755)
			}
		})
	}
}

func TestGenerationRecordStoreNeverOverwritesExistingOrRacedState(t *testing.T) {
	t.Parallel()

	t.Run("existing foreign record", func(t *testing.T) {
		t.Parallel()
		root := prepareGenerationRecordTestRoot(t)
		store := openTestGenerationRecordStore(t, root)
		t.Cleanup(func() { require.NoError(t, store.Release(context.Background())) })
		foreign := mustCommittedGenerationRecord(
			t,
			testGenerationRecordBinding(),
			generationRecordIdentity{id: testFirewallGenerationB},
		)
		writeGenerationRecordFixture(t, generationRecordTestPath(root), foreign)

		_, err := store.create(t.Context(), testCommittedGenerationRecord(t))
		require.Error(t, err)
		got := readGenerationRecordFixture(t, generationRecordTestPath(root))
		if diff := cmp.Diff(foreign, got, generationRecordCmpOptions...); diff != "" {
			t.Errorf("foreign generation record changed (-want +got):\n%s", diff)
		}
	})

	t.Run("foreign record wins publish race", func(t *testing.T) {
		t.Parallel()
		root := prepareGenerationRecordTestRoot(t)
		seed := openTestGenerationRecordStore(t, root)
		require.NoError(t, seed.Release(context.Background()))
		foreign := mustCommittedGenerationRecord(
			t,
			testGenerationRecordBinding(),
			generationRecordIdentity{id: testFirewallGenerationB},
		)
		deps := realGenerationRecordStoreDeps()
		realRename := deps.renameNoReplace
		deps.renameNoReplace = func(ctx context.Context, oldFD int, oldName string, newFD int, newName string) error {
			writeGenerationRecordFixture(t, generationRecordTestPath(root), foreign)
			return realRename(ctx, oldFD, oldName, newFD, newName)
		}
		store, err := openGenerationRecordStore(t.Context(), deps, testGenerationRecordStorePolicy(root))
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, store.Release(context.Background())) })

		_, err = store.create(t.Context(), testCommittedGenerationRecord(t))
		require.Error(t, err)
		assert.ErrorIs(t, err, unix.EEXIST)
		got := readGenerationRecordFixture(t, generationRecordTestPath(root))
		if diff := cmp.Diff(foreign, got, generationRecordCmpOptions...); diff != "" {
			t.Errorf("raced generation record changed (-want +got):\n%s", diff)
		}
		assertNoGenerationRecordTemps(t, root)
	})
}

func TestGenerationRecordStoreCreationFaultsHaveClosedOutcomes(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		inject      func(*generationRecordStoreDeps)
		wantCreated bool
	}{
		"write": {
			inject: func(deps *generationRecordStoreDeps) {
				deps.write = func(context.Context, int, []byte) (int, error) {
					return 0, errors.New("injected write failure")
				}
			},
		},
		"publish": {
			inject: func(deps *generationRecordStoreDeps) {
				deps.renameNoReplace = func(context.Context, int, string, int, string) error {
					return errors.New("injected publish failure")
				}
			},
		},
		"directory sync": {
			inject: func(deps *generationRecordStoreDeps) {
				realFsync := deps.fsync
				var calls atomic.Int32
				deps.fsync = func(ctx context.Context, fd int) error {
					if calls.Add(1) == 2 {
						return errors.New("injected directory sync failure")
					}
					return realFsync(ctx, fd)
				}
			},
			wantCreated: true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := prepareGenerationRecordTestRoot(t)
			seed := openTestGenerationRecordStore(t, root)
			require.NoError(t, seed.Release(t.Context()))
			deps := realGenerationRecordStoreDeps()
			tc.inject(&deps)
			store, err := openGenerationRecordStore(t.Context(), deps, testGenerationRecordStorePolicy(root))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Release(context.Background())) })

			proof, err := store.create(t.Context(), testCommittedGenerationRecord(t))
			require.Error(t, err)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			created, _ := domainErr.Details["record_created"].(bool)
			assert.Equal(t, tc.wantCreated, created)
			if tc.wantCreated {
				assert.NotZero(t, proof.device)
				assert.NotZero(t, proof.inode)
				assert.Equal(t, true, domainErr.Details["durability_uncertain"])
			} else {
				assert.Zero(t, proof.device)
				assert.Zero(t, proof.inode)
			}
			_, _, found, readErr := store.read(t.Context())
			require.NoError(t, readErr)
			assert.Equal(t, tc.wantCreated, found)
			assertNoGenerationRecordTemps(t, root)
		})
	}
}

func TestGenerationRecordStoreSetsExactRecordOwnerAndClassifiesFchownFailure(t *testing.T) {
	t.Parallel()

	t.Run("sets policy owner", func(t *testing.T) {
		t.Parallel()
		root := prepareGenerationRecordTestRoot(t)
		seed := openTestGenerationRecordStore(t, root)
		require.NoError(t, seed.Release(t.Context()))
		policy := testGenerationRecordStorePolicy(root)
		deps := realGenerationRecordStoreDeps()
		realFchown := deps.fchown
		var calls atomic.Int32
		deps.fchown = func(ctx context.Context, fd, uid, gid int) error {
			calls.Add(1)
			assert.Equal(t, int(policy.expectedUID), uid)
			assert.Equal(t, int(policy.expectedGID), gid)
			return realFchown(ctx, fd, uid, gid)
		}
		store, err := openGenerationRecordStore(t.Context(), deps, policy)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, store.Release(context.Background())) })

		proof, err := store.create(t.Context(), testCommittedGenerationRecord(t))
		require.NoError(t, err)
		assert.Equal(t, int32(1), calls.Load())
		assert.Equal(t, policy.expectedUID, proof.uid)
		assert.Equal(t, policy.expectedGID, proof.gid)
	})

	t.Run("failure remains precommit and classified", func(t *testing.T) {
		t.Parallel()
		root := prepareGenerationRecordTestRoot(t)
		seed := openTestGenerationRecordStore(t, root)
		require.NoError(t, seed.Release(t.Context()))
		policy := testGenerationRecordStorePolicy(root)
		deps := realGenerationRecordStoreDeps()
		fault := errors.New("injected fchown failure")
		var calls atomic.Int32
		deps.fchown = func(_ context.Context, _ int, uid, gid int) error {
			calls.Add(1)
			assert.Equal(t, int(policy.expectedUID), uid)
			assert.Equal(t, int(policy.expectedGID), gid)
			return fault
		}
		store, err := openGenerationRecordStore(t.Context(), deps, policy)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, store.Release(context.Background())) })

		proof, err := store.create(t.Context(), testCommittedGenerationRecord(t))
		require.ErrorIs(t, err, fault)
		assert.Equal(t, int32(1), calls.Load())
		assert.Zero(t, proof)
		assertGenerationAuthorityErrorMetadata(t, err)
		domainErr := errs.AsDomainError(err)
		require.NotNil(t, domainErr)
		_, created := domainErr.Details["record_created"]
		assert.False(t, created)
		_, statErr := os.Lstat(generationRecordTestPath(root))
		assert.ErrorIs(t, statErr, os.ErrNotExist)
		assertNoGenerationRecordTemps(t, root)
	})
}

func TestGenerationRecordStoreConcurrentCreatePublishesOnce(t *testing.T) {
	t.Parallel()

	root := prepareGenerationRecordTestRoot(t)
	store := openTestGenerationRecordStore(t, root)
	t.Cleanup(func() { require.NoError(t, store.Release(context.Background())) })
	record := testCommittedGenerationRecord(t)

	const callers = 24
	results := make(chan error, callers)
	var group sync.WaitGroup
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := store.create(t.Context(), record)
			results <- err
		}()
	}
	group.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
			continue
		}
		assert.ErrorContains(t, err, "already exists")
	}
	assert.Equal(t, 1, succeeded)
	got, _, found, err := store.read(t.Context())
	require.NoError(t, err)
	assert.True(t, found)
	if diff := cmp.Diff(record, got, generationRecordCmpOptions...); diff != "" {
		t.Errorf("concurrently created generation record mismatch (-want +got):\n%s", diff)
	}
	assertNoGenerationRecordTemps(t, root)
}

func TestGenerationRecordStoreDetectsUnstableReadEvidence(t *testing.T) {
	t.Parallel()

	root := prepareGenerationRecordTestRoot(t)
	seed := openTestGenerationRecordStore(t, root)
	_, err := seed.create(t.Context(), testCommittedGenerationRecord(t))
	require.NoError(t, err)
	require.NoError(t, seed.Release(t.Context()))

	deps := realGenerationRecordStoreDeps()
	realFstat := deps.fstat
	var recordStats atomic.Int32
	deps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
		if err := realFstat(ctx, fd, stat); err != nil {
			return err
		}
		if stat.Mode&unix.S_IFMT == unix.S_IFREG && stat.Size > 0 && recordStats.Add(1) == 2 {
			stat.Ino++
		}
		return nil
	}
	store, err := openGenerationRecordStore(t.Context(), deps, testGenerationRecordStorePolicy(root))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Release(context.Background())) })

	_, _, _, err = store.read(t.Context())
	require.Error(t, err)
	assert.ErrorContains(t, err, "changed while being read")
}

func TestGenerationRecordStoreReleaseIsSharedCachedAndCancellationIndependent(t *testing.T) {
	t.Parallel()

	type contextKey struct{}
	releaseCause := errors.New("injected close failure")
	var closeCalls atomic.Int32
	var cleanupContextSeen atomic.Bool
	store := &generationRecordStore{state: &generationRecordStoreState{
		deps: generationRecordStoreDeps{close: func(ctx context.Context, _ int) error {
			closeCalls.Add(1)
			cleanupContextSeen.Store(ctx.Err() == nil && ctx.Value(contextKey{}) == "retained")
			return releaseCause
		}},
		directory: 11,
		retained:  []int{11},
		active:    true,
	}}
	cancelled, cancel := context.WithCancel(context.WithValue(t.Context(), contextKey{}, "retained"))
	cancel()
	copyOfStore := *store

	const callers = 24
	results := make(chan error, callers)
	var group sync.WaitGroup
	for index := range callers {
		group.Add(1)
		go func(useCopy bool) {
			defer group.Done()
			if useCopy {
				results <- copyOfStore.Release(cancelled)
				return
			}
			results <- store.Release(cancelled)
		}(index%2 == 0)
	}
	group.Wait()
	close(results)
	var first error
	for releaseErr := range results {
		require.ErrorIs(t, releaseErr, releaseCause)
		if first == nil {
			first = releaseErr
		} else {
			assert.Same(t, first, releaseErr)
		}
	}
	assert.Equal(t, int32(1), closeCalls.Load())
	assert.True(t, cleanupContextSeen.Load())
}

func TestAppendGenerationCleanupErrorPreservesPrimaryAndFirstBoundedEvidence(t *testing.T) {
	t.Parallel()

	primaryCause := errors.New("primary cause")
	sibling := errors.New("primary sibling")
	primary := &errs.DomainError{
		Code: errs.CodeNetworkAlreadyExists, Message: "classified primary", Op: "firewall-admit",
		Entity: "generation-a", Class: errs.ClassConflict, Err: primaryCause,
		Details: map[string]any{"evidence": "original"},
	}
	outer := fmt.Errorf("outer wrapper: %w", errors.Join(primary, sibling))
	firstCleanup := errors.New(strings.Repeat("cleanup", maxGenerationCleanupDetailBytes))
	secondCleanup := errors.New("second cleanup")

	first := appendGenerationCleanupError(outer, "release record store", firstCleanup)
	second := appendGenerationCleanupError(first, "release firewall lock", secondCleanup)
	firstDomain := errs.AsDomainError(first)
	secondDomain := errs.AsDomainError(second)
	require.NotNil(t, firstDomain)
	require.NotNil(t, secondDomain)
	assert.NotSame(t, primary, firstDomain)
	assert.NotSame(t, firstDomain, secondDomain)
	for name, want := range map[string]error{
		"outer":          outer,
		"domain":         primary,
		"primary cause":  primaryCause,
		"sibling":        sibling,
		"first cleanup":  firstCleanup,
		"second cleanup": secondCleanup,
	} {
		t.Run(name, func(t *testing.T) { assert.ErrorIs(t, second, want) })
	}
	assert.Equal(t, primary.Code, secondDomain.Code)
	assert.Equal(t, primary.Message, secondDomain.Message)
	assert.Equal(t, primary.Op, secondDomain.Op)
	assert.Equal(t, primary.Entity, secondDomain.Entity)
	assert.Equal(t, primary.Class, secondDomain.Class)
	assert.Equal(t, map[string]any{"evidence": "original"}, primary.Details)
	wantDetail := "release record store: " + firstCleanup.Error()
	wantDetail = wantDetail[:maxGenerationCleanupDetailBytes-3] + "..."
	wantDetails := map[string]any{
		"evidence":                           "original",
		generationCleanupFailedDetailKey:     true,
		generationCleanupDiagnosticDetailKey: wantDetail,
	}
	if diff := cmp.Diff(wantDetails, secondDomain.Details); diff != "" {
		t.Errorf("cleanup details mismatch (-want +got):\n%s", diff)
	}
}

func prepareGenerationRecordTestRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	// TempDir's numbered child inherits the process umask; the store requires a non-writable trusted root.
	require.NoError(t, os.Chmod(root, 0700))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "var/lib"), 0755))
	return root
}

func testGenerationRecordStorePolicy(root string) generationRecordStorePolicy {
	return generationRecordStorePolicy{
		rootPath: root, expectedUID: uint32(os.Getuid()), expectedGID: uint32(os.Getgid()),
	}
}

func openTestGenerationRecordStore(t *testing.T, root string) *generationRecordStore {
	t.Helper()
	store, err := openGenerationRecordStore(
		t.Context(),
		realGenerationRecordStoreDeps(),
		testGenerationRecordStorePolicy(root),
	)
	require.NoError(t, err)
	return store
}

func testCommittedGenerationRecord(t *testing.T) generationRecord {
	t.Helper()
	return mustCommittedGenerationRecord(
		t,
		testGenerationRecordBinding(),
		generationRecordIdentity{empty: true, id: testFirewallGenerationA},
	)
}

func generationRecordTestPath(root string) string {
	return filepath.Join(root, "var/lib/mvmctl/firewall/generation.json")
}

func writeGenerationRecordFixture(t *testing.T, recordPath string, record generationRecord) {
	t.Helper()
	raw, err := encodeGenerationRecord(record)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(recordPath, raw, 0600))
	require.NoError(t, os.Chmod(recordPath, 0600))
}

func readGenerationRecordFixture(t *testing.T, recordPath string) generationRecord {
	t.Helper()
	raw, err := os.ReadFile(recordPath)
	require.NoError(t, err)
	record, err := decodeGenerationRecord(raw)
	require.NoError(t, err)
	return record
}

func assertGenerationRecordExactMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	require.NoError(t, err)
	assert.Equal(t, want, info.Mode().Perm())
}

func assertNoGenerationRecordTemps(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(generationRecordTestPath(root)))
	require.NoError(t, err)
	for _, entry := range entries {
		assert.False(t, generationRecordTempNamePattern.MatchString(entry.Name()), "temporary record leaked")
	}
}

func assertGenerationAuthorityErrorMetadata(t *testing.T, err error) {
	t.Helper()
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeNetworkFirewallFailed, domainErr.Code)
	assert.Equal(t, errs.ClassInternal, domainErr.Class)
}
