package config

import (
	"context"
	"fmt"
	"time"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/model"
	"mvmctl/pkg/errs"
)

// Service matches the Python mvmctl.core.config._service.SettingsService.
// Handles type coercion, cross-key constraint validation, and DB persistence.
type Service struct {
	repo        SettingsRepository
	constraints *ConstraintRegistry
}

// NewService creates a new Service with the given repo and constraint registry.
func NewService(repo SettingsRepository, constraints *ConstraintRegistry) *Service {
	return &Service{
		repo:        repo,
		constraints: constraints,
	}
}

// Set coerces the value, validates constraints, and persists.
// Matches Python: set() -> coerce + check_constraints + repo.set().
//
// Error message when key is not overridable matches Python exactly:
//
//	"'{category}.{key}' is not an overridable setting. Use 'mvm config ls' to see valid keys."
func (s *Service) Set(ctx context.Context, category, key string, value any) error {
	expected := GetExpectedType(category, key)
	if expected == "" {
		return errs.New(errs.CodeConfigError,
			"'"+category+"."+key+"' is not an overridable setting. Use 'mvm config ls' to see valid keys.")
	}

	coerced, err := infra.Coerce(value, expected)
	if err != nil {
		return err
	}

	if err := s.checkConstraints(ctx, category, key, coerced); err != nil {
		return err
	}

	return s.repo.Set(ctx, category, key, coerced)
}

// GetString resolves a config value as a string using GetValue, then casts.
// Returns error if the key has no default in OverridableDefaults.
func (s *Service) GetString(ctx context.Context, category, key string) (string, error) {
	v, err := s.GetValue(ctx, category, key)
	if err != nil {
		return "", err
	}
	if v == nil {
		return "", fmt.Errorf("config key %s.%s not found", category, key)
	}
	return infra.ToString(v, ""), nil
}

// GetInt resolves a config value as an int using GetValue, then casts.
// Returns error if the key has no default in OverridableDefaults.
func (s *Service) GetInt(ctx context.Context, category, key string) (int, error) {
	v, err := s.GetValue(ctx, category, key)
	if err != nil {
		return 0, err
	}
	if v == nil {
		return 0, fmt.Errorf("config key %s.%s not found", category, key)
	}
	return infra.ToInt(v, 0), nil
}

// GetBool resolves a config value as a bool using GetValue, then casts.
// Returns error if the key has no default in OverridableDefaults.
func (s *Service) GetBool(ctx context.Context, category, key string) (bool, error) {
	v, err := s.GetValue(ctx, category, key)
	if err != nil {
		return false, err
	}
	if v == nil {
		return false, fmt.Errorf("config key %s.%s not found", category, key)
	}
	return infra.ToBool(v, false), nil
}

// GetDuration resolves a config value as a time.Duration using GetValue, then parses.
// Returns error if the key has no default in OverridableDefaults.
func (s *Service) GetDuration(ctx context.Context, category, key string) (time.Duration, error) {
	v, err := s.GetValue(ctx, category, key)
	if err != nil {
		return 0, err
	}
	if v == nil {
		return 0, fmt.Errorf("config key %s.%s not found", category, key)
	}
	switch val := v.(type) {
	case time.Duration:
		return val, nil
	case string:
		d, err := time.ParseDuration(val)
		if err != nil {
			return 0, fmt.Errorf("config key %s.%s: cannot parse duration %q: %w", category, key, val, err)
		}
		return d, nil
	case int:
		return time.Duration(val) * time.Second, nil
	case int64:
		return time.Duration(val) * time.Second, nil
	default:
		return 0, fmt.Errorf("config key %s.%s: unexpected type %T for duration", category, key, v)
	}
}

// Delete removes a setting override. Returns true if a row was deleted.
// Matches Python: delete() -> repo.delete().
func (s *Service) Delete(ctx context.Context, category, key string) (bool, error) {
	return s.repo.Delete(ctx, category, key)
}

// DeleteByCategory removes all overrides in a category after validating it exists.
// Matches Python: validates category exists, then repo.delete_by_category().
func (s *Service) DeleteByCategory(ctx context.Context, category string) (int, error) {
	if _, ok := OverridableSettings[category]; !ok {
		return 0, errs.New(errs.CodeConfigError,
			"'"+category+"' is not a valid setting category. Use 'mvm config ls' to see valid categories.")
	}
	return s.repo.DeleteByCategory(ctx, category)
}

// DeleteAll removes ALL user overrides.
// Matches Python: delete_all() -> repo.delete_all().
func (s *Service) DeleteAll(ctx context.Context) (int, error) {
	return s.repo.DeleteAll(ctx)
}

// ListByCategory returns all keys in a category with type, default, and override info.
// Matches Python: dict with "type", "default", "override" per key.
// Python returns: {key: {"type": expected_type.__name__, "override": override, "default": default}}
// Go matches this exactly with model.SettingInfo{Type, Default, Override}.
func (s *Service) ListByCategory(ctx context.Context, category string) (map[string]model.SettingInfo, error) {
	if _, ok := OverridableSettings[category]; !ok {
		return nil, errs.New(errs.CodeConfigError,
			"'"+category+"' is not a valid setting category. Use 'mvm config ls' to see valid categories.")
	}

	// Python: overrides = self._repo.list_by_category(category)
	// Even with a category filter, Python returns {category: {key: value}} (nested).
	cat := category
	overrides, err := s.repo.ListByCategory(ctx, &cat)
	if err != nil {
		return nil, err
	}

	result := make(map[string]model.SettingInfo, len(OverridableSettings[category]))
	catOverrides := overrides[category]
	if catOverrides == nil {
		catOverrides = make(map[string]any)
	}
	for key, expectedType := range OverridableSettings[category] {
		defaultVal, _ := infra.GetDefault(category, key)
		override := catOverrides[key] // nil if not present
		result[key] = model.SettingInfo{
			Type:     expectedType,
			Default:  defaultVal,
			Override: override,
		}
	}
	return result, nil
}

// ListAll returns all overridable settings across all categories.
// Matches Python: list_all() -> dict of {category: {key: {type, default, override}}}.
func (s *Service) ListAll(ctx context.Context) (map[string]map[string]model.SettingInfo, error) {
	overrides, err := s.repo.ListByCategory(ctx, nil)
	if err != nil {
		return nil, err
	}

	result := make(map[string]map[string]model.SettingInfo, len(OverridableSettings))
	for category, keys := range OverridableSettings {
		catResult := make(map[string]model.SettingInfo, len(keys))
		catOverrides := overrides[category]
		if catOverrides == nil {
			catOverrides = make(map[string]any)
		}
		for key, expectedType := range keys {
			defaultVal, _ := infra.GetDefault(category, key)
			override := catOverrides[key] // nil if not present
			catResult[key] = model.SettingInfo{
				Type:     expectedType,
				Default:  defaultVal,
				Override: override,
			}
		}
		result[category] = catResult
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// checkConstraints validates cross-key constraints before writing.
// Matches Python: _check_constraints(category, key, new_value).
func (s *Service) checkConstraints(ctx context.Context, category, key string, newValue any) error {
	constraints := s.constraints.Get(category, key)
	if len(constraints) == 0 {
		return nil
	}

	resolve := func(otherKey string, otherCategory ...string) (any, error) {
		cat := category
		if len(otherCategory) > 0 {
			cat = otherCategory[0]
		}
		if otherKey == key && cat == category {
			return newValue, nil
		}
		return s.GetValue(ctx, cat, otherKey)
	}

	for _, constraint := range constraints {
		if err := constraint(key, resolve); err != nil {
			return err
		}
	}
	return nil
}

// GetValue returns the effective value for a setting:
// DB override (coerced) or hardcoded default.
// Matches Python: _get_active_value(category, key).
func (s *Service) GetValue(ctx context.Context, category, key string) (any, error) {
	override, err := s.repo.Get(ctx, category, key)
	if err != nil {
		return nil, err
	}
	if override != nil {
		expected := GetExpectedType(category, key)
		if expected != "" {
			return infra.Coerce(override, expected)
		}
		return override, nil
	}
	def, gdErr := infra.GetDefault(category, key)
	if gdErr != nil {
		// Python: bare KeyError propagates through — no wrapping in MVMError.
		// Go: propagate the plain error as-is, no DomainError wrapping.
		return nil, gdErr
	}
	return def, nil
}
