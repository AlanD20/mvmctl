package testutil

import (
	"context"
	"fmt"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
)

// MockReadyAPI supplies the readiness boundary for workflow tests.
type MockReadyAPI struct {
	ExecReadyFunc func(context.Context, inputs.ReadyInput) (*results.ExecResult, error)
}

func (m *MockReadyAPI) ExecReady(ctx context.Context, input inputs.ReadyInput) (*results.ExecResult, error) {
	if m.ExecReadyFunc == nil {
		return nil, fmt.Errorf("unexpected readiness request")
	}
	return m.ExecReadyFunc(ctx, input)
}

// MockEnvironmentPolicyAPI supplies explicit ownership outcomes for workflow tests.
type MockEnvironmentPolicyAPI struct {
	VMToHostTrafficPolicyEnsureFunc           func(context.Context, inputs.VMToHostTrafficPolicyCreateInput, inputs.EnvironmentPolicyOwnershipInput) (*results.EnvironmentPolicyReceipt, error)
	VMToHostTrafficPolicyRemoveOwnedFunc      func(context.Context, inputs.VMToHostTrafficPolicyCreateInput, inputs.EnvironmentPolicyOwnershipInput) error
	NetworkToHostTrafficPolicyEnsureFunc      func(context.Context, inputs.NetworkToHostTrafficPolicyCreateInput, inputs.EnvironmentPolicyOwnershipInput) (*results.EnvironmentPolicyReceipt, error)
	NetworkToHostTrafficPolicyRemoveOwnedFunc func(context.Context, inputs.NetworkToHostTrafficPolicyCreateInput, inputs.EnvironmentPolicyOwnershipInput) error
	NetworkToVMTrafficPolicyEnsureFunc        func(context.Context, inputs.NetworkToVMTrafficPolicyCreateInput, inputs.EnvironmentPolicyOwnershipInput) (*results.EnvironmentPolicyReceipt, error)
	NetworkToVMTrafficPolicyRemoveOwnedFunc   func(context.Context, inputs.NetworkToVMTrafficPolicyCreateInput, inputs.EnvironmentPolicyOwnershipInput) error
	VMToVMTrafficPolicyEnsureFunc             func(context.Context, inputs.VMToVMTrafficPolicyCreateInput, inputs.EnvironmentPolicyOwnershipInput) (*results.EnvironmentPolicyReceipt, error)
	VMToVMTrafficPolicyRemoveOwnedFunc        func(context.Context, inputs.VMToVMTrafficPolicyCreateInput, inputs.EnvironmentPolicyOwnershipInput) error
	ExecPolicyEnsureFunc                      func(context.Context, inputs.VMToVMExecPolicyCreateInput, inputs.EnvironmentPolicyOwnershipInput) (*results.EnvironmentPolicyReceipt, error)
	ExecPolicyRemoveOwnedFunc                 func(context.Context, inputs.VMToVMExecPolicyCreateInput, inputs.EnvironmentPolicyOwnershipInput) error
}

func (m *MockEnvironmentPolicyAPI) NetworkToVMTrafficPolicyEnsure(
	ctx context.Context,
	input inputs.NetworkToVMTrafficPolicyCreateInput,
	ownership inputs.EnvironmentPolicyOwnershipInput,
) (*results.EnvironmentPolicyReceipt, error) {
	if m.NetworkToVMTrafficPolicyEnsureFunc == nil {
		return nil, fmt.Errorf("unexpected owned policy request")
	}
	return m.NetworkToVMTrafficPolicyEnsureFunc(ctx, input, ownership)
}

func (m *MockEnvironmentPolicyAPI) NetworkToVMTrafficPolicyRemoveOwned(
	ctx context.Context,
	input inputs.NetworkToVMTrafficPolicyCreateInput,
	ownership inputs.EnvironmentPolicyOwnershipInput,
) error {
	if m.NetworkToVMTrafficPolicyRemoveOwnedFunc == nil {
		return fmt.Errorf("unexpected owned policy removal")
	}
	return m.NetworkToVMTrafficPolicyRemoveOwnedFunc(ctx, input, ownership)
}

func (m *MockEnvironmentPolicyAPI) VMToVMTrafficPolicyEnsure(
	ctx context.Context,
	input inputs.VMToVMTrafficPolicyCreateInput,
	ownership inputs.EnvironmentPolicyOwnershipInput,
) (*results.EnvironmentPolicyReceipt, error) {
	if m.VMToVMTrafficPolicyEnsureFunc == nil {
		return nil, fmt.Errorf("unexpected owned policy request")
	}
	return m.VMToVMTrafficPolicyEnsureFunc(ctx, input, ownership)
}

func (m *MockEnvironmentPolicyAPI) VMToVMTrafficPolicyRemoveOwned(
	ctx context.Context,
	input inputs.VMToVMTrafficPolicyCreateInput,
	ownership inputs.EnvironmentPolicyOwnershipInput,
) error {
	if m.VMToVMTrafficPolicyRemoveOwnedFunc == nil {
		return fmt.Errorf("unexpected owned policy removal")
	}
	return m.VMToVMTrafficPolicyRemoveOwnedFunc(ctx, input, ownership)
}

func (m *MockEnvironmentPolicyAPI) ExecPolicyEnsure(
	ctx context.Context,
	input inputs.VMToVMExecPolicyCreateInput,
	ownership inputs.EnvironmentPolicyOwnershipInput,
) (*results.EnvironmentPolicyReceipt, error) {
	if m.ExecPolicyEnsureFunc == nil {
		return nil, fmt.Errorf("unexpected owned policy request")
	}
	return m.ExecPolicyEnsureFunc(ctx, input, ownership)
}

func (m *MockEnvironmentPolicyAPI) ExecPolicyRemoveOwned(
	ctx context.Context,
	input inputs.VMToVMExecPolicyCreateInput,
	ownership inputs.EnvironmentPolicyOwnershipInput,
) error {
	if m.ExecPolicyRemoveOwnedFunc == nil {
		return fmt.Errorf("unexpected owned policy removal")
	}
	return m.ExecPolicyRemoveOwnedFunc(ctx, input, ownership)
}
