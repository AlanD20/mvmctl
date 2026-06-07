package volume

import (
	"context"
	"database/sql"

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

func (r *sqliteRepo) Get(ctx context.Context, id string) (*model.VolumeItem, error) {
	var v model.VolumeItem
	err := r.db.GetContext(ctx, &v, `SELECT * FROM volumes WHERE id = ?`, id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &v, err
}

func (r *sqliteRepo) FindByPrefix(ctx context.Context, prefix string) ([]*model.VolumeItem, error) {
	var items []*model.VolumeItem
	return items, r.db.SelectContext(ctx, &items,
		`SELECT * FROM volumes WHERE id LIKE ?`, prefix+"%")
}

func (r *sqliteRepo) GetByName(ctx context.Context, name string) (*model.VolumeItem, error) {
	var v model.VolumeItem
	err := r.db.GetContext(ctx, &v, `SELECT * FROM volumes WHERE name = ?`, name)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &v, err
}

func (r *sqliteRepo) ListAll(ctx context.Context) ([]*model.VolumeItem, error) {
	var items []*model.VolumeItem
	return items, r.db.SelectContext(ctx, &items, `SELECT * FROM volumes ORDER BY created_at`)
}

func (r *sqliteRepo) Upsert(ctx context.Context, v *model.VolumeItem) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO volumes (
			id, name, size_bytes, format, is_read_only, path, status, vm_id, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			size_bytes = excluded.size_bytes,
			format = excluded.format,
			is_read_only = excluded.is_read_only,
			path = excluded.path,
			status = excluded.status,
			vm_id = excluded.vm_id,
			updated_at = CURRENT_TIMESTAMP`,
		v.ID, v.Name, v.SizeBytes, string(v.Format), infra.BoolToInt(v.IsReadOnly),
		v.Path, string(v.Status), v.VMID, v.CreatedAt, v.UpdatedAt,
	)
	return err
}

func (r *sqliteRepo) Delete(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM volumes WHERE id = ?`, id)
	return err
}

func (r *sqliteRepo) FindByIDs(ctx context.Context, ids []string) ([]*model.VolumeItem, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	query, args, err := sqlx.In("SELECT * FROM volumes WHERE id IN (?)", ids)
	if err != nil {
		return nil, err
	}
	query = r.db.Rebind(query)
	var items []*model.VolumeItem
	return items, r.db.SelectContext(ctx, &items, query, args...)
}

func (r *sqliteRepo) Count(ctx context.Context) (int, error) {
	var count int
	return count, sqlx.GetContext(ctx, r.db, &count, `SELECT COUNT(*) FROM volumes`)
}
