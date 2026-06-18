package testutil

import (
	"context"

	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
)

// MockNetworkAPI implements api.NetworkAPI for testing.
type MockNetworkAPI struct {
	NetworkCreateFunc               func(ctx context.Context, input inputs.NetworkCreateInput) (*model.NetworkItem, error)
	NetworkRemoveFunc               func(ctx context.Context, input inputs.NetworkInput, force bool) error
	NetworkListAllFunc              func(ctx context.Context) ([]*model.NetworkItem, error)
	NetworkGetFunc                  func(ctx context.Context, input inputs.NetworkInput) (*model.NetworkItem, error)
	NetworkToJSONFunc               func(networks []*model.NetworkItem) []map[string]any
	NetworkInspectFunc              func(ctx context.Context, input inputs.NetworkInput) (*results.NetworkInspect, error)
	NetworkSetDefaultFunc           func(ctx context.Context, input inputs.NetworkInput) error
	NetworkSyncFunc                 func(ctx context.Context, input inputs.NetworkInput) (map[string]map[string]int, error)
	NetworkPruneFunc                func(ctx context.Context, dryRun bool, includeAll bool) ([]string, error)
	NetworkCreateDefaultNetworkFunc func(ctx context.Context) (*model.NetworkItem, error)
}

func (m *MockNetworkAPI) NetworkCreate(ctx context.Context, input inputs.NetworkCreateInput) (*model.NetworkItem, error) {
	if m.NetworkCreateFunc != nil {
		return m.NetworkCreateFunc(ctx, input)
	}
	return nil, nil
}

func (m *MockNetworkAPI) NetworkRemove(ctx context.Context, input inputs.NetworkInput, force bool) error {
	if m.NetworkRemoveFunc != nil {
		return m.NetworkRemoveFunc(ctx, input, force)
	}
	return nil
}

func (m *MockNetworkAPI) NetworkListAll(ctx context.Context) ([]*model.NetworkItem, error) {
	if m.NetworkListAllFunc != nil {
		return m.NetworkListAllFunc(ctx)
	}
	return nil, nil
}

func (m *MockNetworkAPI) NetworkGet(ctx context.Context, input inputs.NetworkInput) (*model.NetworkItem, error) {
	if m.NetworkGetFunc != nil {
		return m.NetworkGetFunc(ctx, input)
	}
	return nil, nil
}

func (m *MockNetworkAPI) NetworkToJSON(networks []*model.NetworkItem) []map[string]any {
	if m.NetworkToJSONFunc != nil {
		return m.NetworkToJSONFunc(networks)
	}
	return nil
}

func (m *MockNetworkAPI) NetworkInspect(
	ctx context.Context,
	input inputs.NetworkInput,
) (*results.NetworkInspect, error) {
	if m.NetworkInspectFunc != nil {
		return m.NetworkInspectFunc(ctx, input)
	}
	return nil, nil
}

func (m *MockNetworkAPI) NetworkSetDefault(ctx context.Context, input inputs.NetworkInput) error {
	if m.NetworkSetDefaultFunc != nil {
		return m.NetworkSetDefaultFunc(ctx, input)
	}
	return nil
}

func (m *MockNetworkAPI) NetworkSync(
	ctx context.Context,
	input inputs.NetworkInput,
) (map[string]map[string]int, error) {
	if m.NetworkSyncFunc != nil {
		return m.NetworkSyncFunc(ctx, input)
	}
	return nil, nil
}

func (m *MockNetworkAPI) NetworkPrune(ctx context.Context, dryRun bool, includeAll bool) ([]string, error) {
	if m.NetworkPruneFunc != nil {
		return m.NetworkPruneFunc(ctx, dryRun, includeAll)
	}
	return nil, nil
}

func (m *MockNetworkAPI) NetworkCreateDefaultNetwork(ctx context.Context) (*model.NetworkItem, error) {
	if m.NetworkCreateDefaultNetworkFunc != nil {
		return m.NetworkCreateDefaultNetworkFunc(ctx)
	}
	return nil, nil
}
