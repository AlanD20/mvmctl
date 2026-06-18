// Package config provides global configuration storage and retrieval.
// Layer: Core domain — never imports other core/* packages.
package config

import "context"

// All values are JSON-encoded/decoded for storage.
type SettingsRepository interface {
	// Get returns the parsed JSON value for a setting, or nil if not found.
	Get(ctx context.Context, category, key string) (any, error)

	// Set stores a value as JSON. Uses INSERT ... ON CONFLICT with CURRENT_TIMESTAMP.
	Set(ctx context.Context, category, key string, value any) error

	// Delete removes a setting. Returns true if a row was deleted.
	Delete(ctx context.Context, category, key string) (bool, error)

	// DeleteByCategory removes all settings in a category. Returns number of rows deleted.
	DeleteByCategory(ctx context.Context, category string) (int, error)

	// DeleteAll removes ALL user settings. Returns number of rows deleted.
	DeleteAll(ctx context.Context) (int, error)

	// Count returns total number of user settings.
	Count(ctx context.Context) (int, error)

	// ListByCategory lists settings, optionally filtered by category.
	// Returns nested map: {category: {key: value}}.
	// If category is nil, returns all settings (all categories).
	ListByCategory(ctx context.Context, category *string) (map[string]map[string]any, error)
}
