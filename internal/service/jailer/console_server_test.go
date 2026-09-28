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
)

type consoleStopFixture struct {
	server   *ConsoleServer
	locked   *lockedInstance
	path     string
	alive    bool
	opened   []int
	signaled []int
	closed   []int
	identity *fakeProcessIdentityDeps
}

// The existing authority and identity fixtures substitute proc and pidfds only.
// The journal is real and is independently read back after every stop outcome.
func newConsoleStopFixture(t *testing.T) *consoleStopFixture {
	t.Helper()
	record, identity := newActiveRecoveryIdentityFixture(t, true)
	authority, root := newTestInstanceAuthority(t)
	roots, err := openInstanceAuthorityRoots(t.Context(), authority.deps, authority.policy, authority.authorityTree)
	require.NoError(t, err)
	directories, err := roots.openUIDDirectories(t.Context(), record.ownerUID)
	require.NoError(t, err)
	require.NoError(t, directories.writeRecord(t.Context(), record))
	f := &consoleStopFixture{
		locked: &lockedInstance{record: record, directories: directories, roots: roots},
		path:   filepath.Join(root, "var/lib/mvmctl/instances/1000", record.vmID+".json"),
		alive:  true, identity: identity,
	}
	f.server = &ConsoleServer{authority: authority, identity: identity.deps()}
	f.server.recovery = processRecoveryDeps{
		pidfdOpen: func(_ context.Context, pid int) (int, error) {
			f.opened = append(f.opened, pid)
			return 700, nil
		},
		pidfdAlive:      func(context.Context, int) (bool, error) { return f.alive, nil },
		descriptorFlags: func(context.Context, int) (int, error) { return unix.FD_CLOEXEC, nil },
		deriveLocator:   func(context.Context, int) (instanceProcessLocator, error) { return record.launch.relay.locator, nil },
		terminatePIDFD: func(_ context.Context, fd int) error {
			// The receiver must have published complete stopping intent before the
			// first signal. The Firecracker identity and VM lifecycle are retained.
			want := cloneInstanceRecord(record)
			want.active.relay.lifecycle = instanceRelayStopping
			assertInstanceRecordEqual(t, want, f.read(t))
			f.signaled = append(f.signaled, fd)
			return nil
		},
		waitReapPIDFD: func(context.Context, int) error { f.alive = false; return nil },
		close:         func(_ context.Context, fd int) error { f.closed = append(f.closed, fd); return nil },
	}
	t.Cleanup(func() { require.NoError(t, f.locked.Release(context.Background())) })
	return f
}

func (f *consoleStopFixture) read(t *testing.T) instanceRecord {
	t.Helper()
	raw, err := os.ReadFile(f.path)
	require.NoError(t, err)
	record, err := decodeInstanceRecord(raw)
	require.NoError(t, err)
	return record
}

func TestConsoleStopDurableRelayOnlyTransition(t *testing.T) {
	t.Parallel()
	f := newConsoleStopFixture(t)
	want := cloneInstanceRecord(f.locked.record)
	result, err := f.server.stopLocked(t.Context(), f.locked)
	require.NoError(t, err)
	assert.Equal(t, ConsoleStopResult{VMID: want.vmID, Stopped: true}, result)
	want.active.relay.lifecycle = instanceRelayStopped
	assertInstanceRecordEqual(t, want, f.read(t))
	assert.Equal(
		t,
		[]int{want.launch.relay.locator.pid},
		f.opened,
		"Firecracker PID must never be acquired for relay stop",
	)
	assert.Equal(t, []int{700}, f.signaled)
	assert.Equal(t, []int{700}, f.closed)
	assert.False(t, f.alive)

	// A stopped journal must not acquire a now-reused PID on a repeat request.
	f.server.recovery.pidfdOpen = func(context.Context, int) (int, error) {
		t.Fatal("stopped relay must not reopen any PID")
		return -1, nil
	}
	result, err = f.server.stopLocked(t.Context(), f.locked)
	require.NoError(t, err)
	assert.False(t, result.Stopped)
	assertInstanceRecordEqual(t, want, f.read(t))
}

func TestConsoleStopRefusesUnprovenIdentityBeforeMutation(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*consoleStopFixture){
		"reused PID": func(f *consoleStopFixture) {
			f.server.recovery.deriveLocator = func(context.Context, int) (instanceProcessLocator, error) {
				locator := f.locked.record.launch.relay.locator
				locator.startTimeTicks++
				return locator, nil
			}
		},
		"different root": func(f *consoleStopFixture) { f.locked.record.active.relay.root.inode++ },
		"different executable digest": func(f *consoleStopFixture) {
			f.locked.record.active.relay.executable.sha256 = testRelayHash
		},
		"prior boot": func(f *consoleStopFixture) {
			f.server.identity.readBootID = func(context.Context) (string, error) { return processOtherBootID, nil }
		},
		"dead reused zombie": func(f *consoleStopFixture) { f.alive = false },
		"missing verifier":   func(f *consoleStopFixture) { f.server.identity.pread = nil },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			f := newConsoleStopFixture(t)
			before, err := os.ReadFile(f.path)
			require.NoError(t, err)
			change(f)
			_, err = f.server.stopLocked(t.Context(), f.locked)
			require.Error(t, err)
			assert.Empty(t, f.signaled)
			after, err := os.ReadFile(f.path)
			require.NoError(t, err)
			assert.Empty(t, cmp.Diff(before, after), "failed identity admission must not mutate the journal")
		})
	}
}

func TestConsoleStopPublicationErrorsPreventSignal(t *testing.T) {
	t.Parallel()
	for _, afterRename := range []bool{false, true} {
		name := "file sync"
		if afterRename {
			name = "directory sync after rename"
		}
		t.Run(name, func(t *testing.T) {
			f := newConsoleStopFixture(t)
			injected := errors.New("stop publication failed")
			fsync := f.locked.directories.deps.fsync
			f.locked.directories.deps.fsync = func(ctx context.Context, fd int) error {
				if (fd == f.locked.directories.stateFD) == afterRename {
					return injected
				}
				return fsync(ctx, fd)
			}
			_, err := f.server.stopLocked(t.Context(), f.locked)
			require.ErrorIs(t, err, injected)
			assert.Empty(t, f.signaled)
			want := cloneInstanceRecord(f.locked.record)
			if afterRename {
				want.active.relay.lifecycle = instanceRelayStopping
			}
			assertInstanceRecordEqual(t, want, f.read(t))

			// A retry after an uncertain rename must prove stopping durability again.
			if afterRename {
				f.locked.record = f.read(t)
				_, err = f.server.stopLocked(t.Context(), f.locked)
				require.ErrorIs(t, err, injected)
				assert.Empty(t, f.signaled)
			}
		})
	}
}

func TestConsoleStopInterruptedWaitRetainsIdentityAndRetryCompletes(t *testing.T) {
	t.Parallel()
	f := newConsoleStopFixture(t)
	injected := errors.New("wait interrupted")
	f.server.recovery.waitReapPIDFD = func(context.Context, int) error { return injected }
	_, err := f.server.stopLocked(t.Context(), f.locked)
	require.ErrorIs(t, err, injected)
	want := cloneInstanceRecord(f.locked.record)
	want.active.relay.lifecycle = instanceRelayStopping
	assertInstanceRecordEqual(t, want, f.read(t))

	// The same recorded task has exited and has been reaped by its parent. A
	// fresh retry may finish the journal, but it acquires and signals no new task.
	f.locked.record = f.read(t)
	f.server.recovery.pidfdOpen = func(context.Context, int) (int, error) { return -1, unix.ESRCH }
	result, err := f.server.stopLocked(t.Context(), f.locked)
	require.NoError(t, err)
	assert.True(t, result.Stopped)
	want.active.relay.lifecycle = instanceRelayStopped
	assertInstanceRecordEqual(t, want, f.read(t))
	assert.Equal(t, []int{700}, f.signaled, "retry must not signal after proven process absence")
}

func TestConsoleStopRequiresActualExitBeforeTerminalJournal(t *testing.T) {
	t.Parallel()
	f := newConsoleStopFixture(t)
	f.server.recovery.waitReapPIDFD = func(context.Context, int) error { return nil }
	_, err := f.server.stopLocked(t.Context(), f.locked)
	require.Error(t, err)
	want := cloneInstanceRecord(f.locked.record)
	want.active.relay.lifecycle = instanceRelayStopping
	assertInstanceRecordEqual(t, want, f.read(t))
	assert.True(t, f.alive)
}

func TestConsoleObserveDoesNotCompleteInterruptedStop(t *testing.T) {
	t.Parallel()
	f := newConsoleStopFixture(t)
	f.locked.record.active.relay.lifecycle = instanceRelayStopping
	require.NoError(t, f.locked.directories.writeRecord(t.Context(), f.locked.record))
	before, err := os.ReadFile(f.path)
	require.NoError(t, err)
	f.server.recovery.pidfdOpen = func(context.Context, int) (int, error) { return -1, unix.ESRCH }
	// Missing mutation callbacks make accidental repair or terminal publication
	// fail immediately. Observation needs only existing root authority.
	f.server.authority.deps.mkdirAt = nil
	f.server.authority.deps.write = nil
	f.server.authority.deps.renameAt = nil
	f.server.authority.deps.unlinkAt = nil
	result, err := f.server.Observe(t.Context(), ConsoleCaller{UID: testAuthorityUID, GID: testAuthorityGID},
		ConsoleObserveRequest{VMID: f.locked.record.vmID})
	require.NoError(t, err)
	assert.Equal(t, ConsoleObserveResult{VMID: f.locked.record.vmID, State: ConsoleStopping}, result)
	assert.Empty(t, f.signaled)
	after, err := os.ReadFile(f.path)
	require.NoError(t, err)
	assert.Empty(t, cmp.Diff(before, after))
}

func TestStoppedRelayDoesNotBlockExactFirecrackerRecovery(t *testing.T) {
	t.Parallel()
	f := newConsoleStopFixture(t)
	record := cloneInstanceRecord(f.locked.record)
	record.active.relay.lifecycle = instanceRelayStopped
	delete(f.identity.processDirectories, record.launch.relay.locator.pid)
	f.server.recovery.deriveLocator = func(context.Context, int) (instanceProcessLocator, error) {
		return record.launch.process, nil
	}
	processes, err := reopenRecordedProcesses(t.Context(), f.server.recovery, record)
	require.NoError(t, err)
	defer func() { require.NoError(t, processes.Release(t.Context())) }()
	require.NoError(t, validateVMExecRetainedProcessAuthority(processes, record, "source"))
	observed, err := verifyRecoveredActiveIdentity(t.Context(), f.identity.deps(), record)
	require.NoError(t, err)
	assert.Equal(t, record.launch.process.startTimeTicks, observed.startTimeTicks)
	assert.Equal(t, record.active.userIDs, observed.userIDs)
	assert.Equal(t, []int{record.launch.process.pid}, f.opened)
	assert.Empty(t, f.signaled)
}
