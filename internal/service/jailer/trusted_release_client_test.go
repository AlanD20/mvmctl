package jailer

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/pkg/errs"
)

func TestTrustedReleaseClientEncodesNamedRequestsAndTransfersPayloadOwnership(t *testing.T) {
	installRequest := trustedReleaseClientInstallRequest()
	installResult := trustedReleaseClientInstallResult(installRequest, TrustedReleaseInstalled)
	payload := &trustedReleaseClientPayload{reader: *bytes.NewReader([]byte("archive"))}
	exchange := &fakeTrustedReleaseExchange{}
	exchange.exchange = func(
		ctx context.Context,
		request trustedReleaseExchangeRequest,
	) trustedReleaseExchangeOutcome {
		assert.Same(t, t.Context(), ctx)
		assert.Equal(t, TrustedReleaseInstallAction, request.action)
		assert.Equal(t, uint64(7), request.payloadLength)
		assert.Same(t, payload, request.payload)
		decoded, err := privilegedwire.DecodeRequest[TrustedReleaseInstallRequest](
			context.Background(),
			bytes.NewReader(request.frame),
			TrustedReleaseInstallAction,
			128*1024*1024,
		)
		require.NoError(t, err)
		if diff := cmp.Diff(installRequest, decoded.Body); diff != "" {
			t.Errorf("install request mismatch (-want +got):\n%s", diff)
		}
		assert.Equal(t, uint64(7), decoded.PayloadLength)
		require.NoError(t, request.payload.Close())
		return trustedReleaseExchangeOutcome{
			response:    encodeTrustedReleaseClientSuccess(t, TrustedReleaseInstallAction, installResult),
			responseEOF: true,
			started:     true,
		}
	}
	client := newTrustedReleaseClient(exchange)

	got, err := client.Install(t.Context(), installRequest, payload, 7)

	require.NoError(t, err)
	if diff := cmp.Diff(installResult, got); diff != "" {
		t.Errorf("install result mismatch (-want +got):\n%s", diff)
	}
	assert.Equal(t, int32(1), payload.closeCount.Load())
	assert.Equal(t, 1, exchange.calls)

	removeRequest := TrustedReleaseRemoveRequest{Slot: installRequest.Slot}
	removeResult := TrustedReleaseRemoveResult{Removed: true}
	exchange.exchange = func(
		ctx context.Context,
		request trustedReleaseExchangeRequest,
	) trustedReleaseExchangeOutcome {
		assert.Same(t, t.Context(), ctx)
		assert.Equal(t, TrustedReleaseRemoveAction, request.action)
		assert.Zero(t, request.payloadLength)
		assert.Nil(t, request.payload)
		decoded, decodeErr := privilegedwire.DecodeRequest[TrustedReleaseRemoveRequest](
			context.Background(),
			bytes.NewReader(request.frame),
			TrustedReleaseRemoveAction,
			0,
		)
		require.NoError(t, decodeErr)
		if diff := cmp.Diff(removeRequest, decoded.Body); diff != "" {
			t.Errorf("remove request mismatch (-want +got):\n%s", diff)
		}
		return trustedReleaseExchangeOutcome{
			response:    encodeTrustedReleaseClientSuccess(t, TrustedReleaseRemoveAction, removeResult),
			responseEOF: true,
			started:     true,
		}
	}

	gotRemove, err := client.Remove(t.Context(), removeRequest)

	require.NoError(t, err)
	if diff := cmp.Diff(removeResult, gotRemove); diff != "" {
		t.Errorf("remove result mismatch (-want +got):\n%s", diff)
	}
	assert.Equal(t, 2, exchange.calls)
}

func TestTrustedReleaseClientRetainsRejectedPayloadOwnership(t *testing.T) {
	tests := map[string]struct {
		ctx           context.Context
		request       TrustedReleaseInstallRequest
		payload       io.ReadCloser
		payloadLength uint64
	}{
		"nil_context": {
			request:       trustedReleaseClientInstallRequest(),
			payload:       &trustedReleaseClientPayload{},
			payloadLength: 1,
		},
		"invalid_slot": {
			ctx: context.Background(),
			request: TrustedReleaseInstallRequest{
				Slot: ReleaseSlot{Version: "../1.16.1", Architecture: "x86_64"},
			},
			payload:       &trustedReleaseClientPayload{},
			payloadLength: 1,
		},
		"zero_with_payload": {
			ctx:           context.Background(),
			request:       trustedReleaseClientInstallRequest(),
			payload:       &trustedReleaseClientPayload{},
			payloadLength: 0,
		},
		"nonzero_without_payload": {
			ctx:           context.Background(),
			request:       trustedReleaseClientInstallRequest(),
			payloadLength: 1,
		},
		"oversized_payload": {
			ctx:           context.Background(),
			request:       trustedReleaseClientInstallRequest(),
			payload:       &trustedReleaseClientPayload{},
			payloadLength: 128*1024*1024 + 1,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			exchange := &fakeTrustedReleaseExchange{}
			client := newTrustedReleaseClient(exchange)

			result, err := client.Install(test.ctx, test.request, test.payload, test.payloadLength)

			assert.Error(t, err)
			assert.Zero(t, result)
			assert.Zero(t, exchange.calls)
			if payload, ok := test.payload.(*trustedReleaseClientPayload); ok {
				assert.Zero(t, payload.closeCount.Load())
			}
		})
	}
}

func TestTrustedReleaseClientUsesEOFQualifiedResponseAfterCallerCancellation(t *testing.T) {
	request := trustedReleaseClientInstallRequest()
	result := trustedReleaseClientInstallResult(request, TrustedReleaseUnchanged)
	ctx, cancel := context.WithCancel(context.Background())
	exchange := &fakeTrustedReleaseExchange{}
	exchange.exchange = func(context.Context, trustedReleaseExchangeRequest) trustedReleaseExchangeOutcome {
		cancel()
		return trustedReleaseExchangeOutcome{
			response:    encodeTrustedReleaseClientSuccess(t, TrustedReleaseInstallAction, result),
			responseEOF: true,
			started:     true,
			diagnostic: errs.New(
				errs.CodeProcessError,
				"late reap diagnostic",
				errs.WithClass(errs.ClassInternal),
			),
		}
	}
	client := newTrustedReleaseClient(exchange)

	got, err := client.Install(ctx, request, nil, 0)

	require.NoError(t, err)
	if diff := cmp.Diff(result, got); diff != "" {
		t.Errorf("install result mismatch (-want +got):\n%s", diff)
	}
}

func TestTrustedReleaseClientRemoteDomainErrorWinsTransportDiagnostics(t *testing.T) {
	request := TrustedReleaseRemoveRequest{Slot: trustedReleaseClientInstallRequest().Slot}
	remoteErr := errs.New(
		errs.CodeBinaryRemoveFailed,
		"trusted release was removed but durability is uncertain",
		errs.WithClass(errs.ClassInternal),
		errs.WithEntity("1.16.1/x86_64"),
		errs.WithDetails(map[string]any{
			"release_removed":      true,
			"durability_uncertain": true,
		}),
	)
	exchange := &fakeTrustedReleaseExchange{outcome: trustedReleaseExchangeOutcome{
		response:    encodeTrustedReleaseClientError(t, TrustedReleaseRemoveAction, remoteErr),
		responseEOF: true,
		started:     true,
		diagnostic: errs.New(
			errs.CodeProcessError,
			"upload EPIPE and non-zero exit",
			errs.WithClass(errs.ClassInternal),
		),
	}}
	client := newTrustedReleaseClient(exchange)

	result, err := client.Remove(t.Context(), request)

	assert.Zero(t, result)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, remoteErr.Code, domainErr.Code)
	assert.Equal(t, remoteErr.Class, domainErr.Class)
	assert.Equal(t, remoteErr.Message, domainErr.Message)
	assert.Equal(t, remoteErr.Entity, domainErr.Entity)
	assert.Equal(t, remoteErr.Details, domainErr.Details)
	assert.NotContains(t, domainErr.Error(), "upload EPIPE")
}

func TestTrustedReleaseClientRejectsInvalidSuccessSemantics(t *testing.T) {
	request := trustedReleaseClientInstallRequest()
	valid := trustedReleaseClientInstallResult(request, TrustedReleaseInstalled)
	tests := map[string]func(*TrustedReleaseInstallResult){
		"unknown_outcome": func(result *TrustedReleaseInstallResult) {
			result.Outcome = "partial"
		},
		"mismatched_slot": func(result *TrustedReleaseInstallResult) {
			result.Slot.Version = "1.15.0"
		},
		"uppercase_archive_hash": func(result *TrustedReleaseInstallResult) {
			result.ArchiveSHA256 = strings.Repeat("A", 64)
		},
		"invalid_firecracker_size": func(result *TrustedReleaseInstallResult) {
			result.Firecracker.SizeBytes = 0
		},
		"invalid_jailer_hash": func(result *TrustedReleaseInstallResult) {
			result.Jailer.SHA256 = strings.Repeat("z", 64)
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			invalid := valid
			mutate(&invalid)
			exchange := &fakeTrustedReleaseExchange{outcome: trustedReleaseExchangeOutcome{
				response:    encodeTrustedReleaseClientSuccess(t, TrustedReleaseInstallAction, invalid),
				responseEOF: true,
				started:     true,
			}}
			client := newTrustedReleaseClient(exchange)

			result, err := client.Install(t.Context(), request, nil, 0)

			assert.Zero(t, result)
			assertTrustedReleaseUnknownOutcome(t, err, true)
		})
	}
}

func TestTrustedReleaseClientClassifiesMissingOrMalformedResponseByStartState(t *testing.T) {
	tests := map[string]struct {
		outcome trustedReleaseExchangeOutcome
		started bool
	}{
		"pre_start_missing": {
			outcome: trustedReleaseExchangeOutcome{
				diagnostic: errs.New(errs.CodeProcessError, "sudo start failed"),
			},
		},
		"post_start_missing": {
			outcome: trustedReleaseExchangeOutcome{
				started:    true,
				diagnostic: errs.New(errs.CodeProcessError, "process exited"),
			},
			started: true,
		},
		"post_start_non_eof": {
			outcome: trustedReleaseExchangeOutcome{
				response: []byte("MVMRES01"),
				started:  true,
			},
			started: true,
		},
		"post_start_malformed": {
			outcome: trustedReleaseExchangeOutcome{
				response:    []byte("not-a-response"),
				responseEOF: true,
				started:     true,
			},
			started: true,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			client := newTrustedReleaseClient(&fakeTrustedReleaseExchange{outcome: test.outcome})

			result, err := client.Remove(t.Context(), TrustedReleaseRemoveRequest{
				Slot: trustedReleaseClientInstallRequest().Slot,
			})

			assert.Zero(t, result)
			assertTrustedReleaseUnknownOutcome(t, err, test.started)
		})
	}
}

func TestTrustedReleaseClientRejectsInactiveClientWithoutPanicking(t *testing.T) {
	var client *TrustedReleaseClient

	installResult, installErr := client.Install(
		t.Context(),
		trustedReleaseClientInstallRequest(),
		nil,
		0,
	)
	removeResult, removeErr := client.Remove(t.Context(), TrustedReleaseRemoveRequest{
		Slot: trustedReleaseClientInstallRequest().Slot,
	})

	assert.Zero(t, installResult)
	assert.Zero(t, removeResult)
	assert.Error(t, installErr)
	assert.Error(t, removeErr)
}

type fakeTrustedReleaseExchange struct {
	exchange func(context.Context, trustedReleaseExchangeRequest) trustedReleaseExchangeOutcome
	outcome  trustedReleaseExchangeOutcome
	calls    int
}

func (fake *fakeTrustedReleaseExchange) Exchange(
	ctx context.Context,
	request trustedReleaseExchangeRequest,
) trustedReleaseExchangeOutcome {
	fake.calls++
	if fake.exchange != nil {
		return fake.exchange(ctx, request)
	}
	if request.payload != nil {
		_ = request.payload.Close()
	}
	return fake.outcome
}

type trustedReleaseClientPayload struct {
	reader     bytes.Reader
	closeCount atomic.Int32
	closeErr   error
}

func (payload *trustedReleaseClientPayload) Read(target []byte) (int, error) {
	return payload.reader.Read(target)
}

func (payload *trustedReleaseClientPayload) Close() error {
	payload.closeCount.Add(1)
	return payload.closeErr
}

func encodeTrustedReleaseClientSuccess[T any](t *testing.T, action string, result T) []byte {
	t.Helper()
	var encoded bytes.Buffer
	require.NoError(t, privilegedwire.EncodeSuccess(context.Background(), &encoded, action, result))
	return encoded.Bytes()
}

func encodeTrustedReleaseClientError(t *testing.T, action string, operationErr error) []byte {
	t.Helper()
	var encoded bytes.Buffer
	require.NoError(t, privilegedwire.EncodeError(context.Background(), &encoded, action, operationErr))
	return encoded.Bytes()
}

func trustedReleaseClientInstallRequest() TrustedReleaseInstallRequest {
	return TrustedReleaseInstallRequest{
		Slot: ReleaseSlot{Version: "1.16.1", Architecture: "x86_64"},
	}
}

func trustedReleaseClientInstallResult(
	request TrustedReleaseInstallRequest,
	outcome TrustedReleaseInstallOutcome,
) TrustedReleaseInstallResult {
	return TrustedReleaseInstallResult{
		Outcome:       outcome,
		Slot:          request.Slot,
		ArchiveSHA256: strings.Repeat("a", 64),
		Firecracker: TrustedReleaseExecutableResult{
			SHA256: strings.Repeat("b", 64), SizeBytes: 120,
		},
		Jailer: TrustedReleaseExecutableResult{
			SHA256: strings.Repeat("c", 64), SizeBytes: 120,
		},
	}
}

func assertTrustedReleaseUnknownOutcome(t *testing.T, err error, started bool) {
	t.Helper()
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeProcessError, domainErr.Code)
	assert.Equal(t, errs.ClassInternal, domainErr.Class)
	assert.Equal(t, started, domainErr.Details["process_started"])
	assert.Equal(t, started, domainErr.Details["outcome_unknown"])
}
