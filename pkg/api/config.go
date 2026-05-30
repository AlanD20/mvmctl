// Package api provides the public orchestration layer for all operations.
// Matches src/mvmctl/api/config_operations.py exactly.
package api

import (
	"context"
	"fmt"

	"mvmctl/internal/core/config"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/logging"
	"mvmctl/internal/infra/model"
	"mvmctl/pkg/api/inputs"
)

// ConfigGet returns a config value for category and optional key.
// Matches Python's ConfigOperation.get() exactly — uses ConfigInput/ConfigRequest pipeline
// with OVERRIDABLE_SETTINGS validation.
// Returns the raw config value (type varies by setting: string, int, bool, etc.)
// or []config.SettingInfo when key is empty (category listing).
func (op *Operation) ConfigGet(ctx context.Context, category, key string) (interface{}, error) {

	rawInput := inputs.ConfigInput{
		Action:   "get",
		Category: category,
		Key:      key,
	}
	req := inputs.NewConfigRequest(rawInput)
	resolved, err := req.Resolve(ctx)
	if err != nil {
		return nil, err
	}

	if resolved.Category == "" {
		return nil, &errs.DomainError{
			Code:    "config.get.missing_category",
			Op:      "config",
			Message: "Category is required for config get operation.",
			Class:   errs.ClassValidation,
		}
	}

	if resolved.Key == "" {
		return op.Services.Config.ListByCategory(ctx, resolved.Category)
	}

	return config.Resolve(ctx, op.DB, resolved.Category, resolved.Key)
}

// ConfigSet sets a config value for category.key.
// Matches Python's ConfigOperation.set() exactly — ConfigError propagates from
// SettingsService.set() just as in Python (error is returned as second return value
// instead of wrapped in OperationResult).
func (op *Operation) ConfigSet(ctx context.Context, category, key string, value interface{}) (*errs.OperationResult, error) {
	rawInput := inputs.ConfigInput{
		Action:   "set",
		Category: category,
		Key:      key,
		Value:    value,
	}
	req := inputs.NewConfigRequest(rawInput)
	resolved, err := req.Resolve(ctx)
	if err != nil {
		return nil, err
	}

	if err := op.Services.Config.Set(ctx, resolved.Category, resolved.Key, resolved.Value); err != nil {
		return nil, err
	}

	auditLog := logging.NewAuditLog(op.CacheDir)
	_ = auditLog.LogOperation("config.set", nil, fmt.Sprintf("%s.%s=%v", resolved.Category, resolved.Key, resolved.Value))

	return &errs.OperationResult{
		Status:  "success",
		Code:    "config.set",
		Message: fmt.Sprintf("Set %s.%s = %v", resolved.Category, resolved.Key, resolved.Value),
	}, nil
}

// ConfigReset resets a config value to its default (removes override).
// Matches Python's ConfigOperation.reset() exactly — uses ConfigRequest resolution pipeline.
func (op *Operation) ConfigReset(ctx context.Context, category, key string, allOverrides bool) *errs.OperationResult {
	rawInput := inputs.ConfigInput{
		Action:       "reset",
		Category:     category,
		Key:          key,
		AllOverrides: allOverrides,
	}
	req := inputs.NewConfigRequest(rawInput)
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
		deleted, err := op.Services.Config.DeleteAll(ctx)
		if err != nil {
			return &errs.OperationResult{
				Status:    "error",
				Code:      string(errs.CodeConfigError),
				Message:   err.Error(),
				Exception: err,
			}
		}
		if deleted > 0 {
			auditLog := logging.NewAuditLog(op.CacheDir)
			_ = auditLog.LogOperation("config.reset", nil, fmt.Sprintf("all overrides (%d removed)", deleted))
		}
		return &errs.OperationResult{
			Status:  "success",
			Code:    "config.reset",
			Message: fmt.Sprintf("Reset %d override(s) globally", deleted),
			Item:    deleted,
		}
	}

	if resolved.Key == "" {
		deleted, err := op.Services.Config.DeleteByCategory(ctx, resolved.Category)
		if err != nil {
			return &errs.OperationResult{
				Status:    "error",
				Code:      string(errs.CodeConfigError),
				Message:   err.Error(),
				Exception: err,
			}
		}
		if deleted > 0 {
			auditLog := logging.NewAuditLog(op.CacheDir)
			_ = auditLog.LogOperation("config.reset", nil, fmt.Sprintf("%s.* (%d removed)", resolved.Category, deleted))
		}
		return &errs.OperationResult{
			Status:  "success",
			Code:    "config.reset",
			Message: fmt.Sprintf("Reset %d override(s) in %s", deleted, resolved.Category),
			Item:    deleted,
		}
	}

	deletedBool, err := op.Services.Config.Delete(ctx, resolved.Category, resolved.Key)
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
		auditLog := logging.NewAuditLog(op.CacheDir)
		_ = auditLog.LogOperation("config.reset", nil, fmt.Sprintf("%s.%s", resolved.Category, resolved.Key))
	}
	return &errs.OperationResult{
		Status:  "success",
		Code:    "config.reset",
		Message: fmt.Sprintf("Reset %s.%s (%d override(s))", resolved.Category, resolved.Key, resultCount),
		Item:    resultCount,
	}
}

// ConfigListAll returns all overridable settings.
// Matches Python's ConfigOperation.list_all() exactly — uses ConfigRequest resolution pipeline.
func (op *Operation) ConfigListAll(ctx context.Context) (map[string]map[string]model.SettingInfo, error) {
	// Python: inputs = ConfigInput(action="list")
	//         resolved = ConfigRequest(inputs=inputs, db=Database()).resolve()
	rawInput := inputs.ConfigInput{
		Action: "list",
	}
	req := inputs.NewConfigRequest(rawInput)
	_, err := req.Resolve(ctx)
	if err != nil {
		return nil, err
	}
	return op.Services.Config.ListAll(ctx)
}
