# Fast Durable Image Copy for microVMs

> **Status: ✅ IMPLEMENTED** — All optimizations described here are fully implemented in the current codebase.
> 
> See `src/mvmctl/core/image/_service.py` method `materialize_to()` for the actual implementation.

## Overview

When a microVM starts, its rootfs image is copied from a tmpfs (RAM disk) cache to the VM's persistent directory on disk. This copy must be **durable** — if the host crashes, the VM rootfs must not be corrupt. But it also needs to be **fast**, because tmpfs → disk copies of multi-GB images can dominate VM launch latency.

## The Problem

Naive approaches have issues:

| Approach | Speed | Durability | Problem |
|----------|-------|------------|---------|
| `cp` (no sync) | Fast | ❌ | Machine crash = corrupt image |
| `cp` + `fsync()` | Medium | ✅ | `fsync` flushes timestamps too — wasteful |
| `cp` + `fdatasync()` | Faster | ✅ | Skips timestamps — appropriate for data files |
| `shutil.copy2` | Slow | ❌ | No durability guarantee |

Additionally, freshly decompressed ext4 rootfs images often have large zero-filled regions (unused space). Writing those zeros is wasted I/O.

## The Solution ✅ IMPLEMENTED

Three optimizations work together:

### 1. Reflink (Copy-on-Write) ✅ IMPLEMENTED

`cp --reflink=auto` creates an instant CoW clone on btrfs/XFS filesystems — no actual data is written, only metadata. On non-CoW filesystems (ext4), it silently falls back to a regular copy.

**Code reference:** `src/mvmctl/core/image/_service.py` line 443: `"--reflink=auto"`

### 2. Sparse Detection ✅ IMPLEMENTED

`--sparse=always` uses `lseek(SEEK_HOLE/SEEK_DATA)` to detect zero-filled regions and skips writing them. A 2 GB rootfs with 400 MB of actual data writes 400 MB, not 2 GB. This gives **2–5× speedup** on sparse images, which is typical for freshly decompressed ext4 rootfs images.

**Code reference:** `src/mvmctl/core/image/_service.py` line 444: `"--sparse=always"`

### 3. Durability via fdatasync ✅ IMPLEMENTED

After the copy, `os.fdatasync()` flushes file data and critical metadata (file size) to disk, but skips non-critical metadata (mtime/atime/ctime). This is **~10–30% faster** than `fsync()` for new files.

**Code reference:** `src/mvmctl/core/image/_service.py` line 453: `os.fdatasync(f.fileno())`

**Why `fdatasync()` and not `fsync()`?**

| Syscall | Flushes | Use Case |
|---------|---------|----------|
| `fsync()` | Data + ALL metadata | When timestamp preservation matters |
| `fdatasync()` | Data + file size only | When you only need data durability |

For a VM rootfs image, the data must survive a crash. The file's mtime doesn't matter — `fdatasync()` is the correct, faster choice.

## Fallback Path ✅ IMPLEMENTED

If `cp` fails entirely (binary missing, disk full, etc.), the fallback is `dd conv=sparse,fsync` to preserve holes and ensure durability, instead of falling back to `shutil.copy2` which writes all bytes including zeros.

**Code reference:** `src/mvmctl/core/image/_service.py` line 450: `self._copy_with_dd(cached_path, output_path, sparse=True)`

## Performance Characteristics

| Scenario | Mechanism | Expected Speed |
|----------|-----------|----------------|
| btrfs/XFS destination | Reflink CoW (instant) | ~0ms + fdatasync metadata only |
| ext4 with sparse image | `cp --sparse=always` | 2–5× faster than writing all bytes |
| ext4 with non-sparse image | `cp` (no reflink) | Writes all bytes, ~I/O bound |
| cp fails → fallback | dd conv=sparse,fsync | Slower than cp but handles edge cases |

## Why Not sync_file_range() Pipelining?

A more complex approach could pipeline writes using `sync_file_range()` to schedule async flushes while copying (PostgreSQL WAL pattern), then call `fdatasync()` at the end. This requires chunked manual copy with `ctypes` and adds significant complexity.

**Not worth it because:**
1. The bottleneck is the copy itself (SSD write speed), not the sync
2. `fdatasync()` on a 400 MB sparse copy takes ~5–50ms on modern NVMe — negligible vs copy time
3. The pipelining win is marginal when the sync phase is already fast

Only pursue this if profiling shows `fdatasync()` itself is measurably slow (>100ms).

## Related Files

- `src/mvmctl/core/image/_service.py` — `materialize_to()` method
- `src/mvmctl/utils/common.py` — `CacheUtils.get_warm_image_dir()`
