package config

import (
	"context"
	"errors"

	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
)

// Service matches the Python mvmctl.core.config._service.SettingsService.
// Handles type coercion, cross-key constraint validation, and DB persistence.
type Service struct {
	repo SettingsRepository
}

// NewService creates a new Service with the given repo.
// Uses the package-level constraint registry (defaultConstraints) which has all
// built-in constraints auto-registered — matching Python's module-level singleton
// `constraints = ConstraintRegistry()` with built-in constraints registered at
// module load time (no parameterization in the constructor).
func NewService(repo SettingsRepository) *Service {
	return &Service{
		repo: repo,
	}
}

// Get returns the coerced value for a setting, or nil if not found.
// Matches Python: get() -> repo.get() + coerce.
// Python propagates TypeError from coerce() directly — Go does the same.
func (s *Service) Get(ctx context.Context, category, key string) (any, error) {
	value, err := s.repo.Get(ctx, category, key)
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, nil
	}
	expected := GetExpectedType(category, key)
	if expected != "" {
		// Python: return CommonUtils.coerce(value, expected_type) — TypeError propagates
		return Coerce(value, expected)
	}
	return value, nil
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
		return &errs.DomainError{
			Code:    errs.CodeConfigError,
			Message: "'" + category + "." + key + "' is not an overridable setting. Use 'mvm config ls' to see valid keys.",
			Op:      "config.set",
			Class:   errs.ClassValidation,
		}
	}

	// Python: coerced = CommonUtils.coerce(value, expected_type) — TypeError propagates
	// Go: propagate raw Coerce error without wrapping in DomainError
	coerced, err := Coerce(value, expected)
	if err != nil {
		// Python raises TypeError directly — don't wrap, let it propagate as-is.
		// But if it's already a DomainError (from a prior wrap), pass it through.
		var de *errs.DomainError
		if errors.As(err, &de) {
			return de
		}
		return err
	}

	if err := s.checkConstraints(ctx, category, key, coerced); err != nil {
		return err
	}

	return s.repo.Set(ctx, category, key, coerced)
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
		return 0, &errs.DomainError{
			Code:    errs.CodeConfigError,
			Message: "'" + category + "' is not a valid setting category. Use 'mvm config ls' to see valid categories.",
			Op:      "config.delete",
			Class:   errs.ClassValidation,
		}
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
		return nil, &errs.DomainError{
			Code:    errs.CodeConfigError,
			Message: "'" + category + "' is not a valid setting category. Use 'mvm config ls' to see valid categories.",
			Op:      "config.list",
			Class:   errs.ClassValidation,
		}
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
		defaultVal, _ := GetDefault(category, key)
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
			defaultVal, _ := GetDefault(category, key)
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
// Uses the package-level defaultConstraints singleton — Python accesses
// `_constraints.constraints` (module-level ConstraintRegistry instance) directly.
func (s *Service) checkConstraints(ctx context.Context, category, key string, newValue any) error {
	constraints := defaultConstraints.Get(category, key)
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
		return s.getActiveValue(ctx, cat, otherKey)
	}

	for _, constraint := range constraints {
		if err := constraint(key, resolve); err != nil {
			return err
		}
	}
	return nil
}

// getActiveValue returns the effective value for a setting:
// DB override (coerced) or hardcoded default.
// Matches Python: _get_active_value(category, key).
func (s *Service) getActiveValue(ctx context.Context, category, key string) (any, error) {
	override, err := s.repo.Get(ctx, category, key)
	if err != nil {
		return nil, err
	}
	if override != nil {
		expected := GetExpectedType(category, key)
		if expected != "" {
			return Coerce(override, expected)
		}
		return override, nil
	}
	def, gdErr := GetDefault(category, key)
	if gdErr != nil {
		// Python: bare KeyError propagates through — no wrapping in MVMError.
		// Go: propagate the plain error as-is, no DomainError wrapping.
		return nil, gdErr
	}
	return def, nil
}
