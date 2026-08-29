# Snapshot Functionality

This document explains how mvmctl creates, restores, persists, and removes Firecracker snapshots, including the
transitional behavior that must be replaced before v0.3 release signoff.

## Table of Contents

- [Problem](#problem)
- [Architecture](#architecture)
  - [Key characteristics](#key-characteristics)
- [Entry point](#entry-point)
- [Happy path: Snapshot create](#happy-path-snapshot-create)
  - [1. Resolve and enrich](#1-resolve-and-enrich)
  - [2. Generate snapshot ID](#2-generate-snapshot-id)
  - [3. Create snapshot directory](#3-create-snapshot-directory)
  - [4. Copy rootfs](#4-copy-rootfs)
  - [5. Create phantom symlink](#5-create-phantom-symlink)
  - [6. Pause VM and patch drive path](#6-pause-vm-and-patch-drive-path)
  - [7. Create snapshot](#7-create-snapshot)
  - [8. Restore original drive path and resume](#8-restore-original-drive-path-and-resume)
  - [9. Insert DB record](#9-insert-db-record)
- [Happy path: Snapshot restore](#happy-path-snapshot-restore)
  - [1. Resolve snapshot](#1-resolve-snapshot)
  - [2. Load metadata](#2-load-metadata)
  - [3. Derive managed artifacts](#3-derive-managed-artifacts)
  - [4. For each VM to restore](#4-for-each-vm-to-restore)
  - [Phantom symlink](#phantom-symlink)
  - [Concurrent restore safety](#concurrent-restore-safety)
  - [Vsock override](#vsock-override)
  - [Network identity](#network-identity)
- [DB schema](#db-schema)
- [Snapshot config structs](#snapshot-config-structs)
- [Failure modes](#failure-modes)
  - [Firecracker constraints on restore](#firecracker-constraints-on-restore)
  - [Partial failure on create](#partial-failure-on-create)
  - [Stale restore lock](#stale-restore-lock)
  - [Snapshot removal does not affect VMs](#snapshot-removal-does-not-affect-vms)
  - [Reference counting for delete protection](#reference-counting-for-delete-protection)
- [Key files](#key-files)
- [Design decisions](#design-decisions)

## Problem

Firecracker snapshots are a raw API passthrough. Without a managed snapshot domain, there is no way to list available snapshots, track which VM a snapshot came from, or restore a snapshot into a new VM with proper network identity. The user must specify raw file paths for memory and state dumps with no directory management, no rootfs tracking, and no metadata persistence.

## Architecture

The snapshot domain has its own DB table (`snapshots`), cache directory (`~/.cache/mvmctl/snapshots/<id>/`), CLI
commands, and API layer. Snapshots are managed entities stored in a known location and restorable by name. Failed
creates remove the partial directory and do not retain a DB row; running-VM disk/memory crash consistency is still
pending as described below.

### Key characteristics

- **No snapshot Controller** — the public snapshot lifecycle has no mutable state machine. The repository handles DB
  CRUD, while the API layer coordinates filesystem and VM operations. The current restore path does temporarily mutate
  `.restore.lock` and `phantom-rootfs.img`; Task 7 removes that transitional mechanism.
- **Self-contained core domain** — `internal/core/snapshot/` owns repository and resolution behavior and does not import
  another core domain.
- **Orchestration in API layer** — the API layer coordinates VM pausing, rootfs copying, network allocation, Jailer
  launch, and snapshot API calls through the VM controller. This matches the rule that only `pkg/api/` orchestrates
  multiple core domains.

## Entry point

Snapshot operations are triggered from the CLI commands in `internal/cli/snapshot.go`:

- `mvm snapshot create <vm>` — calls `op.SnapshotCreate()` in `pkg/api/snapshot.go`
- `mvm snapshot restore <id> <name>` — calls `op.SnapshotRestore()`
- `mvm snapshot ls` — calls `op.SnapshotList()`
- `mvm snapshot inspect <id>` — calls `op.SnapshotInspect()`
- `mvm snapshot rm <id>` — calls `op.SnapshotRemove()`

The API layer orchestrates all cross-domain operations (VM pause/resume, rootfs copy, network allocation, and
Firecracker spawn). The snapshot repository in `internal/core/snapshot/` handles DB CRUD and resolution. There is no
snapshot service layer.

## Happy path: Snapshot create

### 1. Resolve and enrich

`op.SnapshotCreate()` resolves the VM by identifier and enriches it with all relations (kernel, image, binary, network) via the enricher.

### 2. Generate snapshot ID

A deterministic snapshot ID is generated via `crypto.SnapshotID(sourceVMID, timestamp)` — SHA of the source VM ID concatenated with the timestamp.

### 3. Create snapshot directory

The snapshot cache directory is created at `~/.cache/mvmctl/snapshots/<id>/` (or beneath `MVM_CACHE_DIR`).

### 4. Copy rootfs

The source VM's rootfs is copied to the fixed `snapDir/rootfs.img` leaf via `infra.CopyFile()`. The leaf name is
independent of the guest filesystem type. The current implementation performs this copy before pausing a running VM;
it therefore does not yet guarantee a crash-consistent disk/memory pair. Snapshot crash consistency remains a v0.3
release blocker.

### 5. Create phantom symlink

A transitional symlink `snapDir/phantom-rootfs.img` → `rootfs.img` is created so the captured vmstate can refer to a
snapshot-local backing path. ADR-0016 requires Task 7 to replace this persistent symlink with a private mount-namespace
overlay before v0.3 release signoff.

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

### 3. Derive managed artifacts

Before creating a VM, restore validates the stored snapshot ID as exactly 64 lowercase hexadecimal characters and
derives `snapshots/<id>/{rootfs.img,memory,vmstate}` from the configured cache root. Stored path columns are display
metadata and do not select restore or removal targets.

### 4. For each VM to restore

For each clone (controlled by `--count`, default 1):

1. Generate the new VM ID and copy `snapDir/rootfs.img` to `vms/<new-id>/rootfs.img`.
2. Allocate the network lease/TAP and persist the stopped VM record with its kernel, image, binary, and network identity.
3. Derive the VM's fixed Firecracker/vsock paths and spawn Firecracker in snapshot mode.
4. Acquire an exclusive flock on `snapDir/.restore.lock` (serializes concurrent restores from the same snapshot).
5. Replace `snapDir/phantom-rootfs.img` with a symlink to the restored VM's jailed `/rootfs` mount.
6. Call `firecracker.LoadSnapshot()` with the fixed memory/state paths, network overrides, and vsock override.
7. Release the flock on `.restore.lock`.

### Phantom symlink

The phantom symlink makes snapshots independent of the source VM:

- **During create**: PATCH the running VM's drive to point to `phantom-rootfs.img` before taking the snapshot. The
  vmstate captures that snapshot-local path.
- **During restore**: replace the symlink so the jailed load resolves the restored VM's `/rootfs` mount.

Firecracker's `PUT /snapshot/load` does not support a block-device-path override — the backing file path recorded in the vmstate is used directly. The phantom symlink works around this constraint.

### Concurrent restore safety

The `.restore.lock` file serializes concurrent `mvm snapshot restore` invocations from the same snapshot. The lock is acquired before the phantom symlink is updated and released after LoadSnapshot completes. `flock()` is used because it is automatically released on process exit.

### Vsock override

Firecracker's `PUT /snapshot/load` supports `vsock_override` to change the host UDS path at load time. The snapshot's
guest CID is preserved; the implementation does not issue a separate post-load `PUT /vsock`.

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
| `snapshot_dir` | TEXT | Derived display path to `cache/snapshots/<id>/` |
| `memory_file` | TEXT | Derived display path to the fixed `memory` leaf |
| `state_file` | TEXT | Derived display path to the fixed `vmstate` leaf |
| `rootfs_file` | TEXT | Derived display path to the fixed `rootfs.img` leaf |
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
| `pkg/api/inputs/snapshot.go` | Input structs: `SnapshotCreateInput`, `SnapshotRestoreInput`, `SnapshotInput` |
| `internal/lib/firecracker/client.go` | Firecracker HTTP client: `PauseVM()`, `CreateSnapshot()`, `ResumeVM()`, `LoadSnapshot()` |
| `internal/cli/snapshot.go` | Cobra commands: `mvm snapshot create\|ls\|inspect\|restore\|rm` |

## Design decisions

**No snapshot Controller.** Snapshot metadata has no public state machine. The API layer handles orchestration directly;
the transitional restore lock/symlink mutation is an implementation mechanism scheduled for removal by Task 7.

**Phantom symlink over source VM rootfs reference.** The phantom symlink makes the snapshot self-contained and restorable without the source VM. During restore, the symlink is updated to point to the new VM's rootfs copy. The `flock` serializes the update + LoadSnapshot window.

**DB over filesystem for config storage.** The Firecracker boot config is stored in the `extra_config` DB column rather than as a `config.json` file. The enricher enriches it at restore time. This avoids file management complexity.

**`infra.CopyFile()` for rootfs copy.** Uses the existing sendfile/userspace fallback and `fdatasync`; a new subprocess
call is unnecessary for this operation.

**Snapshots copy the rootfs.** A snapshot stores `image_id`, `kernel_id`, `network_id`, and `binary_id` for metadata and
enrichment, but restore reads the snapshot's fixed rootfs copy rather than the original image file.

**Vsock override preserves the CID.** Restore passes the fixed UDS path through `vsock_override` on `/snapshot/load` and
keeps the guest CID captured in vmstate; it does not issue a separate `PUT /vsock`.
