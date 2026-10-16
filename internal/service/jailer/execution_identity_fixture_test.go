package jailer

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// testInstanceAuthorityDeps keeps account effects in the same private root as the authority.
// Files model host account persistence so separately reopened fixture authorities share accounts.
func testInstanceAuthorityDeps(t *testing.T, root string) instanceAuthorityDeps {
	t.Helper()
	deps := realInstanceAuthorityDeps()
	accountRoot := filepath.Join(root, "fixture-accounts")
	accountPath := func(name string) string { return filepath.Join(accountRoot, name+".json") }
	requireRecord := func(ctx context.Context, record executionIdentityRecord) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		raw, err := os.ReadFile(accountPath(record.Name))
		if err != nil {
			return err
		}
		var account executionIdentityRecord
		if err := json.Unmarshal(raw, &account); err != nil {
			return err
		}
		if record.UID == nil || record.GID == nil || account.UID == nil || account.GID == nil ||
			account.OwnerUID != record.OwnerUID || account.VMID != record.VMID ||
			account.Name != record.Name || account.Nonce != record.Nonce ||
			*account.UID != *record.UID || *account.GID != *record.GID {
			return instanceStateError(record.VMID, "fixture execution account does not match reservation")
		}
		return nil
	}
	deps.accounts = executionIdentityAccountDeps{
		requireHostTools: func(ctx context.Context) error { return ctx.Err() },
		requireNameAbsent: func(ctx context.Context, name string) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			_, err := os.Lstat(accountPath(name))
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			return instanceStateError("", "fixture execution account already exists")
		},
		create: func(ctx context.Context, record executionIdentityRecord, caller instanceCaller) (vmExecutionIdentity, error) {
			if err := ctx.Err(); err != nil {
				return vmExecutionIdentity{}, err
			}
			identity := vmExecutionIdentity{uid: testExecutionUID, gid: testExecutionGID}
			record.Phase, record.UID, record.GID = executionIdentityReady, &identity.uid, &identity.gid
			raw, err := json.Marshal(record)
			if err != nil {
				return vmExecutionIdentity{}, err
			}
			if err := os.MkdirAll(accountRoot, 0700); err != nil {
				return vmExecutionIdentity{}, err
			}
			if err := os.WriteFile(accountPath(record.Name), raw, 0600); err != nil {
				return vmExecutionIdentity{}, err
			}
			return identity, nil
		},
		require: func(ctx context.Context, record executionIdentityRecord, caller instanceCaller) error {
			if record.UID == nil || record.GID == nil || *record.UID == caller.uid || *record.GID == caller.gid {
				return instanceStateError(record.VMID, "fixture execution credentials overlap operator")
			}
			return requireRecord(ctx, record)
		},
		requireRecord:         requireRecord,
		requireProcessAbsence: func(ctx context.Context, _ vmExecutionIdentity) error { return ctx.Err() },
		remove: func(ctx context.Context, record executionIdentityRecord) error {
			if err := requireRecord(ctx, record); err != nil {
				return err
			}
			return os.Remove(accountPath(record.Name))
		},
	}
	return deps
}

// Rationale: a private instance root must also own its allocator, account adapter and grant
// inventory. Two fixtures using the same VM ID must not share durable authority or host accounts.
func TestLaunchAuthorityIndependentRootsRetainIndependentExecutionReservations(t *testing.T) {
	t.Parallel()
	if os.Geteuid() != 0 || os.Getegid() != 0 {
		t.Skip("launch authority filesystem checks require root")
	}
	// Compare only bounded digests and metadata: account contents must never enter failure output.
	type hostState struct {
		Exists  bool
		Mode    uint32
		UID     uint32
		GID     uint32
		Device  uint64
		Inode   uint64
		Size    int64
		Digest  string
		Entries []string
	}
	readHostState := func() map[string]hostState {
		paths := []string{
			"/etc/passwd", "/etc/group", "/etc/shadow", "/etc/gshadow",
			"/var/lib/mvmctl/execution-identities",
			"/var/lib/mvmctl/execution-identities/allocator.lock",
			"/var/lib/mvmctl/execution-identities/" + testVMID + ".json",
			"/var/lib/mvmctl/protected-backing/1000/logical",
		}
		for _, family := range []string{"cloud-init", "kernel", "rootfs", "volume"} {
			paths = append(paths, "/var/lib/mvmctl/protected-backing/1000/logical/"+family)
		}
		states := make(map[string]hostState, len(paths))
		for _, path := range paths {
			var stat unix.Stat_t
			err := unix.Lstat(path, &stat)
			if errors.Is(err, unix.ENOENT) {
				states[path] = hostState{}
				continue
			}
			require.NoError(t, err)
			state := hostState{
				Exists: true, Mode: stat.Mode, UID: stat.Uid, GID: stat.Gid,
				Device: uint64(stat.Dev), Inode: stat.Ino, Size: stat.Size,
			}
			switch stat.Mode & unix.S_IFMT {
			case unix.S_IFREG:
				require.LessOrEqual(t, stat.Size, int64(1<<20), "host proof input exceeds bound: %s", path)
				file, err := os.Open(path)
				require.NoError(t, err)
				digest := sha256.New()
				n, readErr := io.Copy(digest, io.LimitReader(file, (1<<20)+1))
				closeErr := file.Close()
				require.NoError(t, readErr)
				require.NoError(t, closeErr)
				require.LessOrEqual(t, n, int64(1<<20))
				state.Digest = fmt.Sprintf("%x", digest.Sum(nil))
			case unix.S_IFDIR:
				entries, err := os.ReadDir(path)
				require.NoError(t, err)
				for _, entry := range entries {
					state.Entries = append(state.Entries, entry.Name())
				}
			}
			states[path] = state
		}
		return states
	}
	hostBefore := readHostState()
	t.Cleanup(func() {
		if diff := cmp.Diff(hostBefore, readHostState()); diff != "" {
			t.Errorf("host accounts or authority paths changed (-before +after):\n%s", diff)
		}
	})
	first, firstRoot := newTestInstanceAuthority(t)
	second, secondRoot := newTestInstanceAuthority(t)
	firstLaunch, err := registerTestLaunch(t, t.Context(), first, testInstanceCaller(), testLaunchIntent(false))
	require.NoError(t, err)
	secondLaunch, err := registerTestLaunch(t, t.Context(), second, testInstanceCaller(), testLaunchIntent(false))
	require.NoError(t, err)
	readReservation := func(root string) executionIdentityRecord {
		raw, err := os.ReadFile(filepath.Join(root, "var/lib/mvmctl/execution-identities", testVMID+".json"))
		require.NoError(t, err)
		var record executionIdentityRecord
		require.NoError(t, json.Unmarshal(raw, &record))
		return record
	}
	firstRecord, secondRecord := readReservation(firstRoot), readReservation(secondRoot)
	for _, root := range []string{firstRoot, secondRoot} {
		t.Run(filepath.Base(root), func(t *testing.T) {
			record := readReservation(root)
			identityRoot := filepath.Join(root, "var/lib/mvmctl/execution-identities")
			assertExactMode(t, identityRoot, 0700)
			for _, name := range []string{testVMID + ".json", "allocator.lock"} {
				path := filepath.Join(identityRoot, name)
				assertExactMode(t, path, 0600)
				var stat unix.Stat_t
				require.NoError(t, unix.Lstat(path, &stat))
				assert.Equal(t, uint32(0), stat.Uid)
				assert.Equal(t, uint32(0), stat.Gid)
			}
			assert.Equal(t, uint32(1), record.Schema)
			assert.Equal(t, uint32(1000), record.OwnerUID)
			assert.Equal(t, testVMID, record.VMID)
			assert.Equal(t, executionIdentityReady, record.Phase)
			require.NotNil(t, record.UID)
			require.NotNil(t, record.GID)
			assert.Equal(t, testExecutionUID, *record.UID)
			assert.Equal(t, testExecutionGID, *record.GID)
			require.NoError(
				t,
				testInstanceAuthorityDeps(t, root).accounts.require(t.Context(), record, testInstanceCaller()),
			)
			entries, err := os.ReadDir(filepath.Join(root, "var/lib/mvmctl/protected-backing/1000/logical"))
			require.NoError(t, err)
			names := make([]string, 0, len(entries))
			for _, entry := range entries {
				names = append(names, entry.Name())
			}
			if diff := cmp.Diff([]string{"cloud-init", "kernel", "rootfs", "volume"}, names); diff != "" {
				t.Errorf("grant inventory families (-want +got):\n%s", diff)
			}
		})
	}
	assert.NotEqual(t, firstRecord.Nonce, secondRecord.Nonce)
	releaseTestLaunch(t, firstLaunch)
	releaseTestLaunch(t, secondLaunch)
	if diff := cmp.Diff(firstRecord, readReservation(firstRoot)); diff != "" {
		t.Errorf("first stopped reservation changed (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(secondRecord, readReservation(secondRoot)); diff != "" {
		t.Errorf("second stopped reservation changed (-want +got):\n%s", diff)
	}
}
