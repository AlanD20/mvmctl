package firewall

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFloatingIPObservationReportsExactRootBindingAndLifetime(t *testing.T) {
	state := floatingLeaseTestState(t)
	binding := state.intent.FloatingIPs[0]
	observed := kernelObservationFromCompiled(state.compiled)
	observed.tables[0].secondaryElements = []secondaryLeaseElement{
		{key: secondaryLiveKey(), timeout: 5 * time.Second, remaining: 2 * time.Second},
	}
	result, err := state.observeFloatingIPLease(binding.ID, binding.Generation, observed)
	require.NoError(t, err)
	require.Equal(t, binding, result.Binding)
	require.True(t, result.Present)
	require.Equal(t, 2*time.Second, result.Remaining)
	_, err = state.observeFloatingIPLease(binding.ID, binding.Generation+1, observed)
	require.Error(t, err)
	state.intent.FloatingIPs = nil
	_, err = state.observeFloatingIPLease(binding.ID, binding.Generation, observed)
	require.Error(t, err)
}
func TestFloatingIPObservationDistinguishesOwnerlessFromExpired(t *testing.T) {
	state := floatingLeaseTestState(t)
	binding := state.intent.FloatingIPs[0]
	observed := kernelObservationFromCompiled(state.compiled)
	result, err := state.observeFloatingIPLease(binding.ID, binding.Generation, observed)
	require.NoError(t, err)
	require.False(t, result.Present)
	require.Equal(t, binding.OwnerVMID, result.Binding.OwnerVMID)
	state.intent.FloatingIPs[0] = ownerlessFloatingLease(binding)
	var compileErr error
	state.compiled, compileErr = state.intent.compile()
	require.NoError(t, compileErr)
	observed = kernelObservationFromCompiled(state.compiled)
	result, err = state.observeFloatingIPLease(binding.ID, binding.Generation, observed)
	require.NoError(t, err)
	require.Empty(t, result.Binding.OwnerVMID)
	require.False(t, result.Present)
	observed.tables[0].secondaryElements = []secondaryLeaseElement{
		{key: secondaryLiveKey(), timeout: 5 * time.Second, remaining: time.Second},
	}
	_, err = state.observeFloatingIPLease(binding.ID, binding.Generation, observed)
	require.Error(t, err, "foreign timed key must fail graph proof")
}
