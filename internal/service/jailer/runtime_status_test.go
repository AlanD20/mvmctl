package jailer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

type runtimeStatusFixture struct {
	authority             *instanceAuthority
	tree                  *testInstanceAuthorityTree
	root                  string
	recovery              processRecoveryDeps
	identity              processIdentityVerifierDeps
	metadata              map[processMetadataTestKey][]byte
	stats                 map[int]unix.Stat_t
	locators              map[int]instanceProcessLocator
	alive                 map[int]bool
	opened                []int
	closed                []int
	pidCloses             []int
	scans                 int
	bootID                string
	processDirectoryReads int
	processMetadataReads  int
}

// newRuntimeStatusFixture uses real schema-3 files and existing lock fakes. Only proc/pidfd reads are substituted;
// unavailable mutation and hash callbacks ensure observations cannot silently grow cleanup or executable I/O effects.
func newRuntimeStatusFixture(t testing.TB, records ...instanceRecord) *runtimeStatusFixture {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "var/lib"), 0755))
	f := &runtimeStatusFixture{
		root: root, tree: newTestInstanceAuthorityTree(), bootID: processTestHostBootID,
		metadata: make(map[processMetadataTestKey][]byte), stats: make(map[int]unix.Stat_t),
		locators: make(map[int]instanceProcessLocator), alive: make(map[int]bool),
	}
	f.authority = newInstanceAuthorityWithPolicy(realInstanceAuthorityDeps(), testInstanceAuthorityPolicy(root), f.tree)
	readDirNames := f.authority.deps.readDirNames
	f.authority.deps.readDirNames = func(ctx context.Context, fd int) ([]string, error) {
		f.scans++
		return readDirNames(ctx, fd)
	}
	f.authority.deps.mkdirAt = nil
	f.authority.deps.write = nil
	f.authority.deps.renameAt = nil
	f.authority.deps.unlinkAt = nil
	f.recovery = processRecoveryDeps{
		pidfdOpen: func(_ context.Context, pid int) (int, error) {
			f.opened = append(f.opened, pid)
			if _, ok := f.locators[pid]; !ok {
				return -1, unix.ESRCH
			}
			return pid, nil
		},
		pidfdAlive: func(_ context.Context, fd int) (bool, error) { return f.alive[fd], nil },
		deriveLocator: func(_ context.Context, fd int) (instanceProcessLocator, error) {
			return f.locators[fd], nil
		},
		descriptorFlags: func(context.Context, int) (int, error) { return unix.FD_CLOEXEC, nil },
		close: func(_ context.Context, fd int) error {
			f.pidCloses = append(f.pidCloses, fd)
			return nil
		},
	}
	f.identity = processIdentityVerifierDeps{
		readBootID: func(context.Context) (string, error) { return f.bootID, nil },
		openProcessDirectory: func(_ context.Context, pid int) (int, error) {
			f.processDirectoryReads++
			return pid * 10, nil
		},
		readProcessMetadata: func(_ context.Context, fd int, kind processMetadataKind, _ int) ([]byte, error) {
			f.processMetadataReads++
			return f.metadata[processMetadataTestKey{directoryFD: fd, kind: kind}], nil
		},
		pinProcessObject: func(_ context.Context, fd int, object processPinnedObject) (int, error) {
			return fd + int(object), nil
		},
		descriptorFlags: func(context.Context, int) (int, error) { return unix.FD_CLOEXEC, nil },
		fstat: func(_ context.Context, fd int, stat *unix.Stat_t) error {
			*stat = f.stats[fd]
			return nil
		},
		close: func(_ context.Context, fd int) error {
			f.closed = append(f.closed, fd)
			return nil
		},
	}
	for _, record := range records {
		f.writeRecord(t, record)
		if record.launch == nil {
			continue
		}
		f.locators[record.launch.process.pid] = record.launch.process
		f.alive[record.launch.process.pid] = true
		if record.active == nil {
			continue
		}
		f.addProcess(record.launch.process, record.active.userIDs, record.active.groupIDs,
			record.active.mountNamespace, record.active.jailRoot, record.active.executable.device,
			record.active.executable.inode, record.active.executable.sizeBytes, "/mvmctl/"+record.vmID)
		if record.launch.relay.kind == instanceRelayPresent {
			relay := record.active.relay
			f.addProcess(record.launch.relay.locator, relay.userIDs, relay.groupIDs, relay.mountNamespace, relay.root,
				relay.executable.device, relay.executable.inode, relay.executable.sizeBytes, "/user.slice")
		}
	}
	return f
}

func (f *runtimeStatusFixture) writeRecord(t testing.TB, record instanceRecord) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(f.root, "var/lib/mvmctl"), 0755))
	directory := filepath.Join(f.root, "var/lib/mvmctl/instances", fmt.Sprint(record.ownerUID))
	require.NoError(t, os.MkdirAll(directory, 0700))
	raw, err := encodeInstanceRecord(record)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(directory, record.vmID+".json"), raw, 0600))
}

func (f *runtimeStatusFixture) addProcess(
	locator instanceProcessLocator,
	uids, gids processCredentialIdentity,
	mount, root instanceObjectIdentity,
	executableDevice, executableInode, executableSize uint64,
	cgroup string,
) {
	f.locators[locator.pid] = locator
	f.alive[locator.pid] = true
	fd := locator.pid * 10
	f.metadata[processMetadataTestKey{fd, processStatMetadata}] = []byte(
		testProcessStat(locator.pid, locator.startTimeTicks, "S"),
	)
	f.metadata[processMetadataTestKey{fd, processStatusMetadata}] = []byte(fmt.Sprintf(
		"Uid:\t%d\t%d\t%d\t%d\nGid:\t%d\t%d\t%d\t%d\nGroups:\t\n",
		uids.real, uids.effective, uids.saved, uids.filesystem, gids.real, gids.effective, gids.saved, gids.filesystem,
	))
	f.metadata[processMetadataTestKey{fd, processCgroupMetadata}] = []byte("0::" + cgroup + "\n")
	f.stats[fd+int(processMountNamespaceObject)] = unix.Stat_t{
		Dev:  mount.device,
		Ino:  mount.inode,
		Mode: unix.S_IFREG | 0444,
	}
	f.stats[fd+int(processNetworkNamespaceObject)] = unix.Stat_t{
		Dev:  77,
		Ino:  uint64(locator.pid),
		Mode: unix.S_IFREG | 0444,
	}
	f.stats[fd+int(processRootObject)] = unix.Stat_t{Dev: root.device, Ino: root.inode, Mode: unix.S_IFDIR | 0755}
	f.stats[fd+int(processExecutableObject)] = unix.Stat_t{
		Dev: executableDevice, Ino: executableInode, Size: int64(executableSize), Mode: unix.S_IFREG | 0755, Nlink: 1,
	}
}

func (f *runtimeStatusFixture) observe(ctx context.Context, ids ...string) (map[string]runtimeStatus, error) {
	return f.authority.observeRuntimeStatusWithDeps(ctx, testInstanceCaller(), ids, f.recovery, f.identity)
}

func TestRuntimeStatusActiveProcessOutcomes(t *testing.T) {
	t.Parallel()
	record := testActiveInstanceRecord(true)
	pid := record.launch.process.pid
	relayPID := record.launch.relay.locator.pid
	tests := []struct {
		name   string
		change func(*runtimeStatusFixture)
		want   runtimeStatus
	}{
		{"process permission denied", func(f *runtimeStatusFixture) {
			f.recovery.pidfdOpen = func(context.Context, int) (int, error) { return -1, unix.EPERM }
		}, runtimeStatusUnavailable},
		{
			"missing pidfd capability",
			func(f *runtimeStatusFixture) { f.recovery.pidfdOpen = nil },
			runtimeStatusUnavailable,
		},
		{
			"missing metadata capability",
			func(f *runtimeStatusFixture) { f.identity.fstat = nil },
			runtimeStatusUnavailable,
		},
		{"pidfd syscall unavailable", func(f *runtimeStatusFixture) {
			f.recovery.pidfdOpen = func(context.Context, int) (int, error) { return -1, unix.ENOSYS }
		}, runtimeStatusUnavailable},
		{"metadata I/O failure", func(f *runtimeStatusFixture) {
			f.identity.readProcessMetadata = func(context.Context, int, processMetadataKind, int) ([]byte, error) { return nil, unix.EIO }
		}, runtimeStatusUnavailable},
		{"malformed metadata", func(f *runtimeStatusFixture) {
			f.metadata[processMetadataTestKey{pid * 10, processStatusMetadata}] = []byte("Uid: truncated")
		}, runtimeStatusUnavailable},
		{"proc missing while retained task live", func(f *runtimeStatusFixture) {
			f.identity.openProcessDirectory = func(context.Context, int) (int, error) { return -1, unix.ENOENT }
		}, runtimeStatusUnavailable},
		{"locator lookup failure", func(f *runtimeStatusFixture) {
			f.recovery.deriveLocator = func(context.Context, int) (instanceProcessLocator, error) { return instanceProcessLocator{}, unix.EIO }
		}, runtimeStatusUnavailable},
		{"pidfd without close-on-exec", func(f *runtimeStatusFixture) {
			f.recovery.descriptorFlags = func(context.Context, int) (int, error) { return 0, nil }
		}, runtimeStatusUnavailable},
		{"missing firecracker", func(f *runtimeStatusFixture) { delete(f.locators, pid) }, runtimeStatusCrashed},
		{"dead firecracker", func(f *runtimeStatusFixture) { f.alive[pid] = false }, runtimeStatusCrashed},
		{"reused firecracker pid", func(f *runtimeStatusFixture) {
			f.locators[pid] = instanceProcessLocator{pid: pid, startTimeTicks: record.launch.process.startTimeTicks + 1}
		}, runtimeStatusCrashed},
		{"metadata start mismatch", func(f *runtimeStatusFixture) {
			f.metadata[processMetadataTestKey{pid * 10, processStatMetadata}] = []byte(testProcessStat(pid, 123, "S"))
		}, runtimeStatusCrashed},
		{"credentials mismatch", func(f *runtimeStatusFixture) {
			f.metadata[processMetadataTestKey{pid * 10, processStatusMetadata}] = []byte(
				testProcessStatus(9999, testAuthorityGID, ""),
			)
		}, runtimeStatusCrashed},
		{"nonempty supplementary groups", func(f *runtimeStatusFixture) {
			f.metadata[processMetadataTestKey{pid * 10, processStatusMetadata}] = []byte(
				testProcessStatus(testAuthorityUID, testAuthorityGID, "123"),
			)
		}, runtimeStatusCrashed},
		{"cgroup mismatch", func(f *runtimeStatusFixture) {
			f.metadata[processMetadataTestKey{pid * 10, processCgroupMetadata}] = []byte("0::/foreign\n")
		}, runtimeStatusCrashed},
		{"missing relay", func(f *runtimeStatusFixture) { delete(f.locators, relayPID) }, runtimeStatusCrashed},
		{"dead relay", func(f *runtimeStatusFixture) { f.alive[relayPID] = false }, runtimeStatusCrashed},
		{"matching sealed identities", func(*runtimeStatusFixture) {}, runtimeStatusRunning},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newRuntimeStatusFixture(t, record)
			tc.change(f)
			got, err := f.observe(t.Context(), record.vmID)
			if tc.want == runtimeStatusUnavailable {
				require.Error(t, err)
				assert.Nil(t, got, "unavailable observation must expose no partial batch")
				assert.Equal(t, string(runtimeStatusUnavailable), errs.AsDomainError(err).Details["runtime_status"])
				return
			}
			require.NoError(t, err)
			if diff := cmp.Diff(map[string]runtimeStatus{record.vmID: tc.want}, got); diff != "" {
				t.Errorf("runtime observation mismatch (-want +got):\n%s", diff)
			}
			before, encodeErr := encodeInstanceRecord(record)
			require.NoError(t, encodeErr)
			after, readErr := os.ReadFile(filepath.Join(f.root, "var/lib/mvmctl/instances/1000", record.vmID+".json"))
			require.NoError(t, readErr)
			if diff := cmp.Diff(before, after); diff != "" {
				t.Errorf("observation changed durable record (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRuntimeStatusActiveMetadataMismatch(t *testing.T) {
	t.Parallel()
	record := testActiveInstanceRecord(true)
	mutations := map[string]func(*unix.Stat_t){
		"device": func(s *unix.Stat_t) { s.Dev++ }, "inode": func(s *unix.Stat_t) { s.Ino++ },
		"size": func(s *unix.Stat_t) { s.Size++ }, "mode": func(s *unix.Stat_t) { s.Mode ^= 0020 },
		"uid": func(s *unix.Stat_t) { s.Uid++ }, "gid": func(s *unix.Stat_t) { s.Gid++ },
		"links": func(s *unix.Stat_t) { s.Nlink++ },
	}
	for _, locator := range []instanceProcessLocator{record.launch.process, record.launch.relay.locator} {
		for name, mutate := range mutations {
			t.Run(fmt.Sprintf("pid_%d_executable_%s", locator.pid, name), func(t *testing.T) {
				f := newRuntimeStatusFixture(t, record)
				fd := locator.pid*10 + int(processExecutableObject)
				stat := f.stats[fd]
				mutate(&stat)
				f.stats[fd] = stat
				got, err := f.observe(t.Context(), record.vmID)
				require.NoError(t, err)
				assert.Equal(t, runtimeStatusCrashed, got[record.vmID])
			})
		}
		for _, object := range []processPinnedObject{processMountNamespaceObject, processRootObject} {
			t.Run(fmt.Sprintf("pid_%d_object_%d", locator.pid, object), func(t *testing.T) {
				f := newRuntimeStatusFixture(t, record)
				fd := locator.pid*10 + int(object)
				stat := f.stats[fd]
				stat.Ino++
				f.stats[fd] = stat
				got, err := f.observe(t.Context(), record.vmID)
				require.NoError(t, err)
				assert.Equal(t, runtimeStatusCrashed, got[record.vmID])
			})
		}
	}
}

// Rationale: saved and filesystem credentials independently carry authority. A check of only the effective IDs
// would pass an apparently ordinary process whose other credential fields no longer match its active record.
func TestRuntimeStatusEachCredentialFieldMismatch(t *testing.T) {
	t.Parallel()
	record := testActiveInstanceRecord(true)
	roles := []struct {
		name       string
		locator    instanceProcessLocator
		uids, gids processCredentialIdentity
	}{
		{"firecracker", record.launch.process, record.active.userIDs, record.active.groupIDs},
		{"relay", record.launch.relay.locator, record.active.relay.userIDs, record.active.relay.groupIDs},
	}
	for _, role := range roles {
		for _, kind := range []string{"uid", "gid"} {
			for index, field := range []string{"real", "effective", "saved", "filesystem"} {
				t.Run(role.name+"/"+kind+"/"+field, func(t *testing.T) {
					f := newRuntimeStatusFixture(t, record)
					uids := [4]uint32{role.uids.real, role.uids.effective, role.uids.saved, role.uids.filesystem}
					gids := [4]uint32{role.gids.real, role.gids.effective, role.gids.saved, role.gids.filesystem}
					if kind == "uid" {
						uids[index]++
					} else {
						gids[index]++
					}
					f.metadata[processMetadataTestKey{role.locator.pid * 10, processStatusMetadata}] = []byte(
						fmt.Sprintf(
							"Uid:\t%d\t%d\t%d\t%d\nGid:\t%d\t%d\t%d\t%d\nGroups:\t\n",
							uids[0], uids[1], uids[2], uids[3], gids[0], gids[1], gids[2], gids[3],
						),
					)
					got, err := f.observe(t.Context(), record.vmID)
					require.NoError(t, err)
					assert.Equal(t, runtimeStatusCrashed, got[record.vmID])
				})
			}
		}
	}
}

func TestRuntimeStatusFirecrackerRootMetadataMismatch(t *testing.T) {
	t.Parallel()
	record := testActiveInstanceRecord(false)
	mutations := map[string]func(*unix.Stat_t){
		"type": func(stat *unix.Stat_t) { stat.Mode = unix.S_IFREG | 0755 },
		"mode": func(stat *unix.Stat_t) { stat.Mode = unix.S_IFDIR | 0700 },
		"uid":  func(stat *unix.Stat_t) { stat.Uid++ },
		"gid":  func(stat *unix.Stat_t) { stat.Gid++ },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			f := newRuntimeStatusFixture(t, record)
			fd := record.launch.process.pid*10 + int(processRootObject)
			stat := f.stats[fd]
			mutate(&stat)
			f.stats[fd] = stat
			got, err := f.observe(t.Context(), record.vmID)
			require.NoError(t, err)
			assert.Equal(t, runtimeStatusCrashed, got[record.vmID])
		})
	}
}

func TestRuntimeStatusFinalProcessRecheck(t *testing.T) {
	t.Parallel()
	record := testActiveInstanceRecord(true)
	tests := []struct {
		name   string
		change func(*runtimeStatusFixture)
	}{
		{
			"firecracker exits while inspecting relay",
			func(f *runtimeStatusFixture) { f.alive[record.launch.process.pid] = false },
		},
		{"firecracker locator drifts while inspecting relay", func(f *runtimeStatusFixture) {
			locator := f.locators[record.launch.process.pid]
			locator.startTimeTicks++
			f.locators[record.launch.process.pid] = locator
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newRuntimeStatusFixture(t, record)
			read := f.identity.readProcessMetadata
			f.identity.readProcessMetadata = func(ctx context.Context, fd int, kind processMetadataKind, limit int) ([]byte, error) {
				if fd == record.launch.relay.locator.pid*10 {
					tc.change(f)
				}
				return read(ctx, fd, kind, limit)
			}
			got, err := f.observe(t.Context(), record.vmID)
			require.NoError(t, err)
			assert.Equal(t, runtimeStatusCrashed, got[record.vmID])
			if diff := cmp.Diff(
				[]int{record.launch.relay.locator.pid, record.launch.process.pid},
				f.pidCloses,
			); diff != "" {
				t.Errorf("pidfd release order mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// Rationale: the relay can exit or lose its locator after both complete metadata observations. The final sweep must
// recheck the relay as well as Firecracker, and a missing relay proof must invalidate the running classification.
func TestRuntimeStatusFinalRelayRecheck(t *testing.T) {
	t.Parallel()
	record := testActiveInstanceRecord(true)
	relayPID := record.launch.relay.locator.pid
	tests := []struct {
		name        string
		change      func(*runtimeStatusFixture)
		livenessErr error
		locatorErr  error
		want        runtimeStatus
	}{
		{name: "relay exits after metadata observation", change: func(f *runtimeStatusFixture) {
			f.alive[relayPID] = false
		}, want: runtimeStatusCrashed},
		{name: "relay locator changes after metadata observation", change: func(f *runtimeStatusFixture) {
			locator := f.locators[relayPID]
			locator.startTimeTicks++
			f.locators[relayPID] = locator
		}, want: runtimeStatusCrashed},
		{name: "relay final liveness unavailable", livenessErr: unix.EIO, want: runtimeStatusUnavailable},
		{name: "relay final locator unavailable", locatorErr: unix.EIO, want: runtimeStatusUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newRuntimeStatusFixture(t, record)
			relayObserved := false
			finalSweep := false
			read := f.identity.readProcessMetadata
			f.identity.readProcessMetadata = func(ctx context.Context, fd int, kind processMetadataKind, limit int) ([]byte, error) {
				if fd == relayPID*10 {
					relayObserved = true
				}
				return read(ctx, fd, kind, limit)
			}
			alive := f.recovery.pidfdAlive
			f.recovery.pidfdAlive = func(ctx context.Context, fd int) (bool, error) {
				if fd == relayPID && finalSweep && tc.livenessErr != nil {
					return false, tc.livenessErr
				}
				return alive(ctx, fd)
			}
			derive := f.recovery.deriveLocator
			f.recovery.deriveLocator = func(ctx context.Context, fd int) (instanceProcessLocator, error) {
				if fd == record.launch.process.pid && relayObserved {
					finalSweep = true
					if tc.change != nil {
						tc.change(f)
					}
				}
				if fd == relayPID && finalSweep && tc.locatorErr != nil {
					return instanceProcessLocator{}, tc.locatorErr
				}
				return derive(ctx, fd)
			}
			got, err := f.observe(t.Context(), record.vmID)
			require.True(t, finalSweep, "failure must be injected after both processes' metadata observations")
			if tc.want == runtimeStatusUnavailable {
				require.Error(t, err)
				assert.Nil(t, got, "late unavailable relay proof must expose no partial batch")
				assert.Equal(t, string(runtimeStatusUnavailable), errs.AsDomainError(err).Details["runtime_status"])
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got[record.vmID])
		})
	}
}

func TestRuntimeStatusUnavailableDominatesCrashed(t *testing.T) {
	t.Parallel()
	record := testActiveInstanceRecord(true)
	f := newRuntimeStatusFixture(t, record)
	delete(f.locators, record.launch.process.pid)
	f.identity.openProcessDirectory = func(context.Context, int) (int, error) { return -1, unix.EACCES }
	got, err := f.observe(t.Context(), record.vmID)
	require.ErrorIs(t, err, unix.EACCES)
	assert.Nil(t, got)
}

func TestRuntimeStatusCleanupFailureInvalidatesCompleteObservation(t *testing.T) {
	t.Parallel()
	record := testActiveInstanceRecord(true)
	injected := errors.New("injected cleanup failure")
	tests := map[string]func(*runtimeStatusFixture){
		"pidfd":           func(f *runtimeStatusFixture) { f.recovery.close = func(context.Context, int) error { return injected } },
		"proc descriptor": func(f *runtimeStatusFixture) { f.identity.close = func(context.Context, int) error { return injected } },
		"VM lock": func(f *runtimeStatusFixture) {
			f.tree.lockReleaseErr = injected
			f.tree.lockReleaseErrDescription = "vm"
		},
		"index": func(f *runtimeStatusFixture) {
			f.tree.lockReleaseErr = injected
			f.tree.lockReleaseErrDescription = "index"
		},
		"owner":          func(f *runtimeStatusFixture) { f.tree.uidReleaseErr = injected },
		"root authority": func(f *runtimeStatusFixture) { f.tree.treeReleaseErr = injected },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			f := newRuntimeStatusFixture(t, record)
			change(f)
			got, err := f.observe(t.Context(), record.vmID)
			require.ErrorIs(t, err, injected)
			assert.Nil(t, got)
		})
	}
}

func TestRuntimeStatusProcCloseErrorCannotBecomeCrashed(t *testing.T) {
	t.Parallel()
	record := testActiveInstanceRecord(false)
	f := newRuntimeStatusFixture(t, record)
	f.identity.close = func(context.Context, int) error {
		f.alive[record.launch.process.pid] = false
		return unix.ENOENT
	}
	got, err := f.observe(t.Context(), record.vmID)
	require.ErrorIs(t, err, unix.ENOENT)
	assert.Nil(t, got)
}

func TestRuntimeStatusNamespaceDriftBetweenObservations(t *testing.T) {
	t.Parallel()
	record := testActiveInstanceRecord(false)
	f := newRuntimeStatusFixture(t, record)
	stat := f.identity.fstat
	namespaceFD := record.launch.process.pid*10 + int(processNetworkNamespaceObject)
	readCount := 0
	f.identity.fstat = func(ctx context.Context, fd int, value *unix.Stat_t) error {
		if err := stat(ctx, fd, value); err != nil {
			return err
		}
		if fd == namespaceFD {
			value.Ino += uint64(readCount)
			readCount++
		}
		return nil
	}
	got, err := f.observe(t.Context(), record.vmID)
	require.NoError(t, err)
	assert.Equal(t, runtimeStatusCrashed, got[record.vmID])
}

func TestRuntimeStatusRejectsOldSchema(t *testing.T) {
	t.Parallel()
	record := testActiveInstanceRecord(false)
	f := newRuntimeStatusFixture(t, record)
	path := filepath.Join(f.root, "var/lib/mvmctl/instances/1000", record.vmID+".json")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	raw = []byte(strings.Replace(string(raw), `"schema_version":3`, `"schema_version":2`, 1))
	require.NoError(t, os.WriteFile(path, raw, 0600))
	got, err := f.observe(t.Context(), record.vmID)
	require.Error(t, err)
	assert.Nil(t, got)
	assert.Empty(t, f.opened)
}
