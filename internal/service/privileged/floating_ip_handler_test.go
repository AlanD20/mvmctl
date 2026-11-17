package privileged

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/service/network"
	"mvmctl/pkg/errs"
)

func TestFloatingIPDispatchRequiresClosedPayloadFreeRequestBeforeEffects(t *testing.T) {
	id, netID := strings.Repeat("b", 32), strings.Repeat("a", 64)
	// Intentional wire-fixture sum: the four registered actions have different closed request types.
	actions := map[string]any{
		network.FloatingIPCreateAction: network.FloatingIPCreateRequest{
			ID:        id,
			NetworkID: netID,
			Name:      "api",
			IPv4:      "10.10.0.100",
		},
		network.FloatingIPRemoveAction: network.FloatingIPRemoveRequest{
			ID:                 id,
			NetworkID:          netID,
			ExpectedGeneration: 1,
		},
		network.FloatingIPGetAction:  network.FloatingIPGetRequest{ID: id, NetworkID: netID},
		network.FloatingIPListAction: network.FloatingIPListRequest{NetworkID: netID},
	}
	for action, body := range actions {
		for _, mode := range []string{"valid EOF", "trailing bytes", "nonzero payload"} {
			t.Run(action+"/"+mode, func(t *testing.T) {
				endpoint := newReceiverEndpoint()
				var payload []byte
				if mode == "nonzero payload" {
					payload = []byte("x")
				}
				frame := encodeTrustedReleaseRequest(t, action, body, payload)
				if mode == "trailing bytes" {
					frame = append(frame, 'x')
				}
				endpoint.input = *bytes.NewReader(frame)
				called := ""
				result := network.FloatingIPResult{
					ID:         id,
					NetworkID:  netID,
					Name:       "api",
					IPv4:       "10.10.0.100",
					Generation: 1,
				}
				admit := func(uid uint32, selected string) {
					if uid != 1000 {
						t.Fatalf("effect owner UID = %d, want authenticated operator 1000", uid)
					}
					called = selected
				}
				runtime := testRuntimeDeps(0, map[string]string{"SUDO_UID": "1000", "SUDO_GID": "1000"})
				runtime.control = trustedReleaseControlDeps(t, endpoint)
				runtime.floatingIP = floatingIPHandlerEffects{
					create: func(_ context.Context, uid uint32, request network.FloatingIPCreateRequest) (network.FloatingIPResult, error) {
						admit(uid, network.FloatingIPCreateAction)
						if diff := cmp.Diff(body, any(request)); diff != "" {
							t.Fatalf("create intent changed: %s", diff)
						}
						return result, nil
					},
					remove: func(_ context.Context, uid uint32, request network.FloatingIPRemoveRequest) (network.FloatingIPResult, error) {
						admit(uid, network.FloatingIPRemoveAction)
						if diff := cmp.Diff(body, any(request)); diff != "" {
							t.Fatalf("remove intent changed: %s", diff)
						}
						retired := result
						retired.Retired, retired.Generation = true, 2
						return retired, nil
					},
					get: func(_ context.Context, uid uint32, request network.FloatingIPGetRequest) (network.FloatingIPResult, error) {
						admit(uid, network.FloatingIPGetAction)
						if diff := cmp.Diff(body, any(request)); diff != "" {
							t.Fatalf("get intent changed: %s", diff)
						}
						return result, nil
					},
					list: func(_ context.Context, uid uint32, request network.FloatingIPListRequest) (network.FloatingIPPage, error) {
						admit(uid, network.FloatingIPListAction)
						if diff := cmp.Diff(body, any(request)); diff != "" {
							t.Fatalf("list intent changed: %s", diff)
						}
						return network.FloatingIPPage{Items: []network.FloatingIPResult{result}}, nil
					},
				}
				require.NoError(t, run(t.Context(), []string{Marker, action}, endpoint, runtime))
				response, err := privilegedwire.DecodeResponse[json.RawMessage](
					t.Context(),
					bytes.NewReader(endpoint.output.Bytes()),
					action,
				)
				require.NoError(t, err)
				if mode != "valid EOF" {
					require.NotNil(t, response.OperationError())
					if called != "" || response.OperationError().Code != errs.CodeValidationFailed {
						t.Fatalf("unclosed request: effect=%q error=%v", called, response.OperationError())
					}
					return
				}
				require.Nil(t, response.OperationError())
				if called != action {
					t.Fatalf("effect = %q, want %q", called, action)
				}
				if action == network.FloatingIPListAction {
					var got network.FloatingIPPage
					require.NoError(t, json.Unmarshal(response.Result(), &got))
					want := network.FloatingIPPage{Items: []network.FloatingIPResult{result}}
					if diff := cmp.Diff(want, got); diff != "" {
						t.Fatalf("page changed: %s", diff)
					}
				} else {
					var got network.FloatingIPResult
					require.NoError(t, json.Unmarshal(response.Result(), &got))
					if action == network.FloatingIPRemoveAction {
						result.Retired, result.Generation = true, 2
					}
					if diff := cmp.Diff(result, got); diff != "" {
						t.Fatalf("result changed: %s", diff)
					}
				}
			})
		}
	}
}
