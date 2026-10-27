package testutil

import (
	"context"
	"fmt"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
)

func (m *MockPolicyAPI) NetworkToHostTrafficPolicyCreate(
	ctx context.Context, input inputs.NetworkToHostTrafficPolicyCreateInput,
) (*results.NetworkToHostTrafficPolicy, error) {
	if m.NetworkToHostTrafficPolicyCreateFunc != nil {
		return m.NetworkToHostTrafficPolicyCreateFunc(ctx, input)
	}
	return nil, nil
}

func (m *MockPolicyAPI) NetworkToHostTrafficPolicyList(
	ctx context.Context,
) ([]*results.NetworkToHostTrafficPolicy, error) {
	if m.NetworkToHostTrafficPolicyListFunc != nil {
		return m.NetworkToHostTrafficPolicyListFunc(ctx)
	}
	return nil, nil
}

func (m *MockPolicyAPI) NetworkToHostTrafficPolicyInspect(
	ctx context.Context, input inputs.NetworkToHostTrafficPolicyInput,
) (*results.NetworkToHostTrafficPolicy, error) {
	if m.NetworkToHostTrafficPolicyInspectFunc != nil {
		return m.NetworkToHostTrafficPolicyInspectFunc(ctx, input)
	}
	return nil, nil
}

func (m *MockPolicyAPI) NetworkToHostTrafficPolicyRemove(
	ctx context.Context, input inputs.NetworkToHostTrafficPolicyInput,
) error {
	if m.NetworkToHostTrafficPolicyRemoveFunc != nil {
		return m.NetworkToHostTrafficPolicyRemoveFunc(ctx, input)
	}
	return nil
}

func (m *MockPolicyAPI) VMToHostTrafficPolicyCreate(
	ctx context.Context, input inputs.VMToHostTrafficPolicyCreateInput,
) (*results.VMToHostTrafficPolicy, error) {
	if m.VMToHostTrafficPolicyCreateFunc != nil {
		return m.VMToHostTrafficPolicyCreateFunc(ctx, input)
	}
	return nil, nil
}

func (m *MockPolicyAPI) VMToHostTrafficPolicyList(ctx context.Context) ([]*results.VMToHostTrafficPolicy, error) {
	if m.VMToHostTrafficPolicyListFunc != nil {
		return m.VMToHostTrafficPolicyListFunc(ctx)
	}
	return nil, nil
}

func (m *MockPolicyAPI) VMToHostTrafficPolicyInspect(
	ctx context.Context, input inputs.VMToHostTrafficPolicyInput,
) (*results.VMToHostTrafficPolicy, error) {
	if m.VMToHostTrafficPolicyInspectFunc != nil {
		return m.VMToHostTrafficPolicyInspectFunc(ctx, input)
	}
	return nil, nil
}

func (m *MockPolicyAPI) VMToHostTrafficPolicyRemove(
	ctx context.Context, input inputs.VMToHostTrafficPolicyInput,
) error {
	if m.VMToHostTrafficPolicyRemoveFunc != nil {
		return m.VMToHostTrafficPolicyRemoveFunc(ctx, input)
	}
	return nil
}

func (m *MockEnvironmentPolicyAPI) NetworkToHostTrafficPolicyEnsure(
	ctx context.Context,
	input inputs.NetworkToHostTrafficPolicyCreateInput,
	ownership inputs.EnvironmentPolicyOwnershipInput,
) (*results.EnvironmentPolicyReceipt, error) {
	if m.NetworkToHostTrafficPolicyEnsureFunc == nil {
		return nil, fmt.Errorf("unexpected owned policy request")
	}
	return m.NetworkToHostTrafficPolicyEnsureFunc(ctx, input, ownership)
}

func (m *MockEnvironmentPolicyAPI) NetworkToHostTrafficPolicyRemoveOwned(
	ctx context.Context,
	input inputs.NetworkToHostTrafficPolicyCreateInput,
	ownership inputs.EnvironmentPolicyOwnershipInput,
) error {
	if m.NetworkToHostTrafficPolicyRemoveOwnedFunc == nil {
		return fmt.Errorf("unexpected owned policy removal")
	}
	return m.NetworkToHostTrafficPolicyRemoveOwnedFunc(ctx, input, ownership)
}

func (m *MockEnvironmentPolicyAPI) VMToHostTrafficPolicyEnsure(
	ctx context.Context,
	input inputs.VMToHostTrafficPolicyCreateInput,
	ownership inputs.EnvironmentPolicyOwnershipInput,
) (*results.EnvironmentPolicyReceipt, error) {
	if m.VMToHostTrafficPolicyEnsureFunc == nil {
		return nil, fmt.Errorf("unexpected owned policy request")
	}
	return m.VMToHostTrafficPolicyEnsureFunc(ctx, input, ownership)
}

func (m *MockEnvironmentPolicyAPI) VMToHostTrafficPolicyRemoveOwned(
	ctx context.Context,
	input inputs.VMToHostTrafficPolicyCreateInput,
	ownership inputs.EnvironmentPolicyOwnershipInput,
) error {
	if m.VMToHostTrafficPolicyRemoveOwnedFunc == nil {
		return fmt.Errorf("unexpected owned policy removal")
	}
	return m.VMToHostTrafficPolicyRemoveOwnedFunc(ctx, input, ownership)
}
