package config

import (
	"context"

	"github.com/jmoiron/sqlx"
)

// Resolve looks up a setting: check user_settings override, else fall back to default.
// Matches Python: classmethod SettingsService.resolve(db, category, key).
// Python's get_default raises KeyError if key doesn't exist — Go returns error.
func Resolve(ctx context.Context, db *sqlx.DB, category, key string) (any, error) {
	repo := NewRepository(db)
	override, err := repo.Get(ctx, category, key)
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
		return nil, gdErr
	}
	return def, nil
}
