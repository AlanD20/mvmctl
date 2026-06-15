package testutil

import (
	"context"

	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
	"mvmctl/pkg/errs"
)

// MockKeyAPI implements api.KeyAPI for testing.
type MockKeyAPI struct {
	KeyListAllFunc      func(ctx context.Context) ([]*model.SSHKeyItem, error)
	KeyGetFunc          func(ctx context.Context, input inputs.KeyInput) (*model.SSHKeyItem, error)
	KeyCreateFunc       func(ctx context.Context, input inputs.KeyCreateInput) (*model.SSHKeyItem, error)
	KeyImportFunc       func(ctx context.Context, input inputs.KeyImportInput) (*model.SSHKeyItem, error)
	KeyRemoveFunc       func(ctx context.Context, input inputs.KeyInput, force bool) *errs.BatchResult
	KeyInspectFunc      func(ctx context.Context, input inputs.KeyInput) (*results.KeyInspect, error)
	KeyExportFunc       func(ctx context.Context, input inputs.KeyInput, destination string, overwrite bool) ([]string, error)
	KeySetDefaultsFunc  func(ctx context.Context, input inputs.KeyInput) error
	KeyGetDefaultsFunc  func(ctx context.Context) ([]*model.SSHKeyItem, error)
	KeyClearDefaultsFunc func(ctx context.Context) error
}

func (m *MockKeyAPI) KeyListAll(ctx context.Context) ([]*model.SSHKeyItem, error) {
	if m.KeyListAllFunc != nil {
		return m.KeyListAllFunc(ctx)
	}
	return nil, nil
}

func (m *MockKeyAPI) KeyGet(ctx context.Context, input inputs.KeyInput) (*model.SSHKeyItem, error) {
	if m.KeyGetFunc != nil {
		return m.KeyGetFunc(ctx, input)
	}
	return nil, nil
}

func (m *MockKeyAPI) KeyCreate(ctx context.Context, input inputs.KeyCreateInput) (*model.SSHKeyItem, error) {
	if m.KeyCreateFunc != nil {
		return m.KeyCreateFunc(ctx, input)
	}
	return nil, nil
}

func (m *MockKeyAPI) KeyImport(ctx context.Context, input inputs.KeyImportInput) (*model.SSHKeyItem, error) {
	if m.KeyImportFunc != nil {
		return m.KeyImportFunc(ctx, input)
	}
	return nil, nil
}

func (m *MockKeyAPI) KeyRemove(ctx context.Context, input inputs.KeyInput, force bool) *errs.BatchResult {
	if m.KeyRemoveFunc != nil {
		return m.KeyRemoveFunc(ctx, input, force)
	}
	return nil
}

func (m *MockKeyAPI) KeyInspect(ctx context.Context, input inputs.KeyInput) (*results.KeyInspect, error) {
	if m.KeyInspectFunc != nil {
		return m.KeyInspectFunc(ctx, input)
	}
	return nil, nil
}

func (m *MockKeyAPI) KeyExport(ctx context.Context, input inputs.KeyInput, destination string, overwrite bool) ([]string, error) {
	if m.KeyExportFunc != nil {
		return m.KeyExportFunc(ctx, input, destination, overwrite)
	}
	return nil, nil
}

func (m *MockKeyAPI) KeySetDefaults(ctx context.Context, input inputs.KeyInput) error {
	if m.KeySetDefaultsFunc != nil {
		return m.KeySetDefaultsFunc(ctx, input)
	}
	return nil
}

func (m *MockKeyAPI) KeyGetDefaults(ctx context.Context) ([]*model.SSHKeyItem, error) {
	if m.KeyGetDefaultsFunc != nil {
		return m.KeyGetDefaultsFunc(ctx)
	}
	return nil, nil
}

func (m *MockKeyAPI) KeyClearDefaults(ctx context.Context) error {
	if m.KeyClearDefaultsFunc != nil {
		return m.KeyClearDefaultsFunc(ctx)
	}
	return nil
	}
