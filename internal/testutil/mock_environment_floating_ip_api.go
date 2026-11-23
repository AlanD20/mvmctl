package testutil

import (
	"context"
	"fmt"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
)

// MockEnvironmentFloatingIPAPI exposes explicit resource and owned workflow outcomes.
type MockEnvironmentFloatingIPAPI struct {
	FloatingIPAssociateFunc func(context.Context, inputs.FloatingIPAssociateInput) (*model.FloatingIPItem, error)
	FloatingIPTransferFunc  func(context.Context, inputs.FloatingIPTransferInput) (*model.FloatingIPItem, error)
	FloatingIPReleaseFunc   func(context.Context, inputs.FloatingIPReleaseInput) (*model.FloatingIPItem, error)
	FloatingIPInspectFunc   func(context.Context, inputs.FloatingIPInput) (*model.FloatingIPItem, error)

	FloatingIPDelegationGrantFunc  func(context.Context, inputs.FloatingIPDelegationGrantInput) (*model.FloatingIPDelegationItem, error)
	FloatingIPDelegationListFunc   func(context.Context, inputs.FloatingIPInput) ([]*model.FloatingIPDelegationItem, error)
	FloatingIPDelegationRevokeFunc func(context.Context, inputs.FloatingIPDelegationInput) (*model.FloatingIPDelegationItem, error)

	FloatingIPCreateFunc                            func(context.Context, inputs.FloatingIPCreateInput) (*model.FloatingIPItem, error)
	FloatingIPListFunc                              func(context.Context, inputs.FloatingIPNetworkInput) ([]*model.FloatingIPItem, error)
	FloatingIPGetFunc                               func(context.Context, inputs.FloatingIPInput) (*model.FloatingIPItem, error)
	FloatingIPRemoveFunc                            func(context.Context, inputs.FloatingIPInput) (*model.FloatingIPItem, error)
	FloatingIPEnsureFunc                            func(context.Context, inputs.FloatingIPCreateInput, inputs.EnvironmentFloatingIPOwnershipInput) (*model.FloatingIPItem, error)
	FloatingIPDelegationEnsureFunc                  func(context.Context, inputs.FloatingIPDelegationEnsureInput) (*model.FloatingIPDelegationItem, error)
	FloatingIPDelegationGetFunc                     func(context.Context, inputs.FloatingIPDelegationInput) (*model.FloatingIPDelegationItem, error)
	FloatingIPDelegationRemoveOwnedFunc             func(context.Context, inputs.FloatingIPDelegationRemoveOwnedInput) (*model.FloatingIPDelegationItem, error)
	VMToFloatingIPTrafficPolicyEnsureFunc           func(context.Context, inputs.VMToFloatingIPTrafficPolicyCreateInput, inputs.EnvironmentPolicyOwnershipInput) (*results.EnvironmentPolicyReceipt, error)
	VMToFloatingIPTrafficPolicyRemoveOwnedFunc      func(context.Context, inputs.VMToFloatingIPTrafficPolicyCreateInput, inputs.EnvironmentPolicyOwnershipInput) error
	NetworkToFloatingIPTrafficPolicyEnsureFunc      func(context.Context, inputs.NetworkToFloatingIPTrafficPolicyCreateInput, inputs.EnvironmentPolicyOwnershipInput) (*results.EnvironmentPolicyReceipt, error)
	NetworkToFloatingIPTrafficPolicyRemoveOwnedFunc func(context.Context, inputs.NetworkToFloatingIPTrafficPolicyCreateInput, inputs.EnvironmentPolicyOwnershipInput) error
}

func (m *MockEnvironmentFloatingIPAPI) FloatingIPCreate(
	ctx context.Context,
	arg0 inputs.FloatingIPCreateInput,
) (*model.FloatingIPItem, error) {
	if m.FloatingIPCreateFunc == nil {
		return nil, fmt.Errorf("unexpected FloatingIPCreate request")
	}
	return m.FloatingIPCreateFunc(ctx, arg0)
}

func (m *MockEnvironmentFloatingIPAPI) FloatingIPList(
	ctx context.Context,
	arg0 inputs.FloatingIPNetworkInput,
) ([]*model.FloatingIPItem, error) {
	if m.FloatingIPListFunc == nil {
		return nil, fmt.Errorf("unexpected FloatingIPList request")
	}
	return m.FloatingIPListFunc(ctx, arg0)
}

func (m *MockEnvironmentFloatingIPAPI) FloatingIPGet(
	ctx context.Context,
	arg0 inputs.FloatingIPInput,
) (*model.FloatingIPItem, error) {
	if m.FloatingIPGetFunc == nil {
		return nil, fmt.Errorf("unexpected FloatingIPGet request")
	}
	return m.FloatingIPGetFunc(ctx, arg0)
}

func (m *MockEnvironmentFloatingIPAPI) FloatingIPRemove(
	ctx context.Context,
	arg0 inputs.FloatingIPInput,
) (*model.FloatingIPItem, error) {
	if m.FloatingIPRemoveFunc == nil {
		return nil, fmt.Errorf("unexpected FloatingIPRemove request")
	}
	return m.FloatingIPRemoveFunc(ctx, arg0)
}

func (m *MockEnvironmentFloatingIPAPI) FloatingIPEnsure(
	ctx context.Context,
	arg0 inputs.FloatingIPCreateInput,
	arg1 inputs.EnvironmentFloatingIPOwnershipInput,
) (*model.FloatingIPItem, error) {
	if m.FloatingIPEnsureFunc == nil {
		return nil, fmt.Errorf("unexpected FloatingIPEnsure request")
	}
	return m.FloatingIPEnsureFunc(ctx, arg0, arg1)
}

func (m *MockEnvironmentFloatingIPAPI) FloatingIPDelegationEnsure(
	ctx context.Context,
	arg0 inputs.FloatingIPDelegationEnsureInput,
) (*model.FloatingIPDelegationItem, error) {
	if m.FloatingIPDelegationEnsureFunc == nil {
		return nil, fmt.Errorf("unexpected FloatingIPDelegationEnsure request")
	}
	return m.FloatingIPDelegationEnsureFunc(ctx, arg0)
}

func (m *MockEnvironmentFloatingIPAPI) FloatingIPDelegationGet(
	ctx context.Context,
	arg0 inputs.FloatingIPDelegationInput,
) (*model.FloatingIPDelegationItem, error) {
	if m.FloatingIPDelegationGetFunc == nil {
		return nil, fmt.Errorf("unexpected FloatingIPDelegationGet request")
	}
	return m.FloatingIPDelegationGetFunc(ctx, arg0)
}

func (m *MockEnvironmentFloatingIPAPI) FloatingIPDelegationRemoveOwned(
	ctx context.Context,
	arg0 inputs.FloatingIPDelegationRemoveOwnedInput,
) (*model.FloatingIPDelegationItem, error) {
	if m.FloatingIPDelegationRemoveOwnedFunc == nil {
		return nil, fmt.Errorf("unexpected FloatingIPDelegationRemoveOwned request")
	}
	return m.FloatingIPDelegationRemoveOwnedFunc(ctx, arg0)
}

func (m *MockEnvironmentFloatingIPAPI) VMToFloatingIPTrafficPolicyEnsure(
	ctx context.Context,
	arg0 inputs.VMToFloatingIPTrafficPolicyCreateInput,
	arg1 inputs.EnvironmentPolicyOwnershipInput,
) (*results.EnvironmentPolicyReceipt, error) {
	if m.VMToFloatingIPTrafficPolicyEnsureFunc == nil {
		return nil, fmt.Errorf("unexpected VMToFloatingIPTrafficPolicyEnsure request")
	}
	return m.VMToFloatingIPTrafficPolicyEnsureFunc(ctx, arg0, arg1)
}

func (m *MockEnvironmentFloatingIPAPI) VMToFloatingIPTrafficPolicyRemoveOwned(
	ctx context.Context,
	arg0 inputs.VMToFloatingIPTrafficPolicyCreateInput,
	arg1 inputs.EnvironmentPolicyOwnershipInput,
) error {
	if m.VMToFloatingIPTrafficPolicyRemoveOwnedFunc == nil {
		return fmt.Errorf("unexpected VMToFloatingIPTrafficPolicyRemoveOwned request")
	}
	return m.VMToFloatingIPTrafficPolicyRemoveOwnedFunc(ctx, arg0, arg1)
}

func (m *MockEnvironmentFloatingIPAPI) NetworkToFloatingIPTrafficPolicyEnsure(
	ctx context.Context,
	arg0 inputs.NetworkToFloatingIPTrafficPolicyCreateInput,
	arg1 inputs.EnvironmentPolicyOwnershipInput,
) (*results.EnvironmentPolicyReceipt, error) {
	if m.NetworkToFloatingIPTrafficPolicyEnsureFunc == nil {
		return nil, fmt.Errorf("unexpected NetworkToFloatingIPTrafficPolicyEnsure request")
	}
	return m.NetworkToFloatingIPTrafficPolicyEnsureFunc(ctx, arg0, arg1)
}

func (m *MockEnvironmentFloatingIPAPI) NetworkToFloatingIPTrafficPolicyRemoveOwned(
	ctx context.Context,
	arg0 inputs.NetworkToFloatingIPTrafficPolicyCreateInput,
	arg1 inputs.EnvironmentPolicyOwnershipInput,
) error {
	if m.NetworkToFloatingIPTrafficPolicyRemoveOwnedFunc == nil {
		return fmt.Errorf("unexpected NetworkToFloatingIPTrafficPolicyRemoveOwned request")
	}
	return m.NetworkToFloatingIPTrafficPolicyRemoveOwnedFunc(ctx, arg0, arg1)
}

func (m *MockEnvironmentFloatingIPAPI) FloatingIPDelegationGrant(
	ctx context.Context,
	input inputs.FloatingIPDelegationGrantInput,
) (*model.FloatingIPDelegationItem, error) {
	if m.FloatingIPDelegationGrantFunc == nil {
		return nil, fmt.Errorf("unexpected FloatingIPDelegationGrant request")
	}
	return m.FloatingIPDelegationGrantFunc(ctx, input)
}

func (m *MockEnvironmentFloatingIPAPI) FloatingIPDelegationList(
	ctx context.Context,
	input inputs.FloatingIPInput,
) ([]*model.FloatingIPDelegationItem, error) {
	if m.FloatingIPDelegationListFunc == nil {
		return nil, fmt.Errorf("unexpected FloatingIPDelegationList request")
	}
	return m.FloatingIPDelegationListFunc(ctx, input)
}

func (m *MockEnvironmentFloatingIPAPI) FloatingIPDelegationRevoke(
	ctx context.Context,
	input inputs.FloatingIPDelegationInput,
) (*model.FloatingIPDelegationItem, error) {
	if m.FloatingIPDelegationRevokeFunc == nil {
		return nil, fmt.Errorf("unexpected FloatingIPDelegationRevoke request")
	}
	return m.FloatingIPDelegationRevokeFunc(ctx, input)
}

func (m *MockEnvironmentFloatingIPAPI) FloatingIPAssociate(
	ctx context.Context,
	input inputs.FloatingIPAssociateInput,
) (*model.FloatingIPItem, error) {
	if m.FloatingIPAssociateFunc == nil {
		return nil, fmt.Errorf("unexpected FloatingIPAssociate request")
	}
	return m.FloatingIPAssociateFunc(ctx, input)
}

func (m *MockEnvironmentFloatingIPAPI) FloatingIPTransfer(
	ctx context.Context,
	input inputs.FloatingIPTransferInput,
) (*model.FloatingIPItem, error) {
	if m.FloatingIPTransferFunc == nil {
		return nil, fmt.Errorf("unexpected FloatingIPTransfer request")
	}
	return m.FloatingIPTransferFunc(ctx, input)
}

func (m *MockEnvironmentFloatingIPAPI) FloatingIPRelease(
	ctx context.Context,
	input inputs.FloatingIPReleaseInput,
) (*model.FloatingIPItem, error) {
	if m.FloatingIPReleaseFunc == nil {
		return nil, fmt.Errorf("unexpected FloatingIPRelease request")
	}
	return m.FloatingIPReleaseFunc(ctx, input)
}

func (m *MockEnvironmentFloatingIPAPI) FloatingIPInspect(
	ctx context.Context,
	input inputs.FloatingIPInput,
) (*model.FloatingIPItem, error) {
	if m.FloatingIPInspectFunc == nil {
		return nil, fmt.Errorf("unexpected FloatingIPInspect request")
	}
	return m.FloatingIPInspectFunc(ctx, input)
}
