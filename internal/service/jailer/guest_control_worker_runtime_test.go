package jailer

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGuestControlWorkerReadyPublicationRequiresUnchangedRecordedProcess(t *testing.T) {
	process := guestControlWorkerTestProcess()
	spawned := guestControlWorkerTestRecord()
	spawned.Phase = "spawned"
	spawned.Revision = 2
	spawned.Process = &process
	ready := spawned
	ready.Phase = "ready"
	ready.Revision++
	require.NoError(t, validateGuestControlWorkerTransition(spawned, true, ready))
	for name, change := range map[string]func(*guestControlWorkerRecord){
		"new incarnation": func(r *guestControlWorkerRecord) { r.LaunchGeneration++ },
		"new generation":  func(r *guestControlWorkerRecord) { r.Generation++ },
		"no process":      func(r *guestControlWorkerRecord) { r.Process = nil },
		"rebound process": func(r *guestControlWorkerRecord) { p := *r.Process; p.PID++; r.Process = &p },
	} {
		t.Run(name, func(t *testing.T) {
			r := ready
			change(&r)
			require.Error(t, validateGuestControlWorkerTransition(spawned, true, r))
		})
	}
	stopping := ready
	stopping.Phase = "stopping"
	stopping.Revision++
	require.NoError(t, validateGuestControlWorkerTransition(ready, true, stopping))
	require.Error(t, validateGuestControlWorkerTransition(ready, true, ready))
}

func TestGuestControlWorkerPrivateMarkerReservesMalformedInvocation(t *testing.T) {
	require.True(t, IsGuestControlWorkerInvocation([]string{guestControlWorkerMarker, "unexpected"}))
	_, _, err := ReceiveGuestControlWorkerSetup(t.Context(), []string{guestControlWorkerMarker, "unexpected"})
	require.Error(t, err)
	require.True(t, IsGuestControlWorkerInvocation([]string{"__mvm_guest_control_worker_v2"}))
	_, _, err = ReceiveGuestControlWorkerSetup(t.Context(), []string{"__mvm_guest_control_worker_v2"})
	require.Error(t, err)
	require.False(t, IsGuestControlWorkerInvocation(nil))
	require.False(t, IsGuestControlWorkerInvocation([]string{"run"}))
}
