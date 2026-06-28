# Snapshot Functionality

## Problem

Firecracker snapshots are a raw API passthrough. Without a managed snapshot domain, there is no way to list available snapshots, track which VM a snapshot came from, or restore a snapshot into a new VM with proper network identity. The user must specify raw file paths for memory and state dumps with no directory management, no rootfs tracking, and no metadata persistence.

## Architecture

The snapshot domain has its own DB table (`snapshots`), cache directory (`~/.cache/mvm/snapshots/<id>/`), CLI commands, and API layer. Snapshots are managed entities — captured atomically, stored in a known location, restorable by name.

### Key characteristics

- **No Controller** — snapshots are immutable after creation, so no state machine is needed. The repository handles DB CRUD, and filesystem operations are handled directly by the API layer.
- **Self-contained domain** — the snapshot domain imports `internal/lib/firecracker` to call Pause/CreateSnapshot/Resume/LoadSnapshot directly. No cross-core package imports.
- **Orchestration in API layer** — the API layer's `Operation` methods coordinate VM pausing, rootfs copying, network allocation, and Firecracker spawn. This matches the existing pattern where the API layer orchestrates multiple domains.

## Entry point

Snapshot operations are triggered from the CLI commands in `internal/cli/snapshot.go`:

- `mvm snapshot create <vm>` — calls `op.SnapshotCreate()` in `pkg/api/snapshot.go`
- `mvm snapshot restore <id> <name>` — calls `op.SnapshotRestore()`
- `mvm snapshot ls` — calls `op.SnapshotList()`
- `mvm snapshot inspect <id>` — calls `op.SnapshotInspect()`
- `mvm snapshot rm <id>` — calls `op.SnapshotRemove()`

The API layer orchestrates all cross-domain operations (VM pause/resume, rootfs copy, network allocation, Firecracker spawn). The snapshot repository in `internal/core/snapshot/` handles DB CRUD. There is no service layer — snapshots are immutable after creation.

## Happy path: Snapshot create

### 1. Resolve and enrich

`op.SnapshotCreate()` resolves the VM by identifier and enriches it with all relations (kernel, image, binary, network) via the enricher.

### 2. Generate snapshot ID

A deterministic snapshot ID is generated via `crypto.SnapshotID(sourceVMID, timestamp)` — SHA of the source VM ID concatenated with the timestamp.

### 3. Create snapshot directory

The snapshot cache directory is created at `~/.cache/mvm/snapshots/<id>/`.

### 4. Copy rootfs

The source VM's rootfs is copied to `snapDir/rootfs.ext4` via `infra.CopyFile()` (file-level copy with sparse support). This gives a point-in-time consistent rootfs — the source VM continues running, so referencing the original path risks inconsistency.

### 5. Create phantom symlink

A symlink `snapDir/phantom-rootfs.ext4` → `rootfs.ext4` is created. This symlink is critical for making snapshots independent of the source VM's rootfs path.

### 6. Pause VM and patch drive path

The API layer calls `firecracker.PauseVM()` to pause the source VM. It then calls `PATCH /drives/rootfs` to change the running VM's drive path to the phantom symlink. Because the VM is paused, no I/O is in flight.

### 7. Create snapshot

The API layer calls `firecracker.CreateSnapshot(memPath, statePath)`. The vmstate file captures the phantom symlink path (not the source VM's original rootfs path).

### 8. Restore original drive path and resume

The API layer calls `PATCH /drives/rootfs` to restore the original path, then resumes the VM (unless `--pause` is specified).

### 9. Insert DB record

The snapshot metadata is inserted into the `snapshots` table via `snapshotRepo.Upsert()`.

If any step fails, the snapshot directory is cleaned up and no DB record is created — atomic create.

## Happy path: Snapshot restore

### 1. Resolve snapshot

The snapshot is resolved by ID (supports prefix matching, errors on ambiguity).

### 2. Load metadata

Snapshot metadata is loaded from the DB, including kernel/network/binary IDs.

### 3. For each VM to restore

For each clone (controlled by `--count`, default 1):

1. Generate new VM ID
2. Copy `snapDir/rootfs.ext4` → `vms/<new-id>/rootfs.ext4`
3. Acquire exclusive flock on `snapDir/.restore.lock` (serializes concurrent restores from the same snapshot)
4. Replace `snapDir/phantom-rootfs.ext4` → symlink → new VM's rootfs
5. Create VM record with Stopped status, wired kernel/binary/network
6. Create VM directory and Firecracker config
7. Spawn Firecracker in snapshot mode
8. Call `firecracker.LoadSnapshot()` with mem/state paths, network overrides, and vsock override
9. Release flock on `.restore.lock`

### Phantom symlink

The phantom symlink makes snapshots independent of the source VM:

- **During create**: PATCH the running VM's drive to point to the symlink before taking the snapshot. The vmstate captures the snapshot-local symlink path.
- **During restore**: Replace the symlink to point to the new VM's rootfs copy. LoadSnapshot follows the symlink and finds the correct backing file.

Firecracker's `PUT /snapshot/load` does not support a block-device-path override — the backing file path recorded in the vmstate is used directly. The phantom symlink works around this constraint.

### Concurrent restore safety

The `.restore.lock` file serializes concurrent `mvm snapshot restore` invocations from the same snapshot. The lock is acquired before the phantom symlink is updated and released after LoadSnapshot completes. `flock()` is used because it is automatically released on process exit.

### Vsock override

Firecracker's `PUT /snapshot/load` supports `vsock_override` to change the vsock UDS path at load time. Without it, the vmstate's recorded UDS path would collide with the source VM's socket. The vsock guest CID is set via a separate `PUT /vsock` call after LoadSnapshot.

### Network identity

Each clone gets a fresh MAC and IP from the target network (specified via `--network`, defaulting to the snapshot's original network). The guest's in-memory network config from the snapshot will be stale (it remembers the old IP), but DHCP/cloud-init guest-side scripts can re-apply.

## DB schema

The `snapshots` table (in `001_initial_schema.sql`) stores:

| Column | Type | Description |
|--------|------|-------------|
| `id` | TEXT PRIMARY KEY | `crypto.SnapshotID(source_vm_id, timestamp)` |
| `name` | TEXT | User-provided name (defaults to `<source-vm>-<timestamp>`) |
| `source_vm_id` | TEXT | Source VM ID |
| `source_vm_name` | TEXT | Source VM name (denormalized for display) |
| `snapshot_dir` | TEXT | Absolute path to `cache/snapshots/<id>/` |
| `memory_file` | TEXT | Path to memory dump file within snapshot dir |
| `state_file` | TEXT | Path to vmstate file within snapshot dir |
| `rootfs_file` | TEXT | Path to rootfs copy within snapshot dir |
| `kernel_id` | TEXT | Kernel ID used at snapshot time |
| `network_id` | TEXT | Network ID used at snapshot time |
| `binary_id` | TEXT | Firecracker binary ID used at snapshot time |
| `vcpu_count` | INTEGER | vCPU count from source VM |
| `mem_size_mib` | INTEGER | Memory size from source VM |
| `disk_size_mib` | INTEGER | Rootfs size from source VM |
| `image_id` | TEXT | Image ID |
| `ssh_keys` | TEXT | SSH key names (JSON array) |
| `ssh_user` | TEXT | SSH user (nullable) |
| `extra_config` | TEXT | Full Firecracker boot config (JSON blob) |
| `created_at` | TEXT | ISO 8601 timestamp |
| `updated_at` | TEXT | ISO 8601 timestamp |

## Snapshot config structs

Defined in `internal/lib/model/snapshot.go`:

```go
type SnapshotCreateConfig struct {
    MemFile           string
    StateFile         string
    PauseOnly         bool
    PhantomRootfsPath string // symlink path for vmstate (empty = skip PATCH)
    RootfsPath        string // original path to restore after snapshot
}

type SnapshotRestoreConfig struct {
    MemFile          string
    StateFile        string
    Resume           bool
    NetworkOverrides map[string]string // iface_id → host_dev_name
    VsockUDSPath     string            // vsock_override UDS path
    RootfsPath       string            // rootfs path for post-Load PATCH
}
```

## Failure modes

### Firecracker constraints on restore

**vCPU and memory cannot be changed when restoring a snapshot.** The snapshot captures complete KVM vCPU register state and guest memory layout. Loading into a differently configured Firecracker would be an incompatible state restore. The machine config must be set before `/snapshot/load` and must match what was captured.

**Block device path is hardcoded in vmstate.** Firecracker does not support a block-device-path override on `PUT /snapshot/load` (unlike `network_overrides` and `vsock_override`). The phantom symlink works around this by ensuring the vmstate always references the symlink path in the snapshot directory.

### Partial failure on create

If any step fails during snapshot creation, the snapshot directory is removed and no DB record is inserted. The operation is atomic.

### Stale restore lock

The `.restore.lock` file uses `flock()` which is automatically released on process exit. There are no orphaned lock files.

### Snapshot removal does not affect VMs

`mvm snapshot rm` removes the snapshot directory and DB record. It does not touch any running VM. Snapshots and VMs are independent entities.

### Reference counting for delete protection

Before deleting a kernel, network, or binary, the API layer checks `snapshotRepo.CountByKernelID()` / `CountByNetworkID()` / `CountByBinaryID()` to see if any snapshot references the entity. If references exist, the entity is soft-deleted. This check happens in the API layer because the snapshot repository cannot be imported by other core domains (core domains never import other core packages).

## Key files

| File | Purpose |
|------|---------|
| `internal/core/snapshot/repository.go` | Repository interface: CRUD + reference counting |
| `internal/core/snapshot/sqlite.go` | SQLite implementation of snapshot repository |
| `internal/core/snapshot/resolver.go` | Entity resolution by identifier |
| `internal/lib/model/snapshot.go` | `SnapshotItem`, `SnapshotCreateConfig`, `SnapshotRestoreConfig` |
| `pkg/api/snapshot.go` | API orchestration: `SnapshotCreate()`, `SnapshotRestore()`, `SnapshotList()`, `SnapshotInspect()`, `SnapshotRemove()` |
| `pkg/api/inputs/snapshot_input.go` | Input structs: `SnapshotCreateInput`, `SnapshotRestoreInput`, `SnapshotInput` |
| `internal/lib/firecracker/client.go` | Firecracker HTTP client: `PauseVM()`, `CreateSnapshot()`, `ResumeVM()`, `LoadSnapshot()` |
| `internal/cli/snapshot.go` | Cobra commands: `mvm snapshot create\|ls\|inspect\|restore\|rm` |

## Design decisions

**No Controller — snapshots are immutable.** Snapshots have no state machine (they cannot be modified after creation). A Controller adds complexity for no benefit. The API layer handles orchestration directly.

**Phantom symlink over source VM rootfs reference.** The phantom symlink makes the snapshot self-contained and restorable without the source VM. During restore, the symlink is updated to point to the new VM's rootfs copy. The `flock` serializes the update + LoadSnapshot window.

**DB over filesystem for config storage.** The Firecracker boot config is stored in the `extra_config` DB column rather than as a `config.json` file. The enricher enriches it at restore time. This avoids file management complexity.

**`infra.CopyFile()` for rootfs copy.** Uses file-level copy with sparse support. A new subprocess call is unnecessary for this operation.

**Snapshots don't pin images.** A snapshot stores `image_id`, `kernel_id`, `network_id`, and `binary_id` for metadata and enrichment, but the rootfs is copied at snapshot time — the snapshot can be restored without the original image.

**Vsock override + separate CID update.** The vsock UDS path is overridable via `vsock_override` on `/snapshot/load`, but the guest CID requires a separate `PUT /vsock` call after load.
