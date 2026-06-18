package inputs
import (
	"context"
	"mvmctl/internal/core/config"
	"mvmctl/pkg/errs"
)
// ConfigInput specifies config input.
type ConfigInput struct {
	Action       string `json:"action"`
	Category     string `json:"category,omitempty"`
	Key          string `json:"key,omitempty"`
	Value        any    `json:"value,omitempty"`
	AllOverrides bool   `json:"all_overrides"`
}
// ResolvedConfigInput specifies resolved config input.
type ResolvedConfigInput struct {
	Action       string
	Category     string
	Key          string
	Value        any
	AllOverrides bool
}
// ConfigRequest specifies config request.
// Resolve ConfigInput against the database.
type ConfigRequest struct {
	input  ConfigInput
	result *ResolvedConfigInput
}
// NewConfigRequest creates a new ConfigRequest.
func NewConfigRequest(inputs ConfigInput) *ConfigRequest {
	return &ConfigRequest{
		input: inputs,
	}
}
// Result returns the resolved input, or nil if resolve() has not been called.
// Resolve resolves and validates config input.
func (r *ConfigRequest) Resolve(ctx context.Context) (*ResolvedConfigInput, error) {
	category := r.input.Category
	key := r.input.Key
	if r.input.Action == "get" {
		if category == "" {
			return nil, errs.New(errs.CodeConfigError, "Category is required for get operation")
		}
		// key is optional for category-level get
		if key != "" {
			if !config.IsKeyInCategory(category, key) {
				return nil, errs.New(
					errs.CodeConfigError,
					"'"+category+"."+key+"' is not a valid setting key. Use 'mvm config ls' to see valid keys.",
				)
			}
		}
	} else if r.input.Action == "set" {
		if category == "" || key == "" {
			return nil, errs.New(errs.CodeConfigError, "Category and key are required for set operation")
		}
		if r.input.Value == nil {
			return nil, errs.New(errs.CodeConfigError, "Value is required for set operation")
		}
		// Validate key is overridable
		if !config.IsKeyInCategory(category, key) {
			return nil, errs.New(
				errs.CodeConfigError,
				"'"+category+"."+key+"' is not an overridable setting. Use 'mvm config ls' to see valid keys.",
			)
		}
	} else if r.input.Action == "reset" {
		if r.input.AllOverrides {
			// category and key are both optional for --all
		} else if category == "" {
			return nil, errs.New(errs.CodeConfigError, "Category is required for reset operation (or use --all)")
		}
		// key is optional for category-level reset
		if key != "" {
			if !config.IsKeyInCategory(category, key) {
				return nil, errs.New(errs.CodeConfigError, "'"+category+"."+key+"' is not a valid setting key")
			}
		}
	}
	r.result = &ResolvedConfigInput{
		Action:       r.input.Action,
		Category:     category,
		Key:          key,
		Value:        r.input.Value,
		AllOverrides: r.input.AllOverrides,
	}
	return r.result, nil
}
