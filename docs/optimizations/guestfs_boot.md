# libguestfs Boot Time Optimizations

> **STATUS: Partially outdated** — Items #8 and #9 marked ⏳ PENDING in the original doc are now implemented.
>
> Implementation location: `src/mvmctl/core/_shared/_guestfs/_base.py`
>
> **Note:** The guestfs provisioning path is the **fallback backend** in mvmctl. The primary provisioning backend is the loop-mount binary (`mvm-provision` via `_LoopMountBackend`). Guestfs is used only when the loop-mount binary is unavailable or `guestfs_enabled` is set to `true` in config.

## Overview

This document describes the boot-time optimizations for the fallback libguestfs provisioning path in mvmctl. These optimizations reduce appliance startup time by configuring the backend directly, minimizing resource allocation, and disabling unnecessary services. All optimizations documented here are compatible with and highly beneficial for both **ext4** and **btrfs** root filesystems.

## Applied Optimizations ✅ IMPLEMENTED

These optimizations are applied via the `GuestfsHandler` class in `src/mvmctl/core/_shared/_guestfs/_base.py`:

### 1. Direct Backend (Environment Variable) ✅ IMPLEMENTED

The libguestfs appliance uses the `direct` backend (QEMU/KVM directly) instead of libvirt. This eliminates libvirt IPC overhead and dependency resolution delays.

**Implementation:**
```python
os.environ["LIBGUESTFS_BACKEND"] = "direct"
```

**Code reference:** `src/mvmctl/core/_shared/_guestfs/_base.py` line 46

### 2. Networking Disabled (Handle Method) ✅ IMPLEMENTED

Network interface initialization is skipped, eliminating DHCP client startup and link state waits.

**Implementation:**
```python
if hasattr(g, "set_network"):
    g.set_network(False)
```

**Code reference:** `src/mvmctl/core/_shared/_guestfs/_base.py` line 79-80

### 3. Minimal vCPUs (Handle Method) ✅ IMPLEMENTED

The appliance runs with a single vCPU, reducing hardware initialization time.

**Implementation:**
```python
if hasattr(g, "set_smp"):
    g.set_smp(1)
```

**Code reference:** `src/mvmctl/core/_shared/_guestfs/_base.py` line 81-82

### 4. Minimal Memory (Handle Method) ✅ IMPLEMENTED

Memory allocation is reduced to 256MB, significantly faster than the default 500MB+ allocation.

**Implementation:**
```python
if hasattr(g, "set_memsize"):
    g.set_memsize(256)
```

**Code reference:** `src/mvmctl/core/_shared/_guestfs/_base.py` line 83-84

## Aggressive Optimizations ✅ IMPLEMENTED

For cases where every millisecond counts, the following aggressive optimizations can be applied:

### 5. Disable Recovery Process (Handle Method) ✅ IMPLEMENTED

By default, libguestfs forks a "recovery process" that monitors the appliance and kills the QEMU instance if the main process crashes. Disabling this saves a `fork()` and `exec()` call during `g.launch()`.

**Implementation:**
```python
if hasattr(g, "set_recovery_proc"):
    g.set_recovery_proc(False)
```

**Code reference:** `src/mvmctl/core/_shared/_guestfs/_base.py` line 75-76

### 6. Disable Autosync (Handle Method) ✅ IMPLEMENTED

By default, libguestfs calls `sync` automatically when you close the handle or shut down. Since we call `g.umount("/")` after writing, which already flushes buffers, `autosync` is redundant and can be disabled to speed up `g.shutdown()`.

**Implementation:**
```python
if hasattr(g, "set_autosync"):
    g.set_autosync(False)
```

**Code reference:** `src/mvmctl/core/_shared/_guestfs/_base.py` line 77-78

### 7. Explicit Disk Format & Cache Mode (Add Drive) ✅ IMPLEMENTED

Specifying the disk format (e.g., `raw` for `.ext4` or `.btrfs` raw images) avoids QEMU's format probing overhead.

Using **`cachemode="writeback"`** (note: the original doc proposed `cachemode="unsafe"`, but the actual implementation uses `cachemode="writeback"` — a slightly more conservative setting that still provides significant performance benefits while being marginally safer for data integrity).

*   **ext4**: Benefits from reduced metadata flush latency.
*   **btrfs**: Benefits **massively**. Btrfs's Copy-on-Write (CoW) metadata tree updates are extremely synchronous and cause frequent host-side flushes. `writeback` mode hides this overhead by telling QEMU to ignore most sync requests from the appliance guest.

**Implementation:**
```python
# Format is usually "raw" for the images used in mvmctl
g.add_drive(rootfs_path, readonly=False, format="raw", cachemode="writeback")
```

**Code reference:** `src/mvmctl/core/_shared/_guestfs/_base.py` line 92

### 8. Appliance Cache in RAM (Environment Variable) ✅ IMPLEMENTED

The `LIBGUESTFS_CACHEDIR` environment variable is set to `/dev/shm` when available, reducing appliance load time by using tmpfs.

**Implementation:**
```python
if Path("/dev/shm").exists():
    os.environ["LIBGUESTFS_CACHEDIR"] = "/dev/shm"
```

**Code reference:** `src/mvmctl/core/_shared/_guestfs/_base.py` lines 47-48

### 9. Disable QEMU File Locking (Environment Variable) ✅ IMPLEMENTED

QEMU's default file locking (`fcntl` locks) can cause `guestfs_launch` failures when a previous guestfs session crashes and leaves a stale lock on the image file. This is common with the ready pool, where multiple VM creations may reference the same source image.

Setting `QEMU_LOCKING=off` disables this locking mechanism. This is safe in mvmctl because:
- Each VM works on its own **copy** of the image, never the shared source
- The ready pool image is effectively read-only after creation
- No concurrent writers or shared storage scenarios exist

**Implementation:**
```python
os.environ["QEMU_LOCKING"] = "off"
```

**Code reference:** `src/mvmctl/core/_shared/_guestfs/_base.py` line 51

### 10. Fixed Appliance (Automatic) ✅ IMPLEMENTED

`GuestfsService.build_appliance()` at `core/_shared/_guestfs/_service.py:25` attempts to build a fixed appliance during `mvm cache init` (called from `CacheOperation.initialize()` at `api/cache_operations.py:127`). It runs `libguestfs-make-fixed-appliance` if available on the system. If the tool is not installed, it silently skips the build (returns `None`).

**Code reference:** `src/mvmctl/core/_shared/_guestfs/_service.py` line 25

## Recommended Implementation Pattern

This pattern is designed to be filesystem-agnostic and will work with both **ext4** and **btrfs**.

```python
import os
import importlib
from pathlib import Path

# Save original environment
orig_env = {
    "LIBGUESTFS_BACKEND": os.environ.get("LIBGUESTFS_BACKEND"),
    "LIBGUESTFS_CACHEDIR": os.environ.get("LIBGUESTFS_CACHEDIR"),
}

# Set optimization variables
os.environ["LIBGUESTFS_BACKEND"] = "direct"
if Path("/dev/shm").exists():
    os.environ["LIBGUESTFS_CACHEDIR"] = "/dev/shm"

try:
    guestfs = importlib.import_module("guestfs")
    g = guestfs.GuestFS(python_return_dict=True)
    
    # Apply handle optimizations
    if hasattr(g, "set_recovery_proc"):
        g.set_recovery_proc(False)
    if hasattr(g, "set_autosync"):
        g.set_autosync(False)
    if hasattr(g, "set_network"):
        g.set_network(False)
    if hasattr(g, "set_smp"):
        g.set_smp(1)
    if hasattr(g, "set_memsize"):
        g.set_memsize(256)

    # Use explicit format and writeback cache for speed.
    # Compatible with both ext4 and btrfs raw images.
    g.add_drive_opts(rootfs_path, readonly=False, format="raw", cachemode="writeback")
    
    g.launch()
    
    # ... mount, write, umount ...
    g.umount("/")
    
    g.shutdown()
finally:
    # Restore environment
    for key, value in orig_env.items():
        if value is not None:
            os.environ[key] = value
        elif key in os.environ:
            del os.environ[key]
```

## Performance Comparison

| Optimization Tier | Launch Time (typical) | Total Injection Time |
|-------------------|-----------------------|----------------------|
| None (Default) | 8.0s - 15.0s | 10.0s - 20.0s |
| Basic (1-4) ✅ | 3.0s - 5.0s | 4.0s - 6.0s |
| Aggressive (1-9) ✅ (1-9) | 1.0s - 3.0s | 2.0s - 4.0s |
| Ultimate (Fixed App) ✅ | < 1.0s | < 2.0s |

*Note: Measurement variance is primarily driven by CPU speed and disk I/O. Results are consistent across both **ext4** and **btrfs** when using `cachemode="writeback"`.*

## Implementation Status Summary

| # | Optimization | Status | Code Location |
|---|---|---|---|
| 1 | Direct backend (`LIBGUESTFS_BACKEND=direct`) | ✅ | `_base.py:46` |
| 2 | Networking disabled (`set_network(False)`) | ✅ | `_base.py:79-80` |
| 3 | Minimal vCPUs (`set_smp(1)`) | ✅ | `_base.py:81-82` |
| 4 | Minimal memory (`set_memsize(256)`) | ✅ | `_base.py:83-84` |
| 5 | Disable recovery process | ✅ | `_base.py:75-76` |
| 6 | Disable autosync | ✅ | `_base.py:77-78` |
| 7 | Format + cache mode (`cachemode="writeback"`) | ✅ | `_base.py:88-93` |
| 8 | Appliance cache in RAM (`/dev/shm`) | ✅ | `_base.py:47-48` |
| 9 | QEMU lock disable (`QEMU_LOCKING=off`) | ✅ | `_base.py:51` |
| 10 | Fixed appliance | ✅ | `_service.py:25` (via `cache init`) |
