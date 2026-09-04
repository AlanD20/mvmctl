package firewall

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
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
	lease := kernelApplicationLease(t, before, target)

	result, err := lease.applyKernelGeneration(t.Context(), connection, observed, before, target)
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
	lease := kernelApplicationLease(t, before, target)

	result, err := lease.applyKernelGeneration(
		t.Context(), connection, kernelObservation{empty: true}, before, target,
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
	lease := kernelApplicationLease(t, before, target)
	lease.state.mu.Lock()
	foreignBefore := generationRecordIdentity{id: testFirewallGenerationC}
	lease.state.record.committed = foreignBefore
	lease.state.record.before = &foreignBefore
	lease.state.mu.Unlock()
	connection := &recordingKernelApplicationConnection{}

	result, err := lease.applyKernelGeneration(
		t.Context(), connection, kernelObservation{empty: true}, before, target,
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
	tests := map[string]func(*persistentGenerationLease, *kernelObservation){
		"empty marker with tables": func(_ *persistentGenerationLease, observed *kernelObservation) {
			observed.empty = true
		},
		"partial tables": func(_ *persistentGenerationLease, observed *kernelObservation) {
			observed.tables = observed.tables[:1]
		},
		"wrong fixed name": func(_ *persistentGenerationLease, observed *kernelObservation) {
			observed.tables[0].name = "foreign_bridge"
		},
		"reordered tables": func(_ *persistentGenerationLease, observed *kernelObservation) {
			observed.tables[0], observed.tables[1] = observed.tables[1], observed.tables[0]
		},
		"same-name foreign chain": func(_ *persistentGenerationLease, observed *kernelObservation) {
			observed.tables[0].chains[0].name = "foreign"
		},
		"same-name foreign userdata": func(_ *persistentGenerationLease, observed *kernelObservation) {
			observed.tables[0].rules[0].userdata[0] ^= 0xff
		},
		"same-name foreign expression": func(_ *persistentGenerationLease, observed *kernelObservation) {
			observed.tables[0].rules[0].expressions[0].(*expr.Meta).Register++
		},
		"record target mismatch": func(lease *persistentGenerationLease, _ *kernelObservation) {
			lease.state.mu.Lock()
			lease.state.record.target = &generationRecordIdentity{id: testFirewallGenerationC}
			lease.state.mu.Unlock()
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			lease := kernelApplicationLease(t, before, target)
			observed := kernelObservationFromCompiled(before)
			mutate(lease, &observed)
			connection := &recordingKernelApplicationConnection{}

			result, err := lease.applyKernelGeneration(
				t.Context(), connection, observed, before, target,
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
		"one authoritative network loses every base rule": func(target *compiledGeneration) {
			wipedNetwork := target.networkIDs[1]
			retained := target.inet.rules[:0]
			for _, rule := range target.inet.rules {
				if strings.HasSuffix(rule.identity, "."+wipedNetwork) {
					continue
				}
				retained = append(retained, rule)
			}
			target.inet.rules = retained
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
			lease := kernelApplicationLease(t, before, target)

			result, err := lease.applyKernelGeneration(
				t.Context(), connection, kernelObservation{empty: true}, before, target,
			)
			require.Error(t, err)
			assert.False(t, result.commitAttempted)
			if diff := cmp.Diff([]string{"close"}, connection.events); diff != "" {
				t.Errorf("invalid target effects mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestKernelGenerationApplicationSealRejectsMutatedAllowExpressionBeforeQueueing(t *testing.T) {
	t.Parallel()

	target, err := compileGeneration(compatibilityIntent())
	require.NoError(t, err)
	allow := requireCompiledRule(t, target, "bridge.allow.vm."+compatibilityIntent().vmGrants[0].id)
	comparison, ok := allow.Exprs[1].(*expr.Cmp)
	require.True(t, ok)
	comparison.Register++
	before, err := compileGeneration(generationIntent{})
	require.NoError(t, err)
	lease := kernelApplicationLease(t, before, target)
	connection := &recordingKernelApplicationConnection{}

	result, err := lease.applyKernelGeneration(
		t.Context(), connection, kernelObservation{empty: true}, before, target,
	)
	require.ErrorContains(t, err, "integrity seal")
	assert.False(t, result.commitAttempted)
	if diff := cmp.Diff([]string{"close"}, connection.events); diff != "" {
		t.Errorf("unsealed compiler mutation effects mismatch (-want +got):\n%s", diff)
	}
	assert.Zero(t, connection.flushCalls)
}

// Rationale: A structurally valid applying record is copyable data, not the retained global lock/store capability.
// Fabricating an exact record without those live capabilities must never authorize deletion or Flush.
func TestKernelGenerationApplicationRejectsFabricatedRecordWithoutPersistentLeaseAuthority(t *testing.T) {
	t.Parallel()

	before, err := compileGeneration(generationIntent{})
	require.NoError(t, err)
	target, err := compileGeneration(compatibilityIntent())
	require.NoError(t, err)
	fabricated := kernelApplicationApplyingRecord(t, before, target)
	require.NoError(t, validateGenerationRecord(fabricated))
	lease := &persistentGenerationLease{state: &persistentGenerationLeaseState{
		record: fabricated, evidence: generationRecordFileEvidence{device: 41, inode: 73}, active: true,
	}}
	connection := &recordingKernelApplicationConnection{}

	result, err := lease.applyKernelGeneration(
		t.Context(), connection, kernelObservationFromCompiled(before), before, target,
	)
	require.ErrorContains(t, err, "does not retain complete mutation authority")
	assert.False(t, result.commitAttempted)
	if diff := cmp.Diff([]string{"close"}, connection.events); diff != "" {
		t.Errorf("fabricated-record effects mismatch (-want +got):\n%s", diff)
	}
	assert.Zero(t, connection.flushCalls)
}

func TestPersistentGenerationLeaseSerializesAuthorityAcrossKernelFlush(t *testing.T) {
	t.Parallel()

	before, err := compileGeneration(generationIntent{})
	require.NoError(t, err)
	target, err := compileGeneration(compatibilityIntent())
	require.NoError(t, err)
	lease := kernelApplicationLease(t, before, target)
	connection := newBlockingKernelApplicationConnection()
	type applicationResult struct {
		result kernelApplicationResult
		err    error
	}
	completed := make(chan applicationResult, 1)
	go func() {
		result, applyErr := lease.applyKernelGeneration(
			t.Context(), connection, kernelObservation{empty: true}, before, target,
		)
		completed <- applicationResult{result: result, err: applyErr}
	}()
	<-connection.flushStarted
	if lease.state.applicationMu.TryLock() {
		lease.state.applicationMu.Unlock()
		t.Fatal("persistent application serialization was not held across Flush")
	}
	connection.mu.Lock()
	flushRelease := connection.flushRelease
	connection.flushRelease = nil
	close(flushRelease)
	connection.mu.Unlock()

	got := <-completed
	require.NoError(t, got.err)
	assert.True(t, got.result.commitAttempted)
}

func TestGoogleNFTablesApplicationConnectionFlushesOneProtocolBatch(t *testing.T) {
	t.Parallel()

	before, err := compileGeneration(compatibilityIntent())
	require.NoError(t, err)
	targetIntent := compatibilityIntent()
	targetIntent.hostServices = nil
	target, err := compileGeneration(targetIntent)
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
	lease := kernelApplicationLease(t, before, target)

	result, err := lease.applyKernelGeneration(
		t.Context(), connection, kernelObservationFromCompiled(before), before, target,
	)
	require.NoError(t, err)
	assert.True(t, result.commitAttempted)
	require.Len(t, batches, 1)
	require.NotEmpty(t, batches[0])
	assert.Equal(t, netlink.HeaderType(unix.NFNL_MSG_BATCH_BEGIN), batches[0][0].Header.Type)
	assert.Equal(t, netlink.HeaderType(unix.NFNL_MSG_BATCH_END), batches[0][len(batches[0])-1].Header.Type)
	wantCommands := kernelApplicationProtocolCommands(target)
	gotCommands := make([]kernelApplicationProtocolCommand, 0, len(batches[0])-2)
	for _, message := range batches[0][1 : len(batches[0])-1] {
		gotCommands = append(gotCommands, decodeKernelApplicationProtocolCommand(t, message))
	}
	if diff := cmp.Diff(
		wantCommands,
		gotCommands,
		cmp.AllowUnexported(kernelApplicationProtocolCommand{}),
	); diff != "" {
		t.Errorf("nftables protocol commands mismatch (-want +got):\n%s", diff)
	}
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
	lease := kernelApplicationLease(t, before, target)

	result, err := lease.applyKernelGeneration(
		t.Context(), connection, kernelObservation{empty: true}, before, target,
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
	lease := kernelApplicationLease(t, before, target)

	result, err := lease.applyKernelGeneration(
		t.Context(), connection, kernelObservation{empty: true}, before, target,
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
	lease := kernelApplicationLease(t, before, target)

	result, err := lease.applyKernelGeneration(
		t.Context(), connection, kernelObservation{empty: true}, before, target,
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
	lease := kernelApplicationLease(t, before, target)

	result, err := lease.applyKernelGeneration(ctx, connection, kernelObservation{empty: true}, before, target)
	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, result.commitAttempted)
	if diff := cmp.Diff([]string{"close"}, connection.events); diff != "" {
		t.Errorf("pre-cancel application effects mismatch (-want +got):\n%s", diff)
	}
	assert.Zero(t, connection.flushCalls)
	assert.Zero(t, connection.interruptCalls)
	assert.Equal(t, 1, connection.closeCalls)
}

// Rationale: v0.3.0 makes CloseLasting clear the retained socket before closing it. Cancellation may interrupt only
// that socket's deadline; closing it while Flush is active would permit a transient redial outside the namespace.
func TestKernelGenerationApplicationCancellationInterruptsRetainedSocketBeforeClose(t *testing.T) {
	t.Parallel()

	target, err := compileGeneration(compatibilityIntent())
	require.NoError(t, err)
	before, err := compileGeneration(generationIntent{})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	connection := newBlockingKernelApplicationConnection()
	lease := kernelApplicationLease(t, before, target)
	go func() {
		<-connection.flushStarted
		cancel()
	}()

	result, err := lease.applyKernelGeneration(ctx, connection, kernelObservation{empty: true}, before, target)
	require.ErrorIs(t, err, context.Canceled)
	assert.True(t, result.commitAttempted)
	assert.Equal(t, 1, connection.flushCalls)
	assert.Equal(t, 1, connection.interruptCalls)
	assert.Equal(t, 1, connection.closeCalls)
	assert.False(t, connection.closedWhileFlush)
	assert.False(t, connection.flushActive)
	assert.Equal(t, 1, connection.mutationBatches)
	if diff := cmp.Diff([]string{"flush", "interrupt", "close"}, connection.commitEvents); diff != "" {
		t.Errorf("cancelled commit-boundary effects mismatch (-want +got):\n%s", diff)
	}
}

func TestKernelGenerationApplicationPreservesCancellationInterruptFlushAndCloseFailures(t *testing.T) {
	t.Parallel()

	target, err := compileGeneration(compatibilityIntent())
	require.NoError(t, err)
	before, err := compileGeneration(generationIntent{})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	connection := newBlockingKernelApplicationConnection()
	connection.interruptErr = errors.New("injected retained-socket interrupt failure")
	connection.flushErr = errors.New("injected cancelled flush failure")
	connection.closeErr = errors.New("injected cancelled close failure")
	lease := kernelApplicationLease(t, before, target)
	go func() {
		<-connection.flushStarted
		cancel()
	}()

	result, err := lease.applyKernelGeneration(ctx, connection, kernelObservation{empty: true}, before, target)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, err, connection.interruptErr)
	require.ErrorIs(t, err, connection.flushErr)
	require.ErrorIs(t, err, connection.closeErr)
	assert.True(t, result.commitAttempted)
	assert.False(t, connection.closedWhileFlush)
	if diff := cmp.Diff([]string{"flush", "interrupt", "close"}, connection.commitEvents); diff != "" {
		t.Errorf("failed cancelled commit effects mismatch (-want +got):\n%s", diff)
	}
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
	}
	if diff := cmp.Diff(want, fixture.events); diff != "" {
		t.Errorf("unsafe-restoration retirement effects mismatch (-want +got):\n%s", diff)
	}
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

func TestGoogleNFTablesApplicationConnectionWiresLastingSocketConfiguration(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_800_000_000, 0)
	var configured *netlink.Conn
	var calls []string
	socketDeps := kernelApplicationSocketConfigurationDeps{
		setDeadline: func(connection *netlink.Conn, deadline time.Time) error {
			configured = connection
			assert.Equal(t, now.Add(kernelApplicationSocketTimeout), deadline)
			calls = append(calls, "deadline")
			return nil
		},
		setReadBuffer: func(connection *netlink.Conn, size int) error {
			assert.Same(t, configured, connection)
			assert.Equal(t, kernelApplicationSocketBufferBytes, size)
			calls = append(calls, "receive buffer")
			return nil
		},
		setSendBuffer: func(connection *netlink.Conn, size int) error {
			assert.Same(t, configured, connection)
			assert.Equal(t, kernelApplicationSocketBufferBytes, size)
			calls = append(calls, "send buffer")
			return nil
		},
		inspectBuffer: func(connection *netlink.Conn) (int, int, error) {
			assert.Same(t, configured, connection)
			calls = append(calls, "inspect buffers")
			return kernelApplicationSocketBufferBytes, kernelApplicationSocketBufferBytes, nil
		},
		close: func(*netlink.Conn) error {
			calls = append(calls, "option close")
			return nil
		},
	}
	constructorCalls := 0
	constructor := func(options ...nftables.ConnOption) (*nftables.Conn, error) {
		constructorCalls++
		return nftables.New(append(options, nftables.WithTestDial(func([]netlink.Message) ([]netlink.Message, error) {
			return nil, nil
		}))...)
	}

	connection, err := newGoogleNFTablesApplicationConnectionWithDeps(
		t.Context(), now, constructor, socketDeps,
	)
	require.NoError(t, err)
	concrete, ok := connection.(*googleNFTablesApplicationConnection)
	require.True(t, ok)
	assert.Same(t, configured, concrete.netlinkConnection)
	assert.Equal(t, 1, constructorCalls)
	if diff := cmp.Diff(
		[]string{"deadline", "receive buffer", "send buffer", "inspect buffers"},
		calls,
	); diff != "" {
		t.Errorf("production socket-option wiring mismatch (-want +got):\n%s", diff)
	}
	require.NoError(t, connection.close())
}

func TestKernelApplicationSocketConfigurationRejectsInadequateBuffersAndCloses(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		readBytes int
		sendBytes int
	}{
		"receive buffer too small": {
			readBytes: kernelApplicationSocketBufferBytes - 1,
			sendBytes: kernelApplicationSocketBufferBytes,
		},
		"send buffer too small": {
			readBytes: kernelApplicationSocketBufferBytes,
			sendBytes: kernelApplicationSocketBufferBytes - 1,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
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
					return nil
				},
				inspectBuffer: func(*netlink.Conn) (int, int, error) {
					calls = append(calls, "inspect buffers")
					return tc.readBytes, tc.sendBytes, nil
				},
				close: func(*netlink.Conn) error {
					calls = append(calls, "close")
					return nil
				},
			}

			err := configureKernelApplicationSocket(new(netlink.Conn), time.Unix(1_800_000_000, 0), deps)
			require.ErrorContains(t, err, "verify kernel application socket buffers")
			want := []string{"deadline", "receive buffer", "send buffer", "inspect buffers", "close"}
			if diff := cmp.Diff(want, calls); diff != "" {
				t.Errorf("inadequate-buffer cleanup mismatch (-want +got):\n%s", diff)
			}
		})
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
	mu               sync.Mutex
	events           []string
	commitEvents     []string
	flushErr         error
	interruptErr     error
	closeErr         error
	flushCalls       int
	interruptCalls   int
	closeCalls       int
	mutationBatches  int
	flushActive      bool
	closedWhileFlush bool
	flushStarted     chan struct{}
	flushRelease     chan struct{}
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
	connection.mutationBatches++
	connection.flushActive = true
	connection.events = append(connection.events, "flush")
	connection.commitEvents = append(connection.commitEvents, "flush")
	started := connection.flushStarted
	release := connection.flushRelease
	connection.mu.Unlock()
	if started != nil {
		close(started)
		<-release
	}
	connection.mu.Lock()
	connection.flushActive = false
	connection.mu.Unlock()
	return connection.flushErr
}

func (connection *recordingKernelApplicationConnection) interrupt() error {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	connection.interruptCalls++
	connection.events = append(connection.events, "interrupt")
	connection.commitEvents = append(connection.commitEvents, "interrupt")
	if connection.flushRelease != nil {
		close(connection.flushRelease)
		connection.flushRelease = nil
	}
	return connection.interruptErr
}

func (connection *recordingKernelApplicationConnection) close() error {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	connection.closeCalls++
	connection.closedWhileFlush = connection.closedWhileFlush || connection.flushActive
	connection.events = append(connection.events, "close")
	connection.commitEvents = append(connection.commitEvents, "close")
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

type kernelApplicationProtocolCommand struct {
	messageType netlink.HeaderType
	family      nftables.TableFamily
	table       string
	chain       string
	generation  string
	rule        string
}

func kernelApplicationProtocolCommands(target compiledGeneration) []kernelApplicationProtocolCommand {
	commands := []kernelApplicationProtocolCommand{
		{
			messageType: kernelApplicationNetlinkMessageType(unix.NFT_MSG_DELTABLE),
			family:      nftables.TableFamilyBridge,
			table:       managedBridgeTableName,
		},
		{
			messageType: kernelApplicationNetlinkMessageType(unix.NFT_MSG_DELTABLE),
			family:      nftables.TableFamilyINet,
			table:       managedINetTableName,
		},
	}
	for _, table := range [...]compiledTable{target.bridge, target.inet} {
		commands = append(commands, kernelApplicationProtocolCommand{
			messageType: kernelApplicationNetlinkMessageType(unix.NFT_MSG_NEWTABLE),
			family:      table.table.Family,
			table:       table.table.Name,
		})
		for _, chain := range table.chains {
			commands = append(commands, kernelApplicationProtocolCommand{
				messageType: kernelApplicationNetlinkMessageType(unix.NFT_MSG_NEWCHAIN),
				family:      table.table.Family,
				table:       table.table.Name,
				chain:       chain.Name,
			})
		}
		for _, rule := range table.rules {
			commands = append(commands, kernelApplicationProtocolCommand{
				messageType: kernelApplicationNetlinkMessageType(unix.NFT_MSG_NEWRULE),
				family:      table.table.Family,
				table:       table.table.Name,
				chain:       rule.rule.Chain.Name,
				generation:  target.id,
				rule:        rule.identity,
			})
		}
	}
	return commands
}

func decodeKernelApplicationProtocolCommand(
	t *testing.T,
	message netlink.Message,
) kernelApplicationProtocolCommand {
	t.Helper()
	require.GreaterOrEqual(t, len(message.Data), 4)
	command := kernelApplicationProtocolCommand{
		messageType: message.Header.Type,
		family:      nftables.TableFamily(message.Data[0]),
	}
	attributes, err := netlink.UnmarshalAttributes(message.Data[4:])
	require.NoError(t, err)
	switch message.Header.Type {
	case kernelApplicationNetlinkMessageType(unix.NFT_MSG_DELTABLE),
		kernelApplicationNetlinkMessageType(unix.NFT_MSG_NEWTABLE):
		command.table = kernelApplicationProtocolAttribute(t, attributes, unix.NFTA_TABLE_NAME)
	case kernelApplicationNetlinkMessageType(unix.NFT_MSG_NEWCHAIN):
		command.table = kernelApplicationProtocolAttribute(t, attributes, unix.NFTA_CHAIN_TABLE)
		command.chain = kernelApplicationProtocolAttribute(t, attributes, unix.NFTA_CHAIN_NAME)
	case kernelApplicationNetlinkMessageType(unix.NFT_MSG_NEWRULE):
		command.table = kernelApplicationProtocolAttribute(t, attributes, unix.NFTA_RULE_TABLE)
		command.chain = kernelApplicationProtocolAttribute(t, attributes, unix.NFTA_RULE_CHAIN)
		userdata := kernelApplicationProtocolAttributeBytes(t, attributes, unix.NFTA_RULE_USERDATA)
		identity, decodeErr := decodeRuleIdentity(userdata)
		require.NoError(t, decodeErr)
		command.generation = identity.generation
		command.rule = identity.rule
	default:
		t.Fatalf("unexpected nftables protocol message type %d", message.Header.Type)
	}
	return command
}

func kernelApplicationProtocolAttributeBytes(
	t *testing.T,
	attributes []netlink.Attribute,
	wantType uint16,
) []byte {
	t.Helper()
	for _, attribute := range attributes {
		if attribute.Type == wantType {
			return attribute.Data
		}
	}
	t.Fatalf("nftables protocol attribute %d is absent", wantType)
	return nil
}

func kernelApplicationProtocolAttribute(
	t *testing.T,
	attributes []netlink.Attribute,
	wantType uint16,
) string {
	t.Helper()
	return string(bytes.TrimSuffix(kernelApplicationProtocolAttributeBytes(t, attributes, wantType), []byte{0}))
}

func kernelApplicationNetlinkMessageType(messageType int) netlink.HeaderType {
	return netlink.HeaderType((unix.NFNL_SUBSYS_NFTABLES << 8) | messageType)
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

func kernelApplicationLease(
	t *testing.T,
	before compiledGeneration,
	target compiledGeneration,
) *persistentGenerationLease {
	t.Helper()
	authority, _, _ := testPersistentGenerationAuthority(
		kernelApplicationApplyingRecord(t, before, target),
		true,
	)
	lease, err := authority.acquireAfterNetwork(t.Context(), testGenerationNetworkAuthority())
	require.NoError(t, err)
	concrete, ok := lease.(*persistentGenerationLease)
	require.True(t, ok)
	t.Cleanup(func() {
		require.NoError(t, concrete.Release(context.Background()))
	})
	return concrete
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
