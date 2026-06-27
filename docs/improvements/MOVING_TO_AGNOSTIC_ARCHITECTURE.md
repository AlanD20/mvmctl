# Moving to a VMM-Agnostic Architecture

> **STATUS: Design Proposal — not implemented.** All 15 tracked components remain unstarted. Codebase is fully Firecracker-coupled. No `VMMDriver`, `VMMFactory`, or `vmm_type` field exists anywhere in the Go codebase. `FirecrackerGithubReleasesAPIURL` and `FirecrackerGithubDownloadURL` still hardcoded in `internal/infra/constants.go`. Controller directly imports `internal/lib/firecracker` (was `internal/core/vm/firecracker_client.go`, moved to `internal/lib/firecracker/client.go`). No `--vmm` CLI flags exist.
>
> **Last verified:** 2026-06-27

## Implementation Status Summary

| Phase / Component | Status | Current Reality |
|---|---|---|
| **IVMMDriver interface** | ❌ Not implemented | No driver interface exists. Controller directly imports `FirecrackerClient`. |
| **VMMFactory** | ❌ Not implemented | No factory exists. Firecracker is hardcoded everywhere. |
| **FirecrackerDriver** | ❌ Not implemented | `FirecrackerClient` and `firecracker.go` remain inline. |
| **CrosvmDriver** | ❌ Not started | Not a single file exists. |
| **VMMConfig** | ❌ Not implemented | `FirecrackerConfig` is still the only config model. |
| **vmm_type on VM model** | ❌ Not implemented | No `vmm_type` field. Columns named `api_socket_path`, `config_path`. |
| **DB Migration (vmm_type)** | ❌ Not implemented | Schema has no `vmm_type` or renamed columns. |
| **Controller accepts IVMMDriver** | ❌ Not implemented | `VMController` creates `FirecrackerClient` directly. |
| **--vmm CLI flag** | ❌ Not implemented | No `--vmm` or `--vmm-bin` flags. |
| **Constants cleanup** | ❌ Not implemented | `FirecrackerGithubReleasesAPIURL`, `FirecrackerGithubDownloadURL`, `DefaultFirecrackerCIVersion` still in `internal/infra/constants.go`. |
| **Binary domain VMM-agnostic** | ❌ Not implemented | Binary domain is still Firecracker-specific. |
| **Console source abstraction** | ❌ Not implemented | Console relay reads Firecracker's serial output file only. |
| **`FeatureNotSupported` error** | ❌ Not implemented | No such error code in `pkg/errs/`. |

**Conclusion:** This document remains a design proposal. All 15 tracked components are unstarted.

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
- The project is future-proof for new VMM types.
- 80%+ of the codebase (network, images, kernels, SSH keys, cloud-init, provisioning, DB, resolvers) is VMM-agnostic and unchanged.

---

## 2. Current State: Firecracker Coupling Map

### 2.1 Tightly Coupled (Must Be Abstracted) — ALL STILL PRESENT ❌

| File / Location | Coupling | What Needs to Change |
|---|---|---|
| `internal/lib/firecracker/client.go` (moved from `internal/core/vm/firecracker_client.go`) | `FirecrackerClient` — HTTP-over-Unix-socket client for runtime control | Extract into `FirecrackerDriver`. |
| `internal/core/vm/firecracker.go` | `FirecrackerSpawner` — writes JSON config, launches with `--config-file` | Extract into `FirecrackerDriver`. |
| `internal/lib/model/firecracker.go` | `FirecrackerConfig` struct — Firecracker-specific fields | Becomes internal to `FirecrackerDriver`. |
| `internal/core/vm/controller.go` | Direct `FirecrackerClient` imports for pause/resume/start/snapshot | Controller accepts `VMMDriver` instead. |
| `pkg/api/vm.go` | `buildFirecrackerConfig()`, `FirecrackerSpawner` usage | Replace with `buildVMMConfig()`, factory-based driver creation. |
| `internal/infra/constants.go` | `OverridableDefaults["defaults.firecracker"]` (8 filenames + log_level) | Move to `FirecrackerDriver`. |
| `internal/infra/constants.go` | `FirecrackerGithubReleasesAPIURL`, `FirecrackerGithubDownloadURL` | Move into `FirecrackerDriver`. |
| `internal/cli/vm.go` | No `--vmm` or `--vmm-bin` flags | Add `--vmm` + `--vmm-bin` flags. |

### 2.2 Moderately Coupled (Pattern Differences)

| File / Location | Issue | Resolution |
|---|---|---|
| `internal/lib/provisioner/` | `fix_fstab()` hardcodes `/dev/vda` (Firecracker VirtIO naming) | Add VMM-aware `block_device_map` parameter. |
| `internal/lib/model/vm.go` | `api_socket_path`, `config_path` fields are Firecracker-specific | Rename to `control_socket_path`, `vmm_config_path`. |
| `internal/service/console/` | Reads Firecracker's serial output file | Console service needs VMM-aware input source. |

### 2.3 Loosely Coupled / Untouched

These layers are VMM-agnostic and survive intact:

| Layer | Why It Survives |
|---|---|
| `internal/core/vm/repository.go` | Pure DB operations — stores paths as opaque strings |
| `internal/core/vm/resolver.go` | Resolution logic — no VMM knowledge |
| `internal/lib/provisioner/` (except `fix_fstab`) | Rootfs provisioning — VMM-agnostic |
| `internal/core/vm/service.go` | Stateless VM operations — delegates to Controller, no VMM dependency |
| `internal/core/network/*` | Network domain — VMM-agnostic |
| `internal/core/image/*` | Image domain — VMM-agnostic |
| `internal/core/kernel/*` | Kernel domain — VMM-agnostic |
| `internal/core/binary/*` | Binary domain — needs VMM type field added |
| `internal/core/key/*` | SSH key management — VMM-agnostic |
| `internal/core/cloudinit/*` | Cloud-init — VMM-agnostic |
| `internal/core/volume/*` | Volume management — attach/detach abstracted through driver |
| `pkg/api/inputs/*` | Input Validate/Resolve pattern (ADR-0011) — VMM-agnostic |
| `internal/cli/*` (except `vm.go` VMM selection) | Most CLI commands are VMM-agnostic |
| All models except `firecracker.go` | `VM`, `Network`, etc. — VMM-agnostic |

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
│  VMOperation.Create(inputs)                                   │
│    ├─ resolves defaults (vcpus, mem, image, kernel, ...)      │
│    ├─ network setup (bridge, TAP, lease)                      │
│    ├─ rootfs provisioning (resize, cloud-init, SSH)           │
│    ├─ VMMFactory.CreateDriver(vmmType, vm, repo) ────────┐   │
│    │   returns VMMDriver (FirecrackerDriver | CrosvmDriver)  │
│    ├─ driver.GenerateConfig(...)                             │
│    ├─ driver.Spawn()                                          │
│    └─ driver.StartVM()                                        │
└──────────────────────┬───────────────────────────────────────┘
                       │ VMMDriver interface
                       ▼
┌──────────────────────────────────────────────────────────────┐
│                   Core / VM Domain                            │
│                                                               │
│  ┌─────────────────────────────────────────────────────┐      │
│  │           VMMDriver (interface)                     │      │
│  │  ┌─────────────────┐  ┌──────────────────────┐     │      │
│  │  │ FirecrackerDriver│  │   CrosvmDriver       │     │      │
│  │  │                 │  │                      │     │      │
│  │  │ - Spawn()       │  │ - Spawn()            │     │      │
│  │  │ - Stop()        │  │ - Stop()             │     │      │
│  │  │ - Pause()       │  │ - Pause()            │     │      │
│  │  │ - Resume()      │  │ - Resume()           │     │      │
│  │  │ - AttachDrive() │  │ - AttachDrive()      │     │      │
│  │  │ - DetachDrive() │  │ - ResizeDisk()       │     │      │
│  │  │ - Snapshot()    │  │ - (unsupported)      │     │      │
│  │  │ - ConsoleFD()   │  │ - ConsoleFD()        │     │      │
│  │  └─────────────────┘  └──────────────────────┘     │      │
│  └─────────────────────────────────────────────────────┘      │
│                                                               │
│  VMController(entity, repo, driver VMMDriver)                 │
│    - delegates start/stop/pause/resume to driver              │
│    - state machine unchanged                                  │
│                                                               │
│  VMMFactory.CreateDriver(vmmType, vm, repo) → VMMDriver       │
│    - registry of VMM types to driver implementations          │
│    - auto-detection from binary or config                     │
└──────────────────────────────────────────────────────────────┘
```

### Key Architectural Principles

1. **The driver owns VMM-specific knowledge.** Config format, CLI arguments, API protocol, device naming, feature availability — all inside the driver.
2. **VMController stays as the state machine.** It accepts a `VMMDriver` and delegates lifecycle operations. State validation (RUNNING/PAUSED/STOPPED transitions) remains in Controller.
3. **The factory is the only place with VMM switching logic.** `VMMFactory.CreateDriver()` maps VMM type to driver implementation. The rest of the codebase never switches on VMM type.
4. **VMM-agnostic operations stay in API layer.** Network setup, rootfs provisioning, cloud-init, SSH keys — these do NOT belong in drivers.
5. **Feature gaps are explicit.** Each driver declares its capabilities via a `Capabilities` property. The API layer checks capabilities before calling optional features.

---

## 4. The VMMDriver Interface

### 4.1 Core Interface

```go
// internal/core/vm/driver.go

type VMMType string

const (
    VMMFirecracker VMMType = "firecracker"
    VMMCrosvm      VMMType = "crosvm"
    VMMQEMU        VMMType = "qemu"
)

type VMMCapabilities struct {
    Snapshots       bool
    BlockHotplug    bool
    NetworkHotplug  bool
    GPUPassthrough  bool
    VirtioFS        bool
    VhostNet        bool
    GDBDebugging    bool
    TPM             bool
    PCIe            bool
    ConsolePTY      string // "file", "stdout", "tcp", "socket"
    InitrdSupport   bool
    PersistentMemory bool
    SuspendResume   bool
    LiveMigration   bool
}

type VMMDriver interface {
    // Metadata
    Type() VMMType
    Capabilities() VMMCapabilities

    // Lifecycle
    GenerateConfig(config VMMConfig) error
    Spawn() (int, error)
    StartVM() error
    IsRunning() bool
    WaitForBoot(timeout time.Duration) error

    // Runtime Control
    Stop(force bool) error
    Pause() error
    Resume() error
    Reboot() error

    // Storage
    AttachDrive(drive DriveAttachment) error
    DetachDrive(driveID string) error
    ResizeDisk(driveID string, newSizeMiB int) error

    // Snapshots
    CreateSnapshot(memPath, statePath string) error
    LoadSnapshot(memPath, statePath string) error

    // Console
    ConsoleSource() ConsoleSource

    // Cleanup
    Close() error
}
```

### 4.2 Supporting Types

```go
type VMMConfig struct {
    VMMType     VMMType
    VMDir       string
    VMID        string

    // Machine
    VCPUCount   int
    MemSizeMiB  int
    EnablePCI   bool

    // Boot
    KernelPath  string
    KernelCmdline string
    InitrdPath  string

    // Storage
    RootfsPath    string
    RootfsReadOnly bool
    ExtraDrives   []DriveAttachment

    // Network
    TapName  string
    GuestMAC string

    // Console / Logging
    EnableConsole bool
    EnableLogging bool
    LogLevel      string

    // VMM-specific overrides (passed through, not interpreted)
    VMMOptions map[string]any
}

type DriveAttachment struct {
    DriveID      string
    PathOnHost   string
    IsRootDevice bool
    IsReadOnly   bool
}

type ConsoleSource struct {
    Kind string // "file", "stdout", "tcp", "socket"
    Path string
    Host string
    Port int
    FD   int
}
```

### 4.3 VMM Factory

```go
// internal/core/vm/factory.go

type VMMFactory struct {
    registry map[VMMType]func(vm *model.VM, repo vm.Repository) VMMDriver
}

func (f *VMMFactory) Register(vmmType VMMType, constructor func(*model.VM, vm.Repository) VMMDriver) {
    f.registry[vmmType] = constructor
}

func (f *VMMFactory) CreateDriver(vmmType VMMType, vm *model.VM, repo vm.Repository) (VMMDriver, error) {
    constructor, ok := f.registry[vmmType]
    if !ok {
        return nil, fmt.Errorf("unknown VMM type: %s", vmmType)
    }
    return constructor(vm, repo), nil
}
```

---

## 5. VMM Feature Capability Matrix

| Feature | Firecracker | Crosvm | QEMU |
|---|---|---|---|
| **Block storage** | raw, qcow2 | raw, qcow2, qcow3, zstd | raw, qcow2, vmdk, vdi, +more |
| **Block hotplug** | ✅ `PUT /drives` | ❌ (only resize) | ✅ `device_add` via QMP |
| **Network** | TAP | TAP, vhost-net, slirp | TAP, vhost-net, slirp, bridge |
| **Network hotplug** | ❌ | ⚠️ Experimental | ✅ QMP `device_add` |
| **Snapshots** | ✅ Production, stable format | ❌ Experimental, unstable | ✅ `savevm`/`loadvm`, qcow2 internal |
| **GPU / Display** | ❌ | ✅ virtio-gpu, virgl, Vulkan | ✅ virtio-gpu, virgl, VGA, SPICE |
| **virtio-fs** | ❌ | ✅ | ✅ (via vhost-user-fs) |
| **vhost-net** | ❌ (software virtio) | ✅ | ✅ |
| **TPM** | ❌ | ✅ | ✅ (swtpm) |
| **GDB debugging** | ❌ | ✅ (`--gdb`) | ✅ (`-s`, `-gdb`) |
| **Console output** | File-based | stdout | tcp/file/stdout |
| **Suspend/Resume** | ✅ Pause/resume | ✅ suspend/resume | ✅ `stop`/`cont` via QMP |
| **PCIe** | ❌ (limited PCI) | ✅ | ✅ |
| **Boot speed** | Very fast | Fast | Slower (BIOS/EFI) |
| **Memory overhead** | ~5MB per VM | ~15-30MB per VM | ~30-100MB per VM |
| **Process isolation** | Single process (+optional jailer) | Process-per-device (minijail) | Single process (+optional) |
| **Architectures** | x86_64, aarch64 | x86_64, aarch64, riscv64 | x86_64, aarch64, riscv64, arm, mips, ppc, s390x |

---

## 6. Concrete VMM Candidates

### 6.1 Crosvm (Google) — Not Implemented

### 6.2 QEMU (Full System Virtualization) — Not Implemented

---

## 7. Migration Strategy (Phased) — ALL PHASES PENDING

### Phase 1: Extract the Interface (No Functional Change) ❌ NOT STARTED

**Goal:** Define `VMMDriver`, `VMMConfig`, `VMMCapabilities`. `FirecrackerSpawner` and `FirecrackerClient` remain but implement the interface. Controller stops calling Firecracker directly.

### Phase 2: API & CLI VMM Awareness (No New VMM Yet) ❌ NOT STARTED

### Phase 3: VMM-Agnostic Binary Management (No New VMM Yet) ❌ NOT STARTED

### Phase 4: Add Crosvm Driver (Optional) ❌ NOT STARTED

### Phase 5: Add QEMU Driver (Optional) ❌ NOT STARTED

---

## 8. Risks & Trade-offs

### 8.1 Risk: Feature Divergence

### 8.2 Risk: Dual Maintenance Burden

### 8.3 Risk: Performance Regression

### 8.4 Risk: Backward Compatibility

### 8.5 Trade-off: Increased Binary Size

### 8.6 Trade-off: Abstraction Doesn't Fit All VMMs Perfectly

---

## Summary of New Files

| Layer | New Files | Lines (Est.) | Status |
|---|---|---|---|
| Models | `internal/lib/model/vmm.go` | ~100 | ❌ |
| Core/VM | `internal/core/vm/driver.go` (interface) | ~150 | ❌ |
| Core/VM | `internal/core/vm/factory.go` (factory) | ~50 | ❌ |
| Core/VM | `internal/core/vm/firecracker_driver.go` (extracted) | ~800 | ❌ |
| Core/VM | `internal/core/vm/crosvm_driver.go` (new) | ~500 | ❌ |
| Core/VM | `internal/core/vm/qemu_driver.go` (future) | ~700 | ❌ |

## Summary of Modified Files

| Layer | Modified Files | Nature of Change | Status |
|---|---|---|---|
| Models | `internal/lib/model/vm.go` | Add `vmm_type` field, rename columns | ❌ |
| Core/VM | `internal/core/vm/controller.go` | Accept `VMMDriver`, delegate to driver | ❌ |
| Core/VM | `internal/core/vm/firecracker.go` | Trim to thin driver implementation | ❌ |
| Core/VM | `internal/lib/provisioner/` | VMM-aware block device naming | ❌ |
| API | `pkg/api/vm.go`, `pkg/api/inputs/*` | VMM-agnostic config building, factory usage | ❌ |
| CLI | `internal/cli/vm.go` | `--vmm`/`--vmm-bin` flags, generic help text | ❌ |
| Constants | `internal/infra/constants.go` | Remove Firecracker-specific URLs/filenames | ❌ |
| DB | `db/migrations/NNN_vmm_agnostic.sql` | New migration | ❌ |
| Services | `internal/service/console/` | Console source abstraction | ❌ |

---

## Appendix: Phased Timeline

| Phase | Description | Duration | Dependencies | Status |
|---|---|---|---|---|
| **P1** | Extract interface, adapter, Factory, Controller + API refactor | 3-4 days | None | ❌ |
| **P2** | CLI VMM flags, backward compat, constants cleanup, DB migration | 2 days | P1 | ❌ |
| **P3** | VMM-agnostic binary management, remove FC-specific URLs | 1-2 days | P1-P2 | ❌ |
| **P4** | Crosvm driver | 8-10 days | P1-P3 | ❌ |
| **P5** | QEMU driver | 10-14 days | P1-P3 | ❌ |
| **Testing** | Driver conformance tests, system tests per VMM | 3-5 days | P1-P5 | ❌ |

**Minimum viable VMM-agnostic (P1-P3):** ~6-8 days. At this point the abstraction is proven, backward compat is clean, and a new VMM driver can be added as a self-contained PR.
