package kernel

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
)

type sqliteRepo struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) Repository {
	return &sqliteRepo{db: db}
}

func (r *sqliteRepo) Get(ctx context.Context, id string) (*model.KernelItem, error) {
	var k model.KernelItem
	err := r.db.GetContext(ctx, &k,
		`SELECT * FROM kernels WHERE id = ? AND deleted_at IS NULL `, id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &k, err
}

func (r *sqliteRepo) FindByPrefix(ctx context.Context, prefix string) ([]*model.KernelItem, error) {
	var items []*model.KernelItem
	return items, r.db.SelectContext(ctx, &items,
		`SELECT * FROM kernels WHERE id LIKE ? AND deleted_at IS NULL `, prefix+"%")
}

func (r *sqliteRepo) ListAll(ctx context.Context) ([]*model.KernelItem, error) {
	var items []*model.KernelItem
	return items, r.db.SelectContext(ctx, &items,
		`SELECT * FROM kernels WHERE deleted_at IS NULL ORDER BY created_at`)
}

func (r *sqliteRepo) GetByName(ctx context.Context, name string) (*model.KernelItem, error) {
	var k model.KernelItem
	err := r.db.GetContext(ctx, &k,
		`SELECT * FROM kernels WHERE name = ? AND deleted_at IS NULL `, name)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &k, err
}

func (r *sqliteRepo) GetDefault(ctx context.Context) (*model.KernelItem, error) {
	var k model.KernelItem
	err := r.db.GetContext(ctx, &k,
		`SELECT * FROM kernels WHERE is_default = 1 AND deleted_at IS NULL  LIMIT 1`)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &k, err
}

func (r *sqliteRepo) Upsert(ctx context.Context, k *model.KernelItem) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO kernels (
			id, name, base_name, version, arch, type, path,
			is_default, is_present, created_at, updated_at, deleted_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			base_name = excluded.base_name,
			version = excluded.version,
			arch = excluded.arch,
			type = excluded.type,
			path = excluded.path,
			is_default = excluded.is_default,
			is_present = excluded.is_present,
			updated_at = CURRENT_TIMESTAMP,
			deleted_at = excluded.deleted_at`,
		k.ID, k.Name, k.BaseName, k.Version, k.Arch, k.Type, k.Path,
		infra.BoolToInt(k.IsDefault), infra.BoolToInt(k.IsPresent),
		k.CreatedAt, k.UpdatedAt, k.DeletedAt,
	)
	return err
}

func (r *sqliteRepo) Delete(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM kernels WHERE id = ?`, id)
	return err
}

func (r *sqliteRepo) SoftDelete(ctx context.Context, id string) error {
	now := time.Now().Format(time.RFC3339)
	_, err := r.db.ExecContext(ctx,
		`UPDATE kernels SET deleted_at = ?, is_present = 0 WHERE id = ?`, now, id)
	return err
}

func (r *sqliteRepo) Count(ctx context.Context) (int, error) {
	var count int
	return count, sqlx.GetContext(ctx, r.db, &count, `SELECT COUNT(*) FROM kernels WHERE deleted_at IS NULL`)
}

func (r *sqliteRepo) GetByType(ctx context.Context, kernelType string) (*model.KernelItem, error) {
	var k model.KernelItem
	err := r.db.GetContext(ctx, &k,
		`SELECT * FROM kernels WHERE type = ? AND deleted_at IS NULL  LIMIT 1`, kernelType)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &k, err
}

func (r *sqliteRepo) GetByVersionAndType(ctx context.Context, version, kernelType string) (*model.KernelItem, error) {
	var k model.KernelItem
	err := r.db.GetContext(
		ctx,
		&k,
		`SELECT * FROM kernels WHERE version = ? AND type = ? AND deleted_at IS NULL `,
		version,
		kernelType,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &k, err
}

func (r *sqliteRepo) SetDefault(ctx context.Context, id string) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx,
		`UPDATE kernels SET is_default = 0 WHERE deleted_at IS NULL`)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx,
		`UPDATE kernels SET is_default = 1, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND deleted_at IS NULL`, id)
	if err != nil {
		return err
	}

	return tx.Commit()
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
		`UPDATE kernels SET is_present = ?, updated_at = CURRENT_TIMESTAMP WHERE id IN (`+placeholders+`)`,
		args...)
	return err
}
