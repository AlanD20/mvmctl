package firewall

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/nftables"
	"github.com/mdlayher/netlink"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestKernelGenerationApplicationQueuesOneCompleteReplacementBatch(t *testing.T) {
	t.Parallel()

	before, err := compileGeneration(compatibilityIntent())
	require.NoError(t, err)
	targetIntent := compatibilityIntent()
	targetIntent.hostServices = nil
	target, err := compileGeneration(targetIntent)
	require.NoError(t, err)
	observed := kernelObservationFromCompiledGeneration(t, before)
	connection := &recordingKernelApplicationConnection{}

	result, err := applyKernelGeneration(t.Context(), connection, observed, target)
	require.NoError(t, err)
	assert.True(t, result.commitAttempted)

	want := make([]string, 0, 2+2+len(target.bridge.chains)+len(target.bridge.rules)+
		len(target.inet.chains)+len(target.inet.rules))
	want = append(want,
		"delete table bridge/mvmctl_bridge",
		"delete table inet/mvmctl_inet",
		"add table bridge/mvmctl_bridge",
	)
	for _, chain := range target.bridge.chains {
		want = append(want, "add chain bridge/mvmctl_bridge/"+chain.Name)
	}
	for _, rule := range target.bridge.rules {
		want = append(want, "add rule bridge/mvmctl_bridge/"+rule.rule.Chain.Name+"/"+rule.identity)
	}
	want = append(want, "add table inet/mvmctl_inet")
	for _, chain := range target.inet.chains {
		want = append(want, "add chain inet/mvmctl_inet/"+chain.Name)
	}
	for _, rule := range target.inet.rules {
		want = append(want, "add rule inet/mvmctl_inet/"+rule.rule.Chain.Name+"/"+rule.identity)
	}
	want = append(want, "flush", "close")
	if diff := cmp.Diff(want, connection.events); diff != "" {
		t.Errorf("kernel application order mismatch (-want +got):\n%s", diff)
	}
	assert.Equal(t, 1, connection.flushCalls)
	assert.Equal(t, 1, connection.closeCalls)
}

// Rationale: An admitted empty pre-snapshot means neither reserved table exists. Deleting fixed names anyway would
// let absence fail an otherwise valid initial application and would exceed the snapshot's deletion authority.
func TestKernelGenerationApplicationDoesNotDeleteAbsentTables(t *testing.T) {
	t.Parallel()

	target, err := compileGeneration(compatibilityIntent())
	require.NoError(t, err)
	connection := &recordingKernelApplicationConnection{}

	result, err := applyKernelGeneration(t.Context(), connection, kernelObservation{empty: true}, target)
	require.NoError(t, err)
	assert.True(t, result.commitAttempted)
	for _, event := range connection.events {
		assert.NotContains(t, event, "delete table")
	}
	assert.Equal(t, 1, connection.flushCalls)
	assert.Equal(t, 1, connection.closeCalls)
}

// Rationale: Application is the last in-process boundary before root mutates the host firewall. A malformed compiled
// value must not remove the terminal deny policy or redirect a rule through an undeclared parent chain.
func TestKernelGenerationApplicationRejectsBrokenDefaultDenyAndParentChainBeforeQueueing(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*compiledGeneration){
		"missing bridge default deny": func(target *compiledGeneration) {
			for index, rule := range target.bridge.rules {
				if rule.identity == "bridge.default-deny."+compatibilityIntent().endpoints[0].id {
					target.bridge.rules = append(target.bridge.rules[:index], target.bridge.rules[index+1:]...)
					return
				}
			}
		},
		"rule references undeclared parent chain": func(target *compiledGeneration) {
			target.bridge.rules[0].rule.Chain = &nftables.Chain{
				Name: "foreign", Table: target.bridge.table,
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			target, err := compileGeneration(compatibilityIntent())
			require.NoError(t, err)
			mutate(&target)
			connection := &recordingKernelApplicationConnection{}

			result, err := applyKernelGeneration(t.Context(), connection, kernelObservation{empty: true}, target)
			require.Error(t, err)
			assert.False(t, result.commitAttempted)
			assert.Equal(t, []string{"close"}, connection.events)
		})
	}
}

func TestGoogleNFTablesApplicationConnectionFlushesOneProtocolBatch(t *testing.T) {
	t.Parallel()

	target, err := compileGeneration(compatibilityIntent())
	require.NoError(t, err)
	var batches [][]netlink.Message
	acknowledgements := 0
	underlying, err := nftables.New(
		nftables.AsLasting(),
		nftables.WithTestDial(func(request []netlink.Message) ([]netlink.Message, error) {
			if len(request) != 0 {
				batches = append(batches, append([]netlink.Message(nil), request...))
				acknowledgements = len(request) - 2
				return nil, nil
			}
			if acknowledgements == 0 {
				return nil, errors.New("unexpected netlink receive")
			}
			acknowledgements--
			return []netlink.Message{{Header: netlink.Header{Type: netlink.Error}, Data: make([]byte, 4)}}, nil
		}),
	)
	require.NoError(t, err)
	connection := &googleNFTablesApplicationConnection{connection: underlying}

	result, err := applyKernelGeneration(t.Context(), connection, kernelObservation{empty: true}, target)
	require.NoError(t, err)
	assert.True(t, result.commitAttempted)
	require.Len(t, batches, 1)
	require.NotEmpty(t, batches[0])
	assert.Equal(t, netlink.HeaderType(unix.NFNL_MSG_BATCH_BEGIN), batches[0][0].Header.Type)
	assert.Equal(t, netlink.HeaderType(unix.NFNL_MSG_BATCH_END), batches[0][len(batches[0])-1].Header.Type)
	wantCommands := 2 + len(target.bridge.chains) + len(target.bridge.rules) +
		len(target.inet.chains) + len(target.inet.rules)
	assert.Equal(t, wantCommands+2, len(batches[0]))
}

// Rationale: Flush is the sole kernel commit boundary. A batch failure has an ambiguous kernel outcome, closes the
// used lasting connection, and cannot retry that connection.
func TestKernelGenerationApplicationReturnsAmbiguousSingleFlushFailure(t *testing.T) {
	t.Parallel()

	target, err := compileGeneration(compatibilityIntent())
	require.NoError(t, err)
	flushFailure := errors.New("injected batch failure")
	connection := &recordingKernelApplicationConnection{flushErr: flushFailure}

	result, err := applyKernelGeneration(t.Context(), connection, kernelObservation{empty: true}, target)
	require.ErrorIs(t, err, flushFailure)
	assert.True(t, result.commitAttempted)
	assert.Equal(t, 1, connection.flushCalls)
	assert.Equal(t, 1, connection.closeCalls)
}

// Rationale: Lasting-connection close follows Flush and is part of the ambiguous commit boundary. A close failure
// cannot be reported as success even when Flush returned nil, and the connection cannot be closed or flushed again.
func TestKernelGenerationApplicationReturnsAmbiguousCloseFailure(t *testing.T) {
	t.Parallel()

	target, err := compileGeneration(compatibilityIntent())
	require.NoError(t, err)
	closeFailure := errors.New("injected lasting-connection close failure")
	connection := &recordingKernelApplicationConnection{closeErr: closeFailure}

	result, err := applyKernelGeneration(t.Context(), connection, kernelObservation{empty: true}, target)
	require.ErrorIs(t, err, closeFailure)
	assert.True(t, result.commitAttempted)
	assert.Equal(t, 1, connection.flushCalls)
	assert.Equal(t, 1, connection.closeCalls)
}

// Rationale: Cancellation after opening but before queueing is known not to have crossed the commit boundary. The
// unused lasting connection is nevertheless an owned resource and must be closed exactly once.
func TestKernelGenerationApplicationCancellationBeforeQueueingClosesConnection(t *testing.T) {
	t.Parallel()

	target, err := compileGeneration(compatibilityIntent())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	connection := &recordingKernelApplicationConnection{}

	result, err := applyKernelGeneration(ctx, connection, kernelObservation{empty: true}, target)
	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, result.commitAttempted)
	assert.Equal(t, 0, connection.flushCalls)
	assert.Equal(t, 1, connection.closeCalls)
}

// Rationale: Cancellation must close the lasting socket to interrupt a blocked Flush. A completed-looking flush after
// cancellation cannot be reported as success, and the connection must still be closed exactly once.
func TestKernelGenerationApplicationCancellationInterruptsFlushAndClosesOnce(t *testing.T) {
	t.Parallel()

	target, err := compileGeneration(compatibilityIntent())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	connection := newBlockingKernelApplicationConnection()
	go func() {
		<-connection.flushStarted
		cancel()
	}()

	result, err := applyKernelGeneration(ctx, connection, kernelObservation{empty: true}, target)
	require.ErrorIs(t, err, context.Canceled)
	assert.True(t, result.commitAttempted)
	assert.Equal(t, 1, connection.flushCalls)
	assert.Equal(t, 1, connection.closeCalls)
}

func TestKernelApplicationConnectionRechecksEntersAndRestoresExactNamespace(t *testing.T) {
	t.Parallel()

	identity := generationNamespaceIdentity{device: 41, inode: 73}
	connection := &recordingKernelApplicationConnection{}
	fixture := newKernelApplicationNamespaceFixture(identity, connection)
	lease := &kernelObservationNamespaceLease{state: &kernelObservationNamespaceLeaseState{
		deps: fixture.observationDeps(), applicationDeps: fixture.applicationDeps(), fd: 20, identity: identity,
		active: true,
	}}

	got, err := lease.newApplicationConnection(t.Context())
	require.NoError(t, err)
	assert.Same(t, connection, got)

	want := []string{
		"inspect retained 20",
		"run worker",
		"lock thread",
		"open current",
		"inspect current 30",
		"inspect retained 20",
		"setns 20",
		"open current",
		"inspect current 31",
		"new lasting connection",
		"setns 30",
		"open current",
		"inspect current 32",
		"close 32",
		"close 31",
		"close 30",
		"unlock thread",
	}
	if diff := cmp.Diff(want, fixture.events); diff != "" {
		t.Errorf("application namespace event order mismatch (-want +got):\n%s", diff)
	}
}

// Rationale: A worker whose restoration cannot be proven must never return to the Go runtime. The newly opened
// lasting connection is closed, and the locked thread is deliberately retired instead of unlocked.
func TestKernelApplicationConnectionRetiresWorkerWhenNamespaceRestorationDiffers(t *testing.T) {
	t.Parallel()

	identity := generationNamespaceIdentity{device: 41, inode: 73}
	connection := &recordingKernelApplicationConnection{}
	fixture := newKernelApplicationNamespaceFixture(identity, connection)
	fixture.inspected[32] = generationNamespaceIdentity{device: 41, inode: 74}
	lease := &kernelObservationNamespaceLease{state: &kernelObservationNamespaceLeaseState{
		deps: fixture.observationDeps(), applicationDeps: fixture.applicationDeps(), fd: 20, identity: identity,
		active: true,
	}}

	got, err := lease.newApplicationConnection(t.Context())
	assert.Nil(t, got)
	require.Error(t, err)
	assert.ErrorContains(t, err, "open exact namespace-bound")
	assert.Equal(t, 1, connection.closeCalls)
	assert.NotContains(t, fixture.events, "unlock thread")
}

type recordingKernelApplicationConnection struct {
	mu           sync.Mutex
	events       []string
	flushErr     error
	closeErr     error
	flushCalls   int
	closeCalls   int
	flushStarted chan struct{}
	closed       chan struct{}
}

func newBlockingKernelApplicationConnection() *recordingKernelApplicationConnection {
	return &recordingKernelApplicationConnection{
		flushStarted: make(chan struct{}),
		closed:       make(chan struct{}),
	}
}

func (connection *recordingKernelApplicationConnection) deleteTable(table *nftables.Table) {
	connection.record("delete table " + kernelApplicationTableIdentity(table))
}

func (connection *recordingKernelApplicationConnection) addTable(table *nftables.Table) {
	connection.record("add table " + kernelApplicationTableIdentity(table))
}

func (connection *recordingKernelApplicationConnection) addChain(chain *nftables.Chain) {
	connection.record("add chain " + kernelApplicationTableIdentity(chain.Table) + "/" + chain.Name)
}

func (connection *recordingKernelApplicationConnection) addRule(rule compiledRule) {
	connection.record("add rule " + kernelApplicationTableIdentity(rule.rule.Table) + "/" +
		rule.rule.Chain.Name + "/" + rule.identity)
}

func (connection *recordingKernelApplicationConnection) flush() error {
	connection.mu.Lock()
	connection.flushCalls++
	connection.events = append(connection.events, "flush")
	started := connection.flushStarted
	closed := connection.closed
	connection.mu.Unlock()
	if started != nil {
		close(started)
		<-closed
	}
	return connection.flushErr
}

func (connection *recordingKernelApplicationConnection) close() error {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	connection.closeCalls++
	connection.events = append(connection.events, "close")
	if connection.closed != nil {
		close(connection.closed)
		connection.closed = nil
	}
	return connection.closeErr
}

func (connection *recordingKernelApplicationConnection) record(event string) {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	connection.events = append(connection.events, event)
}

func kernelApplicationTableIdentity(table *nftables.Table) string {
	return fmt.Sprintf("%s/%s", kernelApplicationFamilyName(table.Family), table.Name)
}

func kernelApplicationFamilyName(family nftables.TableFamily) string {
	switch family {
	case nftables.TableFamilyBridge:
		return "bridge"
	case nftables.TableFamilyINet:
		return "inet"
	default:
		return fmt.Sprintf("family-%d", family)
	}
}

func kernelObservationFromCompiledGeneration(t *testing.T, generation compiledGeneration) kernelObservation {
	t.Helper()
	if generation.empty {
		return kernelObservation{empty: true}
	}
	return kernelObservation{tables: []kernelObservedTable{
		{family: generation.bridge.table.Family, name: generation.bridge.table.Name},
		{family: generation.inet.table.Family, name: generation.inet.table.Name},
	}}
}

type kernelApplicationNamespaceFixture struct {
	identity   generationNamespaceIdentity
	connection kernelApplicationConnection
	events     []string
	openFDs    []int
	inspected  map[int]generationNamespaceIdentity
}

func newKernelApplicationNamespaceFixture(
	identity generationNamespaceIdentity,
	connection kernelApplicationConnection,
) *kernelApplicationNamespaceFixture {
	return &kernelApplicationNamespaceFixture{
		identity: identity, connection: connection, openFDs: []int{30, 31, 32},
		inspected: map[int]generationNamespaceIdentity{20: identity, 30: identity, 31: identity, 32: identity},
	}
}

func (fixture *kernelApplicationNamespaceFixture) observationDeps() kernelObservationNamespaceDeps {
	return kernelObservationNamespaceDeps{}
}

func (fixture *kernelApplicationNamespaceFixture) applicationDeps() kernelApplicationNamespaceDeps {
	return kernelApplicationNamespaceDeps{
		runWorker: func(ctx context.Context, worker kernelApplicationNamespaceWorker) (kernelApplicationConnection, error) {
			fixture.events = append(fixture.events, "run worker")
			return worker(ctx)
		},
		lockThread: func(context.Context) error {
			fixture.events = append(fixture.events, "lock thread")
			return nil
		},
		unlockThread: func(context.Context) error {
			fixture.events = append(fixture.events, "unlock thread")
			return nil
		},
		openCurrentNamespace: func(context.Context) (int, error) {
			fixture.events = append(fixture.events, "open current")
			fd := fixture.openFDs[0]
			fixture.openFDs = fixture.openFDs[1:]
			return fd, nil
		},
		inspectNamespace: func(_ context.Context, fd int) (generationNamespaceIdentity, error) {
			label := "current"
			if fd == 20 {
				label = "retained"
			}
			fixture.events = append(fixture.events, fmt.Sprintf("inspect %s %d", label, fd))
			return fixture.inspected[fd], nil
		},
		setNamespace: func(_ context.Context, fd int) error {
			fixture.events = append(fixture.events, fmt.Sprintf("setns %d", fd))
			return nil
		},
		newConnection: func(context.Context) (kernelApplicationConnection, error) {
			fixture.events = append(fixture.events, "new lasting connection")
			return fixture.connection, nil
		},
		closeNamespace: func(_ context.Context, fd int) error {
			fixture.events = append(fixture.events, fmt.Sprintf("close %d", fd))
			return nil
		},
	}
}
