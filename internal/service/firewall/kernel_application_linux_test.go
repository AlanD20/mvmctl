package firewall

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/nftables"
	"github.com/google/nftables/expr"
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
	observed := kernelObservationFromCompiled(before)
	connection := &recordingKernelApplicationConnection{}
	record := kernelApplicationApplyingRecord(t, before, target)

	result, err := applyKernelGeneration(t.Context(), connection, record, observed, before, target)
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
	before, err := compileGeneration(generationIntent{})
	require.NoError(t, err)
	connection := &recordingKernelApplicationConnection{}
	record := kernelApplicationApplyingRecord(t, before, target)

	result, err := applyKernelGeneration(
		t.Context(), connection, record, kernelObservation{empty: true}, before, target,
	)
	require.NoError(t, err)
	assert.True(t, result.commitAttempted)
	for _, event := range connection.events {
		assert.NotContains(t, event, "delete table")
	}
	assert.Equal(t, 1, connection.flushCalls)
	assert.Equal(t, 1, connection.closeCalls)
}

func TestKernelGenerationApplicationRejectsEmptyBeforeWithoutEmptyRecordAuthority(t *testing.T) {
	t.Parallel()

	target, err := compileGeneration(compatibilityIntent())
	require.NoError(t, err)
	before, err := compileGeneration(generationIntent{})
	require.NoError(t, err)
	record := kernelApplicationApplyingRecord(t, before, target)
	foreignBefore := generationRecordIdentity{id: testFirewallGenerationC}
	record.committed = foreignBefore
	record.before = &foreignBefore
	connection := &recordingKernelApplicationConnection{}

	result, err := applyKernelGeneration(
		t.Context(), connection, record, kernelObservation{empty: true}, before, target,
	)
	require.Error(t, err)
	assert.False(t, result.commitAttempted)
	if diff := cmp.Diff([]string{"close"}, connection.events); diff != "" {
		t.Errorf("unbound empty pre-state effects mismatch (-want +got):\n%s", diff)
	}
}

// Rationale: Fixed table names alone are not deletion authority. The admitted applying record, exact expected-before
// compiler output, and every semantic field of the fresh observation must agree before either DelTable is queued.
func TestKernelGenerationApplicationRejectsUnqualifiedPreStateBeforeQueueing(t *testing.T) {
	t.Parallel()

	before, err := compileGeneration(compatibilityIntent())
	require.NoError(t, err)
	targetIntent := compatibilityIntent()
	targetIntent.hostServices = nil
	target, err := compileGeneration(targetIntent)
	require.NoError(t, err)
	tests := map[string]func(*generationRecord, *kernelObservation){
		"empty marker with tables": func(_ *generationRecord, observed *kernelObservation) {
			observed.empty = true
		},
		"partial tables": func(_ *generationRecord, observed *kernelObservation) {
			observed.tables = observed.tables[:1]
		},
		"wrong fixed name": func(_ *generationRecord, observed *kernelObservation) {
			observed.tables[0].name = "foreign_bridge"
		},
		"reordered tables": func(_ *generationRecord, observed *kernelObservation) {
			observed.tables[0], observed.tables[1] = observed.tables[1], observed.tables[0]
		},
		"same-name foreign chain": func(_ *generationRecord, observed *kernelObservation) {
			observed.tables[0].chains[0].name = "foreign"
		},
		"same-name foreign userdata": func(_ *generationRecord, observed *kernelObservation) {
			observed.tables[0].rules[0].userdata[0] ^= 0xff
		},
		"same-name foreign expression": func(_ *generationRecord, observed *kernelObservation) {
			observed.tables[0].rules[0].expressions[0].(*expr.Meta).Register++
		},
		"record target mismatch": func(record *generationRecord, _ *kernelObservation) {
			record.target = &generationRecordIdentity{id: testFirewallGenerationC}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			record := kernelApplicationApplyingRecord(t, before, target)
			observed := kernelObservationFromCompiled(before)
			mutate(&record, &observed)
			connection := &recordingKernelApplicationConnection{}

			result, err := applyKernelGeneration(
				t.Context(), connection, record, observed, before, target,
			)
			require.Error(t, err)
			assert.False(t, result.commitAttempted)
			if diff := cmp.Diff([]string{"close"}, connection.events); diff != "" {
				t.Errorf("unqualified pre-state effects mismatch (-want +got):\n%s", diff)
			}
			assert.Zero(t, connection.flushCalls)
		})
	}
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
		"nonempty target clears mutable rule inventory": func(target *compiledGeneration) {
			target.bridge.rules = nil
			target.inet.rules = nil
			target.ingressGuards = nil
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			target, err := compileGeneration(compatibilityIntent())
			require.NoError(t, err)
			mutate(&target)
			require.NoError(t, bindGenerationApplicationSeal(&target))
			connection := &recordingKernelApplicationConnection{}
			before, beforeErr := compileGeneration(generationIntent{})
			require.NoError(t, beforeErr)
			record := kernelApplicationApplyingRecord(t, before, target)

			result, err := applyKernelGeneration(
				t.Context(), connection, record, kernelObservation{empty: true}, before, target,
			)
			require.Error(t, err)
			assert.False(t, result.commitAttempted)
			if diff := cmp.Diff([]string{"close"}, connection.events); diff != "" {
				t.Errorf("invalid target effects mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestGoogleNFTablesApplicationConnectionFlushesOneProtocolBatch(t *testing.T) {
	t.Parallel()

	target, err := compileGeneration(compatibilityIntent())
	require.NoError(t, err)
	before, err := compileGeneration(generationIntent{})
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
	record := kernelApplicationApplyingRecord(t, before, target)

	result, err := applyKernelGeneration(
		t.Context(), connection, record, kernelObservation{empty: true}, before, target,
	)
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
	before, err := compileGeneration(generationIntent{})
	require.NoError(t, err)
	flushFailure := errors.New("injected batch failure")
	connection := &recordingKernelApplicationConnection{flushErr: flushFailure}
	record := kernelApplicationApplyingRecord(t, before, target)

	result, err := applyKernelGeneration(
		t.Context(), connection, record, kernelObservation{empty: true}, before, target,
	)
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
	before, err := compileGeneration(generationIntent{})
	require.NoError(t, err)
	closeFailure := errors.New("injected lasting-connection close failure")
	connection := &recordingKernelApplicationConnection{closeErr: closeFailure}
	record := kernelApplicationApplyingRecord(t, before, target)

	result, err := applyKernelGeneration(
		t.Context(), connection, record, kernelObservation{empty: true}, before, target,
	)
	require.ErrorIs(t, err, closeFailure)
	assert.True(t, result.commitAttempted)
	assert.Equal(t, 1, connection.flushCalls)
	assert.Equal(t, 1, connection.closeCalls)
}

func TestKernelGenerationApplicationPreservesFlushAndCloseFailures(t *testing.T) {
	t.Parallel()

	target, err := compileGeneration(compatibilityIntent())
	require.NoError(t, err)
	before, err := compileGeneration(generationIntent{})
	require.NoError(t, err)
	flushFailure := errors.New("injected batch failure")
	closeFailure := errors.New("injected lasting-connection close failure")
	connection := &recordingKernelApplicationConnection{flushErr: flushFailure, closeErr: closeFailure}
	record := kernelApplicationApplyingRecord(t, before, target)

	result, err := applyKernelGeneration(
		t.Context(), connection, record, kernelObservation{empty: true}, before, target,
	)
	require.ErrorIs(t, err, flushFailure)
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
	before, err := compileGeneration(generationIntent{})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	connection := &recordingKernelApplicationConnection{}
	record := kernelApplicationApplyingRecord(t, before, target)

	result, err := applyKernelGeneration(ctx, connection, record, kernelObservation{empty: true}, before, target)
	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, result.commitAttempted)
	assert.Equal(t, 0, connection.flushCalls)
	assert.Equal(t, 1, connection.closeCalls)
}

// Rationale: v0.3.0 serializes Flush and CloseLasting on one mutex. Cancellation must not race CloseLasting against
// Flush because a winning close would let Flush open a transient socket outside the retained namespace.
func TestKernelGenerationApplicationCancellationKeepsLastingConnectionUntilFlushReturns(t *testing.T) {
	t.Parallel()

	target, err := compileGeneration(compatibilityIntent())
	require.NoError(t, err)
	before, err := compileGeneration(generationIntent{})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	connection := newBlockingKernelApplicationConnection()
	record := kernelApplicationApplyingRecord(t, before, target)
	closeCallsDuringFlush := make(chan int, 1)
	go func() {
		<-connection.flushStarted
		cancel()
		connection.mu.Lock()
		closeCallsDuringFlush <- connection.closeCalls
		connection.mu.Unlock()
		close(connection.flushRelease)
	}()

	result, err := applyKernelGeneration(ctx, connection, record, kernelObservation{empty: true}, before, target)
	require.ErrorIs(t, err, context.Canceled)
	assert.True(t, result.commitAttempted)
	assert.Equal(t, 0, <-closeCallsDuringFlush)
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

// Rationale: Once setns succeeds, every constructor failure and cancellation must restore and verify the original
// namespace with an uncancelled cleanup context before any descriptor is closed or the worker is unlocked.
func TestKernelApplicationConnectionRestoresAfterPostSetnsConstructorCancellation(t *testing.T) {
	t.Parallel()

	identity := generationNamespaceIdentity{device: 41, inode: 73}
	constructorFailure := errors.New("injected lasting-connection constructor failure")
	ctx, cancel := context.WithCancel(t.Context())
	fixture := newKernelApplicationNamespaceFixture(identity, nil)
	fixture.newConnectionHook = cancel
	fixture.newConnectionErr = constructorFailure
	lease := &kernelObservationNamespaceLease{state: &kernelObservationNamespaceLeaseState{
		deps: fixture.observationDeps(), applicationDeps: fixture.applicationDeps(), fd: 20, identity: identity,
		active: true,
	}}

	got, err := lease.newApplicationConnection(ctx)
	assert.Nil(t, got)
	require.ErrorIs(t, err, constructorFailure)
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
		t.Errorf("constructor-failure restoration order mismatch (-want +got):\n%s", diff)
	}
	for _, contextErr := range fixture.cleanupContextErrors {
		assert.NoError(t, contextErr)
	}
}

func TestKernelApplicationSocketConfigurationSetsDeadlineAndVerifiedScaleBuffers(t *testing.T) {
	t.Parallel()

	connection := new(netlink.Conn)
	deadline := time.Unix(1_800_000_000, 0)
	var calls []string
	deps := kernelApplicationSocketConfigurationDeps{
		setDeadline: func(got *netlink.Conn, value time.Time) error {
			assert.Same(t, connection, got)
			assert.Equal(t, deadline, value)
			calls = append(calls, "deadline")
			return nil
		},
		setReadBuffer: func(got *netlink.Conn, bytes int) error {
			assert.Same(t, connection, got)
			assert.Equal(t, kernelApplicationSocketBufferBytes, bytes)
			calls = append(calls, "receive buffer")
			return nil
		},
		setSendBuffer: func(got *netlink.Conn, bytes int) error {
			assert.Same(t, connection, got)
			assert.Equal(t, kernelApplicationSocketBufferBytes, bytes)
			calls = append(calls, "send buffer")
			return nil
		},
		inspectBuffer: func(got *netlink.Conn) (int, int, error) {
			assert.Same(t, connection, got)
			calls = append(calls, "inspect buffers")
			return kernelApplicationSocketBufferBytes, kernelApplicationSocketBufferBytes, nil
		},
		close: func(*netlink.Conn) error {
			calls = append(calls, "close")
			return nil
		},
	}

	err := configureKernelApplicationSocket(connection, deadline, deps)
	require.NoError(t, err)
	want := []string{"deadline", "receive buffer", "send buffer", "inspect buffers"}
	if diff := cmp.Diff(want, calls); diff != "" {
		t.Errorf("socket configuration order mismatch (-want +got):\n%s", diff)
	}
}

// Rationale: google/nftables v0.3.0 does not close the netlink connection when a SockOption fails. The one mvmctl
// callback must therefore close it itself and preserve both the configuration and close failures.
func TestKernelApplicationSocketConfigurationFailureClosesAndPreservesErrors(t *testing.T) {
	t.Parallel()

	optionFailure := errors.New("injected send buffer failure")
	closeFailure := errors.New("injected option cleanup failure")
	var calls []string
	deps := kernelApplicationSocketConfigurationDeps{
		setDeadline: func(*netlink.Conn, time.Time) error {
			calls = append(calls, "deadline")
			return nil
		},
		setReadBuffer: func(*netlink.Conn, int) error {
			calls = append(calls, "receive buffer")
			return nil
		},
		setSendBuffer: func(*netlink.Conn, int) error {
			calls = append(calls, "send buffer")
			return optionFailure
		},
		inspectBuffer: func(*netlink.Conn) (int, int, error) {
			calls = append(calls, "inspect buffers")
			return 0, 0, nil
		},
		close: func(*netlink.Conn) error {
			calls = append(calls, "close")
			return closeFailure
		},
	}

	err := configureKernelApplicationSocket(new(netlink.Conn), time.Unix(1_800_000_000, 0), deps)
	require.ErrorIs(t, err, optionFailure)
	require.ErrorIs(t, err, closeFailure)
	want := []string{"deadline", "receive buffer", "send buffer", "close"}
	if diff := cmp.Diff(want, calls); diff != "" {
		t.Errorf("failed socket configuration order mismatch (-want +got):\n%s", diff)
	}
}

func TestKernelApplicationSocketDeadlineUsesEarlierCallerBound(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_800_000_000, 0)
	assert.Equal(t, now.Add(kernelApplicationSocketTimeout), kernelApplicationSocketDeadline(t.Context(), now))
	callerDeadline := now.Add(kernelApplicationSocketTimeout / 2)
	ctx, cancel := context.WithDeadline(t.Context(), callerDeadline)
	defer cancel()
	assert.Equal(t, callerDeadline, kernelApplicationSocketDeadline(ctx, now))
}

type recordingKernelApplicationConnection struct {
	mu           sync.Mutex
	events       []string
	flushErr     error
	closeErr     error
	flushCalls   int
	closeCalls   int
	flushStarted chan struct{}
	flushRelease chan struct{}
}

func newBlockingKernelApplicationConnection() *recordingKernelApplicationConnection {
	return &recordingKernelApplicationConnection{
		flushStarted: make(chan struct{}),
		flushRelease: make(chan struct{}),
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
	release := connection.flushRelease
	connection.mu.Unlock()
	if started != nil {
		close(started)
		<-release
	}
	return connection.flushErr
}

func (connection *recordingKernelApplicationConnection) close() error {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	connection.closeCalls++
	connection.events = append(connection.events, "close")
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

func kernelApplicationApplyingRecord(
	t *testing.T,
	before compiledGeneration,
	target compiledGeneration,
) generationRecord {
	t.Helper()
	committed, err := newCommittedGenerationRecord(
		testGenerationRecordBinding(),
		kernelApplicationGenerationIdentity(before),
	)
	require.NoError(t, err)
	applying, err := beginGenerationRecordApplication(
		committed,
		kernelApplicationGenerationIdentity(before),
		kernelApplicationGenerationIdentity(target),
	)
	require.NoError(t, err)
	return applying
}

type kernelApplicationNamespaceFixture struct {
	identity             generationNamespaceIdentity
	connection           kernelApplicationConnection
	events               []string
	openFDs              []int
	inspected            map[int]generationNamespaceIdentity
	newConnectionHook    func()
	newConnectionErr     error
	cleanupContextErrors []error
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
		setNamespace: func(ctx context.Context, fd int) error {
			fixture.events = append(fixture.events, fmt.Sprintf("setns %d", fd))
			if fd != 20 {
				fixture.cleanupContextErrors = append(fixture.cleanupContextErrors, ctx.Err())
			}
			return nil
		},
		newConnection: func(context.Context) (kernelApplicationConnection, error) {
			fixture.events = append(fixture.events, "new lasting connection")
			if fixture.newConnectionHook != nil {
				fixture.newConnectionHook()
			}
			return fixture.connection, fixture.newConnectionErr
		},
		closeNamespace: func(ctx context.Context, fd int) error {
			fixture.events = append(fixture.events, fmt.Sprintf("close %d", fd))
			fixture.cleanupContextErrors = append(fixture.cleanupContextErrors, ctx.Err())
			return nil
		},
	}
}
