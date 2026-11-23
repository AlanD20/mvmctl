package testutil

import (
	"context"

	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
)

func (m *MockPolicyAPI) VMToFloatingIPTrafficPolicyCreate(
	ctx context.Context,
	input inputs.VMToFloatingIPTrafficPolicyCreateInput,
) (*results.VMToFloatingIPTrafficPolicy, error) {
	if m.VMToFloatingIPTrafficPolicyCreateFunc != nil {
		return m.VMToFloatingIPTrafficPolicyCreateFunc(ctx, input)
	}
	return nil, nil
}
func (m *MockPolicyAPI) VMToFloatingIPTrafficPolicyList(
	ctx context.Context,
) ([]*results.VMToFloatingIPTrafficPolicy, error) {
	if m.VMToFloatingIPTrafficPolicyListFunc != nil {
		return m.VMToFloatingIPTrafficPolicyListFunc(ctx)
	}
	return nil, nil
}
func (m *MockPolicyAPI) VMToFloatingIPTrafficPolicyInspect(
	ctx context.Context,
	input inputs.VMToFloatingIPTrafficPolicyInput,
) (*results.VMToFloatingIPTrafficPolicy, error) {
	if m.VMToFloatingIPTrafficPolicyInspectFunc != nil {
		return m.VMToFloatingIPTrafficPolicyInspectFunc(ctx, input)
	}
	return nil, nil
}
func (m *MockPolicyAPI) VMToFloatingIPTrafficPolicyRemove(
	ctx context.Context,
	input inputs.VMToFloatingIPTrafficPolicyInput,
) error {
	if m.VMToFloatingIPTrafficPolicyRemoveFunc != nil {
		return m.VMToFloatingIPTrafficPolicyRemoveFunc(ctx, input)
	}
	return nil
}
func (m *MockPolicyAPI) NetworkToFloatingIPTrafficPolicyCreate(
	ctx context.Context,
	input inputs.NetworkToFloatingIPTrafficPolicyCreateInput,
) (*results.NetworkToFloatingIPTrafficPolicy, error) {
	if m.NetworkToFloatingIPTrafficPolicyCreateFunc != nil {
		return m.NetworkToFloatingIPTrafficPolicyCreateFunc(ctx, input)
	}
	return nil, nil
}
func (m *MockPolicyAPI) NetworkToFloatingIPTrafficPolicyList(
	ctx context.Context,
) ([]*results.NetworkToFloatingIPTrafficPolicy, error) {
	if m.NetworkToFloatingIPTrafficPolicyListFunc != nil {
		return m.NetworkToFloatingIPTrafficPolicyListFunc(ctx)
	}
	return nil, nil
}
func (m *MockPolicyAPI) NetworkToFloatingIPTrafficPolicyInspect(
	ctx context.Context,
	input inputs.NetworkToFloatingIPTrafficPolicyInput,
) (*results.NetworkToFloatingIPTrafficPolicy, error) {
	if m.NetworkToFloatingIPTrafficPolicyInspectFunc != nil {
		return m.NetworkToFloatingIPTrafficPolicyInspectFunc(ctx, input)
	}
	return nil, nil
}
func (m *MockPolicyAPI) NetworkToFloatingIPTrafficPolicyRemove(
	ctx context.Context,
	input inputs.NetworkToFloatingIPTrafficPolicyInput,
) error {
	if m.NetworkToFloatingIPTrafficPolicyRemoveFunc != nil {
		return m.NetworkToFloatingIPTrafficPolicyRemoveFunc(ctx, input)
	}
	return nil
}
