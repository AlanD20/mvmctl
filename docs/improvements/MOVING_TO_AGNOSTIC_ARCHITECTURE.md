# Moving to a VMM-Agnostic Architecture

> ## ⚠️ NOT IMPLEMENTED — Design Proposal Only
>
> **Status:** This is a forward-looking design document. **None of the proposed changes have been implemented.**
> The codebase remains fully coupled to Firecracker as the sole VMM.
>
> **Date of last verification:** 2026-05-13

## Implementation Status Summary

| Phase / Component | Status | Current Reality |
|---|---|---|
| **IVMMDriver Protocol** | ❌ Not implemented | No `_vmm_driver.py` exists. Controller directly imports `FirecrackerClient`. |
| **VMMFactory** | ❌ Not implemented | No `_vmm_factory.py` exists. Firecracker is hardcoded everywhere. |
| **FirecrackerDriver** | ❌ Not implemented | `FirecrackerSpawner` and `FirecrackerClient` remain inline in `_firecracker.py`. |
| **CrosvmDriver** | ❌ Not started | Not a single file exists. |
| **VMMConfig** | ❌ Not implemented | `FirecrackerConfig` is still the only config model. |
| **vmm_type on VMInstanceItem** | ❌ Not implemented | No `vmm_type` field. Columns named `api_socket_path`, `config_path`. |
| **DB Migration (vmm_type)** | ❌ Not implemented | Schema `001_initial_schema.sql` has no `vmm_type` or renamed columns. |
| **Controller accepts IVMMDriver** | ❌ Not implemented | `VMController.__init__` only takes `(entity, repo)`, creates `FirecrackerClient` directly. |
| **--vmm CLI flag** | ❌ Not implemented | CLI still uses `--firecracker-bin`. No `--vmm` or `--vmm-bin` flags. |
| **Constants cleanup** | ❌ Not implemented | `FIRECRACKER_GITHUB_RELEASES_API_URL`, `FIRECRACKER_GITHUB_DOWNLOAD_URL`, `DEFAULT_FIRECRACKER_CI_VERSION` still in `constants.py`. |
| **Binary domain VMM-agnostic** | ❌ Not implemented | Binary domain is still Firecracker-specific. |
| **Console source abstraction** | ❌ Not implemented | Console relay reads Firecracker's serial output file only. |
| **`FeatureNotSupported` exception** | ❌ Not implemented | No such exception in `exceptions.py`. |
| **`models/__init__.py` exports** | ❌ Not implemented | Still exports `FirecrackerConfig`, `DriveConfig`. |
| **`core/vm/__init__.py` exports** | ❌ Not implemented | Still exports `FirecrackerSpawner`. No `IVMMDriver` exports. |

**Conclusion:** This document remains a design proposal. All 15 tracked components are unstarted.

---

**Status:** Proposal / Design Document
**Date:** 2026-05-08
**Author:** mvmctl engineering

---

## Table of Contents

1. [Motivation](#1-motivation)
2. [Current State: Firecracker Coupling Map](#2-current-state-firecracker-coupling-map)
3. [Target Architecture Overview](#3-target-architecture-overview)
4. [The `IVMMDriver` Protocol](#4-the-ivmmdriver-protocol)
5. [VMM Feature Capability Matrix](#5-vmm-feature-capability-matrix)
6. [Layer-by-Layer Changes](#6-layer-by-layer-changes)
7. [Concrete VMM Candidates](#7-concrete-vmm-candidates)
8. [Migration Strategy (Phased)](#8-migration-strategy-phased)
9. [Risks & Trade-offs](#9-risks--trade-offs)
10. [Appendix: Driver Implementation Skeleton](#10-appendix-driver-implementation-skeleton)

---

## 1. Motivation

mvmctl currently supports **exactly one VMM: Firecracker (AWS)**. The entire codebase is hardwired to Firecracker — its JSON config format, its HTTP-over-Unix-socket API, its device naming, its boot flow, its binary download URLs, and its file-naming conventions.

This coupling creates several problems:

- **No freedom to choose.** Users who want GPU passthrough (crosvm), full system emulation (QEMU), or a different security model cannot — unless they fork the project.
- **Single point of failure.** A Firecracker regression, licensing change, or architectural shift upstream leaves the project with no alternative.
- **Feature ceiling.** Firecracker intentionally omits features (GPU, audio, virtio-fs, vhost-user, block hotplug) that other VMMs support. Users who need these hit a wall.
- **Adoption barrier.** Teams already invested in QEMU/KVM tooling cannot incrementally adopt mvmctl — they must switch VMMs entirely.

**Goal:** Decouple the VMM from the orchestration layer so that:

- Multiple VMMs can coexist in the same install.
- Users select a VMM per-VM or via a system default.
- The project is future-proof for new VMM types (guestfs-accelerated VMs, jailed QEMU, etc.).
- 80%+ of the codebase (network, images, kernels, SSH keys, cloud-init, provisioning, DB, resolvers) is VMM-agnostic and unchanged.

---

## 2. Current State: Firecracker Coupling Map

Every Firecracker-specific reference in the codebase. This defines the scope of work.

### 2.1 Tightly Coupled (Must Be Abstracted) — ALL STILL PRESENT ❌

| File / Location | Coupling | What Needs to Change | Status |
|---|---|---|---|
| `core/vm/_firecracker.py` | `FirecrackerSpawner` — writes JSON config, launches with `--config-file` | Extract into a `FirecrackerDriver` implementing `IVMMDriver`. | ❌ Unchanged |
| `core/vm/_firecracker.py` | `FirecrackerClient` — HTTP-over-Unix-socket client for runtime control | Extract into `FirecrackerDriver`. | ❌ Unchanged |
| `core/vm/_firecracker.py` | `UnixSocketHTTPConnection` — custom HTTP transport | Moves inside `FirecrackerDriver`. | ❌ Unchanged |
| `models/firecracker.py` | `FirecrackerConfig` dataclass — Firecracker-specific fields | Becomes internal to `FirecrackerDriver`. | ❌ Unchanged |
| `core/vm/_controller.py` | Direct `FirecrackerClient` imports for pause/resume/start/snapshot | Controller accepts `IVMMDriver` instead. | ❌ Unchanged (still imports `FirecrackerClient` directly) |
| `core/vm/__init__.py` | Exports `FirecrackerSpawner` as public symbol | Exports `IVMMDriver` protocol and VMM factory instead. | ❌ Unchanged |
| `api/vm_operations.py` | `build_firecracker_config()`, `FirecrackerSpawner` usage | Replace with `build_vmm_config()`, factory-based driver creation. | ❌ Unchanged |
| `api/vm_operations.py` | Volume hotplug uses `FirecrackerClient.put_drive()` inline | Abstract through `IVMMDriver`. | ❌ Unchanged |
| `models/__init__.py` | Exports `FirecrackerConfig`, `DriveConfig` | Remove or gate behind `if TYPE_CHECKING`. | ❌ Still exported |
| `constants.py` | `OVERRIDABLE_DEFAULTS["defaults.firecracker"]` (8 filenames + log_level) | Move to `FirecrackerDriver`. | ❌ Still in constants |
| `constants.py` | `FIRECRACKER_GITHUB_RELEASES_API_URL`, `FIRECRACKER_GITHUB_DOWNLOAD_URL` | Move into `FirecrackerDriver`. | ❌ Still in constants |
| `constants.py` | `DEFAULT_FIRECRACKER_CI_VERSION` | Move into `FirecrackerDriver`. | ❌ Still in constants |
| `cli/vm.py` | `--firecracker-bin` CLI option | Replace with `--vmm` + `--vmm-bin`. | ❌ Unchanged |

### 2.2 Moderately Coupled (Pattern Differences)

| File / Location | Issue | Resolution |
|---|---|---|
| `core/vm/_provisioner.py` | `fix_fstab()` hardcodes `/dev/vda` (Firecracker VirtIO naming) | Add VMM-aware `block_device_map` parameter. |
| `models/vm.py` — `VMInstanceItem` | `api_socket_path`, `config_path` fields are Firecracker-specific | Rename to `control_socket_path`, `vmm_config_path`. |
| `services/console/` (PTY relay) | Reads Firecracker's serial output file | Console service needs VMM-aware input source. |
| `core/vm/_firecracker.py` | `_build_boot_args()` constructs kernel cmdline | Boot args are largely VMM-agnostic. Move to API layer. |
| `api/vm_operations.py` — respawn flow | `_respawn_firecracker()` re-constructs config | Generalize to `_respawn_vm()`. |

### 2.3 Loosely Coupled / Untouched

These layers are VMM-agnostic and survive intact:

| Layer | Why It Survives |
|---|---|
| `core/vm/_repository.py` | Pure DB operations — stores paths as opaque strings |
| `core/vm/_resolver.py` | Resolution logic — no VMM knowledge |
| `core/vm/_provisioner.py` (except `fix_fstab`) | Rootfs provisioning — VMM-agnostic |
| `core/vm/_service.py` | Stateless VM operations — delegates to Controller, no VMM dependency |
| `core/network/*` | Network domain — VMM-agnostic |
| `core/image/*` | Image domain — VMM-agnostic |
| `core/kernel/*` | Kernel domain — VMM-agnostic |
| `core/binary/*` | Binary domain — needs VMM type field added |
| `core/key/*` | SSH key management — VMM-agnostic |
| `core/cloudinit/*` | Cloud-init — VMM-agnostic |
| `core/volume/*` | Volume management — attach/detach abstracted through driver |
| `api/inputs/*` | Input/Request/Resolved pipeline — VMM-agnostic |
| `cli/*` (except `vm.py` VMM selection) | Most CLI commands are VMM-agnostic |
| All models except `firecracker.py` | `VMInstanceItem`, `NetworkItem`, etc. — VMM-agnostic |

---

## 3. Target Architecture Overview

The core idea: **abstract the VMM behind a driver interface**, then implement one driver per VMM type. The rest of the codebase talks to the interface, never to a concrete VMM.

```
┌──────────────────────────────────────────────────────────────┐
│                        CLI Layer                              │
│  mvm vm create --vmm crosvm --cpus 4 --mem 4096 ...          │
│  mvm vm create --vmm firecracker (default)                    │
└──────────────────────┬───────────────────────────────────────┘
                       │ VMM type enum
                       ▼
┌──────────────────────────────────────────────────────────────┐
│                        API Layer                              │
│  VMOperation.create(inputs)                                   │
│    ├─ resolves defaults (vcpus, mem, image, kernel, ...)      │
│    ├─ network setup (bridge, TAP, lease)                      │
│    ├─ rootfs provisioning (resize, cloud-init, SSH)           │
│    ├─ VMMFactory.create_driver(vmm_type, vm, repo) ──────┐   │
│    │   returns IVMMDriver (FirecrackerDriver | CrosvmDriver)  │
│    ├─ driver.generate_config(...)                             │
│    ├─ driver.spawn()                                          │
│    └─ driver.start_vm()                                       │
└──────────────────────┬───────────────────────────────────────┘
                       │ IVMMDriver protocol
                       ▼
┌──────────────────────────────────────────────────────────────┐
│                   Core / VM Domain                            │
│                                                               │
│  ┌─────────────────────────────────────────────────────┐      │
│  │           IVMMDriver (Protocol)                     │      │
│  │  ┌─────────────────┐  ┌──────────────────────┐     │      │
│  │  │ FirecrackerDriver│  │   CrosvmDriver       │     │      │
│  │  │                 │  │                      │     │      │
│  │  │ - spawn()       │  │ - spawn()            │     │      │
│  │  │ - stop()        │  │ - stop()             │     │      │
│  │  │ - pause()       │  │ - pause()            │     │      │
│  │  │ - resume()      │  │ - resume()           │     │      │
│  │  │ - attach_drive()│  │ - attach_drive()     │     │      │
│  │  │ - detach_drive()│  │ - resize_disk()      │     │      │
│  │  │ - snapshot()    │  │ - (unsupported)      │     │      │
│  │  │ - console_fd()  │  │ - console_fd()       │     │      │
│  │  └─────────────────┘  └──────────────────────┘     │      │
│  └─────────────────────────────────────────────────────┘      │
│                                                               │
│  VMController(entity, repo, driver: IVMMDriver)                │
│    - delegates start/stop/pause/resume to driver               │
│    - state machine unchanged                                   │
│                                                               │
│  VMMFactory.create_driver(vmm_type, vm, repo) → IVMMDriver    │
│    - registry of VMM types to driver classes                   │
│    - auto-detection from binary or config                      │
└──────────────────────────────────────────────────────────────┘
```

### Key Architectural Principles

1. **The driver owns VMM-specific knowledge.** Config format, CLI arguments, API protocol, device naming, feature availability — all inside the driver.
2. **VMController stays as the state machine.** It accepts an `IVMMDriver` and delegates lifecycle operations. State validation (RUNNING/PAUSED/STOPPED transitions) remains in Controller.
3. **The factory is the only place with VMM switching logic.** `VMMFactory.create_driver()` maps VMM type to driver class. The rest of the codebase never switches on VMM type.
4. **VMM-agnostic operations stay in API layer.** Network setup, rootfs provisioning, cloud-init, SSH keys — these do NOT belong in drivers.
5. **Feature gaps are explicit.** Each driver declares its capabilities via a `capabilities: VMMCapabilities` property. The API layer checks capabilities before calling optional features.

---

## 4. The `IVMMDriver` Protocol

### 4.1 Core Protocol

```python
# src/mvmctl/core/vm/_vmm_driver.py

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from pathlib import Path
from typing import Protocol, runtime_checkable


class VMMType(StrEnum):
    FIRECRACKER = "firecracker"
    CROSVM = "crosvm"
    QEMU = "qemu"
    # Future: GUESTFS, HOST_VM, ...


@dataclass(frozen=True)
class VMMCapabilities:
    """Explicit feature availability per VMM."""
    snapshots: bool
    block_hotplug: bool
    network_hotplug: bool
    gpu_passthrough: bool
    virtio_fs: bool
    vhost_net: bool
    gdb_debugging: bool
    tpm: bool
    pcie: bool
    console_pty: str  # "file", "stdout", "tcp", "socket"
    initrd_support: bool
    persistent_memory: bool
    suspend_resume: bool
    live_migration: bool


@runtime_checkable
class IVMMDriver(Protocol):
    """Interface for all VMM driver implementations."""

    # ── Metadata ────────────────────────────────────────────
    vmm_type: VMMType
    capabilities: VMMCapabilities

    # ── Lifecycle ────────────────────────────────────────────
    def generate_config(self, config: VMMConfig) -> None:
        """Generate and persist VMM configuration (JSON, CLI args, etc.)."""
        ...

    def spawn(self) -> int:
        """Launch the VMM process. Returns PID."""
        ...

    def start_vm(self) -> None:
        """Post-spawn VM start (Firecracker: InstanceStart; Crosvm: no-op)."""
        ...

    def is_running(self) -> bool:
        """Check if the VMM process is alive."""
        ...

    def wait_for_boot(self, timeout: float | None = None) -> None:
        """Wait until VM is booted and responsive."""
        ...

    # ── Runtime Control ─────────────────────────────────────
    def stop(self, force: bool = False) -> None:
        """Graceful (ACPI) or forced shutdown."""
        ...

    def pause(self) -> None:
        """Suspend VM execution."""
        ...

    def resume(self) -> None:
        """Resume VM execution."""
        ...

    def reboot(self) -> None:
        """Reboot the guest."""
        ...

    # ── Storage ─────────────────────────────────────────────
    def attach_drive(self, drive_spec: DriveAttachment) -> None:
        """Attach a block device at runtime."""
        ...

    def detach_drive(self, drive_id: str) -> None:
        """Detach a block device at runtime."""
        ...

    def resize_disk(self, drive_id: str, new_size_mib: int) -> None:
        """Resize a block device at runtime."""
        ...

    # ── Snapshots ───────────────────────────────────────────
    def create_snapshot(self, mem_path: Path, state_path: Path) -> None:
        """Snapshot VM state."""
        ...

    def load_snapshot(self, mem_path: Path, state_path: Path) -> None:
        """Load VM from snapshot."""
        ...

    # ── Console ─────────────────────────────────────────────
    @property
    def console_source(self) -> ConsoleSource:
        """How to obtain console output (file path, FD, TCP addr, etc.)."""
        ...

    # ── Cleanup ─────────────────────────────────────────────
    def close(self) -> None:
        """Release resources (sockets, file handles, temp files)."""
        ...
```

### 4.2 Supporting Data Types

```python
@dataclass
class VMMConfig:
    """VMM-agnostic configuration — the API layer builds this."""
    vmm_type: VMMType
    vm_dir: Path
    vm_id: str

    # Machine
    vcpu_count: int
    mem_size_mib: int
    enable_pci: bool

    # Boot
    kernel_path: str
    kernel_cmdline: str
    initrd_path: str | None = None

    # Storage
    rootfs_path: str
    rootfs_read_only: bool = False
    extra_drives: list[DriveAttachment] = field(default_factory=list)

    # Network
    tap_name: str | None = None
    guest_mac: str | None = None

    # Console / Logging
    enable_console: bool = True
    enable_logging: bool = True
    log_level: str = "Info"

    # VMM-specific overrides (passed through, not interpreted)
    vmm_options: dict[str, Any] = field(default_factory=dict)


@dataclass
class DriveAttachment:
    """A block device to attach to a VM."""
    drive_id: str
    path_on_host: str
    is_root_device: bool = False
    is_read_only: bool = False


@dataclass
class ConsoleSource:
    """Describes how to access a VM's console."""
    kind: str  # "file", "stdout", "tcp", "socket"
    path: str | None = None  # File path or socket path
    host: str | None = None  # TCP host (if kind == "tcp")
    port: int | None = None  # TCP port (if kind == "tcp")
    fd: int | None = None  # File descriptor (if kind == "stdout" with pipe)
```

### 4.3 VMM Factory

```python
# src/mvmctl/core/vm/_vmm_factory.py

class VMMFactory:
    """Registry + factory for VMM drivers."""

    _registry: dict[VMMType, type[IVMMDriver]] = {}

    @classmethod
    def register(cls, vmm_type: VMMType, driver_cls: type[IVMMDriver]) -> None:
        cls._registry[vmm_type] = driver_cls

    @classmethod
    def create_driver(
        cls,
        vmm_type: VMMType,
        vm: VMInstanceItem,
        repo: VMRepository,
    ) -> IVMMDriver:
        driver_cls = cls._registry.get(vmm_type)
        if driver_cls is None:
            raise ValueError(f"Unknown VMM type: {vmm_type}")
        return driver_cls(vm, repo)
```

Registration happens at import time in each driver module:

```python
# src/mvmctl/core/vm/_firecracker_driver.py
VMMFactory.register(VMMType.FIRECRACKER, FirecrackerDriver)

# src/mvmctl/core/vm/_crosvm_driver.py
VMMFactory.register(VMMType.CROSVM, CrosvmDriver)
```

---

## 5. VMM Feature Capability Matrix

This matrix determines what features are available per VMM. The API layer checks `driver.capabilities` before calling optional features.

| Feature | Firecracker | Crosvm | QEMU | Notes |
|---|---|---|---|---|
| **Block storage** | raw, qcow2 | raw, qcow2, qcow3, zstd | raw, qcow2, vmdk, vdi, +more | QEMU supports nearly every format |
| **Block hotplug** | ✅ `PUT /drives` | ❌ (only resize) | ✅ `device_add` via QMP | — |
| **Network** | TAP | TAP, vhost-net, slirp | TAP, vhost-net, slirp, bridge | — |
| **Network hotplug** | ❌ | ⚠️ Experimental | ✅ QMP `device_add` | — |
| **Snapshots** | ✅ Production, stable format | ❌ Experimental, unstable | ✅ `savevm`/`loadvm`, qcow2 internal | Firecracker diff snapshots supported |
| **GPU / Display** | ❌ | ✅ virtio-gpu, virgl, Vulkan | ✅ virtio-gpu, virgl, VGA, SPICE | — |
| **virtio-fs** | ❌ | ✅ | ✅ (via vhost-user-fs) | — |
| **vhost-net** | ❌ (software virtio) | ✅ | ✅ | Kernel-bypass for network |
| **vhost-user** | ❌ | ✅ | ✅ | Userspace device backends |
| **TPM** | ❌ | ✅ | ✅ (swtpm) | Virtual Trusted Platform Module |
| **GDB debugging** | ❌ | ✅ (`--gdb`) | ✅ (`-s`, `-gdb`) | Kernel debug |
| **Console output** | File-based | stdout | tcp/file/stdout | Console relay needs adaptation per VMM |
| **Suspend/Resume** | ✅ Pause/resume | ✅ suspend/resume | ✅ `stop`/`cont` via QMP | — |
| **PCIe** | ❌ (limited PCI) | ✅ | ✅ | Needed for hotplug, GPU |
| **Initrd** | ✅ | ✅ | ✅ | — |
| **Persistent memory** | ❌ | ✅ (`--pmem`) | ✅ (`-object memory-backend-file`) | DAX support |
| **Live migration** | ❌ | ❌ | ✅ (TCP, exec, fd) | — |
| **Boot speed** | Very fast | Fast | Slower (BIOS/EFI) | MicroVM-optimized vs full virt |
| **Memory overhead** | ~5MB per VM | ~15-30MB per VM (per-device processes) | ~30-100MB per VM | Crosvm: per-device forking adds overhead |
| **Process isolation** | Single process (+optional jailer) | Process-per-device (minijail) | Single process (+optional) | Crosvm has strongest isolation |
| **Architectures** | x86_64, aarch64 | x86_64, aarch64, riscv64 | x86_64, aarch64, riscv64, arm, mips, ppc, s390x | QEMU supports the most |
| **initrd support** | ✅ | ✅ | ✅ | — |
| **Binary size** | ~15MB | ~15MB | ~50-100MB+ | QEMU varies by build config |
| **Setup complexity** | Low (single binary) | Low (single binary) | Higher (multiple binaries, depends on devices) | — |

### Capability Check Pattern

```python
class VMOperation:
    @staticmethod
    def attach_volume(inputs: VolumeAttachInput) -> OperationResult:
        resolved = VolumeAttachRequest(inputs, db).resolve()
        driver = VMMFactory.create_driver(resolved.vm.vmm_type, resolved.vm, repo)
        
        if not driver.capabilities.block_hotplug:
            raise MVMError(
                f"VMM '{driver.vmm_type}' does not support block hot-plug. "
                "Attach volumes at VM creation time instead."
            )
        
        driver.attach_drive(DriveAttachment(
            drive_id=resolved.volume.id,
            path_on_host=resolved.volume.path,
        ))
        ...
```

---

## 6. Layer-by-Layer Changes

### 6.1 Models Layer ❌ NOT IMPLEMENTED

**New files proposed:**

| File | Contents |
|---|---|
| `models/vmm.py` | `VMMType` enum, `VMMConfig`, `VMMCapabilities`, `DriveAttachment`, `ConsoleSource` |
| `models/firecracker.py` | **KEEP** but make internal to `FirecrackerDriver` (no external exports) |
| `models/crosvm.py` | NEW — Crosvm-specific config (internal to driver) |

### 6.2 Core / VM Domain ❌ NOT IMPLEMENTED

**New files proposed:**

| File | Contents |
|---|---|
| `core/vm/_vmm_driver.py` | `IVMMDriver` protocol, `VMMType`, `VMMCapabilities`, `VMMConfig`, `DriveAttachment`, `ConsoleSource` |
| `core/vm/_vmm_factory.py` | `VMMFactory` registry + factory method |
| `core/vm/_firecracker_driver.py` | `FirecrackerDriver` — extracts ~800 lines from `_firecracker.py` into driver shape |
| `core/vm/_crosvm_driver.py` | `CrosvmDriver` — new implementation |
| `core/vm/_qemu_driver.py` | `QEMUDriver` — future implementation |

### 6.3 API Layer ❌ NOT IMPLEMENTED

### 6.4 CLI Layer ❌ NOT IMPLEMENTED

### 6.5 Constants ❌ NOT IMPLEMENTED

### 6.6 DB Schema ❌ NOT IMPLEMENTED

### 6.7 Services / Console Relay ❌ NOT IMPLEMENTED

---

## 7. Concrete VMM Candidates

### 7.1 Crosvm (Google) ❌ NOT IMPLEMENTED

### 7.2 QEMU (Full System Virtualization) ❌ NOT IMPLEMENTED

### 7.3 GuestFS-Accelerated VM ❌ NOT IMPLEMENTED

### 7.4 Host VM (Nested Virtualization) ❌ NOT IMPLEMENTED

---

## 8. Migration Strategy (Phased) — ALL PHASES PENDING

The migration should happen in discrete, reversible phases. Each phase ends with a working codebase.

### Phase 1: Extract the Protocol (No Functional Change) ❌ NOT STARTED

**Goal:** Define `IVMMDriver`, `VMMConfig`, `VMMCapabilities`. `FirecrackerSpawner` and `FirecrackerClient` remain but implement the protocol. Controller stops calling Firecracker directly.

### Phase 2: API & CLI VMM Awareness (No New VMM Yet) ❌ NOT STARTED

### Phase 3: VMM-Agnostic Binary Management (No New VMM Yet) ❌ NOT STARTED

### Phase 4: Add Crosvm Driver (Optional) ❌ NOT STARTED

### Phase 5: Add QEMU Driver (Optional) ❌ NOT STARTED

---

## 9. Risks & Trade-offs

### 9.1 Risk: Feature Divergence

### 9.2 Risk: Dual Maintenance Burden

### 9.3 Risk: Performance Regression

### 9.4 Risk: Backward Compatibility

### 9.5 Trade-off: Increased Binary Size

### 9.6 Trade-off: Abstraction Doesn't Fit All VMMs Perfectly

---

## 10. Appendix: Driver Implementation Skeleton

### 10.1 FirecrackerDriver (Extracted from Existing Code) ❌ NOT IMPLEMENTED

### 10.2 CrosvmDriver (New) ❌ NOT IMPLEMENTED

### 10.3 FeatureNotSupported Exception ❌ NOT IMPLEMENTED

---

## Summary of New Files

| Layer | New Files | Lines (Est.) | Status |
|---|---|---|---|
| Models | `models/vmm.py` | ~100 | ❌ |
| Core/VM | `core/vm/_vmm_driver.py` (protocol) | ~150 | ❌ |
| Core/VM | `core/vm/_vmm_factory.py` (factory) | ~50 | ❌ |
| Core/VM | `core/vm/_firecracker_driver.py` (extracted) | ~800 | ❌ |
| Core/VM | `core/vm/_crosvm_driver.py` (new) | ~500 | ❌ |
| Core/VM | `core/vm/_qemu_driver.py` (future) | ~700 | ❌ |

## Summary of Modified Files

| Layer | Modified Files | Nature of Change | Status |
|---|---|---|---|
| Models | `models/vm.py`, `models/__init__.py` | Add `vmm_type` field, rename columns | ❌ |
| Core/VM | `core/vm/_controller.py`, `core/vm/__init__.py` | Accept `IVMMDriver`, delegate to driver | ❌ |
| Core/VM | `core/vm/_firecracker.py` | Trim to thin import shim or remove | ❌ |
| Core/VM | `core/vm/_provisioner.py` | VMM-aware block device naming | ❌ |
| API | `api/vm_operations.py`, `api/inputs/*` | VMM-agnostic config building, factory usage | ❌ |
| CLI | `cli/vm.py` | `--vmm`/`--vmm-bin` flags, generic help text | ❌ |
| Constants | `constants.py` | Remove Firecracker-specific URLs/filenames | ❌ |
| DB | `db/migrations/NNN_vmm_agnostic.sql` | New migration | ❌ |
| Services | `services/console/` | Console source abstraction | ❌ |
| Public API | `api/__init__.py`, `models/__init__.py` | Replace `FirecrackerConfig` exports with VMM-agnostic types | ❌ |

---

## Appendix: Phased Timeline

| Phase | Description | Duration | Dependencies | Status |
|---|---|---|---|---|
| **P1** | Extract protocol, adapter, Factory, Controller + API refactor | 3-4 days | None | ❌ |
| **P2** | CLI VMM flags, backward compat, constants cleanup, DB migration | 2 days | P1 | ❌ |
| **P3** | VMM-agnostic binary management, remove FC-specific URLs | 1-2 days | P1-P2 | ❌ |
| **P4** | Crosvm driver | 8-10 days | P1-P3 | ❌ |
| **P5** | QEMU driver | 10-14 days | P1-P3 | ❌ |
| **Testing** | Driver conformance tests, system tests per VMM | 3-5 days | P1-P5 | ❌ |

**Minimum viable VMM-agnostic (P1-P3):** ~6-8 days. At this point the abstraction is proven, backward compat is clean, and a new VMM driver can be added as a self-contained PR.
