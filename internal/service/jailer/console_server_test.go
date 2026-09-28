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

	"mvmctl/pkg/errs"
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

// forbidConsoleProcessAndEndpointAccess makes an accidental prior-boot process or socket admission fail immediately.
func forbidConsoleProcessAndEndpointAccess(t *testing.T, f *consoleStopFixture) {
	t.Helper()
	f.server.recovery.pidfdOpen = func(context.Context, int) (int, error) {
		t.Fatal("inactive boot authority must not open any PID")
		return -1, nil
	}
	f.server.identity.openProcessDirectory = func(context.Context, int) (int, error) {
		t.Fatal("inactive boot authority must not inspect a current process")
		return -1, nil
	}
	f.server.runtime = realManagedVMRuntimeDeps()
	f.server.runtime.open = func(context.Context, string, int, uint32) (int, error) {
		t.Fatal("inactive boot authority must not open the console runtime or socket")
		return -1, nil
	}
	// Existing-only observation must remain usable without any journal mutation capability.
	f.server.authority.deps.mkdirAt = nil
	f.server.authority.deps.write = nil
	f.server.authority.deps.renameAt = nil
	f.server.authority.deps.unlinkAt = nil
}

func TestConsoleObservePriorBootIsStoppedWithoutProcessAccess(t *testing.T) {
	t.Parallel()
	// Rationale: a valid prior-boot journal proves this relay inactive even when its PID is reused on the current boot.
	tests := map[string]instanceRecord{
		"running relay":          testActiveInstanceRecord(true),
		"stopping relay":         testActiveInstanceRecord(true),
		"stopped relay":          testActiveInstanceRecord(true),
		"active absent relay":    testActiveInstanceRecord(false),
		"launching absent relay": testLaunchingInstanceRecordWithRelay(false),
		"cleaning absent relay":  runtimeStatusCleaningRecord(),
	}
	record := tests["stopping relay"]
	record.active.relay.lifecycle = instanceRelayStopping
	tests["stopping relay"] = record
	record = tests["stopped relay"]
	record.active.relay.lifecycle = instanceRelayStopped
	tests["stopped relay"] = record
	for name, record := range tests {
		t.Run(name, func(t *testing.T) {
			f := newConsoleStopFixture(t)
			f.locked.record = record
			require.NoError(t, f.locked.directories.writeRecord(t.Context(), f.locked.record))
			before, err := os.ReadFile(f.path)
			require.NoError(t, err)
			f.server.identity.readBootID = func(context.Context) (string, error) { return processOtherBootID, nil }
			forbidConsoleProcessAndEndpointAccess(t, f)
			result, err := f.server.Observe(t.Context(), ConsoleCaller{UID: testAuthorityUID, GID: testAuthorityGID},
				ConsoleObserveRequest{VMID: f.locked.record.vmID})
			require.NoError(t, err)
			want := ConsoleObserveResult{VMID: f.locked.record.vmID, State: ConsoleStopped}
			if diff := cmp.Diff(want, result); diff != "" {
				t.Errorf("prior-boot console observation mismatch (-want +got):\n%s", diff)
			}
			require.NoError(t, result.Validate(ConsoleObserveRequest{VMID: f.locked.record.vmID}))
			assert.Empty(t, f.signaled)
			after, err := os.ReadFile(f.path)
			require.NoError(t, err)
			if diff := cmp.Diff(before, after); diff != "" {
				t.Errorf("console observation changed the journal (-want +got):\n%s", diff)
			}
		})
	}
}

func TestConsoleObserveSameBootIncompleteLifecycleRemainsUnavailable(t *testing.T) {
	t.Parallel()
	// Rationale: relaxing prior-boot classification must not treat interrupted same-boot authority as stopped.
	tests := map[string]instanceRecord{
		"launching": testLaunchingInstanceRecordWithRelay(false),
		"cleaning":  runtimeStatusCleaningRecord(),
	}
	for name, record := range tests {
		t.Run(name, func(t *testing.T) {
			f := newConsoleStopFixture(t)
			f.locked.record = record
			require.NoError(t, f.locked.directories.writeRecord(t.Context(), record))
			before, err := os.ReadFile(f.path)
			require.NoError(t, err)
			forbidConsoleProcessAndEndpointAccess(t, f)
			_, err = f.server.Observe(t.Context(), ConsoleCaller{UID: testAuthorityUID, GID: testAuthorityGID},
				ConsoleObserveRequest{VMID: record.vmID})
			require.Error(t, err)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, errs.CodeConsoleRelayFailed, domainErr.Code)
			assert.Empty(t, f.signaled)
			after, err := os.ReadFile(f.path)
			require.NoError(t, err)
			if diff := cmp.Diff(before, after); diff != "" {
				t.Errorf("incomplete console observation changed the journal (-want +got):\n%s", diff)
			}
		})
	}
}

func TestConsoleObserveUnavailableBootDoesNotProveStopped(t *testing.T) {
	t.Parallel()
	// Rationale: only a successfully read valid boot identity can prove that the recorded relay is from another boot.
	injected := errors.New("boot identity read failed")
	tests := map[string]struct {
		readBootID func(context.Context) (string, error)
		wantCause  error
	}{
		"missing reader": {},
		"read failure with valid different boot": {
			readBootID: func(context.Context) (string, error) { return processOtherBootID, injected },
			wantCause:  injected,
		},
		"malformed boot": {
			readBootID: func(context.Context) (string, error) { return "not-a-boot-id", nil },
		},
		"empty boot": {readBootID: func(context.Context) (string, error) { return "", nil }},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			f := newConsoleStopFixture(t)
			before, err := os.ReadFile(f.path)
			require.NoError(t, err)
			f.server.identity.readBootID = tc.readBootID
			forbidConsoleProcessAndEndpointAccess(t, f)
			_, err = f.server.Observe(t.Context(), ConsoleCaller{UID: testAuthorityUID, GID: testAuthorityGID},
				ConsoleObserveRequest{VMID: f.locked.record.vmID})
			require.Error(t, err)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, errs.CodeConsoleRelayFailed, domainErr.Code)
			if tc.wantCause != nil {
				assert.ErrorIs(t, err, tc.wantCause)
			}
			assert.Empty(t, f.signaled)
			after, err := os.ReadFile(f.path)
			require.NoError(t, err)
			if diff := cmp.Diff(before, after); diff != "" {
				t.Errorf("failed console observation changed the journal (-want +got):\n%s", diff)
			}
		})
	}
}

func TestConsoleStopPriorBootDoesNotAcquireOrSignalCurrentProcess(t *testing.T) {
	t.Parallel()
	// Rationale: the read-only stopped classification must not relax signal authority for a reused current-boot PID.
	f := newConsoleStopFixture(t)
	before, err := os.ReadFile(f.path)
	require.NoError(t, err)
	f.server.identity.readBootID = func(context.Context) (string, error) { return processOtherBootID, nil }
	forbidConsoleProcessAndEndpointAccess(t, f)
	_, err = f.server.Stop(t.Context(), ConsoleCaller{UID: testAuthorityUID, GID: testAuthorityGID},
		ConsoleStopRequest{VMID: f.locked.record.vmID})
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeConsoleRelayFailed, domainErr.Code)
	assert.Empty(t, f.signaled)
	after, err := os.ReadFile(f.path)
	require.NoError(t, err)
	if diff := cmp.Diff(before, after); diff != "" {
		t.Errorf("rejected prior-boot console stop changed the journal (-want +got):\n%s", diff)
	}
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
