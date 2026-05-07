# Planned Improvements — IMPROVEMENTS_003

This document tracks planned features with architectural analysis and implementation strategy.
For active improvements, see [IMPROVEMENTS_002.md](IMPROVEMENTS_002.md).

---

## Feature 1: `--count` for `mvm vm create`

**Status:** Planned
**Complexity:** Medium
**Files affected:** ~5 (mostly `vm_operations.py`, `_vm_create_input.py`, `cli/vm.py`)

### Goal

```bash
mvm vm create my-vm --image ubuntu-24.04 --count 5
```
Creates 5 VMs (`my-vm`, `my-vm-2`, `my-vm-3`, `my-vm-4`, `my-vm-5`) with the same image, kernel, network config.

### Validation rules

| Condition | Behavior |
|-----------|----------|
| `--count 1` (default) | No change — existing behavior |
| `--count N` + `--ip` | ❌ Conflict error |
| `--count N` + `--mac` | ❌ Conflict error |
| `--count N` + no `--name` | Auto-names: `<image>-1`, `<image>-2`... or uses `os_slug` |
| `--count N` + `--name my-vm` | First VM = `my-vm`, rest = `my-vm-2`, `my-vm-3`... |
| `--count N` + resource exhaustion | Check subnet capacity, max VMs before starting — fail fast |
| `--count N` + `--skip-cleanup` | Applies to each VM independently |

### Shared vs per-VM work

```
SHARED ONCE:
  ┌─ Image: resolve ImageItem → done
  ├─ Kernel: resolve KernelItem → done
  ├─ Binary: resolve BinaryItem → done
  ├─ Network: resolve NetworkItem, ensure bridge + NAT → done
  └─ Guestfs: build appliance once, reuse for all rootfs ops

PER-VM (parallelizable):
  ├─ Name: generate (base or base-N)
  ├─ Rootfs: reflink-copy from warm pool → fast
  ├─ IP: allocate from subnet lease pool
  ├─ TAP: create + attach to bridge
  ├─ Cloud-init: generate per-VM seed (unique hostname, IP, SSH keys)
  ├─ Provision: resize, inject SSH, hostname, fstab (via guestfs shared handle)
  ├─ Firecracker config: per-VM JSON
  ├─ Spawn: Firecracker process per VM
  ├─ Console relay: per-VM relay process
  └─ Nocloud server: per-VM nocloud-net server (if NET mode)
```

### Key design decision: Unify single + batch

**No separate code paths.** `count=1` and `count=N` use the same `_create()` method — always a list, always the same pipeline. The overhead of a list of 1 is negligible (microseconds) compared to VM creation time (seconds).

### Architecture

**Input layer** (`api/inputs/_vm_create_input.py`):
```python
@dataclass
class VMCreateInput:
    name: str
    count: int = 1           # NEW
    ...
    requested_guest_ip: str | None = None  # conflict if count > 1
    requested_guest_mac: str | None = None # conflict if count > 1
```

**Validation resolves ONCE for all VMs** (`VMCreateRequest.resolve()`):
- Resolves image, kernel, binary, network items — **once**, shared for all VMs in the batch
- `_validate_count()`:
  - `count < 1` → error
  - `count > 1` + `requested_guest_ip` → conflict error
  - `count > 1` + `requested_guest_mac` → conflict error
  - `count > subnet_available_ips` → fail fast
  - `count > global_max_vms` → fail fast
- Name generation is literal string manipulation — no re-resolution needed

**Orchestration** (`api/vm_operations.py`):
```python
class VMOperation:
    @staticmethod
    def create(inputs: VMCreateInput) -> OperationResult[list[VMInstanceItem]]:
        # 1. Validate + resolve shared state ONCE
        request = VMCreateRequest(inputs, db)
        resolved = request.resolve()  # shared for all VMs

        # 2. Generate names for the batch
        names = _generate_batch_names(resolved.name, inputs.count)

        # 3. Run per-VM work (sequential first, parallel later)
        vms: list[VMInstanceItem] = []
        for idx, name in enumerate(names):
            context = VMCreateContext.for_create(
                resolved=resolved,
                name=name,
                index=idx,  # unique IP, TAP, mac allocation
                ...
            )
            result = context.execute()
            vms.append(result)

        return OperationResult(status="success", item=vms)
```

**Output** (`cli/vm.py`):
```python
# Always the same format, even for count=1
print_success(f"Created {len(vms)} VM(s): {', '.join(vm.name for vm in vms)}")
# Examples:
#   "Created 1 VM(s): my-vm"
#   "Created 5 VM(s): my-vm, my-vm-2, my-vm-3, my-vm-4, my-vm-5"
```

### Failure handling

| Scenario | Behavior |
|----------|----------|
| VM 3 of 10 fails during rootfs copy | Log error, continue with remaining. Report summary at end |
| All succeed | Return list of created VM names |
| All fail | Return aggregate error |
| Network setup fails midway | No VMs created — safe rollback |

**`--atomic` flag** (default `count > 1` without `--atomic` = best-effort):

When `--atomic` is set, if ANY VM in the batch fails, ALL already-created VMs are torn down (removed, cleanup Firecracker/TAP/leases). The batch is all-or-nothing.

Implementation: catch per-VM errors, then in the `finally` or error handler, iterate the successful VMs and call `VMOperation.remove()` on each (with `--force` since they're freshly created and nothing should block removal).

CLI:
```bash
mvm vm create my-vm --image ubuntu-24.04 --count 5 --atomic   # All or nothing
mvm vm create my-vm --image ubuntu-24.04 --count 5             # Best-effort (default)
```

### Additional improvements (inline)

**Pre-allocate all resources before any VM creation:**
- Reserve ALL IPs from the subnet in a single transaction
- Check ALL generated names against existing `vm_instances` — fail fast on collision
- Check disk space for N rootfs copies (e.g., 10 × 1GB = need 10GB free)
- Only then start the creation loop. Eliminates "VM 7 of 10 fails because subnet ran out."

**Progress reporting during batch:**
- The `on_progress` callback pattern already exists in `VMCreateContext`
- For batch, emit events like `[3/10] ✅ my-vm-3 created (2.3s)`

**Staggered spawning (rate limiting):**
- Don't spawn all Firecracker processes at once
- Use a semaphore (e.g., 4 concurrent max) or small delay between spawns
- Prevents I/O storm and CPU spike from N simultaneous kernel boots

### Implementation order

1. Add `count` field to `VMCreateInput` + validation in `VMCreateRequest` (validate once)
2. Add `--count` CLI option to `cli/vm.py` in the `create` command
3. Unify `VMOperation.create()` — always batch path, always return `list[VMInstanceItem]`
4. Add `_generate_batch_names()` helper
5. Ensure `VMCreateContext` can accept an index parameter for unique IP/TAP/mac allocation
6. Parallelize per-VM loop with `ThreadPoolExecutor` (optional perf improvement)
7. Update CLI output format to consistent `"Created N VM(s): ..."`
8. Pre-allocation of IPs/names/disk before loop
9. Progress reporting via `on_progress`
10. Staggered spawning (semaphore)
11. Update tests

---



## Feature 2: Volume domain

**Status:** Planned
**Complexity:** Medium-High
**Files affected:** ~15 (new domain + integration across existing stack)

### What is a volume?

A persistent data disk, independent of any VM. Created once, attachable to VMs as an additional block device. Survives VM deletion. Think `docker volume`.

### CLI surface

```bash
# Volume CRUD
mvm volume create my-data --size 10G                    # Create a 10GB raw disk
mvm volume create my-data --size 10G --format qcow2     # Create qcow2
mvm volume rm my-data                                   # Remove a volume
mvm volume ls                                           # List volumes
mvm volume inspect my-data                              # Show volume details

# Attachment during VM creation
mvm vm create my-vm --image ubuntu-24.04 --volume my-data  # Attach at create time
mvm vm create my-vm --image ubuntu-24.04 --volume vol-a --volume vol-b  # Multiple volumes

# Attachment to running VMs (hot-plug)
mvm vm attach-volume my-vm my-data                      # Attach to running VM
mvm vm detach-volume my-vm my-data                      # Detach from running VM
```

### Architecture — standard domain pattern

```
models/volume.py — VolumeItem
core/volume/
  _controller.py    — VolumeController (single volume lifecycle)
  _service.py       — VolumeService (create disk image, resize, format)
  _repository.py    — VolumeRepository (DB CRUD)
  _resolver.py      — VolumeResolver (resolve by name/prefix)
api/inputs/
  _volume_input.py          — VolumeInput → VolumeRequest → ResolvedVolumeInput
  _volume_create_input.py   — VolumeCreateInput → ... → ResolvedVolumeCreateInput
api/volume_operations.py    — VolumeOperation (create, rm, ls, get, inspect)
cli/volume.py               — Typer commands
```

### VolumeItem model

```python
@dataclass
class VolumeItem:
    id: str                # SHA256 hash
    name: str              # User-friendly name (UNIQUE)
    size_bytes: int        # Size in bytes
    format: str            # 'raw' | 'qcow2'
    path: str              # Absolute path to disk image
    status: str            # 'available' | 'attached'
    vm_id: str | None      # Which VM it's attached to (None if available)
    created_at: str
    updated_at: str
```

### DB schema — modify existing, NOT a new migration

**Pre-production rule: no backward compatibility, no legacy code.** Edit `001_initial_schema.sql` directly. Add `volumes` table + `volume_ids` column to `vm_instances` inline. Never leave dead migration files behind.

Add a prominent banner at the top of `001_initial_schema.sql`:

```sql
-- ⚠️ PRE-PRODUCTION: This schema is mutated directly.
-- No backward compatibility migrations.
-- Old databases should be deleted and recreated.
```

Then add to the existing schema:

```sql
-- VOLUMES: Persistent data disks attachable to VMs
CREATE TABLE volumes (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    size_bytes INTEGER NOT NULL,
    format TEXT NOT NULL DEFAULT 'raw',
    path TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'available',
    vm_id TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL
);
CREATE INDEX idx_volumes_vm ON volumes(vm_id);
CREATE INDEX idx_volumes_name ON volumes(name);
```

And add to `vm_instances`: `volume_ids TEXT` (JSON array, nullable) — persisted attached volume IDs so they survive VM restart.

### Core layer

**VolumeService** — stateless disk operations:
```python
class VolumeService:
    @staticmethod
    def create_disk(path: Path, size_bytes: int, format: str = "raw") -> None:
        """Create a disk image. Uses qemu-img for qcow2, fallocate for raw."""

    @staticmethod
    def remove_disk(path: Path) -> None:
        """Remove disk image from disk."""

    @staticmethod
    def get_disk_info(path: Path) -> dict:
        """Get disk size, format, actual usage via qemu-img info."""
```

**VolumeController** — stateful entity operations:
```python
class VolumeController:
    def __init__(self, entity: str | VolumeItem, repo: VolumeRepository) -> None:
        ...

    def get(self) -> VolumeItem: ...
    def attach(self, vm_id: str) -> None: ...    # Update DB status
    def detach(self) -> None: ...                 # Update DB status
```

### Integration points — the pipeline

The pipeline from `--volume` CLI flag to Firecracker drive is **half-wired already**. This completes it:

```
cli/vm.py: --volume flag
    │
    ▼
VMCreateInput.volumes: list[str] | None   ← NEW field
    │
    ▼
VMCreateRequest.resolve()
    │  └─ resolves volume names → VolumeItem → DriveConfig objects
    │  └─ populates ResolvedVMCreateInput.extra_drives ✅ (field exists!)
    ▼
VMOperation._build_firecracker_config()
    │  └─ reads extra_drives → passes to FirecrackerConfig  ← NEW
    ▼
FirecrackerConfig.extra_drives: list[DriveConfig]   ← NEW field
    │
    ▼
FirecrackerSpawner._build_drives_config()
    │  └─ iterates extra_drives, appends to drives list ✅ (TODO exists!)
    ▼
Firecracker boots with /dev/vdb, /dev/vdc, etc.
```

### Hot-plug for running VMs

Firecracker API supports `PUT /drives` to attach/detach drives on a running VM:

```python
# In core/vm/_firecracker.py (FirecrackerClient)
def put_drive(self, drive_config: DriveConfig) -> None:
    """Attach or update a drive on a running VM via PUT /drives."""
    ...

def patch_drive(self, drive_id: str) -> None:
    """Remove a drive from a running VM via PATCH /drives."""
    ...
```

CLI chain:
```
mvm vm attach-volume my-vm my-data
→ VMOperation.attach_volume(inputs)
  → Resolves VM, resolves Volume
  → VolumeController.attach(vm_id) — updates DB
  → FirecrackerClient.put_drive(drive_config) — hot-plug
```

### Additional improvements (inline)

**Volume resize (grow only):**
```bash
mvm volume resize my-data 20G
```
Uses `qemu-img resize` for qcow2, `fallocate` for raw.

**Drive limit per VM:**
- Firecracker max drives = 8 (including rootfs + cloud-init)
- So max volumes per VM = 6 (ISO mode) or 7 (NET/INJECT mode)
- Validate at attach time, fail with clear message

### Implementation order

1. **DB changes** — Add `volumes` table + `volume_ids` column to `vm_instances` in existing schema
2. **Models** — `VolumeItem` dataclass
3. **Core domain** — Repository → Service → Resolver → Controller (in that order)
4. **Input layer** — `VolumeInput`, `VolumeRequest`, `ResolvedVolumeInput`
5. **API layer** — `VolumeOperation` (create, rm, ls, get, inspect)
6. **CLI layer** — `cli/volume.py` commands
7. **VM integration** — wire `extra_drives` through `VMCreateInput` → `VMCreateRequest` → `FirecrackerConfig` → `FirecrackerSpawner`
8. **Hot-plug** — `FirecrackerClient.put_drive()` + attach/detach operations
9. **Respawn** — ensure attached volumes are re-attached on VM restart (`from_vm()`)
10. Volume resize command
11. Drive limit validation at attach time

## Dependency between features

The two features are **independent** and can be implemented in parallel or sequentially. `--count` is purely additive to the VM create flow. Volumes are a new domain that only needs the `extra_drives` wiring in the VM creation pipeline, which doesn't conflict with `--count`.

**However**: if you want `mvm vm create --count 5 --volume my-data`, then both features need to be done. The volume integration simply adds extra drives to each per-VM `VMCreateContext`, which works naturally with batch creation.

## Future phases (separate docs)

| Phase | File | Description |
|-------|------|-------------|
| After volumes | [IMPROVEMENTS_004.md](IMPROVEMENTS_004.md) | `--from-volume` — full-disk boot from a volume |
| After `--count` stable | [IMPROVEMENTS_005.md](IMPROVEMENTS_005.md) | Resource grouping (batch ID) |
| Any time | [IMPROVEMENTS_006.md](IMPROVEMENTS_006.md) | JSON output mode |
| When needed | [IMPROVEMENTS_007.md](IMPROVEMENTS_007.md) | nftables for iptables at scale |

---

## Notes

- Items marked with ~~strikethrough~~ are removed/deferred
- Items marked with [x] should be moved to archive when verified complete
