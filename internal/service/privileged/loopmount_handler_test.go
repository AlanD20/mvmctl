package privileged

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/service/loopmount"
	"mvmctl/pkg/errs"
)

func TestLoopMountHandlersBindCallerAndDeliverTypedResults(t *testing.T) {
	caller := loopmount.Caller{UID: 1201, GID: 1302}

	t.Run("provision_vm", func(t *testing.T) {
		request := validLoopMountProvisionRequest()
		want := validLoopMountProvisionResult(request)
		endpoint := newReceiverEndpoint()
		endpoint.input = *bytes.NewReader(encodeLoopMountRequest(t, loopmount.VMProvisionAction, request, 0))
		calls := 0
		effects := loopMountHandlerEffects{provisionVM: func(
			ctx context.Context,
			gotCaller loopmount.Caller,
			gotRequest loopmount.VMProvisionRequest,
		) (loopmount.VMProvisionResult, error) {
			calls++
			assert.Same(t, t.Context(), ctx)
			if diff := cmp.Diff(caller, gotCaller); diff != "" {
				t.Errorf("provision caller mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(request, gotRequest); diff != "" {
				t.Errorf("provision request mismatch (-want +got):\n%s", diff)
			}
			return want, nil
		}}

		err := handleLoopMountProvisionVM(
			t.Context(),
			endpoint,
			caller,
			effects,
			trustedReleaseControlDeps(t, endpoint),
		)

		require.NoError(t, err)
		assert.Equal(t, 1, calls)
		assert.Equal(t, 1, endpoint.shutdownCalls)
		response, decodeErr := privilegedwire.DecodeResponse[loopmount.VMProvisionResult](
			context.Background(),
			bytes.NewReader(endpoint.output.Bytes()),
			loopmount.VMProvisionAction,
		)
		require.NoError(t, decodeErr)
		assert.Nil(t, response.OperationError())
		if diff := cmp.Diff(want, response.Result()); diff != "" {
			t.Errorf("provision response mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("inspect_image", func(t *testing.T) {
		request := validLoopMountInspectRequest()
		want := validLoopMountInspectResult(request)
		endpoint := newReceiverEndpoint()
		endpoint.input = *bytes.NewReader(encodeLoopMountRequest(t, loopmount.ImageInspectAction, request, 0))
		calls := 0
		effects := loopMountHandlerEffects{inspectImage: func(
			ctx context.Context,
			gotCaller loopmount.Caller,
			gotRequest loopmount.ImageInspectRequest,
		) (loopmount.ImageInspectResult, error) {
			calls++
			assert.Same(t, t.Context(), ctx)
			if diff := cmp.Diff(caller, gotCaller); diff != "" {
				t.Errorf("inspect caller mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(request, gotRequest); diff != "" {
				t.Errorf("inspect request mismatch (-want +got):\n%s", diff)
			}
			return want, nil
		}}

		err := handleLoopMountInspectImage(
			t.Context(),
			endpoint,
			caller,
			effects,
			trustedReleaseControlDeps(t, endpoint),
		)

		require.NoError(t, err)
		assert.Equal(t, 1, calls)
		assert.Equal(t, 1, endpoint.shutdownCalls)
		response, decodeErr := privilegedwire.DecodeResponse[loopmount.ImageInspectResult](
			context.Background(),
			bytes.NewReader(endpoint.output.Bytes()),
			loopmount.ImageInspectAction,
		)
		require.NoError(t, decodeErr)
		assert.Nil(t, response.OperationError())
		if diff := cmp.Diff(want, response.Result()); diff != "" {
			t.Errorf("inspect response mismatch (-want +got):\n%s", diff)
		}
	})
}

// Rationale: Both rootfs effects must remain behind an exact action, zero payload, validated request, and actual EOF.
// No malformed or foreign frame may reach either mount-capable effect.
func TestLoopMountHandlersRejectUnadmittedInputBeforeEffect(t *testing.T) {
	provisionRequest := validLoopMountProvisionRequest()
	inspectRequest := validLoopMountInspectRequest()
	invalidProvisionRequest := provisionRequest
	invalidProvisionRequest.VMID = "not-a-vm-id"
	invalidInspectRequest := inspectRequest
	invalidInspectRequest.ImageID = "not-an-image-id"
	tests := map[string]struct {
		action string
		frame  []byte
		run    func(context.Context, controlEndpoint, loopmount.Caller, loopMountHandlerEffects, controlDeps) error
	}{
		"provision_malformed": {
			action: loopmount.VMProvisionAction,
			frame:  []byte("not-a-request"),
			run:    handleLoopMountProvisionVM,
		},
		"provision_declared_payload": {
			action: loopmount.VMProvisionAction,
			frame:  encodeLoopMountRequest(t, loopmount.VMProvisionAction, provisionRequest, 1),
			run:    handleLoopMountProvisionVM,
		},
		"provision_trailing_input": {
			action: loopmount.VMProvisionAction,
			frame:  append(encodeLoopMountRequest(t, loopmount.VMProvisionAction, provisionRequest, 0), 'x'),
			run:    handleLoopMountProvisionVM,
		},
		"provision_foreign_action": {
			action: loopmount.VMProvisionAction,
			frame:  encodeLoopMountRequest(t, loopmount.ImageInspectAction, provisionRequest, 0),
			run:    handleLoopMountProvisionVM,
		},
		"provision_invalid_request": {
			action: loopmount.VMProvisionAction,
			frame:  encodeLoopMountRequest(t, loopmount.VMProvisionAction, invalidProvisionRequest, 0),
			run:    handleLoopMountProvisionVM,
		},
		"inspect_malformed": {
			action: loopmount.ImageInspectAction,
			frame:  []byte("not-a-request"),
			run:    handleLoopMountInspectImage,
		},
		"inspect_declared_payload": {
			action: loopmount.ImageInspectAction,
			frame:  encodeLoopMountRequest(t, loopmount.ImageInspectAction, inspectRequest, 1),
			run:    handleLoopMountInspectImage,
		},
		"inspect_trailing_input": {
			action: loopmount.ImageInspectAction,
			frame:  append(encodeLoopMountRequest(t, loopmount.ImageInspectAction, inspectRequest, 0), 'x'),
			run:    handleLoopMountInspectImage,
		},
		"inspect_foreign_action": {
			action: loopmount.ImageInspectAction,
			frame:  encodeLoopMountRequest(t, loopmount.VMProvisionAction, inspectRequest, 0),
			run:    handleLoopMountInspectImage,
		},
		"inspect_invalid_request": {
			action: loopmount.ImageInspectAction,
			frame:  encodeLoopMountRequest(t, loopmount.ImageInspectAction, invalidInspectRequest, 0),
			run:    handleLoopMountInspectImage,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			endpoint := newReceiverEndpoint()
			endpoint.input = *bytes.NewReader(tc.frame)
			effectCalls := 0
			effects := loopMountHandlerEffects{
				provisionVM: func(
					context.Context,
					loopmount.Caller,
					loopmount.VMProvisionRequest,
				) (loopmount.VMProvisionResult, error) {
					effectCalls++
					return loopmount.VMProvisionResult{}, nil
				},
				inspectImage: func(
					context.Context,
					loopmount.Caller,
					loopmount.ImageInspectRequest,
				) (loopmount.ImageInspectResult, error) {
					effectCalls++
					return loopmount.ImageInspectResult{}, nil
				},
			}

			err := tc.run(
				t.Context(),
				endpoint,
				loopmount.Caller{UID: 1201, GID: 1302},
				effects,
				trustedReleaseControlDeps(t, endpoint),
			)

			require.NoError(t, err)
			assert.Zero(t, effectCalls)
			operationErr := decodeReceiverError(t, endpoint.output.Bytes(), tc.action)
			assert.Equal(t, errs.CodeValidationFailed, operationErr.Code)
			assert.Equal(t, 1, endpoint.shutdownCalls)
		})
	}
}

func TestLoopMountHandlersRejectNilEffectsAndInvalidResults(t *testing.T) {
	tests := map[string]struct {
		action  string
		frame   []byte
		effects loopMountHandlerEffects
		run     func(context.Context, controlEndpoint, loopmount.Caller, loopMountHandlerEffects, controlDeps) error
		wantErr string
	}{
		"provision_nil_effect": {
			action:  loopmount.VMProvisionAction,
			frame:   encodeLoopMountRequest(t, loopmount.VMProvisionAction, validLoopMountProvisionRequest(), 0),
			run:     handleLoopMountProvisionVM,
			wantErr: "provision handler is not active",
		},
		"inspect_nil_effect": {
			action:  loopmount.ImageInspectAction,
			frame:   encodeLoopMountRequest(t, loopmount.ImageInspectAction, validLoopMountInspectRequest(), 0),
			run:     handleLoopMountInspectImage,
			wantErr: "inspection handler is not active",
		},
		"provision_invalid_result": {
			action: loopmount.VMProvisionAction,
			frame:  encodeLoopMountRequest(t, loopmount.VMProvisionAction, validLoopMountProvisionRequest(), 0),
			effects: loopMountHandlerEffects{provisionVM: func(
				context.Context,
				loopmount.Caller,
				loopmount.VMProvisionRequest,
			) (loopmount.VMProvisionResult, error) {
				return loopmount.VMProvisionResult{}, nil
			}},
			run:     handleLoopMountProvisionVM,
			wantErr: "result ID does not match request",
		},
		"inspect_invalid_result": {
			action: loopmount.ImageInspectAction,
			frame:  encodeLoopMountRequest(t, loopmount.ImageInspectAction, validLoopMountInspectRequest(), 0),
			effects: loopMountHandlerEffects{inspectImage: func(
				context.Context,
				loopmount.Caller,
				loopmount.ImageInspectRequest,
			) (loopmount.ImageInspectResult, error) {
				return loopmount.ImageInspectResult{}, nil
			}},
			run:     handleLoopMountInspectImage,
			wantErr: "result ID does not match request",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			endpoint := newReceiverEndpoint()
			endpoint.input = *bytes.NewReader(tc.frame)

			err := tc.run(
				t.Context(),
				endpoint,
				loopmount.Caller{UID: 1201, GID: 1302},
				tc.effects,
				trustedReleaseControlDeps(t, endpoint),
			)

			require.NoError(t, err)
			operationErr := decodeReceiverError(t, endpoint.output.Bytes(), tc.action)
			assert.ErrorContains(t, operationErr, tc.wantErr)
			assert.Equal(t, 1, endpoint.shutdownCalls)
		})
	}
}

// Rationale: A Server cleanup failure is an authoritative operation error, while a response transport failure belongs
// to the handler transaction and must be returned without a second frame.
func TestLoopMountHandlersPreserveCleanupAndResponseFailures(t *testing.T) {
	t.Run("cleanup_error_response", func(t *testing.T) {
		request := validLoopMountInspectRequest()
		endpoint := newReceiverEndpoint()
		endpoint.input = *bytes.NewReader(encodeLoopMountRequest(t, loopmount.ImageInspectAction, request, 0))
		cleanupErr := errs.New(
			errs.CodeLoopMountError,
			"release managed image inspection authority",
			errs.WithClass(errs.ClassInternal),
			errs.WithDetails(map[string]any{"rootfs_release_failed": true}),
		)
		effects := loopMountHandlerEffects{inspectImage: func(
			context.Context,
			loopmount.Caller,
			loopmount.ImageInspectRequest,
		) (loopmount.ImageInspectResult, error) {
			return loopmount.ImageInspectResult{}, cleanupErr
		}}

		err := handleLoopMountInspectImage(
			t.Context(),
			endpoint,
			loopmount.Caller{UID: 1201, GID: 1302},
			effects,
			trustedReleaseControlDeps(t, endpoint),
		)

		require.NoError(t, err)
		operationErr := decodeReceiverError(t, endpoint.output.Bytes(), loopmount.ImageInspectAction)
		assert.Equal(t, cleanupErr.Code, operationErr.Code)
		assert.Equal(t, cleanupErr.Class, operationErr.Class)
		assert.Equal(t, true, operationErr.Details["rootfs_release_failed"])
	})

	for _, action := range []string{loopmount.VMProvisionAction, loopmount.ImageInspectAction} {
		t.Run(action+"_response_failure", func(t *testing.T) {
			endpoint := newReceiverEndpoint()
			effects := loopMountHandlerEffects{}
			var run func(context.Context, controlEndpoint, loopmount.Caller, loopMountHandlerEffects, controlDeps) error
			switch action {
			case loopmount.VMProvisionAction:
				request := validLoopMountProvisionRequest()
				endpoint.input = *bytes.NewReader(encodeLoopMountRequest(t, action, request, 0))
				effects.provisionVM = func(
					context.Context,
					loopmount.Caller,
					loopmount.VMProvisionRequest,
				) (loopmount.VMProvisionResult, error) {
					return validLoopMountProvisionResult(request), nil
				}
				run = handleLoopMountProvisionVM
			case loopmount.ImageInspectAction:
				request := validLoopMountInspectRequest()
				endpoint.input = *bytes.NewReader(encodeLoopMountRequest(t, action, request, 0))
				effects.inspectImage = func(
					context.Context,
					loopmount.Caller,
					loopmount.ImageInspectRequest,
				) (loopmount.ImageInspectResult, error) {
					return validLoopMountInspectResult(request), nil
				}
				run = handleLoopMountInspectImage
			}
			sentinel := errors.New("write sentinel")
			endpoint.writeFunc = func(value []byte) (int, error) {
				endpoint.output.Write(value[:1])
				return 1, sentinel
			}

			err := run(
				t.Context(),
				endpoint,
				loopmount.Caller{UID: 1201, GID: 1302},
				effects,
				trustedReleaseControlDeps(t, endpoint),
			)

			require.Error(t, err)
			assert.ErrorIs(t, err, sentinel)
			assert.Equal(t, 1, endpoint.writeCalls)
			assert.LessOrEqual(t, bytes.Count(endpoint.output.Bytes(), []byte("MVMRES01")), 1)
		})
	}
}

func TestRunRoutesExactLoopMountActionsWithAuthenticatedCaller(t *testing.T) {
	caller := loopmount.Caller{UID: 1000, GID: 1000}

	t.Run("provision_vm", func(t *testing.T) {
		request := validLoopMountProvisionRequest()
		want := validLoopMountProvisionResult(request)
		endpoint := newReceiverEndpoint()
		endpoint.input = *bytes.NewReader(encodeLoopMountRequest(t, loopmount.VMProvisionAction, request, 0))
		provisionCalls := 0
		inspectCalls := 0
		runtime := testRuntimeDeps(0, map[string]string{"SUDO_UID": "1000", "SUDO_GID": "1000"})
		runtime.control = trustedReleaseControlDeps(t, endpoint)
		runtime.loopMount = loopMountHandlerEffects{
			provisionVM: func(
				ctx context.Context,
				gotCaller loopmount.Caller,
				gotRequest loopmount.VMProvisionRequest,
			) (loopmount.VMProvisionResult, error) {
				provisionCalls++
				assert.Same(t, t.Context(), ctx)
				if diff := cmp.Diff(caller, gotCaller); diff != "" {
					t.Errorf("authenticated caller mismatch (-want +got):\n%s", diff)
				}
				if diff := cmp.Diff(request, gotRequest); diff != "" {
					t.Errorf("provision request mismatch (-want +got):\n%s", diff)
				}
				return want, nil
			},
			inspectImage: func(
				context.Context,
				loopmount.Caller,
				loopmount.ImageInspectRequest,
			) (loopmount.ImageInspectResult, error) {
				inspectCalls++
				return loopmount.ImageInspectResult{}, nil
			},
		}

		err := run(t.Context(), []string{Marker, loopmount.VMProvisionAction}, endpoint, runtime)

		require.NoError(t, err)
		assert.Equal(t, 1, provisionCalls)
		assert.Zero(t, inspectCalls)
		response, decodeErr := privilegedwire.DecodeResponse[loopmount.VMProvisionResult](
			context.Background(),
			bytes.NewReader(endpoint.output.Bytes()),
			loopmount.VMProvisionAction,
		)
		require.NoError(t, decodeErr)
		assert.Nil(t, response.OperationError())
		if diff := cmp.Diff(want, response.Result()); diff != "" {
			t.Errorf("provision response mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("inspect_image", func(t *testing.T) {
		request := validLoopMountInspectRequest()
		want := validLoopMountInspectResult(request)
		endpoint := newReceiverEndpoint()
		endpoint.input = *bytes.NewReader(encodeLoopMountRequest(t, loopmount.ImageInspectAction, request, 0))
		provisionCalls := 0
		inspectCalls := 0
		runtime := testRuntimeDeps(0, map[string]string{"SUDO_UID": "1000", "SUDO_GID": "1000"})
		runtime.control = trustedReleaseControlDeps(t, endpoint)
		runtime.loopMount = loopMountHandlerEffects{
			provisionVM: func(
				context.Context,
				loopmount.Caller,
				loopmount.VMProvisionRequest,
			) (loopmount.VMProvisionResult, error) {
				provisionCalls++
				return loopmount.VMProvisionResult{}, nil
			},
			inspectImage: func(
				ctx context.Context,
				gotCaller loopmount.Caller,
				gotRequest loopmount.ImageInspectRequest,
			) (loopmount.ImageInspectResult, error) {
				inspectCalls++
				assert.Same(t, t.Context(), ctx)
				if diff := cmp.Diff(caller, gotCaller); diff != "" {
					t.Errorf("authenticated caller mismatch (-want +got):\n%s", diff)
				}
				if diff := cmp.Diff(request, gotRequest); diff != "" {
					t.Errorf("inspect request mismatch (-want +got):\n%s", diff)
				}
				return want, nil
			},
		}

		err := run(t.Context(), []string{Marker, loopmount.ImageInspectAction}, endpoint, runtime)

		require.NoError(t, err)
		assert.Zero(t, provisionCalls)
		assert.Equal(t, 1, inspectCalls)
		response, decodeErr := privilegedwire.DecodeResponse[loopmount.ImageInspectResult](
			context.Background(),
			bytes.NewReader(endpoint.output.Bytes()),
			loopmount.ImageInspectAction,
		)
		require.NoError(t, decodeErr)
		assert.Nil(t, response.OperationError())
		if diff := cmp.Diff(want, response.Result()); diff != "" {
			t.Errorf("inspect response mismatch (-want +got):\n%s", diff)
		}
	})
}

// Rationale: The registered optimization action must fail closed when its production effect is unavailable.
func TestRunLoopMountOptimizeImageRequiresActiveHandler(t *testing.T) {
	request := loopmount.ImageOptimizeRequest{
		CacheLocator:       "/home/alice/.cache/mvmctl",
		ImageID:            strings.Repeat("c", 64),
		ExpectedImageBytes: 1 << 30,
		Filesystem:         loopmount.FilesystemExt4,
		Plan:               loopmount.ImageOptimizePlan{Shrink: true},
	}
	endpoint := newReceiverEndpoint()
	endpoint.input = *bytes.NewReader(encodeLoopMountRequest(t, loopmount.ImageOptimizeAction, request, 0))
	effectCalls := 0
	runtime := testRuntimeDeps(0, map[string]string{"SUDO_UID": "1000", "SUDO_GID": "1000"})
	runtime.control = trustedReleaseControlDeps(t, endpoint)
	runtime.loopMount = loopMountHandlerEffects{
		provisionVM: func(
			context.Context,
			loopmount.Caller,
			loopmount.VMProvisionRequest,
		) (loopmount.VMProvisionResult, error) {
			effectCalls++
			return loopmount.VMProvisionResult{}, nil
		},
		inspectImage: func(
			context.Context,
			loopmount.Caller,
			loopmount.ImageInspectRequest,
		) (loopmount.ImageInspectResult, error) {
			effectCalls++
			return loopmount.ImageInspectResult{}, nil
		},
	}

	err := run(t.Context(), []string{Marker, loopmount.ImageOptimizeAction}, endpoint, runtime)

	require.NoError(t, err)
	assert.Zero(t, effectCalls)
	operationErr := decodeReceiverError(t, endpoint.output.Bytes(), loopmount.ImageOptimizeAction)
	assert.Equal(t, errs.CodeInternal, operationErr.Code)
	assert.ErrorContains(t, operationErr, "loopmount image optimization handler is not active")
}

// Rationale: Exact LoopMount dispatch transfers response ownership to the selected handler. An outer second frame
// could conceal the first response failure and corrupt the one-request protocol transaction.
func TestRunLoopMountResponseFailureIsNotDoubleFramed(t *testing.T) {
	request := validLoopMountProvisionRequest()
	endpoint := newReceiverEndpoint()
	endpoint.input = *bytes.NewReader(encodeLoopMountRequest(t, loopmount.VMProvisionAction, request, 0))
	sentinel := errors.New("write sentinel")
	endpoint.writeFunc = func(value []byte) (int, error) {
		endpoint.output.Write(value[:1])
		return 1, sentinel
	}
	runtime := testRuntimeDeps(0, map[string]string{"SUDO_UID": "1000", "SUDO_GID": "1000"})
	runtime.control = trustedReleaseControlDeps(t, endpoint)
	runtime.loopMount = loopMountHandlerEffects{provisionVM: func(
		context.Context,
		loopmount.Caller,
		loopmount.VMProvisionRequest,
	) (loopmount.VMProvisionResult, error) {
		return validLoopMountProvisionResult(request), nil
	}}

	err := run(t.Context(), []string{Marker, loopmount.VMProvisionAction}, endpoint, runtime)

	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
	assert.Equal(t, 1, endpoint.writeCalls)
	assert.LessOrEqual(t, bytes.Count(endpoint.output.Bytes(), []byte("MVMRES01")), 1)
}

// Rationale: The composition root must not hand LoopMount the shared embedded slice or authorize an empty agent.
// Cancellation must win before the generated binary becomes effect input.
func TestCopyEmbeddedLoopMountAgentBinaryRequiresContextAndIndependentNonEmptyBytes(t *testing.T) {
	t.Run("copies_non_empty_generated_agent", func(t *testing.T) {
		embedded := []byte{0x7f, 'E', 'L', 'F', 1}
		want := bytes.Clone(embedded)

		got, err := copyEmbeddedLoopMountAgentBinary(t.Context(), embedded)

		require.NoError(t, err)
		require.NotEmpty(t, got)
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("embedded agent mismatch (-want +got):\n%s", diff)
		}
		embedded[0] = 0
		assert.Equal(t, byte(0x7f), got[0])
		got[1] = 0
		assert.Equal(t, byte('E'), want[1])
	})

	t.Run("rejects_empty_agent", func(t *testing.T) {
		got, err := copyEmbeddedLoopMountAgentBinary(t.Context(), nil)

		require.Error(t, err)
		assert.Nil(t, got)
		assert.Equal(t, errs.CodeLoopMountError, errs.AsDomainError(err).Code)
	})

	t.Run("rejects_nil_context", func(t *testing.T) {
		got, err := copyEmbeddedLoopMountAgentBinary(nil, []byte("generated-agent"))

		require.Error(t, err)
		assert.Nil(t, got)
		assert.Equal(t, errs.CodeInternal, errs.AsDomainError(err).Code)
	})

	t.Run("observes_cancellation_before_copy", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		got, err := copyEmbeddedLoopMountAgentBinary(ctx, []byte("generated-agent"))

		require.Error(t, err)
		assert.Nil(t, got)
		assert.ErrorIs(t, err, context.Canceled)
	})
}

func validLoopMountProvisionRequest() loopmount.VMProvisionRequest {
	return loopmount.VMProvisionRequest{
		CacheLocator:        "/home/alice/.cache/mvmctl",
		VMID:                strings.Repeat("a", 32),
		ExpectedRootfsBytes: 128 << 20,
		Filesystem:          loopmount.FilesystemExt4,
		Plan:                loopmount.VMProvisionPlan{FixFstab: true},
	}
}

func validLoopMountProvisionResult(request loopmount.VMProvisionRequest) loopmount.VMProvisionResult {
	return loopmount.VMProvisionResult{
		VMID:         request.VMID,
		Filesystem:   request.Filesystem,
		RootfsBytes:  request.ExpectedRootfsBytes,
		FilesWritten: 1,
	}
}

func validLoopMountInspectRequest() loopmount.ImageInspectRequest {
	return loopmount.ImageInspectRequest{
		CacheLocator:       "/home/alice/.cache/mvmctl",
		ImageID:            strings.Repeat("b", 64),
		ExpectedImageBytes: 1 << 30,
		Filesystem:         loopmount.FilesystemExt4,
	}
}

func validLoopMountInspectResult(request loopmount.ImageInspectRequest) loopmount.ImageInspectResult {
	return loopmount.ImageInspectResult{ImageID: request.ImageID, OSType: "ubuntu"}
}

func encodeLoopMountRequest(t *testing.T, action string, body any, payloadLength uint64) []byte {
	t.Helper()
	var frame bytes.Buffer
	require.NoError(t, privilegedwire.EncodeRequest(t.Context(), &frame, action, body, payloadLength))
	return frame.Bytes()
}
