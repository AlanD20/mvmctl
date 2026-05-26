package kernel

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

func (r *sqliteRepo) Get(ctx context.Context, id string) (*model.KernelItem, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT * FROM kernels WHERE id = ? AND deleted_at IS NULL AND is_present = 1`, id)
	k, err := scanKernel(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return k, err
}

func (r *sqliteRepo) FindByPrefix(ctx context.Context, prefix string) ([]*model.KernelItem, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT * FROM kernels WHERE id LIKE ? AND deleted_at IS NULL AND is_present = 1`, prefix+"%")
	if err != nil {
		return nil, fmt.Errorf("find kernels by prefix: %w", err)
	}
	defer rows.Close()
	return scanKernels(rows)
}

func (r *sqliteRepo) Count(ctx context.Context) (int, error) {
	var c int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM kernels WHERE deleted_at IS NULL`).Scan(&c)
	return c, err
}

func (r *sqliteRepo) ListAll(ctx context.Context) ([]*model.KernelItem, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT * FROM kernels WHERE deleted_at IS NULL ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list kernels: %w", err)
	}
	defer rows.Close()
	return scanKernels(rows)
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
			updated_at = CURRENT_TIMESTAMP`,
		k.ID, k.Name, k.BaseName, k.Version, k.Arch, k.Type, k.Path,
		infra.BoolToInt(k.IsDefault), infra.BoolToInt(k.IsPresent),
		k.CreatedAt, k.UpdatedAt, k.DeletedAt,
	)
	return err
}

func (r *sqliteRepo) SoftDelete(ctx context.Context, id string) error {
	now := time.Now().Format(time.RFC3339)
	_, err := r.db.ExecContext(ctx,
		`UPDATE kernels SET deleted_at = ?, is_present = 0 WHERE id = ?`, now, id)
	return err
}

func (r *sqliteRepo) Delete(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM kernels WHERE id = ?`, id)
	return err
}

func (r *sqliteRepo) SetDefault(ctx context.Context, id string) error {
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`UPDATE kernels SET is_default = 0 WHERE deleted_at IS NULL`)
	if err != nil {
		return err
	}
	_, err = tx.Exec(
		`UPDATE kernels SET is_default = 1 WHERE id = ? AND deleted_at IS NULL`, id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (r *sqliteRepo) GetDefault(ctx context.Context) (*model.KernelItem, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT * FROM kernels WHERE is_default = 1 AND deleted_at IS NULL AND is_present = 1 LIMIT 1`)
	k, err := scanKernel(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return k, err
}

func (r *sqliteRepo) GetByName(ctx context.Context, name string) (*model.KernelItem, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT * FROM kernels WHERE name = ? AND deleted_at IS NULL AND is_present = 1 LIMIT 1`, name)
	k, err := scanKernel(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return k, err
}

func (r *sqliteRepo) GetByType(ctx context.Context, kernelType string) (*model.KernelItem, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT * FROM kernels WHERE type = ? AND deleted_at IS NULL AND is_present = 1 LIMIT 1`, kernelType)
	k, err := scanKernel(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return k, err
}

func (r *sqliteRepo) GetByVersionAndType(ctx context.Context, version, kernelType string) (*model.KernelItem, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT * FROM kernels WHERE version = ? AND type = ? AND deleted_at IS NULL AND is_present = 1 LIMIT 1`,
		version, kernelType)
	k, err := scanKernel(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return k, err
}

func (r *sqliteRepo) UpdateManyIsPresent(ctx context.Context, ids []string, isPresent bool) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, 0, len(ids)+1)
	args = append(args, infra.BoolToInt(isPresent))
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := r.db.ExecContext(ctx,
		`UPDATE kernels SET is_present = ?, updated_at = CURRENT_TIMESTAMP WHERE id IN (`+placeholders+`)`,
		args...)
	return err
}

func scanKernel(scanner interface{ Scan(dest ...any) error }) (*model.KernelItem, error) {
	var k model.KernelItem
	var deletedAt sql.NullString
	// Scan bool-ish columns as interface{} then coerce, matching Python's
	// KernelItem.__post_init__() which calls CommonUtils.coerce_bool_fields()
	// to handle both int (0/1) and string ("0"/"1"/"true"/"false") representations
	// from SQLite.
	var isDefaultRaw, isPresentRaw any
	err := scanner.Scan(
		&k.ID, &k.Name, &k.BaseName, &k.Version, &k.Arch, &k.Type,
		&k.Path, &isDefaultRaw, &isPresentRaw,
		&k.CreatedAt, &k.UpdatedAt, &deletedAt,
	)
	if err != nil {
		return nil, err
	}
	k.IsDefault = coerceBool(isDefaultRaw)
	k.IsPresent = coerceBool(isPresentRaw)
	if deletedAt.Valid {
		k.DeletedAt = &deletedAt.String
	}
	return &k, nil
}

// coerceBool converts a value from SQLite (int64, int, float64, string) to bool,
// matching Python's CommonUtils.coerce_bool_fields() behavior:
//   - int/float: non-zero → true
//   - string: "1" or "true" (case-insensitive) → true; everything else → false
//   - bool: returned as-is
func coerceBool(v any) bool {
	switch val := v.(type) {
	case bool:
		return val
	case int64:
		return val != 0
	case int:
		return val != 0
	case float64:
		return val != 0
	case string:
		return val == "1" || strings.ToLower(val) == "true"
	}
	return false
}

func scanKernels(rows *sql.Rows) ([]*model.KernelItem, error) {
	var kernels []*model.KernelItem
	for rows.Next() {
		k, err := scanKernel(rows)
		if err != nil {
			return nil, fmt.Errorf("scan kernel: %w", err)
		}
		kernels = append(kernels, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration: %w", err)
	}
	return kernels, nil
}
