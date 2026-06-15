package testutil

import (
	"context"

	"mvmctl/internal/lib/model"
)

// MockConfigAPI implements api.ConfigAPI for testing.
type MockConfigAPI struct {
	ConfigGetFunc    func(ctx context.Context, category string, key string) (any, error)
	ConfigSetFunc    func(ctx context.Context, category string, key string, value any) error
	ConfigResetFunc  func(ctx context.Context, category string, key string, allOverrides bool) (int, error)
	ConfigListAllFunc func(ctx context.Context) (map[string]map[string]model.SettingInfo, error)
}

func (m *MockConfigAPI) ConfigGet(ctx context.Context, category string, key string) (any, error) {
	if m.ConfigGetFunc != nil {
		return m.ConfigGetFunc(ctx, category, key)
	}
	return nil, nil
}

func (m *MockConfigAPI) ConfigSet(ctx context.Context, category string, key string, value any) error {
	if m.ConfigSetFunc != nil {
		return m.ConfigSetFunc(ctx, category, key, value)
	}
	return nil
}

func (m *MockConfigAPI) ConfigReset(ctx context.Context, category string, key string, allOverrides bool) (int, error) {
	if m.ConfigResetFunc != nil {
		return m.ConfigResetFunc(ctx, category, key, allOverrides)
	}
	return 0, nil
}

func (m *MockConfigAPI) ConfigListAll(ctx context.Context) (map[string]map[string]model.SettingInfo, error) {
	if m.ConfigListAllFunc != nil {
		return m.ConfigListAllFunc(ctx)
	}
	return nil, nil
	}
