package key

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"mvmctl/internal/infra/model"
)

type sqliteRepo struct {
	db *sql.DB
}

// NewRepository creates a new Repository backed by SQLite.
func NewRepository(db *sql.DB) Repository {
	return &sqliteRepo{db: db}
}

func (r *sqliteRepo) GetByName(ctx context.Context, name string) (*model.SSHKeyItem, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT * FROM ssh_keys WHERE name = ?`, name)
	return scanKey(row)
}

func (r *sqliteRepo) FindByPrefix(ctx context.Context, prefix string) ([]*model.SSHKeyItem, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT * FROM ssh_keys WHERE id LIKE ?`, prefix+"%")
	if err != nil {
		return nil, fmt.Errorf("find by prefix: %w", err)
	}
	defer rows.Close()
	return scanKeys(rows)
}

func (r *sqliteRepo) Count(ctx context.Context) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM ssh_keys").Scan(&count)
	return count, err
}

func (r *sqliteRepo) List(ctx context.Context) ([]*model.SSHKeyItem, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT * FROM ssh_keys ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list keys: %w", err)
	}
	defer rows.Close()
	return scanKeys(rows)
}

func (r *sqliteRepo) Upsert(ctx context.Context, key *model.SSHKeyItem) error {
	var privKey any
	if key.PrivateKeyPath != nil {
		privKey = *key.PrivateKeyPath
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO ssh_keys (
			id, name, fingerprint, algorithm, comment,
			private_key_path, public_key_path, is_default, is_present, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			fingerprint = excluded.fingerprint,
			algorithm = excluded.algorithm,
			comment = excluded.comment,
			private_key_path = excluded.private_key_path,
			public_key_path = excluded.public_key_path,
			is_default = excluded.is_default,
			is_present = excluded.is_present,
			updated_at = CURRENT_TIMESTAMP`,
		key.ID, key.Name, key.Fingerprint, key.Algorithm, key.Comment,
		privKey, key.PublicKeyPath,
		boolToInt(key.IsDefault), boolToInt(key.IsPresent),
		key.CreatedAt, key.UpdatedAt)
	return err
}

func (r *sqliteRepo) UpdateManyIsPresent(ctx context.Context, ids []string, present bool) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1] // remove trailing comma
	args := make([]any, 0, len(ids)+1)
	args = append(args, boolToInt(present))
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := r.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE ssh_keys SET is_present = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id IN (%s)`, placeholders),
		args...)
	return err
}

func (r *sqliteRepo) Delete(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM ssh_keys WHERE id = ?", id)
	return err
}

// SetDefault sets a key as default. Does NOT clear other defaults,
// matching Python's behavior exactly.
func (r *sqliteRepo) SetDefault(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, "UPDATE ssh_keys SET is_default = 1 WHERE id = ?", id)
	return err
}

// GetDefaults returns all keys marked as default.
func (r *sqliteRepo) GetDefaults(ctx context.Context) ([]*model.SSHKeyItem, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT * FROM ssh_keys WHERE is_default = 1 ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("get defaults: %w", err)
	}
	defer rows.Close()
	return scanKeys(rows)
}

func (r *sqliteRepo) ClearDefaults(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, "UPDATE ssh_keys SET is_default = 0")
	return err
}

// scanKey scans a single row into an SSHKeyItem.
// Returns nil, nil if no rows.
func scanKey(row *sql.Row) (*model.SSHKeyItem, error) {
	var k model.SSHKeyItem
	var privPath sql.NullString
	var isDefault, isPresent int
	var createdAt, updatedAt string
	err := row.Scan(&k.ID, &k.Name, &k.Fingerprint, &k.Algorithm, &k.Comment,
		&privPath, &k.PublicKeyPath, &isDefault, &isPresent, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan key: %w", err)
	}
	if privPath.Valid {
		k.PrivateKeyPath = &privPath.String
	}
	k.IsDefault = isDefault == 1
	k.IsPresent = isPresent == 1
	k.CreatedAt = createdAt
	k.UpdatedAt = updatedAt
	return &k, nil
}

// scanKeys scans multiple rows into a slice of SSHKeyItem.
func scanKeys(rows *sql.Rows) ([]*model.SSHKeyItem, error) {
	var keys []*model.SSHKeyItem
	for rows.Next() {
		var k model.SSHKeyItem
		var privPath sql.NullString
		var isDefault, isPresent int
		var createdAt, updatedAt string
		err := rows.Scan(&k.ID, &k.Name, &k.Fingerprint, &k.Algorithm, &k.Comment,
			&privPath, &k.PublicKeyPath, &isDefault, &isPresent, &createdAt, &updatedAt)
		if err != nil {
			return nil, fmt.Errorf("scan key: %w", err)
		}
		if privPath.Valid {
			k.PrivateKeyPath = &privPath.String
		}
		k.IsDefault = isDefault == 1
		k.IsPresent = isPresent == 1
		k.CreatedAt = createdAt
		k.UpdatedAt = updatedAt
		keys = append(keys, &k)
	}
	return keys, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
