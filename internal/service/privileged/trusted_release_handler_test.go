package privileged

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/service/jailer"
	"mvmctl/pkg/errs"
)

func TestTrustedReleaseInstallHandlerAdmitsZeroAndStreamsNonzeroOnce(t *testing.T) {
	request := validTrustedReleaseInstallRequest()
	tests := map[string]struct {
		payload        []byte
		wantNilPayload bool
	}{
		"zero_selects_root_fetch":  {wantNilPayload: true},
		"nonzero_streams_raw_body": {payload: []byte("archive")},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			endpoint := newReceiverEndpoint()
			endpoint.input = *bytes.NewReader(encodeTrustedReleaseRequest(
				t,
				jailer.TrustedReleaseInstallAction,
				request,
				tc.payload,
			))
			installCalls := 0
			effects := trustedReleaseHandlerEffects{
				install: func(
					ctx context.Context,
					gotRequest jailer.TrustedReleaseInstallRequest,
					payload io.Reader,
					payloadLength uint64,
				) (jailer.TrustedReleaseInstallResult, error) {
					installCalls++
					assert.Same(t, t.Context(), ctx)
					if diff := cmp.Diff(request, gotRequest); diff != "" {
						t.Errorf("install request mismatch (-want +got):\n%s", diff)
					}
					assert.Equal(t, uint64(len(tc.payload)), payloadLength)
					if tc.wantNilPayload {
						assert.Nil(t, payload)
					} else {
						gotPayload, err := io.ReadAll(payload)
						require.NoError(t, err)
						assert.Equal(t, tc.payload, gotPayload)
					}
					return validTrustedReleaseInstallResult(request, jailer.TrustedReleaseInstalled), nil
				},
			}
			control := trustedReleaseControlDeps(t, endpoint)

			err := handleTrustedReleaseInstall(t.Context(), endpoint, effects, control)

			require.NoError(t, err)
			assert.Equal(t, 1, installCalls)
			assert.Equal(t, 1, endpoint.shutdownCalls)
			assert.Zero(t, endpoint.closeCalls)
			response, decodeErr := privilegedwire.DecodeResponse[jailer.TrustedReleaseInstallResult](
				context.Background(),
				bytes.NewReader(endpoint.output.Bytes()),
				jailer.TrustedReleaseInstallAction,
			)
			require.NoError(t, decodeErr)
			assert.Nil(t, response.OperationError())
			want := validTrustedReleaseInstallResult(request, jailer.TrustedReleaseInstalled)
			if diff := cmp.Diff(want, response.Result()); diff != "" {
				t.Errorf("install response mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// Rationale: The handler must not hide truncation or trailing bytes behind a limited reader. The Jailer effect receives
// the raw stream once and remains the sole owner of exact declared-length plus EOF admission.
func TestTrustedReleaseInstallHandlerLeavesExactPayloadAdmissionToJailer(t *testing.T) {
	request := validTrustedReleaseInstallRequest()
	tests := map[string]struct {
		declared uint64
		payload  []byte
		wantErr  string
	}{
		"exact":     {declared: 7, payload: []byte("archive")},
		"truncated": {declared: 8, payload: []byte("archive"), wantErr: "truncated"},
		"trailing":  {declared: 6, payload: []byte("archive"), wantErr: "trailing"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			endpoint := newReceiverEndpoint()
			frame := encodeTrustedReleaseHeader(
				t,
				jailer.TrustedReleaseInstallAction,
				request,
				tc.declared,
			)
			endpoint.input = *bytes.NewReader(append(frame, tc.payload...))
			installCalls := 0
			published := false
			effects := trustedReleaseHandlerEffects{
				install: func(
					_ context.Context,
					_ jailer.TrustedReleaseInstallRequest,
					payload io.Reader,
					payloadLength uint64,
				) (jailer.TrustedReleaseInstallResult, error) {
					installCalls++
					assert.Equal(t, tc.declared, payloadLength)
					body := make([]byte, payloadLength)
					if _, err := io.ReadFull(payload, body); err != nil {
						return jailer.TrustedReleaseInstallResult{}, errs.New(
							errs.CodeBinaryUntrusted,
							"trusted release archive is truncated",
						)
					}
					var trailing [1]byte
					count, err := payload.Read(trailing[:])
					if count > 0 || err == nil {
						return jailer.TrustedReleaseInstallResult{}, errs.New(
							errs.CodeBinaryUntrusted,
							"trusted release archive contains trailing input",
						)
					}
					if !errors.Is(err, io.EOF) {
						return jailer.TrustedReleaseInstallResult{}, err
					}
					published = true
					return validTrustedReleaseInstallResult(request, jailer.TrustedReleaseInstalled), nil
				},
			}

			err := handleTrustedReleaseInstall(t.Context(), endpoint, effects, trustedReleaseControlDeps(t, endpoint))

			require.NoError(t, err)
			assert.Equal(t, 1, installCalls)
			if tc.wantErr != "" {
				operationErr := decodeReceiverError(t, endpoint.output.Bytes(), jailer.TrustedReleaseInstallAction)
				assert.ErrorContains(t, operationErr, tc.wantErr)
				assert.False(t, published)
				return
			}
			assert.True(t, published)
			response, decodeErr := privilegedwire.DecodeResponse[jailer.TrustedReleaseInstallResult](
				context.Background(),
				bytes.NewReader(endpoint.output.Bytes()),
				jailer.TrustedReleaseInstallAction,
			)
			require.NoError(t, decodeErr)
			assert.Nil(t, response.OperationError())
		})
	}
}

// Rationale: A zero-payload operation must observe actual request EOF before it can mutate release authority.
func TestTrustedReleaseZeroInstallAndRemoveRequireEOFFirst(t *testing.T) {
	installRequest := validTrustedReleaseInstallRequest()
	tests := map[string]struct {
		action string
		body   any
		run    func(context.Context, controlEndpoint, trustedReleaseHandlerEffects, controlDeps) error
	}{
		"install": {
			action: jailer.TrustedReleaseInstallAction,
			body:   installRequest,
			run:    handleTrustedReleaseInstall,
		},
		"remove": {
			action: jailer.TrustedReleaseRemoveAction,
			body:   jailer.TrustedReleaseRemoveRequest{Slot: installRequest.Slot},
			run:    handleTrustedReleaseRemove,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			endpoint := newReceiverEndpoint()
			frame := encodeTrustedReleaseRequest(t, tc.action, tc.body, nil)
			frame = append(frame, 'x')
			endpoint.input = *bytes.NewReader(frame)
			effectCalls := 0
			effects := trustedReleaseHandlerEffects{
				install: func(
					context.Context,
					jailer.TrustedReleaseInstallRequest,
					io.Reader,
					uint64,
				) (jailer.TrustedReleaseInstallResult, error) {
					effectCalls++
					return jailer.TrustedReleaseInstallResult{}, nil
				},
				remove: func(
					context.Context,
					jailer.TrustedReleaseRemoveRequest,
				) (jailer.TrustedReleaseRemoveResult, error) {
					effectCalls++
					return jailer.TrustedReleaseRemoveResult{}, nil
				},
			}

			err := tc.run(t.Context(), endpoint, effects, trustedReleaseControlDeps(t, endpoint))

			require.NoError(t, err)
			assert.Zero(t, effectCalls)
			operationErr := decodeReceiverError(t, endpoint.output.Bytes(), tc.action)
			assert.Equal(t, errs.CodeValidationFailed, operationErr.Code)
			assert.ErrorContains(t, operationErr, "trailing input")
			assert.Equal(t, 1, endpoint.shutdownCalls)
		})
	}
}

func TestTrustedReleaseHandlersRejectPayloadBoundsBeforeEffect(t *testing.T) {
	request := validTrustedReleaseInstallRequest()
	tests := map[string]struct {
		action        string
		body          any
		payloadLength uint64
		handler       func(context.Context, controlEndpoint, trustedReleaseHandlerEffects, controlDeps) error
	}{
		"install_oversize": {
			action:        jailer.TrustedReleaseInstallAction,
			body:          request,
			payloadLength: 128*1024*1024 + 1,
			handler:       handleTrustedReleaseInstall,
		},
		"remove_declared_payload": {
			action:        jailer.TrustedReleaseRemoveAction,
			body:          jailer.TrustedReleaseRemoveRequest{Slot: request.Slot},
			payloadLength: 1,
			handler:       handleTrustedReleaseRemove,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			endpoint := newReceiverEndpoint()
			endpoint.input = *bytes.NewReader(encodeTrustedReleaseHeader(t, tc.action, tc.body, tc.payloadLength))
			effectCalls := 0
			effects := trustedReleaseHandlerEffects{
				install: func(
					context.Context,
					jailer.TrustedReleaseInstallRequest,
					io.Reader,
					uint64,
				) (jailer.TrustedReleaseInstallResult, error) {
					effectCalls++
					return jailer.TrustedReleaseInstallResult{}, nil
				},
				remove: func(
					context.Context,
					jailer.TrustedReleaseRemoveRequest,
				) (jailer.TrustedReleaseRemoveResult, error) {
					effectCalls++
					return jailer.TrustedReleaseRemoveResult{}, nil
				},
			}

			err := tc.handler(t.Context(), endpoint, effects, trustedReleaseControlDeps(t, endpoint))

			require.NoError(t, err)
			assert.Zero(t, effectCalls)
			operationErr := decodeReceiverError(t, endpoint.output.Bytes(), tc.action)
			assert.Equal(t, errs.CodeValidationFailed, operationErr.Code)
			assert.ErrorContains(t, operationErr, "payload length exceeds")
		})
	}
}

func TestTrustedReleaseHandlersRequireEveryClosedRequestField(t *testing.T) {
	tests := map[string]struct {
		action  string
		header  string
		handler func(context.Context, controlEndpoint, trustedReleaseHandlerEffects, controlDeps) error
	}{
		"install_missing_allow_replacement": {
			action: jailer.TrustedReleaseInstallAction,
			header: `{"schema_version":1,"action":"jailer-release-install",` +
				`"body":{"slot":{"version":"1.16.1","architecture":"x86_64"}}}`,
			handler: handleTrustedReleaseInstall,
		},
		"install_null_allow_replacement": {
			action: jailer.TrustedReleaseInstallAction,
			header: `{"schema_version":1,"action":"jailer-release-install",` +
				`"body":{"slot":{"version":"1.16.1","architecture":"x86_64"},"allow_replacement":null}}`,
			handler: handleTrustedReleaseInstall,
		},
		"install_missing_slot": {
			action:  jailer.TrustedReleaseInstallAction,
			header:  `{"schema_version":1,"action":"jailer-release-install","body":{"allow_replacement":false}}`,
			handler: handleTrustedReleaseInstall,
		},
		"remove_null_slot": {
			action:  jailer.TrustedReleaseRemoveAction,
			header:  `{"schema_version":1,"action":"jailer-release-remove","body":{"slot":null}}`,
			handler: handleTrustedReleaseRemove,
		},
		"remove_missing_slot_version": {
			action: jailer.TrustedReleaseRemoveAction,
			header: `{"schema_version":1,"action":"jailer-release-remove",` +
				`"body":{"slot":{"architecture":"x86_64"}}}`,
			handler: handleTrustedReleaseRemove,
		},
		"remove_null_slot_architecture": {
			action: jailer.TrustedReleaseRemoveAction,
			header: `{"schema_version":1,"action":"jailer-release-remove",` +
				`"body":{"slot":{"version":"1.16.1","architecture":null}}}`,
			handler: handleTrustedReleaseRemove,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			endpoint := newReceiverEndpoint()
			endpoint.input = *bytes.NewReader(rawTrustedReleaseRequestFrame(tc.header, 0))
			effectCalls := 0
			effects := trustedReleaseHandlerEffects{
				install: func(
					context.Context,
					jailer.TrustedReleaseInstallRequest,
					io.Reader,
					uint64,
				) (jailer.TrustedReleaseInstallResult, error) {
					effectCalls++
					return jailer.TrustedReleaseInstallResult{}, nil
				},
				remove: func(
					context.Context,
					jailer.TrustedReleaseRemoveRequest,
				) (jailer.TrustedReleaseRemoveResult, error) {
					effectCalls++
					return jailer.TrustedReleaseRemoveResult{}, nil
				},
			}

			err := tc.handler(t.Context(), endpoint, effects, trustedReleaseControlDeps(t, endpoint))

			require.NoError(t, err)
			assert.Zero(t, effectCalls)
			operationErr := decodeReceiverError(t, endpoint.output.Bytes(), tc.action)
			assert.Equal(t, errs.CodeValidationFailed, operationErr.Code)
		})
	}
}

func TestTrustedReleaseInstallHandlerPreservesDomainErrorAndPartialState(t *testing.T) {
	request := validTrustedReleaseInstallRequest()
	endpoint := newReceiverEndpoint()
	endpoint.input = *bytes.NewReader(encodeTrustedReleaseRequest(
		t,
		jailer.TrustedReleaseInstallAction,
		request,
		nil,
	))
	operationErr := errs.New(
		errs.CodeBinaryTrustedInstallFailed,
		"trusted release committed but durability is uncertain",
		errs.WithClass(errs.ClassInternal),
		errs.WithEntity("1.16.1"),
		errs.WithDetails(map[string]any{"release_installed": true, "durability_uncertain": true}),
	)
	effects := trustedReleaseHandlerEffects{
		install: func(
			context.Context,
			jailer.TrustedReleaseInstallRequest,
			io.Reader,
			uint64,
		) (jailer.TrustedReleaseInstallResult, error) {
			return jailer.TrustedReleaseInstallResult{}, operationErr
		},
	}
	control := trustedReleaseControlDeps(t, endpoint)

	err := handleTrustedReleaseInstall(t.Context(), endpoint, effects, control)

	require.NoError(t, err)
	got := decodeReceiverError(t, endpoint.output.Bytes(), jailer.TrustedReleaseInstallAction)
	assert.Equal(t, operationErr.Code, got.Code)
	assert.Equal(t, operationErr.Class, got.Class)
	assert.Equal(t, operationErr.Entity, got.Entity)
	if diff := cmp.Diff(operationErr.Details, got.Details); diff != "" {
		t.Errorf("preserved DomainError details mismatch (-want +got):\n%s", diff)
	}
}

func TestTrustedReleaseRemoveHandlerReturnsBothClosedBooleanResults(t *testing.T) {
	request := jailer.TrustedReleaseRemoveRequest{Slot: validTrustedReleaseInstallRequest().Slot}
	for _, removed := range []bool{false, true} {
		t.Run(strings.ToLower(strconv.FormatBool(removed)), func(t *testing.T) {
			endpoint := newReceiverEndpoint()
			endpoint.input = *bytes.NewReader(encodeTrustedReleaseRequest(
				t,
				jailer.TrustedReleaseRemoveAction,
				request,
				nil,
			))
			effects := trustedReleaseHandlerEffects{
				remove: func(
					context.Context,
					jailer.TrustedReleaseRemoveRequest,
				) (jailer.TrustedReleaseRemoveResult, error) {
					return jailer.TrustedReleaseRemoveResult{Removed: removed}, nil
				},
			}

			err := handleTrustedReleaseRemove(t.Context(), endpoint, effects, trustedReleaseControlDeps(t, endpoint))

			require.NoError(t, err)
			response, decodeErr := privilegedwire.DecodeResponse[jailer.TrustedReleaseRemoveResult](
				context.Background(),
				bytes.NewReader(endpoint.output.Bytes()),
				jailer.TrustedReleaseRemoveAction,
			)
			require.NoError(t, decodeErr)
			assert.Nil(t, response.OperationError())
			assert.Equal(t, jailer.TrustedReleaseRemoveResult{Removed: removed}, response.Result())
		})
	}
}

func TestTrustedReleaseInstallHandlerRejectsInvalidEffectResultAsOneError(t *testing.T) {
	request := validTrustedReleaseInstallRequest()
	tests := map[string]func(jailer.TrustedReleaseInstallResult) jailer.TrustedReleaseInstallResult{
		"invalid_outcome": func(result jailer.TrustedReleaseInstallResult) jailer.TrustedReleaseInstallResult {
			result.Outcome = "repaired"
			return result
		},
		"slot_mismatch": func(result jailer.TrustedReleaseInstallResult) jailer.TrustedReleaseInstallResult {
			result.Slot.Version = "1.16.0"
			return result
		},
		"uppercase_hash": func(result jailer.TrustedReleaseInstallResult) jailer.TrustedReleaseInstallResult {
			result.ArchiveSHA256 = strings.Repeat("A", 64)
			return result
		},
		"invalid_size": func(result jailer.TrustedReleaseInstallResult) jailer.TrustedReleaseInstallResult {
			result.Firecracker.SizeBytes = 1
			return result
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			endpoint := newReceiverEndpoint()
			endpoint.input = *bytes.NewReader(encodeTrustedReleaseRequest(
				t,
				jailer.TrustedReleaseInstallAction,
				request,
				nil,
			))
			effects := trustedReleaseHandlerEffects{
				install: func(
					context.Context,
					jailer.TrustedReleaseInstallRequest,
					io.Reader,
					uint64,
				) (jailer.TrustedReleaseInstallResult, error) {
					return mutate(validTrustedReleaseInstallResult(request, jailer.TrustedReleaseInstalled)), nil
				},
			}

			err := handleTrustedReleaseInstall(t.Context(), endpoint, effects, trustedReleaseControlDeps(t, endpoint))

			require.NoError(t, err)
			operationErr := decodeReceiverError(t, endpoint.output.Bytes(), jailer.TrustedReleaseInstallAction)
			assert.Equal(t, errs.CodeValidationFailed, operationErr.Code)
			assert.Equal(t, 1, bytes.Count(endpoint.output.Bytes(), []byte("MVMRES01")))
			assert.Equal(t, 1, endpoint.shutdownCalls)
		})
	}
}

// Rationale: A declared payload receives its phase deadline immediately after header decode, even when semantic
// validation fails, while the resulting error receives a separate fresh response budget.
func TestTrustedReleaseHandlerStartsPayloadAndResponseDeadlinesAtPhaseBoundaries(t *testing.T) {
	request := validTrustedReleaseInstallRequest()
	request.Slot.Version = "../1.16.1"
	endpoint := newReceiverEndpoint()
	endpoint.input = *bytes.NewReader(encodeTrustedReleaseHeader(
		t,
		jailer.TrustedReleaseInstallAction,
		request,
		1,
	))
	headerNow := time.Now()
	payloadNow := headerNow.Add(2 * time.Minute)
	responseNow := headerNow.Add(4 * time.Minute)
	times := []time.Time{headerNow, payloadNow, responseNow}
	control := trustedReleaseControlDeps(t, endpoint)
	control.now = func() time.Time {
		value := times[0]
		times = times[1:]
		return value
	}
	effectCalls := 0
	effects := trustedReleaseHandlerEffects{
		install: func(
			context.Context,
			jailer.TrustedReleaseInstallRequest,
			io.Reader,
			uint64,
		) (jailer.TrustedReleaseInstallResult, error) {
			effectCalls++
			return jailer.TrustedReleaseInstallResult{}, nil
		},
	}

	err := handleTrustedReleaseInstall(t.Context(), endpoint, effects, control)

	require.NoError(t, err)
	assert.Zero(t, effectCalls)
	assert.Empty(t, times)
	assert.Equal(t, responseNow.Add(5*time.Second), endpoint.writeDeadline)
	operationErr := decodeReceiverError(t, endpoint.output.Bytes(), jailer.TrustedReleaseInstallAction)
	assert.Equal(t, errs.CodeValidationFailed, operationErr.Code)
}

func TestTrustedReleaseHandlerFreshResponseDeadlineFailureReturnsWithoutFrame(t *testing.T) {
	request := validTrustedReleaseInstallRequest()
	headerNow := time.Now()
	responseNow := headerNow.Add(2 * time.Minute)
	times := []time.Time{headerNow, responseNow}
	sentinel := errors.New("deadline sentinel")
	endpoint := newReceiverEndpoint()
	endpoint.input = *bytes.NewReader(encodeTrustedReleaseRequest(
		t,
		jailer.TrustedReleaseInstallAction,
		request,
		nil,
	))
	endpoint.writeDeadlineErr = sentinel
	control := trustedReleaseControlDeps(t, endpoint)
	control.now = func() time.Time {
		value := times[0]
		times = times[1:]
		return value
	}
	effects := trustedReleaseHandlerEffects{
		install: func(
			context.Context,
			jailer.TrustedReleaseInstallRequest,
			io.Reader,
			uint64,
		) (jailer.TrustedReleaseInstallResult, error) {
			return validTrustedReleaseInstallResult(request, jailer.TrustedReleaseInstalled), nil
		},
	}

	err := handleTrustedReleaseInstall(t.Context(), endpoint, effects, control)

	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
	assert.Equal(t, responseNow.Add(5*time.Second), endpoint.writeDeadline)
	assert.Zero(t, endpoint.writeCalls)
	assert.Zero(t, endpoint.shutdownCalls)
}

func TestTrustedReleaseHandlersCancelBlockedHeaderAndPayloadReads(t *testing.T) {
	request := validTrustedReleaseInstallRequest()
	t.Run("blocked_header", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		endpoint := newReceiverEndpoint()
		control := trustedReleaseControlDeps(t, endpoint)
		control.recvmsg = func(context.Context, int, []byte, int) (int, error) {
			return 0, unix.EAGAIN
		}
		var pollTimeout int
		control.poll = func(_ context.Context, _ []unix.PollFd, timeoutMillis int) (int, error) {
			pollTimeout = timeoutMillis
			cancel()
			return 0, nil
		}
		effectCalls := 0
		effects := trustedReleaseHandlerEffects{
			install: func(
				context.Context,
				jailer.TrustedReleaseInstallRequest,
				io.Reader,
				uint64,
			) (jailer.TrustedReleaseInstallResult, error) {
				effectCalls++
				return jailer.TrustedReleaseInstallResult{}, nil
			},
		}

		err := handleTrustedReleaseInstall(ctx, endpoint, effects, control)

		require.NoError(t, err)
		assert.Zero(t, effectCalls)
		assert.LessOrEqual(t, pollTimeout, 100)
		decodeReceiverError(t, endpoint.output.Bytes(), jailer.TrustedReleaseInstallAction)
	})

	t.Run("blocked_payload", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		endpoint := newReceiverEndpoint()
		endpoint.input = *bytes.NewReader(encodeTrustedReleaseHeader(
			t,
			jailer.TrustedReleaseInstallAction,
			request,
			1,
		))
		control := trustedReleaseControlDeps(t, endpoint)
		originalReceive := control.recvmsg
		control.recvmsg = func(ctx context.Context, fd int, target []byte, flags int) (int, error) {
			count, err := originalReceive(ctx, fd, target, flags)
			if count == 0 && err == nil {
				return 0, unix.EAGAIN
			}
			return count, err
		}
		var pollTimeout int
		control.poll = func(_ context.Context, _ []unix.PollFd, timeoutMillis int) (int, error) {
			pollTimeout = timeoutMillis
			cancel()
			return 0, nil
		}
		effectCalls := 0
		effects := trustedReleaseHandlerEffects{
			install: func(
				effectCtx context.Context,
				_ jailer.TrustedReleaseInstallRequest,
				payload io.Reader,
				_ uint64,
			) (jailer.TrustedReleaseInstallResult, error) {
				effectCalls++
				assert.Same(t, ctx, effectCtx)
				var value [1]byte
				_, err := payload.Read(value[:])
				assert.ErrorIs(t, err, context.Canceled)
				return jailer.TrustedReleaseInstallResult{}, err
			},
		}

		err := handleTrustedReleaseInstall(ctx, endpoint, effects, control)

		require.NoError(t, err)
		assert.Equal(t, 1, effectCalls)
		assert.Positive(t, pollTimeout)
		assert.LessOrEqual(t, pollTimeout, 100)
		decodeReceiverError(t, endpoint.output.Bytes(), jailer.TrustedReleaseInstallAction)
	})
}

func TestTrustedReleaseHandlerResponseTransportFailureIsNotDoubleFramed(t *testing.T) {
	request := validTrustedReleaseInstallRequest()
	sentinel := errors.New("write sentinel")
	endpoint := newReceiverEndpoint()
	endpoint.input = *bytes.NewReader(encodeTrustedReleaseRequest(
		t,
		jailer.TrustedReleaseInstallAction,
		request,
		nil,
	))
	endpoint.writeFunc = func(value []byte) (int, error) {
		endpoint.output.Write(value[:1])
		return 1, sentinel
	}
	effects := trustedReleaseHandlerEffects{
		install: func(
			context.Context,
			jailer.TrustedReleaseInstallRequest,
			io.Reader,
			uint64,
		) (jailer.TrustedReleaseInstallResult, error) {
			return validTrustedReleaseInstallResult(request, jailer.TrustedReleaseInstalled), nil
		},
	}
	control := trustedReleaseControlDeps(t, endpoint)

	err := handleTrustedReleaseInstall(t.Context(), endpoint, effects, control)

	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
	assert.Equal(t, 1, endpoint.writeCalls)
	assert.LessOrEqual(t, bytes.Count(endpoint.output.Bytes(), []byte("MVMRES01")), 1)

}

func TestRun_RoutesExactTrustedReleaseActions(t *testing.T) {
	installRequest := validTrustedReleaseInstallRequest()
	installResult := validTrustedReleaseInstallResult(installRequest, jailer.TrustedReleaseInstalled)
	removeRequest := jailer.TrustedReleaseRemoveRequest{Slot: installRequest.Slot}
	removeResult := jailer.TrustedReleaseRemoveResult{Removed: true}

	t.Run("install", func(t *testing.T) {
		endpoint := newReceiverEndpoint()
		endpoint.input = *bytes.NewReader(encodeTrustedReleaseRequest(
			t,
			jailer.TrustedReleaseInstallAction,
			installRequest,
			nil,
		))
		installCalls := 0
		removeCalls := 0
		runtime := testRuntimeDeps(0, map[string]string{"SUDO_UID": "1000", "SUDO_GID": "1000"})
		runtime.control = trustedReleaseControlDeps(t, endpoint)
		runtime.trustedRelease = trustedReleaseHandlerEffects{
			install: func(
				ctx context.Context,
				gotRequest jailer.TrustedReleaseInstallRequest,
				payload io.Reader,
				payloadLength uint64,
			) (jailer.TrustedReleaseInstallResult, error) {
				installCalls++
				assert.Same(t, t.Context(), ctx)
				if diff := cmp.Diff(installRequest, gotRequest); diff != "" {
					t.Errorf("install request mismatch (-want +got):\n%s", diff)
				}
				assert.Nil(t, payload)
				assert.Zero(t, payloadLength)
				return installResult, nil
			},
			remove: func(
				context.Context,
				jailer.TrustedReleaseRemoveRequest,
			) (jailer.TrustedReleaseRemoveResult, error) {
				removeCalls++
				return jailer.TrustedReleaseRemoveResult{}, nil
			},
		}

		err := run(t.Context(), []string{Marker, jailer.TrustedReleaseInstallAction}, endpoint, runtime)

		require.NoError(t, err)
		assert.Equal(t, 1, installCalls)
		assert.Zero(t, removeCalls)
		response, decodeErr := privilegedwire.DecodeResponse[jailer.TrustedReleaseInstallResult](
			context.Background(),
			bytes.NewReader(endpoint.output.Bytes()),
			jailer.TrustedReleaseInstallAction,
		)
		require.NoError(t, decodeErr)
		assert.Nil(t, response.OperationError())
		if diff := cmp.Diff(installResult, response.Result()); diff != "" {
			t.Errorf("install response mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("remove", func(t *testing.T) {
		endpoint := newReceiverEndpoint()
		endpoint.input = *bytes.NewReader(encodeTrustedReleaseRequest(
			t,
			jailer.TrustedReleaseRemoveAction,
			removeRequest,
			nil,
		))
		installCalls := 0
		removeCalls := 0
		runtime := testRuntimeDeps(0, map[string]string{"SUDO_UID": "1000", "SUDO_GID": "1000"})
		runtime.control = trustedReleaseControlDeps(t, endpoint)
		runtime.trustedRelease = trustedReleaseHandlerEffects{
			install: func(
				context.Context,
				jailer.TrustedReleaseInstallRequest,
				io.Reader,
				uint64,
			) (jailer.TrustedReleaseInstallResult, error) {
				installCalls++
				return jailer.TrustedReleaseInstallResult{}, nil
			},
			remove: func(
				ctx context.Context,
				gotRequest jailer.TrustedReleaseRemoveRequest,
			) (jailer.TrustedReleaseRemoveResult, error) {
				removeCalls++
				assert.Same(t, t.Context(), ctx)
				if diff := cmp.Diff(removeRequest, gotRequest); diff != "" {
					t.Errorf("remove request mismatch (-want +got):\n%s", diff)
				}
				return removeResult, nil
			},
		}

		err := run(t.Context(), []string{Marker, jailer.TrustedReleaseRemoveAction}, endpoint, runtime)

		require.NoError(t, err)
		assert.Zero(t, installCalls)
		assert.Equal(t, 1, removeCalls)
		response, decodeErr := privilegedwire.DecodeResponse[jailer.TrustedReleaseRemoveResult](
			context.Background(),
			bytes.NewReader(endpoint.output.Bytes()),
			jailer.TrustedReleaseRemoveAction,
		)
		require.NoError(t, decodeErr)
		assert.Nil(t, response.OperationError())
		if diff := cmp.Diff(removeResult, response.Result()); diff != "" {
			t.Errorf("remove response mismatch (-want +got):\n%s", diff)
		}
	})
}

// Rationale: A release handler owns its response transaction after exact action routing. If response delivery fails,
// outer dispatch must return that failure without attempting a second error frame on the same control socket.
func TestRun_TrustedReleaseResponseFailureIsNotDoubleFramed(t *testing.T) {
	installRequest := validTrustedReleaseInstallRequest()
	sentinel := errors.New("write sentinel")
	tests := map[string]struct {
		action  string
		frame   []byte
		effects trustedReleaseHandlerEffects
	}{
		"install": {
			action: jailer.TrustedReleaseInstallAction,
			frame: encodeTrustedReleaseRequest(
				t,
				jailer.TrustedReleaseInstallAction,
				installRequest,
				nil,
			),
			effects: trustedReleaseHandlerEffects{install: func(
				context.Context,
				jailer.TrustedReleaseInstallRequest,
				io.Reader,
				uint64,
			) (jailer.TrustedReleaseInstallResult, error) {
				return validTrustedReleaseInstallResult(installRequest, jailer.TrustedReleaseInstalled), nil
			}},
		},
		"remove": {
			action: jailer.TrustedReleaseRemoveAction,
			frame: encodeTrustedReleaseRequest(
				t,
				jailer.TrustedReleaseRemoveAction,
				jailer.TrustedReleaseRemoveRequest{Slot: installRequest.Slot},
				nil,
			),
			effects: trustedReleaseHandlerEffects{remove: func(
				context.Context,
				jailer.TrustedReleaseRemoveRequest,
			) (jailer.TrustedReleaseRemoveResult, error) {
				return jailer.TrustedReleaseRemoveResult{Removed: true}, nil
			}},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			endpoint := newReceiverEndpoint()
			endpoint.input = *bytes.NewReader(tc.frame)
			endpoint.writeFunc = func(value []byte) (int, error) {
				endpoint.output.Write(value[:1])
				return 1, sentinel
			}
			runtime := testRuntimeDeps(0, map[string]string{"SUDO_UID": "1000", "SUDO_GID": "1000"})
			runtime.control = trustedReleaseControlDeps(t, endpoint)
			runtime.trustedRelease = tc.effects

			err := run(t.Context(), []string{Marker, tc.action}, endpoint, runtime)

			require.Error(t, err)
			assert.ErrorIs(t, err, sentinel)
			assert.Equal(t, 1, endpoint.writeCalls)
			assert.LessOrEqual(t, bytes.Count(endpoint.output.Bytes(), []byte("MVMRES01")), 1)
		})
	}
}

func validTrustedReleaseInstallRequest() jailer.TrustedReleaseInstallRequest {
	return jailer.TrustedReleaseInstallRequest{
		Slot: jailer.ReleaseSlot{Version: "1.16.1", Architecture: "x86_64"},
	}
}

func validTrustedReleaseInstallResult(
	request jailer.TrustedReleaseInstallRequest,
	outcome jailer.TrustedReleaseInstallOutcome,
) jailer.TrustedReleaseInstallResult {
	return jailer.TrustedReleaseInstallResult{
		Outcome:       outcome,
		Slot:          request.Slot,
		ArchiveSHA256: strings.Repeat("a", 64),
		Firecracker: jailer.TrustedReleaseExecutableResult{
			SHA256: strings.Repeat("b", 64), SizeBytes: 120,
		},
		Jailer: jailer.TrustedReleaseExecutableResult{
			SHA256: strings.Repeat("c", 64), SizeBytes: 120,
		},
	}
}

func trustedReleaseControlDeps(t *testing.T, endpoint *receiverEndpoint) controlDeps {
	t.Helper()
	deps := validControlDeps(endpoint, endpoint.calls)
	deps.recvmsg = func(_ context.Context, fd int, target []byte, flags int) (int, error) {
		endpoint.readCalls++
		if endpoint.calls != nil {
			*endpoint.calls = append(*endpoint.calls, "control.recvmsg")
		}
		assert.Equal(t, 0, fd)
		assert.Equal(t, unix.MSG_DONTWAIT, flags)
		count, err := endpoint.input.Read(target)
		if errors.Is(err, io.EOF) {
			return 0, nil
		}
		return count, err
	}
	deps.poll = func(context.Context, []unix.PollFd, int) (int, error) {
		return 0, unix.EIO
	}
	return deps
}

func encodeTrustedReleaseRequest(t *testing.T, action string, body any, payload []byte) []byte {
	t.Helper()
	frame := encodeTrustedReleaseHeader(t, action, body, uint64(len(payload)))
	return append(frame, payload...)
}

func encodeTrustedReleaseHeader(t *testing.T, action string, body any, payloadLength uint64) []byte {
	t.Helper()
	var frame bytes.Buffer
	require.NoError(t, privilegedwire.EncodeRequest(t.Context(), &frame, action, body, payloadLength))
	return frame.Bytes()
}

func rawTrustedReleaseRequestFrame(header string, payloadLength uint64) []byte {
	frame := make([]byte, 20+len(header))
	copy(frame, "MVMREQ01")
	binary.BigEndian.PutUint32(frame[8:12], uint32(len(header)))
	binary.BigEndian.PutUint64(frame[12:20], payloadLength)
	copy(frame[20:], header)
	return frame
}
