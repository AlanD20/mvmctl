package jailer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

// Rationale: a stable absence proof is meaningful only while the same per-VM authority lock excludes a concurrent
// launch or cleanup. The typed composition seam must reject an inactive locked-instance shell before filesystem I/O.
func TestLockedInstanceRequireFreshRecoveryManagedVMCgroupAbsentRequiresActiveVMLock(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	deps := fixture.deps()
	openCalls := 0
	deps.openRoot = func(context.Context) (int, error) {
		openCalls++
		return -1, errors.New("unexpected cgroup root open")
	}
	locked := &lockedInstance{record: instanceRecord{vmID: testVMID}}

	err := locked.requireFreshRecoveryManagedVMCgroupAbsent(
		t.Context(),
		managedVMCgroupReadDepsFrom(deps),
	)
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMStateInvalid, errs.AsDomainError(err).Code)
	assert.Zero(t, openCalls)
}

// Rationale: reboot recovery may observe an absent cgroup hierarchy, but it must never recreate any part of that
// ephemeral hierarchy while proving the durable VM no longer has a cgroup.
func TestRequireFreshRecoveryManagedVMCgroupAbsentAcceptsMissingHierarchyWithoutMutation(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		addParent bool
	}{
		"missing parent": {},
		"missing leaf":   {addParent: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newManagedVMCgroupTestFixture(t)
			if tc.addParent {
				require.NoError(t, os.Mkdir(
					filepath.Join(fixture.root, infra.JailerCgroupParent),
					os.FileMode(managedVMCgroupParentMode),
				))
			}
			before := snapshotManagedVMCgroupRecoveryTree(t, fixture.root)
			probe := newManagedVMCgroupAbsenceProbe(fixture)

			err := requireFreshRecoveryManagedVMCgroupAbsent(
				t.Context(),
				managedVMCgroupReadDepsFrom(probe.deps),
				testVMID,
			)
			require.NoError(t, err)
			probe.assertReadOnly(t)
			if diff := cmp.Diff(before, snapshotManagedVMCgroupRecoveryTree(t, fixture.root)); diff != "" {
				t.Errorf("managed cgroup tree changed (-before +after):\n%s", diff)
			}
			wantOpened := []string{"root", "root"}
			if tc.addParent {
				wantOpened = []string{"root", "parent", "root", "parent"}
			}
			if diff := cmp.Diff(wantOpened, probe.openedRoles); diff != "" {
				t.Errorf("opened descriptor roles mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(reverseInts(probe.openedFDs), probe.closedFDs); diff != "" {
				t.Errorf("closed descriptor order mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRequireFreshRecoveryManagedVMCgroupAbsentNeedsNoMutationOrMembershipDependencies(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	deps := fixture.deps()
	deps.mkdirAt = nil
	deps.fchown = nil
	deps.fchmod = nil
	deps.pread = nil
	deps.unlinkAt = nil

	require.NoError(t, requireFreshRecoveryManagedVMCgroupAbsent(
		t.Context(),
		managedVMCgroupReadDepsFrom(deps),
		testVMID,
	))
}

// Rationale: ENOENT is authoritative only when both observations agree. An appeared or disappeared fixed parent or
// leaf is ambiguous concurrent state and must fail closed without repairing or removing either side of the race.
func TestRequireFreshRecoveryManagedVMCgroupAbsentRejectsAppearedOrDisappearedHierarchy(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		prepare func(*testing.T, *managedVMCgroupTestFixture)
		hook    func(*testing.T, *managedVMCgroupTestFixture, *managedVMCgroupAbsenceProbe)
	}{
		"parent appeared": {
			hook: func(t *testing.T, fixture *managedVMCgroupTestFixture, probe *managedVMCgroupAbsenceProbe) {
				realOpenAt2 := probe.deps.openAt2
				parentAttempts := 0
				probe.deps.openAt2 = func(
					ctx context.Context,
					fd int,
					name string,
					how *unix.OpenHow,
				) (int, error) {
					if name == infra.JailerCgroupParent {
						parentAttempts++
						if parentAttempts == 2 {
							require.NoError(t, os.Mkdir(
								filepath.Join(fixture.root, infra.JailerCgroupParent),
								0o755,
							))
						}
					}
					return realOpenAt2(ctx, fd, name, how)
				}
			},
		},
		"parent disappeared": {
			prepare: func(t *testing.T, fixture *managedVMCgroupTestFixture) {
				require.NoError(t, os.Mkdir(filepath.Join(fixture.root, infra.JailerCgroupParent), 0o755))
			},
			hook: func(t *testing.T, fixture *managedVMCgroupTestFixture, probe *managedVMCgroupAbsenceProbe) {
				realOpenAt2 := probe.deps.openAt2
				parentAttempts := 0
				probe.deps.openAt2 = func(
					ctx context.Context,
					fd int,
					name string,
					how *unix.OpenHow,
				) (int, error) {
					if name == infra.JailerCgroupParent {
						parentAttempts++
						if parentAttempts == 2 {
							require.NoError(t, os.Remove(filepath.Join(fixture.root, infra.JailerCgroupParent)))
						}
					}
					return realOpenAt2(ctx, fd, name, how)
				}
			},
		},
		"leaf appeared": {
			prepare: func(t *testing.T, fixture *managedVMCgroupTestFixture) {
				require.NoError(t, os.Mkdir(filepath.Join(fixture.root, infra.JailerCgroupParent), 0o755))
			},
			hook: func(t *testing.T, fixture *managedVMCgroupTestFixture, probe *managedVMCgroupAbsenceProbe) {
				realOpenAt2 := probe.deps.openAt2
				leafAttempts := 0
				probe.deps.openAt2 = func(
					ctx context.Context,
					fd int,
					name string,
					how *unix.OpenHow,
				) (int, error) {
					if name == testVMID {
						leafAttempts++
						if leafAttempts == 2 {
							require.NoError(t, os.Mkdir(fixture.vmLeafPath(testVMID), 0o755))
						}
					}
					return realOpenAt2(ctx, fd, name, how)
				}
			},
		},
		"leaf disappeared": {
			prepare: func(_ *testing.T, fixture *managedVMCgroupTestFixture) { fixture.addVMLeaf(testVMID) },
			hook: func(t *testing.T, fixture *managedVMCgroupTestFixture, probe *managedVMCgroupAbsenceProbe) {
				realOpenAt2 := probe.deps.openAt2
				leafAttempts := 0
				probe.deps.openAt2 = func(
					ctx context.Context,
					fd int,
					name string,
					how *unix.OpenHow,
				) (int, error) {
					if name == testVMID {
						leafAttempts++
						if leafAttempts == 2 {
							require.NoError(t, os.Remove(filepath.Join(fixture.vmLeafPath(testVMID), "cgroup.procs")))
							require.NoError(t, os.Remove(fixture.vmLeafPath(testVMID)))
						}
					}
					return realOpenAt2(ctx, fd, name, how)
				}
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newManagedVMCgroupTestFixture(t)
			if tc.prepare != nil {
				tc.prepare(t, fixture)
			}
			probe := newManagedVMCgroupAbsenceProbe(fixture)
			tc.hook(t, fixture, probe)

			err := requireFreshRecoveryManagedVMCgroupAbsent(
				t.Context(),
				managedVMCgroupReadDepsFrom(probe.deps),
				testVMID,
			)
			require.Error(t, err)
			assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
			probe.assertReadOnly(t)
			if diff := cmp.Diff(reverseInts(probe.openedFDs), probe.closedFDs); diff != "" {
				t.Errorf("closed descriptor order mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// Rationale: a same-name leaf can belong to an interrupted or foreign lifecycle. Absence recovery must reject even an
// otherwise exact leaf without reading membership, adopting it, or returning cleanup authority.
func TestRequireFreshRecoveryManagedVMCgroupAbsentRejectsPresentExactLeafWithoutAdoption(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	fixture.addVMLeaf(testVMID)
	before := snapshotManagedVMCgroupRecoveryTree(t, fixture.root)
	probe := newManagedVMCgroupAbsenceProbe(fixture)

	err := requireFreshRecoveryManagedVMCgroupAbsent(
		t.Context(),
		managedVMCgroupReadDepsFrom(probe.deps),
		testVMID,
	)
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMStateInvalid, errs.AsDomainError(err).Code)
	probe.assertReadOnly(t)
	if diff := cmp.Diff(before, snapshotManagedVMCgroupRecoveryTree(t, fixture.root)); diff != "" {
		t.Errorf("managed cgroup tree changed (-before +after):\n%s", diff)
	}
	if diff := cmp.Diff(
		[]string{"root", "parent", "leaf", "root", "parent", "leaf"},
		probe.openedRoles,
	); diff != "" {
		t.Errorf("opened descriptor roles mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(reverseInts(probe.openedFDs), probe.closedFDs); diff != "" {
		t.Errorf("closed descriptor order mismatch (-want +got):\n%s", diff)
	}
}

func TestRequireFreshRecoveryManagedVMCgroupAbsentRejectsUnsafeHierarchyWithoutMutation(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		addLeaf   bool
		configure func(*managedVMCgroupTestFixture, *managedVMCgroupAbsenceProbe)
	}{
		"root owner": {configure: func(_ *managedVMCgroupTestFixture, probe *managedVMCgroupAbsenceProbe) {
			probe.rewriteStatx("root", func(stat *unix.Statx_t) { stat.Uid++ })
		}},
		"root mode": {configure: func(fixture *managedVMCgroupTestFixture, _ *managedVMCgroupAbsenceProbe) {
			require.NoError(t, os.Chmod(fixture.root, 0o750))
		}},
		"root type": {configure: func(_ *managedVMCgroupTestFixture, probe *managedVMCgroupAbsenceProbe) {
			probe.rewriteStatx("root", func(stat *unix.Statx_t) {
				stat.Mode = stat.Mode&^unix.S_IFMT | unix.S_IFREG
			})
		}},
		"root filesystem": {configure: func(_ *managedVMCgroupTestFixture, probe *managedVMCgroupAbsenceProbe) {
			probe.deps.statfs = func(context.Context, int, *unix.Statfs_t) error { return nil }
		}},
		"parent owner": {configure: func(_ *managedVMCgroupTestFixture, probe *managedVMCgroupAbsenceProbe) {
			probe.rewriteStatx("parent", func(stat *unix.Statx_t) { stat.Gid++ })
		}},
		"parent mode": {configure: func(fixture *managedVMCgroupTestFixture, _ *managedVMCgroupAbsenceProbe) {
			require.NoError(t, os.Chmod(filepath.Join(fixture.root, infra.JailerCgroupParent), 0o700))
		}},
		"parent type": {configure: func(_ *managedVMCgroupTestFixture, probe *managedVMCgroupAbsenceProbe) {
			probe.rewriteStatx("parent", func(stat *unix.Statx_t) {
				stat.Mode = stat.Mode&^unix.S_IFMT | unix.S_IFREG
			})
		}},
		"parent mount": {configure: func(_ *managedVMCgroupTestFixture, probe *managedVMCgroupAbsenceProbe) {
			probe.rewriteStatx("parent", func(stat *unix.Statx_t) { stat.Mnt_id++ })
		}},
		"parent device": {configure: func(_ *managedVMCgroupTestFixture, probe *managedVMCgroupAbsenceProbe) {
			probe.rewriteStatx("parent", func(stat *unix.Statx_t) { stat.Dev_minor++ })
		}},
		"leaf owner": {
			addLeaf: true,
			configure: func(_ *managedVMCgroupTestFixture, probe *managedVMCgroupAbsenceProbe) {
				probe.rewriteStatx("leaf", func(stat *unix.Statx_t) { stat.Uid++ })
			},
		},
		"leaf mode": {
			addLeaf: true,
			configure: func(fixture *managedVMCgroupTestFixture, _ *managedVMCgroupAbsenceProbe) {
				require.NoError(t, os.Chmod(fixture.vmLeafPath(testVMID), 0o755))
			},
		},
		"leaf type": {
			addLeaf: true,
			configure: func(_ *managedVMCgroupTestFixture, probe *managedVMCgroupAbsenceProbe) {
				probe.rewriteStatx("leaf", func(stat *unix.Statx_t) {
					stat.Mode = stat.Mode&^unix.S_IFMT | unix.S_IFREG
				})
			},
		},
		"leaf mount": {
			addLeaf: true,
			configure: func(_ *managedVMCgroupTestFixture, probe *managedVMCgroupAbsenceProbe) {
				probe.rewriteStatx("leaf", func(stat *unix.Statx_t) { stat.Mnt_id++ })
			},
		},
		"leaf device": {
			addLeaf: true,
			configure: func(_ *managedVMCgroupTestFixture, probe *managedVMCgroupAbsenceProbe) {
				probe.rewriteStatx("leaf", func(stat *unix.Statx_t) { stat.Dev_major++ })
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newManagedVMCgroupTestFixture(t)
			if tc.addLeaf {
				fixture.addVMLeaf(testVMID)
			} else {
				require.NoError(t, os.Mkdir(
					filepath.Join(fixture.root, infra.JailerCgroupParent),
					os.FileMode(managedVMCgroupParentMode),
				))
			}
			probe := newManagedVMCgroupAbsenceProbe(fixture)
			tc.configure(fixture, probe)
			before := snapshotManagedVMCgroupRecoveryTree(t, fixture.root)

			err := requireFreshRecoveryManagedVMCgroupAbsent(
				t.Context(),
				managedVMCgroupReadDepsFrom(probe.deps),
				testVMID,
			)
			require.Error(t, err)
			assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
			probe.assertReadOnly(t)
			if diff := cmp.Diff(before, snapshotManagedVMCgroupRecoveryTree(t, fixture.root)); diff != "" {
				t.Errorf("managed cgroup tree changed (-before +after):\n%s", diff)
			}
			if diff := cmp.Diff(reverseInts(probe.openedFDs), probe.closedFDs); diff != "" {
				t.Errorf("closed descriptor order mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// Rationale: the first observation cannot bless the second. Every re-opened component must independently pass exact
// metadata and filesystem checks before its identity may confirm stable absence or presence.
func TestRequireFreshRecoveryManagedVMCgroupAbsentRejectsSecondObservationUnsafeState(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		addLeaf   bool
		configure func(*managedVMCgroupAbsenceProbe)
	}{
		"root owner": {configure: func(probe *managedVMCgroupAbsenceProbe) {
			probe.rewriteStatxCall("root", 2, func(stat *unix.Statx_t) { stat.Uid++ })
		}},
		"root mode": {configure: func(probe *managedVMCgroupAbsenceProbe) {
			probe.rewriteStatxCall("root", 2, func(stat *unix.Statx_t) {
				stat.Mode = stat.Mode&^0o7777 | 0o700
			})
		}},
		"root type": {configure: func(probe *managedVMCgroupAbsenceProbe) {
			probe.rewriteStatxCall("root", 2, func(stat *unix.Statx_t) {
				stat.Mode = stat.Mode&^unix.S_IFMT | unix.S_IFREG
			})
		}},
		"root filesystem": {configure: func(probe *managedVMCgroupAbsenceProbe) {
			realStatfs := probe.deps.statfs
			calls := 0
			probe.deps.statfs = func(ctx context.Context, fd int, stat *unix.Statfs_t) error {
				calls++
				if calls == 2 {
					return nil
				}
				return realStatfs(ctx, fd, stat)
			}
		}},
		"root stat": {configure: func(probe *managedVMCgroupAbsenceProbe) {
			probe.failStatx("root", 2)
		}},
		"parent owner": {configure: func(probe *managedVMCgroupAbsenceProbe) {
			probe.rewriteStatxCall("parent", 2, func(stat *unix.Statx_t) { stat.Gid++ })
		}},
		"parent mode": {configure: func(probe *managedVMCgroupAbsenceProbe) {
			probe.rewriteStatxCall("parent", 2, func(stat *unix.Statx_t) {
				stat.Mode = stat.Mode&^0o7777 | 0o700
			})
		}},
		"parent type": {configure: func(probe *managedVMCgroupAbsenceProbe) {
			probe.rewriteStatxCall("parent", 2, func(stat *unix.Statx_t) {
				stat.Mode = stat.Mode&^unix.S_IFMT | unix.S_IFREG
			})
		}},
		"parent stat": {configure: func(probe *managedVMCgroupAbsenceProbe) {
			probe.failStatx("parent", 2)
		}},
		"leaf owner": {addLeaf: true, configure: func(probe *managedVMCgroupAbsenceProbe) {
			probe.rewriteStatxCall("leaf", 2, func(stat *unix.Statx_t) { stat.Uid++ })
		}},
		"leaf mode": {addLeaf: true, configure: func(probe *managedVMCgroupAbsenceProbe) {
			probe.rewriteStatxCall("leaf", 2, func(stat *unix.Statx_t) {
				stat.Mode = stat.Mode&^0o7777 | 0o755
			})
		}},
		"leaf type": {addLeaf: true, configure: func(probe *managedVMCgroupAbsenceProbe) {
			probe.rewriteStatxCall("leaf", 2, func(stat *unix.Statx_t) {
				stat.Mode = stat.Mode&^unix.S_IFMT | unix.S_IFREG
			})
		}},
		"leaf stat": {addLeaf: true, configure: func(probe *managedVMCgroupAbsenceProbe) {
			probe.failStatx("leaf", 2)
		}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newManagedVMCgroupTestFixture(t)
			if tc.addLeaf {
				fixture.addVMLeaf(testVMID)
			} else {
				require.NoError(t, os.Mkdir(
					filepath.Join(fixture.root, infra.JailerCgroupParent),
					os.FileMode(managedVMCgroupParentMode),
				))
			}
			probe := newManagedVMCgroupAbsenceProbe(fixture)
			tc.configure(probe)

			err := requireFreshRecoveryManagedVMCgroupAbsent(
				t.Context(),
				managedVMCgroupReadDepsFrom(probe.deps),
				testVMID,
			)
			require.Error(t, err)
			assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
			probe.assertReadOnly(t)
			if diff := cmp.Diff(reverseInts(probe.openedFDs), probe.closedFDs); diff != "" {
				t.Errorf("closed descriptor order mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRequireFreshRecoveryManagedVMCgroupAbsentPreservesSymlinkParentsAndLeaves(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*managedVMCgroupTestFixture){
		"parent": func(fixture *managedVMCgroupTestFixture) {
			require.NoError(t, os.Mkdir(filepath.Join(fixture.root, "elsewhere"), 0o755))
			require.NoError(t, os.Symlink("elsewhere", filepath.Join(fixture.root, infra.JailerCgroupParent)))
		},
		"leaf": func(fixture *managedVMCgroupTestFixture) {
			parent := filepath.Join(fixture.root, infra.JailerCgroupParent)
			require.NoError(t, os.Mkdir(parent, 0o755))
			require.NoError(t, os.Mkdir(filepath.Join(parent, "elsewhere"), 0o755))
			require.NoError(t, os.Symlink("elsewhere", fixture.vmLeafPath(testVMID)))
		},
	}
	for name, prepare := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newManagedVMCgroupTestFixture(t)
			prepare(fixture)
			before := snapshotManagedVMCgroupRecoveryTree(t, fixture.root)
			probe := newManagedVMCgroupAbsenceProbe(fixture)

			err := requireFreshRecoveryManagedVMCgroupAbsent(
				t.Context(),
				managedVMCgroupReadDepsFrom(probe.deps),
				testVMID,
			)
			require.Error(t, err)
			assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
			probe.assertReadOnly(t)
			if diff := cmp.Diff(before, snapshotManagedVMCgroupRecoveryTree(t, fixture.root)); diff != "" {
				t.Errorf("managed cgroup tree changed (-before +after):\n%s", diff)
			}
			if diff := cmp.Diff(reverseInts(probe.openedFDs), probe.closedFDs); diff != "" {
				t.Errorf("closed descriptor order mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// Rationale: absence observed through a detached root, parent, or leaf does not describe the fixed managed hierarchy.
// A second descriptor-relative observation must reject each deterministic replacement without touching either object.
func TestRequireFreshRecoveryManagedVMCgroupAbsentRejectsReplacementRaces(t *testing.T) {
	t.Parallel()

	t.Run("root", func(t *testing.T) {
		container := t.TempDir()
		root := filepath.Join(container, "cgroup")
		require.NoError(t, os.Mkdir(root, 0o755))
		fixture := &managedVMCgroupTestFixture{t: t, root: root}
		probe := newManagedVMCgroupAbsenceProbe(fixture)
		realOpenRoot := probe.deps.openRoot
		calls := 0
		probe.deps.openRoot = func(ctx context.Context) (int, error) {
			fd, err := realOpenRoot(ctx)
			calls++
			if err == nil && calls == 1 {
				require.NoError(t, os.Rename(root, root+"-detached"))
				require.NoError(t, os.Mkdir(root, 0o755))
			}
			return fd, err
		}

		err := requireFreshRecoveryManagedVMCgroupAbsent(
			t.Context(),
			managedVMCgroupReadDepsFrom(probe.deps),
			testVMID,
		)
		require.Error(t, err)
		assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
		probe.assertReadOnly(t)
		if diff := cmp.Diff([]string{"root", "root"}, probe.openedRoles); diff != "" {
			t.Errorf("replacement descriptor roles mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(reverseInts(probe.openedFDs), probe.closedFDs); diff != "" {
			t.Errorf("replacement close order mismatch (-want +got):\n%s", diff)
		}
		assert.DirExists(t, root)
		assert.DirExists(t, root+"-detached")
	})

	t.Run("parent", func(t *testing.T) {
		fixture := newManagedVMCgroupTestFixture(t)
		parent := filepath.Join(fixture.root, infra.JailerCgroupParent)
		require.NoError(t, os.Mkdir(parent, 0o755))
		probe := newManagedVMCgroupAbsenceProbe(fixture)
		realOpenAt2 := probe.deps.openAt2
		leafAttempts := 0
		probe.deps.openAt2 = func(ctx context.Context, fd int, name string, how *unix.OpenHow) (int, error) {
			if name == testVMID {
				leafAttempts++
				if leafAttempts == 1 {
					require.NoError(t, os.Rename(parent, parent+"-detached"))
					require.NoError(t, os.Mkdir(parent, 0o755))
				}
			}
			return realOpenAt2(ctx, fd, name, how)
		}

		err := requireFreshRecoveryManagedVMCgroupAbsent(
			t.Context(),
			managedVMCgroupReadDepsFrom(probe.deps),
			testVMID,
		)
		require.Error(t, err)
		assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
		probe.assertReadOnly(t)
		if diff := cmp.Diff([]string{"root", "parent", "root", "parent"}, probe.openedRoles); diff != "" {
			t.Errorf("replacement descriptor roles mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(reverseInts(probe.openedFDs), probe.closedFDs); diff != "" {
			t.Errorf("replacement close order mismatch (-want +got):\n%s", diff)
		}
		assert.DirExists(t, parent)
		assert.DirExists(t, parent+"-detached")
	})

	t.Run("leaf", func(t *testing.T) {
		fixture := newManagedVMCgroupTestFixture(t)
		fixture.addVMLeaf(testVMID)
		leaf := fixture.vmLeafPath(testVMID)
		probe := newManagedVMCgroupAbsenceProbe(fixture)
		realOpenAt2 := probe.deps.openAt2
		leafOpens := 0
		probe.deps.openAt2 = func(ctx context.Context, fd int, name string, how *unix.OpenHow) (int, error) {
			opened, err := realOpenAt2(ctx, fd, name, how)
			if err == nil && name == testVMID {
				leafOpens++
				if leafOpens == 1 {
					require.NoError(t, os.Rename(leaf, leaf+"-detached"))
					require.NoError(t, os.Mkdir(leaf, 0o755))
				}
			}
			return opened, err
		}

		err := requireFreshRecoveryManagedVMCgroupAbsent(
			t.Context(),
			managedVMCgroupReadDepsFrom(probe.deps),
			testVMID,
		)
		require.Error(t, err)
		assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
		probe.assertReadOnly(t)
		if diff := cmp.Diff(
			[]string{"root", "parent", "leaf", "root", "parent", "leaf"},
			probe.openedRoles,
		); diff != "" {
			t.Errorf("replacement descriptor roles mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(reverseInts(probe.openedFDs), probe.closedFDs); diff != "" {
			t.Errorf("replacement close order mismatch (-want +got):\n%s", diff)
		}
		assert.DirExists(t, leaf)
		assert.DirExists(t, leaf+"-detached")
	})
}

func TestRequireFreshRecoveryManagedVMCgroupAbsentFailsClosedOnOpenAndStatErrors(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		addLeaf bool
		fault   func(*managedVMCgroupAbsenceProbe)
	}{
		"open root": {fault: func(probe *managedVMCgroupAbsenceProbe) {
			probe.failOpenRoot(1)
		}},
		"inspect root": {fault: func(probe *managedVMCgroupAbsenceProbe) {
			probe.failStatx("root", 1)
		}},
		"inspect root filesystem": {fault: func(probe *managedVMCgroupAbsenceProbe) {
			probe.deps.statfs = func(context.Context, int, *unix.Statfs_t) error { return unix.EIO }
		}},
		"open parent": {fault: func(probe *managedVMCgroupAbsenceProbe) {
			probe.failOpenAt2(infra.JailerCgroupParent, 1)
		}},
		"inspect parent": {fault: func(probe *managedVMCgroupAbsenceProbe) {
			probe.failStatx("parent", 1)
		}},
		"open leaf": {fault: func(probe *managedVMCgroupAbsenceProbe) {
			probe.failOpenAt2(testVMID, 1)
		}},
		"inspect leaf": {addLeaf: true, fault: func(probe *managedVMCgroupAbsenceProbe) {
			probe.failStatx("leaf", 1)
		}},
		"reopen root": {fault: func(probe *managedVMCgroupAbsenceProbe) {
			probe.failOpenRoot(2)
		}},
		"reopen parent": {fault: func(probe *managedVMCgroupAbsenceProbe) {
			probe.failOpenAt2(infra.JailerCgroupParent, 2)
		}},
		"recheck leaf": {fault: func(probe *managedVMCgroupAbsenceProbe) {
			probe.failOpenAt2(testVMID, 2)
		}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newManagedVMCgroupTestFixture(t)
			if tc.addLeaf {
				fixture.addVMLeaf(testVMID)
			} else {
				require.NoError(t, os.Mkdir(
					filepath.Join(fixture.root, infra.JailerCgroupParent),
					os.FileMode(managedVMCgroupParentMode),
				))
			}
			before := snapshotManagedVMCgroupRecoveryTree(t, fixture.root)
			probe := newManagedVMCgroupAbsenceProbe(fixture)
			tc.fault(probe)

			err := requireFreshRecoveryManagedVMCgroupAbsent(
				t.Context(),
				managedVMCgroupReadDepsFrom(probe.deps),
				testVMID,
			)
			require.Error(t, err)
			assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
			probe.assertReadOnly(t)
			if diff := cmp.Diff(before, snapshotManagedVMCgroupRecoveryTree(t, fixture.root)); diff != "" {
				t.Errorf("managed cgroup tree changed (-before +after):\n%s", diff)
			}
			if diff := cmp.Diff(reverseInts(probe.openedFDs), probe.closedFDs); diff != "" {
				t.Errorf("closed descriptor order mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// Rationale: descriptor cleanup is diagnostic evidence, not a replacement for the operation failure. Joining both
// causes must preserve errors.Is matching for the primary observation error and the close failure.
func TestRequireFreshRecoveryManagedVMCgroupAbsentPreservesPrimaryAndCloseErrors(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	require.NoError(t, os.Mkdir(
		filepath.Join(fixture.root, infra.JailerCgroupParent),
		os.FileMode(managedVMCgroupParentMode),
	))
	probe := newManagedVMCgroupAbsenceProbe(fixture)
	probe.failOpenAt2(testVMID, 2)
	closeErr := errors.New("injected close failure after primary error")
	realClose := probe.deps.close
	probe.deps.close = func(ctx context.Context, fd int) error {
		err := realClose(ctx, fd)
		if fd == probe.openedFDs[len(probe.openedFDs)-1] {
			return errors.Join(err, closeErr)
		}
		return err
	}

	err := requireFreshRecoveryManagedVMCgroupAbsent(
		t.Context(),
		managedVMCgroupReadDepsFrom(probe.deps),
		testVMID,
	)
	require.ErrorIs(t, err, unix.EIO)
	require.ErrorIs(t, err, closeErr)
	assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
	if diff := cmp.Diff(reverseInts(probe.openedFDs), probe.closedFDs); diff != "" {
		t.Errorf("closed descriptor order mismatch (-want +got):\n%s", diff)
	}
}

type managedVMCgroupAbsenceContextKey struct{}

// Rationale: cancellation can arrive after the final absence observation. Every acquired descriptor must still close
// once in reverse order through a cancellation-detached context that preserves diagnostic values and close errors.
func TestRequireFreshRecoveryManagedVMCgroupAbsentPreservesCloseErrorWithoutCancellation(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	require.NoError(t, os.Mkdir(
		filepath.Join(fixture.root, infra.JailerCgroupParent),
		os.FileMode(managedVMCgroupParentMode),
	))
	probe := newManagedVMCgroupAbsenceProbe(fixture)
	ctx, cancel := context.WithCancel(context.WithValue(
		t.Context(),
		managedVMCgroupAbsenceContextKey{},
		"fresh-recovery",
	))
	realOpenAt2 := probe.deps.openAt2
	leafAttempts := 0
	probe.deps.openAt2 = func(openCtx context.Context, fd int, name string, how *unix.OpenHow) (int, error) {
		opened, err := realOpenAt2(openCtx, fd, name, how)
		if name == testVMID {
			leafAttempts++
			if leafAttempts == 2 {
				cancel()
			}
		}
		return opened, err
	}
	closeErr := errors.New("injected absence descriptor close failure")
	realClose := probe.deps.close
	closeContextErrs := []error{}
	closeContextValues := []string{}
	probe.deps.close = func(closeCtx context.Context, fd int) error {
		closeContextErrs = append(closeContextErrs, closeCtx.Err())
		value, _ := closeCtx.Value(managedVMCgroupAbsenceContextKey{}).(string)
		closeContextValues = append(closeContextValues, value)
		err := realClose(closeCtx, fd)
		if fd == probe.openedFDs[len(probe.openedFDs)-1] {
			return errors.Join(err, closeErr)
		}
		return err
	}

	err := requireFreshRecoveryManagedVMCgroupAbsent(ctx, managedVMCgroupReadDepsFrom(probe.deps), testVMID)
	require.ErrorIs(t, err, closeErr)
	assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
	assert.Equal(t, 2, leafAttempts)
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	probe.assertReadOnly(t)
	if diff := cmp.Diff(reverseInts(probe.openedFDs), probe.closedFDs); diff != "" {
		t.Errorf("closed descriptor order mismatch (-want +got):\n%s", diff)
	}
	wantContextErrs := make([]error, len(probe.closedFDs))
	if diff := cmp.Diff(wantContextErrs, closeContextErrs); diff != "" {
		t.Errorf("close context errors mismatch (-want +got):\n%s", diff)
	}
	wantContextValues := make([]string, len(probe.closedFDs))
	for index := range wantContextValues {
		wantContextValues[index] = "fresh-recovery"
	}
	if diff := cmp.Diff(wantContextValues, closeContextValues); diff != "" {
		t.Errorf("close context values mismatch (-want +got):\n%s", diff)
	}
}

type managedVMCgroupAbsenceProbe struct {
	deps            managedVMCgroupDeps
	openedRoles     []string
	openedFDs       []int
	closedFDs       []int
	mutationCalls   []string
	membershipReads int
	roleByFD        map[int]string
}

func newManagedVMCgroupAbsenceProbe(fixture *managedVMCgroupTestFixture) *managedVMCgroupAbsenceProbe {
	fixture.t.Helper()
	probe := &managedVMCgroupAbsenceProbe{
		deps:        fixture.deps(),
		openedRoles: []string{},
		openedFDs:   []int{},
		closedFDs:   []int{},
		roleByFD:    make(map[int]string),
	}
	realOpenRoot := probe.deps.openRoot
	probe.deps.openRoot = func(ctx context.Context) (int, error) {
		fd, err := realOpenRoot(ctx)
		if err == nil {
			probe.openedRoles = append(probe.openedRoles, "root")
			probe.openedFDs = append(probe.openedFDs, fd)
			probe.roleByFD[fd] = "root"
		}
		return fd, err
	}
	realOpenAt2 := probe.deps.openAt2
	probe.deps.openAt2 = func(ctx context.Context, parentFD int, name string, how *unix.OpenHow) (int, error) {
		fd, err := realOpenAt2(ctx, parentFD, name, how)
		if err == nil {
			role := "unexpected:" + name
			switch name {
			case infra.JailerCgroupParent:
				role = "parent"
			case testVMID:
				role = "leaf"
			}
			probe.openedRoles = append(probe.openedRoles, role)
			probe.openedFDs = append(probe.openedFDs, fd)
			probe.roleByFD[fd] = role
		}
		return fd, err
	}
	realClose := probe.deps.close
	probe.deps.close = func(ctx context.Context, fd int) error {
		probe.closedFDs = append(probe.closedFDs, fd)
		return realClose(ctx, fd)
	}
	probe.deps.mkdirAt = func(context.Context, int, string, uint32) error {
		probe.mutationCalls = append(probe.mutationCalls, "mkdir")
		return errors.New("unexpected mkdir")
	}
	probe.deps.fchown = func(context.Context, int, int, int) error {
		probe.mutationCalls = append(probe.mutationCalls, "chown")
		return errors.New("unexpected chown")
	}
	probe.deps.fchmod = func(context.Context, int, uint32) error {
		probe.mutationCalls = append(probe.mutationCalls, "chmod")
		return errors.New("unexpected chmod")
	}
	probe.deps.unlinkAt = func(context.Context, int, string, int) error {
		probe.mutationCalls = append(probe.mutationCalls, "unlink")
		return errors.New("unexpected unlink")
	}
	probe.deps.pread = func(context.Context, int, []byte, int64) (int, error) {
		probe.membershipReads++
		return 0, errors.New("unexpected membership read")
	}
	return probe
}

func (probe *managedVMCgroupAbsenceProbe) assertReadOnly(t *testing.T) {
	t.Helper()
	assert.Empty(t, probe.mutationCalls)
	assert.Zero(t, probe.membershipReads)
}

func (probe *managedVMCgroupAbsenceProbe) rewriteStatx(role string, rewrite func(*unix.Statx_t)) {
	realStatx := probe.deps.statx
	probe.deps.statx = func(ctx context.Context, fd int, stat *unix.Statx_t) error {
		if err := realStatx(ctx, fd, stat); err != nil {
			return err
		}
		if probe.roleByFD[fd] == role {
			rewrite(stat)
		}
		return nil
	}
}

func (probe *managedVMCgroupAbsenceProbe) rewriteStatxCall(
	role string,
	wantCall int,
	rewrite func(*unix.Statx_t),
) {
	realStatx := probe.deps.statx
	calls := 0
	probe.deps.statx = func(ctx context.Context, fd int, stat *unix.Statx_t) error {
		if err := realStatx(ctx, fd, stat); err != nil {
			return err
		}
		if probe.roleByFD[fd] == role {
			calls++
			if calls == wantCall {
				rewrite(stat)
			}
		}
		return nil
	}
}

func (probe *managedVMCgroupAbsenceProbe) failOpenRoot(wantCall int) {
	realOpenRoot := probe.deps.openRoot
	calls := 0
	probe.deps.openRoot = func(ctx context.Context) (int, error) {
		calls++
		if calls == wantCall {
			return -1, unix.EIO
		}
		return realOpenRoot(ctx)
	}
}

func (probe *managedVMCgroupAbsenceProbe) failOpenAt2(name string, wantCall int) {
	realOpenAt2 := probe.deps.openAt2
	calls := 0
	probe.deps.openAt2 = func(ctx context.Context, fd int, openedName string, how *unix.OpenHow) (int, error) {
		if openedName == name {
			calls++
			if calls == wantCall {
				return -1, unix.EIO
			}
		}
		return realOpenAt2(ctx, fd, openedName, how)
	}
}

func (probe *managedVMCgroupAbsenceProbe) failStatx(role string, wantCall int) {
	realStatx := probe.deps.statx
	calls := 0
	probe.deps.statx = func(ctx context.Context, fd int, stat *unix.Statx_t) error {
		if probe.roleByFD[fd] == role {
			calls++
			if calls == wantCall {
				return unix.EIO
			}
		}
		return realStatx(ctx, fd, stat)
	}
}

func snapshotManagedVMCgroupRecoveryTree(t *testing.T, root string) []string {
	t.Helper()
	entries := []string{}
	require.NoError(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		value := relative + "|" + info.Mode().String()
		if info.Mode().IsRegular() {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += "|" + string(raw)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			value += "|" + target
		}
		entries = append(entries, value)
		return nil
	}))
	return entries
}

func reverseInts(values []int) []int {
	reversed := make([]int, len(values))
	for index := range values {
		reversed[len(values)-1-index] = values[index]
	}
	return reversed
}
