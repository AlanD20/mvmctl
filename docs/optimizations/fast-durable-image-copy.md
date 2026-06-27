# Fast Durable Image Copy for microVMs

> **STATUS: Current.** Two-level fallback (sendfile(2) → io.Copy) + fdatasync in `infra.CopyFile()` via `MaterializeTo()`. Sparse dd fallback (`CopyBytesDD`) exists in `internal/lib/system/block.go` and is used by the loopmount backend, but is NOT part of the `MaterializeTo()` chain.

## Overview

When a microVM starts, its rootfs image is copied from the warm pool to the VM's persistent directory on disk. This copy must be **durable** — if the host crashes, the VM rootfs must not be corrupt. But it also needs to be **fast**, because disk copies of multi-GB images can dominate VM launch latency.

## The Problem

Naive approaches have issues:

| Approach | Speed | Durability | Problem |
|---|---|---|---|
| `io.Copy` (no sync) | Fast | ❌ | Machine crash = corrupt image |
| `sendfile(2)` + `fdatasync()` | Fastest | ✅ | In-kernel zero-copy, no metadata waste |
| `cp --sparse=always` | Medium | ❌ | No durability guarantee without explicit sync |
| `dd conv=sparse,fsync` | Slowest | ✅ | Sparse-aware but slower than sendfile |

The warm pool images obtained from the cache are already stored on a tmpfs (`$TMPDIR/ready` by default, configurable via `MVM_WARM_POOL=disk`). The copy from tmpfs to the VM's persistent disk is the critical path.

## The Solution ✅ IMPLEMENTED

Three optimizations work together in `MaterializeTo()` (`internal/core/image/service.go`), which delegates to `infra.CopyFile()` (`internal/infra/io.go`):

### 1. sendfile(2) Zero-Copy Transfer ✅ IMPLEMENTED

[`sendfile(2)`](https://man7.org/linux/man-pages/man2/sendfile.2.html) copies data between file descriptors entirely in kernel space — no data is copied to userspace. The Go implementation uses `unix.Sendfile()` from `golang.org/x/sys/unix`.

**Code reference:** `internal/infra/io.go` — `copyViaSendfile()`

```go
const maxSend = 0x7ffff000
var offset int64
for {
    n, err := unix.Sendfile(int(dst.Fd()), int(src.Fd()), &offset, maxSend)
    if err != nil {
        return fmt.Errorf("sendfile at offset %d: %w", offset, err)
    }
    if n == 0 {
        break
    }
}
```

This is the **primary** copy path and succeeds in almost all cases since both source (tmpfs) and destination (ext4/XFS/btrfs) are regular files.

### 2. io.Copy Userspace Fallback ✅ IMPLEMENTED

If `sendfile(2)` fails (e.g., the kernel or filesystem doesn't support it), the fallback is a standard `io.Copy` with Go's default 32KB buffer.

**Code reference:** `internal/infra/io.go` — `copyViaIO()`

```go
_, err = io.Copy(dst, src)
```

### 3. fdatasync for Durability ✅ IMPLEMENTED

After either copy method succeeds, `CopyFile()` opens the output file and calls `syscall.Fdatasync()` to flush file data and critical metadata (file size) to disk, skipping non-critical metadata (mtime/atime/ctime).

**Code reference:** `internal/infra/io.go` (inside `CopyFile()`)

```go
f, err := os.Open(outputPath)
// ...
if err := syscall.Fdatasync(int(f.Fd())); err != nil {
    f.Close()
    return err
}
```

**Why `fdatasync()` and not `fsync()`?**

| Syscall | Flushes | Use Case |
|---|---|---|
| `fsync()` | Data + ALL metadata | When timestamp preservation matters |
| `fdatasync()` | Data + file size only | When you only need data durability |

For a VM rootfs image, the data must survive a crash. The file's mtime doesn't matter — `fdatasync()` is the correct, faster choice.

### 5. cp --sparse=always (Loopmount Partition Extraction) ✅ IMPLEMENTED

The loopmount provisioner backend's `ExtractPartition()` uses `cp --sparse=always` when the input image is already a raw filesystem (detected via `blkid`). This copies the raw filesystem image while preserving holes, with a fallback to `dd conv=sparse,fsync`.

**Code reference:** `internal/lib/provisioner/loopmount/backend.go`

```go
result, _ := system.DefaultRunner.Run(
    ctx,
    []string{"cp", "--sparse=always", rawPath, outputPath},
    RunCmdOpts{Capture: true, Check: false},
)
if !result.Success() {
    if err := system.CopyBytesDD(ctx, rawPath, outputPath, 0, 0); err != nil {
        return "", err
    }
}
```

## Fallback Chain Summary

The fallback chain in `MaterializeTo()` (`→ infra.CopyFile()`) is:

```
sendfile(2)  →  io.Copy
```

Both paths end with `syscall.Fdatasync()` for data integrity.

The loopmount backend's `ExtractPartition()` uses a separate chain with its own dd fallback:

```
cp --sparse=always  →  dd conv=sparse,fsync
```

The `CopyBytesDD()` utility (`internal/lib/system/block.go`) is available for other callers that need a sparse-aware dd with built-in fsync, but it is NOT part of the `MaterializeTo()` chain.

### Why sendfile(2) Over reflink

The Go codebase uses `sendfile(2)` instead of `cp --reflink=auto` (copy-on-write / reflink) because:

1. `sendfile(2)` works across **all** filesystems (tmpfs, ext4, XFS, btrfs, zfs) — not just CoW-capable ones.
2. `sendfile(2)` is an in-kernel zero-copy operation — no userspace buffer, no context switch overhead per chunk.
3. No dependency on `cp` binary availability or version.
4. The warm pool is on tmpfs, so the CoW benefit of reflink (instant metadata-only clone) is inapplicable — tmpfs does not support reflink.

## Performance Characteristics

| Scenario | Primary Mechanism | Expected Speed |
|---|---|---|
| tmpfs → ext4/XFS/btrfs | `sendfile(2)` + `fdatasync()` | Fastest (in-kernel zero-copy) |
| sendfile fails → fallback | `io.Copy` (32KB buffer) | Medium (userspace buffer) |
| Raw fs extraction (loopmount) | `cp --sparse=always` | Faster for sparse raw images |

## Why Not Pipelined sync_file_range()?

A more complex approach could pipeline writes using `sync_file_range()` to schedule async flushes while copying (PostgreSQL WAL pattern), then call `fdatasync()` at the end. This requires chunked manual copy with raw syscalls.

**Not worth it because:**
1. The bottleneck is the copy itself (SSD write speed), not the sync.
2. `fdatasync()` on the output file takes ~5-50ms on modern NVMe — negligible vs copy time.
3. The pipelining win is marginal when the sync phase is already fast.

Only pursue this if profiling shows `fdatasync()` itself is measurably slow (>100ms).

## Related Files

- `internal/core/image/service.go` — `MaterializeTo()`, `EnsureCached()`
- `internal/infra/io.go` — `copyViaSendfile()`, `copyViaIO()`, `CopyFile()`
- `internal/lib/system/block.go` — `CopyBytesDD()`
- `internal/lib/provisioner/loopmount/backend.go` — `ExtractPartition()`
- `internal/infra/constants.go` — `GetWarmImagesDir()`
- `internal/infra/io.go` — `CopyPreservingMetadata()`
