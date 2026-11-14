package jailer

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConsoleObserveRejectsNonAuthoritativeEndpointShapes(t *testing.T) {
	t.Parallel()
	pid := 4343
	request := ConsoleObserveRequest{VMID: testVMID}
	valid := ConsoleObserveResult{VMID: testVMID, State: ConsoleRunning, PID: &pid,
		SocketPath: consoleRuntimeSocketPath(1000, testVMID)}
	tests := map[string]func(*ConsoleObserveResult){
		"cache endpoint": func(result *ConsoleObserveResult) { result.SocketPath = "/home/caller/cache/console.sock" },
		"different VM": func(result *ConsoleObserveResult) {
			result.SocketPath = consoleRuntimeSocketPath(1000, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		},
		"noncanonical owner": func(result *ConsoleObserveResult) {
			result.SocketPath = "/run/mvmctl/runtime/01000/" + testVMID + "/console.sock"
		},
		"root owner":            func(result *ConsoleObserveResult) { result.SocketPath = consoleRuntimeSocketPath(0, testVMID) },
		"missing PID":           func(result *ConsoleObserveResult) { result.PID = nil },
		"stopped with endpoint": func(result *ConsoleObserveResult) { result.State = ConsoleStopped },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) { result := valid; mutate(&result); require.Error(t, result.Validate(request)) })
	}
	require.NoError(t, valid.Validate(request))
}

func TestConsoleStopResultRequiresExplicitOutcome(t *testing.T) {
	t.Parallel()
	for name, raw := range map[string]string{
		"missing":    `{"vm_id":"` + testVMID + `"}`,
		"null":       `{"vm_id":"` + testVMID + `","stopped":null}`,
		"wrong type": `{"vm_id":"` + testVMID + `","stopped":"false"}`,
	} {
		t.Run(name, func(t *testing.T) {
			var result ConsoleStopResult
			require.Error(t, json.Unmarshal([]byte(raw), &result))
		})
	}
	var result ConsoleStopResult
	require.NoError(t, json.Unmarshal([]byte(`{"vm_id":"`+testVMID+`","stopped":false}`), &result))
	assert.Equal(t, ConsoleStopResult{VMID: testVMID}, result)
}
