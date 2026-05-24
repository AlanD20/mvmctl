// Package api provides the public orchestration layer for all operations.
// Matches src/mvmctl/api/config_operations.py exactly.
package api

import (
	"context"
	"database/sql"
	"fmt"

	"mvmctl/internal/core/config"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
	"mvmctl/pkg/api/inputs"
)

// ConfigOperation provides config settings orchestration.
// Matches Python's ConfigOperation exactly.
type ConfigOperation struct {
	svc      *config.Service
	repo     config.SettingsRepository
	db       *sql.DB
	cacheDir string
}

// NewConfigOperation creates a ConfigOperation.
func NewConfigOperation(svc *config.Service, repo config.SettingsRepository, db *sql.DB, cacheDir string) *ConfigOperation {
	return &ConfigOperation{
		svc:      svc,
		repo:     repo,
		db:       db,
		cacheDir: cacheDir,
	}
}

// Get returns a config value for category and optional key.
// Matches Python's ConfigOperation.get() exactly — uses ConfigInput/ConfigRequest pipeline
// with OVERRIDABLE_SETTINGS validation.
// Returns the raw config value (type varies by setting: string, int, bool, etc.)
// or []config.SettingInfo when key is empty (category listing).
func (o *ConfigOperation) Get(ctx context.Context, category, key string) (interface{}, error) {
	cat := &category
	var keyPtr *string
	if key != "" {
		keyPtr = &key
	}

	rawInput := inputs.ConfigInput{
		Action:   "get",
		Category: cat,
		Key:      keyPtr,
	}
	req := inputs.NewConfigRequest(rawInput, o.db)
	resolved, err := req.Resolve(ctx)
	if err != nil {
		return nil, err
	}

	// Python: cat = resolved.category
	//         if cat is None:
	//             raise ConfigError("Category is required for config get operation.",
	//                               code="config.get.missing_category")
	if resolved.Category == nil {
		return nil, &errs.DomainError{
			Code:    "config.get.missing_category",
			Op:      "config",
			Message: "Category is required for config get operation.",
			Class:   errs.ClassValidation,
		}
	}

	if resolved.Key == nil {
		// Python: return resolved.service.list_by_category(cat)
		return o.svc.ListByCategory(ctx, *resolved.Category)
	}

	// Python: return SettingsService.resolve(db, cat, resolved.key)
	return config.Resolve(ctx, o.db, *resolved.Category, *resolved.Key)
}

// Set sets a config value for category.key.
// Matches Python's ConfigOperation.set() exactly — ConfigError propagates from
// SettingsService.set() just as in Python (error is returned as second return value
// instead of wrapped in OperationResult).
func (o *ConfigOperation) Set(ctx context.Context, category, key string, value interface{}) (*errs.OperationResult, error) {
	cat := &category
	keyPtr := &key

	rawInput := inputs.ConfigInput{
		Action:   "set",
		Category: cat,
		Key:      keyPtr,
		Value:    value,
	}
	req := inputs.NewConfigRequest(rawInput, o.db)
	resolved, err := req.Resolve(ctx)
	if err != nil {
		return nil, err // ConfigError propagates, matching Python
	}

	if err := o.svc.Set(ctx, *resolved.Category, *resolved.Key, resolved.Value); err != nil {
		return nil, err // ConfigError propagates, matching Python
	}

	auditLog := infra.NewAuditLog(o.cacheDir)
	_ = auditLog.LogOperation("config.set", nil, fmt.Sprintf("%s.%s=%v", *resolved.Category, *resolved.Key, resolved.Value))

	return &errs.OperationResult{
		Status:  "success",
		Code:    "config.set",
		Message: fmt.Sprintf("Set %s.%s = %v", *resolved.Category, *resolved.Key, resolved.Value),
	}, nil
}

// Reset resets a config value to its default (removes override).
// Matches Python's ConfigOperation.reset() exactly — uses ConfigRequest resolution pipeline.
func (o *ConfigOperation) Reset(ctx context.Context, category, key string, allOverrides bool) *errs.OperationResult {
	// Python: inputs = ConfigInput(action="reset", ...)
	//         resolved = ConfigRequest(inputs=inputs, db=Database()).resolve()
	var cat *string
	if category != "" {
		cat = &category
	}
	var keyPtr *string
	if key != "" {
		keyPtr = &key
	}
	rawInput := inputs.ConfigInput{
		Action:       "reset",
		Category:     cat,
		Key:          keyPtr,
		AllOverrides: allOverrides,
	}
	req := inputs.NewConfigRequest(rawInput, o.db)
	resolved, err := req.Resolve(ctx)
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      string(errs.CodeConfigError),
			Message:   err.Error(),
			Exception: err,
		}
	}

	if resolved.AllOverrides {
		deleted, err := o.svc.DeleteAll(ctx)
		if err != nil {
			return &errs.OperationResult{
				Status:    "error",
				Code:      string(errs.CodeConfigError),
				Message:   err.Error(),
				Exception: err,
			}
		}
		if deleted > 0 {
			auditLog := infra.NewAuditLog(o.cacheDir)
			_ = auditLog.LogOperation("config.reset", nil, fmt.Sprintf("all overrides (%d removed)", deleted))
		}
		return &errs.OperationResult{
			Status:  "success",
			Code:    "config.reset",
			Message: fmt.Sprintf("Reset %d override(s) globally", deleted),
			Item:    deleted,
		}
	}

	if resolved.Key == nil {
		deleted, err := o.svc.DeleteByCategory(ctx, *resolved.Category)
		if err != nil {
			return &errs.OperationResult{
				Status:    "error",
				Code:      string(errs.CodeConfigError),
				Message:   err.Error(),
				Exception: err,
			}
		}
		if deleted > 0 {
			auditLog := infra.NewAuditLog(o.cacheDir)
			_ = auditLog.LogOperation("config.reset", nil, fmt.Sprintf("%s.* (%d removed)", *resolved.Category, deleted))
		}
		return &errs.OperationResult{
			Status:  "success",
			Code:    "config.reset",
			Message: fmt.Sprintf("Reset %d override(s) in %s", deleted, *resolved.Category),
			Item:    deleted,
		}
	}

	deletedBool, err := o.svc.Delete(ctx, *resolved.Category, *resolved.Key)
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      string(errs.CodeConfigError),
			Message:   err.Error(),
			Exception: err,
		}
	}
	resultCount := 0
	if deletedBool {
		resultCount = 1
		auditLog := infra.NewAuditLog(o.cacheDir)
		_ = auditLog.LogOperation("config.reset", nil, fmt.Sprintf("%s.%s", *resolved.Category, *resolved.Key))
	}
	return &errs.OperationResult{
		Status:  "success",
		Code:    "config.reset",
		Message: fmt.Sprintf("Reset %s.%s (%d override(s))", *resolved.Category, *resolved.Key, resultCount),
		Item:    resultCount,
	}
}

// ListAll returns all overridable settings.
// Matches Python's ConfigOperation.list_all() exactly — uses ConfigRequest resolution pipeline.
func (o *ConfigOperation) ListAll(ctx context.Context) (map[string]map[string]model.SettingInfo, error) {
	// Python: inputs = ConfigInput(action="list")
	//         resolved = ConfigRequest(inputs=inputs, db=Database()).resolve()
	rawInput := inputs.ConfigInput{
		Action: "list",
	}
	req := inputs.NewConfigRequest(rawInput, o.db)
	_, err := req.Resolve(ctx)
	if err != nil {
		return nil, err
	}
	return o.svc.ListAll(ctx)
}
