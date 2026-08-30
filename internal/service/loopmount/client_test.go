package loopmount

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/lib/system"
	"mvmctl/pkg/errs"
)

// Rationale: Process start and authenticated socket EOF are independent evidence. Only their true/true combination
// can authorize a decoded result; every other combination must preserve whether a privileged effect could have begun.
func TestLoopMountClientRequiresStartedEOFCombination(t *testing.T) {
	request := validLoopMountProvisionRequest()
	result := validLoopMountProvisionResult(request)
	response := encodeLoopMountSuccess(t, VMProvisionAction, result)
	tests := map[string]struct {
		started     bool
		responseEOF bool
		wantSuccess bool
	}{
		"not started without EOF": {},
		"not started with EOF": {
			responseEOF: true,
		},
		"started without EOF": {
			started: true,
		},
		"started with EOF": {
			started:     true,
			responseEOF: true,
			wantSuccess: true,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			exchanger := &fakeLoopMountExchanger{outcome: &fakeLoopMountOutcome{
				response:    response,
				responseEOF: test.responseEOF,
				started:     test.started,
			}}
			client := newClientWithExchanger(exchanger)

			got, err := client.ProvisionVM(t.Context(), request)

			if test.wantSuccess {
				require.NoError(t, err)
				if diff := cmp.Diff(result, got); diff != "" {
					t.Errorf("ProvisionVM() mismatch (-want +got):\n%s", diff)
				}
				return
			}
			assertLoopMountTransportError(t, err, test.started, test.started)
		})
	}
}

// Rationale: EOF is transport authority independent of later caller cancellation and reap diagnostics. A complete
// response received from a started process remains authoritative after the original context is cancelled.
func TestLoopMountClientUsesEOFQualifiedResponseAfterCallerCancellation(t *testing.T) {
	request := validLoopMountProvisionRequest()
	result := validLoopMountProvisionResult(request)
	ctx, cancel := context.WithCancel(context.Background())
	exchanger := &fakeLoopMountExchanger{}
	exchanger.exchange = func(context.Context, *system.PrivilegedControlRequest) privilegedExchangeOutcome {
		cancel()
		return &fakeLoopMountOutcome{
			response:    encodeLoopMountSuccess(t, VMProvisionAction, result),
			responseEOF: true,
			started:     true,
			diagnostic: errs.New(
				errs.CodeProcessError,
				"late reap diagnostic",
				errs.WithClass(errs.ClassInternal),
			),
		}
	}
	client := newClientWithExchanger(exchanger)

	got, err := client.ProvisionVM(ctx, request)

	require.NoError(t, err)
	if diff := cmp.Diff(result, got); diff != "" {
		t.Errorf("ProvisionVM() mismatch (-want +got):\n%s", diff)
	}
}

func TestLoopMountClientNamedImageCapabilitiesDecodeTypedResults(t *testing.T) {
	inspectRequest := ImageInspectRequest{
		CacheLocator:       "/srv/mvm-cache",
		ImageID:            testImageID,
		ExpectedImageBytes: 256 << 20,
		Filesystem:         FilesystemExt4,
	}
	inspectResult := ImageInspectResult{ImageID: testImageID, OSType: "alpine"}
	inspectClient := newClientWithExchanger(&fakeLoopMountExchanger{outcome: &fakeLoopMountOutcome{
		response:    encodeLoopMountSuccess(t, ImageInspectAction, inspectResult),
		responseEOF: true,
		started:     true,
	}})

	gotInspect, err := inspectClient.InspectImage(t.Context(), inspectRequest)

	require.NoError(t, err)
	if diff := cmp.Diff(inspectResult, gotInspect); diff != "" {
		t.Errorf("InspectImage() mismatch (-want +got):\n%s", diff)
	}

	optimizeRequest := ImageOptimizeRequest{
		CacheLocator:       "/srv/mvm-cache",
		ImageID:            testImageID,
		ExpectedImageBytes: 256 << 20,
		Filesystem:         FilesystemExt4,
		Plan:               ImageOptimizePlan{Shrink: true},
	}
	optimizeResult := ImageOptimizeResult{
		ImageID:    testImageID,
		Filesystem: FilesystemExt4,
		ImageBytes: 192 << 20,
	}
	optimizeClient := newClientWithExchanger(&fakeLoopMountExchanger{outcome: &fakeLoopMountOutcome{
		response:    encodeLoopMountSuccess(t, ImageOptimizeAction, optimizeResult),
		responseEOF: true,
		started:     true,
	}})

	gotOptimize, err := optimizeClient.OptimizeImage(t.Context(), optimizeRequest)

	require.NoError(t, err)
	if diff := cmp.Diff(optimizeResult, gotOptimize); diff != "" {
		t.Errorf("OptimizeImage() mismatch (-want +got):\n%s", diff)
	}
}

// Rationale: A valid EOF-qualified remote DomainError is operation authority. Transport diagnostics must not replace
// its stable code, class, entity, message, or partial-state details.
func TestLoopMountClientRemoteDomainErrorWinsTransportDiagnostics(t *testing.T) {
	request := validLoopMountProvisionRequest()
	remoteErr := errs.New(
		errs.CodeLoopMountError,
		"rootfs was provisioned but cleanup remains",
		errs.WithClass(errs.ClassInternal),
		errs.WithEntity(testVMID),
		errs.WithDetails(map[string]any{
			"provisioned":     true,
			"cleanup_pending": true,
		}),
	)
	exchanger := &fakeLoopMountExchanger{outcome: &fakeLoopMountOutcome{
		response:    encodeLoopMountError(t, VMProvisionAction, remoteErr),
		responseEOF: true,
		started:     true,
		diagnostic: errs.New(
			errs.CodeProcessError,
			"non-zero exit diagnostic",
			errs.WithClass(errs.ClassInternal),
		),
	}}
	client := newClientWithExchanger(exchanger)

	_, err := client.ProvisionVM(t.Context(), request)

	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, remoteErr.Code, domainErr.Code)
	assert.Equal(t, remoteErr.Class, domainErr.Class)
	assert.Equal(t, remoteErr.Message, domainErr.Message)
	assert.Equal(t, remoteErr.Entity, domainErr.Entity)
	if diff := cmp.Diff(remoteErr.Details, domainErr.Details); diff != "" {
		t.Errorf("remote error details mismatch (-want +got):\n%s", diff)
	}
	assert.NotContains(t, domainErr.Error(), "non-zero exit diagnostic")
}

// Rationale: A syntactically complete response remains untrusted. Malformed frames, action mismatches, invalid results,
// and a missing transport outcome cannot be promoted to operation authority.
func TestLoopMountClientRejectsMalformedMismatchedAndInvalidResponses(t *testing.T) {
	request := validLoopMountProvisionRequest()
	invalidResult := validLoopMountProvisionResult(request)
	invalidResult.VMID = "ffffffffffffffffffffffffffffffff"
	tests := map[string]struct {
		outcome privilegedExchangeOutcome
		started bool
	}{
		"nil outcome": {},
		"malformed response": {
			outcome: &fakeLoopMountOutcome{
				response:    []byte("not-a-response"),
				responseEOF: true,
				started:     true,
			},
			started: true,
		},
		"mismatched action": {
			outcome: &fakeLoopMountOutcome{
				response: encodeLoopMountSuccess(
					t,
					ImageInspectAction,
					ImageInspectResult{ImageID: testImageID, OSType: "alpine"},
				),
				responseEOF: true,
				started:     true,
			},
			started: true,
		},
		"invalid result semantics": {
			outcome: &fakeLoopMountOutcome{
				response:    encodeLoopMountSuccess(t, VMProvisionAction, invalidResult),
				responseEOF: true,
				started:     true,
			},
			started: true,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			client := newClientWithExchanger(&fakeLoopMountExchanger{outcome: test.outcome})

			_, err := client.ProvisionVM(t.Context(), request)

			assertLoopMountTransportError(t, err, test.started, test.started)
		})
	}
}

// Rationale: Diagnostics are not operation authority, but they are required evidence when no authoritative response
// exists. Normalization must retain the original cause for logs and higher-level diagnosis.
func TestLoopMountClientRetainsDiagnosticsForNonAuthoritativeResponse(t *testing.T) {
	diagnosticCause := errors.New("sudo wait sentinel")
	diagnostic := errs.WrapMsg(
		errs.CodeProcessError,
		"privileged process wait failed",
		diagnosticCause,
		errs.WithClass(errs.ClassInternal),
	)
	client := newClientWithExchanger(&fakeLoopMountExchanger{outcome: &fakeLoopMountOutcome{
		started:    true,
		diagnostic: diagnostic,
	}})

	_, err := client.ProvisionVM(t.Context(), validLoopMountProvisionRequest())

	assertLoopMountTransportError(t, err, true, true)
	assert.ErrorIs(t, err, diagnosticCause)
}

// Rationale: Before process start, cancellation, inactive bindings, and internal framing-contract failures have known
// no-effect status. They must share one stable process error classification and retain their diagnostic cause.
func TestLoopMountClientNormalizesPreExchangeFailures(t *testing.T) {
	request := validLoopMountProvisionRequest()

	t.Run("nil client", func(t *testing.T) {
		var client *Client

		_, err := client.ProvisionVM(t.Context(), request)

		assertLoopMountTransportError(t, err, false, false)
	})

	t.Run("inactive client", func(t *testing.T) {
		client := &Client{}

		_, err := client.ProvisionVM(t.Context(), request)

		assertLoopMountTransportError(t, err, false, false)
	})

	t.Run("cancelled before exchange", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		exchanger := &fakeLoopMountExchanger{}
		client := newClientWithExchanger(exchanger)

		_, err := client.ProvisionVM(ctx, request)

		assertLoopMountTransportError(t, err, false, false)
		assert.ErrorIs(t, err, context.Canceled)
		assert.Zero(t, exchanger.calls)
	})

	t.Run("invalid internal action", func(t *testing.T) {
		exchanger := &fakeLoopMountExchanger{}
		client := newClientWithExchanger(exchanger)

		_, err := exchangeLoopMount(
			t.Context(),
			client,
			"",
			request,
			request.Validate,
			func(result VMProvisionResult) error { return result.Validate(request) },
		)

		assertLoopMountTransportError(t, err, false, false)
		assert.Zero(t, exchanger.calls)
	})

	t.Run("incomplete internal result contract", func(t *testing.T) {
		exchanger := &fakeLoopMountExchanger{}
		client := newClientWithExchanger(exchanger)

		_, err := exchangeLoopMount[VMProvisionRequest, VMProvisionResult](
			t.Context(),
			client,
			VMProvisionAction,
			request,
			request.Validate,
			nil,
		)

		assertLoopMountTransportError(t, err, false, false)
		assert.Zero(t, exchanger.calls)
	})
}

// Rationale: Request admission is a caller-visible validation contract, not a process transport failure. Invalid
// requests must never reach the privileged exchanger and must retain CodeValidationFailed.
func TestLoopMountClientRetainsRequestValidationErrors(t *testing.T) {
	request := validLoopMountProvisionRequest()
	request.VMID = "../../etc/shadow"
	var client *Client

	_, err := client.ProvisionVM(t.Context(), request)

	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeValidationFailed, domainErr.Code)
}

type fakeLoopMountOutcome struct {
	response    []byte
	responseEOF bool
	started     bool
	diagnostic  *errs.DomainError
}

func (outcome *fakeLoopMountOutcome) Response() []byte {
	return append([]byte(nil), outcome.response...)
}

func (outcome *fakeLoopMountOutcome) ResponseEOF() bool { return outcome.responseEOF }

func (outcome *fakeLoopMountOutcome) ProcessStarted() bool { return outcome.started }

func (outcome *fakeLoopMountOutcome) Diagnostic() *errs.DomainError { return outcome.diagnostic }

// fakeLoopMountExchanger provides one deterministic boundary outcome without opening a process or socket.
type fakeLoopMountExchanger struct {
	exchange func(context.Context, *system.PrivilegedControlRequest) privilegedExchangeOutcome
	outcome  privilegedExchangeOutcome
	calls    int
}

func (exchanger *fakeLoopMountExchanger) Exchange(
	ctx context.Context,
	request *system.PrivilegedControlRequest,
) privilegedExchangeOutcome {
	exchanger.calls++
	if exchanger.exchange != nil {
		return exchanger.exchange(ctx, request)
	}
	return exchanger.outcome
}

// validLoopMountProvisionRequest creates a no-resize request whose unchanged-size result is independently known.
func validLoopMountProvisionRequest() VMProvisionRequest {
	return VMProvisionRequest{
		CacheLocator:        "/srv/mvm-cache",
		VMID:                testVMID,
		ExpectedRootfsBytes: 512 << 20,
		Filesystem:          FilesystemExt4,
		Plan:                VMProvisionPlan{FixFstab: true},
	}
}

// validLoopMountProvisionResult binds every authoritative result field to the no-resize request contract.
func validLoopMountProvisionResult(request VMProvisionRequest) VMProvisionResult {
	return VMProvisionResult{
		VMID:        request.VMID,
		Filesystem:  request.Filesystem,
		RootfsBytes: request.ExpectedRootfsBytes,
	}
}

// encodeLoopMountSuccess creates one strict success envelope for client-boundary tests.
func encodeLoopMountSuccess[T any](t *testing.T, action string, result T) []byte {
	t.Helper()
	var encoded bytes.Buffer
	require.NoError(t, privilegedwire.EncodeSuccess(context.Background(), &encoded, action, result))
	return encoded.Bytes()
}

// encodeLoopMountError creates one strict remote DomainError envelope for precedence tests.
func encodeLoopMountError(t *testing.T, action string, operationErr error) []byte {
	t.Helper()
	var encoded bytes.Buffer
	require.NoError(t, privilegedwire.EncodeError(context.Background(), &encoded, action, operationErr))
	return encoded.Bytes()
}

// assertLoopMountTransportError verifies the complete stable classification and retry-safety details.
func assertLoopMountTransportError(t *testing.T, err error, started, outcomeUnknown bool) {
	t.Helper()
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeProcessError, domainErr.Code)
	assert.Equal(t, errs.ClassInternal, domainErr.Class)
	assert.Equal(t, started, domainErr.Details["process_started"])
	assert.Equal(t, outcomeUnknown, domainErr.Details["outcome_unknown"])
}
