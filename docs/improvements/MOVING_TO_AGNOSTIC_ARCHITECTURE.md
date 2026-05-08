# Moving to a VMM-Agnostic Architecture

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
   - [6.1 Models Layer](#61-models-layer)
   - [6.2 Core / VM Domain](#62-core--vm-domain)
   - [6.3 API Layer](#63-api-layer)
   - [6.4 CLI Layer](#64-cli-layer)
   - [6.5 Constants](#65-constants)
   - [6.6 DB Schema](#66-db-schema)
   - [6.7 Services / Console Relay](#67-services--console-relay)
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

### 2.1 Tightly Coupled (Must Be Abstracted)

| File / Location | Coupling | What Needs to Change |
|-----------------|----------|----------------------|
| `core/vm/_firecracker.py` (881 lines) | `FirecrackerSpawner` — writes JSON config, launches with `--config-file` | Extract into a `FirecrackerDriver` implementing `IVMMDriver`. Config generation moves into driver. |
| `core/vm/_firecracker.py` (341 lines) | `FirecrackerClient` — HTTP-over-Unix-socket client for runtime control | Extract into `FirecrackerDriver`. Runtime commands become driver methods. |
| `core/vm/_firecracker.py` | `UnixSocketHTTPConnection` — custom HTTP transport | Moves inside `FirecrackerDriver`. |
| `models/firecracker.py` (86 lines) | `FirecrackerConfig` dataclass — Firecracker-specific fields | Becomes internal to `FirecrackerDriver`. New `VMMConfig` union type for external use. |
| `core/vm/_controller.py` (360 lines) | Direct `FirecrackerClient` imports for pause/resume/start/snapshot | Controller accepts `IVMMDriver` instead of hardcoding Firecracker. |
| `core/vm/__init__.py` | Exports `FirecrackerSpawner` as public symbol | Exports `IVMMDriver` protocol and VMM factory instead. |
| `api/vm_operations.py` | `build_firecracker_config()` method, direct `FirecrackerSpawner` usage | Replace with `build_vmm_config(vmm_type, ...)`, factory-based driver creation. |
| `api/vm_operations.py` | Volume hotplug uses `FirecrackerClient.put_drive()` inline | Abstract volume operations through `IVMMDriver.attach_drive()` / `detach_drive()`. |
| `models/__init__.py` | Exports `FirecrackerConfig`, `DriveConfig` | Remove or gate behind `if TYPE_CHECKING`. |
| `constants.py` | `OVERRIDABLE_DEFAULTS["defaults.firecracker"]` (8 filenames + log_level) | Move to `FirecrackerDriver`. Add VMM-agnostic defaults section. |
| `constants.py` | `FIRECRACKER_GITHUB_RELEASES_API_URL`, `FIRECRACKER_GITHUB_DOWNLOAD_URL` | Move into `FirecrackerDriver`. |
| `constants.py` | `DEFAULT_FIRECRACKER_CI_VERSION` | Move into `FirecrackerDriver`. |
| `cli/vm.py` | `--firecracker-bin` CLI option, "Create and start a new Firecracker VM" help text | Replace with `--vmm` (enum) + `--vmm-bin` (per-VMM override). |

### 2.2 Moderately Coupled (Pattern Differences)

| File / Location | Issue | Resolution |
|-----------------|-------|------------|
| `core/vm/_provisioner.py` | `fix_fstab()` hardcodes `/dev/vda` (Firecracker VirtIO naming) | Add VMM-aware `block_device_map` parameter. |
| `models/vm.py` — `VMInstanceItem` | `api_socket_path`, `config_path` fields are Firecracker-specific | Rename to `control_socket_path`, `vmm_config_path` or keep as opaque paths. |
| `services/console/` (PTY relay) | Reads Firecracker's serial output file | Crosvm sends serial to stdout; QEMU uses `-serial file:...` or TCP. Console service needs VMM-aware input source. |
| `core/vm/_firecracker.py` | `_build_boot_args()` constructs kernel cmdline with Firecracker-specific `ip=` syntax | Boot args are largely VMM-agnostic (kernel parameters). The `ip=` network config is kernel-level, not VMM-level — should work across VMMs. Move to API layer. |
| `api/vm_operations.py` — respawn flow | `_respawn_firecracker()` re-constructs config | Generalize to `_respawn_vm()`. |

### 2.3 Loosely Coupled / Untouched

These layers are VMM-agnostic and survive intact:

| Layer | Why It Survives |
|-------|-----------------|
| `core/vm/_repository.py` | Pure DB operations — stores `control_socket_path` as opaque string |
| `core/vm/_resolver.py` | Resolution logic — no VMM knowledge |
| `core/vm/_provisioner.py` (except `fix_fstab`) | Rootfs provisioning (resize, hostname, SSH, cloud-init) — VMM-agnostic |
| `core/vm/_service.py` | Stateless VM operations — delegates to Controller, no VMM dependency |
| `core/network/*` | Network domain — bridge, TAP, iptables — VMM-agnostic |
| `core/image/*` | Image domain — download, cache, materialize — VMM-agnostic |
| `core/kernel/*` | Kernel domain — download, cache, resolve — VMM-agnostic |
| `core/binary/*` | Binary domain — download, cache — needs VMM type field added |
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
│    │   returns IVMMDriver (FirecrackerDriver | CrosvmDriver) │  │
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
|---------|-------------|--------|------|-------|
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

### 6.1 Models Layer

**New files:**

| File | Contents |
|------|----------|
| `models/vmm.py` | `VMMType` enum, `VMMConfig`, `VMMCapabilities`, `DriveAttachment`, `ConsoleSource` |
| `models/firecracker.py` | **KEEP** but make internal to `FirecrackerDriver` (no external exports) |
| `models/crosvm.py` | NEW — Crosvm-specific config (internal to driver) |

**Modified files:**

| File | Change |
|------|--------|
| `models/vm.py` — `VMInstanceItem` | Add `vmm_type: str` field. Rename `api_socket_path` → `control_socket_path`, `config_path` → `vmm_config_path` (or keep as-is with deprecation shim) |
| `models/__init__.py` | Remove `FirecrackerConfig`, `DriveConfig` from public exports. Add `VMMType`, `VMMConfig`, `VMMCapabilities` |

**`VMMConfig` design:**

```python
@dataclass
class VMMConfig:
    """VMM-agnostic config — built by API layer, consumed by drivers."""
    
    vmm_type: VMMType
    vm_dir: Path
    
    # Machine
    vcpu_count: int
    mem_size_mib: int
    
    # Boot
    kernel_path: str
    kernel_cmdline: str
    initrd_path: str | None
    
    # Storage
    rootfs_path: str
    extra_drives: list[DriveAttachment]
    
    # Network
    tap_name: str | None
    guest_mac: str | None
    guest_ip: str | None
    network_gateway: str | None
    network_netmask: str | None
    
    # Console / Logging
    enable_console: bool
    enable_logging: bool
    log_level: str
    
    # Cloud-init
    cloud_init_mode: CloudInitMode | None
    cloud_init_iso_path: Path | None
    cloud_init_nocloud_url: str | None
    
    # Spawn behavior
    relay_enabled: bool
    snapshot_mode: bool
    
    # VMM-specific passthrough
    vmm_options: dict[str, Any]  # Driver interprets these
```

### 6.2 Core / VM Domain

**New files:**

| File | Contents |
|------|----------|
| `core/vm/_vmm_driver.py` | `IVMMDriver` protocol, `VMMType`, `VMMCapabilities`, `VMMConfig`, `DriveAttachment`, `ConsoleSource` |
| `core/vm/_vmm_factory.py` | `VMMFactory` registry + factory method |
| `core/vm/_firecracker_driver.py` | `FirecrackerDriver` — extracts ~800 lines from `_firecracker.py` into driver shape |
| `core/vm/_crosvm_driver.py` | `CrosvmDriver` — new implementation |
| `core/vm/_qemu_driver.py` | `QEMUDriver` — future implementation |

**Modified files:**

| File | Change |
|------|--------|
| `core/vm/_firecracker.py` | Either **delete** (code moved to `_firecracker_driver.py`) or trim to a thin import shim for backward compat |
| `core/vm/_controller.py` | `__init__` accepts `driver: IVMMDriver \| None = None`. If no driver, factory creates from `vm.vmm_type`. All lifecycle methods delegate to `self._driver` instead of creating `FirecrackerClient` inline. |
| `core/vm/__init__.py` | Export `IVMMDriver`, `VMMType`, `VMMFactory`, `VMMCapabilities`. Remove `FirecrackerSpawner`. |

**Controller before/after:**

```python
# BEFORE
class VMController:
    def __init__(self, entity, repo):
        self._vm = resolve(entity, repo)
    
    def pause(self) -> None:
        client = FirecrackerClient(self._vm.vm_dir / self._vm.api_socket_path)
        client.pause_vm()

# AFTER
class VMController:
    def __init__(self, entity, repo, driver: IVMMDriver | None = None):
        self._vm = resolve(entity, repo)
        self._driver = driver or VMMFactory.create_driver(
            VMMType(self._vm.vmm_type), self._vm, repo
        )
    
    def pause(self) -> None:
        self._driver.pause()
```

**Firecracker before/after:**

```python
# BEFORE — core/vm/_firecracker.py
class FirecrackerSpawner:
    def generate(self) -> FirecrackerConfigDict: ...
    def write_to_file(self) -> None: ...
    def spawn(self) -> int: ...

class FirecrackerClient:
    def __init__(self, socket_path): ...
    def start_instance(self): ...
    def pause_vm(self): ...
    def send_ctrl_alt_del(self): ...
    def put_drive(self, ...): ...
    def create_snapshot(self, ...): ...

# AFTER — core/vm/_firecracker_driver.py
@VMMFactory.register(VMMType.FIRECRACKER)
class FirecrackerDriver:
    def __init__(self, vm: VMInstanceItem, repo: VMRepository):
        self._vm = vm
        self._repo = repo
    
    # IVMMDriver implementation
    def generate_config(self, config: VMMConfig) -> None:
        # Build Firecracker JSON internally; write to vm_dir
        ...
    
    def spawn(self) -> int:
        # Launch: firecracker --api-sock ... --config-file ...
        ...
    
    def pause(self) -> None:
        # HTTP PATCH /vm via Unix socket
        ...
    
    def attach_drive(self, drive: DriveAttachment) -> None:
        # HTTP PUT /drives/{id}
        ...
    
    @property
    def capabilities(self) -> VMMCapabilities:
        return VMMCapabilities(
            snapshots=True,
            block_hotplug=True,
            network_hotplug=False,
            gpu_passthrough=False,
            console_pty="file",
            ...
        )
```

### 6.3 API Layer

**Modified files:**

| File | Change |
|------|--------|
| `api/vm_operations.py` | `build_firecracker_config()` → `build_vmm_config(vmm_type, ...)`. `VMCreateContext.execute()` uses `VMMFactory` instead of `FirecrackerSpawner`. Volume hotplug uses `driver.attach_drive()`. |
| `api/inputs/_vm_create_input.py` | Add `vmm_type: VMMType \| None` field (default: `VMMType.FIRECRACKER`). |

**Build config flow:**

```python
# BEFORE — api/vm_operations.py
ctx = VMCreateContext(name)
config = FirecrackerConfig(
    binary_path=resolved.binary.path,
    kernel_path=resolved.kernel.path,
    vcpu_count=resolved.vcpu_count,
    ...
)
spawner = FirecrackerSpawner(config)
spawner.write_to_file()
spawner.spawn()

# AFTER
ctx = VMCreateContext(name)
vmm_config = VMMConfig(
    vmm_type=resolved.vmm_type,
    kernel_path=resolved.kernel.path,
    vcpu_count=resolved.vcpu_count,
    ...
)
driver = VMMFactory.create_driver(
    resolved.vmm_type, vm_instance, vm_repo
)
driver.generate_config(vmm_config)
driver.spawn()
```

### 6.4 CLI Layer

**Modified files:**

| File | Change |
|------|--------|
| `cli/vm.py` | `--firecracker-bin` flag becomes `--vmm-bin` (path, applies to all VMMs). New `--vmm` flag: `--vmm firecracker|crosvm|qemu` (default from config). Help text generalized. |
| `cli/vm.py` | VM create flow passes `vmm_type` through to `VMCreateInput`. |

```python
# cli/vm.py — BEFORE
@handle_errors
def vm_create(
    name: str = typer.Argument(...),
    firecracker_bin: Optional[Path] = typer.Option(None, "--firecracker-bin"),
    ...
):

# cli/vm.py — AFTER
@handle_errors
def vm_create(
    name: str = typer.Argument(...),
    vmm: VMMType = typer.Option(VMMType.FIRECRACKER, "--vmm",
                                  help="VMM to use"),
    vmm_bin: Optional[Path] = typer.Option(None, "--vmm-bin",
                                            help="Path to VMM binary"),
    ...
):
```

### 6.5 Constants

**Changes:**

| Section | Change |
|---------|--------|
| `OVERRIDABLE_DEFAULTS["defaults.firecracker"]` | **Remove** — move into `FirecrackerDriver` as private defaults |
| `OVERRIDABLE_DEFAULTS["defaults.vm"]` | Add `vmm_type: str = "firecracker"` (system-wide default VMM) |
| `FIRECRACKER_GITHUB_RELEASES_API_URL` | **Remove** — move into `FirecrackerDriver` |
| `FIRECRACKER_GITHUB_DOWNLOAD_URL` | **Remove** — move into `FirecrackerDriver` |
| `DEFAULT_FIRECRACKER_CI_VERSION` | **Remove** — move into `FirecrackerDriver` |
| `KERNEL_TYPE_FIRECRACKER` | Keep for now (kernel type is orthogonal to VMM type) |

**Rationale for moving constants into drivers:** A VMM driver should be self-contained. If we add CrosvmDriver, its download URLs and binary versions belong inside the driver, not in the global constants file. The constants file should only contain VMM-agnostic project defaults.

### 6.6 DB Schema

**Migration: `NNN_vmm_agnostic.sql`**

```sql
-- Add VMM type column to vm_instances
ALTER TABLE vm_instances ADD COLUMN vmm_type TEXT NOT NULL DEFAULT 'firecracker';

-- Rename columns for VMM-agnostic naming
ALTER TABLE vm_instances RENAME COLUMN api_socket_path TO control_socket_path;
ALTER TABLE vm_instances RENAME COLUMN config_path TO vmm_config_path;
```

Note: SQLite does not support `RENAME COLUMN` before 3.25.0 (2018). If targeting older SQLite, use the standard `CREATE TABLE ... AS SELECT` migration pattern. Since mvmctl ships with its own SQLite, this should be fine.

**`VMInstanceItem` update:**

```python
@dataclass
class VMInstanceItem:
    vmm_type: str = "firecracker"  # New field
    control_socket_path: str | None = None  # Renamed from api_socket_path
    vmm_config_path: str | None = None  # Renamed from config_path
    # Legacy fields — keep for backward compat during migration
    api_socket_path: str | None = None  # Deprecated, maps to control_socket_path
    config_path: str | None = None  # Deprecated, maps to vmm_config_path
```

### 6.7 Services / Console Relay

The console relay (`services/console/`) currently reads Firecracker's serial output file. Different VMMs output serial differently:

| VMM | Console Output | Relay Adaptation |
|-----|---------------|------------------|
| Firecracker | Writes to file (`firecracker.console.log`) | Current behavior — read file |
| Crosvm | Sends to **stdout** by default | Pipe crosvm stdout through relay; or use `--serial file=path` |
| QEMU | `-serial file:path` or `-serial tcp:host:port` | File: same as Firecracker. TCP: relay connects as TCP client |

**Console source abstraction:**

```python
# In the driver or a console helper:
class ConsoleSource:
    kind: str  # "file", "stdout", "tcp"
    path: str | None = None
    fd: int | None = None

# relay process reads from source:
if source.kind == "file":
    # tail -f source.path
elif source.kind == "stdout":
    # read from pipe FD
elif source.kind == "tcp":
    # socket.connect(source.host, source.port)
```

The relay binary (`mvm-console-relay`) would need a `--console-source` flag or env var to specify how to read console output. This is a small change — the relay already accepts a socket path and FD.

---

## 7. Concrete VMM Candidates

### 7.1 Crosvm (Google)

- **Strengths:** Process-per-device isolation, GPU passthrough, vhost-net, virtio-fs, single binary, fast boot, riscv64
- **Weaknesses:** No block hotplug, experimental snapshots, no production versioning, smaller community
- **Best for:** Desktop workloads, GPU-accelerated microVMs, security-sensitive deployments
- **Binary:** Single `crosvm` binary from ChromeOS toolchain or GitHub releases
- **Integration effort:** ~8-10 days (new driver, console adaptation, feature gap handling)

### 7.2 QEMU (Full System Virtualization)

- **Strengths:** Most feature-rich, widest architecture support, QMP for runtime control, live migration, block hotplug via `device_add`, mature and battle-tested
- **Weaknesses:** Slower boot (BIOS/EFI initialization), larger binary, more complex command-line, heavier resource footprint
- **Best for:** Workloads needing live migration, full ACPI/EFI, legacy OS support, or maximum device compatibility
- **Binary:** `qemu-system-x86_64` (or per-arch) — typically 50-100MB with all devices
- **Integration effort:** ~10-14 days (complex CLI, QMP protocol client, device model abstraction)
- **Note:** QEMU can be configured for microVM-style fast boot (no BIOS, direct kernel boot via `-kernel`, `-initrd`, `-append`), making it competitive with Firecracker for serverless workloads.

### 7.3 GuestFS-Accelerated VM

- **Concept:** Use `libguestfs` to create a "VM-like" environment that is actually a lightweight container with kernel filesystem access. Not a real VMM — the rootfs is mounted and processed via `guestfs` tools.
- **Best for:** Provisioning, inspection, repair operations where you need VM semantics without full virtualization overhead
- **Integration effort:** Could leverage existing `mvm-provision` and `VMProvisioner` code with a minimal "virtual" driver
- **Note:** This is speculative — no existing project does this at scale. Would be novel engineering.

### 7.4 Host VM (Nested Virtualization)

- **Concept:** A VM that runs inside another VM (e.g., Firecracker-in-Firecracker, or QEMU-in-QEMU). Useful for CI, testing, and multi-tenant isolation.
- **Best for:** Testing mvmctl itself (CI), secure sandboxing within a VM
- **Integration effort:** Low — existing drivers work unchanged; just the "host" VM would need to expose KVM to the nested VM. This is more about deployment topology than driver changes.

---

## 8. Migration Strategy (Phased)

The migration should happen in discrete, reversible phases. Each phase ends with a working codebase.

### Phase 1: Extract the Protocol (No Functional Change)

**Goal:** Define `IVMMDriver`, `VMMConfig`, `VMMCapabilities`. `FirecrackerSpawner` and `FirecrackerClient` remain but implement the protocol. Controller stops calling Firecracker directly.

**Steps:**
1. Create `core/vm/_vmm_driver.py` with `IVMMDriver` protocol, `VMMType`, `VMMConfig`, `VMMCapabilities`, `DriveAttachment`, `ConsoleSource`
2. Create `core/vm/_vmm_factory.py` with `VMMFactory` registry
3. Create `core/vm/_firecracker_driver.py` — a thin wrapper that adapts existing `FirecrackerSpawner` + `FirecrackerClient` into the `IVMMDriver` interface
4. Update `VMController.__init__()` to accept `driver: IVMMDriver | None` and use factory if None
5. Update `VMController` methods to delegate to `self._driver` instead of creating `FirecrackerClient` inline
6. Update `api/vm_operations.py` — `build_firecracker_config()` becomes `build_vmm_config()`, returns `VMMConfig`
7. Add `vmm_type` field to `VMInstanceItem` (default `"firecracker"`)
8. Create DB migration for the new column

**Verification:** All existing tests pass. `mvm vm create` works identically.

**Duration:** ~3-4 days

### Phase 2: API & CLI VMM Awareness (No New VMM Yet)

**Goal:** Users can specify `--vmm` (though only `firecracker` is available). Public API reflects VMM selection. Backward compat maintained.

**Steps:**
1. Add `--vmm` flag to `cli/vm.py` (`--vmm firecracker`, with default from config)
2. Propagate `vmm_type` through `VMCreateInput` → `ResolvedVMCreateInput`
3. Remove `--firecracker-bin` flag, replace with unified `--vmm-bin`
4. Add `OVERRIDABLE_DEFAULTS["defaults.vm"]["vmm_type"]` to constants
5. Update `models/__init__.py` exports (swap `FirecrackerConfig` for `VMMType`, `VMMConfig`)
6. Update help text across CLI to be VMM-agnostic

**Verification:** All tests pass. `mvm vm create --vmm firecracker ...` works. `mvm vm create` (no flag) defaults to firecracker. `mvm vm create --firecracker-bin ...` gets a deprecation warning.

**Duration:** ~2 days

### Phase 3: VMM-Agnostic Binary Management (No New VMM Yet)

**Goal:** The binary cache and resolution system supports multiple VMM binary types. Currently it downloads `firecracker-v1.15.1` — generalize to `{vmm}-{version}`.

**Steps:**
1. Add `vmm_type` field to `BinaryItem`
2. Update binary download/download URL pattern to be VMM-aware
3. Rename `KERNEL_TYPE_FIRECRACKER` to accommodate multi-VMM kernel types (or keep as-is — kernels are VMM-agnostic)
4. Remove Firecracker-specific GitHub URLs from `constants.py`, move into `FirecrackerDriver`

**Duration:** ~1-2 days

### Phase 4: Add Crosvm Driver (Optional — Only If Desired)

**Goal:** A working `CrosvmDriver` with core operations (create, list, stop, console).

**Steps:**
1. Implement `CrosvmDriver(spawn, stop, pause, resume, generate_config)`
2. Console relay adapts to crosvm stdout-based serial
3. Volume hotplug gracefully reports "unsupported" via capability check
4. Snapshots explicitly disabled via capability check
5. `fix_fstab()` in provisioner accepts VMM-aware block device naming

**Verification:** `mvm vm create --vmm crosvm ...` boots a VM. Console works. Stop works. Volume attach shows clear error about missing support.

**Duration:** ~8-10 days

### Phase 5: Add QEMU Driver (Optional — Only If Desired)

**Goal:** A working `QEMUDriver` with core operations. Uses direct kernel boot (no BIOS) for competitive boot speed.

**Steps:**
1. Implement QMP (QEMU Machine Protocol) client — replaces both Firecracker's HTTP API and Crosvm's subprocess commands
2. Implement `QEMUDriver(spawn, stop, pause, resume, generate_config)` using QMP
3. Configure direct kernel boot: `-kernel vmlinuz -initrd initrd -append "..."` — no BIOS
4. Enable block hotplug via QMP `device_add`
5. Console via `-serial file:path` (compatible with existing relay)
6. Capability check: most features supported

**Note:** QEMU is the most complex VMM to integrate due to its vast CLI surface and QMP protocol. However, it also provides the richest feature set (snapshots, hotplug, live migration, GPU, etc.).

**Duration:** ~10-14 days

---

## 9. Risks & Trade-offs

### 9.1 Risk: Feature Divergence

**Problem:** Different VMMs support different features. If the CLI exposes a feature that the selected VMM doesn't support, users get runtime errors.

**Mitigation:**
- The `VMMCapabilities` object is checked before any optional operation.
- CLI commands show VMM-agnostic help text. VMM-specific limitations are documented per command output (e.g., `mvm volume attach` shows "Note: crosvm VMs do not support hot-plug").
- `mvm vm capabilities <name>` command to show what the VMM supports.

### 9.2 Risk: Dual Maintenance Burden

**Problem:** Each additional VMM doubles the surface area that needs testing, updating, and debugging.

**Mitigation:**
- Drivers are separate files with a clear interface — changes to one rarely affect others.
- A driver must pass a standard conformance test suite before acceptance.
- The project can accept community VMM drivers without accepting maintenance burden (they're self-contained).
- Not all VMMs need to be perfect — a driver can declare features as unsupported.

### 9.3 Risk: Performance Regression

**Problem:** The abstraction layer adds indirection. Method calls through `IVMMDriver` go through a Python protocol dispatch.

**Mitigation:**
- The overhead is negligible — the bottleneck is subprocess spawning and I/O, not Python method dispatch.
- Hot paths (spawn, start, stop) directly call driver methods. No additional abstraction layers.
- If profiling shows overhead, `IVMMDriver` can become an ABC with `__slots__` or a `union` type.

### 9.4 Risk: Backward Compatibility

**Problem:** Users with existing VMs (all Firecracker) need a smooth migration.

**Mitigation:**
- Phase 1: No schema changes, no data loss. Existing VMs continue to work.
- Phase 2-3: DB migration adds `vmm_type` column with default `"firecracker"`. Old VMs are implicitly Firecracker.
- Old `api_socket_path` and `config_path` column names get backward-compat accessors in `VMInstanceItem`.
- `VMController` creates `FirecrackerDriver` if no `vmm_type` is set (backward compat shim).

### 9.5 Trade-off: Increased Binary Size

**Problem:** Bundling multiple VMM binaries in the compiled mvm binary increases distribution size.

**Mitigation:**
- VMM binaries are NOT bundled with mvmctl (they are downloaded to the cache on first use, just like kernels and images).
- The mvmctl binary itself stays small — only the driver code is included.
- Users install only the VMMs they need via `mvm binary pull <vmm-type>`.

### 9.6 Trade-off: Abstraction Doesn't Fit All VMMs Perfectly

**Problem:** Some VMMs have fundamentally different architectures. QEMU has QMP; Crosvm uses subprocess commands; Firecracker uses HTTP. The abstraction might leak.

**Mitigation:**
- The `IVMMDriver` protocol is designed as an **interface**, not a **base class**. Drivers are free to implement methods differently.
- The protocol methods are high-level lifecycle operations (`spawn`, `stop`, `pause`) — these map cleanly to all VMMs.
- VMM-specific behavior is isolated inside the driver, not exposed to callers.
- If a method truly doesn't fit a VMM (e.g., `create_snapshot` for Crosvm), the driver raises `FeatureNotSupported(driver.capabilities)`, and callers check capabilities first.

---

## 10. Appendix: Driver Implementation Skeleton

### 10.1 FirecrackerDriver (Extracted from Existing Code)

```python
# src/mvmctl/core/vm/_firecracker_driver.py

@VMMFactory.register(VMMType.FIRECRACKER)
class FirecrackerDriver:
    """Firecracker VMM driver — wraps existing FirecrackerSpawner + FirecrackerClient."""
    
    vmm_type = VMMType.FIRECRACKER
    
    @property
    def capabilities(self) -> VMMCapabilities:
        return VMMCapabilities(
            snapshots=True,
            block_hotplug=True,
            network_hotplug=False,
            gpu_passthrough=False,
            virtio_fs=False,
            vhost_net=False,
            gdb_debugging=False,
            tpm=False,
            pcie=False,
            console_pty="file",
            initrd_support=True,
            persistent_memory=False,
            suspend_resume=True,
            live_migration=False,
        )
    
    def __init__(self, vm: VMInstanceItem, repo: VMRepository):
        self._vm = vm
        self._repo = repo
        # Delegate to existing internal implementation
        from mvmctl.core.vm._firecracker import FirecrackerSpawner, FirecrackerClient
        self._spawner_cls = FirecrackerSpawner
        self._client_cls = FirecrackerClient
        self._spawner: FirecrackerSpawner | None = None
        self._client: FirecrackerClient | None = None
    
    def generate_config(self, config: VMMConfig) -> None:
        # Map VMMConfig → FirecrackerConfigDict
        fc_config = self._build_firecracker_config(config)
        self._spawner = self._spawner_cls(fc_config)  # Adapts FirecrackerConfig
        self._spawner.write_to_file()
    
    def spawn(self) -> int:
        if self._spawner is None:
            raise RuntimeError("generate_config() must be called before spawn()")
        return self._spawner.spawn()
    
    def pause(self) -> None:
        self._get_client().pause_vm()
    
    def attach_drive(self, drive: DriveAttachment) -> None:
        self._get_client().put_drive(
            drive_id=drive.drive_id,
            path_on_host=drive.path_on_host,
            is_root_device=drive.is_root_device,
            is_read_only=drive.is_read_only,
        )
    
    def _get_client(self) -> FirecrackerClient:
        if self._client is None:
            self._client = self._client_cls(
                self._vm.vm_dir / self._vm.control_socket_path
            )
        return self._client
```

### 10.2 CrosvmDriver (New)

```python
# src/mvmctl/core/vm/_crosvm_driver.py

@VMMFactory.register(VMMType.CROSVM)
class CrosvmDriver:
    """Crosvm VMM driver — CLI-arg based, subprocess runtime control."""
    
    vmm_type = VMMType.CROSVM
    
    @property
    def capabilities(self) -> VMMCapabilities:
        return VMMCapabilities(
            snapshots=False,
            block_hotplug=False,
            network_hotplug=True,
            gpu_passthrough=True,
            virtio_fs=True,
            vhost_net=True,
            gdb_debugging=True,
            tpm=True,
            pcie=True,
            console_pty="stdout",
            initrd_support=True,
            persistent_memory=True,
            suspend_resume=True,
            live_migration=False,
        )
    
    def __init__(self, vm: VMInstanceItem, repo: VMRepository):
        self._vm = vm
        self._repo = repo
        self._process: subprocess.Popen | None = None
    
    def generate_config(self, config: VMMConfig) -> None:
        # Crosvm has no config file — build CLI args instead
        self._cli_args = self._build_cli_args(config)
        # Write to a file for respawn purposes
        self._write_cli_args(config.vm_dir, self._cli_args)
    
    def spawn(self) -> int:
        cmd = [self._resolve_binary(), "run"] + self._cli_args
        self._process = subprocess.Popen(
            cmd,
            stdout=subprocess.PIPE if config.enable_console else subprocess.DEVNULL,
            stderr=subprocess.PIPE,
            start_new_session=True,
        )
        return self._process.pid
    
    def stop(self, force: bool = False) -> None:
        if force:
            self._process.kill()
        else:
            subprocess.run(["crosvm", "stop", str(self._control_socket)],
                         check=False)
    
    def pause(self) -> None:
        subprocess.run(["crosvm", "suspend", str(self._control_socket)],
                      check=True)
    
    def resume(self) -> None:
        subprocess.run(["crosvm", "resume", str(self._control_socket)],
                      check=True)
    
    def attach_drive(self, drive: DriveAttachment) -> None:
        raise FeatureNotSupported(
            "Crosvm does not support runtime block device hot-plug. "
            "Attach volumes at VM creation time."
        )
    
    def _build_cli_args(self, config: VMMConfig) -> list[str]:
        args = [
            "--cpus", str(config.vcpu_count),
            "--mem", str(config.mem_size_mib),
            "--block", f"{config.rootfs_path},root,id=rootfs",
            "-p", config.kernel_cmdline,
            "-s", str(self._control_socket),
        ]
        if config.tap_name:
            net_arg = f"tap-name={config.tap_name}"
            if config.guest_mac:
                net_arg += f",mac={config.guest_mac}"
            args.extend(["--net", net_arg])
        if config.initrd_path:
            args.extend(["--initrd", config.initrd_path])
        for drive in config.extra_drives:
            args.extend(["--block", f"{drive.path_on_host},id={drive.drive_id}"])
        args.append(config.kernel_path)
        return args
```

### 10.3 FeatureNotSupported Exception

```python
# src/mvmctl/exceptions.py (or core/vm/_vmm_driver.py)

class FeatureNotSupported(MVMError):
    """Raised when a VMM driver does not support the requested operation."""
    def __init__(self, message: str, capabilities: VMMCapabilities | None = None):
        self.capabilities = capabilities
        super().__init__(message)
```

---

## Summary of New Files

| Layer | New Files | Lines (Est.) |
|-------|-----------|-------------|
| Models | `models/vmm.py` | ~100 |
| Core/VM | `core/vm/_vmm_driver.py` (protocol) | ~150 |
| Core/VM | `core/vm/_vmm_factory.py` (factory) | ~50 |
| Core/VM | `core/vm/_firecracker_driver.py` (extracted) | ~800 |
| Core/VM | `core/vm/_crosvm_driver.py` (new) | ~500 |
| Core/VM | `core/vm/_qemu_driver.py` (future) | ~700 |

## Summary of Modified Files

| Layer | Modified Files | Nature of Change |
|-------|--------------|------------------|
| Models | `models/vm.py`, `models/__init__.py` | Add `vmm_type` field, rename columns |
| Core/VM | `core/vm/_controller.py`, `core/vm/__init__.py` | Accept `IVMMDriver`, delegate to driver |
| Core/VM | `core/vm/_firecracker.py` | Trim to thin import shim or remove |
| Core/VM | `core/vm/_provisioner.py` | VMM-aware block device naming |
| API | `api/vm_operations.py`, `api/inputs/*` | VMM-agnostic config building, factory usage |
| CLI | `cli/vm.py` | `--vmm`/`--vmm-bin` flags, generic help text |
| Constants | `constants.py` | Remove Firecracker-specific URLs/filenames |
| DB | `db/migrations/NNN_vmm_agnostic.sql` | New migration |
| Services | `services/console/` | Console source abstraction |
| Public API | `api/__init__.py`, `models/__init__.py` | Replace `FirecrackerConfig` exports with VMM-agnostic types |

---

## Appendix: Phased Timeline

| Phase | Description | Duration | Dependencies |
|-------|-------------|----------|--------------|
| **P1** | Extract protocol, adapter, Factory, Controller + API refactor | 3-4 days | None |
| **P2** | CLI VMM flags, backward compat, constants cleanup, DB migration | 2 days | P1 |
| **P3** | VMM-agnostic binary management, remove FC-specific URLs | 1-2 days | P1-P2 |
| **P4** | Crosvm driver | 8-10 days | P1-P3 |
| **P5** | QEMU driver | 10-14 days | P1-P3 |
| **Testing** | Driver conformance tests, system tests per VMM | 3-5 days | P1-P5 |

**Minimum viable VMM-agnostic (P1-P3):** ~6-8 days. At this point the abstraction is proven, backward compat is clean, and a new VMM driver can be added as a self-contained PR.
