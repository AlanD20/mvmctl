package firewall

import (
	"context"
	"encoding/binary"
	"errors"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/nftables"
	"github.com/mdlayher/netlink"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

// Rationale: The collector, rather than its caller, must own the complete read-only nftables request grammar and must
// discard every per-request transport after checked close.
func TestCollectKernelObservationSnapshotRequestsExactManagedState(t *testing.T) {
	raw, want := capturedKernelObservationFixture(t)
	factory := &fakeKernelSnapshotTransportFactory{steps: successfulKernelSnapshotSteps(t, raw)}

	got, err := collectKernelObservationSnapshot(t.Context(), factory)
	require.NoError(t, err)
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(
		kernelObservation{}, kernelObservedTable{}, kernelObservedChain{}, kernelObservedRule{}, tableIdentity{},
	)); diff != "" {
		t.Fatalf("kernel observation mismatch (-want +got):\n%s", diff)
	}

	wantExchanges := exactKernelSnapshotExchanges()
	if diff := cmp.Diff(wantExchanges, factory.exchanges, cmp.AllowUnexported(
		kernelObservationSnapshotExchange{}, kernelObservationReplySpec{},
	)); diff != "" {
		t.Fatalf("kernel snapshot exchanges mismatch (-want +got):\n%s", diff)
	}
	assertFreshCheckedClose(t, factory, len(wantExchanges))
}

// Rationale: The collector's reply contract must survive the real raw-transport boundary. In particular, Linux marks
// rule dump objects MULTI|APPEND even though the terminating DONE remains only MULTI.
func TestCollectKernelObservationSnapshotUsesRealTransportReplyContracts(t *testing.T) {
	raw, want := capturedKernelObservationFixture(t)
	steps := successfulKernelSnapshotSteps(t, raw)
	factory := &realKernelSnapshotTransportFactory{
		t:     t,
		steps: steps,
		specs: exactKernelSnapshotExchanges(),
	}

	got, err := collectKernelObservationSnapshot(t.Context(), factory)
	require.NoError(t, err)
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(
		kernelObservation{}, kernelObservedTable{}, kernelObservedChain{}, kernelObservedRule{}, tableIdentity{},
	)); diff != "" {
		t.Fatalf("kernel observation mismatch through real transport (-want +got):\n%s", diff)
	}
	require.Len(t, factory.connections, len(steps))
	for _, connection := range factory.connections {
		assert.Equal(t, 1, connection.closeCalls)
	}
}

// Rationale: Absence is authoritative only when both exact table lookups return ENOENT within the same stable
// generation; no child-object query is then necessary.
func TestCollectKernelObservationSnapshotAcceptsStableEmptyRuleset(t *testing.T) {
	factory := &fakeKernelSnapshotTransportFactory{steps: []fakeKernelSnapshotTransportStep{
		{replies: []netlink.Message{testKernelGenerationReply(t, uint32(testNFTBaseSequence))}},
		{err: testKernelSnapshotRejectedError(unix.ENOENT)},
		{err: testKernelSnapshotRejectedError(unix.ENOENT)},
		{replies: []netlink.Message{testKernelGenerationReply(t, uint32(testNFTBaseSequence))}},
	}}

	got, err := collectKernelObservationSnapshot(t.Context(), factory)
	require.NoError(t, err)
	if diff := cmp.Diff(kernelObservation{empty: true}, got, cmp.AllowUnexported(kernelObservation{})); diff != "" {
		t.Fatalf("empty kernel observation mismatch (-want +got):\n%s", diff)
	}

	want := exactKernelSnapshotExchanges()[:3]
	want = append(slices.Clone(want), exactKernelSnapshotExchanges()[len(exactKernelSnapshotExchanges())-1])
	if diff := cmp.Diff(want, factory.exchanges, cmp.AllowUnexported(
		kernelObservationSnapshotExchange{}, kernelObservationReplySpec{},
	)); diff != "" {
		t.Fatalf("empty snapshot exchanges mismatch (-want +got):\n%s", diff)
	}
	assertFreshCheckedClose(t, factory, len(want))
}

// Rationale: One present managed table and one absent managed table is partial state, never an empty snapshot.
func TestCollectKernelObservationSnapshotRejectsPartialManagedTables(t *testing.T) {
	raw, _ := capturedKernelObservationFixture(t)
	tests := map[string][]fakeKernelSnapshotTransportStep{
		"bridge present": {
			{replies: []netlink.Message{testKernelGenerationReply(t, uint32(testNFTBaseSequence))}},
			{replies: []netlink.Message{raw.tableReplies[0]}},
			{err: testKernelSnapshotRejectedError(unix.ENOENT)},
			{replies: []netlink.Message{testKernelGenerationReply(t, uint32(testNFTBaseSequence))}},
		},
		"inet present": {
			{replies: []netlink.Message{testKernelGenerationReply(t, uint32(testNFTBaseSequence))}},
			{err: testKernelSnapshotRejectedError(unix.ENOENT)},
			{replies: []netlink.Message{raw.tableReplies[1]}},
			{replies: []netlink.Message{testKernelGenerationReply(t, uint32(testNFTBaseSequence))}},
		},
	}
	for name, steps := range tests {
		t.Run(name, func(t *testing.T) {
			factory := &fakeKernelSnapshotTransportFactory{steps: steps}
			_, err := collectKernelObservationSnapshot(t.Context(), factory)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "managed table observation is partial")
			assertFreshCheckedClose(t, factory, 4)
		})
	}
}

// Rationale: ENOENT means managed-state absence only for the exact table lookup and only when the hardened transport
// classified it as a kernel rejection without a cleanup failure.
func TestCollectKernelObservationSnapshotClassifiesOnlyExactTableENOENT(t *testing.T) {
	raw, _ := capturedKernelObservationFixture(t)
	closeErr := errors.New("close failed")
	tests := map[string]struct {
		steps []fakeKernelSnapshotTransportStep
		want  error
	}{
		"exact chain lookup": {
			steps: append(slices.Clone(successfulKernelSnapshotSteps(t, raw)[:3]),
				fakeKernelSnapshotTransportStep{err: testKernelSnapshotRejectedError(unix.ENOENT)}),
			want: unix.ENOENT,
		},
		"unclassified table receive failure": {
			steps: []fakeKernelSnapshotTransportStep{
				{replies: []netlink.Message{testKernelGenerationReply(t, uint32(testNFTBaseSequence))}},
				{err: generationAuthorityError("receive kernel observation reply", unix.ENOENT, nil)},
			},
			want: unix.ENOENT,
		},
		"unproven table rejection": {
			steps: []fakeKernelSnapshotTransportStep{
				{replies: []netlink.Message{testKernelGenerationReply(t, uint32(testNFTBaseSequence))}},
				{err: generationAuthorityError(kernelObservationRejectedRequestMessage, unix.ENOENT, nil)},
			},
			want: unix.ENOENT,
		},
		"exact table lookup with failed close": {
			steps: []fakeKernelSnapshotTransportStep{
				{replies: []netlink.Message{testKernelGenerationReply(t, uint32(testNFTBaseSequence))}},
				{err: testKernelSnapshotRejectedError(unix.ENOENT), closeErr: closeErr},
			},
			want: closeErr,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			factory := &fakeKernelSnapshotTransportFactory{steps: tt.steps}
			_, err := collectKernelObservationSnapshot(t.Context(), factory)
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.want)
			assertFreshCheckedClose(t, factory, len(tt.steps))
		})
	}
}

// Rationale: Every dependency failure remains classified, and even a transport returned alongside an acquisition
// failure must be checked closed before the failure escapes.
func TestCollectKernelObservationSnapshotPreservesTransportFailures(t *testing.T) {
	requestErr := generationAuthorityError("kernel query failed", unix.EIO, nil)
	openErr := generationAuthorityError("open failed", unix.EPERM, nil)
	tests := map[string]struct {
		steps       []fakeKernelSnapshotTransportStep
		want        error
		wantCreated int
	}{
		"exchange": {
			steps: []fakeKernelSnapshotTransportStep{
				{replies: []netlink.Message{testKernelGenerationReply(t, uint32(testNFTBaseSequence))}},
				{err: requestErr},
			},
			want:        requestErr,
			wantCreated: 2,
		},
		"open": {
			steps:       []fakeKernelSnapshotTransportStep{{openErr: openErr}},
			want:        openErr,
			wantCreated: 0,
		},
		"open after allocation": {
			steps:       []fakeKernelSnapshotTransportStep{{openErr: openErr, returnTransportOnOpenError: true}},
			want:        openErr,
			wantCreated: 1,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			factory := &fakeKernelSnapshotTransportFactory{steps: tt.steps}
			_, err := collectKernelObservationSnapshot(t.Context(), factory)
			require.Error(t, err)
			assert.Same(t, tt.want, errs.AsDomainError(err))
			assert.Len(t, factory.transports, tt.wantCreated)
			for _, transport := range factory.transports {
				assert.Equal(t, 1, transport.closeCalls)
			}
		})
	}
}

// Rationale: Equal pre/post generation IDs are insufficient if any object reply carries different per-reply
// base-sequence evidence, and unequal full generation IDs always indicate drift.
func TestCollectKernelObservationSnapshotRejectsGenerationDrift(t *testing.T) {
	raw, _ := capturedKernelObservationFixture(t)
	tests := map[string][]fakeKernelSnapshotTransportStep{
		"pre and post generation differ": func() []fakeKernelSnapshotTransportStep {
			steps := successfulKernelSnapshotSteps(t, raw)
			steps[len(steps)-1].replies[0] = testKernelGenerationReply(t, uint32(testNFTBaseSequence)+1)
			return steps
		}(),
		"empty ruleset generation differs": {
			{replies: []netlink.Message{testKernelGenerationReply(t, uint32(testNFTBaseSequence))}},
			{err: testKernelSnapshotRejectedError(unix.ENOENT)},
			{err: testKernelSnapshotRejectedError(unix.ENOENT)},
			{replies: []netlink.Message{testKernelGenerationReply(t, uint32(testNFTBaseSequence)+1)}},
		},
		"partial managed tables generation differs": {
			{replies: []netlink.Message{testKernelGenerationReply(t, uint32(testNFTBaseSequence))}},
			{replies: []netlink.Message{raw.tableReplies[0]}},
			{err: testKernelSnapshotRejectedError(unix.ENOENT)},
			{replies: []netlink.Message{testKernelGenerationReply(t, uint32(testNFTBaseSequence)+1)}},
		},
		"object base sequence differs": func() []fakeKernelSnapshotTransportStep {
			steps := successfulKernelSnapshotSteps(t, raw)
			message := cloneKernelMessages(steps[3].replies)[0]
			binary.BigEndian.PutUint16(message.Data[2:4], testNFTBaseSequence+1)
			steps[3].replies[0] = message
			return steps
		}(),
	}
	for name, steps := range tests {
		t.Run(name, func(t *testing.T) {
			factory := &fakeKernelSnapshotTransportFactory{steps: steps}
			_, err := collectKernelObservationSnapshot(t.Context(), factory)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "generation")
			assertFreshCheckedClose(t, factory, len(steps))
		})
	}
}

// Rationale: Sets, stateful objects, and flowtables are never part of a managed generation. The first returned object
// must terminate collection before any later dump or generation query can dilute that evidence.
func TestCollectKernelObservationSnapshotRejectsForbiddenObjectsImmediately(t *testing.T) {
	raw, _ := capturedKernelObservationFixture(t)
	tests := map[string]struct {
		stepIndex   int
		messageType uint16
		flags       netlink.HeaderFlags
	}{
		"set":       {stepIndex: 10, messageType: unix.NFT_MSG_NEWSET, flags: netlink.Multi},
		"object":    {stepIndex: 12, messageType: unix.NFT_MSG_NEWOBJ, flags: netlink.Multi | netlink.Append},
		"flowtable": {stepIndex: 14, messageType: unix.NFT_MSG_NEWFLOWTABLE, flags: netlink.Multi | netlink.Append},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			steps := successfulKernelSnapshotSteps(t, raw)[:tc.stepIndex+1]
			steps[tc.stepIndex].replies = []netlink.Message{
				testKernelMessage(t, tc.messageType, nftables.TableFamilyBridge, tc.flags, nil),
			}
			factory := &realKernelSnapshotTransportFactory{
				t:     t,
				steps: steps,
				specs: exactKernelSnapshotExchanges()[:tc.stepIndex+1],
			}

			_, err := collectKernelObservationSnapshot(t.Context(), factory)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "managed tables contain a set, object, or flowtable")
			require.Len(t, factory.connections, tc.stepIndex+1)
			for _, connection := range factory.connections {
				assert.Equal(t, 1, connection.closeCalls)
			}
		})
	}
}

// Rationale: Every raw reply in one logical snapshot consumes one aggregate budget even though each request owns a
// fresh connection. Reopening a transport must not reset either the message or byte limit.
func TestCollectKernelObservationSnapshotSharesRealTransportBudget(t *testing.T) {
	raw, _ := capturedKernelObservationFixture(t)
	generationBytes := int(testKernelGenerationReply(t, uint32(testNFTBaseSequence)).Header.Length)
	tests := map[string]struct {
		limits kernelObservationTransportLimits
		want   string
	}{
		"message count": {
			limits: kernelObservationTransportLimits{
				messageBytes: maxKernelObservationMessageBytes,
				messages:     1,
				totalBytes:   maxKernelObservationBytes,
			},
			want: "reply count exceeds limit",
		},
		"aggregate bytes": {
			limits: kernelObservationTransportLimits{
				messageBytes: maxKernelObservationMessageBytes,
				messages:     maxKernelObservationMessages,
				totalBytes:   generationBytes,
			},
			want: "reply bytes exceed limit",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			factory := &realKernelSnapshotTransportFactory{
				t:      t,
				steps:  successfulKernelSnapshotSteps(t, raw),
				specs:  exactKernelSnapshotExchanges(),
				limits: tc.limits,
			}

			_, err := collectKernelObservationSnapshot(t.Context(), factory)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			require.Len(t, factory.connections, 2)
			for _, connection := range factory.connections {
				assert.Equal(t, 1, connection.closeCalls)
			}
		})
	}
}

// Rationale: The strict generation and object decoders must reject foreign, missing, and unknown evidence rather than
// silently treating it as managed state.
func TestCollectKernelObservationSnapshotRejectsForeignOrMalformedReplies(t *testing.T) {
	raw, _ := capturedKernelObservationFixture(t)
	tests := map[string][]fakeKernelSnapshotTransportStep{
		"unknown generation attribute": func() []fakeKernelSnapshotTransportStep {
			generation := testKernelGenerationReply(t, uint32(testNFTBaseSequence))
			generation.Data = append(generation.Data,
				mustMarshalKernelAttributes(t, []netlink.Attribute{{Type: 99}})...)
			generation.Header.Length = uint32(netlinkHeaderBytes + len(generation.Data))
			return []fakeKernelSnapshotTransportStep{{replies: []netlink.Message{generation}}}
		}(),
		"missing generation reply": {{replies: nil}},
		"foreign exact table reply": func() []fakeKernelSnapshotTransportStep {
			foreign := cloneKernelMessages(raw.tableReplies[:1])[0]
			foreign.Data[0] = byte(nftables.TableFamilyINet)
			return []fakeKernelSnapshotTransportStep{
				{replies: []netlink.Message{testKernelGenerationReply(t, uint32(testNFTBaseSequence))}},
				{replies: []netlink.Message{foreign}},
				{replies: []netlink.Message{raw.tableReplies[1]}},
				{replies: []netlink.Message{raw.chainReplies[0]}},
				{replies: []netlink.Message{raw.chainReplies[1]}},
				{replies: []netlink.Message{raw.chainReplies[2]}},
				{replies: []netlink.Message{raw.chainReplies[3]}},
				{replies: []netlink.Message{raw.chainReplies[4]}},
				{}, {}, {}, {}, {}, {}, {}, {},
				{replies: []netlink.Message{testKernelGenerationReply(t, uint32(testNFTBaseSequence))}},
			}
		}(),
	}
	for name, steps := range tests {
		t.Run(name, func(t *testing.T) {
			factory := &fakeKernelSnapshotTransportFactory{steps: steps}
			_, err := collectKernelObservationSnapshot(t.Context(), factory)
			require.Error(t, err)
			assertFreshCheckedClose(t, factory, len(steps))
		})
	}
}

// Rationale: Ruleset-generation sampling is the transaction fence. Every field in the pinned NEWGEN grammar must be
// validated before its value can classify an otherwise plausible snapshot as stable.
func TestDecodeKernelRulesetGenerationRejectsMalformedReplies(t *testing.T) {
	tests := map[string]struct {
		mutate func(*netlink.Message)
		want   string
	}{
		"response family": {
			mutate: func(message *netlink.Message) { message.Data[0] = byte(nftables.TableFamilyBridge) },
			want:   "family is invalid",
		},
		"netfilter version": {
			mutate: func(message *netlink.Message) { message.Data[1] = 1 },
			want:   "netfilter header is invalid",
		},
		"response flags": {
			mutate: func(message *netlink.Message) { message.Header.Flags = netlink.Multi },
			want:   "type or flags are invalid",
		},
		"generation width": {
			mutate: func(message *netlink.Message) {
				setMessageAttributeData(t, message, unix.NFTA_GEN_ID, []byte{1, 2, 3})
			},
			want: "generation ID is not a uint32",
		},
		"process ID width": {
			mutate: func(message *netlink.Message) {
				setMessageAttributeData(t, message, unix.NFTA_GEN_PROC_PID, []byte{1, 2, 3})
			},
			want: "process ID is not a uint32",
		},
		"zero process ID": {
			mutate: func(message *netlink.Message) {
				setMessageAttributeData(t, message, unix.NFTA_GEN_PROC_PID, testBigEndianUint32(0))
			},
			want: "process identity is invalid",
		},
		"unterminated process name": {
			mutate: func(message *netlink.Message) {
				setMessageAttributeData(t, message, unix.NFTA_GEN_PROC_NAME, []byte("nft"))
			},
			want: "not an exact NUL-terminated string",
		},
		"oversized process name": {
			mutate: func(message *netlink.Message) {
				setMessageAttributeData(t, message, unix.NFTA_GEN_PROC_NAME, []byte("0123456789abcdef\x00"))
			},
			want: "process identity is invalid",
		},
		"base sequence": {
			mutate: func(message *netlink.Message) {
				binary.BigEndian.PutUint16(message.Data[2:4], testNFTBaseSequence+1)
			},
			want: "base sequence is inconsistent",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			message := testKernelGenerationReply(t, uint32(testNFTBaseSequence))
			tc.mutate(&message)

			_, err := decodeKernelRulesetGeneration(message)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// Rationale: Cancellation is checked before each connection effect, while a cancellation after acquisition still
// closes the acquired transport with a cancellation-free cleanup context.
func TestCollectKernelObservationSnapshotCancellation(t *testing.T) {
	t.Run("after acquisition", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		factory := &fakeKernelSnapshotTransportFactory{steps: []fakeKernelSnapshotTransportStep{{
			beforeExchange: cancel,
			err:            generationAuthorityError("generation query canceled", context.Canceled, nil),
		}}}

		_, err := collectKernelObservationSnapshot(ctx, factory)
		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
		assertFreshCheckedClose(t, factory, 1)
		assert.NoError(t, factory.transports[0].closeContextErr)
	})

	t.Run("before acquisition", func(t *testing.T) {
		preCanceled, stop := context.WithCancel(t.Context())
		stop()
		untouched := &fakeKernelSnapshotTransportFactory{}
		_, err := collectKernelObservationSnapshot(preCanceled, untouched)
		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
		assert.Zero(t, untouched.openCalls)
	})
}

// Rationale: A checked-close failure is authoritative on success and diagnostic on failure; it may never hide or
// reclassify the primary DomainError.
func TestCollectKernelObservationSnapshotComposesCloseFailures(t *testing.T) {
	closeErr := errors.New("close failed")
	t.Run("after successful exchange", func(t *testing.T) {
		factory := &fakeKernelSnapshotTransportFactory{steps: []fakeKernelSnapshotTransportStep{{
			replies:  []netlink.Message{testKernelGenerationReply(t, uint32(testNFTBaseSequence))},
			closeErr: closeErr,
		}}}

		_, err := collectKernelObservationSnapshot(t.Context(), factory)
		require.Error(t, err)
		assert.ErrorIs(t, err, closeErr)
		domainErr := errs.AsDomainError(err)
		require.NotNil(t, domainErr)
		assert.Equal(t, true, domainErr.Details[generationCleanupFailedDetailKey])
		assertFreshCheckedClose(t, factory, 1)
	})

	t.Run("after failed exchange", func(t *testing.T) {
		primary := generationAuthorityError("generation failed", unix.EIO, nil)
		factory := &fakeKernelSnapshotTransportFactory{steps: []fakeKernelSnapshotTransportStep{{
			err:      primary,
			closeErr: closeErr,
		}}}
		_, err := collectKernelObservationSnapshot(t.Context(), factory)
		require.Error(t, err)
		assert.ErrorIs(t, err, primary)
		assert.ErrorIs(t, err, closeErr)
		domainErr := errs.AsDomainError(err)
		require.NotNil(t, domainErr)
		assert.Equal(t, primary.Code, domainErr.Code)
		assert.Equal(t, primary.Message, domainErr.Message)
		assert.Equal(t, true, domainErr.Details[generationCleanupFailedDetailKey])
		assertFreshCheckedClose(t, factory, 1)
	})

	t.Run("after acquisition failure with transport", func(t *testing.T) {
		primary := generationAuthorityError("open failed", unix.EPERM, nil)
		factory := &fakeKernelSnapshotTransportFactory{steps: []fakeKernelSnapshotTransportStep{{
			openErr:                    primary,
			returnTransportOnOpenError: true,
			closeErr:                   closeErr,
		}}}
		_, err := collectKernelObservationSnapshot(t.Context(), factory)
		require.Error(t, err)
		assert.ErrorIs(t, err, primary)
		assert.ErrorIs(t, err, closeErr)
		domainErr := errs.AsDomainError(err)
		require.NotNil(t, domainErr)
		assert.Equal(t, primary.Code, domainErr.Code)
		assert.Equal(t, primary.Message, domainErr.Message)
		assert.Equal(t, true, domainErr.Details[generationCleanupFailedDetailKey])
		require.Len(t, factory.transports, 1)
		assert.Zero(t, factory.transports[0].exchangeCalls)
		assert.Equal(t, 1, factory.transports[0].closeCalls)
		assert.NoError(t, factory.transports[0].closeContextErr)
	})
}

type kernelObservationSnapshotExchange struct {
	request netlink.Message
	spec    kernelObservationReplySpec
}

type fakeKernelSnapshotTransportStep struct {
	replies                    []netlink.Message
	err                        error
	closeErr                   error
	openErr                    error
	returnTransportOnOpenError bool
	beforeExchange             func()
}

type fakeKernelSnapshotTransportFactory struct {
	steps      []fakeKernelSnapshotTransportStep
	openCalls  int
	exchanges  []kernelObservationSnapshotExchange
	transports []*fakeKernelSnapshotTransport
}

func (factory *fakeKernelSnapshotTransportFactory) newTransport(
	context.Context,
	*kernelObservationReceiveBudget,
) (kernelObservationSnapshotTransport, error) {
	index := factory.openCalls
	factory.openCalls++
	if index >= len(factory.steps) {
		return nil, errors.New("unexpected kernel snapshot transport acquisition")
	}
	step := factory.steps[index]
	if step.openErr != nil && !step.returnTransportOnOpenError {
		return nil, step.openErr
	}
	transport := &fakeKernelSnapshotTransport{factory: factory, step: step}
	factory.transports = append(factory.transports, transport)
	return transport, step.openErr
}

type realKernelSnapshotTransportFactory struct {
	t           *testing.T
	steps       []fakeKernelSnapshotTransportStep
	specs       []kernelObservationSnapshotExchange
	limits      kernelObservationTransportLimits
	openCalls   int
	connections []*fakeKernelObservationConnection
}

func (factory *realKernelSnapshotTransportFactory) newTransport(
	_ context.Context,
	budget *kernelObservationReceiveBudget,
) (kernelObservationSnapshotTransport, error) {
	factory.t.Helper()
	index := factory.openCalls
	factory.openCalls++
	if index >= len(factory.steps) || index >= len(factory.specs) {
		return nil, errors.New("unexpected real kernel snapshot transport acquisition")
	}
	step := factory.steps[index]
	if step.err != nil || step.openErr != nil || step.closeErr != nil {
		return nil, errors.New("real kernel snapshot transport fixture contains an unsupported injected error")
	}
	messages := cloneKernelMessages(step.replies)
	for messageIndex := range messages {
		messages[messageIndex].Header.Sequence = testKernelTransportSequence
		messages[messageIndex].Header.PID = testKernelTransportPID
	}
	if factory.specs[index].spec.mode == kernelObservationReplyMultipart {
		messages = append(messages, testKernelTransportDone())
	}
	connection, recvmsg, _ := testKernelTransportConnection([]testKernelRecvmsgResult{{
		data: testKernelTransportDatagram(factory.t, messages...),
	}})
	factory.connections = append(factory.connections, connection)
	transport := newKernelObservationTransport(connection, recvmsg)
	transport.budget = budget
	if factory.limits != (kernelObservationTransportLimits{}) {
		transport.limits = factory.limits
	}
	return transport, nil
}

type fakeKernelSnapshotTransport struct {
	factory         *fakeKernelSnapshotTransportFactory
	step            fakeKernelSnapshotTransportStep
	exchangeCalls   int
	closeCalls      int
	closeContextErr error
}

func (transport *fakeKernelSnapshotTransport) exchange(
	_ context.Context,
	request netlink.Message,
	spec kernelObservationReplySpec,
) ([]netlink.Message, error) {
	transport.exchangeCalls++
	if transport.exchangeCalls != 1 {
		return nil, errors.New("kernel snapshot transport was reused")
	}
	transport.factory.exchanges = append(transport.factory.exchanges, kernelObservationSnapshotExchange{
		request: cloneKernelMessage(request),
		spec:    spec,
	})
	if transport.step.beforeExchange != nil {
		transport.step.beforeExchange()
	}
	return cloneKernelMessages(transport.step.replies), transport.step.err
}

func (transport *fakeKernelSnapshotTransport) closeConnection(ctx context.Context) error {
	transport.closeCalls++
	transport.closeContextErr = ctx.Err()
	return transport.step.closeErr
}

func successfulKernelSnapshotSteps(
	t *testing.T,
	raw kernelObservationMessages,
) []fakeKernelSnapshotTransportStep {
	t.Helper()
	steps := []fakeKernelSnapshotTransportStep{
		{replies: []netlink.Message{testKernelGenerationReply(t, uint32(testNFTBaseSequence))}},
		{replies: []netlink.Message{raw.tableReplies[0]}},
		{replies: []netlink.Message{raw.tableReplies[1]}},
	}
	for _, reply := range raw.chainReplies {
		steps = append(steps, fakeKernelSnapshotTransportStep{replies: []netlink.Message{reply}})
	}
	for _, family := range []nftables.TableFamily{nftables.TableFamilyBridge, nftables.TableFamilyINet} {
		var replies []netlink.Message
		for _, reply := range raw.ruleReplies {
			if nftables.TableFamily(reply.Data[0]) == family {
				replies = append(replies, reply)
			}
		}
		steps = append(steps, fakeKernelSnapshotTransportStep{replies: replies})
	}
	steps = append(steps,
		fakeKernelSnapshotTransportStep{}, fakeKernelSnapshotTransportStep{},
		fakeKernelSnapshotTransportStep{}, fakeKernelSnapshotTransportStep{},
		fakeKernelSnapshotTransportStep{}, fakeKernelSnapshotTransportStep{},
		fakeKernelSnapshotTransportStep{replies: []netlink.Message{
			testKernelGenerationReply(t, uint32(testNFTBaseSequence)),
		}},
	)
	return steps
}

func exactKernelSnapshotExchanges() []kernelObservationSnapshotExchange {
	singular := kernelObservationReplySpec{mode: kernelObservationReplySingular}
	result := []kernelObservationSnapshotExchange{{
		request: testExactKernelSnapshotRequest(unix.NFT_MSG_GETGEN, netlink.Request, 0, nil),
		spec:    singular,
	}}
	result = append(result,
		kernelObservationSnapshotExchange{
			request: testExactKernelSnapshotRequest(unix.NFT_MSG_GETTABLE, netlink.Request,
				nftables.TableFamilyBridge, testExactKernelTableAttribute(managedBridgeTableName, 1)),
			spec: singular,
		},
		kernelObservationSnapshotExchange{
			request: testExactKernelSnapshotRequest(unix.NFT_MSG_GETTABLE, netlink.Request,
				nftables.TableFamilyINet, testExactKernelTableAttribute(managedINetTableName, 1)),
			spec: singular,
		},
	)
	for _, identity := range []struct {
		family nftables.TableFamily
		table  string
		chain  string
	}{
		{nftables.TableFamilyBridge, managedBridgeTableName, managedBridgePreroutingChainName},
		{nftables.TableFamilyBridge, managedBridgeTableName, managedBridgeForwardChainName},
		{nftables.TableFamilyINet, managedINetTableName, managedINetForwardChainName},
		{nftables.TableFamilyINet, managedINetTableName, managedINetInputChainName},
		{nftables.TableFamilyINet, managedINetTableName, managedINetNATChainName},
	} {
		attributes := append(testExactKernelTableAttribute(identity.table, unix.NFTA_CHAIN_TABLE),
			testExactKernelTableAttribute(identity.chain, unix.NFTA_CHAIN_NAME)...)
		result = append(result, kernelObservationSnapshotExchange{
			request: testExactKernelSnapshotRequest(unix.NFT_MSG_GETCHAIN, netlink.Request,
				identity.family, attributes),
			spec: singular,
		})
	}
	for _, kind := range []struct {
		messageType uint16
		attribute   uint16
		replyFlags  netlink.HeaderFlags
	}{
		{unix.NFT_MSG_GETRULE, unix.NFTA_RULE_TABLE, netlink.Multi | netlink.Append},
		{unix.NFT_MSG_GETSET, unix.NFTA_SET_TABLE, netlink.Multi},
		{unix.NFT_MSG_GETOBJ, unix.NFTA_OBJ_TABLE, netlink.Multi | netlink.Append},
		{unix.NFT_MSG_GETFLOWTABLE, 1, netlink.Multi | netlink.Append},
	} {
		for _, identity := range []struct {
			family nftables.TableFamily
			name   string
		}{
			{nftables.TableFamilyBridge, managedBridgeTableName},
			{nftables.TableFamilyINet, managedINetTableName},
		} {
			result = append(result, kernelObservationSnapshotExchange{
				request: testExactKernelSnapshotRequest(kind.messageType, netlink.Request|netlink.Dump,
					identity.family, testExactKernelTableAttribute(identity.name, kind.attribute)),
				spec: kernelObservationReplySpec{
					flags: kind.replyFlags,
					mode:  kernelObservationReplyMultipart,
				},
			})
		}
	}
	return append(result, kernelObservationSnapshotExchange{
		request: testExactKernelSnapshotRequest(unix.NFT_MSG_GETGEN, netlink.Request, 0, nil),
		spec:    singular,
	})
}

func testExactKernelSnapshotRequest(
	messageType uint16,
	flags netlink.HeaderFlags,
	family nftables.TableFamily,
	attributes []byte,
) netlink.Message {
	return netlink.Message{
		Header: netlink.Header{
			Type:  netlink.HeaderType((unix.NFNL_SUBSYS_NFTABLES << 8) | messageType),
			Flags: flags,
		},
		Data: append([]byte{byte(family), 0, 0, 0}, attributes...),
	}
}

func testExactKernelTableAttribute(value string, attributeType uint16) []byte {
	length := 4 + len(value) + 1
	paddedLength := (length + 3) &^ 3
	result := make([]byte, paddedLength)
	binary.NativeEndian.PutUint16(result[0:2], uint16(length))
	binary.NativeEndian.PutUint16(result[2:4], attributeType)
	copy(result[4:], value)
	return result
}

func testKernelGenerationReply(t *testing.T, generationID uint32) netlink.Message {
	t.Helper()
	attributes := mustMarshalKernelAttributes(t, []netlink.Attribute{
		{Type: unix.NFTA_GEN_ID, Data: testBigEndianUint32(generationID)},
		{Type: unix.NFTA_GEN_PROC_PID, Data: testBigEndianUint32(1)},
		{Type: unix.NFTA_GEN_PROC_NAME, Data: []byte("nft\x00")},
	})
	data := []byte{byte(nftables.TableFamilyUnspecified), 0, 0, 0}
	binary.BigEndian.PutUint16(data[2:4], uint16(generationID))
	data = append(data, attributes...)
	return netlink.Message{
		Header: netlink.Header{
			Length:   uint32(netlinkHeaderBytes + len(data)),
			Type:     netlink.HeaderType(kernelNetfilterSubsystemType | unix.NFT_MSG_NEWGEN),
			Sequence: 1,
			PID:      1,
		},
		Data: data,
	}
}

func testKernelSnapshotRejectedError(errno error) error {
	return kernelObservationTransportError(kernelObservationRejectedRequestMessage, errno, true)
}

func cloneKernelMessage(message netlink.Message) netlink.Message {
	message.Data = slices.Clone(message.Data)
	return message
}

func assertFreshCheckedClose(t *testing.T, factory *fakeKernelSnapshotTransportFactory, count int) {
	t.Helper()
	assert.Equal(t, count, factory.openCalls)
	require.Len(t, factory.transports, count)
	for _, transport := range factory.transports {
		assert.Equal(t, 1, transport.exchangeCalls)
		assert.Equal(t, 1, transport.closeCalls)
		assert.NoError(t, transport.closeContextErr)
	}
}
