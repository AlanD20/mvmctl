package privileged

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/guestcontrolwire"
	"mvmctl/internal/service/jailer"
	"mvmctl/internal/service/network"
	"mvmctl/pkg/errs"
)

func guestControlWorkerPrincipalFixture() jailer.GuestControlPrincipal {
	return jailer.GuestControlPrincipal{OwnerUID: 1000, OwnerGID: 1000, VMID: strings.Repeat("a", 32),
		BootID: "11111111-1111-4111-8111-111111111111", LaunchGeneration: 7, ExecutionUID: 1001}
}

func guestControlWorkerRequestFixture() guestcontrolwire.Request {
	return guestcontrolwire.Request{RequestID: "11111111-1111-4111-8111-111111111111",
		Operation: guestcontrolwire.Inspect,
		Input:     guestcontrolwire.FloatingIPInput{ResourceID: strings.Repeat("b", 32), ExpectedGeneration: 3}}
}

func TestGuestControlWorkerRequestRequiresExactSessionIncarnation(t *testing.T) {
	p := guestControlWorkerPrincipalFixture()
	for _, name := range []string{"exact", "operator", "VM", "boot", "launch generation", "execution UID"} {
		t.Run(name, func(t *testing.T) {
			actual := jailer.FloatingIPVMIdentity{OwnerUID: p.OwnerUID, VMID: p.VMID, BootID: p.BootID,
				LaunchGeneration: p.LaunchGeneration, ExecutionUID: p.ExecutionUID}
			switch name {
			case "operator":
				actual.OwnerUID++
			case "VM":
				actual.VMID = strings.Repeat("c", 32)
			case "boot":
				actual.BootID = "22222222-2222-4222-8222-222222222222"
			case "launch generation":
				actual.LaunchGeneration++
			case "execution UID":
				actual.ExecutionUID++
			}
			err := requireGuestControlRequestIncarnation(p, actual)
			if name == "exact" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestGuestControlWorkerResponseUsesClosedProjectionAndConservativeUnknownOutcome(t *testing.T) {
	request := guestControlWorkerRequestFixture()
	for _, tc := range []struct {
		name    string
		cause   error
		effects bool
		want    guestcontrolwire.FailureCode
	}{
		{"denied", errs.New(errs.CodeValidationFailed, "secret /root/key payload"),
			false, guestcontrolwire.FailureDenied},
		{"conflict", errs.New(errs.CodeInternal, "secret", errs.WithClass(errs.ClassConflict)),
			false, guestcontrolwire.FailureConflict},
		{"transport", errors.New("secret /var/lib/mvmctl/credentials"), false, guestcontrolwire.FailureUnavailable},
		{"unknown mutation", errors.New("secret write failure"), true, guestcontrolwire.FailureOutcomeUnknown},
		{"validation after mutation entry", errs.New(errs.CodeValidationFailed, "secret"),
			true, guestcontrolwire.FailureOutcomeUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := guestControlWorkerResponse(request, network.FloatingIPGuestResult{}, tc.effects, tc.cause)
			require.NoError(t, response.ValidateFor(request))
			require.Nil(t, response.Result)
			require.NotNil(t, response.Failure)
			require.Equal(t, tc.want, response.Failure.Code)
			raw, err := json.Marshal(response)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "secret")
			require.NotContains(t, string(raw), "/root")
			require.NotContains(t, string(raw), "/var/lib")
		})
	}
	for _, ownership := range []network.FloatingIPGuestOwnership{network.FloatingIPGuestUnassociated,
		network.FloatingIPGuestSelf, network.FloatingIPGuestOther} {
		result := network.FloatingIPGuestResult{ResourceID: request.Input.ResourceID, IPv4: "10.0.0.100", Generation: 3,
			Ownership: ownership}
		if ownership != network.FloatingIPGuestUnassociated {
			result.LeaseRemainingMillis = 1000
		}
		response := guestControlWorkerResponse(request, result, false, nil)
		require.NoError(t, response.ValidateFor(request))
		require.NotNil(t, response.Result)
		want := guestcontrolwire.FloatingIPResult{ResourceID: request.Input.ResourceID,
			IPv4: "10.0.0.100", Generation: 3,
			Ownership: guestcontrolwire.Ownership(ownership), LeaseRemainingMillis: result.LeaseRemainingMillis}
		if diff := cmp.Diff(want, *response.Result); diff != "" {
			t.Errorf("public result (-want +got):\n%s", diff)
		}
	}
	invalid := network.FloatingIPGuestResult{ResourceID: request.Input.ResourceID, IPv4: "10.0.0.100", Generation: 3,
		Ownership: network.FloatingIPGuestOther, LeaseRemainingMillis: 0}
	rejected := guestControlWorkerResponse(request, invalid, false, nil)
	require.Nil(t, rejected.Result)
	require.NotNil(t, rejected.Failure)
	require.Equal(t, guestcontrolwire.FailureInternal, rejected.Failure.Code)

}

func TestGuestControlWorkerCompositorRejectsMalformedRequestBeforeAdmission(t *testing.T) {
	request := guestControlWorkerRequestFixture()
	request.Operation = "exec"
	_, effects, err := executeGuestControlRequest(context.Background(), jailer.GuestControlPrincipal{}, request)
	require.Error(t, err)
	require.False(t, effects)
}
