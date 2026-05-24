package config

import "context"

// SettingsRepository matches the Python mvmctl.core.config._repository.SettingsRepository.
// All values are JSON-encoded/decoded for storage.
type SettingsRepository interface {
	// Get returns the parsed JSON value for a setting, or nil if not found.
	// Matches Python: returns Any | None via json.loads(row["value"]).
	Get(ctx context.Context, category, key string) (any, error)

	// Set stores a value as JSON. Uses INSERT ... ON CONFLICT with CURRENT_TIMESTAMP.
	// Matches Python: json.dumps(value), CURRENT_TIMESTAMP.
	Set(ctx context.Context, category, key string, value any) error

	// Delete removes a setting. Returns true if a row was deleted.
	// Matches Python: cursor.rowcount > 0.
	Delete(ctx context.Context, category, key string) (bool, error)

	// DeleteByCategory removes all settings in a category. Returns number of rows deleted.
	// Matches Python: cursor.rowcount.
	DeleteByCategory(ctx context.Context, category string) (int, error)

	// DeleteAll removes ALL user settings. Returns number of rows deleted.
	// Matches Python: DELETE FROM user_settings.
	DeleteAll(ctx context.Context) (int, error)

	// Count returns total number of user settings.
	// Matches Python: SELECT COUNT(*) FROM user_settings.
	Count(ctx context.Context) (int, error)

	// ListByCategory lists settings, optionally filtered by category.
	// Returns nested map: {category: {key: value}}.
	// Matches Python: ORDER BY category, key.
	// If category is nil, returns all settings (all categories).
	ListByCategory(ctx context.Context, category *string) (map[string]map[string]any, error)
}
