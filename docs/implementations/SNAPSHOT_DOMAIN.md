> **STATUS: Implemented.** Snapshot domain with its own DB table, CLI commands, API layer, and cache directory. Full create/list/inspect/restore/remove lifecycle.
>
> **Last updated:** 2026-06-28

# Snapshot Functionality

## Problem

Firecracker snapshots are a raw API passthrough. The current `mvm vm snapshot`
command takes three positional arguments (VM, mem_file, state_file) and passes
them directly to Firecracker's `PUT /snapshot/create` with no metadata, no
rootfs tracking, no listing, no management.

The user experience is:
```
mvm vm snapshot vmtest ./vmtest.mem ./vmtest.state   # files go somewhere
mvm vm load vmtest2 ./vmtest.mem ./vmtest.state      # "VM not found"
```

There's no way to answer "what snapshots do I have?", "what VM was this
snapshot from?", or "restore this snapshot into a new VM".

## Solution: First-Class Snapshot Domain

Introduce a new domain `snapshot` with its own DB table, cache directory, CLI
commands, and API layer. Snapshots become managed entities — captured atomic,
stored in a known location, restorable by name.

### Comparison

| Aspect | Current (raw passthrough) | New (managed domain) |
|--------|--------------------------|---------------------|
| Snapshot files location | User-specified paths | `cache/snapshots/<id>/` |
| Rootfs tracking | None | Copy in snapshot directory |
| Metadata stored | None | DB table with VM config snapshot |
| Listing | Impossible | `mvm snapshot ls` |
| Clone from snapshot | Manual + error-prone | `mvm snapshot restore <id> <name> --count N` |
| Network identity | Broken on clone | Fresh MAC/IP per clone via `--network` |

### Key Architectural Decisions (from grilling)

| Decision | Rationale |
|----------|-----------|
| **No Controller** — snapshot entity is immutable after creation | No state machine needed. Service handles DB ops + filesystem ops. Heavy orchestration is API layer. |
| **Service is thin** — DB CRUD + simple filesystem ops | Cross-domain orchestration (VM pause/snapshot/resume, network allocation, rootfs copy) lives in API layer per existing pattern. |
| **Snapshot domain is self-contained** — uses `internal/lib/firecracker` client directly | Extracted from `internal/core/vm/` to shared lib. No cross-core imports. |
| **Orchestration in API layer** — `pkg/api/` calls VM controller + snapshot repository | Matches existing VM creation pattern where API layer orchestrates network, cloud-init, etc. |
| **Atomic create** — any failure cleans up everything | Snapshot dir removed, no DB insert on any partial failure. |
| **Legacy commands removed** — `mvm vm snapshot`/`mvm vm load` deleted | Pre-prod, breaking change is acceptable. Replaced by `mvm snapshot create`/`restore`. |
| **Migration: modify `001_initial_schema.sql`** in-place | Pre-prod — no new migration number. |
| **`crypto.SnapshotID(sourceVMID, timestamp)`** — SHA of source VM ID + timestamp | Deterministic, reproducible. Matches existing `crypto.VMID` pattern. |
| **Prefix resolution same as VM domain** — error on ambiguity | "Multiple snapshots found matching prefix". |
| **`snapshot rm` does NOT touch VMs** — snapshots and VMs are independent | No FK from VM → snapshot. No VMs killed during rm. |

## DB Schema

### `snapshots` table (added to `001_initial_schema.sql`)

| Column | Type | Description |
|--------|------|-------------|
| `id` | TEXT PRIMARY KEY | `crypto.SnapshotID(source_vm_id, timestamp)` |
| `name` | TEXT | User-provided name (optional, defaults to `<source-vm>-<timestamp>`) |
| `source_vm_id` | TEXT | Source VM ID (the VM that was snapshotted) |
| `source_vm_name` | TEXT | Source VM name (denormalized for display) |
| `snapshot_dir` | TEXT | Absolute path to `cache/snapshots/<id>/` |
| `memory_file` | TEXT | Path to memory dump file within snapshot dir |
| `state_file` | TEXT | Path to vmstate file within snapshot dir |
| `rootfs_file` | TEXT | Path to rootfs copy within snapshot dir |
| `kernel_id` | TEXT | Kernel ID used by source VM at snapshot time |
| `network_id` | TEXT | Network ID used by source VM at snapshot time |
| `binary_id` | TEXT | Firecracker binary ID used at snapshot time |
| `vcpu_count` | INTEGER | vCPU count from source VM |
| `mem_size_mib` | INTEGER | Memory size from source VM |
| `disk_size_mib` | INTEGER | Rootfs size from source VM |
| `ssh_keys` | TEXT | SSH key names (JSON array) |
| `ssh_user` | TEXT | SSH user from source VM (nullable) |
| `extra_config` | TEXT | Full Firecracker boot config (JSON blob — boot args, LSM flags, PCI, console settings, etc.). Used by enricher instead of on-disk `config.json`. |
| `created_at` | TEXT | ISO 8601 timestamp |
| `updated_at` | TEXT | ISO 8601 timestamp (consistency with other tables) |

No `size_bytes` — removed as unnecessary for v1.

## Cache Directory Layout

```
~/.cache/mvm/snapshots/
└── <snapshot-id>/
    ├── rootfs.ext4            # Copy of source VM's rootfs (IMMORTAL — never modified)
    ├── phantom-rootfs.ext4    # SYMLINK — redirects to the current restore's rootfs copy
    ├── .restore.lock          # flock mutex for concurrent restore safety
    ├── memory                 # Firecracker memory snapshot (from PUT /snapshot/create)
    └── vmstate                # Firecracker VM state snapshot (from PUT /snapshot/create)
```

No `config.json` — config is stored in the DB `extra_config` column and enriched
via the enricher at restore time.

### Why copy the rootfs?

The source VM continues running after snapshot. If we reference the original
rootfs path and the source VM's rootfs changes (it will — the guest writes to
it), the snapshot is inconsistent. Copying at snapshot time gives a
point-in-time consistent rootfs.

Uses `infra.CopyFile()` (file-level copy with sparse support). Not a new
subprocess call.

### Phantom Symlink: Rootfs Independence from Source VM

Firecracker's `PUT /snapshot/load` opens the block device backing file at the
path recorded in the vmstate file during `PUT /snapshot/create`. There is no
block-device-path override parameter on the Load API — the path is **hardcoded**
in the vmstate binary.

To make the snapshot completely independent of the source VM's rootfs path, we:

1. **During snapshot create** — PATCH the running VM's drive to point to a
   symlink in the snapshot directory *before* taking the snapshot. The vmstate
   captures this snapshot-local path, not the source VM's path.

2. **During snapshot restore** — Replace that symlink to point to the new VM's
   rootfs copy. LoadSnapshot follows the symlink, finds the correct backing file.

The symlink is named `phantom-rootfs.ext4` — it holds no real data, it's just a
redirect that gets updated on each restore. The actual frozen rootfs at
`snapDir/rootfs.ext4` is never touched after creation.

```
Snapshot directory layout:

  snapshots/<snap-id>/
  ├── rootfs.ext4               ← IMMORTAL: frozen rootfs, never modified
  ├── phantom-rootfs.ext4       ← SYMLINK: pointer updated on each restore
  ├── memory                    ← Firecracker memory dump
  ├── vmstate                   ← Firecracker VM state (captures phantom path)
  └── .restore.lock             ← flock mutex for concurrent restore safety

Snapshot create flow:

  Source VM paused
       │
       ├── Copy rootfs → snapshots/<id>/rootfs.ext4
       ├── Symlink phantom-rootfs.ext4 → rootfs.ext4
       │
       ├── PATCH /drives/rootfs → path_on_host = phantom-rootfs.ext4
       │   (changes the running VM's drive path WHILE PAUSED)
       │
       ├── PUT /snapshot/create → vmstate captures "phantom-rootfs.ext4" ✓
       │
       ├── PATCH /drives/rootfs → path_on_host = <original-path>
       │   (restores original path before resume)
       │
       └── Source VM resumed

Snapshot restore flow (single VM):

  ┌─ Lock .restore.lock (flock LOCK_EX)
  │
  ├── Copy snapshots/<id>/rootfs.ext4 → vms/<new>/rootfs.ext4
  ├── Replace phantom-rootfs.ext4 → symlink → vms/<new>/rootfs.ext4
  │
  ├── PUT /snapshot/load
  │   ├── snapshot_path: <vmstate>
  │   ├── mem_file_path: <memory>
  │   ├── network_overrides: eth0 → <new-tap>
  │   ├── vsock_override: {uds_path: <new-uds-path>}
  │   └── Opens phantom-rootfs.ext4 → follows symlink → vms/<new>/rootfs.ext4 ✓
  │
  └─ Unlock .restore.lock (flock LOCK_UN)

Multiple restores from the same snapshot (concurrent):

  Thread A                     Thread B
  ──────────                   ──────────
  LOCK .restore.lock           (waiting...)
    phantom → vms/A/rootfs
    LoadSnapshot OPENS
      → FD to vms/A ✓
    LoadSnapshot done
  UNLOCK                       LOCK .restore.lock
                                 phantom → vms/B/rootfs
                                 LoadSnapshot OPENS
                                   → FD to vms/B ✓
                                 LoadSnapshot done
                               UNLOCK

  Running VMs keep their file descriptors (FDs) to their own rootfs copies.
  Replacing the symlink does NOT affect already-running VMs.
```

### What happens to the source VM during snapshot create?

When we `PATCH /drives/rootfs` on the paused source VM, Firecracker changes the
backing file path. The source VM is paused — no I/O in flight. After
`CreateSnapshot` captures the phantom path, we `PATCH /drives/rootfs` back to
the original path before resuming. The source VM never sees the phantom path
while running — it's only active during the paused snapshot window.

### Why not just symlink the source VM's path?

Two reasons:

1. **Self-contained snapshot** — The snapshot should be restorable without the
   source VM. If the vmstate points to `vms/<source-id>/rootfs.ext4`, you need
   that source VM directory to exist when you restore. The phantom approach
   points to `snapshots/<snap-id>/phantom-rootfs.ext4` — the snapshot is the
   only dependency.

2. **Concurrent restore safety** — Running VMs keep FDs to their rootfs copies.
   The phantom symlink is a shared pointer that gets updated on each restore.
   The flock serializes the update + open window. The old approach (symlink at
   source VM path) had no lock and depended on the source VM directory staying
   intact.

### Concurrent Restore Safety

The `.restore.lock` file in the snapshot directory serializes concurrent
`mvm snapshot restore` invocations from the same snapshot. The lock is acquired
before the phantom symlink is updated and released after LoadSnapshot
completes.

**Lock usage:**
- **Scope:** Per `snapshot restore` CLI invocation (wraps phantom update +
  LoadSnapshot)
- **Mechanism:** `flock()` on a regular file — automatically released when the
  process exits (no orphaned locks)
- **Location:** `snapshots/<snap-id>/.restore.lock`
- **Convention:** Exclusive lock (`LOCK_EX`). All restores from the same
  snapshot contend for the same lock file.
- **Multiple VMs from one invocation:** The lock is acquired once per iteration
  of the for loop (count > 1 is supported but each iteration acquires and
  releases the lock independently). Note: this means concurrent processes are
  serialized, but within a single process with count > 1, later iterations
  could race with concurrent processes on the phantom update.

### Vsock Override

Firecracker's `PUT /snapshot/load` supports a `vsock_override` parameter that
changes the vsock UDS path at load time. Without it, the vmstate's recorded UDS
path is used — which would collide with the source VM's vsock socket if the
source VM is still running.

The vsock UDS path is passed through `SnapshotRestoreConfig.VsockUDSPath` and
sent as `vsock_override.uds_path` in the LoadSnapshot request body. The
`PutVsock` call after LoadSnapshot is retained as a belt-and-suspenders
measure (it also sets the new guest CID, which `vsock_override` does not).

### Config Structs

The controller accepts explicit config structs for snapshot create and restore,
defined in `internal/lib/model/snapshot.go`:

```go
type SnapshotCreateConfig struct {
    MemFile           string   // path for memory dump
    StateFile         string   // path for vmstate file
    PauseOnly         bool     // leave VM paused after snapshot
    PhantomRootfsPath string   // symlink path for vmstate (empty = skip PATCH)
    RootfsPath        string   // original path to restore after snapshot
}

type SnapshotRestoreConfig struct {
    MemFile          string            // memory dump path
    StateFile        string            // vmstate file path
    Resume           bool              // auto-resume after load
    NetworkOverrides map[string]string // iface_id → host_dev_name
    VsockUDSPath     string            // vsock_override UDS path
    RootfsPath       string            // rootfs path for post-Load PATCH
}
```

These are cross-package data structures passed from the API layer
(`pkg/api/snapshot.go`) to the controller (`internal/core/vm/controller.go`).
They live in the model package so both sides can import them without circular
dependencies.

## CLI Commands

### `mvm snapshot create <vm>`

Snapshots a running VM.

```
Arguments:
  vm           VM identifier (name, ID, IP, MAC)

Flags:
  --name       Optional snapshot name (default: <vm>-<timestamp>)
  --pause      Leave VM paused after snapshot (default: auto-resume)
```

**Flow (API layer orchestrates):**
1. Resolve VM by identifier
2. Enrich VM with all relations (kernel, image, binary, network) via enricher
3. Generate snapshot ID via `crypto.SnapshotID()`
4. Create `cache/snapshots/<id>/`
5. Copy rootfs via `infra.CopyFile()` (source VM's rootfs → snapshot dir)
6. Create `snapDir/phantom-rootfs.ext4` → symlink → `snapDir/rootfs.ext4`
7. Use controller to:
   a. `PauseVM()` if VM is running
   b. `PATCH /drives/rootfs` with `path_on_host = snapDir/phantom-rootfs.ext4`
   c. `CreateSnapshot(memPath, statePath)` — vmstate captures phantom path
   d. `PATCH /drives/rootfs` with original path (restore before resume)
   e. If not `--pause`: `ResumeVM()`
8. Insert DB record via snapshot service
9. Audit log

Any step fails → cleanup snapshot dir, no DB insert. Atomic.

### `mvm snapshot ls`

Lists all snapshots.

```
Flags:
  --json       JSON output
```

### `mvm snapshot inspect <identifier>`

Shows detailed information for a single snapshot.

```
Arguments:
  identifier   Snapshot ID (or prefix -- errors on ambiguity)

Flags:
  --json       JSON output
```

### `mvm snapshot restore <id> <name> [--network net] [--resume]`

Restores one or more VMs from a snapshot.

```
Arguments:
  id           Snapshot ID (or prefix — errors on ambiguity)
  name         Name for the new VM(s)

Flags:
  --count N    Number of VMs to create (default: 1)
  --network    Network to attach (default: snapshot's original network)
  --resume     Start VM immediately after load (default: leave paused)
```

**Network identity for clones:**

Each clone (including the single `--count 1` case) gets a **fresh MAC and IP**
allocated from the target network. The snapshot captures guest memory state,
which includes the original IP and MAC. On first boot after restore:

- For `--count 1` and same network: the guest will ARP for its old IP. If no
  other VM uses it, it works. If the IP is taken, the guest has a network
  conflict.
- For `--count > 1`: every clone boots with the same MAC/IP in memory →
  immediate conflict.

**Strategy for v1:** Accept `--network` flag. On restore, allocate fresh MAC/IP
from the specified network. The guest's in-memory network config will be stale
(it remembers the old IP), but DHCP/cloud-init guest-side scripts can re-apply.
Document this limitation.

**Flow (API layer orchestrates):**
1. Resolve snapshot by ID (prefix — error on ambiguity)
2. Load snapshot metadata from DB
3. Enrich snapshot relations (kernel, network, binary) via snapshot enricher
4. For each VM to restore:
   a. Generate new VM ID
   b. Copy `snapDir/rootfs.ext4` → `vms/<new-id>/rootfs.ext4`
   c. Acquire `flock(LOCK_EX)` on `snapDir/.restore.lock`
   d. Replace `snapDir/phantom-rootfs.ext4` → symlink → new VM's rootfs
   e. Create VM record with Stopped status, wired kernel/binary/network
   f. Create VM directory and Firecracker config
   g. Spawn Firecracker in snapshot mode via `vmRespawnFirecracker()`
   h. Load snapshot via controller:
      - `LoadSnapshot(mem, state, resume, network_overrides, vsock_override)`
      - The phantom symlink resolves the rootfs at load time
   i. Release `flock(LOCK_UN)` on `.restore.lock`
5. Print created VM names

### `mvm snapshot rm <id>`

Removes a snapshot.

```
Arguments:
  id           Snapshot ID (or prefix — errors on ambiguity)

Flags:
  --force      Skip confirmation
```

**Flow:**
1. Resolve snapshot by ID
2. Confirm (unless `--force`)
3. Remove `cache/snapshots/<id>/` recursively
4. Delete DB record

Does NOT touch any VM. Snapshots and VMs are independent entities.

## API Layer

### SnapshotAPI interface (`pkg/api/`)

Added to the composite `API` interface in `pkg/api/interfaces.go`.

```go
type SnapshotAPI interface {
    SnapshotCreate(ctx context.Context,
        input inputs.SnapshotCreateInput,
        onProgress event.OnProgressCallback) (*model.SnapshotItem, error)
    SnapshotList(ctx context.Context) []*model.SnapshotItem
    SnapshotInspect(ctx context.Context, input inputs.SnapshotInput) (*results.SnapshotInspect, error)
    SnapshotRestore(ctx context.Context, input inputs.SnapshotRestoreInput) ([]*model.VMItem, error)
    SnapshotRemove(ctx context.Context, input inputs.SnapshotInput) *errs.BatchResult
}
```

Heavy orchestration (VM pause/snapshot/resume, rootfs copy, network allocation,
Firecracker spawn) lives in the API layer's `Operation` methods — NOT in the
snapshot service. Matches the existing pattern where `vmBuilderExecute()` in the
API layer orchestrates multiple domains.

### Input structs (`pkg/api/inputs/`)

```go
type SnapshotCreateInput struct {
    Identifier string  // VM identifier (name, ID, IP, MAC)
    Name       *string // Optional snapshot name
    Pause      bool    // Leave VM paused after snapshot
}

type SnapshotRestoreInput struct {
    SnapshotID string
    Name       string
    Count      int
    Network    *string // Optional network override
    Resume     bool
}

type SnapshotInput struct {
    Identifiers []string
    Force       bool
}
```

### Model (`internal/lib/model/`)

```go
type SnapshotItem struct {
    ID           string       `json:"id"              db:"id"`
    Name         string       `json:"name"            db:"name"`
    SourceVMID   string       `json:"source_vm_id"    db:"source_vm_id"`
    SourceVMName string       `json:"source_vm_name"  db:"source_vm_name"`
    SnapshotDir  string       `json:"snapshot_dir"    db:"snapshot_dir"`
    MemoryFile   string       `json:"memory_file"     db:"memory_file"`
    StateFile    string       `json:"state_file"      db:"state_file"`
    RootfsFile   string       `json:"rootfs_file"     db:"rootfs_file"`
    KernelID     string       `json:"kernel_id"       db:"kernel_id"`
    NetworkID    string       `json:"network_id"      db:"network_id"`
    BinaryID     string       `json:"binary_id"       db:"binary_id"`
    VCPUCount    int          `json:"vcpu_count"      db:"vcpu_count"`
    MemSizeMiB   int          `json:"mem_size_mib"    db:"mem_size_mib"`
    DiskSizeMiB  int          `json:"disk_size_mib"   db:"disk_size_mib"`
    ImageID      string               `json:"image_id"               db:"image_id"`
    SSHKeys      db.StringSlice       `json:"ssh_keys"               db:"ssh_keys"`
    SSHUser      *string              `json:"ssh_user,omitempty"     db:"ssh_user"`
    ExtraConfig  *SnapshotExtraConfig `json:"extra_config,omitempty" db:"extra_config"`
    CreatedAt    string               `json:"created_at"             db:"created_at"`
    UpdatedAt    string               `json:"updated_at"             db:"updated_at"`

    // Enriched relations (populated by enricher, not persisted)
    Image   *ImageItem   `json:"image,omitempty"`
    Kernel  *KernelItem  `json:"kernel,omitempty"`
    Network *NetworkItem `json:"network,omitempty"`
    Binary  *BinaryItem  `json:"binary,omitempty"`
}
```

Snapshot config structs (cross-package — used by API layer and controller):

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

No `SizeBytes`. No `config.json` path — config stored in `ExtraConfig` DB column.

## Core Domain (`internal/core/snapshot/`)

```
internal/core/snapshot/
├── repository.go    # Repository interface (CRUD + lookup methods)
├── sqlite.go        # SQLite implementation
└── resolver.go      # Entity resolution by identifier
```

No Controller. No Service file. Snapshots are immutable after creation — no state machine needed. Cross-domain orchestration lives entirely in the API layer.

### Repository interface

```go
type Repository interface {
    // Basic CRUD
    Get(ctx context.Context, id string) (*model.SnapshotItem, error)
    GetByName(ctx context.Context, name string) (*model.SnapshotItem, error)
    FindByPrefix(ctx context.Context, prefix string) ([]*model.SnapshotItem, error)
    ListAll(ctx context.Context) ([]*model.SnapshotItem, error)

    // Mutations
    Upsert(ctx context.Context, item *model.SnapshotItem) error
    Delete(ctx context.Context, id string) error

    // Reference counting for delete protection
    CountByKernelID(ctx context.Context, kernelID string) (int, error)
    CountByNetworkID(ctx context.Context, networkID string) (int, error)
    CountByBinaryID(ctx context.Context, binaryID string) (int, error)

    // Reference queries (for enricher reverse-relation)
    FindByKernelID(ctx context.Context, kernelID string) ([]*model.SnapshotItem, error)
    FindByKernelIDs(ctx context.Context, kernelIDs []string) ([]*model.SnapshotItem, error)
    FindByNetworkID(ctx context.Context, networkID string) ([]*model.SnapshotItem, error)
    FindByNetworkIDs(ctx context.Context, networkIDs []string) ([]*model.SnapshotItem, error)
    FindByBinaryID(ctx context.Context, binaryID string) ([]*model.SnapshotItem, error)
    FindByBinaryIDs(ctx context.Context, binaryIDs []string) ([]*model.SnapshotItem, error)
}
```

There is no `service.go` in the snapshot domain — all orchestration is handled by the API layer. The repository provides DB CRUD, and the API layer's `Operation` methods handle filesystem ops (snapshot directory creation/removal, rootfs copy, etc.).

The snapshot domain does NOT:
- Resolve VMs or enrich relations (API layer does)
- Call Firecracker API (API layer does via `internal/lib/firecracker`)
- Copy rootfs (API layer does via `infra.CopyFile()`)
- Allocate networks (API layer does)

## Enricher

### `EnrichSnapshot()` in `internal/enricher/`

New enrichment method for snapshot kernel/network/binary relations:

```go
func (e *Enricher) EnrichSnapshot(ctx context.Context, snapshots []*model.SnapshotItem, include ...string) error
```

Enriches: kernel_id → KernelItem, image_id → ImageItem, network_id → NetworkItem, binary_id → BinaryItem.
Same switch/case dispatch pattern as `EnrichVM`.

### Cross-cutting impact: soft-delete protection

Because snapshots store `kernel_id`, `network_id`, `binary_id`, the delete paths
for **4 existing domains** must check for snapshot references before hard-deleting:

| Domain | Current protection | New check needed |
|--------|-------------------|------------------|
| `kernel rm` | Checks `KernelItem.VMs` | Also check `snapshot_repo.CountByKernelID()` |
| `network rm` | Checks `NetworkItem.VMs` | Also check `snapshot_repo.CountByNetworkID()` |
| `binary rm` | Checks `BinaryItem.VMs` | Also check `snapshot_repo.CountByBinaryID()` |
| `image rm` | Checks `ImageItem.VMs` | No change — snapshots reference rootfs copy, not image |

The pattern: API layer enriches the entity with both VM and snapshot reference
counts before calling the service's `Remove()`. If snapshots reference the
entity, route to `SoftDelete()` instead of `Delete()`.

Since "Core domains NEVER import other core/* packages", the snapshot repo
cannot be imported by kernel/network/binary services. The API layer owns this
check and makes the final decision.

## Firecracker Client Relocation

The `FirecrackerClient` (HTTP client for Firecracker API over Unix socket) moves
from `internal/core/vm/firecracker_client.go` to
`internal/lib/firecracker/client.go`. This makes it a shared leaf utility
accessible by all core domains without cross-package imports.

- Rename: `vm.NewFirecrackerClient` → `firecracker.NewClient`
- Package: `package firecracker` in `internal/lib/firecracker/`
- Updated in: `internal/core/vm/controller.go` (8 call sites)
- Test file moves too: `firecracker_client_test.go` → `internal/lib/firecracker/`

Then the snapshot domain can import `internal/lib/firecracker` directly to call
`PauseVM()`, `CreateSnapshot()`, `ResumeVM()`, `LoadSnapshot()` — no VM
controller involvement.

## Migration

Add `snapshots` table to `001_initial_schema.sql` (modify existing migration —
pre-prod, no new file number).

## Implementation Order

1. **Firecracker client relocation** — move to `internal/lib/firecracker/`
2. **Model type** — add `SnapshotItem` to `internal/lib/model/`
3. **DB migration** — add `snapshots` table to `001_initial_schema.sql`
4. **Repository** — interface + SQLite in `internal/core/snapshot/`
5. **Service responsibilities** — handled directly by the API layer (no service.go). DB CRUD via repository, filesystem ops via the API layer's `Operation` methods. This matches the pattern where snapshots are immutable after creation — no state machine needed.
6. **Enricher** — `EnrichSnapshot()` in `internal/enricher/`
7. **API layer** — `SnapshotAPI` implementation + input structs
8. **Cross-domain delete protection** — update kernel/network/binary delete paths
9. **CLI commands** — `mvm snapshot create|ls|restore|rm`
10. **Remove legacy** — delete `mvm vm snapshot` and `mvm vm load`
11. **Unit tests** — per `docs/development/HOW_AGENTS_WRITE_UNIT_TESTS.md`

## Firecracker Constraints on Snapshot Restore

### Resources are Immutable on Restore

**vCPU and memory cannot be changed when restoring a snapshot.** This is a hard
Firecracker constraint, not a design choice.

When loading a snapshot, the machine configuration (`vcpu_count`, `mem_size_mib`)
must be set via `/machine-config` **before** `/snapshot/load`. Firecracker then
validates that the config matches what was captured in the snapshot state file.
If they differ, Firecracker rejects the load.

The reason: the snapshot captures the complete KVM vCPU register state (for
exactly N vCPUs), the APIC/IOAPIC state, and the guest memory layout. Loading
this into a Firecracker configured for M vCPUs would be an incompatible state
restore — KVM cannot remap vCPUs at load time.

**Disk** can technically be resized on restore (the rootfs is just a file copy),
but this is not implemented in v1. The snapshot stores the source VM's resource
metadata purely for display purposes (`mvm snapshot ls` shows what was captured).
All restored VMs inherit the exact same resources as the source snapshot.

### Block Device Path is Hardcoded in vmstate

Firecracker's `PUT /snapshot/load` does **not** support a block-device-path
override (unlike `network_overrides` and `vsock_override`). The backing file
path recorded in the vmstate file is the path used during load. The file must
exist at that path when LoadSnapshot is called — Firecracker opens it during
load, not at resume time.

The phantom symlink works around this constraint by ensuring the vmstate path
always points to a symlink (`phantom-rootfs.ext4`) in the snapshot directory.
This symlink is updated to point to the correct rootfs copy before each restore.

### Vsock Override is Supported

Firecracker DOES support `vsock_override` on `PUT /snapshot/load` (added in
Firecracker v1.x). This changes the vsock UDS path at load time, avoiding
collisions with the source VM's vsock socket. The UDS path is set via
`SnapshotRestoreConfig.VsockUDSPath` and sent as
`vsock_override: {uds_path: "<new-path>"}` in the LoadSnapshot request body.

The vsock guest CID is NOT overridable via `vsock_override` — only the UDS path
changes. The CID is updated via a separate `PUT /vsock` call after LoadSnapshot.

## Open Questions (Resolved)

| Question | Resolution |
|----------|------------|
| Rootfs copy mechanism | Use `infra.CopyFile()` (file-level copy with sparse support). Not a new subprocess call. |
| `--pause` flag implementation | Snapshot domain calls `firecracker.PauseVM()` → `firecracker.CreateSnapshot()` → conditionally `firecracker.ResumeVM()` directly. API layer orchestrates. |
| config.json in cache dir | Removed. Config stored in DB `extra_config` column, enriched at restore. |
| Snapshot from already-paused VM | No-op pause, proceed. Same as current `Controller.Snapshot()` path. |
| `--count` with same rootfs | All clones share the snapshot's rootfs copy. Correct — point-in-time consistent. |
| Compression | Not in v1. |
