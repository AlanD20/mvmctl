package privileged

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/service/network"
)

func TestFloatingIPDelegationDispatchBindsOwnerAndRequiresEOFBeforeEffects(t *testing.T) {
	id, resource, vmID, networkID := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c",
		32), strings.Repeat("d", 64)
	result := network.FloatingIPDelegationResult{ID: id, NetworkID: networkID, ResourceID: resource, VMID: vmID,
		BootID: "11111111-1111-4111-8111-111111111111", ExecutionUID: 2000, LaunchGeneration: 1, Method: "associate",
		LeaseDuration: 5 * time.Second, Revision: 1, State: "active"}
	actions := map[string]any{
		network.FloatingIPDelegationGrantAction: network.FloatingIPDelegationGrantRequest{
			ID: id, NetworkID: networkID, ResourceID: resource, VMID: vmID, Method: "associate", LeaseDuration: 5 * time.Second},
		network.FloatingIPDelegationRemoveOwnedAction: network.FloatingIPDelegationRemoveOwnedRequest{Grant: result},
		network.FloatingIPDelegationGetAction: network.FloatingIPDelegationGetRequest{
			NetworkID: networkID,
			ID:        id,
		},
		network.FloatingIPDelegationListAction: network.FloatingIPDelegationListRequest{NetworkID: networkID,
			ResourceID: resource},
		network.FloatingIPDelegationRevokeAction: network.FloatingIPDelegationRevokeRequest{
			NetworkID: networkID, ID: id, ExpectedRevision: 1,
		},
	}
	for action, body := range actions {
		for _, mode := range []string{"EOF", "trailing", "payload", "principal"} {
			t.Run(action+"/"+mode, func(t *testing.T) {
				endpoint := newReceiverEndpoint()
				encodedBody := body
				if mode == "principal" {
					raw, err := json.Marshal(body)
					require.NoError(t, err)
					var injected map[string]any
					require.NoError(t, json.Unmarshal(raw, &injected))
					injected["owner_uid"] = uint32(0)
					encodedBody = injected
				}
				var payload []byte
				if mode == "payload" {
					payload = []byte("x")
				}
				frame := encodeTrustedReleaseRequest(t, action, encodedBody, payload)
				if mode == "trailing" {
					frame = append(frame, 'x')
				}
				endpoint.input = *bytes.NewReader(frame)
				called := false
				check := func(uid uint32, request any) {
					assert.Equal(t, uint32(1000), uid, "only authenticated caller may authorize operator CRUD")
					if diff := cmp.Diff(body, request); diff != "" {
						t.Errorf("closed request (-want +got):\n%s", diff)
					}
					called = true
				}
				runtime := testRuntimeDeps(0, map[string]string{"SUDO_UID": "1000", "SUDO_GID": "1000"})
				runtime.control = trustedReleaseControlDeps(t, endpoint)
				runtime.floatingIPDelegation = floatingIPDelegationHandlerEffects{
					removeOwned: func(_ context.Context, uid uint32,
						r network.FloatingIPDelegationRemoveOwnedRequest) (network.FloatingIPDelegationResult, error) {
						check(uid, r)
						revoked := result
						revoked.Revision = 3
						revoked.State = "revoked"
						return revoked, nil
					},
					grant: func(_ context.Context, uid, gid uint32,
						r network.FloatingIPDelegationGrantRequest) (network.FloatingIPDelegationResult, error) {
						check(uid, r)
						assert.Equal(t, uint32(1000), gid)
						return result, nil
					},
					get: func(_ context.Context, uid uint32,
						r network.FloatingIPDelegationGetRequest) (network.FloatingIPDelegationResult, error) {
						check(uid, r)
						return result, nil
					},
					list: func(_ context.Context, uid uint32,
						r network.FloatingIPDelegationListRequest) (network.FloatingIPDelegationPage, error) {
						check(uid, r)
						return network.FloatingIPDelegationPage{
							Items: []network.FloatingIPDelegationResult{result},
						}, nil
					},
					revoke: func(_ context.Context, uid uint32,
						r network.FloatingIPDelegationRevokeRequest) (network.FloatingIPDelegationResult, error) {
						check(uid, r)
						revoked := result
						revoked.Revision = 3
						revoked.State = "revoked"
						return revoked, nil
					},
				}
				require.NoError(t, run(t.Context(), []string{Marker, action}, endpoint, runtime))
				response, err := privilegedwire.DecodeResponse[json.RawMessage](t.Context(),
					bytes.NewReader(endpoint.output.Bytes()), action)
				require.NoError(t, err)
				if mode != "EOF" {
					require.NotNil(t, response.OperationError())
					assert.False(t, called)
					return
				}
				require.Nil(t, response.OperationError())
				assert.True(t, called)
			})
		}
	}
}
