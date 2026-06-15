package testutil

// MockOperation implements api.API for testing by embedding all per-domain mocks.
type MockOperation struct {
	MockVMAPI
	MockImageAPI
	MockNetworkAPI
	MockVolumeAPI
	MockKernelAPI
	MockKeyAPI
	MockBinaryAPI
	MockHostAPI
	MockConsoleAPI
	MockSSHAPI
	MockConfigAPI
	MockCacheAPI
	MockLogAPI
	MockCPAPI
	MockInitAPI
}
