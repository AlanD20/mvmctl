package image

import (
	"context"
	"database/sql"
	"fmt"
	"mvmctl/internal/infra"
	"strings"
	"time"
)

type sqliteRepo struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) Repository {
	return &sqliteRepo{db: db}
}

func (r *sqliteRepo) Get(ctx context.Context, imageID string) (*ImageItem, error) {
	row := r.db.QueryRowContext(ctx, `SELECT * FROM images WHERE id = ?`, imageID)
	img, err := scanImage(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return img, err
}

func (r *sqliteRepo) FindByPrefix(ctx context.Context, prefix string) ([]*ImageItem, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT * FROM images WHERE id LIKE ? AND deleted_at IS NULL AND is_present = 1`,
		prefix+"%",
	)
	if err != nil {
		return nil, fmt.Errorf("find by prefix: %w", err)
	}
	defer rows.Close()
	return scanImages(rows)
}

func (r *sqliteRepo) GetByType(ctx context.Context, imgType string) (*ImageItem, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT * FROM images WHERE type = ? AND deleted_at IS NULL AND is_present = 1 ORDER BY is_default DESC, created_at DESC`,
		imgType,
	)
	img, err := scanImage(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return img, err
}

func (r *sqliteRepo) GetByVersionAndType(ctx context.Context, version, imgType string) (*ImageItem, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT * FROM images WHERE version = ? AND type = ? AND deleted_at IS NULL AND is_present = 1 LIMIT 1`,
		version, imgType,
	)
	img, err := scanImage(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return img, err
}

func (r *sqliteRepo) GetByName(ctx context.Context, name string) (*ImageItem, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT * FROM images WHERE name = ? AND deleted_at IS NULL AND is_present = 1`,
		name,
	)
	img, err := scanImage(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return img, err
}

func (r *sqliteRepo) Count(ctx context.Context) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM images WHERE deleted_at IS NULL`).Scan(&count)
	return count, err
}

func (r *sqliteRepo) ListAll(ctx context.Context) ([]*ImageItem, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT * FROM images WHERE deleted_at IS NULL ORDER BY created_at`,
	)
	if err != nil {
		return nil, fmt.Errorf("list all images: %w", err)
	}
	defer rows.Close()
	return scanImages(rows)
}

func (r *sqliteRepo) Upsert(ctx context.Context, img *ImageItem) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO images (
			id, type, version, name, distro, arch, path, fs_type, fs_uuid,
			compressed_size, original_size, compression_ratio,
			compressed_format, minimum_rootfs_size_mib, pulled_at, is_default,
			is_present, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
			updated_at = CURRENT_TIMESTAMP`,
		img.ID,
		img.Type,
		img.Version,
		img.Name,
		img.Distro,
		img.Arch,
		img.Path,
		img.FSType,
		img.FSUUID,
		img.CompressedSize,
		img.OriginalSize,
		img.CompressionRatio,
		img.CompressedFormat,
		img.MinRootfsSizeMiB,
		img.PulledAt,
		infra.BoolToInt(img.IsDefault),
		infra.BoolToInt(img.IsPresent),
		img.CreatedAt,
		img.UpdatedAt,
	)
	return err
}

func (r *sqliteRepo) SoftDelete(ctx context.Context, imageID string) error {
	now := time.Now().Format(time.RFC3339)
	_, err := r.db.ExecContext(ctx,
		`UPDATE images SET deleted_at = ?, is_present = 0 WHERE id = ?`,
		now, imageID,
	)
	return err
}

func (r *sqliteRepo) Delete(ctx context.Context, imageID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM images WHERE id = ?`, imageID)
	return err
}

func (r *sqliteRepo) SetDefault(ctx context.Context, imageID string) error {
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec(`UPDATE images SET is_default = 0 WHERE deleted_at IS NULL`)
	if err != nil {
		return err
	}
	_, err = tx.Exec(
		`UPDATE images SET is_default = 1 WHERE id = ? AND deleted_at IS NULL`,
		imageID,
	)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (r *sqliteRepo) GetDefault(ctx context.Context) (*ImageItem, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT * FROM images WHERE is_default = 1 AND is_present = 1 LIMIT 1`,
	)
	img, err := scanImage(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return img, err
}

func (r *sqliteRepo) UpdateManyIsPresent(ctx context.Context, imageIDs []string, isPresent bool) error {
	if len(imageIDs) == 0 {
		return nil
	}
	placeholders := strings.Repeat("?,", len(imageIDs))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, 0, len(imageIDs)+1)
	args = append(args, infra.BoolToInt(isPresent))
	for _, id := range imageIDs {
		args = append(args, id)
	}
	_, err := r.db.ExecContext(ctx,
		`UPDATE images SET is_present = ?, updated_at = CURRENT_TIMESTAMP WHERE id IN (`+placeholders+`)`,
		args...,
	)
	return err
}

// scanImage scans a single row into an ImageItem.
// COLUMN ORDER must match the SQL SELECT * order in the images table:
// id, type, version, name, distro, arch, path, fs_type, fs_uuid,
// compressed_size, original_size, compression_ratio, compressed_format,
// minimum_rootfs_size_mib, pulled_at, is_default, is_present, created_at,
// updated_at, deleted_at
func scanImage(scanner interface{ Scan(dest ...any) error }) (*ImageItem, error) {
	var img ImageItem
	var distro, fsUUID, compressedFormat, deletedAt sql.NullString
	var pulledAt, createdAt, updatedAt sql.NullString
	var compressedSize sql.NullInt64
	var compressionRatio sql.NullFloat64
	var isDefault, isPresent int

	err := scanner.Scan(
		&img.ID, &img.Type, &img.Version, &img.Name, &distro,
		&img.Arch, &img.Path, &img.FSType, &fsUUID,
		&compressedSize, &img.OriginalSize, &compressionRatio,
		&compressedFormat, &img.MinRootfsSizeMiB,
		&pulledAt, &isDefault, &isPresent,
		&createdAt, &updatedAt, &deletedAt,
	)
	if err != nil {
		return nil, err
	}

	if distro.Valid {
		img.Distro = &distro.String
	}
	if fsUUID.Valid {
		img.FSUUID = &fsUUID.String
	}
	if compressedSize.Valid {
		v := compressedSize.Int64
		img.CompressedSize = &v
	}
	if compressionRatio.Valid {
		img.CompressionRatio = &compressionRatio.Float64
	}
	if compressedFormat.Valid {
		img.CompressedFormat = &compressedFormat.String
	}
	if pulledAt.Valid {
		img.PulledAt = pulledAt.String
	}
	if createdAt.Valid {
		img.CreatedAt = createdAt.String
	}
	if updatedAt.Valid {
		img.UpdatedAt = updatedAt.String
	}
	if deletedAt.Valid {
		img.DeletedAt = &deletedAt.String
	}
	img.IsDefault = isDefault == 1
	img.IsPresent = isPresent == 1

	return &img, nil
}

func scanImages(rows *sql.Rows) ([]*ImageItem, error) {
	var images []*ImageItem
	for rows.Next() {
		var img ImageItem
		var distro, fsUUID, compressedFormat, deletedAt sql.NullString
		var pulledAt, createdAt, updatedAt sql.NullString
		var compressedSize sql.NullInt64
		var compressionRatio sql.NullFloat64
		var isDefault, isPresent int

		err := rows.Scan(
			&img.ID, &img.Type, &img.Version, &img.Name, &distro,
			&img.Arch, &img.Path, &img.FSType, &fsUUID,
			&compressedSize, &img.OriginalSize, &compressionRatio,
			&compressedFormat, &img.MinRootfsSizeMiB,
			&pulledAt, &isDefault, &isPresent,
			&createdAt, &updatedAt, &deletedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan image: %w", err)
		}

		if distro.Valid {
			img.Distro = &distro.String
		}
		if fsUUID.Valid {
			img.FSUUID = &fsUUID.String
		}
		if compressedSize.Valid {
			v := compressedSize.Int64
			img.CompressedSize = &v
		}
		if compressionRatio.Valid {
			img.CompressionRatio = &compressionRatio.Float64
		}
		if compressedFormat.Valid {
			img.CompressedFormat = &compressedFormat.String
		}
		if pulledAt.Valid {
			img.PulledAt = pulledAt.String
		}
		if createdAt.Valid {
			img.CreatedAt = createdAt.String
		}
		if updatedAt.Valid {
			img.UpdatedAt = updatedAt.String
		}
		if deletedAt.Valid {
			img.DeletedAt = &deletedAt.String
		}
		img.IsDefault = isDefault == 1
		img.IsPresent = isPresent == 1

		images = append(images, &img)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration: %w", err)
	}
	return images, nil
}
