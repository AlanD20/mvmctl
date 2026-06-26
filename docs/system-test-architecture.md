# System Test Architecture — Three-Tier, Domain-Isolated VM Execution

**Status:** Active — primary reference for system test patterns  
**Last updated:** 2026-06-25  
**Supersedes:** The snapshot-based approach (abandoned due to TAP name conflicts without per-VM network namespaces).  
**See also:** [ADR-0012: Unified Test Architecture](adr/0012-unified-test-architecture.md) (L0/L1/L2 language-boundary decision), [HOW_AGENTS_WRITE_SYSTEM_TESTS.md](development/HOW_AGENTS_WRITE_SYSTEM_TESTS.md) (how-to writing guide), [COVERAGE_MATRIX.md](../tests/system/COVERAGE_MATRIX.md) (coverage tracking).

---

## Table of Contents

1. [Architecture Overview](#1-architecture-overview)
2. [Custom Base Image](#2-custom-base-image)
3. [Three Tiers](#3-three-tiers)
4. [The Shared Read-Only Asset Volume](#4-the-shared-read-only-asset-volume)
5. [Tier 1: Host-Level CLI Operations](#5-tier-1-host-level-cli-operations)
6. [Tier 2: VM Creation/Interaction](#6-tier-2-vm-creationinteraction)
7. [Tier 3: Host-Level Domains (No Runner VM)](#7-tier-3-host-level-domains-no-runner-vm)
8. [How Tests Call `mvm`](#8-how-tests-call-mvm)
9. [Python Orchestrator (`scripts/run-system-tests.py`)](#9-python-orchestrator)
10. [File Layout](#10-file-layout)
11. [Per-Domain Tier Classification](#11-per-domain-tier-classification)
12. [Fixture Scoping Strategy](#12-fixture-scoping-strategy)
13. [Known-Limitation Patterns (xfail)](#13-known-limitation-patterns-xfail)
14. [Guest-Side Test Patterns](#14-guest-side-test-patterns)
15. [Per-File Compliance Checklist](#15-per-file-compliance-checklist)
16. [Parallelism Model](#16-parallelism-model)
17. [Migration Phases](#17-migration-phases)

---

## 1. Architecture Overview

```
Host (your machine)
│
├── scripts/run-system-tests.py   ← orchestrator (creates VMs, runs tests, destroys)
│
├── TIER 1 — One VM per domain from custom base image + shared volume
│   ├── mvm vm create --image mvm-test-runner:<version> --volume asset-mirror
│   ├── mount /dev/vdb /mnt && MVM_ASSET_MIRROR=/mnt mvm init --non-interactive
│   ├── pytest tests/system/<domain>/        ← runs INSIDE the VM
│   └── mvm vm rm --force
│
├── TIER 2 — Same as T1 + nested-virt + kernel for Firecracker-in-VM tests
│   ├── mvm vm create --image mvm-test-runner:<version> --nested-virt --volume asset-mirror
│   ├── mount /dev/vdb /mnt && MVM_ASSET_MIRROR=/mnt mvm init --non-interactive
│   ├── MVM_ASSET_MIRROR=/mnt mvm kernel pull / image pull / bin pull (cache hits)
│   ├── pytest tests/system/<domain>/        ← runs INSIDE the VM
│   └── mvm vm rm --force
│
└── TIER 3 — Tests run directly on host (no runner VM)
    └── pytest tests/system/<domain>/
```

### Key Decisions

| Decision | Rationale |
|----------|-----------|
| **Custom base image** | A single `mvm-test-runner:<mvm-version>` image is built during `--prepare`. Contains mvm binary, system tests, and all OS deps (`python3-pytest`, `qemu-utils`). Built once, cached by version. |
| **Tests run inside the VM** | The orchestrator runs `mvm vm exec -- python3 -m pytest ...` inside the test VM. Tests call `mvm` directly via `subprocess.run` — no vsock proxy. |
| **`conftest.py` is simple** | `_run_mvm` is a thin wrapper around `subprocess.run(["mvm", *args])`. The `runner_vm` fixture gets the VM name from `MVM_TEST_VM` env var (set by the orchestrator). |
| **Shared RO volume on all tiers** | The `asset-mirror` volume is attached to every VM (T1 and T2). Mounting is cheap — `--shareable --read-only` means no per-VM state. |
| **`--push` flag** | Overrides baked-in tests by copying `tests/system/` fresh into the VM. Useful during test development without rebuilding the base image. |
| **`--rebuild` flag** | Forces rebuild of both the shared volume and the custom base image. Removes volume and image first, then recreates from scratch. |

---

## 2. Custom Base Image

### Building (`--prepare`)

The custom base image `mvm-test-runner:<mvm-version>` is built once and cached:

```
1. Create builder VM from ubuntu-minimal:noble
2. mvm cp mvm-binary → /usr/local/bin/mvm
3. mvm cp tests/system/ → /tests/
4. apt-get install python3-pytest qemu-utils
5. apt-get clean
6. mvm vm stop (graceful stop)
7. mvm image import mvm-test-runner:<version> <builder-vm> --default
8. Destroy builder VM
```

### Contents

| Item | Location in image | Purpose |
|------|-------------------|---------|
| mvm binary | `/usr/local/bin/mvm` | The exact version being tested |
| System tests | `/tests/system/<domain>/` | All `tests/system/` files |
| python3-pytest | System package | Test runner |
| qemu-utils | System package | Volume/disk operations in T2 tests |

### Caching

- Rebuilt only when the mvm version changes (version string from `mvm --version`).
- `--rebuild` forces rebuild (removes image first, then recreates).
- Image is versioned as `mvm-test-runner:<mvm-version>` — multiple versions can coexist.

---

## 3. Three Tiers

All T1/T2 runner VMs are Firecracker microVMs created with **Firecracker v1.16** and **kernel 7.0.11** (the `official:7.0.11` kernel built by mvmctl). The kernel has `nftables`, `tuntap`, and `kvm` compiled in — these are required for the runner VM to set up networking and (for T2) expose nested KVM.

| Tier | Runs inside VM | Shared volume | Nested KVM | Runner VM kernel | What's tested |
|------|---------------|---------------|------------|-----------------|---------------|
| **1** | Yes — `mvm vm exec -- pytest ...` | Yes (mounted at `/mnt`) | Not needed | `official:7.0.11` (nftables + tuntap + kvm) | Host-level CLI: help, config, init, cache, keys, invariants, bin, images, kernel, network, host, run |
| **2** | Yes — `mvm vm exec -- pytest ...` | Yes (mounted at `/mnt`) | Yes | `official:7.0.11` (nftables + tuntap + kvm) | VM lifecycle: volume, vm_lifecycle, ssh, cp, console, logs, full_journeys |
| **3** | No — runs directly on host | Host mirror | Yes (host KVM) | N/A (host-direct) | vm_fresh_env, vm_nested_isolated, vm_snapshot_load, volume_hotplug, kernel_build, env |

---

## 4. The Shared Read-Only Asset Volume

### Creating the Volume (`--prepare` or `ensure_shared_volume`)

The host asset mirror (`~/.cache/mvm-asset-mirror/`) is the source of truth. A shareable read-only volume is created once and reused across all test runs.

```bash
mvm volume create asset-mirror 6G --shareable --read-only --format raw
# Populated via sudo loop mount:
sudo mkfs.ext4 <volume_path>
sudo mount -o loop <volume_path> /mnt/.mvm-asset-populate
sudo cp -r ~/.cache/mvm-asset-mirror/* /mnt/.mvm-asset-populate/
sudo umount
```

### Volume Semantics

Because `asset-mirror` has `IsShareable=true` and `IsReadOnly=true`:
- **Attach**: mvmctl leaves `Status = VolumeStatusAvailable` (no `VMID` set).
- **Detach**: no-op at the DB level.
- **Firecracker**: receives the drive config with `is_read_only: true`.
- **Multiple VMs**: each VM opens the same file with `O_RDONLY` — Linux allows this, Firecracker has no locking.
- **Removal**: only with `--force` (no attachment tracking).

### Contents

| Asset | Size | Purpose |
|-------|------|---------|
| `vmlinux-*` (firecracker kernels) | ~30 MB | Default kernel for test VMs |
| `alpine:3.23` image | ~8 MB | Fast VM creation |
| `ubuntu:noble` image | ~600 MB | Heavier tests (SSH, nested virt) |
| `firecracker-v1.16.0` tarball | ~20 MB | Firecracker binary |

---

## 5. Tier 1: Host-Level CLI Operations

### Domains
- `cli/test_cli.py`
- `config/test_config.py`
- `init/test_init.py`
- `cache/test_cache.py`
- `keys/test_keys.py`
- `invariants/test_invariants.py`
- `bin/test_bin.py`
- `images/test_images.py`
- `kernel/test_kernel.py`, `kernel/test_kernel_import.py`
- `network/test_network.py`, `network/test_nftables.py`
- `host/test_host.py`
- `run/test_run.py`

### Runner VM Spec

All T1 runner VMs use the same Firecracker/kernel as T2 (for consistency — they are built from the same base image and created by the same orchestrator):

- **Hypervisor:** Firecracker v1.16
- **Kernel:** `official:7.0.11` (features: `nftables`, `tuntap`, `kvm`)
- **Resources:** 2 vCPU, 1024 MB RAM, 7 GB disk
- **Network:** `sys-test-net` (per-VM TAP isolation)
- **Volume:** `asset-mirror` (shared RO, mounted at `/dev/vdb`)

The `kvm` feature is unused for T1 tests (they never create nested VMs), but it has no negative effect. Using a single kernel for both tiers avoids an extra build.

### Provisioning Flow

```bash
mvm vm create <vm-name> --image mvm-test-runner:<version> \
  --kernel official:7.0.11 \
  --user runner --vcpu 2 --mem 1024 --disk-size 7G \
  --network sys-test-net --volume asset-mirror

mvm vm exec <vm-name> --user runner --timeout 60 -- \
  "sudo mkdir -p /mnt && sudo mount /dev/vdb /mnt && \
   MVM_ASSET_MIRROR=/mnt mvm init --non-interactive"
```

### Test Execution

```bash
mvm vm exec <vm-name> --user runner --timeout 600 -- \
  "cd / && MVM_ASSET_MIRROR=/mnt python3 -m pytest \
   /tests/system/cli/test_cli.py --tb=short -q"
```

---

## 6. Tier 2: VM Creation/Interaction

### Domains
- `volume/test_volume.py`
- `vm/test_vm_lifecycle.py`
- `ssh/test_ssh.py`
- `console/test_console.py`
- `logs/test_logs.py`
- `full_journeys/test_full_journeys.py` (Tier 3 — see note below)

### Runner VM Spec

Same as T1, but with nested virt enabled (requires `kvm` in the kernel — the `official:7.0.11` kernel has it):

- **Hypervisor:** Firecracker v1.16
- **Kernel:** `official:7.0.11` (features: `nftables`, `tuntap`, `kvm`)
- **Resources:** 4 vCPU, 4096 MB RAM, 7 GB disk (more resources for running nested Firecracker VMs).
- **Network:** `sys-test-net` (per-VM TAP isolation).
- **Nested virt:** Enabled (`--nested-virt`).
- **Volume:** `asset-mirror` (shared RO, mounted at `/dev/vdb`).

### Provisioning Flow

```bash
mvm vm create <vm-name> --image mvm-test-runner:<version> \
  --kernel official:7.0.11 \
  --user runner --vcpus 4 --mem 4096 --disk-size 7G \
  --network sys-test-net --nested-virt --volume asset-mirror
```

### Asset Registration (Cache Hits)

After init, asset pulls are cache hits against the shared volume:

```bash
mvm vm exec <vm-name> --user runner --timeout 120 -- \
  "MVM_ASSET_MIRROR=/mnt mvm kernel pull --type firecracker --version v1.15 --default"
mvm vm exec <vm-name> --user runner --timeout 120 -- \
  "MVM_ASSET_MIRROR=/mnt mvm image pull alpine:3.23"
mvm vm exec <vm-name> --user runner --timeout 120 -- \
  "MVM_ASSET_MIRROR=/mnt mvm bin pull firecracker --version 1.16.0 --default --force"
```

---

## 7. Tier 3: Host-Level Domains (No Runner VM)

### Domains
- `vm/test_vm_fresh_env.py` — full fresh-environment pipeline + nested virt negative case (consolidated from former `test_vm_nested_virt.py`).
- `vm/test_vm_nested_isolated.py` — triple-nested VM tests.
- `vm/test_vm_snapshot_load.py` — snapshot lifecycle.
- `volume/test_volume_hotplug.py` — PCI hotplug (requires Firecracker dev-preview, does not work reliably nested).
- `kernel/test_kernel.py` — kernel build tests (need full KVM host access).
- `cp/test_cp.py` — vsock agent file copy (some paths reject nested virt).
- `env/test_env.py` — env workflow apply/destroy/diff (creates resources via spec; running inside a runner VM would add unnecessary nesting for orchestration tests).

> **Note:** `test_vm_nested_virt.py` was merged into `test_vm_fresh_env.py` in June 2026. The two files had ~80% overlap (both tested nested virt verification). The unique negative-case test (VM without `--nested-virt` flag) was kept; the rest was redundant.

### Why These Run on the Host

These tests **create VMs as part of their own logic**. Running them inside a runner VM adds:
- Unnecessary nesting (VM → VM → VM for nested virt).
- Extra resource overhead.
- No benefit — these tests manage their own environments. Some (like hotplug and kernel build) need direct KVM/device access that nested virt cannot provide.

### Execution

```bash
# Run directly on the host
cd tests/system
MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror \
  python3 -m pytest volume/test_volume_hotplug.py --tb=short -q
```

---

## 8. How Tests Call `mvm`

Since tests run INSIDE the VM (Tier 1/2), they call `mvm` directly.

### Inside the test VM

```python
import subprocess

def test_help():
    result = subprocess.run(["mvm", "help"], capture_output=True, text=True)
    assert result.returncode == 0
    assert "Usage:" in result.stdout
```

### conftest.py helpers

The conftest provides `_run_mvm` as a convenience wrapper:

```python
def _run_mvm(vm_name: str, *args: str, ...) -> subprocess.CompletedProcess:
    """Run mvm command inside the test VM."""
    return subprocess.run(
        ["mvm", *args],
        capture_output=True, text=True,
        env={**os.environ, "NO_COLOR": "1"},
        ...
    )
```

The `vm_name` parameter is accepted for compatibility but NOT used — since we're already inside the target VM, all commands run locally. The orchestrator sets `MVM_TEST_VM` to the VM name for test introspection if needed.

---

## 9. Python Orchestrator (`scripts/run-system-tests.py`)

### Usage

```bash
# Build base image + smoke test
MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror \
  python3 scripts/run-system-tests.py --prepare

# Run all domains (T1 + T2 + T3)
MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror \
  python3 scripts/run-system-tests.py

# Run specific domains
python3 scripts/run-system-tests.py cli network vm_nested_virt

# Push fresh test files (no rebuild needed)
python3 scripts/run-system-tests.py cli --push

# Run only specific tiers
python3 scripts/run-system-tests.py --tier1-only
python3 scripts/run-system-tests.py --tier2-only
python3 scripts/run-system-tests.py --tier3-only
```

### Script Flow

```
1. Parse args
2. --prepare mode:
     a. Get mvm version
     b. Ensure shared volume + network
     c. Build custom base image mvm-test-runner:<version> (or skip if cached)
     d. Create T1 smoke VM → mount volume → mvm init → destroy
     e. Create T2 smoke VM → mount volume → mvm init → pull cache hit → destroy
3. Normal run mode:
     a. Detect mvm version
     b. Ensure shared volume + network
     c. Select domains based on args/tier flags
     d. T1 + T2 in parallel (ThreadPoolExecutor):
          Create VM → mount volume → mvm init →
          [--push: mvm cp tests/system] →
          mvm vm exec -- python3 -m pytest ... →
          destroy VM
     e. T3 sequentially on host:
          pytest tests/system/<domain>/
4. Print summary: X passed, Y failed, Z total
```

---

## 10. File Layout

```
tests/system/
├── conftest.py              ← _run_mvm helper (direct subprocess, no vsock proxy)
├── pytest.ini               ← Markers
├── __init__.py
│
├── cli/test_cli.py          ← TIER 1
├── config/test_config.py    ← TIER 1
├── init/test_init.py        ← TIER 1
├── cache/test_cache.py      ← TIER 1
├── keys/test_keys.py        ← TIER 1
├── invariants/test_invariants.py  ← TIER 1
│
├── network/test_network.py  ← TIER 1 (no nested virt needed)
├── network/test_nftables.py ← TIER 1
├── bin/test_bin.py          ← TIER 1
├── images/test_images.py    ← TIER 1
├── kernel/test_kernel.py    ← TIER 1
├── kernel/test_kernel_import.py   ← TIER 1
├── host/test_host.py        ← TIER 1
├── run/test_run.py          ← TIER 1
│
├── volume/test_volume.py    ← TIER 2 (needs nested virt)
├── volume/test_volume_hotplug.py  ← TIER 3 (FC PCI hotplug dev-preview)
├── vm/test_vm_lifecycle.py  ← TIER 2
├── ssh/test_ssh.py          ← TIER 2
├── cp/test_cp.py            ← TIER 3 (vsock agent path rejection nested)
├── console/test_console.py  ← TIER 2
├── logs/test_logs.py        ← TIER 2
├── full_journeys/test_full_journeys.py  ← TIER 2
├── env/test_env.py          ← TIER 2
│
├── vm/test_vm_nested_isolated.py  ← TIER 3
├── vm/test_vm_fresh_env.py        ← TIER 3
├── vm/test_vm_snapshot_load.py    ← TIER 3
│
├── COVERAGE_MATRIX.md       ← Coverage tracking
└── PENDING_FAILURES.md      ← Known issues
```

---

## 11. Per-Domain Tier Classification

| Domain | Tier | Test Files | Notes |
|--------|------|-----------|-------|
| cli | 1 | `test_cli.py` | Host-level CLI tests |
| config | 1 | `test_config.py` | Config file I/O |
| init | 1 | `test_init.py` | Init lifecycle |
| cache | 1 | `test_cache.py` | Cache operations |
| keys | 1 | `test_keys.py` | SSH key management |
| invariants | 1 | `test_invariants.py` | State invariants |
| bin | 1 | `test_bin.py` | Binary pull/management |
| images | 1 | `test_images.py` | Image pull/import |
| kernel | 1 | `test_kernel.py`, `test_kernel_import.py` | Kernel management |
| network | 1 | `test_network.py`, `test_nftables.py` | Network creation/rules |
| host | 1 | `test_host.py` | Host info/status |
| run | 1 | `test_run.py` | Service subprocesses |
| volume | 2 | `test_volume.py` | Volume lifecycle |
| vm_lifecycle | 2 | `test_vm_lifecycle.py` | VM create/start/stop/pause |
| ssh | 2 | `test_ssh.py` | SSH into VMs |
| console | 2 | `test_console.py` | Console output |
| logs | 2 | `test_logs.py` | Log retrieval |
| full_journeys | 2 | `test_full_journeys.py` | Multi-step scenarios |
| env | 3 | `test_env.py` | Environment workflow (apply/destroy/diff) — runs host-direct because creating VMs via env spec adds unnecessary nesting |
| volume_hotplug | 3 | `test_volume_hotplug.py` | PCI hotplug (needs host KVM — FC dev-preview, broken nested) |
| cp | 3 | `test_cp.py` | File copy to/from VMs (vsock agent path rejection nested) |
| vm_nested_isolated | 3 | `test_vm_nested_isolated.py` | Host-only, triple-nested |
| vm_fresh_env | 3 | `test_vm_fresh_env.py` | Host-only. Includes negative-case test from former `test_vm_nested_virt.py`. |
| vm_snapshot_load | 3 | `test_vm_snapshot_load.py` | Host-only |
| kernel_build | 3 | `test_kernel.py` | Kernel build tests (need full KVM host access) |

---

## 12. Fixture Scoping Strategy

Fixture scoping determines trade-offs between test speed and isolation. The following rules govern fixture scope decisions:

| Scope | Use Case | Examples |
|-------|----------|----------|
| **module** | Read-only state, no side effects across tests | Cache, keys, config tests |
| **function** | Tests that mutate shared state (VM state, PCI state, volumes) | Volume hotplug, snapshot, destructive tests |

### Rule: Mutating tests MUST use function-scoped fixtures

Any test that modifies Firecracker or kernel state (hotplug, snapshot, nested virt, PCI operations) **must** use a function-scoped fixture. Module-scoped VMs leak state across tests because:

- Firecracker is Developer Preview — hotplug/unplug does not fully reset PCI BARs.
- Nested VMs leave orphaned bridges, TAPs, or iptables rules.
- Volume attachment state persists in Firecracker even after CLI-level detach.

**Exception:** If a domain's tests are read-only or create-zero-state resources (keys, config, images, kernels), module-scoped is acceptable to save ~60-120s per test instance.

---

## 13. Known-Limitation Patterns (xfail)

Firecracker itself has limitations (especially in Developer Preview features). When a test exercises a code path that Firecracker does not support, **do not silently skip**. Use `pytest.mark.xfail` with an explicit reason:

```python
@pytest.mark.xfail(
    reason="Firecracker v1.16 dev-preview hot-unplug does not fully reset PCI BARs; "
           "re-attaching the same volume to the same running VM fails virtio-blk probe."
)
def test_hotplug_re_attach_after_hot_unplug(self, ...):
    ...
```

### xfail rules

1. **Only xfail when the limitation is in a downstream dependency** (Firecracker, kernel, guest OS). Never xfail for bugs in mvmctl's own code.
2. **Always document the exact failure reason** so the marker can be removed when the dependency is fixed.
3. **An `xfail` is NOT a skip** — the test still runs and collects result data. If the test unexpectedly passes, `XPASS` alerts the team to remove the marker.
4. **No `pytest.skip()`** — conditional skipping is banned because it hides regressions. Use `xfail` to document known failures or restructure the test to expect the known error.

---

## 14. Guest-Side Test Patterns

When testing features that require interaction with the guest OS (PCI hotplug, block device discovery, virtio drivers), follow these patterns:

### Device discovery — use sysfs, not lspci

Minimal guest images (Alpine) may not have `lspci`. Use kernel-provided sysfs paths instead:

```python
# Instead of:
result = _run_mvm(..., "lspci -D | grep 'Virtio.*block'")

# Use:
result = _run_mvm(..., "readlink /sys/block/vdb/device/driver")
```

### Block device counting — use /sys/block

```python
def _count_virtio_block_devices(runner_vm, vm_name):
    result = _run_mvm(..., "ls /sys/block | grep '^vd[b-z]' | wc -l")
    ...
```

### Device-polling helpers

When waiting for a block device to appear, poll `/proc/partitions` (always available, no extra tools needed):

```python
def _wait_for_vdb(runner_vm, vm_name, timeout=10.0):
    """Poll guest /proc/partitions via vsock exec until vdb appears."""
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        result = _run_mvm(runner_vm, "vm", "exec", vm_name, ...,
                          "cat /proc/partitions", check=False, timeout=15)
        if result.returncode == 0 and "vdb" in result.stdout:
            return True
        time.sleep(0.5)
    return False
```

### Hardened device-name matching

Block device names (`vdb`, `vdc`) are not guaranteed across PCI remove/re-add cycles. Tests that must locate the hotplugged device should iterate `/sys/block/vd*` and identify the device by matching properties (size, serial, etc.) rather than hardcoding the name.

### Manual investigation pattern

When a hotplug or PCI device operation fails, the recommended investigation flow is:

1. **Reproduce manually** — create the VM, attach/detach, collect `/proc/partitions`, guest `dmesg`, and Firecracker logs.
2. **Isolate the layer** — does the failure happen at the Firecracker API level (HTTP 400/500) or the guest kernel level (driver probe fails)?
3. **Document the finding** — update PENDING_FAILURES.md or add an xfail marker with the precise error signature.
4. **Fix or work around** — either fix the product code, or document the limitation and xfail the test.

---

## 15. Per-File Compliance Checklist

Every test file in `tests/system/` MUST:

1. **Tests call `mvm` directly** — Use `subprocess.run(["mvm", ...])` or `_run_mvm(...)`. No vsock proxy, no `mvm vm exec` nesting.

2. **Tier 3: Direct host calls allowed** — Tests that create VMs or check host state run directly on the host.

3. **No `os.path.exists` on VM filesystem paths** — Those paths are inside the VM (Tier 1/2). Tier 3 tests may check host paths.

4. **No direct SQLite connections** — Use `mvm` commands to query state.

5. **No `zzz_destructive/`** — Destructive tests go in their domain file with `@pytest.mark.destructive`.

6. **Clean up in `finally`** — Every resource created (VM, network, volume, key) must have a `try/finally` cleanup.

7. **Unique names** — Use `uuid` for resource names to avoid collisions across parallel domains.

8. **`pytestmark`** — Each file must have `pytestmark = [pytest.mark.system, pytest.mark.tier<N>, pytest.mark.domain_xxx]`.

9. **No `pytest.skip()`** — Conditional skipping is banned. Use `xfail` for known downstream limitations.

10. **Device discovery via sysfs, not lspci** — Guest-side device paths use `/sys/block`, `/proc/partitions`, or `readlink` on device symlinks. Avoid tools that may not be present in minimal guest images.

11. **Function-scoped fixture for state-mutating tests** — Tests that modify Firecracker/guest PCI state must use `@pytest.fixture` (function scope). Module-scoped is only acceptable for read-only or zero-state domains.

---

## 16. Parallelism Model

| Dimension | How it works |
|-----------|-------------|
| **T1 domains** | Parallel via `ThreadPoolExecutor`. Each domain gets its own VM. No shared state beyond the RO volume. |
| **T2 domains** | Parallel via `ThreadPoolExecutor`. Each domain gets its own VM with nested virt. Shared RO volume. |
| **T3 domains** | Sequential on host. Destructive tests need ordering. |
| **Within a domain** | Sequential within each VM. Test ordering matters (create → read → update → delete). |
| **Limits** | `--workers N` flag controls max parallel VMs. Default: 4. |

---

## 17. Migration Phases

### Phase A — Done ✅
- `--shareable` volume flag implemented in mvmctl.
- Custom base image system (`--prepare` → `mvm image import`).
- `scripts/run-system-tests.py` orchestrator with `--push`, `--rebuild`.
- Tier 1/2/3 classification with shared volume on all tiers.
- vsock agent `fsync` fix (prevents file corruption on VM stop).

### Phase B — Done ✅
- Fix `test_cli.py` to call `mvm` directly instead of using proxy.
- Fix all Tier 1/2 tests to work inside the VM (direct `subprocess.run`).
- Remove `_guest_run` vsock proxy from conftest (simplify to direct execution).
- ✅ Consolidate `test_vm_nested_virt.py` into `test_vm_fresh_env.py` (80% overlap, kept unique negative-case test).
- ✅ Update all T3 tests to use kernel `official:7.0.11` (was `6.19.9` in some files).
- ✅ Add kernel cleanup in `test_vm_nested_isolated.py` fixture to avoid leaking kernel records on host.
- ✅ Volume hotplug: PCI rescan on attach, sysfs-based PCI removal on detach, post-detach rescan, function-scoped fixture, xfail for Firecracker re-attach limitation.
- ✅ `--tier2-only` bug fix (was also executing Tier 3).
- ✅ Verify all domains pass.

### Phase C — Cleanup (Complete ✅)
- ✅ `tests/e2e/` deleted entirely.
- ✅ `requires_firecracker_116` pytest marker registered in `pytest.ini`.
- ✅ Stale PENDING_FAILURES entries removed (resolved or xfail).
- ✅ This document promoted from DRAFT to active reference, cross-references updated in ADR 0012, AGENTS.md, and qa-engineer.md.
