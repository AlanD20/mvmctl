package firewall

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
)

func TestGenerationApplicationPersistsApplyingBeforeMutationAndVerifiesBeforeCommit(t *testing.T) {
	t.Parallel()

	before, target := testGenerationApplicationPair(t)
	lease, store := testGenerationApplicationLease(t, committedRecordForCompiled(t, before))
	var timelineMu sync.Mutex
	timeline := make([]string, 0, 5)
	recordEvent := func(event string) {
		timelineMu.Lock()
		defer timelineMu.Unlock()
		timeline = append(timeline, event)
	}
	store.recordEvent = recordEvent
	kernel := &fakeKernelGenerationApplication{
		observations: []kernelObservation{
			kernelObservationFromCompiled(before),
			kernelObservationFromCompiled(target),
		},
		results:     []kernelApplicationResult{{commitAttempted: true}},
		recordEvent: recordEvent,
	}

	err := lease.applyCompiledGeneration(t.Context(), kernel, before, target)
	require.NoError(t, err)
	if diff := cmp.Diff([]string{"observe", "apply", "observe"}, kernel.events); diff != "" {
		t.Errorf("kernel event order mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{
		"record applying",
		"observe",
		"apply",
		"observe",
		"record committed",
	}, timeline); diff != "" {
		t.Errorf("generation transaction order mismatch (-want +got):\n%s", diff)
	}
	require.Len(t, store.replacements, 2)
	assert.Equal(t, generationRecordApplying, store.replacements[0].lifecycle)
	assert.Equal(t, generationRecordCommitted, store.replacements[1].lifecycle)
	assert.Equal(t, generationRecordIdentityFromCompiled(target), store.replacements[1].committed)
}

// Rationale: A committed empty generation that is already exact is a no-op. It must not create the reserved tables,
// submit an empty Flush transaction, or churn the durable record.
func TestGenerationApplicationExactEmptyNoOpDoesNotMutate(t *testing.T) {
	t.Parallel()

	empty, err := compileGeneration(generationIntent{})
	require.NoError(t, err)
	lease, store := testGenerationApplicationLease(t, committedRecordForCompiled(t, empty))
	kernel := &fakeKernelGenerationApplication{observations: []kernelObservation{{empty: true}}}

	err = lease.applyCompiledGeneration(t.Context(), kernel, empty, empty)
	require.NoError(t, err)
	if diff := cmp.Diff([]string{"observe"}, kernel.events); diff != "" {
		t.Errorf("exact no-op event order mismatch (-want +got):\n%s", diff)
	}
	assert.Empty(t, store.replacements)
}

// Rationale: Any error after Flush starts is ambiguous until exactly one fresh observation classifies the complete
// generation. Record repair is allowed only after that observation proves exact before or exact target.
func TestGenerationApplicationReconcilesAmbiguousFlushExactlyOnce(t *testing.T) {
	t.Parallel()

	before, target := testGenerationApplicationPair(t)
	flushFailure := errors.New("injected ambiguous flush")
	tests := map[string]struct {
		recovery       func() kernelObservation
		wantState      generationApplicationKernelState
		wantLifecycle  generationRecordLifecycle
		wantGeneration generationRecordIdentity
		wantUnknown    bool
	}{
		"exact before rolls durable state back": {
			recovery:       func() kernelObservation { return kernelObservationFromCompiled(before) },
			wantState:      generationApplicationKernelBefore,
			wantLifecycle:  generationRecordCommitted,
			wantGeneration: generationRecordIdentityFromCompiled(before),
		},
		"exact target commits durable target": {
			recovery:       func() kernelObservation { return kernelObservationFromCompiled(target) },
			wantState:      generationApplicationKernelTarget,
			wantLifecycle:  generationRecordCommitted,
			wantGeneration: generationRecordIdentityFromCompiled(target),
		},
		"unknown retains applying authority": {
			recovery: func() kernelObservation {
				observed := kernelObservationFromCompiled(target)
				observed.generationID = testFirewallGenerationC
				return observed
			},
			wantState:      generationApplicationKernelUnknown,
			wantLifecycle:  generationRecordApplying,
			wantGeneration: generationRecordIdentityFromCompiled(before),
			wantUnknown:    true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			lease, store := testGenerationApplicationLease(t, committedRecordForCompiled(t, before))
			kernel := &fakeKernelGenerationApplication{
				observations: []kernelObservation{kernelObservationFromCompiled(before), tc.recovery()},
				results:      []kernelApplicationResult{{commitAttempted: true}},
				applyErrors:  []error{flushFailure},
			}

			err := lease.applyCompiledGeneration(t.Context(), kernel, before, target)
			require.ErrorIs(t, err, flushFailure)
			if diff := cmp.Diff([]string{"observe", "apply", "observe"}, kernel.events); diff != "" {
				t.Errorf("ambiguous application event order mismatch (-want +got):\n%s", diff)
			}
			assert.Equal(t, tc.wantUnknown, generationApplicationOutcomeUnknown(err))
			assert.Equal(t, string(tc.wantState), generationApplicationKernelStateDetail(err))
			got, admittedErr := lease.admittedRecord()
			require.NoError(t, admittedErr)
			assert.Equal(t, tc.wantLifecycle, got.lifecycle)
			assert.Equal(t, tc.wantGeneration, got.committed)
			if tc.wantUnknown {
				require.NotNil(t, got.before)
				require.NotNil(t, got.target)
				assert.Equal(t, generationRecordIdentityFromCompiled(before), *got.before)
				assert.Equal(t, generationRecordIdentityFromCompiled(target), *got.target)
				assert.Len(t, store.replacements, 1)
			} else {
				assert.Len(t, store.replacements, 2)
			}
		})
	}
}

// Rationale: Recovery after process interruption must never blindly replay an applying transition. Exact target
// completes metadata only, exact before permits one fresh mutation, and any third state remains recoverable applying.
func TestGenerationApplicationRetriesApplyingRecordByExactObservedState(t *testing.T) {
	t.Parallel()

	before, target := testGenerationApplicationPair(t)
	applying := applyingRecordForCompiled(t, before, target)
	tests := map[string]struct {
		observations    []kernelObservation
		results         []kernelApplicationResult
		wantEvents      []string
		wantLifecycle   generationRecordLifecycle
		wantReplacement int
		wantError       bool
	}{
		"target completes without another mutation": {
			observations:    []kernelObservation{kernelObservationFromCompiled(target)},
			wantEvents:      []string{"observe"},
			wantLifecycle:   generationRecordCommitted,
			wantReplacement: 1,
		},
		"before permits one new mutation": {
			observations: []kernelObservation{
				kernelObservationFromCompiled(before),
				kernelObservationFromCompiled(target),
			},
			results:         []kernelApplicationResult{{commitAttempted: true}},
			wantEvents:      []string{"observe", "apply", "observe"},
			wantLifecycle:   generationRecordCommitted,
			wantReplacement: 1,
		},
		"unknown performs no mutation": {
			observations:    []kernelObservation{{empty: false, generationID: testFirewallGenerationC}},
			wantEvents:      []string{"observe"},
			wantLifecycle:   generationRecordApplying,
			wantReplacement: 0,
			wantError:       true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			lease, store := testGenerationApplicationLease(t, applying)
			kernel := &fakeKernelGenerationApplication{
				observations: tc.observations,
				results:      tc.results,
			}

			err := lease.applyCompiledGeneration(t.Context(), kernel, before, target)
			if tc.wantError {
				require.Error(t, err)
				assert.True(t, generationApplicationOutcomeUnknown(err))
			} else {
				require.NoError(t, err)
			}
			if diff := cmp.Diff(tc.wantEvents, kernel.events); diff != "" {
				t.Errorf("retry event order mismatch (-want +got):\n%s", diff)
			}
			assert.Len(t, store.replacements, tc.wantReplacement)
			got, admittedErr := lease.admittedRecord()
			require.NoError(t, admittedErr)
			assert.Equal(t, tc.wantLifecycle, got.lifecycle)
		})
	}
}

// Rationale: Failure before Flush has known exact-before kernel state. It may repair the applying record without a
// recovery readback, while still returning the original initialization or pre-commit cancellation failure.
func TestGenerationApplicationRollsBackKnownPreCommitFailure(t *testing.T) {
	t.Parallel()

	before, target := testGenerationApplicationPair(t)
	openFailure := errors.New("injected connection open failure")
	lease, store := testGenerationApplicationLease(t, committedRecordForCompiled(t, before))
	kernel := &fakeKernelGenerationApplication{
		observations: []kernelObservation{kernelObservationFromCompiled(before)},
		results:      []kernelApplicationResult{{}},
		applyErrors:  []error{openFailure},
	}

	err := lease.applyCompiledGeneration(t.Context(), kernel, before, target)
	require.ErrorIs(t, err, openFailure)
	assert.False(t, generationApplicationOutcomeUnknown(err))
	assert.Equal(t, string(generationApplicationKernelBefore), generationApplicationKernelStateDetail(err))
	if diff := cmp.Diff([]string{"observe", "apply"}, kernel.events); diff != "" {
		t.Errorf("pre-commit failure event order mismatch (-want +got):\n%s", diff)
	}
	require.Len(t, store.replacements, 2)
	assert.Equal(t, generationRecordCommitted, store.replacements[1].lifecycle)
	assert.Equal(t, generationRecordIdentityFromCompiled(before), store.replacements[1].committed)
}

// Rationale: Caller cancellation after Flush begins cannot become success. Recovery detaches cancellation for one
// bounded observation, records an exact target if proven, and still returns the original cancellation.
func TestGenerationApplicationCancellationReconcilesWithFreshContext(t *testing.T) {
	t.Parallel()

	before, target := testGenerationApplicationPair(t)
	ctx, cancel := context.WithCancel(t.Context())
	lease, _ := testGenerationApplicationLease(t, committedRecordForCompiled(t, before))
	kernel := &fakeKernelGenerationApplication{
		observations: []kernelObservation{
			kernelObservationFromCompiled(before),
			kernelObservationFromCompiled(target),
		},
		results:     []kernelApplicationResult{{commitAttempted: true}},
		applyErrors: []error{context.Canceled},
		applyHook:   cancel,
	}

	err := lease.applyCompiledGeneration(ctx, kernel, before, target)
	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, generationApplicationOutcomeUnknown(err))
	assert.Equal(t, string(generationApplicationKernelTarget), generationApplicationKernelStateDetail(err))
	if diff := cmp.Diff([]error{nil, nil}, kernel.observationContextErrors); diff != "" {
		t.Errorf("observation context errors mismatch (-want +got):\n%s", diff)
	}
	got, admittedErr := lease.admittedRecord()
	require.NoError(t, admittedErr)
	assert.Equal(t, generationRecordCommitted, got.lifecycle)
	assert.Equal(t, generationRecordIdentityFromCompiled(target), got.committed)
}

func TestGenerationApplicationRecoveryInspectionFailureRetainsApplying(t *testing.T) {
	t.Parallel()

	before, target := testGenerationApplicationPair(t)
	flushFailure := errors.New("injected ambiguous flush")
	inspectionFailure := errors.New("injected recovery inspection failure")
	lease, _ := testGenerationApplicationLease(t, committedRecordForCompiled(t, before))
	kernel := &fakeKernelGenerationApplication{
		observations: []kernelObservation{kernelObservationFromCompiled(before), {}},
		observeErrs:  []error{nil, inspectionFailure},
		results:      []kernelApplicationResult{{commitAttempted: true}},
		applyErrors:  []error{flushFailure},
	}

	err := lease.applyCompiledGeneration(t.Context(), kernel, before, target)
	require.ErrorIs(t, err, flushFailure)
	require.ErrorIs(t, err, inspectionFailure)
	assert.True(t, generationApplicationOutcomeUnknown(err))
	if diff := cmp.Diff([]string{"observe", "apply", "observe"}, kernel.events); diff != "" {
		t.Errorf("inspection failure event order mismatch (-want +got):\n%s", diff)
	}
	got, admittedErr := lease.admittedRecord()
	require.NoError(t, admittedErr)
	assert.Equal(t, generationRecordApplying, got.lifecycle)
}

// Rationale: Exact-before readback makes the kernel outcome known, but it does not make a failed durable rollback
// disappear. The applying record remains authoritative until a later reconciliation can persist committed-before.
func TestGenerationApplicationPreservesExactBeforeRollbackWriteFailure(t *testing.T) {
	t.Parallel()

	before, target := testGenerationApplicationPair(t)
	flushFailure := errors.New("injected ambiguous flush")
	rollbackFailure := errors.New("injected committed-before write failure")
	lease, store := testGenerationApplicationLease(t, committedRecordForCompiled(t, before))
	store.replaceErrors = []error{nil, rollbackFailure}
	kernel := &fakeKernelGenerationApplication{
		observations: []kernelObservation{
			kernelObservationFromCompiled(before),
			kernelObservationFromCompiled(before),
		},
		results:     []kernelApplicationResult{{commitAttempted: true}},
		applyErrors: []error{flushFailure},
	}

	err := lease.applyCompiledGeneration(t.Context(), kernel, before, target)
	require.ErrorIs(t, err, flushFailure)
	require.ErrorIs(t, err, rollbackFailure)
	assert.False(t, generationApplicationOutcomeUnknown(err))
	assert.Equal(t, string(generationApplicationKernelBefore), generationApplicationKernelStateDetail(err))
	got, admittedErr := lease.admittedRecord()
	require.NoError(t, admittedErr)
	assert.Equal(t, generationRecordApplying, got.lifecycle)
	require.NotNil(t, got.before)
	require.NotNil(t, got.target)
	assert.Equal(t, generationRecordIdentityFromCompiled(before), *got.before)
	assert.Equal(t, generationRecordIdentityFromCompiled(target), *got.target)
}

// Rationale: If committed-target rename may have happened but returned no qualifying evidence, both the final write
// error and the Flush error remain visible. In-memory applying authority is retained and all later mutation is barred.
func TestGenerationApplicationBlocksAfterAmbiguousExactTargetCommitWriteFailure(t *testing.T) {
	t.Parallel()

	before, target := testGenerationApplicationPair(t)
	flushFailure := errors.New("injected ambiguous flush")
	commitFailure := generationAuthorityError(
		"injected committed-target write failure",
		errors.New("injected record readback failure"),
		map[string]any{"record_replaced": true},
	)
	lease, store := testGenerationApplicationLease(t, committedRecordForCompiled(t, before))
	store.replaceErrors = []error{nil, commitFailure}
	kernel := &fakeKernelGenerationApplication{
		observations: []kernelObservation{
			kernelObservationFromCompiled(before),
			kernelObservationFromCompiled(target),
		},
		results:     []kernelApplicationResult{{commitAttempted: true}},
		applyErrors: []error{flushFailure},
	}

	err := lease.applyCompiledGeneration(t.Context(), kernel, before, target)
	require.ErrorIs(t, err, flushFailure)
	require.ErrorIs(t, err, commitFailure)
	assert.False(t, generationApplicationOutcomeUnknown(err))
	assert.Equal(t, string(generationApplicationKernelTarget), generationApplicationKernelStateDetail(err))
	got, admittedErr := lease.admittedRecord()
	require.NoError(t, admittedErr)
	assert.Equal(t, generationRecordApplying, got.lifecycle)
	require.NotNil(t, got.before)
	require.NotNil(t, got.target)
	assert.Equal(t, generationRecordIdentityFromCompiled(before), *got.before)
	assert.Equal(t, generationRecordIdentityFromCompiled(target), *got.target)

	blockedKernel := &fakeKernelGenerationApplication{}
	blockedErr := lease.applyCompiledGeneration(t.Context(), blockedKernel, before, target)
	require.Error(t, blockedErr)
	assert.ErrorContains(t, blockedErr, "ambiguous")
	assert.Empty(t, blockedKernel.events)
}

func TestGenerationApplicationDoesNotMutateAfterApplyingRecordWriteFailure(t *testing.T) {
	t.Parallel()

	before, target := testGenerationApplicationPair(t)
	writeFailure := errors.New("injected applying record write failure")
	lease, store := testGenerationApplicationLease(t, committedRecordForCompiled(t, before))
	store.replaceErrors = []error{writeFailure}
	kernel := &fakeKernelGenerationApplication{
		observations: []kernelObservation{kernelObservationFromCompiled(before)},
	}

	err := lease.applyCompiledGeneration(t.Context(), kernel, before, target)
	require.ErrorIs(t, err, writeFailure)
	assert.Empty(t, kernel.events)
	assert.Len(t, store.replacements, 1)
	got, admittedErr := lease.admittedRecord()
	require.NoError(t, admittedErr)
	assert.Equal(t, generationRecordCommitted, got.lifecycle)
}

// Rationale: Copies of a retained lease share one concrete application authority. A second transaction cannot
// observe or mutate between the first transaction's applying record, Flush, reconciliation, and committed record.
func TestGenerationApplicationSerializesTransactionsAcrossCopiedLeaseValues(t *testing.T) {
	t.Parallel()

	before, target := testGenerationApplicationPair(t)
	lease, store := testGenerationApplicationLease(t, committedRecordForCompiled(t, before))
	copiedLease := *lease
	firstApplyStarted := make(chan struct{})
	firstApplyRelease := make(chan struct{})
	firstKernel := &fakeKernelGenerationApplication{
		observations: []kernelObservation{
			kernelObservationFromCompiled(before),
			kernelObservationFromCompiled(target),
		},
		results: []kernelApplicationResult{{commitAttempted: true}},
		applyHook: func() {
			close(firstApplyStarted)
			<-firstApplyRelease
		},
	}
	firstResult := make(chan error, 1)
	go func() {
		firstResult <- lease.applyCompiledGeneration(t.Context(), firstKernel, before, target)
	}()
	<-firstApplyStarted

	secondStarted := make(chan struct{})
	secondKernel := &fakeKernelGenerationApplication{}
	secondResult := make(chan error, 1)
	go func() {
		close(secondStarted)
		secondResult <- copiedLease.applyCompiledGeneration(t.Context(), secondKernel, before, target)
	}()
	<-secondStarted
	if lease.state.applicationMu.TryLock() {
		lease.state.applicationMu.Unlock()
		close(firstApplyRelease)
		<-firstResult
		<-secondResult
		t.Fatal("persistent application serialization was not held across the outer transaction")
	}
	close(firstApplyRelease)

	require.NoError(t, <-firstResult)
	require.Error(t, <-secondResult)
	assert.Empty(t, secondKernel.events)
	require.Len(t, store.replacements, 2)
	assert.Equal(t, generationRecordApplying, store.replacements[0].lifecycle)
	assert.Equal(t, generationRecordCommitted, store.replacements[1].lifecycle)
	assert.Equal(t, generationRecordIdentityFromCompiled(target), store.replacements[1].committed)
}

// Rationale: Standalone durable-record replacement uses the same mutex as the whole application transaction. It may
// run after reconciliation, but never between the admitted applying record and its exact committed outcome.
func TestGenerationApplicationSerializesStandaloneReplacementAfterReconciliation(t *testing.T) {
	before, target := testGenerationApplicationPair(t)
	lease, store := testGenerationApplicationLease(t, committedRecordForCompiled(t, before))
	copiedLease := *lease
	applyStarted := make(chan struct{})
	applyRelease := make(chan struct{})
	kernel := &fakeKernelGenerationApplication{
		observations: []kernelObservation{
			kernelObservationFromCompiled(before),
			kernelObservationFromCompiled(target),
		},
		results: []kernelApplicationResult{{commitAttempted: true}},
		applyHook: func() {
			close(applyStarted)
			<-applyRelease
		},
	}
	applicationResult := make(chan error, 1)
	go func() {
		applicationResult <- lease.applyCompiledGeneration(t.Context(), kernel, before, target)
	}()
	<-applyStarted

	replacementResult := make(chan error, 1)
	replacement := committedRecordForCompiled(t, before)
	go func() {
		replacementResult <- copiedLease.replaceAdmittedRecord(t.Context(), replacement)
	}()
	completed, blockErr := waitForGenerationApplicationMutexBlock(
		t,
		replacementResult,
		"(*persistentGenerationLease).replaceAdmittedRecord",
	)
	if blockErr != nil {
		close(applyRelease)
		<-applicationResult
		if !completed {
			<-replacementResult
		}
		t.Fatal(blockErr)
	}
	close(applyRelease)

	require.NoError(t, <-applicationResult)
	require.NoError(t, <-replacementResult)
	require.Len(t, store.replacements, 3)
	assert.Equal(t, generationRecordApplying, store.replacements[0].lifecycle)
	assert.Equal(t, generationRecordIdentityFromCompiled(target), store.replacements[1].committed)
	assert.Equal(t, generationRecordIdentityFromCompiled(before), store.replacements[2].committed)
}

// Rationale: Release shares the application mutex across copied lease values. It closes retained resources only after
// exact post-Flush reconciliation and the final committed record have completed.
func TestGenerationApplicationSerializesReleaseAfterReconciliation(t *testing.T) {
	before, target := testGenerationApplicationPair(t)
	lease, store := testGenerationApplicationLease(t, committedRecordForCompiled(t, before))
	copiedLease := *lease
	global := lease.state.global.(*fakeFirewallGlobalAuthority)
	var timelineMu sync.Mutex
	timeline := make([]string, 0, 8)
	recordEvent := func(event string) {
		timelineMu.Lock()
		defer timelineMu.Unlock()
		timeline = append(timeline, event)
	}
	store.recordEvent = recordEvent
	global.events = recordEvent
	global.lock.events = recordEvent
	applyStarted := make(chan struct{})
	applyRelease := make(chan struct{})
	kernel := &fakeKernelGenerationApplication{
		observations: []kernelObservation{
			kernelObservationFromCompiled(before),
			kernelObservationFromCompiled(target),
		},
		results:     []kernelApplicationResult{{commitAttempted: true}},
		recordEvent: recordEvent,
		applyHook: func() {
			close(applyStarted)
			<-applyRelease
		},
	}
	applicationResult := make(chan error, 1)
	go func() {
		applicationResult <- lease.applyCompiledGeneration(t.Context(), kernel, before, target)
	}()
	<-applyStarted

	releaseResult := make(chan error, 1)
	go func() {
		releaseResult <- copiedLease.Release(t.Context())
	}()
	completed, blockErr := waitForGenerationApplicationMutexBlock(
		t,
		releaseResult,
		"(*persistentGenerationLease).Release",
	)
	if blockErr != nil {
		close(applyRelease)
		<-applicationResult
		if !completed {
			<-releaseResult
		}
		t.Fatal(blockErr)
	}
	close(applyRelease)

	require.NoError(t, <-applicationResult)
	require.NoError(t, <-releaseResult)
	if diff := cmp.Diff([]string{
		"record applying",
		"observe",
		"apply",
		"observe",
		"record committed",
		"release record store",
		"release firewall lock",
		"release firewall authority",
	}, timeline); diff != "" {
		t.Errorf("application and release order mismatch (-want +got):\n%s", diff)
	}
	_, err := lease.admittedRecord()
	require.Error(t, err)
}

// waitForGenerationApplicationMutexBlock positively observes the named competing method stopped beneath
// sync.Mutex while the first transaction holds applicationMu. A sleep or immediate default-select could run before
// the competitor and mistake scheduler timing for serialization, so these two non-parallel tests inspect the bounded
// goroutine snapshot instead of inferring that the method started.
func waitForGenerationApplicationMutexBlock(
	t *testing.T,
	completed <-chan error,
	method string,
) (bool, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	stack := make([]byte, 1<<20)
	for {
		select {
		case err := <-completed:
			return true, fmt.Errorf("%s completed while the first application transaction was held: %v", method, err)
		case <-ctx.Done():
			return false, fmt.Errorf("observe %s blocked at application mutex: %w", method, ctx.Err())
		default:
		}
		written := runtime.Stack(stack, true)
		for _, goroutine := range strings.Split(string(stack[:written]), "\n\n") {
			if strings.Contains(goroutine, method) && strings.Contains(goroutine, "[sync.Mutex.Lock]") {
				return false, nil
			}
		}
		runtime.Gosched()
	}
}

func testGenerationApplicationPair(t *testing.T) (compiledGeneration, compiledGeneration) {
	t.Helper()
	before, err := compileGeneration(generationIntent{})
	require.NoError(t, err)
	target, err := compileGeneration(compatibilityIntent())
	require.NoError(t, err)
	return before, target
}

func committedRecordForCompiled(t *testing.T, generation compiledGeneration) generationRecord {
	t.Helper()
	record, err := newCommittedGenerationRecord(
		testGenerationRecordBinding(),
		generationRecordIdentityFromCompiled(generation),
	)
	require.NoError(t, err)
	return record
}

func applyingRecordForCompiled(
	t *testing.T,
	before compiledGeneration,
	target compiledGeneration,
) generationRecord {
	t.Helper()
	record, err := beginGenerationRecordApplication(
		committedRecordForCompiled(t, before),
		generationRecordIdentityFromCompiled(before),
		generationRecordIdentityFromCompiled(target),
	)
	require.NoError(t, err)
	return record
}

func testGenerationApplicationLease(
	t *testing.T,
	record generationRecord,
) (*persistentGenerationLease, *recordingGenerationApplicationStore) {
	t.Helper()
	store := &recordingGenerationApplicationStore{}
	global := &fakeFirewallGlobalAuthority{}
	return &persistentGenerationLease{state: &persistentGenerationLeaseState{
		global: global, lock: &global.lock, store: store, record: cloneGenerationRecord(record),
		evidence: generationRecordFileEvidence{device: 41, inode: 73, size: 101}, active: true,
	}}, store
}

type fakeKernelGenerationApplication struct {
	mu           sync.Mutex
	observations []kernelObservation
	observeErrs  []error
	results      []kernelApplicationResult
	applyErrors  []error
	events       []string
	recordEvent  func(string)
	applyHook    func()

	observationContextErrors []error
}

func (kernel *fakeKernelGenerationApplication) observeGeneration(ctx context.Context) (kernelObservation, error) {
	kernel.mu.Lock()
	defer kernel.mu.Unlock()
	kernel.appendEvent("observe")
	kernel.observationContextErrors = append(kernel.observationContextErrors, ctx.Err())
	var observed kernelObservation
	if len(kernel.observations) > 0 {
		observed = kernel.observations[0]
		kernel.observations = kernel.observations[1:]
	}
	var err error
	if len(kernel.observeErrs) > 0 {
		err = kernel.observeErrs[0]
		kernel.observeErrs = kernel.observeErrs[1:]
	}
	return observed, err
}

func (kernel *fakeKernelGenerationApplication) applyGenerationDuringApplication(
	context.Context,
	*persistentGenerationLease,
	kernelObservation,
	compiledGeneration,
	compiledGeneration,
) (kernelApplicationResult, error) {
	kernel.mu.Lock()
	defer kernel.mu.Unlock()
	kernel.appendEvent("apply")
	if kernel.applyHook != nil {
		kernel.applyHook()
	}
	var result kernelApplicationResult
	if len(kernel.results) > 0 {
		result = kernel.results[0]
		kernel.results = kernel.results[1:]
	}
	var err error
	if len(kernel.applyErrors) > 0 {
		err = kernel.applyErrors[0]
		kernel.applyErrors = kernel.applyErrors[1:]
	}
	return result, err
}

func (kernel *fakeKernelGenerationApplication) appendEvent(event string) {
	kernel.events = append(kernel.events, event)
	if kernel.recordEvent != nil {
		kernel.recordEvent(event)
	}
}

type recordingGenerationApplicationStore struct {
	mu            sync.Mutex
	replacements  []generationRecord
	replaceErrors []error
	nextInode     uint64
	recordEvent   func(string)
}

func (*recordingGenerationApplicationStore) read(
	context.Context,
) (generationRecord, generationRecordFileEvidence, bool, error) {
	return generationRecord{}, generationRecordFileEvidence{}, false, errors.New("unexpected record read")
}

func (store *recordingGenerationApplicationStore) replace(
	_ context.Context,
	_ generationRecord,
	_ generationRecordFileEvidence,
	replacement generationRecord,
) (generationRecordFileEvidence, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.replacements = append(store.replacements, cloneGenerationRecord(replacement))
	if store.recordEvent != nil {
		store.recordEvent("record " + string(replacement.lifecycle))
	}
	var err error
	if len(store.replaceErrors) > 0 {
		err = store.replaceErrors[0]
		store.replaceErrors = store.replaceErrors[1:]
	}
	if err != nil {
		return generationRecordFileEvidence{}, err
	}
	store.nextInode++
	return generationRecordFileEvidence{device: 41, inode: 73 + store.nextInode, size: 101}, nil
}

func (store *recordingGenerationApplicationStore) Release(context.Context) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.recordEvent != nil {
		store.recordEvent("release record store")
	}
	return nil
}

func generationApplicationOutcomeUnknown(err error) bool {
	domainErr := errs.AsDomainError(err)
	return domainErr != nil && domainErr.Details["outcome_unknown"] == true
}

func generationApplicationKernelStateDetail(err error) string {
	domainErr := errs.AsDomainError(err)
	if domainErr == nil {
		return ""
	}
	value, _ := domainErr.Details[generationApplicationKernelStateDetailKey].(string)
	return value
}

var _ generationKernelApplication = (*fakeKernelGenerationApplication)(nil)
