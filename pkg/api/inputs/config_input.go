package inputs

import (
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

// Validate checks that the config input is valid for the given action.
func (i *ConfigInput) Validate() error {
	switch i.Action {
	case "get":
		if i.Category == "" {
			return errs.New(errs.CodeConfigError, "Category is required for get operation")
		}
		if i.Key != "" && !config.IsKeyInCategory(i.Category, i.Key) {
			return errs.New(
				errs.CodeConfigError,
				"'"+i.Category+"."+i.Key+"' is not a valid setting key. Use 'mvm config ls' to see valid keys.",
			)
		}
	case "set":
		if i.Category == "" || i.Key == "" {
			return errs.New(errs.CodeConfigError, "Category and key are required for set operation")
		}
		if i.Value == nil {
			return errs.New(errs.CodeConfigError, "Value is required for set operation")
		}
		if !config.IsKeyInCategory(i.Category, i.Key) {
			return errs.New(
				errs.CodeConfigError,
				"'"+i.Category+"."+i.Key+"' is not an overridable setting. Use 'mvm config ls' to see valid keys.",
			)
		}
	case "reset":
		if i.AllOverrides {
			// category and key are both optional for --all
		} else if i.Category == "" {
			return errs.New(errs.CodeConfigError, "Category is required for reset operation (or use --all)")
		}
		if i.Key != "" && !config.IsKeyInCategory(i.Category, i.Key) {
			return errs.New(errs.CodeConfigError, "'"+i.Category+"."+i.Key+"' is not a valid setting key")
		}
	case "list":
		// No additional validation needed for listing
	default:
		return errs.New(errs.CodeConfigError, "Invalid action: '"+i.Action+"'. Use get, set, reset, or list.")
	}
	return nil
}

// Resolve validates the config input and returns it. The resolved shape is
// identical to the input, so this method simply validates and returns self.
func (i *ConfigInput) Resolve() (*ConfigInput, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	return i, nil
}
