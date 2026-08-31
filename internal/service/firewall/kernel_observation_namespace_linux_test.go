package firewall

import (
	"context"
	"errors"
	"sync"
	"syscall"
	"testing"

	"github.com/mdlayher/netlink"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

func TestKernelObservationNamespaceAuthorityBindsFreshTransportsToExactRetainedNamespace(t *testing.T) {
	t.Parallel()

	fixture := newKernelObservationNamespaceFixture()
	authority := newKernelObservationNamespaceAuthorityWithDeps(fixture.dependencies())
	lease, err := authority.retainAfterGeneration(
		t.Context(),
		&fakeGenerationNetworkAuthority{identity: fixture.identity},
		fixture.generationLease(t),
	)
	require.NoError(t, err)
	require.NotNil(t, lease)
	factory, err := lease.snapshotTransportFactory()
	require.NoError(t, err)

	first, err := factory.newTransport(t.Context(), &kernelObservationReceiveBudget{})
	require.NoError(t, err)
	second, err := factory.newTransport(t.Context(), &kernelObservationReceiveBudget{})
	require.NoError(t, err)
	assert.NotSame(t, first, second)
	require.NoError(t, first.closeConnection(t.Context()))
	require.NoError(t, second.closeConnection(t.Context()))
	require.NoError(t, lease.Release(t.Context()))

	assert.Equal(t, []string{kernelObservationCurrentNetworkNamespacePath}, fixture.openPaths)
	require.Len(t, fixture.dialConfigs, 2)
	for _, config := range fixture.dialConfigs {
		assert.Equal(t, fixture.duplicateFD, config.NetNS)
		assert.True(t, config.Strict)
		assert.Zero(t, config.Groups)
		assert.Zero(t, config.PID)
	}
	assert.Equal(t, []int{unix.NETLINK_NETFILTER, unix.NETLINK_NETFILTER}, fixture.dialFamilies)
	assert.Equal(t, []int{fixture.openFD, fixture.duplicateFD}, fixture.closedNamespaceFDs)
	for _, connection := range fixture.connections {
		assert.Equal(t, 1, connection.closeCount())
	}
}

// Rationale: A descriptor that is not the exact close-on-exec nsfs network namespace cannot authorize a socket.
// Every rejected duplicate must be closed before the original pin, and no default-namespace dial may occur.
func TestKernelObservationNamespaceAuthorityRejectsUnsafeOrDriftedDescriptors(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*kernelObservationNamespaceFixture){
		"unsafe opened descriptor": func(fixture *kernelObservationNamespaceFixture) {
			fixture.openFD = 2
		},
		"opened descriptor is not regular": func(fixture *kernelObservationNamespaceFixture) {
			stat := fixture.stats[fixture.openFD]
			stat.Mode = unix.S_IFDIR | 0500
			fixture.stats[fixture.openFD] = stat
		},
		"wrong namespace filesystem": func(fixture *kernelObservationNamespaceFixture) {
			fixture.filesystems[fixture.openFD] = 0
		},
		"wrong namespace type": func(fixture *kernelObservationNamespaceFixture) {
			fixture.namespaceTypes[fixture.openFD] = unix.CLONE_NEWUTS
		},
		"network authority identity mismatch": func(fixture *kernelObservationNamespaceFixture) {
			fixture.networkIdentity.inode++
		},
		"generation record identity mismatch": func(fixture *kernelObservationNamespaceFixture) {
			fixture.record.binding.namespace.inode++
		},
		"unsafe duplicate descriptor": func(fixture *kernelObservationNamespaceFixture) {
			fixture.duplicateFD = 1
		},
		"duplicate identity drift": func(fixture *kernelObservationNamespaceFixture) {
			stat := fixture.stats[fixture.duplicateFD]
			stat.Ino++
			fixture.stats[fixture.duplicateFD] = stat
		},
		"duplicate lacks close-on-exec": func(fixture *kernelObservationNamespaceFixture) {
			fixture.fdFlags[fixture.duplicateFD] = 0
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newKernelObservationNamespaceFixture()
			mutate(fixture)
			authority := newKernelObservationNamespaceAuthorityWithDeps(fixture.dependencies())
			lease, err := authority.retainAfterGeneration(
				t.Context(),
				&fakeGenerationNetworkAuthority{identity: fixture.networkIdentity},
				fixture.generationLease(t),
			)
			assert.Nil(t, lease)
			require.Error(t, err)
			assert.Empty(t, fixture.dialConfigs)
			if fixture.duplicateCalls != 0 && fixture.duplicateFD != fixture.openFD {
				require.NotEmpty(t, fixture.closedNamespaceFDs)
				assert.Equal(t, fixture.duplicateFD, fixture.closedNamespaceFDs[0])
			}
			if len(fixture.openPaths) != 0 && fixture.openFD >= 0 {
				require.NotEmpty(t, fixture.closedNamespaceFDs)
				assert.Equal(t, fixture.openFD, fixture.closedNamespaceFDs[len(fixture.closedNamespaceFDs)-1])
			}
		})
	}
}

// Rationale: Cancellation after a namespace-bound socket is opened returns partial transport authority so the
// collector's existing cleanup path closes that exact connection once.
func TestKernelObservationNamespaceFactoryCancellationClosesConnectionOnce(t *testing.T) {
	t.Parallel()

	fixture := newKernelObservationNamespaceFixture()
	ctx, cancel := context.WithCancel(t.Context())
	fixture.afterDial = cancel
	authority := newKernelObservationNamespaceAuthorityWithDeps(fixture.dependencies())
	lease, err := authority.retainAfterGeneration(
		t.Context(),
		&fakeGenerationNetworkAuthority{identity: fixture.identity},
		fixture.generationLease(t),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lease.Release(context.Background())) })
	factory, err := lease.snapshotTransportFactory()
	require.NoError(t, err)

	_, err = exchangeKernelSnapshot(
		ctx,
		factory,
		&kernelObservationReceiveBudget{},
		kernelSnapshotGenerationRequest(),
		kernelObservationReplySpec{mode: kernelObservationReplySingular},
	)
	require.ErrorIs(t, err, context.Canceled)
	require.Len(t, fixture.connections, 1)
	assert.Equal(t, 1, fixture.connections[0].closeCount())
}

func TestKernelObservationNamespaceLeaseCopiesReleaseOnceAndCacheError(t *testing.T) {
	t.Parallel()

	fixture := newKernelObservationNamespaceFixture()
	closeFailure := errors.New("injected retained namespace close failure")
	fixture.closeErrors[fixture.duplicateFD] = closeFailure
	authority := newKernelObservationNamespaceAuthorityWithDeps(fixture.dependencies())
	lease, err := authority.retainAfterGeneration(
		t.Context(),
		&fakeGenerationNetworkAuthority{identity: fixture.identity},
		fixture.generationLease(t),
	)
	require.NoError(t, err)
	copyOfLease := *lease

	const callers = 24
	results := make(chan error, callers)
	var group sync.WaitGroup
	for index := range callers {
		group.Add(1)
		go func(useCopy bool) {
			defer group.Done()
			if useCopy {
				results <- copyOfLease.Release(t.Context())
				return
			}
			results <- lease.Release(t.Context())
		}(index%2 == 0)
	}
	group.Wait()
	close(results)
	var first error
	for releaseErr := range results {
		require.ErrorIs(t, releaseErr, closeFailure)
		if first == nil {
			first = releaseErr
		} else {
			assert.Same(t, first, releaseErr)
		}
	}
	assert.Equal(t, 1, fixture.closeCount(fixture.duplicateFD))
	_, err = lease.snapshotTransportFactory()
	assert.Error(t, err)
}

// Rationale: Descriptor cleanup is secondary evidence. It must preserve a classified primary and close a rejected
// duplicate before the original namespace pin.
func TestKernelObservationNamespaceAuthorityCleanupPreservesPrimaryDomainError(t *testing.T) {
	t.Parallel()

	fixture := newKernelObservationNamespaceFixture()
	primaryCause := errors.New("duplicate inspection failed")
	primary := &errs.DomainError{
		Code: errs.CodeNetworkAlreadyExists, Message: "classified namespace failure", Op: "firewall-inspect",
		Entity: "host-netns", Class: errs.ClassConflict, Err: primaryCause,
		Details: map[string]any{"evidence": "original"},
	}
	fixture.statErrors[fixture.duplicateFD] = primary
	duplicateClose := errors.New("duplicate close failed")
	originalClose := errors.New("original close failed")
	fixture.closeErrors[fixture.duplicateFD] = duplicateClose
	fixture.closeErrors[fixture.openFD] = originalClose
	authority := newKernelObservationNamespaceAuthorityWithDeps(fixture.dependencies())

	lease, err := authority.retainAfterGeneration(
		t.Context(),
		&fakeGenerationNetworkAuthority{identity: fixture.identity},
		fixture.generationLease(t),
	)
	assert.Nil(t, lease)
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, primary.Code, domainErr.Code)
	assert.Equal(t, primary.Message, domainErr.Message)
	assert.Equal(t, primary.Op, domainErr.Op)
	assert.Equal(t, primary.Entity, domainErr.Entity)
	assert.Equal(t, primary.Class, domainErr.Class)
	assert.Equal(t, "original", domainErr.Details["evidence"])
	assert.Equal(t, true, domainErr.Details[generationCleanupFailedDetailKey])
	assert.ErrorIs(t, err, primary)
	assert.ErrorIs(t, err, primaryCause)
	assert.ErrorIs(t, err, duplicateClose)
	assert.ErrorIs(t, err, originalClose)
	assert.Equal(t, map[string]any{"evidence": "original"}, primary.Details)
	assert.Equal(t, []int{fixture.duplicateFD, fixture.openFD}, fixture.closedNamespaceFDs)
}

type fakeKernelObservationGenerationLease struct {
	record generationRecord
	err    error
}

func (lease *fakeKernelObservationGenerationLease) admittedRecord() (generationRecord, error) {
	return cloneGenerationRecord(lease.record), lease.err
}

func (*fakeKernelObservationGenerationLease) replaceAdmittedRecord(context.Context, generationRecord) error {
	return errors.New("unexpected generation replacement")
}

func (*fakeKernelObservationGenerationLease) Release(context.Context) error { return nil }

type kernelObservationNamespaceFixture struct {
	identity           generationNamespaceIdentity
	networkIdentity    generationNamespaceIdentity
	record             generationRecord
	openFD             int
	duplicateFD        int
	stats              map[int]unix.Stat_t
	statErrors         map[int]error
	filesystems        map[int]int64
	namespaceTypes     map[int]int
	fdFlags            map[int]int
	closeErrors        map[int]error
	openPaths          []string
	duplicateCalls     int
	closedNamespaceFDs []int
	dialFamilies       []int
	dialConfigs        []netlink.Config
	connections        []*kernelObservationNamespaceConnection
	afterDial          func()
}

func newKernelObservationNamespaceFixture() *kernelObservationNamespaceFixture {
	identity := generationNamespaceIdentity{device: 41, inode: 73}
	record, err := newCommittedGenerationRecord(
		generationRecordBinding{bootID: testFirewallBootID, namespace: identity},
		generationRecordIdentity{id: testFirewallGenerationA},
	)
	if err != nil {
		panic(err)
	}
	return &kernelObservationNamespaceFixture{
		identity: identity, networkIdentity: identity, record: record,
		openFD: 10, duplicateFD: 20,
		stats: map[int]unix.Stat_t{
			10: {Mode: unix.S_IFREG | 0444, Dev: 41, Ino: 73},
			20: {Mode: unix.S_IFREG | 0444, Dev: 41, Ino: 73},
		},
		statErrors:     make(map[int]error),
		filesystems:    map[int]int64{10: unix.NSFS_MAGIC, 20: unix.NSFS_MAGIC},
		namespaceTypes: map[int]int{10: unix.CLONE_NEWNET, 20: unix.CLONE_NEWNET},
		fdFlags:        map[int]int{10: unix.FD_CLOEXEC, 20: unix.FD_CLOEXEC},
		closeErrors:    make(map[int]error),
	}
}

func (fixture *kernelObservationNamespaceFixture) generationLease(t *testing.T) generationPersistentLease {
	t.Helper()
	return &fakeKernelObservationGenerationLease{record: fixture.record}
}

func (fixture *kernelObservationNamespaceFixture) dependencies() kernelObservationNamespaceDeps {
	return kernelObservationNamespaceDeps{
		open: func(_ context.Context, path string, flags int, _ uint32) (int, error) {
			fixture.openPaths = append(fixture.openPaths, path)
			if flags != kernelObservationNamespaceOpenFlags {
				return -1, errors.New("unexpected namespace open flags")
			}
			return fixture.openFD, nil
		},
		duplicateCloexec: func(_ context.Context, fd int, minimum int) (int, error) {
			fixture.duplicateCalls++
			if fd != fixture.openFD || minimum != 3 {
				return -1, errors.New("unexpected namespace duplication request")
			}
			return fixture.duplicateFD, nil
		},
		fstat: func(_ context.Context, fd int, stat *unix.Stat_t) error {
			if err := fixture.statErrors[fd]; err != nil {
				return err
			}
			value, ok := fixture.stats[fd]
			if !ok {
				return errors.New("unexpected namespace stat descriptor")
			}
			*stat = value
			return nil
		},
		fstatfs: func(_ context.Context, fd int, stat *unix.Statfs_t) error {
			value, ok := fixture.filesystems[fd]
			if !ok {
				return errors.New("unexpected namespace filesystem descriptor")
			}
			stat.Type = value
			return nil
		},
		ioctlNamespaceType: func(_ context.Context, fd int) (int, error) {
			value, ok := fixture.namespaceTypes[fd]
			if !ok {
				return 0, errors.New("unexpected namespace type descriptor")
			}
			return value, nil
		},
		getFDFlags: func(_ context.Context, fd int) (int, error) {
			value, ok := fixture.fdFlags[fd]
			if !ok {
				return 0, errors.New("unexpected namespace flags descriptor")
			}
			return value, nil
		},
		close: func(_ context.Context, fd int) error {
			fixture.closedNamespaceFDs = append(fixture.closedNamespaceFDs, fd)
			return fixture.closeErrors[fd]
		},
		dial: func(_ context.Context, family int, config *netlink.Config) (kernelObservationNetlinkConnection, error) {
			fixture.dialFamilies = append(fixture.dialFamilies, family)
			if config == nil {
				return nil, errors.New("namespace-bound netlink config is missing")
			}
			fixture.dialConfigs = append(fixture.dialConfigs, *config)
			connection := &kernelObservationNamespaceConnection{}
			fixture.connections = append(fixture.connections, connection)
			if fixture.afterDial != nil {
				fixture.afterDial()
			}
			return connection, nil
		},
	}
}

func (fixture *kernelObservationNamespaceFixture) closeCount(fd int) int {
	count := 0
	for _, closed := range fixture.closedNamespaceFDs {
		if closed == fd {
			count++
		}
	}
	return count
}

type kernelObservationNamespaceConnection struct {
	mu         sync.Mutex
	closeCalls int
}

func (*kernelObservationNamespaceConnection) Send(netlink.Message) (netlink.Message, error) {
	return netlink.Message{}, errors.New("unexpected send")
}

func (*kernelObservationNamespaceConnection) SyscallConn() (syscall.RawConn, error) {
	return nil, errors.New("unexpected raw connection")
}

func (connection *kernelObservationNamespaceConnection) Close() error {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	connection.closeCalls++
	return nil
}

func (connection *kernelObservationNamespaceConnection) closeCount() int {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	return connection.closeCalls
}
