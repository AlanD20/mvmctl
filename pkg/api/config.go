// Package api provides the public orchestration layer for all operations.
// Matches src/mvmctl/api/config_operations.py exactly.
package api

import (
	"context"
	"fmt"

	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

// ConfigAPI defines the public interface for config operations.
type ConfigAPI interface {
	ConfigGet(ctx context.Context, category, key string) (any, error)
	ConfigSet(ctx context.Context, category, key string, value any) error
	ConfigReset(ctx context.Context, category, key string, allOverrides bool) (int, error)
	ConfigListAll(ctx context.Context) (map[string]map[string]model.SettingInfo, error)
}

// ConfigGet returns a config value for category and optional key.
// Matches Python's ConfigOperation.get() exactly — uses ConfigInput/ConfigRequest pipeline
// with OVERRIDABLE_SETTINGS validation.
// Returns the raw config value (type varies by setting: string, int, bool, etc.)
// or map[string]map[string]model.SettingInfo when key is empty (category listing).
func (op *Operation) ConfigGet(ctx context.Context, category, key string) (any, error) {

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
		return nil, errs.New(errs.Code("config.get.missing_category"), "Category is required for config get operation.")
	}

	if resolved.Key == "" {
		return op.Services.Config.ListByCategory(ctx, resolved.Category)
	}

	return op.Services.Config.GetValue(ctx, resolved.Category, resolved.Key)
}

// ConfigSet sets a config value for category.key.
// Matches Python's ConfigOperation.set() exactly — ConfigError propagates from
// SettingsService.set() just as in Python (error is returned directly).
func (op *Operation) ConfigSet(
	ctx context.Context,
	category, key string,
	value any,
) error {
	rawInput := inputs.ConfigInput{
		Action:   "set",
		Category: category,
		Key:      key,
		Value:    value,
	}
	req := inputs.NewConfigRequest(rawInput)
	resolved, err := req.Resolve(ctx)
	if err != nil {
		return err
	}

	if err := op.Services.Config.Set(ctx, resolved.Category, resolved.Key, resolved.Value); err != nil {
		return err
	}

	op.AuditLog.LogOperation(
		"config.set",
		map[string]any{
			"category": resolved.Category,
			"key":      resolved.Key,
			"value":    resolved.Value,
		},
		"",
	)

	return nil
}

// ConfigReset resets a config value to its default (removes override).
// Matches Python's ConfigOperation.reset() exactly — uses ConfigRequest resolution pipeline.
func (op *Operation) ConfigReset(ctx context.Context, category, key string, allOverrides bool) (int, error) {
	rawInput := inputs.ConfigInput{
		Action:       "reset",
		Category:     category,
		Key:          key,
		AllOverrides: allOverrides,
	}
	req := inputs.NewConfigRequest(rawInput)
	resolved, err := req.Resolve(ctx)
	if err != nil {
		return 0, errs.WrapMsg(errs.CodeConfigError, err.Error(), err, errs.WithClass(errs.ClassValidation))
	}

	if resolved.AllOverrides {
		deleted, err := op.Services.Config.DeleteAll(ctx)
		if err != nil {
			return 0, errs.WrapMsg(errs.CodeConfigError, err.Error(), err, errs.WithClass(errs.ClassInternal))
		}
		if deleted > 0 {
			op.AuditLog.LogOperation("config.reset", nil, fmt.Sprintf("all overrides (%d removed)", deleted))
		}
		return deleted, nil
	}

	if resolved.Key == "" {
		deleted, err := op.Services.Config.DeleteByCategory(ctx, resolved.Category)
		if err != nil {
			return 0, errs.WrapMsg(errs.CodeConfigError, err.Error(), err, errs.WithClass(errs.ClassInternal))
		}
		if deleted > 0 {
			op.AuditLog.LogOperation("config.reset", nil, fmt.Sprintf("%s.* (%d removed)", resolved.Category, deleted))
		}
		return deleted, nil
	}

	deletedBool, err := op.Services.Config.Delete(ctx, resolved.Category, resolved.Key)
	if err != nil {
		return 0, errs.WrapMsg(errs.CodeConfigError, err.Error(), err, errs.WithClass(errs.ClassInternal))
	}
	resultCount := 0
	if deletedBool {
		resultCount = 1
		op.AuditLog.LogOperation("config.reset", nil, fmt.Sprintf("%s.%s", resolved.Category, resolved.Key))
	}
	return resultCount, nil
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
