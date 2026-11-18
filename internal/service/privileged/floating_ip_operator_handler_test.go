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

func TestOperatorFloatingIPDispatchRequiresAuthenticatedCallerAndCompleteTypedFrame(t *testing.T) {
	id, netID, vmID := strings.Repeat("b", 32), strings.Repeat("a", 64), strings.Repeat("c", 32)
	// The codec boundary deliberately admits only these three closed request types.
	for action, body := range map[string]any{
		network.FloatingIPAssociateAction: network.FloatingIPAssociateRequest{ID: id, NetworkID: netID, VMID: vmID, ExpectedGeneration: 7},
		network.FloatingIPTransferAction:  network.FloatingIPTransferRequest{ID: id, NetworkID: netID, VMID: vmID, ExpectedGeneration: 7},
		network.FloatingIPReleaseAction:   network.FloatingIPReleaseRequest{ID: id, NetworkID: netID, ExpectedGeneration: 7},
	} {
		for _, mode := range []string{"complete EOF", "trailing byte", "payload"} {
			t.Run(action+"/"+mode, func(t *testing.T) {
				endpoint := newReceiverEndpoint()
				var payload []byte
				if mode == "payload" {
					payload = []byte("x")
				}
				frame := encodeTrustedReleaseRequest(t, action, body, payload)
				if mode == "trailing byte" {
					frame = append(frame, 'x')
				}
				endpoint.input = *bytes.NewReader(frame)
				runtime := testRuntimeDeps(0, map[string]string{"SUDO_UID": "1000", "SUDO_GID": "1000"})
				runtime.control = trustedReleaseControlDeps(t, endpoint)
				calls := 0
				admit := func(uid, gid uint32, request any) {
					calls++
					require.Equal(t, uint32(1000), uid)
					require.Equal(t, uint32(1000), gid)
					if diff := cmp.Diff(body, request); diff != "" {
						t.Fatalf("operator request (-want +got):\n%s", diff)
					}
				}
				want := network.FloatingIPResult{
					ID:         id,
					NetworkID:  netID,
					Generation: 8,
					IPv4:       "10.10.0.100",
					Name:       "api",
				}
				runtime.floatingIPOperator = floatingIPOperatorEffects{
					associate: func(_ context.Context, uid, gid uint32, request network.FloatingIPAssociateRequest) (network.FloatingIPResult, error) {
						admit(uid, gid, request)
						return want, nil
					},
					transfer: func(_ context.Context, uid, gid uint32, request network.FloatingIPTransferRequest) (network.FloatingIPResult, error) {
						admit(uid, gid, request)
						return want, nil
					},
					release: func(_ context.Context, uid, gid uint32, request network.FloatingIPReleaseRequest) (network.FloatingIPResult, error) {
						admit(uid, gid, request)
						return want, nil
					},
				}
				require.NoError(t, run(t.Context(), []string{Marker, action}, endpoint, runtime))
				response, err := privilegedwire.DecodeResponse[json.RawMessage](
					t.Context(),
					bytes.NewReader(endpoint.output.Bytes()),
					action,
				)
				require.NoError(t, err)
				if mode != "complete EOF" {
					require.Zero(t, calls)
					require.NotNil(t, response.OperationError())
					return
				}
				require.Equal(t, 1, calls)
				require.Nil(t, response.OperationError())
				var got network.FloatingIPResult
				require.NoError(t, json.Unmarshal(response.Result(), &got))
				if diff := cmp.Diff(want, got); diff != "" {
					t.Fatalf("operator result (-want +got):\n%s", diff)
				}
			})
		}
	}
}

func TestOperatorAliasSyncFailureReportsCommittedOwnership(t *testing.T) {
	result := network.FloatingIPResult{ID: strings.Repeat("a", 32), Generation: 4, OwnerVMID: strings.Repeat("b", 32)}
	err := operatorAliasSyncError(result, context.DeadlineExceeded)
	domain := errs.AsDomainError(err)
	require.NotNil(t, domain)
	require.Equal(t, true, domain.Details["root_committed"])
	require.Equal(t, true, domain.Details["alias_sync_failed"])
	require.Equal(t, uint64(4), domain.Details["generation"])
	require.Equal(t, result.OwnerVMID, domain.Details["owner_vm_id"])
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
