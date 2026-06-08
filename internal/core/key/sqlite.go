package key

import (
	"context"
	"database/sql"

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

func (r *sqliteRepo) Get(ctx context.Context, id string) (*model.SSHKeyItem, error) {
	var k model.SSHKeyItem
	err := r.db.GetContext(ctx, &k, `SELECT * FROM ssh_keys WHERE id = ?`, id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &k, err
}

func (r *sqliteRepo) ListAll(ctx context.Context) ([]*model.SSHKeyItem, error) {
	var items []*model.SSHKeyItem
	return items, r.db.SelectContext(ctx, &items, `SELECT * FROM ssh_keys ORDER BY name`)
}

func (r *sqliteRepo) GetByName(ctx context.Context, name string) (*model.SSHKeyItem, error) {
	var k model.SSHKeyItem
	err := r.db.GetContext(ctx, &k, `SELECT * FROM ssh_keys WHERE name = ?`, name)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &k, err
}

func (r *sqliteRepo) GetDefault(ctx context.Context) (*model.SSHKeyItem, error) {
	var k model.SSHKeyItem
	err := r.db.GetContext(ctx, &k, `SELECT * FROM ssh_keys WHERE is_default = 1 LIMIT 1`)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &k, err
}

func (r *sqliteRepo) Upsert(ctx context.Context, k *model.SSHKeyItem) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO ssh_keys (
			id, name, fingerprint, algorithm, comment,
			private_key_path, public_key_path,
			is_default, is_present, created_at, updated_at
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
		k.ID, k.Name, k.Fingerprint, k.Algorithm, k.Comment,
		k.PrivateKeyPath, k.PublicKeyPath,
		infra.BoolToInt(k.IsDefault), infra.BoolToInt(k.IsPresent),
		k.CreatedAt, k.UpdatedAt,
	)
	return err
}

func (r *sqliteRepo) Delete(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM ssh_keys WHERE id = ?`, id)
	return err
}

func (r *sqliteRepo) SetDefault(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(
		ctx,
		`UPDATE ssh_keys SET is_default = 1, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		id,
	)
	return err
}

func (r *sqliteRepo) List(ctx context.Context) ([]*model.SSHKeyItem, error) {
	var items []*model.SSHKeyItem
	return items, r.db.SelectContext(ctx, &items, `SELECT * FROM ssh_keys ORDER BY name`)
}

func (r *sqliteRepo) FindByPrefix(ctx context.Context, prefix string) ([]*model.SSHKeyItem, error) {
	var items []*model.SSHKeyItem
	return items, r.db.SelectContext(ctx, &items,
		`SELECT * FROM ssh_keys WHERE id LIKE ?`, prefix+"%")
}

func (r *sqliteRepo) GetDefaults(ctx context.Context) ([]*model.SSHKeyItem, error) {
	var items []*model.SSHKeyItem
	return items, r.db.SelectContext(ctx, &items,
		`SELECT * FROM ssh_keys WHERE is_default = 1`)
}

func (r *sqliteRepo) ClearDefaults(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `UPDATE ssh_keys SET is_default = 0`)
	return err
}

func (r *sqliteRepo) UpdateManyIsPresent(ctx context.Context, ids []string, present bool) error {
	if len(ids) == 0 {
		return nil
	}
	query, args, err := sqlx.In("UPDATE ssh_keys SET is_present = ?, updated_at = CURRENT_TIMESTAMP WHERE id IN (?)",
		infra.BoolToInt(present), ids)
	if err != nil {
		return err
	}
	query = r.db.Rebind(query)
	_, err = r.db.ExecContext(ctx, query, args...)
	return err
}

func (r *sqliteRepo) Count(ctx context.Context) (int, error) {
	var count int
	return count, sqlx.GetContext(ctx, r.db, &count, `SELECT COUNT(*) FROM ssh_keys`)
}
