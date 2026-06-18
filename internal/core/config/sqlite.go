package config

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/jmoiron/sqlx"

	"mvmctl/internal/lib/model"
)

type sqliteRepo struct {
	db *sqlx.DB
}

// NewRepository creates a new SettingsRepository backed by SQLite.
func NewRepository(db *sqlx.DB) SettingsRepository {
	return &sqliteRepo{db: db}
}

// Get returns the parsed JSON value for a setting, or nil if not found.
// Database errors (including "no such table") propagate;
// only sql.ErrNoRows is treated as not-found (returns nil, nil).
func (r *sqliteRepo) Get(ctx context.Context, category, key string) (any, error) {
	var valueStr string
	err := sqlx.GetContext(ctx, r.db, &valueStr,
		"SELECT value FROM user_settings WHERE category = ? AND key = ?",
		category, key)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var val any
	if err := json.Unmarshal([]byte(valueStr), &val); err != nil {
		return nil, fmt.Errorf("parse setting value: %w", err)
	}
	return val, nil
}

// Set stores a value as JSON. Uses INSERT ... ON CONFLICT with CURRENT_TIMESTAMP.
func (r *sqliteRepo) Set(ctx context.Context, category, key string, value any) error {
	var valueStr string
	if value == nil {
		valueStr = "null"
	} else {
		data, mErr := json.Marshal(value)
		if mErr != nil {
			return fmt.Errorf("marshal setting value: %w", mErr)
		}
		valueStr = string(data)
	}

	_, err := r.db.ExecContext(ctx, `
		INSERT INTO user_settings (category, key, value, updated_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(category, key) DO UPDATE SET
			value = excluded.value,
			updated_at = CURRENT_TIMESTAMP
	`, category, key, valueStr)
	if err != nil {
		return err
	}
	return nil
}

// Delete removes a setting. Returns true if a row was deleted.
func (r *sqliteRepo) Delete(ctx context.Context, category, key string) (bool, error) {
	result, err := r.db.ExecContext(ctx,
		"DELETE FROM user_settings WHERE category = ? AND key = ?",
		category, key,
	)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

// DeleteByCategory removes all settings in a category. Returns number of rows deleted.
func (r *sqliteRepo) DeleteByCategory(ctx context.Context, category string) (int, error) {
	result, err := r.db.ExecContext(ctx,
		"DELETE FROM user_settings WHERE category = ?",
		category,
	)
	if err != nil {
		return 0, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(rows), nil
}

// DeleteAll removes ALL user settings. Returns number of rows deleted.
func (r *sqliteRepo) DeleteAll(ctx context.Context) (int, error) {
	result, err := r.db.ExecContext(ctx, "DELETE FROM user_settings")
	if err != nil {
		return 0, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(rows), nil
}

// Count returns total number of user settings.
// Database errors (including "no such table") propagate.
func (r *sqliteRepo) Count(ctx context.Context) (int, error) {
	var c int
	err := sqlx.GetContext(ctx, r.db, &c, "SELECT COUNT(*) FROM user_settings")
	if err != nil {
		return 0, err
	}
	return c, nil
}

// ListByCategory lists settings, optionally filtered by category.
// Returns nested map: {category: {key: value}} with ORDER BY category, key.
// If category is nil, returns all settings.
// Database errors (including "no such table") propagate.
func (r *sqliteRepo) ListByCategory(ctx context.Context, category *string) (map[string]map[string]any, error) {
	query := "SELECT category, key, value FROM user_settings"
	var args []any
	if category != nil {
		query += " WHERE category = ?"
		args = append(args, *category)
	}
	query += " ORDER BY category, key"

	var settings []model.Setting
	if err := r.db.SelectContext(ctx, &settings, query, args...); err != nil {
		return nil, fmt.Errorf("query settings: %w", err)
	}

	result := make(map[string]map[string]any)
	for _, s := range settings {
		var val any
		if err := json.Unmarshal([]byte(s.Value), &val); err != nil {
			return nil, fmt.Errorf("parse setting value for %s.%s: %w", s.Category, s.Key, err)
		}
		if _, ok := result[s.Category]; !ok {
			result[s.Category] = make(map[string]any)
		}
		result[s.Category][s.Key] = val
	}
	return result, nil
}
