package image

import (
	"context"
	"database/sql"
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

func (r *sqliteRepo) Get(ctx context.Context, imageID string) (*model.ImageItem, error) {
	var img model.ImageItem
	err := r.db.GetContext(ctx, &img, `SELECT * FROM images WHERE id = ?`, imageID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &img, err
}

func (r *sqliteRepo) FindByPrefix(ctx context.Context, prefix string) ([]*model.ImageItem, error) {
	var items []*model.ImageItem
	return items, r.db.SelectContext(ctx, &items,
		`SELECT * FROM images WHERE id LIKE ? AND deleted_at IS NULL `, prefix+"%")
}

func (r *sqliteRepo) GetByType(ctx context.Context, imgType string) (*model.ImageItem, error) {
	var img model.ImageItem
	err := r.db.GetContext(
		ctx,
		&img,
		`SELECT * FROM images WHERE type = ? AND deleted_at IS NULL  ORDER BY is_default DESC, created_at DESC`,
		imgType,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &img, err
}

func (r *sqliteRepo) GetByVersionAndType(ctx context.Context, version, imgType string) (*model.ImageItem, error) {
	var img model.ImageItem
	err := r.db.GetContext(
		ctx,
		&img,
		`SELECT * FROM images WHERE version = ? AND type = ? AND deleted_at IS NULL `,
		version,
		imgType,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &img, err
}

func (r *sqliteRepo) GetByName(ctx context.Context, name string) (*model.ImageItem, error) {
	var img model.ImageItem
	err := r.db.GetContext(ctx, &img,
		`SELECT * FROM images WHERE name = ? AND deleted_at IS NULL `, name)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &img, err
}

func (r *sqliteRepo) Count(ctx context.Context) (int, error) {
	var count int
	return count, sqlx.GetContext(ctx, r.db, &count, `SELECT COUNT(*) FROM images WHERE deleted_at IS NULL`)
}

func (r *sqliteRepo) ListAll(ctx context.Context) ([]*model.ImageItem, error) {
	var items []*model.ImageItem
	return items, r.db.SelectContext(ctx, &items,
		`SELECT * FROM images WHERE deleted_at IS NULL ORDER BY created_at`)
}

func (r *sqliteRepo) Upsert(ctx context.Context, img *model.ImageItem) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO images (
			id, type, version, name, distro, arch, path, fs_type, fs_uuid,
			compressed_size, original_size, compression_ratio,
			compressed_format, minimum_rootfs_size_mib, pulled_at, is_default,
			is_present, is_imported, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			type = excluded.type,
			version = excluded.version,
			name = excluded.name,
			distro = excluded.distro,
			arch = excluded.arch,
			path = excluded.path,
			fs_type = excluded.fs_type,
			fs_uuid = excluded.fs_uuid,
			compressed_size = excluded.compressed_size,
			original_size = excluded.original_size,
			compression_ratio = excluded.compression_ratio,
			compressed_format = excluded.compressed_format,
			minimum_rootfs_size_mib = excluded.minimum_rootfs_size_mib,
			pulled_at = excluded.pulled_at,
			is_default = excluded.is_default,
			is_present = excluded.is_present,
			is_imported = excluded.is_imported,
			updated_at = CURRENT_TIMESTAMP`,
		img.ID, img.Type, img.Version, img.Name, img.Distro, img.Arch,
		img.Path, img.FSType, img.FSUUID,
		img.CompressedSize, img.OriginalSize, img.CompressionRatio,
		img.CompressedFormat, img.MinRootfsSizeMiB, img.PulledAt,
		infra.BoolToInt(img.IsDefault), infra.BoolToInt(img.IsPresent),
		infra.BoolToInt(img.IsImported),
		img.CreatedAt, img.UpdatedAt,
	)
	return err
}

func (r *sqliteRepo) SoftDelete(ctx context.Context, imageID string) error {
	now := time.Now().Format(time.RFC3339)
	_, err := r.db.ExecContext(ctx,
		`UPDATE images SET deleted_at = ?, is_present = 0 WHERE id = ?`, now, imageID)
	return err
}

func (r *sqliteRepo) GetDefault(ctx context.Context) (*model.ImageItem, error) {
	var img model.ImageItem
	err := r.db.GetContext(ctx, &img,
		`SELECT * FROM images WHERE is_default = 1 AND deleted_at IS NULL  LIMIT 1`)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &img, err
}

func (r *sqliteRepo) UpdateManyIsPresent(ctx context.Context, ids []string, present bool) error {
	if len(ids) == 0 {
		return nil
	}
	query, args, err := sqlx.In("UPDATE images SET is_present = ?, updated_at = CURRENT_TIMESTAMP WHERE id IN (?)",
		infra.BoolToInt(present), ids)
	if err != nil {
		return err
	}
	query = r.db.Rebind(query)
	_, err = r.db.ExecContext(ctx, query, args...)
	return err
}

func (r *sqliteRepo) Delete(ctx context.Context, imageID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM images WHERE id = ?`, imageID)
	return err
}

func (r *sqliteRepo) SetDefault(ctx context.Context, imageID string) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx,
		`UPDATE images SET is_default = 0 WHERE deleted_at IS NULL`)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx,
		`UPDATE images SET is_default = 1, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND deleted_at IS NULL`, imageID)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func (r *sqliteRepo) ListAllByIDs(ctx context.Context, ids []string) ([]*model.ImageItem, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	query, args, err := sqlx.In("SELECT * FROM images WHERE id IN (?) AND deleted_at IS NULL ORDER BY created_at", ids)
	if err != nil {
		return nil, err
	}
	query = r.db.Rebind(query)
	var items []*model.ImageItem
	return items, r.db.SelectContext(ctx, &items, query, args...)
}
