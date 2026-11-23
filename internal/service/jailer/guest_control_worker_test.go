package jailer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/system"
)

func guestControlWorkerTestRecord() guestControlWorkerRecord {
	return guestControlWorkerRecord{Schema: 1, ResourceOwnerUID: 1000, ResourceOwnerGID: 1000,
		VMID:   strings.Repeat("a", 32),
		BootID: "11111111-1111-4111-8111-111111111111", LaunchGeneration: 7, ExecutionUID: 1001,
		Generation: 1, Revision: 1, Phase: "prepared"}
}

func guestControlWorkerTestProcess() guestControlWorkerProcessRecord {
	return guestControlWorkerProcessRecord{PID: 99, StartTimeTicks: 123, ExpectedUID: 0, ExpectedGID: 0,
		Executable: system.PinnedExecutableIdentity{Device: 1, Inode: 2, SizeBytes: 100, Mode: unix.S_IFREG | 0755,
			UID: 0, GID: 0, Links: 1, SHA256: strings.Repeat("b", 64)},
		MountDevice: 3, MountInode: 4, NetworkDevice: 3, NetworkInode: 5, RootDevice: 6, RootInode: 7}
}

func TestGuestControlWorkerRecordRejectsForeignAndNoncanonicalAuthority(t *testing.T) {
	record := guestControlWorkerTestRecord()
	raw, err := encodeGuestControlWorkerRecord(record)
	require.NoError(t, err)
	got, err := decodeGuestControlWorkerRecord(raw)
	require.NoError(t, err)
	if diff := cmp.Diff(record, got); diff != "" {
		t.Errorf("worker roundtrip (-want +got):\n%s", diff)
	}
	for name, mutate := range map[string]func(*guestControlWorkerRecord){
		"operator root":           func(r *guestControlWorkerRecord) { r.ResourceOwnerUID = 0 },
		"operator execution":      func(r *guestControlWorkerRecord) { r.ExecutionUID = r.ResourceOwnerUID },
		"unknown ready state":     func(r *guestControlWorkerRecord) { r.Phase = "ready" },
		"spawned without process": func(r *guestControlWorkerRecord) { r.Phase = "spawned" },
		"prepared with process": func(r *guestControlWorkerRecord) {
			p := guestControlWorkerTestProcess()
			r.Process = &p
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := record
			mutate(&r)
			_, err := encodeGuestControlWorkerRecord(r)
			require.Error(t, err)
		})
	}
	for name, malformed := range map[string][]byte{
		"unknown field": append(append([]byte(nil), raw[:len(raw)-1]...), []byte(",\"extra\":true}")...),
		"whitespace":    append([]byte(" "), raw...),
		"duplicate":     append(append([]byte(nil), raw[:len(raw)-1]...), []byte(",\"schema\":1}")...),
	} {
		t.Run(name, func(t *testing.T) { _, err := decodeGuestControlWorkerRecord(malformed); require.Error(t, err) })
	}
}

func TestGuestControlWorkerTransitionsFenceBeforeRetirementAndAdvanceGeneration(t *testing.T) {
	prepared := guestControlWorkerTestRecord()
	spawned := prepared
	spawned.Revision++
	spawned.Phase = "spawned"
	process := guestControlWorkerTestProcess()
	spawned.Process = &process
	stopping := spawned
	stopping.Revision++
	stopping.Phase = "stopping"
	stopped := stopping
	stopped.Revision++
	stopped.Phase = "stopped"
	next := prepared
	next.Revision = stopped.Revision + 1
	next.Generation++
	next.LaunchGeneration++
	for _, pair := range [][2]guestControlWorkerRecord{
		{prepared, spawned}, {spawned, stopping}, {stopping, stopped}, {stopped, next},
	} {
		require.NoError(t, validateGuestControlWorkerTransition(pair[0], true, pair[1]))
	}
	for name, pair := range map[string][2]guestControlWorkerRecord{
		"unfenced retirement":                {spawned, stopped},
		"blind spawn replay":                 {spawned, spawned},
		"generation rollback":                {stopped, prepared},
		"incarnation replacement while live": {spawned, next},
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, validateGuestControlWorkerTransition(pair[0], true, pair[1]))
		})
	}
	rebound := stopping
	rebound.Process = new(guestControlWorkerProcessRecord)
	*rebound.Process = process
	rebound.Process.PID++
	require.Error(t, validateGuestControlWorkerTransition(spawned, true, rebound))
}

func guestControlWorkerStoreFixture(t *testing.T) *guestControlWorkerStore {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "var/lib"), 0755))
	store, err := openGuestControlWorkerStore(t.Context(), realInstanceAuthorityDeps(), instanceAuthorityPolicy{
		rootPath: root, expectedUID: uint32(os.Geteuid()), expectedGID: uint32(os.Getegid())},
		1000, strings.Repeat("a", 32))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.close(context.Background())) })
	return store
}

func TestGuestControlWorkerStorePublishesDurableCanonicalLifecycle(t *testing.T) {
	store := guestControlWorkerStoreFixture(t)
	record := guestControlWorkerTestRecord()
	require.NoError(t, store.persist(t.Context(), record))
	got, raw, found, err := store.read(t.Context())
	require.NoError(t, err)
	require.True(t, found)
	if diff := cmp.Diff(record, got); diff != "" {
		t.Errorf("worker readback (-want +got):\n%s", diff)
	}
	var decoded map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &decoded))
	var phase string
	require.NoError(t, json.Unmarshal(decoded["phase"], &phase))
	require.Equal(t, "prepared", phase)
	stopping := record
	stopping.Phase = "stopping"
	stopping.Revision++
	require.NoError(t, store.persist(t.Context(), stopping))
	stopped := stopping
	stopped.Phase = "stopped"
	stopped.Revision++
	require.NoError(t, store.persist(t.Context(), stopped))
	require.NoError(t, store.close(t.Context()))
	reopened, err := openGuestControlWorkerStore(t.Context(), realInstanceAuthorityDeps(),
		store.directories.policy, 1000, record.VMID)
	require.NoError(t, err)
	defer reopened.close(context.Background())
	if diff := cmp.Diff(stopped, reopened.record); diff != "" {
		t.Errorf("durable retirement (-want +got):\n%s", diff)
	}
	require.Error(t, reopened.persist(t.Context(), record), "retired generation must not replay")
}

func TestGuestControlWorkerStoreDetectsReboundRecordAndUnknownPublication(t *testing.T) {
	for _, name := range []string{"record replaced", "directory sync unknown"} {
		t.Run(name, func(t *testing.T) {
			store := guestControlWorkerStoreFixture(t)
			record := guestControlWorkerTestRecord()
			require.NoError(t, store.persist(t.Context(), record))
			target := record
			target.Phase = "stopping"
			target.Revision++
			if name == "record replaced" {
				foreign := record
				foreign.Generation++
				foreign.Revision++
				raw, err := encodeGuestControlWorkerRecord(foreign)
				require.NoError(t, err)
				fd, err := unix.Openat(store.directories.stateFD, record.VMID+".json",
					unix.O_WRONLY|unix.O_TRUNC|unix.O_NOFOLLOW, 0)
				require.NoError(t, err)
				_, err = unix.Write(fd, raw)
				require.NoError(t, err)
				require.NoError(t, unix.Close(fd))
			} else {
				original := store.directories.deps.fsync
				store.directories.deps.fsync = func(ctx context.Context, fd int) error {
					if fd == store.directories.stateFD {
						return errors.New("directory sync unknown")
					}
					return original(ctx, fd)
				}
			}
			require.Error(t, store.persist(t.Context(), target))
			if name == "directory sync unknown" {
				require.True(t, store.uncertain)
				require.Error(t, store.persist(t.Context(), target),
					"unknown publication must not replay in the same lease")
				observed, _, found, err := store.read(t.Context())
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, "stopping", observed.Phase, "visible publication can precede sync failure")
			}
		})
	}
}

func TestGuestControlWorkerProcessRequiresRootExecutionSeparateFromOperator(t *testing.T) {
	for _, name := range []string{"valid", "operator UID", "operator GID", "rebound executable",
		"writable executable", "missing hash", "missing locator"} {
		t.Run(name, func(t *testing.T) {
			p := guestControlWorkerTestProcess()
			switch name {
			case "operator UID":
				p.ExpectedUID = 1000
			case "operator GID":
				p.ExpectedGID = 1000
			case "rebound executable":
				p.Executable.UID = 1000
			case "writable executable":
				p.Executable.Mode = unix.S_IFREG | 0777
			case "missing hash":
				p.Executable.SHA256 = ""
			case "missing locator":
				p.StartTimeTicks = 0
			}
			err := validateGuestControlWorkerProcessRecord(p)
			if name == "valid" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestGuestControlWorkerLifecycleRejectsMissingGenuineAuthority(t *testing.T) {
	var lease *GuestControlWorkerLease
	require.Error(t, lease.Prepare(t.Context()))
	require.Error(t, lease.RecordSpawn(t.Context(), &GuestControlWorkerProcess{}))
	require.Error(t, lease.StopVerified(t.Context()))
	require.NoError(t, lease.Release(t.Context()))
	_, err := NewGuestControlWorkerServer().Lock(t.Context(), &FloatingIPAdmissionLease{})
	require.Error(t, err)
}

func TestGuestControlWorkerStoreRejectsForeignRecordAndHardlinkedLockWithoutRepair(t *testing.T) {
	for _, name := range []string{"symlink record", "hardlinked lock"} {
		t.Run(name, func(t *testing.T) {
			store := guestControlWorkerStoreFixture(t)
			policy := store.directories.policy
			vmID := store.vmID
			require.NoError(t, store.close(t.Context()))
			directory := filepath.Join(policy.rootPath, "var/lib/mvmctl/guest-control/1000")
			foreign := filepath.Join(policy.rootPath, "foreign")
			if name == "symlink record" {
				require.NoError(t, os.WriteFile(foreign, []byte("foreign authority"), 0600))
				require.NoError(t, os.Symlink(foreign, filepath.Join(directory, vmID+".json")))
			} else {
				require.NoError(t, os.Link(filepath.Join(directory, vmID+".lock"), foreign))
			}
			rejected, err := openGuestControlWorkerStore(t.Context(), realInstanceAuthorityDeps(), policy, 1000, vmID)
			require.Error(t, err)
			require.Nil(t, rejected)
			if name == "symlink record" {
				raw, err := os.ReadFile(foreign)
				require.NoError(t, err)
				require.Equal(t, "foreign authority", string(raw))
			} else {
				var stat unix.Stat_t
				require.NoError(t, unix.Stat(foreign, &stat))
				require.EqualValues(t, 2, stat.Nlink)
			}
		})
	}
}

func TestGuestControlWorkerProcessRefusesReboundPIDLocatorWithoutAdoption(t *testing.T) {
	boot, err := realProcessIdentityVerifierDeps().readBootID(t.Context())
	require.NoError(t, err)
	record := guestControlWorkerTestRecord()
	record.BootID = boot
	process := guestControlWorkerTestProcess()
	process.PID = os.Getpid()
	process.StartTimeTicks = 1
	record.Phase = "spawned"
	record.Process = &process
	fd, absent, err := reopenGuestControlWorkerProcess(t.Context(), record)
	require.Error(t, err)
	require.False(t, absent)
	require.Equal(t, -1, fd)
	self, err := unix.PidfdOpen(os.Getpid(), 0)
	require.NoError(t, err)
	defer unix.Close(self)
	alive, err := processPIDFDAlive(t.Context(), self)
	require.NoError(t, err)
	require.True(t, alive)
}
