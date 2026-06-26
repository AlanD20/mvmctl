package snapshot

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jmoiron/sqlx"

	"mvmctl/internal/lib/model"
)

type sqliteRepo struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) Repository {
	return &sqliteRepo{db: db}
}

const snapshotBaseQuery = "SELECT * FROM snapshots"

// --- Basic CRUD ---

func (r *sqliteRepo) Get(ctx context.Context, id string) (*model.SnapshotItem, error) {
	var item model.SnapshotItem
	err := sqlx.GetContext(ctx, r.db, &item, snapshotBaseQuery+" WHERE id = ?", id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &item, err
}

func (r *sqliteRepo) FindByPrefix(ctx context.Context, prefix string) ([]*model.SnapshotItem, error) {
	var rows []*model.SnapshotItem
	if err := sqlx.SelectContext(ctx, r.db, &rows, snapshotBaseQuery+" WHERE id LIKE ?", prefix+"%"); err != nil {
		return nil, fmt.Errorf("find snapshot by prefix: %w", err)
	}
	return rows, nil
}

func (r *sqliteRepo) GetByName(ctx context.Context, name string) (*model.SnapshotItem, error) {
	var item model.SnapshotItem
	err := sqlx.GetContext(ctx, r.db, &item, snapshotBaseQuery+" WHERE name = ?", name)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &item, err
}

func (r *sqliteRepo) ListAll(ctx context.Context) ([]*model.SnapshotItem, error) {
	var rows []*model.SnapshotItem
	if err := sqlx.SelectContext(ctx, r.db, &rows, snapshotBaseQuery+" ORDER BY created_at"); err != nil {
		return nil, fmt.Errorf("list all snapshots: %w", err)
	}
	return rows, nil
}

// --- Mutations ---

func (r *sqliteRepo) Upsert(ctx context.Context, item *model.SnapshotItem) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO snapshots (
			id, name, source_vm_id, source_vm_name,
			snapshot_dir, memory_file, state_file, rootfs_file,
			image_id, kernel_id, network_id, binary_id,
			vcpu_count, mem_size_mib, disk_size_mib,
			ssh_keys, ssh_user, extra_config,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			source_vm_id = excluded.source_vm_id,
			source_vm_name = excluded.source_vm_name,
			snapshot_dir = excluded.snapshot_dir,
			memory_file = excluded.memory_file,
			state_file = excluded.state_file,
			rootfs_file = excluded.rootfs_file,
			image_id = excluded.image_id,
			kernel_id = excluded.kernel_id,
			network_id = excluded.network_id,
			binary_id = excluded.binary_id,
			vcpu_count = excluded.vcpu_count,
			mem_size_mib = excluded.mem_size_mib,
			disk_size_mib = excluded.disk_size_mib,
			ssh_keys = excluded.ssh_keys,
			ssh_user = excluded.ssh_user,
			extra_config = excluded.extra_config,
			updated_at = CURRENT_TIMESTAMP
	`,
		item.ID,
		item.Name,
		item.SourceVMID,
		item.SourceVMName,
		item.SnapshotDir,
		item.MemoryFile,
		item.StateFile,
		item.RootfsFile,
		item.ImageID,
		item.KernelID,
		item.NetworkID,
		item.BinaryID,
		item.VCPUCount,
		item.MemSizeMiB,
		item.DiskSizeMiB,
		item.SSHKeys,
		item.SSHUser,
		item.ExtraConfig,
		item.CreatedAt,
		item.UpdatedAt,
	)
	return err
}

func (r *sqliteRepo) Delete(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM snapshots WHERE id = ?", id)
	return err
}

// --- Reference counting ---

func (r *sqliteRepo) CountByKernelID(ctx context.Context, kernelID string) (int, error) {
	var c int
	if err := sqlx.GetContext(
		ctx,
		r.db,
		&c,
		"SELECT COUNT(*) FROM snapshots WHERE kernel_id = ?",
		kernelID,
	); err != nil {
		return 0, err
	}
	return c, nil
}

func (r *sqliteRepo) CountByNetworkID(ctx context.Context, networkID string) (int, error) {
	var c int
	if err := sqlx.GetContext(
		ctx,
		r.db,
		&c,
		"SELECT COUNT(*) FROM snapshots WHERE network_id = ?",
		networkID,
	); err != nil {
		return 0, err
	}
	return c, nil
}

func (r *sqliteRepo) CountByBinaryID(ctx context.Context, binaryID string) (int, error) {
	var c int
	if err := sqlx.GetContext(
		ctx,
		r.db,
		&c,
		"SELECT COUNT(*) FROM snapshots WHERE binary_id = ?",
		binaryID,
	); err != nil {
		return 0, err
	}
	return c, nil
}

// --- Reference queries ---

func (r *sqliteRepo) FindByKernelID(ctx context.Context, kernelID string) ([]*model.SnapshotItem, error) {
	var rows []*model.SnapshotItem
	if err := sqlx.SelectContext(ctx, r.db, &rows, snapshotBaseQuery+" WHERE kernel_id = ?", kernelID); err != nil {
		return nil, fmt.Errorf("find snapshots by kernel id: %w", err)
	}
	return rows, nil
}

func (r *sqliteRepo) FindByKernelIDs(ctx context.Context, kernelIDs []string) ([]*model.SnapshotItem, error) {
	if len(kernelIDs) == 0 {
		return nil, nil
	}
	query, args, err := sqlx.In(snapshotBaseQuery+" WHERE kernel_id IN (?)", kernelIDs)
	if err != nil {
		return nil, fmt.Errorf("build kernel IDs query: %w", err)
	}
	var rows []*model.SnapshotItem
	if err := sqlx.SelectContext(ctx, r.db, &rows, r.db.Rebind(query), args...); err != nil {
		return nil, fmt.Errorf("find snapshots by kernel IDs: %w", err)
	}
	return rows, nil
}

func (r *sqliteRepo) FindByNetworkID(ctx context.Context, networkID string) ([]*model.SnapshotItem, error) {
	var rows []*model.SnapshotItem
	if err := sqlx.SelectContext(ctx, r.db, &rows, snapshotBaseQuery+" WHERE network_id = ?", networkID); err != nil {
		return nil, fmt.Errorf("find snapshots by network id: %w", err)
	}
	return rows, nil
}

func (r *sqliteRepo) FindByNetworkIDs(ctx context.Context, networkIDs []string) ([]*model.SnapshotItem, error) {
	if len(networkIDs) == 0 {
		return nil, nil
	}
	query, args, err := sqlx.In(snapshotBaseQuery+" WHERE network_id IN (?)", networkIDs)
	if err != nil {
		return nil, fmt.Errorf("build network IDs query: %w", err)
	}
	var rows []*model.SnapshotItem
	if err := sqlx.SelectContext(ctx, r.db, &rows, r.db.Rebind(query), args...); err != nil {
		return nil, fmt.Errorf("find snapshots by network IDs: %w", err)
	}
	return rows, nil
}

func (r *sqliteRepo) FindByBinaryID(ctx context.Context, binaryID string) ([]*model.SnapshotItem, error) {
	var rows []*model.SnapshotItem
	if err := sqlx.SelectContext(ctx, r.db, &rows, snapshotBaseQuery+" WHERE binary_id = ?", binaryID); err != nil {
		return nil, fmt.Errorf("find snapshots by binary id: %w", err)
	}
	return rows, nil
}

func (r *sqliteRepo) FindByBinaryIDs(ctx context.Context, binaryIDs []string) ([]*model.SnapshotItem, error) {
	if len(binaryIDs) == 0 {
		return nil, nil
	}
	query, args, err := sqlx.In(snapshotBaseQuery+" WHERE binary_id IN (?)", binaryIDs)
	if err != nil {
		return nil, fmt.Errorf("build binary IDs query: %w", err)
	}
	var rows []*model.SnapshotItem
	if err := sqlx.SelectContext(ctx, r.db, &rows, r.db.Rebind(query), args...); err != nil {
		return nil, fmt.Errorf("find snapshots by binary IDs: %w", err)
	}
	return rows, nil
}
