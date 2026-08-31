package firewall

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
)

func TestPersistentGenerationAuthorityAcquiresOnlyAfterNetworkProofAndAdmitsExactRecord(t *testing.T) {
	t.Parallel()

	var eventMu sync.Mutex
	events := make([]string, 0, 9)
	appendEvent := func(event string) {
		eventMu.Lock()
		defer eventMu.Unlock()
		events = append(events, event)
	}
	record := mustCommittedGenerationRecord(
		t,
		testGenerationRecordBinding(),
		generationRecordIdentity{empty: true, id: testFirewallGenerationA},
	)
	store := &fakeGenerationAuthorityStore{events: appendEvent, record: record, found: true}
	global := &fakeFirewallGlobalAuthority{events: appendEvent}
	authority := newPersistentGenerationAuthorityWithDeps(persistentGenerationAuthorityDeps{
		openGlobalAuthority: func(context.Context) (firewallGlobalAuthority, error) {
			appendEvent("open firewall authority")
			return global, nil
		},
		openRecordStore: func(context.Context) (generationAuthorityStore, error) {
			appendEvent("open record store")
			return store, nil
		},
		readBootID: func(context.Context) (string, error) {
			appendEvent("read boot ID")
			return testFirewallBootID, nil
		},
	})
	network := &fakeGenerationNetworkAuthority{events: appendEvent, identity: testGenerationRecordBinding().namespace}

	lease, err := authority.acquireAfterNetwork(t.Context(), network)
	require.NoError(t, err)
	require.NotNil(t, lease)
	got, err := lease.admittedRecord()
	require.NoError(t, err)
	if diff := cmp.Diff(record, got, generationRecordCmpOptions...); diff != "" {
		t.Errorf("admitted generation record mismatch (-want +got):\n%s", diff)
	}
	require.NoError(t, lease.Release(t.Context()))

	wantEvents := []string{
		"network authority",
		"open firewall authority",
		"acquire firewall lock",
		"open record store",
		"read boot ID",
		"read record",
		"release record store",
		"release firewall lock",
		"release firewall authority",
	}
	if diff := cmp.Diff(wantEvents, events); diff != "" {
		t.Errorf("generation authority event order mismatch (-want +got):\n%s", diff)
	}
}

// Rationale: Applying state is durable recovery authority only when the complete exact transition and host binding
// survive admission. The inert boundary must retain that state without performing or predeclaring a kernel effect.
func TestPersistentGenerationAuthorityAdmitsExactApplyingRecord(t *testing.T) {
	t.Parallel()

	binding := testGenerationRecordBinding()
	committed := mustCommittedGenerationRecord(
		t,
		binding,
		generationRecordIdentity{empty: true, id: testFirewallGenerationA},
	)
	applying, err := beginGenerationRecordApplication(
		committed,
		generationRecordIdentity{empty: true, id: testFirewallGenerationA},
		generationRecordIdentity{id: testFirewallGenerationB},
	)
	require.NoError(t, err)
	authority, _, _ := testPersistentGenerationAuthority(applying, true)

	lease, err := authority.acquireAfterNetwork(t.Context(), testGenerationNetworkAuthority())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lease.Release(context.Background())) })
	got, err := lease.admittedRecord()
	require.NoError(t, err)
	if diff := cmp.Diff(applying, got, generationRecordCmpOptions...); diff != "" {
		t.Errorf("applying generation record mismatch (-want +got):\n%s", diff)
	}
}

func TestPersistentGenerationLeaseReplacesFromExactAdmittedRecordAndEvidence(t *testing.T) {
	t.Parallel()

	current := testCommittedGenerationRecord(t)
	replacement := testApplyingGenerationRecord(t, current)
	authority, _, store := testPersistentGenerationAuthority(current, true)
	store.evidence = generationRecordFileEvidence{device: 17, inode: 23, size: 101}
	store.replaceProof = generationRecordFileEvidence{device: 17, inode: 29, size: 141}
	lease, err := authority.acquireAfterNetwork(t.Context(), testGenerationNetworkAuthority())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lease.Release(context.Background())) })

	err = lease.replaceAdmittedRecord(t.Context(), replacement)
	require.NoError(t, err)
	assert.Equal(t, int32(1), store.replaceCalls.Load())
	if diff := cmp.Diff(current, store.replaceExpected, generationRecordCmpOptions...); diff != "" {
		t.Errorf("replacement expected record mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(
		store.evidence,
		store.replaceExpectedEvidence,
		cmp.AllowUnexported(generationRecordFileEvidence{}),
	); diff != "" {
		t.Errorf("replacement expected evidence mismatch (-want +got):\n%s", diff)
	}
	got, err := lease.admittedRecord()
	require.NoError(t, err)
	if diff := cmp.Diff(replacement, got, generationRecordCmpOptions...); diff != "" {
		t.Errorf("lease replacement record mismatch (-want +got):\n%s", diff)
	}
}

// Rationale: A postrename error is still a successful authority transfer when readback returns exact new evidence.
// Keeping the old in-memory record would let later cleanup reason from authority that no longer exists.
func TestPersistentGenerationLeaseTransfersProvenReplacementDespitePostcommitError(t *testing.T) {
	t.Parallel()

	current := testCommittedGenerationRecord(t)
	replacement := testApplyingGenerationRecord(t, current)
	postcommit := generationAuthorityError(
		"injected postcommit failure",
		errors.New("parent sync failed"),
		map[string]any{"record_replaced": true, "durability_uncertain": true},
	)
	authority, _, store := testPersistentGenerationAuthority(current, true)
	store.replaceProof = generationRecordFileEvidence{device: 17, inode: 29, size: 141}
	store.replaceErr = postcommit
	lease, err := authority.acquireAfterNetwork(t.Context(), testGenerationNetworkAuthority())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lease.Release(context.Background())) })

	err = lease.replaceAdmittedRecord(t.Context(), replacement)
	require.ErrorIs(t, err, postcommit)
	got, admittedErr := lease.admittedRecord()
	require.NoError(t, admittedErr)
	if diff := cmp.Diff(replacement, got, generationRecordCmpOptions...); diff != "" {
		t.Errorf("proven replacement was not transferred (-want +got):\n%s", diff)
	}
}

// Rationale: Replacement evidence can become unknowable only after rename. The lease must then refuse all later
// mutation instead of guessing whether its retained record is old or new.
func TestPersistentGenerationLeaseBlocksMutationAfterAmbiguousReplacement(t *testing.T) {
	t.Parallel()

	current := testCommittedGenerationRecord(t)
	replacement := testApplyingGenerationRecord(t, current)
	ambiguous := generationAuthorityError(
		"injected ambiguous replacement",
		errors.New("readback failed"),
		map[string]any{"record_replaced": true},
	)
	authority, _, store := testPersistentGenerationAuthority(current, true)
	store.replaceErr = ambiguous
	lease, err := authority.acquireAfterNetwork(t.Context(), testGenerationNetworkAuthority())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lease.Release(context.Background())) })

	err = lease.replaceAdmittedRecord(t.Context(), replacement)
	require.ErrorIs(t, err, ambiguous)
	secondErr := lease.replaceAdmittedRecord(t.Context(), replacement)
	require.Error(t, secondErr)
	assert.ErrorContains(t, secondErr, "ambiguous")
	assert.Equal(t, int32(1), store.replaceCalls.Load())
}

func TestPersistentGenerationLeasePrecommitFailureRetainsMutationAuthority(t *testing.T) {
	t.Parallel()

	current := testCommittedGenerationRecord(t)
	replacement := testApplyingGenerationRecord(t, current)
	precommit := errors.New("injected precommit failure")
	authority, _, store := testPersistentGenerationAuthority(current, true)
	store.replaceErr = precommit
	lease, err := authority.acquireAfterNetwork(t.Context(), testGenerationNetworkAuthority())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lease.Release(context.Background())) })

	err = lease.replaceAdmittedRecord(t.Context(), replacement)
	require.ErrorIs(t, err, precommit)
	store.replaceErr = nil
	store.replaceProof = generationRecordFileEvidence{device: 17, inode: 29, size: 141}
	require.NoError(t, lease.replaceAdmittedRecord(t.Context(), replacement))
	assert.Equal(t, int32(2), store.replaceCalls.Load())
}

func TestPersistentGenerationAuthorityRejectsMissingCorruptAndForeignStateWithoutRepair(t *testing.T) {
	t.Parallel()

	binding := testGenerationRecordBinding()
	foreignBoot := mustCommittedGenerationRecord(
		t,
		generationRecordBinding{
			bootID:    "22345678-1234-4abc-8def-1234567890ab",
			namespace: binding.namespace,
		},
		generationRecordIdentity{id: testFirewallGenerationA},
	)
	foreignNamespace := mustCommittedGenerationRecord(
		t,
		generationRecordBinding{
			bootID: binding.bootID,
			namespace: generationNamespaceIdentity{
				device: binding.namespace.device,
				inode:  binding.namespace.inode + 1,
			},
		},
		generationRecordIdentity{id: testFirewallGenerationA},
	)
	corruptErr := generationAuthorityError("decode corrupt firewall generation record", errors.New("corrupt"), nil)
	tests := map[string]struct {
		record      generationRecord
		found       bool
		readErr     error
		wantMessage string
	}{
		"missing":           {wantMessage: "is missing"},
		"corrupt":           {readErr: corruptErr, wantMessage: "decode corrupt"},
		"foreign boot":      {record: foreignBoot, found: true, wantMessage: "another host boot"},
		"foreign namespace": {record: foreignNamespace, found: true, wantMessage: "another host network namespace"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			authority, global, store := testPersistentGenerationAuthority(tc.record, tc.found)
			store.readErr = tc.readErr

			lease, err := authority.acquireAfterNetwork(t.Context(), testGenerationNetworkAuthority())
			assert.Nil(t, lease)
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.wantMessage)
			assert.Equal(t, int32(1), store.releaseCalls.Load())
			assert.Equal(t, int32(1), global.lock.releaseCalls.Load())
			assert.Equal(t, int32(1), global.releaseCalls.Load())
		})
	}
}

func TestPersistentGenerationAuthorityClassifiesRawRecordStoreReadFailure(t *testing.T) {
	t.Parallel()

	rawFailure := errors.New("injected raw record read failure")
	record := mustCommittedGenerationRecord(
		t,
		testGenerationRecordBinding(),
		generationRecordIdentity{id: testFirewallGenerationA},
	)
	authority, global, store := testPersistentGenerationAuthority(record, true)
	store.readErr = rawFailure

	lease, err := authority.acquireAfterNetwork(t.Context(), testGenerationNetworkAuthority())
	assert.Nil(t, lease)
	require.ErrorIs(t, err, rawFailure)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeNetworkFirewallFailed, domainErr.Code)
	assert.Equal(t, errs.ClassInternal, domainErr.Class)
	assert.Equal(t, "read firewall generation authority record", domainErr.Message)
	assert.Equal(t, int32(1), store.releaseCalls.Load())
	assert.Equal(t, int32(1), global.lock.releaseCalls.Load())
	assert.Equal(t, int32(1), global.releaseCalls.Load())
}

func TestPersistentGenerationAuthorityNeverOpensFirewallBeforeValidNetworkProof(t *testing.T) {
	t.Parallel()

	classified := &errs.DomainError{
		Code: errs.CodeNetworkAlreadyExists, Message: "network proof failed", Class: errs.ClassConflict,
	}
	tests := map[string]*fakeGenerationNetworkAuthority{
		"proof failure": {err: classified},
		"zero device": {
			identity: generationNamespaceIdentity{inode: testFirewallNamespaceIno},
		},
		"zero inode": {
			identity: generationNamespaceIdentity{device: testFirewallNamespaceDev},
		},
	}
	for name, network := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var opened atomic.Bool
			authority := newPersistentGenerationAuthorityWithDeps(persistentGenerationAuthorityDeps{
				openGlobalAuthority: func(context.Context) (firewallGlobalAuthority, error) {
					opened.Store(true)
					return nil, errors.New("must not open")
				},
			})

			lease, err := authority.acquireAfterNetwork(t.Context(), network)
			assert.Nil(t, lease)
			require.Error(t, err)
			assert.False(t, opened.Load())
			if name == "proof failure" {
				assert.Same(t, classified, err)
			}
		})
	}
}

func TestPersistentGenerationAuthorityReleasesEveryPartiallyReturnedCapabilityInReverse(t *testing.T) {
	t.Parallel()

	injected := errors.New("injected acquisition failure")
	tests := map[string]struct {
		inject      func(*persistentGenerationAuthorityDeps, *fakeFirewallGlobalAuthority, *fakeGenerationAuthorityStore)
		wantRelease []string
	}{
		"global open returns capability with error": {
			inject: func(deps *persistentGenerationAuthorityDeps, global *fakeFirewallGlobalAuthority, _ *fakeGenerationAuthorityStore) {
				deps.openGlobalAuthority = func(context.Context) (firewallGlobalAuthority, error) {
					return global, injected
				}
			},
			wantRelease: []string{"release firewall authority"},
		},
		"lock acquisition returns capability with error": {
			inject: func(_ *persistentGenerationAuthorityDeps, global *fakeFirewallGlobalAuthority, _ *fakeGenerationAuthorityStore) {
				global.acquireErr = injected
			},
			wantRelease: []string{"release firewall lock", "release firewall authority"},
		},
		"store open returns capability with error": {
			inject: func(deps *persistentGenerationAuthorityDeps, _ *fakeFirewallGlobalAuthority, store *fakeGenerationAuthorityStore) {
				deps.openRecordStore = func(context.Context) (generationAuthorityStore, error) {
					return store, injected
				}
			},
			wantRelease: []string{
				"release record store", "release firewall lock", "release firewall authority",
			},
		},
		"boot ID read failure": {
			inject: func(deps *persistentGenerationAuthorityDeps, _ *fakeFirewallGlobalAuthority, _ *fakeGenerationAuthorityStore) {
				deps.readBootID = func(context.Context) (string, error) { return "", injected }
			},
			wantRelease: []string{
				"release record store", "release firewall lock", "release firewall authority",
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var eventMu sync.Mutex
			events := make([]string, 0, 3)
			appendEvent := func(event string) {
				eventMu.Lock()
				defer eventMu.Unlock()
				if strings.HasPrefix(event, "release") {
					events = append(events, event)
				}
			}
			global := &fakeFirewallGlobalAuthority{events: appendEvent}
			store := &fakeGenerationAuthorityStore{events: appendEvent}
			deps := persistentGenerationAuthorityDeps{
				openGlobalAuthority: func(context.Context) (firewallGlobalAuthority, error) { return global, nil },
				openRecordStore:     func(context.Context) (generationAuthorityStore, error) { return store, nil },
				readBootID:          func(context.Context) (string, error) { return testFirewallBootID, nil },
			}
			tc.inject(&deps, global, store)
			authority := newPersistentGenerationAuthorityWithDeps(deps)

			lease, err := authority.acquireAfterNetwork(t.Context(), testGenerationNetworkAuthority())
			assert.Nil(t, lease)
			require.ErrorIs(t, err, injected)
			if diff := cmp.Diff(tc.wantRelease, events); diff != "" {
				t.Errorf("partial authority release order mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestPersistentGenerationAuthorityCleanupPreservesClassifiedPrimaryAndFullCauseChain(t *testing.T) {
	t.Parallel()

	primaryCause := errors.New("primary cause")
	primarySibling := errors.New("primary sibling")
	classified := &errs.DomainError{
		Code: errs.CodeNetworkAlreadyExists, Message: "classified primary", Op: "network-authority",
		Entity: "network-a", Class: errs.ClassConflict, Err: primaryCause,
		Details: map[string]any{"evidence": "original"},
	}
	primary := fmt.Errorf("outer primary: %w", errors.Join(classified, primarySibling))
	storeCleanup := errors.New(strings.Repeat("store-cleanup", maxGenerationCleanupDetailBytes))
	lockCleanup := errors.New("lock cleanup")
	globalCleanup := errors.New("global cleanup")
	record := mustCommittedGenerationRecord(
		t,
		testGenerationRecordBinding(),
		generationRecordIdentity{id: testFirewallGenerationA},
	)
	authority, global, store := testPersistentGenerationAuthority(record, true)
	store.readErr = primary
	store.releaseErr = storeCleanup
	global.lock.releaseErr = lockCleanup
	global.releaseErr = globalCleanup

	lease, err := authority.acquireAfterNetwork(t.Context(), testGenerationNetworkAuthority())
	assert.Nil(t, lease)
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, classified.Code, domainErr.Code)
	assert.Equal(t, classified.Message, domainErr.Message)
	assert.Equal(t, classified.Op, domainErr.Op)
	assert.Equal(t, classified.Entity, domainErr.Entity)
	assert.Equal(t, classified.Class, domainErr.Class)
	assert.Equal(t, "original", domainErr.Details["evidence"])
	assert.Equal(t, true, domainErr.Details[generationCleanupFailedDetailKey])
	detail, ok := domainErr.Details[generationCleanupDiagnosticDetailKey].(string)
	require.True(t, ok)
	assert.LessOrEqual(t, len(detail), maxGenerationCleanupDetailBytes)
	assert.Contains(t, detail, "release firewall generation record store")
	assert.ErrorIs(t, err, primary)
	assert.ErrorIs(t, err, classified)
	assert.ErrorIs(t, err, primaryCause)
	assert.ErrorIs(t, err, primarySibling)
	assert.ErrorIs(t, err, storeCleanup)
	assert.ErrorIs(t, err, lockCleanup)
	assert.ErrorIs(t, err, globalCleanup)
	assert.Equal(t, map[string]any{"evidence": "original"}, classified.Details)
}

func TestPersistentGenerationLeaseCopiesReleaseOnceCachedAndWithoutCancellation(t *testing.T) {
	t.Parallel()

	type contextKey struct{}
	record := mustCommittedGenerationRecord(
		t,
		testGenerationRecordBinding(),
		generationRecordIdentity{id: testFirewallGenerationA},
	)
	authority, global, store := testPersistentGenerationAuthority(record, true)
	cleanupCause := errors.New("injected store cleanup")
	store.releaseErr = cleanupCause
	lease, err := authority.acquireAfterNetwork(t.Context(), testGenerationNetworkAuthority())
	require.NoError(t, err)
	concrete := lease.(*persistentGenerationLease)
	copyOfLease := *concrete
	cancelled, cancel := context.WithCancel(context.WithValue(t.Context(), contextKey{}, "retained"))
	cancel()
	store.inspectContext = func(ctx context.Context) {
		store.contextOK.Store(ctx.Err() == nil && ctx.Value(contextKey{}) == "retained")
	}
	global.inspectContext = func(ctx context.Context) {
		global.contextOK.Store(ctx.Err() == nil && ctx.Value(contextKey{}) == "retained")
	}
	global.lock.inspectContext = func(ctx context.Context) {
		global.lock.contextOK.Store(ctx.Err() == nil && ctx.Value(contextKey{}) == "retained")
	}

	const callers = 32
	results := make(chan error, callers)
	var group sync.WaitGroup
	for index := range callers {
		group.Add(1)
		go func(useCopy bool) {
			defer group.Done()
			if useCopy {
				results <- copyOfLease.Release(cancelled)
				return
			}
			results <- lease.Release(cancelled)
		}(index%2 == 0)
	}
	group.Wait()
	close(results)
	var first error
	for releaseErr := range results {
		require.ErrorIs(t, releaseErr, cleanupCause)
		if first == nil {
			first = releaseErr
		} else {
			assert.Same(t, first, releaseErr)
		}
	}
	assert.Equal(t, int32(1), store.releaseCalls.Load())
	assert.Equal(t, int32(1), global.lock.releaseCalls.Load())
	assert.Equal(t, int32(1), global.releaseCalls.Load())
	assert.True(t, store.contextOK.Load())
	assert.True(t, global.lock.contextOK.Load())
	assert.True(t, global.contextOK.Load())
	_, err = lease.admittedRecord()
	require.Error(t, err)
}

type fakeGenerationNetworkAuthority struct {
	events   func(string)
	identity generationNamespaceIdentity
	err      error
}

func (authority *fakeGenerationNetworkAuthority) firewallGenerationNamespace(
	_ context.Context,
) (generationNamespaceIdentity, error) {
	if authority.events != nil {
		authority.events("network authority")
	}
	return authority.identity, authority.err
}

type fakeGenerationAuthorityStore struct {
	events                  func(string)
	record                  generationRecord
	evidence                generationRecordFileEvidence
	found                   bool
	readErr                 error
	replaceProof            generationRecordFileEvidence
	replaceErr              error
	replaceExpected         generationRecord
	replaceExpectedEvidence generationRecordFileEvidence
	replaceCalls            atomic.Int32
	releaseErr              error
	inspectContext          func(context.Context)
	releaseCalls            atomic.Int32
	contextOK               atomic.Bool
}

func (store *fakeGenerationAuthorityStore) read(
	_ context.Context,
) (generationRecord, generationRecordFileEvidence, bool, error) {
	if store.events != nil {
		store.events("read record")
	}
	return store.record, store.evidence, store.found, store.readErr
}

func (store *fakeGenerationAuthorityStore) replace(
	_ context.Context,
	expected generationRecord,
	expectedEvidence generationRecordFileEvidence,
	_ generationRecord,
) (generationRecordFileEvidence, error) {
	store.replaceCalls.Add(1)
	store.replaceExpected = cloneGenerationRecord(expected)
	store.replaceExpectedEvidence = expectedEvidence
	return store.replaceProof, store.replaceErr
}

func (store *fakeGenerationAuthorityStore) Release(ctx context.Context) error {
	store.releaseCalls.Add(1)
	if store.inspectContext != nil {
		store.inspectContext(ctx)
	}
	if store.events != nil {
		store.events("release record store")
	}
	return store.releaseErr
}

type fakeFirewallGlobalAuthority struct {
	events         func(string)
	lock           fakeFirewallGlobalLock
	acquireErr     error
	releaseErr     error
	inspectContext func(context.Context)
	releaseCalls   atomic.Int32
	contextOK      atomic.Bool
}

func (authority *fakeFirewallGlobalAuthority) acquire(ctx context.Context) (firewallGlobalLock, error) {
	if authority.events != nil {
		authority.events("acquire firewall lock")
		authority.lock.events = authority.events
	}
	return &authority.lock, authority.acquireErr
}

func (authority *fakeFirewallGlobalAuthority) Release(ctx context.Context) error {
	authority.releaseCalls.Add(1)
	if authority.inspectContext != nil {
		authority.inspectContext(ctx)
	}
	if authority.events != nil {
		authority.events("release firewall authority")
	}
	return authority.releaseErr
}

type fakeFirewallGlobalLock struct {
	events         func(string)
	releaseErr     error
	inspectContext func(context.Context)
	releaseCalls   atomic.Int32
	contextOK      atomic.Bool
}

func (lock *fakeFirewallGlobalLock) Release(ctx context.Context) error {
	lock.releaseCalls.Add(1)
	if lock.inspectContext != nil {
		lock.inspectContext(ctx)
	}
	if lock.events != nil {
		lock.events("release firewall lock")
	}
	return lock.releaseErr
}

func testGenerationNetworkAuthority() generationNetworkAuthority {
	return &fakeGenerationNetworkAuthority{identity: testGenerationRecordBinding().namespace}
}

func testPersistentGenerationAuthority(
	record generationRecord,
	found bool,
) (*persistentGenerationAuthority, *fakeFirewallGlobalAuthority, *fakeGenerationAuthorityStore) {
	global := &fakeFirewallGlobalAuthority{}
	store := &fakeGenerationAuthorityStore{
		record: record, evidence: generationRecordFileEvidence{device: 11, inode: 12}, found: found,
	}
	authority := newPersistentGenerationAuthorityWithDeps(persistentGenerationAuthorityDeps{
		openGlobalAuthority: func(context.Context) (firewallGlobalAuthority, error) { return global, nil },
		openRecordStore:     func(context.Context) (generationAuthorityStore, error) { return store, nil },
		readBootID:          func(context.Context) (string, error) { return testFirewallBootID, nil },
	})
	return authority, global, store
}
