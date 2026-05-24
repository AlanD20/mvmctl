package config

import (
	"context"
	"database/sql"
	"fmt"
)

type sqliteRepo struct {
	db *sql.DB
}

// NewRepository creates a new SettingsRepository backed by SQLite.
func NewRepository(db *sql.DB) SettingsRepository {
	return &sqliteRepo{db: db}
}

// Get returns the parsed JSON value for a setting, or nil if not found.
// Matches Python: json.loads(row["value"]), returns None on not found.
// Error behavior matches Python: database errors (including "no such table") propagate;
// only sql.ErrNoRows is treated as not-found (returns nil, nil).
func (r *sqliteRepo) Get(ctx context.Context, category, key string) (any, error) {
	var valueStr string
	err := r.db.QueryRowContext(ctx,
		"SELECT value FROM user_settings WHERE category = ? AND key = ?",
		category, key,
	).Scan(&valueStr)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	val, err := UnmarshalValue(valueStr)
	if err != nil {
		return nil, fmt.Errorf("parse setting value: %w", err)
	}
	return val, nil
}

// Set stores a value as JSON. Uses INSERT ... ON CONFLICT with CURRENT_TIMESTAMP.
// Matches Python exactly: json.dumps(value), CURRENT_TIMESTAMP, ON CONFLICT DO UPDATE.
func (r *sqliteRepo) Set(ctx context.Context, category, key string, value any) error {
	valueStr, err := MarshalValue(value)
	if err != nil {
		return fmt.Errorf("marshal setting value: %w", err)
	}

	_, err = r.db.ExecContext(ctx, `
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
// Matches Python: returns cursor.rowcount > 0.
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
// Matches Python: returns cursor.rowcount.
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
// Matches Python: DELETE FROM user_settings, return rowcount.
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
// Matches Python: SELECT COUNT(*) FROM user_settings.
// Error behavior matches Python: database errors (including "no such table") propagate.
func (r *sqliteRepo) Count(ctx context.Context) (int, error) {
	var c int
	err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM user_settings").Scan(&c)
	if err != nil {
		return 0, err
	}
	return c, nil
}

// ListByCategory lists settings, optionally filtered by category.
// Returns nested map: {category: {key: value}} with ORDER BY category, key.
// Matches Python: ORDER BY category, key; json.loads on each value.
// If category is nil, returns all settings.
// Error behavior matches Python: database errors (including "no such table") propagate.
func (r *sqliteRepo) ListByCategory(ctx context.Context, category *string) (map[string]map[string]any, error) {
	query := "SELECT category, key, value FROM user_settings"
	var args []any
	if category != nil {
		query += " WHERE category = ?"
		args = append(args, *category)
	}
	query += " ORDER BY category, key"

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]map[string]any)
	for rows.Next() {
		var cat, keyOut, valueStr string
		if err := rows.Scan(&cat, &keyOut, &valueStr); err != nil {
			return nil, fmt.Errorf("scan setting row: %w", err)
		}
		val, err := UnmarshalValue(valueStr)
		if err != nil {
			return nil, fmt.Errorf("parse setting value for %s.%s: %w", cat, keyOut, err)
		}
		if _, ok := result[cat]; !ok {
			result[cat] = make(map[string]any)
		}
		result[cat][keyOut] = val
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return result, nil
}


