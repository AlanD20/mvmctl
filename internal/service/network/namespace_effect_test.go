package network

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

type namespaceEffectTestContextKey struct{}

// Rationale: success is authoritative only after exact publication, host restoration, and reverse local cleanup.
func TestNamespaceEffectPublishesAndRemovesDerivedNamespace(t *testing.T) {
	t.Parallel()

	fixture := newNamespaceEffectFixture()
	effect := newNamespaceEffectWithDeps(fixture.dependencies(), fixture.authority(), fixture.policy())
	key := mustTopologyKey(t, testOwnerUID, testVMID, testNetworkID)

	pin, err := effect.CreateNamespace(t.Context(), namespaceCreateSpec{key: key})
	require.NoError(t, err)
	require.NotNil(t, pin)
	wantHandle := namespaceHandle{
		path:     deriveTopologyNames(key).namespaceHandle,
		identity: namespaceIdentity{device: 5, inode: 400},
		ownerUID: 0,
		ownerGID: 0,
		mode:     uint32(0o444),
	}
	if diff := cmp.Diff(
		wantHandle,
		pin.Handle(),
		cmp.AllowUnexported(namespaceHandle{}, namespaceIdentity{}),
	); diff != "" {
		t.Errorf("namespace handle mismatch (-want +got):\n%s", diff)
	}
	wantCreateEvents := []string{
		"worker.enter",
		"authority.create",
		"authority.target_identity",
		"open.root",
		"fstat.10",
		"openat.parent",
		"fstat.11",
		"statx.11",
		"openat.placeholder",
		"fstat.12",
		"statx.12",
		"close.10",
		"thread.lock",
		"open.thread-net.1",
		"fstat.13",
		"fstatfs.13",
		"nstype.13",
		"unshare",
		"open.thread-net.2",
		"fstat.14",
		"fstatfs.14",
		"nstype.14",
		"open_tree.14",
		"fstat.11",
		"statx.11",
		"openat.prepublish",
		"fstat.16",
		"statx.16",
		"move_mount.15.16",
		"fsync.11",
		"fstat.11",
		"statx.11",
		"openat.published",
		"fstat.17",
		"fstatfs.17",
		"nstype.17",
		"setns.13",
		"open.thread-net.3",
		"fstat.18",
		"fstatfs.18",
		"nstype.18",
		"close.18",
		"close.16",
		"close.15",
		"close.13",
		"thread.unlock",
		"worker.exit",
	}
	if diff := cmp.Diff(wantCreateEvents, fixture.eventsSnapshot()); diff != "" {
		t.Errorf("namespace create events mismatch (-want +got):\n%s", diff)
	}
	assert.False(t, fixture.workerViolation)

	createCount := len(fixture.eventsSnapshot())
	require.NoError(t, effect.DeleteNamespace(t.Context(), pin))
	wantDeleteEvents := []string{
		"fstat.11",
		"statx.11",
		"fstat.14",
		"fstatfs.14",
		"nstype.14",
		"fstat.17",
		"fstatfs.17",
		"nstype.17",
		"openat.current",
		"fstat.19",
		"fstatfs.19",
		"nstype.19",
		"close.19",
		"close.17",
		"unmount./proc/self/fd/11/" + testVMID,
		"authority.remove",
	}
	if diff := cmp.Diff(wantDeleteEvents, fixture.eventsSnapshot()[createCount:]); diff != "" {
		t.Errorf("namespace delete events mismatch (-want +got):\n%s", diff)
	}
	require.NoError(t, pin.Release(t.Context()))
	wantReleaseEvents := []string{"close.14", "close.12", "close.11", "authority.release"}
	gotReleaseEvents := fixture.eventsSnapshot()[createCount+len(wantDeleteEvents):]
	if diff := cmp.Diff(wantReleaseEvents, gotReleaseEvents); diff != "" {
		t.Errorf("namespace release events mismatch (-want +got):\n%s", diff)
	}
}

// Rationale: cancellation after any mutation boundary must preserve exact partial-state evidence and cleanup authority.
func TestNamespaceEffectCancellationRetainsOutcomeEvidence(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		cancelAfter string
		outcome     namespaceCreateOutcome
	}{
		"authority target": {
			cancelAfter: "authority.create",
			outcome:     namespaceCreateOutcome{cleanupRequired: true},
		},
		"target identity": {
			cancelAfter: "authority.target_identity",
			outcome:     namespaceCreateOutcome{cleanupRequired: true},
		},
		"thread lock": {
			cancelAfter: "thread.lock",
			outcome:     namespaceCreateOutcome{cleanupRequired: true},
		},
		"namespace creation": {
			cancelAfter: "unshare",
			outcome:     namespaceCreateOutcome{cleanupRequired: true, created: true, restored: true},
		},
		"namespace pin": {
			cancelAfter: "nstype.14",
			outcome: namespaceCreateOutcome{
				cleanupRequired: true, created: true, pinned: true, restored: true,
			},
		},
		"detached mount": {
			cancelAfter: "open_tree.14",
			outcome: namespaceCreateOutcome{
				cleanupRequired: true, created: true, pinned: true, restored: true,
			},
		},
		"reserved target recheck": {
			cancelAfter: "openat.prepublish",
			outcome: namespaceCreateOutcome{
				cleanupRequired: true, created: true, pinned: true, restored: true,
			},
		},
		"mount publication": {
			cancelAfter: "move_mount.15.16",
			outcome: namespaceCreateOutcome{
				cleanupRequired: true, created: true, pinned: true, published: true, restored: true,
			},
		},
		"published verification": {
			cancelAfter: "nstype.17",
			outcome: namespaceCreateOutcome{
				cleanupRequired: true,
				created:         true,
				pinned:          true,
				published:       true,
				verified:        true,
				restored:        true,
			},
		},
		"host restoration": {
			cancelAfter: "setns.13",
			outcome: namespaceCreateOutcome{
				cleanupRequired: true,
				created:         true,
				pinned:          true,
				published:       true,
				verified:        true,
				restored:        true,
			},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.WithValue(
				t.Context(),
				namespaceEffectTestContextKey{},
				"retained",
			))
			fixture := newNamespaceEffectFixture()
			fixture.cancelAfter = test.cancelAfter
			fixture.cancel = cancel
			effect := newNamespaceEffectWithDeps(fixture.dependencies(), fixture.authority(), fixture.policy())
			key := mustTopologyKey(t, testOwnerUID, testVMID, testNetworkID)

			pin, err := effect.CreateNamespace(ctx, namespaceCreateSpec{key: key})
			require.ErrorIs(t, err, context.Canceled)
			require.NotNil(t, pin)
			assert.Equal(t, namespaceHandle{}, pin.Handle())
			assertNamespaceCreateError(t, err, test.outcome)

			cleanupCtx := context.WithoutCancel(ctx)
			require.NoError(t, effect.DeleteNamespace(cleanupCtx, pin))
			require.NoError(t, pin.Release(cleanupCtx))
			for _, observed := range fixture.cleanupContextsSnapshot() {
				assert.Equal(t, "retained", observed.Value(namespaceEffectTestContextKey{}))
				assert.NoError(t, observed.Err())
			}
		})
	}
}

// Rationale: unlocking a thread is cleanup and must retain context values without inheriting cancellation.
func TestNamespaceEffectThreadUnlockUsesCleanupContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.WithValue(
		t.Context(),
		namespaceEffectTestContextKey{},
		"retained",
	))
	fixture := newNamespaceEffectFixture()
	fixture.cancelAfter = "thread.lock"
	fixture.cancel = cancel
	effect := newNamespaceEffectWithDeps(fixture.dependencies(), fixture.authority(), fixture.policy())
	key := mustTopologyKey(t, testOwnerUID, testVMID, testNetworkID)

	pin, err := effect.CreateNamespace(ctx, namespaceCreateSpec{key: key})
	require.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, pin)
	assert.Equal(t, namespaceHandle{}, pin.Handle())
	contexts := fixture.cleanupContextsSnapshot()
	require.NotEmpty(t, contexts)
	assert.Equal(t, 1, fixture.eventCount("thread.unlock"))
	for _, observed := range contexts {
		assert.Equal(t, "retained", observed.Value(namespaceEffectTestContextKey{}))
		assert.NoError(t, observed.Err())
	}

	require.NoError(t, effect.DeleteNamespace(ctx, pin))
	require.NoError(t, pin.Release(ctx))
}

// Rationale: an unsafe restoration retires the locked worker; synchronous caller work resumes outside that boundary.
func TestNamespaceEffectUnsafeWorkerExitsLockedBeforeCallerResumes(t *testing.T) {
	t.Parallel()

	fixture := newNamespaceEffectFixture()
	cause := errors.New("host namespace restoration failed")
	fixture.inject("setns.13", 1, cause)
	effect := newNamespaceEffectWithDeps(fixture.dependencies(), fixture.authority(), fixture.policy())
	key := mustTopologyKey(t, testOwnerUID, testVMID, testNetworkID)

	pin, err := effect.CreateNamespace(t.Context(), namespaceCreateSpec{key: key})
	require.ErrorIs(t, err, cause)
	require.NotNil(t, pin)
	assert.Equal(t, namespaceHandle{}, pin.Handle())
	fixture.record("caller.after_create")
	events := fixture.eventsSnapshot()
	require.GreaterOrEqual(t, len(events), 2)
	assert.Equal(t, "worker.exit", events[len(events)-2])
	assert.Equal(t, "caller.after_create", events[len(events)-1])
	assert.Zero(t, fixture.eventCount("thread.unlock"))
	assert.False(t, fixture.workerViolation)

	require.NoError(t, effect.DeleteNamespace(t.Context(), pin))
	require.NoError(t, pin.Release(t.Context()))
}

// Rationale: every injected Linux-boundary failure must return exact authority and immutable partial-state evidence.
func TestNamespaceEffectKernelFaultsReturnExactOutcome(t *testing.T) {
	t.Parallel()

	partial := namespaceCreateOutcome{cleanupRequired: true}
	created := namespaceCreateOutcome{cleanupRequired: true, created: true, restored: true}
	pinned := namespaceCreateOutcome{cleanupRequired: true, created: true, pinned: true, restored: true}
	published := namespaceCreateOutcome{
		cleanupRequired: true, created: true, pinned: true, published: true, restored: true,
	}
	verifiedUnrestored := namespaceCreateOutcome{
		cleanupRequired: true, created: true, pinned: true, published: true, verified: true,
	}
	verified := namespaceCreateOutcome{
		cleanupRequired: true, created: true, pinned: true, published: true, verified: true, restored: true,
	}
	tests := map[string]struct {
		event   string
		at      int
		outcome namespaceCreateOutcome
		fatal   bool
	}{
		"target identity":                {event: "authority.target_identity", at: 1, outcome: partial},
		"open root":                      {event: "open.root", at: 1, outcome: partial},
		"inspect root":                   {event: "fstat.10", at: 1, outcome: partial},
		"open target directory":          {event: "openat.parent", at: 1, outcome: partial},
		"inspect target directory":       {event: "fstat.11", at: 1, outcome: partial},
		"inspect target directory mount": {event: "statx.11", at: 1, outcome: partial},
		"open placeholder":               {event: "openat.placeholder", at: 1, outcome: partial},
		"inspect placeholder":            {event: "fstat.12", at: 1, outcome: partial},
		"inspect placeholder mount":      {event: "statx.12", at: 1, outcome: partial},
		"thread lock":                    {event: "thread.lock", at: 1, outcome: partial},
		"open host namespace":            {event: "open.thread-net.1", at: 1, outcome: partial},
		"inspect host inode":             {event: "fstat.13", at: 1, outcome: partial},
		"inspect host filesystem":        {event: "fstatfs.13", at: 1, outcome: partial},
		"inspect host type":              {event: "nstype.13", at: 1, outcome: partial},
		"unshare":                        {event: "unshare", at: 1, outcome: partial},
		"open created namespace":         {event: "open.thread-net.2", at: 1, outcome: created},
		"inspect created inode":          {event: "fstat.14", at: 1, outcome: created},
		"inspect created fs":             {event: "fstatfs.14", at: 1, outcome: created},
		"inspect created type":           {event: "nstype.14", at: 1, outcome: created},
		"clone mount":                    {event: "open_tree.14", at: 1, outcome: pinned},
		"recheck directory":              {event: "fstat.11", at: 2, outcome: pinned},
		"recheck directory mount":        {event: "statx.11", at: 2, outcome: pinned},
		"reopen placeholder":             {event: "openat.prepublish", at: 1, outcome: pinned},
		"recheck placeholder":            {event: "fstat.16", at: 1, outcome: pinned},
		"recheck placeholder mount":      {event: "statx.16", at: 1, outcome: pinned},
		"publish mount":                  {event: "move_mount.15.16", at: 1, outcome: pinned},
		"sync publication":               {event: "fsync.11", at: 1, outcome: published},
		"recheck published parent":       {event: "fstat.11", at: 3, outcome: published},
		"recheck published parent mount": {event: "statx.11", at: 3, outcome: published},
		"open published mount":           {event: "openat.published", at: 1, outcome: published},
		"inspect published inode":        {event: "fstat.17", at: 1, outcome: published},
		"inspect published fs":           {event: "fstatfs.17", at: 1, outcome: published},
		"inspect published type":         {event: "nstype.17", at: 1, outcome: published},
		"restore host":                   {event: "setns.13", at: 1, outcome: verifiedUnrestored, fatal: true},
		"open restored host":             {event: "open.thread-net.3", at: 1, outcome: verifiedUnrestored, fatal: true},
		"inspect restored inode":         {event: "fstat.18", at: 1, outcome: verifiedUnrestored, fatal: true},
		"inspect restored fs":            {event: "fstatfs.18", at: 1, outcome: verifiedUnrestored, fatal: true},
		"inspect restored type":          {event: "nstype.18", at: 1, outcome: verifiedUnrestored, fatal: true},
		"close restored pin":             {event: "close.18", at: 1, outcome: verified},
		"close publication target":       {event: "close.16", at: 1, outcome: verified},
		"close detached mount":           {event: "close.15", at: 1, outcome: verified},
		"close host pin":                 {event: "close.13", at: 1, outcome: verified},
		"unlock thread":                  {event: "thread.unlock", at: 1, outcome: verified},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newNamespaceEffectFixture()
			cause := errors.New("injected " + name)
			fixture.inject(test.event, test.at, cause)
			effect := newNamespaceEffectWithDeps(fixture.dependencies(), fixture.authority(), fixture.policy())
			key := mustTopologyKey(t, testOwnerUID, testVMID, testNetworkID)

			pin, err := effect.CreateNamespace(t.Context(), namespaceCreateSpec{key: key})
			require.ErrorIs(t, err, cause)
			require.NotNil(t, pin)
			assert.Equal(t, namespaceHandle{}, pin.Handle())
			want := test.outcome
			if test.event == "close.18" || test.event == "close.16" || test.event == "close.15" ||
				test.event == "close.13" {
				want.restored = true
			}
			assertNamespaceCreateError(t, err, want, test.fatal)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			if test.fatal {
				assert.Equal(t, true, domainErr.Details["network_worker_retired"])
				assert.Equal(t, true, domainErr.Details["network_thread_contaminated"])
				assert.NotContains(t, fixture.eventsSnapshot(), "thread.unlock")
				assert.Equal(t, 1, fixture.eventCount("worker.exit"))
				assert.False(t, fixture.workerViolation)
			}

			require.NoError(t, effect.DeleteNamespace(t.Context(), pin))
			require.NoError(t, pin.Release(t.Context()))
		})
	}
}

// Rationale: a same-inode target on a different mount lifetime or mount-ID kind is not the retained authority target.
func TestNamespaceEffectRejectsAuthorityMountIdentityDriftBeforeEffects(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		event string
		at    int
		stat  unix.Statx_t
	}{
		"directory mount ID": {
			event: "statx.11", at: 1, stat: namespaceEffectStatx(999, true),
		},
		"directory mount kind": {
			event: "statx.11", at: 1, stat: namespaceEffectStatx(800, false),
		},
		"directory mount unavailable": {
			event: "statx.11", at: 1, stat: unix.Statx_t{},
		},
		"placeholder mount ID": {
			event: "statx.12", at: 1, stat: namespaceEffectStatx(999, false),
		},
		"placeholder mount kind": {
			event: "statx.12", at: 1, stat: namespaceEffectStatx(801, true),
		},
		"placeholder mount unavailable": {
			event: "statx.12", at: 1, stat: unix.Statx_t{},
		},
		"directory changes before publication": {
			event: "statx.11", at: 2, stat: namespaceEffectStatx(999, true),
		},
		"placeholder changes before publication": {
			event: "statx.16", at: 1, stat: namespaceEffectStatx(999, false),
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newNamespaceEffectFixture()
			fixture.overrideStatx(test.event, test.at, test.stat)
			effect := newNamespaceEffectWithDeps(fixture.dependencies(), fixture.authority(), fixture.policy())
			key := mustTopologyKey(t, testOwnerUID, testVMID, testNetworkID)

			pin, err := effect.CreateNamespace(t.Context(), namespaceCreateSpec{key: key})
			require.Error(t, err)
			require.NotNil(t, pin)
			assert.Zero(t, fixture.eventCount("move_mount.15.16"))
			if test.at == 1 && (test.event == "statx.11" || test.event == "statx.12") {
				assert.Zero(t, fixture.eventCount("unshare"))
			}
			require.NoError(t, effect.DeleteNamespace(t.Context(), pin))
			require.NoError(t, pin.Release(t.Context()))
		})
	}
}

// Rationale: kernels without a mount-ID field remain usable only when authority and reinspection agree on unavailable.
func TestNamespaceEffectAcceptsExplicitlyUnavailableMountIdentity(t *testing.T) {
	t.Parallel()

	fixture := newNamespaceEffectFixture()
	fixture.authorityIdentity = &namespaceTargetIdentity{
		directoryDevice:   8,
		directoryInode:    100,
		directoryMount:    namespaceTargetMountIdentity{kind: namespaceTargetMountIdentityUnavailable},
		placeholderDevice: 8,
		placeholderInode:  200,
		placeholderMount:  namespaceTargetMountIdentity{kind: namespaceTargetMountIdentityUnavailable},
	}
	fixture.statxMounts[11] = unix.Statx_t{}
	fixture.statxMounts[12] = unix.Statx_t{}
	fixture.statxMounts[16] = unix.Statx_t{}
	effect := newNamespaceEffectWithDeps(fixture.dependencies(), fixture.authority(), fixture.policy())
	key := mustTopologyKey(t, testOwnerUID, testVMID, testNetworkID)

	pin, err := effect.CreateNamespace(t.Context(), namespaceCreateSpec{key: key})
	require.NoError(t, err)
	require.NoError(t, effect.DeleteNamespace(t.Context(), pin))
	require.NoError(t, pin.Release(t.Context()))
}

// Rationale: both reserved-name identities are rechecked immediately before publication and after mounting.
func TestNamespaceEffectRejectsReservedNameAndMountIdentityChanges(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		mutate      func(*namespaceEffectFixture)
		wantMove    bool
		wantUnmount bool
	}{
		"placeholder replaced before publication": {
			mutate: func(fixture *namespaceEffectFixture) {
				fixture.stats[16] = namespaceEffectPlaceholderStat(8, 999)
			},
		},
		"directory replaced before publication": {
			mutate: func(fixture *namespaceEffectFixture) {
				fixture.overrideStat("fstat.11", 2, namespaceEffectDirectoryStat(8, 999, 0o700))
			},
		},
		"published namespace changed": {
			mutate: func(fixture *namespaceEffectFixture) {
				fixture.stats[17] = namespaceEffectNamespaceStat(5, 999)
			},
			wantMove:    true,
			wantUnmount: true,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newNamespaceEffectFixture()
			test.mutate(fixture)
			effect := newNamespaceEffectWithDeps(fixture.dependencies(), fixture.authority(), fixture.policy())
			key := mustTopologyKey(t, testOwnerUID, testVMID, testNetworkID)

			pin, err := effect.CreateNamespace(t.Context(), namespaceCreateSpec{key: key})
			require.Error(t, err)
			require.NotNil(t, pin)
			assert.Equal(t, test.wantMove, fixture.eventCount("move_mount.15.16") == 1)
			if test.wantUnmount {
				fixture.stats[17] = namespaceEffectNamespaceStat(5, 400)
			}
			require.NoError(t, effect.DeleteNamespace(t.Context(), pin))
			assert.Equal(t, test.wantUnmount, fixture.eventCount("unmount./proc/self/fd/11/"+testVMID) == 1)
			require.NoError(t, pin.Release(t.Context()))
		})
	}
}

// Rationale: an nsfs mount is admitted only with the complete fixed metadata and network-namespace type contract.
func TestNamespaceEffectRejectsInvalidPublishedMountMetadata(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*namespaceEffectFixture){
		"file type": func(fixture *namespaceEffectFixture) {
			stat := fixture.stats[17]
			stat.Mode = unix.S_IFDIR | 0o444
			fixture.stats[17] = stat
		},
		"owner": func(fixture *namespaceEffectFixture) {
			stat := fixture.stats[17]
			stat.Uid = 1
			fixture.stats[17] = stat
		},
		"group": func(fixture *namespaceEffectFixture) {
			stat := fixture.stats[17]
			stat.Gid = 1
			fixture.stats[17] = stat
		},
		"mode": func(fixture *namespaceEffectFixture) {
			stat := fixture.stats[17]
			stat.Mode = unix.S_IFREG | 0o400
			fixture.stats[17] = stat
		},
		"link count": func(fixture *namespaceEffectFixture) {
			stat := fixture.stats[17]
			stat.Nlink = 2
			fixture.stats[17] = stat
		},
		"size": func(fixture *namespaceEffectFixture) {
			stat := fixture.stats[17]
			stat.Size = 1
			fixture.stats[17] = stat
		},
		"device": func(fixture *namespaceEffectFixture) {
			stat := fixture.stats[17]
			stat.Dev = 0
			fixture.stats[17] = stat
		},
		"inode": func(fixture *namespaceEffectFixture) {
			stat := fixture.stats[17]
			stat.Ino = 0
			fixture.stats[17] = stat
		},
		"filesystem": func(fixture *namespaceEffectFixture) {
			fixture.statfsTypes[17] = unix.EXT4_SUPER_MAGIC
		},
		"namespace type": func(fixture *namespaceEffectFixture) {
			fixture.namespaceTypes[17] = unix.CLONE_NEWUSER
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newNamespaceEffectFixture()
			mutate(fixture)
			effect := newNamespaceEffectWithDeps(fixture.dependencies(), fixture.authority(), fixture.policy())
			key := mustTopologyKey(t, testOwnerUID, testVMID, testNetworkID)

			pin, err := effect.CreateNamespace(t.Context(), namespaceCreateSpec{key: key})
			require.Error(t, err)
			require.NotNil(t, pin)
			assertNamespaceCreateError(t, err, namespaceCreateOutcome{
				cleanupRequired: true,
				created:         true,
				pinned:          true,
				published:       true,
				restored:        true,
			})

			fixture.stats[17] = namespaceEffectNamespaceStat(5, 400)
			fixture.statfsTypes[17] = unix.NSFS_MAGIC
			fixture.namespaceTypes[17] = unix.CLONE_NEWNET
			require.NoError(t, effect.DeleteNamespace(t.Context(), pin))
			require.NoError(t, pin.Release(t.Context()))
		})
	}
}

// Rationale: cleanup must never unmount a different namespace substituted at the reserved VM name.
func TestNamespaceEffectDeleteRejectsReservedNameReplacement(t *testing.T) {
	t.Parallel()

	fixture := newNamespaceEffectFixture()
	effect := newNamespaceEffectWithDeps(fixture.dependencies(), fixture.authority(), fixture.policy())
	key := mustTopologyKey(t, testOwnerUID, testVMID, testNetworkID)
	pin, err := effect.CreateNamespace(t.Context(), namespaceCreateSpec{key: key})
	require.NoError(t, err)
	fixture.stats[19] = namespaceEffectNamespaceStat(5, 999)

	err = effect.DeleteNamespace(t.Context(), pin)
	require.Error(t, err)
	assertNamespaceRemoveError(t, err)
	assert.Zero(t, fixture.eventCount("unmount./proc/self/fd/11/"+testVMID))

	fixture.stats[19] = namespaceEffectNamespaceStat(5, 400)
	require.NoError(t, effect.DeleteNamespace(t.Context(), pin))
	require.NoError(t, pin.Release(t.Context()))
}

// Rationale: an unmount error is reconciled against exact retained identities before any destructive retry.
func TestNamespaceEffectReconcilesUnmountErrorWithoutDuplicateUnmount(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		configure          func(*namespaceEffectFixture, error)
		wantAfterFirst     map[string]any
		wantUnmounts       int
		restoreReplacement bool
	}{
		"exact mount remains": {
			configure: func(fixture *namespaceEffectFixture, cause error) {
				fixture.failNext("unmount./proc/self/fd/11/"+testVMID, cause)
			},
			wantAfterFirst: map[string]any{
				"cleanup_required":         true,
				"namespace_published":      true,
				"namespace_unmounted":      false,
				"authority_target_removed": false,
				"unmount_outcome_unknown":  false,
			},
			wantUnmounts: 2,
		},
		"placeholder visible after effect": {
			configure: func(fixture *namespaceEffectFixture, cause error) {
				fixture.unmountAfterEffect = cause
			},
			wantAfterFirst: map[string]any{
				"cleanup_required":         true,
				"namespace_published":      true,
				"namespace_unmounted":      true,
				"authority_target_removed": false,
				"unmount_outcome_unknown":  false,
			},
			wantUnmounts: 1,
		},
		"replacement after effect": {
			configure: func(fixture *namespaceEffectFixture, cause error) {
				fixture.unmountAfterEffect = cause
				fixture.stats[16] = namespaceEffectPlaceholderStat(8, 999)
			},
			wantAfterFirst: map[string]any{
				"cleanup_required":         true,
				"namespace_published":      true,
				"namespace_unmounted":      false,
				"authority_target_removed": false,
				"unmount_outcome_unknown":  true,
			},
			wantUnmounts:       1,
			restoreReplacement: true,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newNamespaceEffectFixture()
			effect := newNamespaceEffectWithDeps(fixture.dependencies(), fixture.authority(), fixture.policy())
			key := mustTopologyKey(t, testOwnerUID, testVMID, testNetworkID)
			pin, err := effect.CreateNamespace(t.Context(), namespaceCreateSpec{key: key})
			require.NoError(t, err)
			cause := errors.New("unmount result lost")
			test.configure(fixture, cause)

			err = effect.DeleteNamespace(t.Context(), pin)
			require.ErrorIs(t, err, cause)
			assertNamespaceRemoveError(t, err, test.wantAfterFirst)
			assert.Zero(t, fixture.eventCount("authority.remove"))
			if test.restoreReplacement {
				fixture.stats[16] = namespaceEffectPlaceholderStat(8, 200)
			}
			require.NoError(t, effect.DeleteNamespace(t.Context(), pin))
			assert.Equal(t, test.wantUnmounts, fixture.eventCount("unmount./proc/self/fd/11/"+testVMID))
			assert.Equal(t, 1, fixture.eventCount("authority.remove"))
			require.NoError(t, pin.Release(t.Context()))
		})
	}
}

// Rationale: cleanup consumes only completed effects and retries the first still-pending mutation under cancellation.
func TestNamespaceEffectCleanupFaultsRetryPrecisely(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"inspect directory":          "fstat.11",
		"inspect retained pin":       "fstat.14",
		"inspect published pin":      "fstat.17",
		"open reserved name":         "openat.current",
		"inspect current name":       "fstat.19",
		"inspect current filesystem": "fstatfs.19",
		"inspect current type":       "nstype.19",
		"close current pin":          "close.19",
		"close published pin":        "close.17",
		"unmount":                    "unmount./proc/self/fd/11/" + testVMID,
		"remove authority target":    "authority.remove",
	}
	for name, event := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newNamespaceEffectFixture()
			effect := newNamespaceEffectWithDeps(fixture.dependencies(), fixture.authority(), fixture.policy())
			key := mustTopologyKey(t, testOwnerUID, testVMID, testNetworkID)
			pin, err := effect.CreateNamespace(t.Context(), namespaceCreateSpec{key: key})
			require.NoError(t, err)
			cause := errors.New("cleanup fault")
			fixture.failNext(event, cause)
			ctx, cancel := context.WithCancel(context.WithValue(
				t.Context(),
				namespaceEffectTestContextKey{},
				"retained",
			))
			cancel()

			err = effect.DeleteNamespace(ctx, pin)
			require.ErrorIs(t, err, cause)
			assertNamespaceRemoveError(t, err)
			require.NoError(t, effect.DeleteNamespace(ctx, pin))
			require.NoError(t, pin.Release(ctx))
		})
	}
}

// Rationale: copied pins share one cached close result and never retry an ambiguously consumed descriptor.
func TestNamespacePinCopiedConcurrentReleaseIsExactOnce(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"namespace pin":    "close.14",
		"placeholder pin":  "close.12",
		"directory pin":    "close.11",
		"authority target": "authority.release",
	}
	for name, event := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newNamespaceEffectFixture()
			effect := newNamespaceEffectWithDeps(fixture.dependencies(), fixture.authority(), fixture.policy())
			key := mustTopologyKey(t, testOwnerUID, testVMID, testNetworkID)
			pinValue, err := effect.CreateNamespace(t.Context(), namespaceCreateSpec{key: key})
			require.NoError(t, err)
			pin := pinValue.(*namespacePin)
			require.NoError(t, effect.DeleteNamespace(t.Context(), pin))
			cause := errors.New("ambiguous release")
			fixture.failNext(event, cause)

			const callers = 24
			start := make(chan struct{})
			results := make(chan error, callers)
			var ready sync.WaitGroup
			ready.Add(callers)
			for range callers {
				copyOfPin := *pin
				go func() {
					ready.Done()
					<-start
					results <- copyOfPin.Release(t.Context())
				}()
			}
			ready.Wait()
			close(start)
			for range callers {
				got := <-results
				require.ErrorIs(t, got, cause)
				assertNamespaceRemoveError(t, got)
			}
			assert.Equal(t, 1, fixture.eventCount(event))
			assert.Equal(t, 1, fixture.eventCount("close.14"))
			assert.Equal(t, 1, fixture.eventCount("close.12"))
			assert.Equal(t, 1, fixture.eventCount("close.11"))
			assert.Equal(t, 1, fixture.eventCount("authority.release"))
		})
	}
}

func TestOpenNamespaceTargetUsesOrdinaryVerifiedDescriptors(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	uid := fmt.Sprintf("%d", testOwnerUID)
	parentPath := filepath.Join(root, "run", "mvmctl", "authority", uid, "netns")
	require.NoError(t, os.MkdirAll(parentPath, 0o700))
	require.NoError(t, os.Chmod(parentPath, 0o700))
	placeholderPath := filepath.Join(parentPath, testVMID)
	file, err := os.OpenFile(placeholderPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	var directoryStat unix.Stat_t
	require.NoError(t, unix.Lstat(parentPath, &directoryStat))
	var placeholderStat unix.Stat_t
	require.NoError(t, unix.Lstat(placeholderPath, &placeholderStat))
	target := namespaceTargetIdentity{
		directoryDevice:   directoryStat.Dev,
		directoryInode:    directoryStat.Ino,
		placeholderDevice: placeholderStat.Dev,
		placeholderInode:  placeholderStat.Ino,
	}
	target.directoryMount = namespaceEffectPathMountIdentity(t, parentPath)
	target.placeholderMount = namespaceEffectPathMountIdentity(t, placeholderPath)
	policy := namespaceEffectPolicy{
		rootPath: root, expectedUID: uint32(os.Geteuid()), expectedGID: uint32(os.Getegid()),
	}
	deps := systemNamespaceEffectDeps()
	key := mustTopologyKey(t, testOwnerUID, testVMID, testNetworkID)

	parentFD, placeholderFD, err := openExactNamespaceTarget(t.Context(), deps, policy, key, target)
	require.NoError(t, err)
	require.NotEqual(t, -1, parentFD)
	require.NotEqual(t, -1, placeholderFD)
	// Regression: O_PATH would make the required fsync and metadata verification operations fail with EBADF.
	require.NoError(t, unix.Fsync(parentFD))
	require.NoError(t, unix.Fchmod(parentFD, 0o700))
	require.NoError(t, unix.Fsync(placeholderFD))
	require.NoError(t, closeConsumedNamespaceFD(t.Context(), deps, &placeholderFD))
	require.NoError(t, closeConsumedNamespaceFD(t.Context(), deps, &parentFD))
}

func TestSystemNamespaceEffectDependenciesRejectCancellationBeforeSyscalls(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	deps := systemNamespaceEffectDeps()
	tests := map[string]func() error{
		"worker": func() error {
			return deps.runWorker(ctx, func(context.Context) error {
				return errors.New("worker must not run")
			})
		},
		"lock thread": func() error { return deps.lockThread(ctx) },
		"open": func() error {
			_, err := deps.open(ctx, "", 0, 0)
			return err
		},
		"openat2": func() error {
			_, err := deps.openAt2(ctx, -1, "", nil)
			return err
		},
		"fstat":   func() error { return deps.fstat(ctx, -1, &unix.Stat_t{}) },
		"statx":   func() error { return deps.statx(ctx, -1, &unix.Statx_t{}) },
		"fstatfs": func() error { return deps.fstatfs(ctx, -1, &unix.Statfs_t{}) },
		"fsync":   func() error { return deps.fsync(ctx, -1) },
		"namespace type": func() error {
			_, err := deps.ioctlNamespaceType(ctx, -1)
			return err
		},
		"unshare": func() error { return deps.unshare(ctx, unix.CLONE_NEWNET) },
		"setns":   func() error { return deps.setns(ctx, -1, unix.CLONE_NEWNET) },
		"open tree": func() error {
			_, err := deps.openTree(ctx, -1, "", 0)
			return err
		},
		"move mount": func() error { return deps.moveMount(ctx, -1, "", -1, "", 0) },
		"unmount":    func() error { return deps.unmount(ctx, "", 0) },
		"close":      func() error { return deps.close(ctx, -1) },
	}
	for name, operation := range tests {
		t.Run(name, func(t *testing.T) {
			assert.ErrorIs(t, operation(), context.Canceled)
		})
	}
}

// Rationale: namespace switching executes on a short-lived locked worker, never on the synchronous caller thread.
func TestSystemNamespaceWorkerRetiresLockedThreadBeforeCallerContinues(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	callerThread := unix.Gettid()
	workerThread := 0
	cause := errors.New("unsafe restoration")
	deps := systemNamespaceEffectDeps()

	err := deps.runWorker(t.Context(), func(ctx context.Context) error {
		require.NoError(t, deps.lockThread(ctx))
		workerThread = unix.Gettid()
		return cause
	})

	require.ErrorIs(t, err, cause)
	assert.NotZero(t, workerThread)
	assert.NotEqual(t, callerThread, workerThread)
	assert.Equal(t, callerThread, unix.Gettid())
}

func assertNamespaceCreateError(t *testing.T, err error, outcome namespaceCreateOutcome, fatal ...bool) {
	t.Helper()
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	wantDetails := map[string]any{
		"cleanup_required":        outcome.cleanupRequired,
		"namespace_created":       outcome.created,
		"namespace_pinned":        outcome.pinned,
		"namespace_published":     outcome.published,
		"publication_verified":    outcome.verified,
		"host_namespace_restored": outcome.restored,
	}
	if len(fatal) == 1 && fatal[0] {
		wantDetails["network_worker_retired"] = true
		wantDetails["network_thread_contaminated"] = true
	}
	want := namespaceErrorMetadata{
		Code: errs.CodeNetworkCreateFailed, Op: "network", Entity: testVMID, Class: errs.ClassInternal,
		Details: wantDetails,
	}
	got := namespaceErrorMetadata{
		Code: domainErr.Code, Op: domainErr.Op, Entity: domainErr.Entity, Class: domainErr.Class,
		Details: domainErr.Details,
	}
	if domainErr.Message == "" {
		t.Error("namespace create error message is empty")
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("namespace create error mismatch (-want +got):\n%s", diff)
	}
}

func assertNamespaceRemoveError(t *testing.T, err error, wantDetails ...map[string]any) {
	t.Helper()
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	want := namespaceErrorMetadata{
		Code: errs.CodeNetworkRemoveFailed, Op: "network", Entity: testVMID, Class: errs.ClassInternal,
	}
	got := namespaceErrorMetadata{
		Code: domainErr.Code, Op: domainErr.Op, Entity: domainErr.Entity, Class: domainErr.Class,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("namespace remove error mismatch (-want +got):\n%s", diff)
	}
	required := []string{
		"cleanup_required",
		"namespace_published",
		"namespace_unmounted",
		"authority_target_removed",
		"unmount_outcome_unknown",
	}
	for _, key := range required {
		_, ok := domainErr.Details[key]
		assert.True(t, ok, "missing removal evidence %q", key)
	}
	if len(wantDetails) == 1 {
		if diff := cmp.Diff(wantDetails[0], domainErr.Details); diff != "" {
			t.Errorf("namespace removal evidence mismatch (-want +got):\n%s", diff)
		}
	}
}

type namespaceErrorMetadata struct {
	Code    errs.Code
	Op      string
	Entity  string
	Class   errs.Class
	Details map[string]any
}

type fakeNamespaceTargetAuthority struct {
	fixture *namespaceEffectFixture
	target  *fakeNamespaceTargetLease
}

func (authority *fakeNamespaceTargetAuthority) create(
	ctx context.Context,
	_ topologyKey,
) (namespaceTargetLease, error) {
	if err := authority.fixture.recordContext("authority.create", ctx, false); err != nil {
		return authority.target, err
	}
	return authority.target, nil
}

type fakeNamespaceTargetLease struct {
	fixture  *namespaceEffectFixture
	mu       sync.Mutex // guards removed and released below.
	removed  bool
	released bool
}

func (lease *fakeNamespaceTargetLease) targetIdentity(ctx context.Context) (namespaceTargetIdentity, error) {
	if err := lease.fixture.recordContext("authority.target_identity", ctx, false); err != nil {
		return namespaceTargetIdentity{}, err
	}
	lease.fixture.mu.Lock()
	defer lease.fixture.mu.Unlock()
	if lease.fixture.authorityIdentity != nil {
		return *lease.fixture.authorityIdentity, nil
	}
	return namespaceTargetIdentity{
		directoryDevice:   8,
		directoryInode:    100,
		directoryMount:    namespaceTargetMountIdentity{kind: namespaceTargetMountIdentityUnique, value: 800},
		placeholderDevice: 8,
		placeholderInode:  200,
		placeholderMount: namespaceTargetMountIdentity{
			kind: namespaceTargetMountIdentityLegacy, value: 801,
		},
	}, nil
}

func (lease *fakeNamespaceTargetLease) remove(ctx context.Context) error {
	if err := lease.fixture.recordContext("authority.remove", ctx, true); err != nil {
		return err
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	lease.removed = true
	return nil
}

func (lease *fakeNamespaceTargetLease) release(ctx context.Context) error {
	if err := lease.fixture.recordContext("authority.release", ctx, true); err != nil {
		return err
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	lease.released = true
	return nil
}

type namespaceEffectFixture struct {
	mu sync.Mutex // guards all mutable fixture evidence and simulated kernel state below.

	events             []string
	eventCounts        map[string]int
	failAt             map[string]map[int]error
	statOverrides      map[string]unix.Stat_t
	statxOverrides     map[string]unix.Statx_t
	stats              map[int]unix.Stat_t
	statxMounts        map[int]unix.Statx_t
	authorityIdentity  *namespaceTargetIdentity
	statfsTypes        map[int]int64
	namespaceTypes     map[int]int
	cancelAfter        string
	cancel             context.CancelFunc
	threadNetOpens     int
	placeholderOpen    int
	publishedOpen      int
	mounted            bool
	restored           bool
	workerActive       bool
	workerViolation    bool
	unmountAfterEffect error
	cleanupContexts    []context.Context
}

func newNamespaceEffectFixture() *namespaceEffectFixture {
	return &namespaceEffectFixture{
		eventCounts:    make(map[string]int),
		failAt:         make(map[string]map[int]error),
		statOverrides:  make(map[string]unix.Stat_t),
		statxOverrides: make(map[string]unix.Statx_t),
		stats: map[int]unix.Stat_t{
			10: namespaceEffectDirectoryStat(1, 1, 0o755),
			11: namespaceEffectDirectoryStat(8, 100, 0o700),
			12: namespaceEffectPlaceholderStat(8, 200),
			13: namespaceEffectNamespaceStat(5, 300),
			14: namespaceEffectNamespaceStat(5, 400),
			16: namespaceEffectPlaceholderStat(8, 200),
			17: namespaceEffectNamespaceStat(5, 400),
			18: namespaceEffectNamespaceStat(5, 300),
			19: namespaceEffectNamespaceStat(5, 400),
		},
		statxMounts: map[int]unix.Statx_t{
			11: namespaceEffectStatx(800, true),
			12: namespaceEffectStatx(801, false),
			16: namespaceEffectStatx(801, false),
		},
		statfsTypes: map[int]int64{
			13: unix.NSFS_MAGIC,
			14: unix.NSFS_MAGIC,
			17: unix.NSFS_MAGIC,
			18: unix.NSFS_MAGIC,
			19: unix.NSFS_MAGIC,
		},
		namespaceTypes: map[int]int{
			13: unix.CLONE_NEWNET,
			14: unix.CLONE_NEWNET,
			17: unix.CLONE_NEWNET,
			18: unix.CLONE_NEWNET,
			19: unix.CLONE_NEWNET,
		},
	}
}

func (fixture *namespaceEffectFixture) authority() *fakeNamespaceTargetAuthority {
	return &fakeNamespaceTargetAuthority{
		fixture: fixture,
		target:  &fakeNamespaceTargetLease{fixture: fixture},
	}
}

func (*namespaceEffectFixture) policy() namespaceEffectPolicy {
	return namespaceEffectPolicy{rootPath: "/", expectedUID: 0, expectedGID: 0}
}

func (fixture *namespaceEffectFixture) dependencies() namespaceEffectDeps {
	return namespaceEffectDeps{
		runWorker: fixture.runWorker,
		lockThread: func(ctx context.Context) error {
			fixture.requireWorker()
			return fixture.recordContext("thread.lock", ctx, false)
		},
		unlockThread: func(ctx context.Context) error {
			fixture.requireWorker()
			return fixture.recordContext("thread.unlock", ctx, true)
		},
		open:    fixture.open,
		openAt2: fixture.openAt2,
		fstat: func(ctx context.Context, fd int, stat *unix.Stat_t) error {
			event := fmt.Sprintf("fstat.%d", fd)
			if err := fixture.recordContext(event, ctx, false); err != nil {
				return err
			}
			fixture.mu.Lock()
			value := fixture.stats[fd]
			if override, ok := fixture.statOverrides[fmt.Sprintf("%s#%d", event, fixture.eventCounts[event])]; ok {
				value = override
			}
			*stat = value
			fixture.mu.Unlock()
			return nil
		},
		statx: func(ctx context.Context, fd int, stat *unix.Statx_t) error {
			event := fmt.Sprintf("statx.%d", fd)
			if err := fixture.recordContext(event, ctx, false); err != nil {
				return err
			}
			fixture.mu.Lock()
			value := fixture.statxMounts[fd]
			if override, ok := fixture.statxOverrides[fmt.Sprintf("%s#%d", event, fixture.eventCounts[event])]; ok {
				value = override
			}
			*stat = value
			fixture.mu.Unlock()
			return nil
		},
		fstatfs: func(ctx context.Context, fd int, stat *unix.Statfs_t) error {
			if err := fixture.recordContext(fmt.Sprintf("fstatfs.%d", fd), ctx, false); err != nil {
				return err
			}
			fixture.mu.Lock()
			stat.Type = fixture.statfsTypes[fd]
			fixture.mu.Unlock()
			return nil
		},
		fsync: func(ctx context.Context, fd int) error {
			return fixture.recordContext(fmt.Sprintf("fsync.%d", fd), ctx, false)
		},
		ioctlNamespaceType: func(ctx context.Context, fd int) (int, error) {
			if err := fixture.recordContext(fmt.Sprintf("nstype.%d", fd), ctx, false); err != nil {
				return 0, err
			}
			fixture.mu.Lock()
			namespaceType := fixture.namespaceTypes[fd]
			fixture.mu.Unlock()
			return namespaceType, nil
		},
		unshare: func(ctx context.Context, flags int) error {
			fixture.requireWorker()
			if flags != unix.CLONE_NEWNET {
				return errors.New("unexpected unshare flags")
			}
			if err := fixture.recordContext("unshare", ctx, false); err != nil {
				return err
			}
			fixture.mu.Lock()
			fixture.restored = false
			fixture.mu.Unlock()
			return nil
		},
		setns: func(ctx context.Context, fd, namespaceType int) error {
			fixture.requireWorker()
			if fd != 13 || namespaceType != unix.CLONE_NEWNET {
				return errors.New("unexpected setns arguments")
			}
			if err := fixture.recordContext("setns.13", ctx, true); err != nil {
				return err
			}
			fixture.mu.Lock()
			fixture.restored = true
			fixture.mu.Unlock()
			return nil
		},
		openTree: func(ctx context.Context, fd int, path string, flags uint) (int, error) {
			if fd != 14 || path != "" || flags != uint(namespaceOpenTreeFlags) {
				return -1, errors.New("unexpected open_tree arguments")
			}
			if err := fixture.recordContext("open_tree.14", ctx, false); err != nil {
				return -1, err
			}
			return 15, nil
		},
		moveMount: func(ctx context.Context, fromFD int, fromPath string, toFD int, toPath string, flags int) error {
			if fromFD != 15 || fromPath != "" || toFD != 16 || toPath != "" || flags != namespaceMoveMountFlags {
				return errors.New("unexpected move_mount arguments")
			}
			if err := fixture.recordContext("move_mount.15.16", ctx, false); err != nil {
				return err
			}
			fixture.mu.Lock()
			fixture.mounted = true
			fixture.mu.Unlock()
			return nil
		},
		unmount: func(ctx context.Context, path string, flags int) error {
			if flags != 0 {
				return errors.New("namespace unmount must not be lazy")
			}
			if err := fixture.recordContext("unmount."+path, ctx, true); err != nil {
				return err
			}
			fixture.mu.Lock()
			fixture.mounted = false
			afterEffectErr := fixture.unmountAfterEffect
			fixture.unmountAfterEffect = nil
			fixture.mu.Unlock()
			return afterEffectErr
		},
		close: func(ctx context.Context, fd int) error {
			return fixture.recordContext(fmt.Sprintf("close.%d", fd), ctx, true)
		},
	}
}

func (fixture *namespaceEffectFixture) runWorker(
	ctx context.Context,
	worker func(context.Context) error,
) error {
	result := make(chan error, 1)
	go func() {
		fixture.mu.Lock()
		fixture.workerActive = true
		fixture.events = append(fixture.events, "worker.enter")
		fixture.eventCounts["worker.enter"]++
		fixture.mu.Unlock()
		err := worker(ctx)
		fixture.mu.Lock()
		fixture.workerActive = false
		fixture.events = append(fixture.events, "worker.exit")
		fixture.eventCounts["worker.exit"]++
		fixture.mu.Unlock()
		result <- err
	}()
	return <-result
}

func (fixture *namespaceEffectFixture) requireWorker() {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if !fixture.workerActive {
		fixture.workerViolation = true
	}
}

func (fixture *namespaceEffectFixture) open(ctx context.Context, path string, flags int, _ uint32) (int, error) {
	if path == fixture.policy().rootPath {
		if flags != namespaceDirectoryOpenFlags {
			return -1, fmt.Errorf("unexpected root open flags %#x", flags)
		}
		if err := fixture.recordContext("open.root", ctx, false); err != nil {
			return -1, err
		}
		return 10, nil
	}
	if path != namespaceThreadNetworkPath || flags != namespaceCurrentOpenFlags {
		return -1, fmt.Errorf("unexpected namespace open path=%q flags=%#x", path, flags)
	}
	fixture.mu.Lock()
	fixture.threadNetOpens++
	count := fixture.threadNetOpens
	restored := fixture.restored
	fixture.mu.Unlock()
	event := fmt.Sprintf("open.thread-net.%d", count)
	if err := fixture.recordContext(event, ctx, false); err != nil {
		return -1, err
	}
	if restored {
		return 18, nil
	}
	if count == 1 {
		return 13, nil
	}
	return 14, nil
}

func (fixture *namespaceEffectFixture) openAt2(
	ctx context.Context,
	parentFD int,
	name string,
	how *unix.OpenHow,
) (int, error) {
	var event string
	var fd int
	switch {
	case parentFD == 10:
		if name != "run/mvmctl/authority/1000/netns" || how.Flags != uint64(namespaceDirectoryOpenFlags) ||
			how.Resolve != namespaceResolveBeneath {
			return -1, fmt.Errorf("unexpected namespace parent open: name=%q how=%#v", name, *how)
		}
		event, fd = "openat.parent", 11
	case parentFD == 11 && !fixture.mountedValue():
		fixture.mu.Lock()
		completedPlaceholderOpens := fixture.placeholderOpen
		fixture.mu.Unlock()
		wantResolve := uint64(namespaceResolveManaged)
		if completedPlaceholderOpens >= 2 {
			wantResolve = namespaceResolveBeneath
		}
		if name != testVMID || how.Flags != uint64(namespaceLeafInspectFlags) || how.Resolve != wantResolve {
			return -1, fmt.Errorf("unexpected placeholder open: name=%q how=%#v", name, *how)
		}
		fixture.mu.Lock()
		fixture.placeholderOpen++
		count := fixture.placeholderOpen
		fixture.mu.Unlock()
		if count == 1 {
			event, fd = "openat.placeholder", 12
		} else {
			event, fd = "openat.prepublish", 16
		}
	case parentFD == 11:
		if name != testVMID || how.Flags != uint64(namespaceLeafInspectFlags) ||
			how.Resolve != namespaceResolveBeneath {
			return -1, fmt.Errorf("unexpected published open: name=%q how=%#v", name, *how)
		}
		fixture.mu.Lock()
		fixture.publishedOpen++
		count := fixture.publishedOpen
		fixture.mu.Unlock()
		if count == 1 {
			event, fd = "openat.published", 17
		} else {
			event, fd = "openat.current", 19
		}
	default:
		return -1, fmt.Errorf("unexpected openat parent=%d name=%q flags=%#x", parentFD, name, how.Flags)
	}
	if err := fixture.recordContext(event, ctx, event == "openat.current"); err != nil {
		return -1, err
	}
	return fd, nil
}

func (fixture *namespaceEffectFixture) inject(event string, at int, err error) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.failAt[event] == nil {
		fixture.failAt[event] = make(map[int]error)
	}
	fixture.failAt[event][at] = err
}

func (fixture *namespaceEffectFixture) failNext(event string, err error) {
	fixture.mu.Lock()
	next := fixture.eventCounts[event] + 1
	fixture.mu.Unlock()
	fixture.inject(event, next, err)
}

func (fixture *namespaceEffectFixture) overrideStat(event string, at int, stat unix.Stat_t) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.statOverrides[fmt.Sprintf("%s#%d", event, at)] = stat
}

func (fixture *namespaceEffectFixture) overrideStatx(event string, at int, stat unix.Statx_t) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.statxOverrides[fmt.Sprintf("%s#%d", event, at)] = stat
}

func (fixture *namespaceEffectFixture) record(event string) {
	fixture.mu.Lock()
	fixture.events = append(fixture.events, event)
	fixture.eventCounts[event]++
	cancel := fixture.cancelAfter == event && fixture.cancel != nil
	cancelFunc := fixture.cancel
	fixture.mu.Unlock()
	if cancel {
		cancelFunc()
	}
}

func (fixture *namespaceEffectFixture) recordContext(event string, ctx context.Context, cleanup bool) error {
	var contextErr error
	if ctx == nil {
		contextErr = errors.New("nil context")
	} else {
		contextErr = ctx.Err()
	}
	fixture.mu.Lock()
	fixture.events = append(fixture.events, event)
	fixture.eventCounts[event]++
	count := fixture.eventCounts[event]
	err := fixture.failAt[event][count]
	if cleanup {
		fixture.cleanupContexts = append(fixture.cleanupContexts, ctx)
	}
	cancel := fixture.cancelAfter == event && fixture.cancel != nil
	cancelFunc := fixture.cancel
	fixture.mu.Unlock()
	if cancel {
		cancelFunc()
	}
	if err != nil {
		return err
	}
	return contextErr
}

func (fixture *namespaceEffectFixture) eventCount(event string) int {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return fixture.eventCounts[event]
}

func (fixture *namespaceEffectFixture) eventsSnapshot() []string {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return append([]string(nil), fixture.events...)
}

func (fixture *namespaceEffectFixture) cleanupContextsSnapshot() []context.Context {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return append([]context.Context(nil), fixture.cleanupContexts...)
}

func (fixture *namespaceEffectFixture) mountedValue() bool {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return fixture.mounted
}

func namespaceEffectDirectoryStat(device, inode uint64, mode uint32) unix.Stat_t {
	return unix.Stat_t{Dev: device, Ino: inode, Mode: unix.S_IFDIR | mode, Uid: 0, Gid: 0, Nlink: 2}
}

func namespaceEffectPlaceholderStat(device, inode uint64) unix.Stat_t {
	return unix.Stat_t{
		Dev: device, Ino: inode, Mode: unix.S_IFREG | 0o600, Uid: 0, Gid: 0, Nlink: 1,
	}
}

func namespaceEffectNamespaceStat(device, inode uint64) unix.Stat_t {
	return unix.Stat_t{Dev: device, Ino: inode, Mode: unix.S_IFREG | 0o444, Uid: 0, Gid: 0, Nlink: 1}
}

func namespaceEffectStatx(mountID uint64, unique bool) unix.Statx_t {
	mask := uint32(unix.STATX_MNT_ID)
	if unique {
		mask = unix.STATX_MNT_ID_UNIQUE
	}
	return unix.Statx_t{Mask: mask, Mnt_id: mountID}
}

func namespaceEffectPathMountIdentity(t *testing.T, path string) namespaceTargetMountIdentity {
	t.Helper()
	var stat unix.Statx_t
	require.NoError(t, unix.Statx(
		unix.AT_FDCWD,
		path,
		unix.AT_NO_AUTOMOUNT|unix.AT_SYMLINK_NOFOLLOW|unix.AT_STATX_SYNC_AS_STAT,
		unix.STATX_MNT_ID|unix.STATX_MNT_ID_UNIQUE,
		&stat,
	))
	require.NotZero(t, stat.Mnt_id)
	if stat.Mask&unix.STATX_MNT_ID_UNIQUE != 0 {
		return namespaceTargetMountIdentity{kind: namespaceTargetMountIdentityUnique, value: stat.Mnt_id}
	}
	require.NotZero(t, stat.Mask&unix.STATX_MNT_ID)
	return namespaceTargetMountIdentity{kind: namespaceTargetMountIdentityLegacy, value: stat.Mnt_id}
}
