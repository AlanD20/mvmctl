package binary

import (
	"context"
	"database/sql"
	"fmt"
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

func (r *sqliteRepo) Get(ctx context.Context, id string) (*model.BinaryItem, error) {
	var b model.BinaryItem
	err := r.db.GetContext(ctx, &b,
		`SELECT * FROM binaries WHERE id = ? AND deleted_at IS NULL `, id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &b, err
}

func (r *sqliteRepo) FindByPrefix(
	ctx context.Context,
	prefix string,
	architecture string,
	includeDeleted ...bool,
) ([]*model.BinaryItem, error) {
	query := `SELECT * FROM binaries WHERE id LIKE ? AND architecture = ?`
	if len(includeDeleted) == 0 || !includeDeleted[0] {
		query += ` AND deleted_at IS NULL`
	}
	var items []*model.BinaryItem
	return items, r.db.SelectContext(ctx, &items, query, prefix+"%", architecture)
}

func (r *sqliteRepo) ListAll(ctx context.Context, architecture string) ([]*model.BinaryItem, error) {
	var items []*model.BinaryItem
	return items, r.db.SelectContext(ctx, &items,
		`SELECT * FROM binaries WHERE architecture = ? AND deleted_at IS NULL ORDER BY created_at`, architecture)
}

func (r *sqliteRepo) ListByType(ctx context.Context, typ, architecture string) ([]*model.BinaryItem, error) {
	var items []*model.BinaryItem
	return items, r.db.SelectContext(ctx, &items,
		`SELECT * FROM binaries
		 WHERE type = ? AND architecture = ? AND deleted_at IS NULL
		 ORDER BY created_at`, typ, architecture)
}

func (r *sqliteRepo) GetByTypeAndVersion(
	ctx context.Context,
	typ string,
	architecture string,
	version string,
) (*model.BinaryItem, error) {
	var b model.BinaryItem
	err := r.db.GetContext(
		ctx,
		&b,
		`SELECT * FROM binaries
		 WHERE type = ? AND architecture = ? AND version = ? AND deleted_at IS NULL`,
		typ,
		architecture,
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
			id, type, architecture, version, full_version, ci_version, path,
			is_default, is_present, created_at, updated_at, deleted_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			type = excluded.type,
			architecture = excluded.architecture,
			version = excluded.version,
			full_version = excluded.full_version,
			ci_version = excluded.ci_version,
			path = excluded.path,
			is_default = excluded.is_default,
			is_present = excluded.is_present,
			updated_at = CURRENT_TIMESTAMP,
			deleted_at = excluded.deleted_at`,
		b.ID, b.Type, b.Architecture, b.Version, b.FullVersion, b.CIVersion, b.Path,
		infra.BoolToInt(b.IsDefault), infra.BoolToInt(b.IsPresent),
		b.CreatedAt, b.UpdatedAt, b.DeletedAt,
	)
	return err
}

func (r *sqliteRepo) Delete(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM binaries WHERE id = ?`, id)
	return err
}

func (r *sqliteRepo) DeleteByType(ctx context.Context, typ string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM binaries WHERE type = ?`, typ)
	return err
}

func (r *sqliteRepo) DeleteByTypeAndVersion(ctx context.Context, typ, version string) error {
	normalized := strings.TrimPrefix(version, "v")
	prefixed := "v" + normalized
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM binaries WHERE type = ? AND (version = ? OR version = ?)`,
		typ, normalized, prefixed)
	return err
}

func (r *sqliteRepo) SetDefault(ctx context.Context, typ, architecture, id string) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx,
		`UPDATE binaries SET is_default = 0
		 WHERE type = ? AND architecture = ? AND deleted_at IS NULL`, typ, architecture)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE binaries SET is_default = 1, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND type = ? AND architecture = ? AND deleted_at IS NULL`, id, typ, architecture)
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

func (r *sqliteRepo) GetDefault(ctx context.Context, typ, architecture string) (*model.BinaryItem, error) {
	var b model.BinaryItem
	err := r.db.GetContext(ctx, &b,
		`SELECT * FROM binaries
		 WHERE type = ? AND architecture = ? AND is_default = 1 AND deleted_at IS NULL`, typ, architecture)
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

func (r *sqliteRepo) ReplaceReleaseProjection(
	ctx context.Context,
	architecture string,
	version string,
	pair *model.BinaryPair,
	setDefault bool,
) error {
	if err := validateReleaseProjectionPair(architecture, version, pair); err != nil {
		return err
	}

	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		// Rollback is intentionally best-effort after Commit; sql.Tx reports sql.ErrTxDone.
		_ = tx.Rollback()
	}()

	members := []*model.BinaryItem{pair.Firecracker, pair.Jailer}
	for _, member := range members {
		var wasDefault bool
		if err := tx.GetContext(
			ctx,
			&wasDefault,
			`SELECT EXISTS(
				SELECT 1 FROM binaries
				WHERE type = ? AND architecture = ? AND version = ?
				  AND deleted_at IS NULL AND is_default = 1
			)`,
			member.Type,
			architecture,
			version,
		); err != nil {
			return err
		}
		memberDefault := setDefault || wasDefault

		if _, err := tx.ExecContext(
			ctx,
			`UPDATE binaries
			 SET is_default = 0,
			     is_present = 0,
			     deleted_at = COALESCE(deleted_at, CURRENT_TIMESTAMP),
			     updated_at = CURRENT_TIMESTAMP
			 WHERE type = ? AND architecture = ? AND version = ?
			   AND deleted_at IS NULL AND id <> ?`,
			member.Type,
			architecture,
			version,
			member.ID,
		); err != nil {
			return err
		}
		if memberDefault {
			if _, err := tx.ExecContext(
				ctx,
				`UPDATE binaries SET is_default = 0, updated_at = CURRENT_TIMESTAMP
				 WHERE type = ? AND architecture = ? AND deleted_at IS NULL`,
				member.Type,
				architecture,
			); err != nil {
				return err
			}
		}
		if err := upsertReleaseProjectionMember(ctx, tx, member, memberDefault); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (r *sqliteRepo) RemoveReleaseProjection(ctx context.Context, architecture, version string) error {
	_, err := r.db.ExecContext(
		ctx,
		`UPDATE binaries
		 SET is_default = 0,
		     is_present = 0,
		     deleted_at = COALESCE(deleted_at, CURRENT_TIMESTAMP),
		     updated_at = CURRENT_TIMESTAMP
		 WHERE architecture = ? AND version = ? AND deleted_at IS NULL`,
		architecture,
		version,
	)
	return err
}

func validateReleaseProjectionPair(architecture, version string, pair *model.BinaryPair) error {
	if architecture == "" || version == "" || pair == nil || pair.Firecracker == nil || pair.Jailer == nil {
		return fmt.Errorf("complete binary release projection is required")
	}
	if pair.Firecracker.ID == pair.Jailer.ID {
		return fmt.Errorf("binary release projection members require independent IDs")
	}
	for _, member := range []*model.BinaryItem{pair.Firecracker, pair.Jailer} {
		if member.ID == "" || member.Architecture != architecture || member.Version != version ||
			!member.IsPresent || member.DeletedAt != nil {
			return fmt.Errorf("binary release projection member does not match slot %s/%s", architecture, version)
		}
	}
	if pair.Firecracker.Type != "firecracker" || pair.Jailer.Type != "jailer" {
		return fmt.Errorf("binary release projection requires Firecracker and Jailer members")
	}
	return nil
}

func upsertReleaseProjectionMember(
	ctx context.Context,
	tx *sqlx.Tx,
	member *model.BinaryItem,
	isDefault bool,
) error {
	_, err := tx.ExecContext(
		ctx,
		`INSERT INTO binaries (
			id, type, architecture, version, full_version, ci_version, path,
			is_default, is_present, created_at, updated_at, deleted_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, NULL)
		ON CONFLICT(id) DO UPDATE SET
			type = excluded.type,
			architecture = excluded.architecture,
			version = excluded.version,
			full_version = excluded.full_version,
			ci_version = excluded.ci_version,
			path = excluded.path,
			is_default = excluded.is_default,
			is_present = 1,
			updated_at = excluded.updated_at,
			deleted_at = NULL`,
		member.ID,
		member.Type,
		member.Architecture,
		member.Version,
		member.FullVersion,
		member.CIVersion,
		member.Path,
		infra.BoolToInt(isDefault),
		member.CreatedAt,
		member.UpdatedAt,
	)
	return err
}
