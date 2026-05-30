package inputs

import (
	"context"
	"database/sql"

	"mvmctl/internal/core/config"
	"mvmctl/internal/infra/errs"
)

// ConfigInput matches Python's ConfigInput dataclass.
//
//	@dataclass
//	class ConfigInput:
//	    action: str  # 'get', 'set', 'list', 'reset'
//	    category: str | None = None  # e.g. 'defaults.vm'
//	    key: str | None = None  # e.g. 'vcpu_count'
//	    value: Any | None = None  # for 'set'
//	    all_overrides: bool = False  # for 'reset --all'
//
// Value uses any because config values can be int, bool, string, map, or
// slice — Go has no union type for this.
type ConfigInput struct {
	Action       string  `json:"action"`
	Category     *string `json:"category,omitempty"`
	Key          *string `json:"key,omitempty"`
	Value        any     `json:"value,omitempty"`
	AllOverrides bool    `json:"all_overrides"`
}

// ResolvedConfigInput matches Python's ResolvedConfigInput (frozen dataclass).
//
//	@dataclass(frozen=True)
//	class ResolvedConfigInput:
//	    action: str
//	    category: str | None
//	    key: str | None
//	    value: Any | None
//	    all_overrides: bool
//	    service: SettingsService
//
// Value uses any because config values can be int, bool, string, map, or
// slice — Go has no union type for this.
type ResolvedConfigInput struct {
	Action       string
	Category     *string
	Key          *string
	Value        any
	AllOverrides bool
	Service      *config.Service // SettingsService — set during resolve
}

// ConfigRequest matches Python's ConfigRequest.
//
// Resolve ConfigInput against the database.
type ConfigRequest struct {
	db      *sql.DB
	input   ConfigInput
	result  *ResolvedConfigInput
	service *config.Service
}

// NewConfigRequest creates a new ConfigRequest.
func NewConfigRequest(inputs ConfigInput, db *sql.DB) *ConfigRequest {
	svc := config.NewService(config.NewRepository(db))
	return &ConfigRequest{
		db:      db,
		input:   inputs,
		service: svc,
	}
}

// Result returns the resolved input, or nil if resolve() has not been called.

// isKeyInCategory checks if a key is valid for a given category in OverridableSettings.
func isKeyInCategory(category, key string) bool {
	if catSettings, ok := config.OverridableSettings[category]; ok {
		for k := range catSettings {
			if k == key {
				return true
			}
		}
	}
	return false
}

// Resolve resolves and validates config input.
// Matches Python's ConfigRequest.resolve().
func (r *ConfigRequest) Resolve(ctx context.Context) (*ResolvedConfigInput, error) {
	category := r.input.Category
	key := r.input.Key

	if r.input.Action == "get" {
		if category == nil || *category == "" {
			return nil, &errs.DomainError{
				Code:    errs.CodeConfigError,
				Op:      "config",
				Message: "Category is required for get operation",
				Class:   errs.ClassValidation,
			}
		}
		// key is optional for category-level get
		if key != nil {
			if !isKeyInCategory(*category, *key) {
				return nil, &errs.DomainError{
					Code:    errs.CodeConfigError,
					Op:      "config",
					Message: "'" + *category + "." + *key + "' is not a valid setting key. Use 'mvm config ls' to see valid keys.",
					Class:   errs.ClassValidation,
				}
			}
		}
	} else if r.input.Action == "set" {
		if category == nil || *category == "" || key == nil || *key == "" {
			return nil, &errs.DomainError{
				Code:    errs.CodeConfigError,
				Op:      "config",
				Message: "Category and key are required for set operation",
				Class:   errs.ClassValidation,
			}
		}
		if r.input.Value == nil {
			return nil, &errs.DomainError{
				Code:    errs.CodeConfigError,
				Op:      "config",
				Message: "Value is required for set operation",
				Class:   errs.ClassValidation,
			}
		}

		// Validate key is overridable
		if !isKeyInCategory(*category, *key) {
			return nil, &errs.DomainError{
				Code:    errs.CodeConfigError,
				Op:      "config",
				Message: "'" + *category + "." + *key + "' is not an overridable setting. Use 'mvm config ls' to see valid keys.",
				Class:   errs.ClassValidation,
			}
		}
	} else if r.input.Action == "reset" {
		if r.input.AllOverrides {
			// category and key are both optional for --all
		} else if category == nil || *category == "" {
			return nil, &errs.DomainError{
				Code:    errs.CodeConfigError,
				Op:      "config",
				Message: "Category is required for reset operation (or use --all)",
				Class:   errs.ClassValidation,
			}
		}
		// key is optional for category-level reset
		if key != nil {
			catName := ""
			if category != nil {
				catName = *category
			}
			if !isKeyInCategory(catName, *key) {
				return nil, &errs.DomainError{
					Code:    errs.CodeConfigError,
					Op:      "config",
					Message: "'" + catName + "." + *key + "' is not a valid setting key",
					Class:   errs.ClassValidation,
				}
			}
		}
	}

	r.result = &ResolvedConfigInput{
		Action:       r.input.Action,
		Category:     category,
		Key:          key,
		Value:        r.input.Value,
		AllOverrides: r.input.AllOverrides,
		Service:      r.service,
	}

	return r.result, nil
}
