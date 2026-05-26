package binary

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/model"
)

type sqliteRepo struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) Repository {
	return &sqliteRepo{db: db}
}

func (r *sqliteRepo) Get(ctx context.Context, id string) (*model.BinaryItem, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT * FROM binaries WHERE id = ? AND deleted_at IS NULL AND is_present = 1`, id)
	b, err := scanBinary(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return b, err
}

func (r *sqliteRepo) FindByPrefix(ctx context.Context, prefix string) ([]*model.BinaryItem, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT * FROM binaries WHERE id LIKE ? AND deleted_at IS NULL AND is_present = 1`, prefix+"%")
	if err != nil {
		return nil, fmt.Errorf("find binaries by prefix: %w", err)
	}
	defer rows.Close()
	return scanBinaries(rows)
}

func (r *sqliteRepo) ListAll(ctx context.Context) ([]*model.BinaryItem, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT * FROM binaries WHERE deleted_at IS NULL ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list binaries: %w", err)
	}
	defer rows.Close()
	return scanBinaries(rows)
}

func (r *sqliteRepo) ListByName(ctx context.Context, name string) ([]*model.BinaryItem, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT * FROM binaries WHERE name = ? AND deleted_at IS NULL AND is_present = 1 ORDER BY created_at`, name)
	if err != nil {
		return nil, fmt.Errorf("list binaries by name: %w", err)
	}
	defer rows.Close()
	return scanBinaries(rows)
}

func (r *sqliteRepo) GetByNameAndVersion(ctx context.Context, name, version string) (*model.BinaryItem, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT * FROM binaries WHERE name = ? AND version = ? AND deleted_at IS NULL AND is_present = 1 LIMIT 1`,
		name, version)
	b, err := scanBinary(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return b, err
}

func (r *sqliteRepo) Upsert(ctx context.Context, b *model.BinaryItem) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO binaries (
			id, name, version, full_version, ci_version, path,
			is_default, is_present, created_at, updated_at, deleted_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			version = excluded.version,
			full_version = excluded.full_version,
			ci_version = excluded.ci_version,
			path = excluded.path,
			is_default = excluded.is_default,
			is_present = excluded.is_present,
			updated_at = CURRENT_TIMESTAMP,
			deleted_at = excluded.deleted_at`,
		b.ID, b.Name, b.Version, b.FullVersion, b.CIVersion, b.Path,
		infra.BoolToInt(b.IsDefault), infra.BoolToInt(b.IsPresent),
		b.CreatedAt, b.UpdatedAt, b.DeletedAt,
	)
	return err
}

func (r *sqliteRepo) Delete(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM binaries WHERE id = ?`, id)
	return err
}

func (r *sqliteRepo) DeleteByName(ctx context.Context, name string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM binaries WHERE name = ?`, name)
	return err
}

func (r *sqliteRepo) DeleteByNameAndVersion(ctx context.Context, name, version string) error {
	normalized := strings.TrimPrefix(version, "v")
	prefixed := "v" + normalized
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM binaries WHERE name = ? AND (version = ? OR version = ?)`,
		name, normalized, prefixed)
	return err
}

func (r *sqliteRepo) SetDefault(ctx context.Context, name, version, path string) error {
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(
		`UPDATE binaries SET is_default = 0 WHERE name = ? AND deleted_at IS NULL`, name)
	if err != nil {
		return err
	}
	_, err = tx.Exec(
		`UPDATE binaries SET is_default = 1, updated_at = CURRENT_TIMESTAMP
		WHERE name = ? AND version = ? AND deleted_at IS NULL`, name, version)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (r *sqliteRepo) Count(ctx context.Context) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM binaries WHERE deleted_at IS NULL`).Scan(&count)
	return count, err
}

func (r *sqliteRepo) GetDefault(ctx context.Context, name string) (*model.BinaryItem, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT * FROM binaries WHERE name = ? AND is_default = 1 AND deleted_at IS NULL AND is_present = 1 LIMIT 1`, name)
	b, err := scanBinary(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return b, err
}

func (r *sqliteRepo) SoftDelete(ctx context.Context, id string) error {
	now := time.Now().Format(time.RFC3339)
	_, err := r.db.ExecContext(ctx,
		`UPDATE binaries SET deleted_at = ?, is_present = 0 WHERE id = ?`, now, id)
	return err
}

func (r *sqliteRepo) UpdateManyIsPresent(ctx context.Context, ids []string, present bool) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, 0, len(ids)+1)
	args = append(args, infra.BoolToInt(present))
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := r.db.ExecContext(ctx,
		`UPDATE binaries SET is_present = ?, updated_at = CURRENT_TIMESTAMP WHERE id IN (`+placeholders+`)`,
		args...)
	return err
}

func scanBinary(scanner interface{ Scan(dest ...any) error }) (*model.BinaryItem, error) {
	var b model.BinaryItem
	var ciVersion, deletedAt sql.NullString
	var isDefault, isPresent int
	err := scanner.Scan(
		&b.ID, &b.Name, &b.Version, &b.FullVersion, &ciVersion, &b.Path,
		&isDefault, &isPresent, &b.CreatedAt, &b.UpdatedAt, &deletedAt,
	)
	if err != nil {
		return nil, err
	}
	b.IsDefault = isDefault == 1
	b.IsPresent = isPresent == 1
	if ciVersion.Valid {
		b.CIVersion = &ciVersion.String
	}
	if deletedAt.Valid {
		b.DeletedAt = &deletedAt.String
	}
	return &b, nil
}

func scanBinaries(rows *sql.Rows) ([]*model.BinaryItem, error) {
	var binaries []*model.BinaryItem
	for rows.Next() {
		b, err := scanBinary(rows)
		if err != nil {
			return nil, fmt.Errorf("scan binary: %w", err)
		}
		binaries = append(binaries, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration: %w", err)
	}
	return binaries, nil
}
