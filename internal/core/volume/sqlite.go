package volume

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/model"
)

type sqliteRepo struct {
	db *sql.DB
}

// NewRepository creates a new Repository backed by SQLite.
func NewRepository(db *sql.DB) Repository {
	return &sqliteRepo{db: db}
}

// Get returns a volume by its full 64-char ID, or nil if not found.
// Matches Python's Repository.get() exactly:
//
//	@_graceful_read(default=None)
//	def get(self, volume_id: str) -> VolumeItem | None:
//	    with self._db.connect() as conn:
//	        row = conn.execute(
//	            "SELECT * FROM volumes WHERE id = ?", (volume_id,)
//	        ).fetchone()
//	    if row is None:
//	        return None
//	    return VolumeItem(**dict(row))
//
// Python's @_graceful_read catches "no such table" errors and returns None.
// Go equivalent: Return errors directly. Callers must handle "no such table"
// errors (from un-migrated DB) as they would in any other domain.
func (r *sqliteRepo) Get(ctx context.Context, id string) (*model.VolumeItem, error) {
	row := r.db.QueryRowContext(ctx, `SELECT * FROM volumes WHERE id = ?`, id)
	v, err := scanVolume(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return v, err
}

// FindByPrefix returns all volumes whose ID starts with prefix.
// Matches Python's Repository.find_by_prefix() exactly.
func (r *sqliteRepo) FindByPrefix(ctx context.Context, prefix string) ([]*model.VolumeItem, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT * FROM volumes WHERE id LIKE ?`, prefix+"%")
	if err != nil {
		return nil, fmt.Errorf("find by prefix: %w", err)
	}
	defer rows.Close()
	return scanVolumes(rows)
}

// GetByName returns a volume by its name, or nil if not found.
// Matches Python's Repository.get_by_name() exactly.
func (r *sqliteRepo) GetByName(ctx context.Context, name string) (*model.VolumeItem, error) {
	row := r.db.QueryRowContext(ctx, `SELECT * FROM volumes WHERE name = ?`, name)
	v, err := scanVolume(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return v, err
}

// ListAll returns all volumes ordered by created_at.
// Matches Python's Repository.list_all() exactly.
func (r *sqliteRepo) ListAll(ctx context.Context) ([]*model.VolumeItem, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT * FROM volumes ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list volumes: %w", err)
	}
	defer rows.Close()
	return scanVolumes(rows)
}

// Upsert inserts or replaces a volume record matching Python's Repository.upsert() exactly.
func (r *sqliteRepo) Upsert(ctx context.Context, v *model.VolumeItem) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO volumes (
			id, name, size_bytes, format, is_read_only, path, status,
			vm_id, created_at, updated_at
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
		v.ID, v.Name, v.SizeBytes, v.Format, infra.BoolToInt(v.IsReadOnly), v.Path, string(v.Status),
		v.VMID, v.CreatedAt, v.UpdatedAt)
	return err
}

// Delete removes a volume by ID. No-op if not found.
// Matches Python's Repository.delete() exactly.
func (r *sqliteRepo) Delete(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM volumes WHERE id = ?", id)
	return err
}

// FindByIDs returns all volumes matching the given IDs.
// Matches Python's Repository.find_by_ids() exactly.
func (r *sqliteRepo) FindByIDs(ctx context.Context, ids []string) ([]*model.VolumeItem, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := r.db.QueryContext(ctx, `SELECT * FROM volumes WHERE id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("find by ids: %w", err)
	}
	defer rows.Close()
	return scanVolumes(rows)
}

// Count returns the total number of volumes.
// Matches Python's Repository.count() exactly.
func (r *sqliteRepo) Count(ctx context.Context) (int, error) {
	var c int
	err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM volumes").Scan(&c)
	if err != nil {
		return 0, err
	}
	return c, nil
}

// scanVolume scans a single row into a Volume. Returns sql.ErrNoRows if no rows.
// Matches Python's VolumeItem.__post_init__ exactly:
//   - coerces status from raw DB string to Status enum type
//   - coerces is_read_only from SQLite int (0/1) to bool
func scanVolume(row *sql.Row) (*model.VolumeItem, error) {
	var v model.VolumeItem
	var vmID sql.NullString
	var isReadOnly int
	var statusStr string
	var createdAt, updatedAt string
	err := row.Scan(&v.ID, &v.Name, &v.SizeBytes, &v.Format, &isReadOnly, &v.Path, &statusStr, &vmID,
		&createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	if vmID.Valid {
		v.VMID = &vmID.String
	}
	// Coerce status from raw string to VolumeStatus enum
	v.Status = model.VolumeStatus(statusStr)
	// Coerce is_read_only from SQLite int to bool (Python: coerce_bool_fields(self, {"is_read_only"}))
	v.IsReadOnly = isReadOnly == 1
	v.CreatedAt = createdAt
	v.UpdatedAt = updatedAt
	return &v, nil
}

func scanVolumes(rows *sql.Rows) ([]*model.VolumeItem, error) {
	var volumes []*model.VolumeItem
	for rows.Next() {
		var v model.VolumeItem
		var vmID sql.NullString
		var isReadOnly int
		var statusStr string
		var createdAt, updatedAt string
		err := rows.Scan(&v.ID, &v.Name, &v.SizeBytes, &v.Format, &isReadOnly, &v.Path, &statusStr, &vmID,
			&createdAt, &updatedAt)
		if err != nil {
			return nil, fmt.Errorf("scan volume: %w", err)
		}
		if vmID.Valid {
			v.VMID = &vmID.String
		}
		// Coerce status from raw string to VolumeStatus enum
		v.Status = model.VolumeStatus(statusStr)
		// Coerce is_read_only from SQLite int to bool (Python: coerce_bool_fields(self, {"is_read_only"}))
		v.IsReadOnly = isReadOnly == 1
		v.CreatedAt = createdAt
		v.UpdatedAt = updatedAt
		volumes = append(volumes, &v)
	}
	return volumes, rows.Err()
}
