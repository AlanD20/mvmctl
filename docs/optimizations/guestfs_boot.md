# libguestfs Boot Time Optimizations

> **Note:** The guestfs provisioning path is the **fallback backend** in mvmctl. The primary provisioning backend is the loop-mount binary (`mvm-provision` via `_LoopMountBackend`). Guestfs is used only when the loop-mount binary is unavailable or `guestfs_enabled` is set to `true` in config.

## Overview

This document describes the boot-time optimizations for the fallback libguestfs provisioning path in mvmctl. These optimizations reduce appliance startup time by configuring the backend directly, minimizing resource allocation, and disabling unnecessary services. All optimizations documented here are compatible with and highly beneficial for both **ext4** and **btrfs** root filesystems.

## Applied Optimizations

These optimizations are applied via the `_GuestfsBackend` class in `src/mvmctl/core/_shared/_provisioner/_backend.py`:

### 1. Direct Backend (Environment Variable)

The libguestfs appliance uses the `direct` backend (QEMU/KVM directly) instead of libvirt. This eliminates libvirt IPC overhead and dependency resolution delays.

**Implementation:**
```python
os.environ["LIBGUESTFS_BACKEND"] = "direct"
```

### 2. Networking Disabled (Handle Method)

Network interface initialization is skipped, eliminating DHCP client startup and link state waits.

**Implementation:**
```python
if hasattr(g, "set_network"):
    g.set_network(False)
```

### 3. Minimal vCPUs (Handle Method)

The appliance runs with a single vCPU, reducing hardware initialization time.

**Implementation:**
```python
if hasattr(g, "set_smp"):
    g.set_smp(1)
```

### 4. Minimal Memory (Handle Method)

Memory allocation is reduced to 256MB, significantly faster than the default 500MB+ allocation.

**Implementation:**
```python
if hasattr(g, "set_memsize"):
    g.set_memsize(256)
```

## Aggressive Optimizations

For cases where every millisecond counts, the following aggressive optimizations can be applied:

### 5. Disable Recovery Process (Handle Method)

By default, libguestfs forks a "recovery process" that monitors the appliance and kills the QEMU instance if the main process crashes. Disabling this saves a `fork()` and `exec()` call during `g.launch()`.

**Implementation:**
```python
if hasattr(g, "set_recovery_proc"):
    g.set_recovery_proc(False)
```

### 6. Disable Autosync (Handle Method)

By default, libguestfs calls `sync` automatically when you close the handle or shut down. Since we call `g.umount("/")` after writing, which already flushes buffers, `autosync` is redundant and can be disabled to speed up `g.shutdown()`.

**Implementation:**
```python
if hasattr(g, "set_autosync"):
    g.set_autosync(False)
```

### 7. Explicit Disk Format & Cache Mode (Add Drive)

Specifying the disk format (e.g., `raw` for `.ext4` or `.btrfs` raw images) avoids QEMU's format probing overhead. 

Using **`cachemode="unsafe"`** is the single most important optimization for filesystem operations inside libguestfs, especially for **btrfs**.

*   **ext4**: Benefits from reduced metadata flush latency.
*   **btrfs**: Benefits **massively**. Btrfs's Copy-on-Write (CoW) metadata tree updates are extremely synchronous and cause frequent host-side flushes. `unsafe` mode hides this overhead by telling QEMU to ignore all sync requests from the appliance guest.

**Implementation:**
```python
# Format is usually "raw" for the images used in mvmctl
g.add_drive(rootfs_path, readonly=False, format="raw", cachemode="unsafe")
```

### 8. Appliance Cache in RAM (Environment Variable)

Setting `LIBGUESTFS_CACHEDIR` to a RAM-backed filesystem (like `/dev/shm`) speeds up the supermin appliance checking and building phase.

**Implementation:**
```python
os.environ["LIBGUESTFS_CACHEDIR"] = "/dev/shm"
```

### 9. Disable QEMU File Locking (Environment Variable)

QEMU's default file locking (`fcntl` locks) can cause `guestfs_launch` failures when a previous guestfs session crashes and leaves a stale lock on the image file. This is common with the ready pool, where multiple VM creations may reference the same source image.

Setting `QEMU_LOCKING=off` disables this locking mechanism. This is safe in mvmctl because:
- Each VM works on its own **copy** of the image, never the shared source
- The ready pool image is effectively read-only after creation
- No concurrent writers or shared storage scenarios exist

**Implementation:**
```python
os.environ["QEMU_LOCKING"] = "off"
```

### 10. Fixed Appliance (Environment Variable)

Using a pre-built fixed appliance completely bypasses the `supermin` checking logic. This is the ultimate optimization for launch speed.

**Implementation:**
```bash
# First, create the fixed appliance once: this is performed by mvm cache init
mkdir -p $MVM_CACHE_DIR/appliance
libguestfs-make-fixed-appliance $MVM_CACHE_DIR/appliance

# Then, use it by setting LIBGUESTFS_PATH:
export LIBGUESTFS_PATH=$MVM_CACHE_DIR/appliance
```

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

    # Use explicit format and unsafe cache for speed.
    # Compatible with both ext4 and btrfs raw images.
    g.add_drive(rootfs_path, readonly=False, format="raw", cachemode="unsafe")
    
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
| Basic (1-4) | 3.0s - 5.0s | 4.0s - 6.0s |
| Aggressive (1-8) | 1.0s - 3.0s | 2.0s - 4.0s |
| Ultimate (Fixed App) | < 1.0s | < 2.0s |

*Note: Measurement variance is primarily driven by CPU speed and disk I/O. Results are consistent across both **ext4** and **btrfs** when using `cachemode="unsafe"`.*
