# Planned Improvements — IMPROVEMENTS_003

> ## Status Overview
>
> | Feature | Status | Notes |
> |---------|--------|-------|
> | `--count` for `mvm vm create` | ✅ **IMPLEMENTED** | `--count N` and `--atomic` flags exist in `cli/vm.py`. `VMCreateInput.count`, `generate_batch_names()` in `utils/common.py`. |
> | Volume domain | ✅ **IMPLEMENTED** | Moved to IMPROVEMENTS_002 as completed item |
>
> **Last verified:** 2026-05-14

This document tracks planned features with architectural analysis and implementation strategy.
For active improvements, see [IMPROVEMENTS_002.md](IMPROVEMENTS_002.md).

---

## Feature 1: `--count` for `mvm vm create`

**Status:** ✅ **IMPLEMENTED** — See `cli/vm.py`, `VMCreateInput.count`, `generate_batch_names()` in `utils/common.py`
**Complexity:** Medium
**Files affected:** ~5 (mostly `vm_operations.py`, `_vm_create_input.py`, `cli/vm.py`)

### Goal

```bash
mvm vm create my-vm --image ubuntu-24.04 --count 5
```
Creates 5 VMs (`my-vm`, `my-vm-2`, `my-vm-3`, `my-vm-4`, `my-vm-5`) with the same image, kernel, network config.

### Validation rules

| Condition | Behavior |
|---|---|
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
|---|---|
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

## Feature 2: Volume domain ✅ IMPLEMENTED

**Original status:** Planned → **✅ COMPLETED**

Volume domain has been fully implemented. See `core/volume/`, `api/volume_operations.py`, `cli/volume.py`, `models/volume.py`.

Refer to the actual implementation in the codebase rather than this design document.

---

## Dependency between features

The two features are **independent** and can be implemented in parallel or sequentially. `--count` is purely additive to the VM create flow. Volumes are a new domain that only needs the `extra_drives` wiring in the VM creation pipeline, which doesn't conflict with `--count`.

**However**: if you want `mvm vm create --count 5 --volume my-data`, then both features need to be done. The volume integration simply adds extra drives to each per-VM `VMCreateContext`, which works naturally with batch creation. (Both `--count` and volume integration are now implemented ✅.)

## Future phases (separate docs)

| Phase | File | Description | Status |
|---|---|---|---|
| After volumes | [IMPROVEMENTS_004.md](IMPROVEMENTS_004.md) | `--from-volume` — full-disk boot from a volume | ❌ Pending |
| After `--count` stable | [IMPROVEMENTS_005.md](IMPROVEMENTS_005.md) | Resource grouping (batch ID) | ❌ Pending |
| Any time | [IMPROVEMENTS_006.md](IMPROVEMENTS_006.md) | JSON output mode | ⚠️ Partially Implemented |
| When needed | [IMPROVEMENTS_007.md](IMPROVEMENTS_007.md) | nftables for iptables at scale | ✅ Done |

---

## Notes

- Items marked with ~~strikethrough~~ are removed/deferred
- Items marked with [x] should be moved to archive when verified complete
