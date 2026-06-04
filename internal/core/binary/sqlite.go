package binary

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/model"
)

type sqliteRepo struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) Repository {
	return &sqliteRepo{db: db}
}

func (r *sqliteRepo) Get(ctx context.Context, id string) (*model.BinaryItem, error) {
	var b model.BinaryItem
	err := r.db.GetContext(ctx, &b,
		`SELECT * FROM binaries WHERE id = ? AND deleted_at IS NULL `, id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &b, err
}

func (r *sqliteRepo) FindByPrefix(ctx context.Context, prefix string) ([]*model.BinaryItem, error) {
	var items []*model.BinaryItem
	return items, r.db.SelectContext(ctx, &items,
		`SELECT * FROM binaries WHERE id LIKE ? AND deleted_at IS NULL `, prefix+"%")
}

func (r *sqliteRepo) ListAll(ctx context.Context) ([]*model.BinaryItem, error) {
	var items []*model.BinaryItem
	return items, r.db.SelectContext(ctx, &items,
		`SELECT * FROM binaries WHERE deleted_at IS NULL ORDER BY created_at`)
}

func (r *sqliteRepo) ListByName(ctx context.Context, name string) ([]*model.BinaryItem, error) {
	var items []*model.BinaryItem
	return items, r.db.SelectContext(ctx, &items,
		`SELECT * FROM binaries WHERE name = ? AND deleted_at IS NULL  ORDER BY created_at`, name)
}

func (r *sqliteRepo) GetByNameAndVersion(ctx context.Context, name, version string) (*model.BinaryItem, error) {
	var b model.BinaryItem
	err := r.db.GetContext(
		ctx,
		&b,
		`SELECT * FROM binaries WHERE name = ? AND version = ? AND deleted_at IS NULL `,
		name,
		version,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &b, err
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
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx,
		`UPDATE binaries SET is_default = 0 WHERE name = ? AND deleted_at IS NULL`, name)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE binaries SET is_default = 1, updated_at = CURRENT_TIMESTAMP
		WHERE name = ? AND version = ? AND deleted_at IS NULL`, name, version)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (r *sqliteRepo) Count(ctx context.Context) (int, error) {
	var count int
	err := sqlx.GetContext(ctx, r.db, &count, `SELECT COUNT(*) FROM binaries WHERE deleted_at IS NULL`)
	return count, err
}

func (r *sqliteRepo) GetDefault(ctx context.Context, name string) (*model.BinaryItem, error) {
	var b model.BinaryItem
	err := r.db.GetContext(ctx, &b,
		`SELECT * FROM binaries WHERE name = ? AND is_default = 1 AND deleted_at IS NULL `, name)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &b, err
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
