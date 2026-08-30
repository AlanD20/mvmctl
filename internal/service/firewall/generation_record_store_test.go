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

func TestGenerationRecordStoreRejectsUnsafeManagedDirectoryWithoutRepair(t *testing.T) {
	t.Parallel()

	root := prepareGenerationRecordTestRoot(t)
	managedPath := filepath.Join(root, "var/lib/mvmctl")
	require.NoError(t, os.Mkdir(managedPath, 0755))

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
