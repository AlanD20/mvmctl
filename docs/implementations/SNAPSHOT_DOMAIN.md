> **STATUS: Approved design (not yet implemented).** All architectural decisions
> resolved via grilling session 2026-06-18. See ADR-level decisions inline.
>
> **Last updated:** 2026-06-18

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
| **Orchestration in API layer** — `pkg/api/` calls VM controller + snapshot service | Matches existing VM creation pattern where API layer orchestrates network, cloud-init, etc. |
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
    ├── rootfs.ext4         # Copy of source VM's rootfs (taken at snapshot time)
    ├── memory              # Firecracker memory snapshot (from PUT /snapshot/create)
    └── vmstate             # Firecracker VM state snapshot (from PUT /snapshot/create)
```

No `config.json` — config is stored in the DB `extra_config` column and enriched
via the enricher at restore time.

### Why copy the rootfs?

The source VM continues running after snapshot. If we reference the original
rootfs path and the source VM's rootfs changes (it will — the guest writes to
it), the snapshot is inconsistent. Copying at snapshot time gives a
point-in-time consistent rootfs.

Reuses the existing `builder.cloneImage()` mechanism from the API layer's VM
create flow. Not a new `cp --reflink=auto` call.

## CLI Commands

### `mvm snapshot create <vm> [name]`

Snapshots a running VM.

```
Arguments:
  vm           VM identifier (name, ID, IP, MAC)
  name         Optional snapshot name (default: <vm>-<timestamp>)

Flags:
  --pause      Leave VM paused after snapshot (default: auto-resume)
```

**Flow (API layer orchestrates):**
1. Resolve VM by identifier
2. Enrich VM with all relations (kernel, image, binary, network) via enricher
3. Generate snapshot ID via `crypto.SnapshotID()`
4. Create `cache/snapshots/<id>/`
5. Copy rootfs via existing cloneImage mechanism (not a new subprocess call)
6. Use `internal/lib/firecracker` client directly to:
   a. `PauseVM()` if VM is running
   b. `CreateSnapshot(memPath, statePath)` — paths inside snapshot dir
   c. If not `--pause`: `ResumeVM()`
7. Insert DB record via snapshot service
8. Audit log

Any step fails → cleanup snapshot dir, no DB insert. Atomic.

### `mvm snapshot ls [id]`

Lists all snapshots or shows details for one.

```
Flags:
  --json       JSON output
```

### `mvm snapshot restore <id> <name> [--count N] [--network net] [--resume]`

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
4. For each count:
   a. Generate new VM ID
   b. Create VM record with: generated ID, given name (append `-N`), Stopped status
   c. Set RootfsPath to snapshot's rootfs copy
   d. Wire kernel, binary from enriched snapshot metadata
   e. If `--network` provided: resolve network, allocate fresh IP+MAC, set NetworkID
   f. If no `--network`: use snapshot's original network (same IP/MAC)
   g. Create VM directory and Firecracker config
   h. Spawn Firecracker in snapshot mode via `vmRespawnFirecracker()`
   i. Use `internal/lib/firecracker` client to call `LoadSnapshot(mem, state, resume)`
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
    SnapshotCreate(ctx, input inputs.SnapshotCreateInput,
        onProgress event.OnProgressCallback) (*model.SnapshotItem, error)
    SnapshotList(ctx) ([]*model.SnapshotItem, error)
    SnapshotGet(ctx, id string) (*model.SnapshotItem, error)
    SnapshotRestore(ctx, input inputs.SnapshotRestoreInput) ([]*model.VMItem, error)
    SnapshotRemove(ctx, input inputs.SnapshotInput) error
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
    SSHKeys      db.StringSlice `json:"ssh_keys"      db:"ssh_keys"`
    SSHUser      *string      `json:"ssh_user,omitempty"     db:"ssh_user"`
    ExtraConfig  *string      `json:"extra_config,omitempty" db:"extra_config"`
    CreatedAt    string       `json:"created_at"      db:"created_at"`
    UpdatedAt    string       `json:"updated_at"      db:"updated_at"`

    // Enriched relations (populated by enricher, not persisted)
    Kernel  *KernelItem  `json:"kernel,omitempty"  db:"-"`
    Network *NetworkItem `json:"network,omitempty" db:"-"`
    Binary  *BinaryItem  `json:"binary,omitempty"  db:"-"`
}
```

No `SizeBytes`. No `config.json` path — config stored in `ExtraConfig` DB column.

## Core Domain (`internal/core/snapshot/`)

```
internal/core/snapshot/
├── repository.go    # Repository interface (CRUD + lookup methods)
├── sqlite.go        # SQLite implementation
└── service.go       # Service — thin DB ops + filesystem ops
```

No Controller. Snapshots are immutable after creation — no state machine needed.

### Repository interface

```go
type Repository interface {
    // Basic CRUD
    Get(ctx, id string) (*model.SnapshotItem, error)
    FindByPrefix(ctx, prefix string) ([]*model.SnapshotItem, error)
    ListAll(ctx) ([]*model.SnapshotItem, error)
    Upsert(ctx, item *model.SnapshotItem) error
    Delete(ctx, id string) error

    // Reference counting for soft-delete protection
    CountByKernelID(ctx, kernelID string) (int, error)
    CountByNetworkID(ctx, networkID string) (int, error)
    CountByBinaryID(ctx, binaryID string) (int, error)

    // Reference queries
    FindByKernelID(ctx, kernelID string) ([]*model.SnapshotItem, error)
    FindByNetworkID(ctx, networkID string) ([]*model.SnapshotItem, error)
    FindByBinaryID(ctx, binaryID string) ([]*model.SnapshotItem, error)
}
```

### Service responsibilities

- `List()` / `Get()` — query DB
- `Store()` / `Delete()` — thin wrappers around DB operations
- `CreateDir()` / `RemoveDir()` — filesystem ops for snapshot directory
- `Remove()` — deletes filesystem dir + DB record (atomic: dir first, then DB)

The service does NOT:
- Resolve VMs or enrich relations (API layer does)
- Call Firecracker API (API layer does via `internal/lib/firecracker`)
- Copy rootfs (API layer does via cloneImage)
- Allocate networks (API layer does)

## Enricher

### `EnrichSnapshot()` in `internal/enricher/`

New enrichment method for snapshot kernel/network/binary relations:

```go
func (e *Enricher) EnrichSnapshot(ctx []*model.SnapshotItem, relations ...string) error
```

Enriches: kernel_id → KernelItem, network_id → NetworkItem, binary_id → BinaryItem.
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
5. **Service** — thin service in `internal/core/snapshot/service.go`
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

## Open Questions (Resolved)

| Question | Resolution |
|----------|------------|
| Rootfs copy mechanism | Reuse existing `cloneImage()` from API layer. Not a new `cp` call. |
| `--pause` flag implementation | Snapshot domain calls `firecracker.PauseVM()` → `firecracker.CreateSnapshot()` → conditionally `firecracker.ResumeVM()` directly. API layer orchestrates. |
| config.json in cache dir | Removed. Config stored in DB `extra_config` column, enriched at restore. |
| Snapshot from already-paused VM | No-op pause, proceed. Same as current `Controller.Snapshot()` path. |
| `--count` with same rootfs | All clones share the snapshot's rootfs copy. Correct — point-in-time consistent. |
| Compression | Not in v1. |
