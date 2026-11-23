package network

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

func TestTAPEffectCreatesPersistentOwnedTAPOnVerifiedNamespaceWorker(t *testing.T) {
	t.Parallel()

	fixture := newTAPEffectFixture()
	name := linkName("mvt012345678901")
	alias := managedTAPAlias(name, 1000)
	effect := fixture.effect()
	require.NoError(t, effect.createTAPDown(
		t.Context(), fixture.targetFD, fixture.targetIdentity, name, alias, 1000,
	))

	assert.Equal(t, []string{
		"TUNSETIFF fd=30 name=mvt012345678901 flags=0x9002",
		"TUNSETOWNER fd=30 owner=1000",
		"TUNSETPERSIST fd=30 persist=true",
	}, fixture.ioctlEvents())
	assert.Equal(t, []tapDeviceOpen{{
		path: tapDevicePath, flags: unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW,
	}}, fixture.deviceOpens())
	assert.Equal(t, 1, fixture.eventCount("close_device.30"))
	assert.Equal(t, 1, fixture.eventCount("unlock_thread"))
	assert.False(t, fixture.workerRetired())
	assert.Equal(t, fixture.hostIdentity, fixture.currentIdentity())
	assert.True(t, fixture.persistenceSawManagedAlias())
	created := fixture.route.mustLink(t, fixture.targetFD, name)
	assert.Equal(t, alias, created.alias)
	assert.Equal(t, uint8(1), created.tunPersist)
}

func TestTAPEffectRejectsWrongOrMalformedTunDeviceBeforeIoctl(t *testing.T) {
	t.Parallel()

	tests := map[string]unix.Stat_t{
		"regular file": {Mode: unix.S_IFREG | 0o600, Rdev: unix.Mkdev(tapDeviceMajor, tapDeviceMinor)},
		"wrong major":  {Mode: unix.S_IFCHR | 0o600, Rdev: unix.Mkdev(tapDeviceMajor+1, tapDeviceMinor)},
		"wrong minor":  {Mode: unix.S_IFCHR | 0o600, Rdev: unix.Mkdev(tapDeviceMajor, tapDeviceMinor+1)},
	}
	for name, stat := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newTAPEffectFixture()
			fixture.deviceStat = stat
			tapName := linkName("mvt012345678901")
			err := fixture.effect().createTAPDown(
				t.Context(), fixture.targetFD, fixture.targetIdentity, tapName,
				managedTAPAlias(tapName, 1000), 1000,
			)
			assert.Error(t, err)
			assert.Empty(t, fixture.ioctlEvents())
			assert.Equal(t, 1, fixture.eventCount("close_device.30"))
			assert.Equal(t, 1, fixture.eventCount("unlock_thread"))
			assert.Equal(t, fixture.hostIdentity, fixture.currentIdentity())
		})
	}
}

func TestTAPEffectVerifiesTargetNamespaceBeforeOpeningTun(t *testing.T) {
	t.Parallel()

	fixture := newTAPEffectFixture()
	fixture.targetAfterSetns = fixture.hostIdentity
	name := linkName("mvt012345678901")
	err := fixture.effect().createTAPDown(
		t.Context(), fixture.targetFD, fixture.targetIdentity, name, managedTAPAlias(name, 1000), 1000,
	)
	assert.Error(t, err)
	assert.Empty(t, fixture.deviceOpens())
	assert.Empty(t, fixture.ioctlEvents())
	assert.Equal(t, 1, fixture.eventCount("unlock_thread"))
	assert.Equal(t, fixture.hostIdentity, fixture.currentIdentity())
}

func TestTAPEffectCancellationAfterPersistRestoresHostAndClosesQueue(t *testing.T) {
	t.Parallel()

	fixture := newTAPEffectFixture()
	ctx, cancel := context.WithCancel(t.Context())
	fixture.cancelAfterPersist = cancel
	name := linkName("mvt012345678901")
	err := fixture.effect().createTAPDown(
		ctx, fixture.targetFD, fixture.targetIdentity, name, managedTAPAlias(name, 1000), 1000,
	)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, fixture.eventCount("close_device.30"))
	assert.Equal(t, 1, fixture.eventCount("unlock_thread"))
	assert.Equal(t, 0, fixture.cancelledCleanupCalls())
	assert.Equal(t, fixture.hostIdentity, fixture.currentIdentity())
	assert.Equal(t, uint8(1), fixture.route.mustLink(t, fixture.targetFD, name).tunPersist)
}

func TestTAPEffectDoesNotPersistAfterAliasErrorOrCancellation(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		setup       func(*tapEffectFixture)
		cancelAlias bool
	}{
		"alias acknowledgement error": {setup: func(fixture *tapEffectFixture) {
			fixture.route.aliasError = errors.New("alias acknowledgement lost")
		}},
		"alias reinspection mismatch": {setup: func(fixture *tapEffectFixture) {
			fixture.route.applyAlias = false
		}},
		"cancellation after alias": {cancelAlias: true},
	}
	for testName, test := range tests {
		t.Run(testName, func(t *testing.T) {
			fixture := newTAPEffectFixture()
			if test.setup != nil {
				test.setup(fixture)
			}
			ctx := t.Context()
			if test.cancelAlias {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(t.Context())
				fixture.route.cancelAlias = cancel
			}
			name := linkName("mvt012345678901")
			err := fixture.effect().createTAPDown(
				ctx, fixture.targetFD, fixture.targetIdentity, name, managedTAPAlias(name, 1000), 1000,
			)
			assert.Error(t, err)
			assert.NotContains(t, fixture.ioctlEvents(), "TUNSETPERSIST fd=30 persist=true")
			assert.Nil(t, fixture.route.link(fixture.targetFD, name))
			assert.Equal(t, 1, fixture.eventCount("close_device.30"))
			assert.Equal(t, fixture.hostIdentity, fixture.currentIdentity())
		})
	}
}

func TestTAPEffectUnprovenHostRestorationReturnsFatalClassificationAndRetiresWorker(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*tapEffectFixture){
		"setns failure": func(fixture *tapEffectFixture) {
			fixture.restoreError = errors.New("restore failed")
		},
		"identity mismatch": func(fixture *tapEffectFixture) {
			fixture.restoredIdentity = fixture.targetIdentity
		},
	}
	for name, setup := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newTAPEffectFixture()
			setup(fixture)
			name := linkName("mvt012345678901")
			err := fixture.effect().createTAPDown(
				t.Context(), fixture.targetFD, fixture.targetIdentity, name, managedTAPAlias(name, 1000), 1000,
			)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, true, domainErr.Details["network_worker_retired"])
			assert.Equal(t, true, domainErr.Details["network_thread_contaminated"])
			assert.Zero(t, fixture.eventCount("unlock_thread"))
			assert.True(t, fixture.workerRetired())
			assert.Equal(t, 1, fixture.eventCount("close_device.30"))
		})
	}
}

type tapDeviceOpen struct {
	path  string
	flags int
}

type tapEffectFixture struct {
	mu sync.Mutex

	hostIdentity       namespaceIdentity
	targetIdentity     namespaceIdentity
	targetAfterSetns   namespaceIdentity
	restoredIdentity   namespaceIdentity
	targetFD           int
	current            namespaceIdentity
	namespaceFDs       map[int]namespaceIdentity
	nextNamespaceFD    int
	locked             bool
	retired            bool
	restoreError       error
	deviceStat         unix.Stat_t
	cancelAfterPersist context.CancelFunc
	persistSawAlias    bool
	queueName          linkName
	route              *fakeRouteNetlink

	events           []string
	ioctls           []string
	opens            []tapDeviceOpen
	cancelledCleanup int
}

func newTAPEffectFixture() *tapEffectFixture {
	host := namespaceIdentity{device: 5, inode: 300}
	target := namespaceIdentity{device: 5, inode: 400}
	return &tapEffectFixture{
		hostIdentity: host, targetIdentity: target, targetAfterSetns: target, restoredIdentity: host,
		targetFD: 20, current: host, namespaceFDs: map[int]namespaceIdentity{20: target}, nextNamespaceFD: 10,
		deviceStat: unix.Stat_t{Mode: unix.S_IFCHR | 0o600, Rdev: unix.Mkdev(tapDeviceMajor, tapDeviceMinor)},
		route:      newFakeRouteNetlink(),
	}
}

func (fixture *tapEffectFixture) effect() *tapEffect {
	return &tapEffect{
		deps: tapEffectDeps{
			namespace:     fixture.namespaceDependencies(),
			openDevice:    fixture.openDevice,
			fstatDevice:   fixture.fstatDevice,
			closeDevice:   fixture.closeDevice,
			setIFF:        fixture.setIFF,
			setOwner:      fixture.setOwner,
			setPersistent: fixture.setPersistent,
		},
		policy: namespaceEffectPolicy{expectedUID: 0, expectedGID: 0},
		route:  fixture.route,
	}
}

func (fixture *tapEffectFixture) namespaceDependencies() namespaceEffectDeps {
	deps := systemNamespaceEffectDeps()
	deps.runWorker = func(ctx context.Context, worker func(context.Context) error) error {
		fixture.record("run_worker", ctx, false)
		err := worker(ctx)
		fixture.mu.Lock()
		if fixture.locked {
			fixture.retired = true
			fixture.locked = false
		}
		fixture.mu.Unlock()
		return err
	}
	deps.lockThread = func(ctx context.Context) error {
		fixture.record("lock_thread", ctx, false)
		fixture.mu.Lock()
		fixture.locked = true
		fixture.mu.Unlock()
		return nil
	}
	deps.unlockThread = func(ctx context.Context) error {
		fixture.record("unlock_thread", ctx, true)
		fixture.mu.Lock()
		fixture.locked = false
		fixture.mu.Unlock()
		return nil
	}
	deps.open = fixture.openNamespace
	deps.fstat = fixture.fstatNamespace
	deps.fstatfs = func(ctx context.Context, fd int, stat *unix.Statfs_t) error {
		fixture.record(fmt.Sprintf("fstatfs_namespace.%d", fd), ctx, false)
		stat.Type = unix.NSFS_MAGIC
		return nil
	}
	deps.ioctlNamespaceType = func(ctx context.Context, fd int) (int, error) {
		fixture.record(fmt.Sprintf("nstype_namespace.%d", fd), ctx, false)
		return unix.CLONE_NEWNET, nil
	}
	deps.setns = fixture.setNamespace
	deps.close = func(ctx context.Context, fd int) error {
		fixture.record(fmt.Sprintf("close_namespace.%d", fd), ctx, true)
		return nil
	}
	return deps
}

func (fixture *tapEffectFixture) openNamespace(ctx context.Context, path string, flags int, _ uint32) (int, error) {
	fixture.record("open_namespace", ctx, false)
	if path != namespaceThreadNetworkPath || flags != namespaceCurrentOpenFlags {
		return -1, errors.New("unexpected namespace open")
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fd := fixture.nextNamespaceFD
	fixture.nextNamespaceFD++
	identity := fixture.current
	if identity == fixture.hostIdentity && fd > 10 {
		identity = fixture.restoredIdentity
	}
	fixture.namespaceFDs[fd] = identity
	return fd, nil
}

func (fixture *tapEffectFixture) fstatNamespace(ctx context.Context, fd int, stat *unix.Stat_t) error {
	fixture.record(fmt.Sprintf("fstat_namespace.%d", fd), ctx, false)
	fixture.mu.Lock()
	identity, ok := fixture.namespaceFDs[fd]
	fixture.mu.Unlock()
	if !ok {
		return errors.New("unknown namespace descriptor")
	}
	*stat = unix.Stat_t{
		Mode: unix.S_IFREG | namespaceHandleMode, Uid: 0, Gid: 0, Nlink: 1,
		Dev: identity.device, Ino: identity.inode,
	}
	return nil
}

func (fixture *tapEffectFixture) setNamespace(ctx context.Context, fd, namespaceType int) error {
	fixture.record(fmt.Sprintf("setns.%d", fd), ctx, true)
	if namespaceType != unix.CLONE_NEWNET {
		return errors.New("unexpected namespace type")
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fd == fixture.targetFD {
		fixture.current = fixture.targetAfterSetns
		return nil
	}
	if fd == 10 {
		if fixture.restoreError != nil {
			return fixture.restoreError
		}
		fixture.current = fixture.hostIdentity
		return nil
	}
	return errors.New("unexpected namespace descriptor")
}

func (fixture *tapEffectFixture) openDevice(ctx context.Context, path string, flags int, _ uint32) (int, error) {
	fixture.record("open_device", ctx, false)
	fixture.mu.Lock()
	fixture.opens = append(fixture.opens, tapDeviceOpen{path: path, flags: flags})
	fixture.mu.Unlock()
	return 30, nil
}

func (fixture *tapEffectFixture) fstatDevice(ctx context.Context, fd int, stat *unix.Stat_t) error {
	fixture.record(fmt.Sprintf("fstat_device.%d", fd), ctx, false)
	fixture.mu.Lock()
	*stat = fixture.deviceStat
	fixture.mu.Unlock()
	return nil
}

func (fixture *tapEffectFixture) closeDevice(ctx context.Context, fd int) error {
	fixture.record(fmt.Sprintf("close_device.%d", fd), ctx, true)
	fixture.route.mu.Lock()
	if link, ok := fixture.route.links[fixture.targetFD][fixture.queueName]; ok && link.tunPersist == 0 {
		delete(fixture.route.links[fixture.targetFD], fixture.queueName)
	}
	fixture.route.mu.Unlock()
	return nil
}

func (fixture *tapEffectFixture) setIFF(ctx context.Context, fd int, name linkName, flags uint16) error {
	fixture.recordIOCTL(ctx, fmt.Sprintf("TUNSETIFF fd=%d name=%s flags=%#x", fd, name, flags))
	fixture.mu.Lock()
	fixture.queueName = name
	fixture.mu.Unlock()
	fixture.route.put(fixture.targetFD, routeLinkObservation{
		name: name, kind: linkKindTAP, ifIndex: 21, tunType: unix.IFF_TAP,
	})
	return nil
}

func (fixture *tapEffectFixture) setOwner(ctx context.Context, fd int, owner OwnerUID) error {
	fixture.recordIOCTL(ctx, fmt.Sprintf("TUNSETOWNER fd=%d owner=%d", fd, owner))
	link := fixture.route.link(fixture.targetFD, fixture.queueName)
	if link != nil {
		link.tunOwner = uint32(owner)
		fixture.route.put(fixture.targetFD, *link)
	}
	return nil
}

func (fixture *tapEffectFixture) setPersistent(ctx context.Context, fd int, persistent bool) error {
	fixture.recordIOCTL(ctx, fmt.Sprintf("TUNSETPERSIST fd=%d persist=%t", fd, persistent))
	link := fixture.route.link(fixture.targetFD, fixture.queueName)
	if link != nil {
		fixture.mu.Lock()
		fixture.persistSawAlias = link.alias == managedTAPAlias(fixture.queueName, OwnerUID(link.tunOwner))
		fixture.mu.Unlock()
		if persistent {
			link.tunPersist = 1
		}
		fixture.route.put(fixture.targetFD, *link)
	}
	if fixture.cancelAfterPersist != nil {
		fixture.cancelAfterPersist()
	}
	return nil
}

func (fixture *tapEffectFixture) recordIOCTL(ctx context.Context, event string) {
	fixture.record(event, ctx, false)
	fixture.mu.Lock()
	fixture.ioctls = append(fixture.ioctls, event)
	fixture.mu.Unlock()
}

func (fixture *tapEffectFixture) record(event string, ctx context.Context, cleanup bool) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.events = append(fixture.events, event)
	if cleanup && ctx.Err() != nil {
		fixture.cancelledCleanup++
	}
}

func (fixture *tapEffectFixture) eventCount(event string) int {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	count := 0
	for _, candidate := range fixture.events {
		if candidate == event {
			count++
		}
	}
	return count
}

func (fixture *tapEffectFixture) ioctlEvents() []string {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return append([]string(nil), fixture.ioctls...)
}

func (fixture *tapEffectFixture) deviceOpens() []tapDeviceOpen {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return append([]tapDeviceOpen(nil), fixture.opens...)
}

func (fixture *tapEffectFixture) workerRetired() bool {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return fixture.retired
}

func (fixture *tapEffectFixture) currentIdentity() namespaceIdentity {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return fixture.current
}

func (fixture *tapEffectFixture) cancelledCleanupCalls() int {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return fixture.cancelledCleanup
}

func (fixture *tapEffectFixture) persistenceSawManagedAlias() bool {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return fixture.persistSawAlias
}
