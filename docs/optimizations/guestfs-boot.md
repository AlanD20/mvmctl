# libguestfs Boot Time Optimizations

> **STATUS: Current — all optimizations implemented.** The Go codebase uses the `guestfish` CLI subprocess (no native Go guestfs bindings). All 10 optimization items are ✅ IMPLEMENTED.
>
> Implementation location: `internal/lib/provisioner/guestfs/`
>
> **Note:** The guestfs provisioning path is the **fallback backend** in mvmctl. The primary provisioning backend is the loop-mount subprocess (`mvm provision` via `LoopMountBackend`). Guestfs is used only when the loop-mount binary is unavailable or `guestfs_enabled` is set to `true` in config (`internal/infra/constants.go:122`).

## Overview

This document describes the boot-time optimizations for the fallback libguestfs provisioning path in mvmctl. These optimizations reduce appliance startup time by configuring the backend directly, minimizing resource allocation, and disabling unnecessary services.

The Go implementation uses `guestfish` CLI as a subprocess (Go has no native guestfs bindings). Environment variables control most backend settings; the remaining handle-level settings are passed as stdin commands to `guestfish`.

## Applied Optimizations ✅ IMPLEMENTED

### 1. Direct Backend (Environment Variable) ✅ IMPLEMENTED

The libguestfs appliance uses the `direct` backend (QEMU/KVM directly) instead of libvirt. This eliminates libvirt IPC overhead and dependency resolution delays.

**Implementation:** `internal/lib/provisioner/guestfs/base.go:81`
```go
os.Setenv("LIBGUESTFS_BACKEND", "direct")
```

### 2. Appliance Cache in RAM (Environment Variable) ✅ IMPLEMENTED

The `LIBGUESTFS_CACHEDIR` environment variable is set to `/dev/shm` when available, reducing appliance load time by using tmpfs.

**Implementation:** `internal/lib/provisioner/guestfs/base.go:83-84`
```go
if _, err := os.Stat("/dev/shm"); err == nil {
    os.Setenv("LIBGUESTFS_CACHEDIR", "/dev/shm")
}
```

### 3. Minimal Memory (Environment Variable) ✅ IMPLEMENTED

Memory allocation is reduced to 256MB, significantly faster than the default 500MB+ allocation. This is set via `LIBGUESTFS_MEMSIZE` env var instead of the `set_memsize()` API (which doesn't exist in guestfish 1.56.x as a CLI flag).

**Implementation:** `internal/lib/provisioner/guestfs/base.go:82`
```go
os.Setenv("LIBGUESTFS_MEMSIZE", "256")
```

### 4. Disable QEMU File Locking (Environment Variable) ✅ IMPLEMENTED

QEMU's default file locking (`fcntl` locks) can cause `guestfs_launch` failures when a previous guestfs session crashes and leaves a stale lock on the image file. This is common with the ready pool, where multiple VM creations may reference the same source image.

Setting `QEMU_LOCKING=off` disables this locking mechanism. This is safe in mvmctl because:
- Each VM works on its own **copy** of the image, never the shared source.
- The ready pool image is effectively read-only after creation.
- No concurrent writers or shared storage scenarios exist.

**Implementation:** `internal/lib/provisioner/guestfs/base.go:86`
```go
os.Setenv("QEMU_LOCKING", "off")
```

### 5. Kernel Detection for Appliance (Environment Variable) ✅ IMPLEMENTED

`initEnv()` (`internal/lib/provisioner/guestfs/base.go:88-93`) uses `KernelDetector.FindBestKernel()` to select the optimal host kernel for the libguestfs appliance. The detector scans `/boot` for kernel images, uses the `file` command to extract version strings, and scores candidates based on virtio module availability and whether the kernel looks like a custom build. Kernels with more virtio drivers (critical for guestfs) are ranked higher. The selected kernel is passed to the appliance via `SUPERMIN_KERNEL` and `SUPERMIN_MODULES` environment variables.

**Code reference:** `internal/lib/provisioner/guestfs/kernel_detector.go` — `KernelDetector.FindBestKernel()`.

### 6. Minimal vCPUs (stdin Command) ✅ IMPLEMENTED

The appliance runs with a single vCPU, reducing hardware initialization time. This is implemented by prepending `set-smp 1` to the guestfish stdin script (guestfish 1.56.x does not support `--smp` as a CLI flag).

**Implementation:** `internal/lib/provisioner/guestfs/base.go:147`
```go
input = "set-smp 1\nset-recovery-proc false\nrun\n" + ...
```

### 7. Disable Recovery Process (stdin Command) ✅ IMPLEMENTED

By default, libguestfs forks a "recovery process" that monitors the appliance and kills the QEMU instance if the main process crashes. Disabling this saves a `fork()` and `exec()` call during `g.launch()`. Implemented via stdin command `set-recovery-proc false`.

**Implementation:** `internal/lib/provisioner/guestfs/base.go:147` (same line as vCPU)

### 8. **--no-sync** (CLI Flag) ✅ IMPLEMENTED

The `guestfishRun()` function always passes `--no-sync` to the guestfish CLI. This disables the default `sync()` call on handle close (equivalent to `set_autosync(false)` in the Python bindings). Since we explicitly call `sync` in the script at the end of provisioning, autosync is redundant.

**Implementation:** `internal/lib/provisioner/guestfs/base.go:114`
```go
allArgs = append(allArgs, "--no-sync")
```

### 9. Fixed Appliance (Build + Cache) ✅ IMPLEMENTED

`BuildAppliance()` at `internal/lib/provisioner/guestfs/utils.go:47-137` builds the libguestfs fixed appliance during `mvm cache init`. It runs `libguestfs-make-fixed-appliance` if available on the system. If the tool is not installed, it silently skips the build (returns `nil`).

Before building, it calls `CleanStaleState()` (`utils.go:140-198`) to kill abandoned QEMU/guestfish processes, remove stale lock files, daemon sockets, and cached appliance directories that could cause the appliance build to hang.

**Code reference:** `internal/lib/provisioner/guestfs/utils.go` — `BuildAppliance()`, `CleanStaleState()`, `PruneAppliance()`, `EnsureAppliance()`.

### 10. Retry Logic (3 attempts, backoff) ✅ IMPLEMENTED

The `guestfishRun()` function wraps the guestfish invocation in a retry loop with up to 3 attempts and 500ms × (attempt+1) backoff between retries. This handles transient QEMU launch failures (resource contention, kernel detection race) without aborting the entire provisioning operation.

**Implementation:** `internal/lib/provisioner/guestfs/base.go:154-161`
```go
for attempt := range 3 {
    if attempt > 0 {
        select {
        case <-ctx.Done():
            return "", ctx.Err()
        case <-time.After(time.Duration(500*(attempt+1)) * time.Millisecond):
        }
    }
    // ... run guestfish ...
}
```

## Differences from Legacy Python Implementation

The Go guestfs implementation differs from the legacy Python version in key ways:

| Aspect | Python (legacy) | Go (current) |
|---|---|---|
| **Bindings** | Native Python `guestfs` module (`import guestfs`) | `guestfish` CLI subprocess |
| **Handle lifecycle** | `with OptimizedGuestfs() as g:` context manager | `GuestfsHandle` struct with explicit methods |
| **Backend config** | `os.environ["LIBGUESTFS_BACKEND"]="direct"` | Same env var in `initEnv()` |
| **Memory** | `g.set_memsize(256)` | `LIBGUESTFS_MEMSIZE=256` env var |
| **SMP** | `g.set_smp(1)` | `set-smp 1` stdin command |
| **Recovery proc** | `g.set_recovery_proc(False)` | `set-recovery-proc false` stdin command |
| **Autosync** | `g.set_autosync(False)` | `--no-sync` CLI flag |
| **Network** | `g.set_network(False)` | Not needed — implied by env + no-net default |
| **Add drive** | `g.add_drive_opts(..., format="raw", cachemode="writeback")` | `-a` flag + `--format` (cachemode not supported in guestfish 1.56.x) |
| **Kernel detection** | `KernelDetector.find_best_kernel()` | Same, ported identically |
| **Fixed appliance** | `build_appliance()` | `BuildAppliance()` — same logic |
| **Retry** | 3 attempts, 0.5s backoff in `__enter__()` | 3 attempts, variable backoff in `guestfishRun()` |

## Provisioner: Builder Pattern

The `GuestfsBackend` (`backend.go`) uses the same builder pattern as the loopmount backend:

1. Call builder methods: `Resize()`, `SetHostname()`, `InjectDNS()`, `SetupSSH()`, `InjectCloudInit()`, `DisableCloudInit()`, `Shrink()`, `Deblob()`, `FixFstab()`
2. Call `Run()` to execute all queued operations in a single guestfish script (`provisioner.go:99-129`)

The `RunDeferred()` function (`provisioner.go:52-135`) executes provisioning in two guestfish sessions:
1. **Pre-read session**: Detect root device via `list-filesystems`
2. **Main session**: Mount, grow/shrink, inject files, run shell commands via `sh "..."`, upload files

## Performance Comparison

| Optimization Tier | Launch Time (typical) | Total Injection Time |
|---|---|---|
| None (Default) | 8.0s - 15.0s | 10.0s - 20.0s |
| Basic (1-5) ✅ | 3.0s - 5.0s | 4.0s - 6.0s |
| Aggressive (1-8) ✅ | 1.0s - 3.0s | 2.0s - 4.0s |
| Ultimate (Fixed App) ✅ | < 1.0s | < 2.0s |

## Implementation Status Summary

| # | Optimization | Status | Code Location |
|---|---|---|---|
| 1 | Direct backend (`LIBGUESTFS_BACKEND=direct`) | ✅ | `base.go:81` |
| 2 | Appliance cache in RAM (`/dev/shm`) | ✅ | `base.go:83-84` |
| 3 | Minimal memory (`LIBGUESTFS_MEMSIZE=256`) | ✅ | `base.go:82` |
| 4 | QEMU lock disable (`QEMU_LOCKING=off`) | ✅ | `base.go:86` |
| 5 | Kernel detection (`KernelDetector.FindBestKernel()`) | ✅ | `kernel_detector.go` |
| 6 | Minimal vCPUs (`set-smp 1` stdin) | ✅ | `base.go:147` |
| 7 | Disable recovery process (`set-recovery-proc false` stdin) | ✅ | `base.go:147` |
| 8 | Disable autosync (`--no-sync` CLI flag) | ✅ | `base.go:114` |
| 9 | Fixed appliance (`BuildAppliance()`) | ✅ | `utils.go:47-137` |
| 10 | Retry logic (3 attempts, backoff) | ✅ | `base.go:154-161` |

## Related Files

- `internal/lib/provisioner/guestfs/base.go` — `initEnv()`, `guestfishRun()`, `GuestfsHandle`
- `internal/lib/provisioner/guestfs/backend.go` — `GuestfsBackend` builder
- `internal/lib/provisioner/guestfs/provisioner.go` — `RunDeferred()`, `buildScript()`
- `internal/lib/provisioner/guestfs/scripts.go` — Shell script templates (hostname, DNS, SSH, cloud-init, deblob)
- `internal/lib/provisioner/guestfs/utils.go` — `BuildAppliance()`, `CleanStaleState()`, `PruneAppliance()`
- `internal/lib/provisioner/guestfs/kernel_detector.go` — `KernelDetector`
- `internal/lib/provisioner/backend.go` — Backend factory (guestfs vs loopmount selection)
- `internal/infra/constants.go` — `guestfs_enabled` default (line 122)
- `docs/adr/0003-loopmount-guestfs-mutual-exclusion.md` — ADR for backend selection
