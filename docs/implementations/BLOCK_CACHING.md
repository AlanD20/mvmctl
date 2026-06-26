# Block Device Caching Strategies

Firecracker offers two caching strategies for block devices: **Writeback** (default) and
**Unsafe**. This document explains how they work, how they affect data integrity, and how
mvmctl's vsock agent interacts with them.

## Background

When a process inside the microVM writes data to a file, the data travels through several
layers before reaching persistent storage:

```
Guest process → Guest kernel page cache → VirtIO block device → Firecracker → Host page cache → Disk
```

Each layer may acknowledge the write before the data is actually on disk. The caching
strategy determines when Firecracker flushes its internal buffer to the backing file.

## Caching Strategies

### Writeback (default)

When a block device is configured with `cache_type: Writeback`, Firecracker advertises the
VirtIO `flush` feature to the guest driver. If the guest negotiates this feature, the guest
kernel can send `VIRTIO_BLK_T_FLUSH` requests. When Firecracker receives a flush request,
it calls `fsync(2)` on the backing file, committing all data in the host page cache to disk.

```
Guest process → Guest kernel page cache → VirtIO write (data acknowledged by Firecracker)
                                                              ↓
Guest sync() → VIRTIO_BLK_T_FLUSH → Firecracker fsync → Disk
```

**Integrity guarantee:** Once a flush request is acknowledged by Firecracker, the data is
on physical storage. If the host crashes or loses power after the flush completes, no data
is lost.

**Performance cost:** Each `sync()` syscall from the guest triggers an `fsync()` on the
backing file. For workloads with frequent small writes, this adds significant latency.

### Unsafe

When configured with `cache_type: Unsafe`, Firecracker does NOT advertise the VirtIO
`flush` feature. The guest `sync()` syscall becomes a no-op for the block device —
Firecracker never fsync's the backing file. Data may remain in the host page cache
indefinitely.

```
Guest process → Guest kernel page cache → VirtIO write (data acknowledged by Firecracker)
                                                              ↓
Guest sync() → VIRTIO_BLK_T_FLUSH → ⛔ ignored by Firecracker → data stays in host page cache
```

**Integrity risk:** If the host crashes or loses power, data in the host page cache is
lost. The guest was told the write completed, but the data never reached disk.

**Performance benefit:** Zero flush-related syscalls. Maximum throughput for ephemeral
workloads.

## mvmctl vsock agent integration

The vsock guest agent (`internal/service/vsockagent/`) automatically calls `unix.Sync()`
after every `mvm vm exec` command and every `mvm cp` file transfer, unless `--no-sync` is
passed.

In **Writeback** mode (default), this ensures data reaches physical storage before the
operation is considered complete. This prevents data loss when the VM is stopped
immediately after a write-heavy operation — a scenario that previously truncated files
because Firecracker's host page cache hadn't flushed yet.

In **Unsafe** mode, the `sync()` call is still performed by the agent, but Firecracker
ignores it, so it's effectively a no-op. The `--no-sync` flag can be used to skip even
this no-op, saving a small amount of overhead.

## When to use each mode

| Mode | Use case | Data integrity |
|---|---|---|
| **Writeback** (default) | Persistent VMs, databases, file servers, any workload where data matters | ✅ Full integrity — sync() guarantees data on disk |
| **Unsafe** | Ephemeral VMs, CI runners, serverless, scratch compute | ⚠️ No integrity — data lost on host crash |

## CLI flags

Both `mvm cp` and `mvm vm exec` support `--no-sync` to skip the automatic `unix.Sync()`
call:

```bash
# Skip sync after file transfer (faster, riskier)
mvm cp --no-sync ./file.txt my-vm:/path/

# Skip sync after command
mvm vm exec --no-sync my-vm -- echo quick
```

Without `--no-sync`, the agent calls `unix.Sync()` automatically, which:
1. Flushes the guest kernel page cache to the VirtIO block device
2. Sends `VIRTIO_BLK_T_FLUSH` to Firecracker
3. Causes Firecracker to `fsync()` the backing file
4. Ensures data is on physical storage before the command returns

## Manual sync

If you're writing files inside the VM via methods other than `mvm cp` or `mvm vm exec`
(e.g., SSH, or directly from a process running inside the VM), you must sync manually
before stopping the VM to avoid data loss. There are three levels, each stronger than the
last:

### 1. `sync` — flush everything (recommended)

```bash
# Inside the VM, before stopping:
sync
```

Calls `sync(2)` which sends `VIRTIO_BLK_T_FLUSH` to all block devices, causing
Firecracker to `fsync()` all backing files.

### 2. `fsync` — flush a single file

```bash
# From your application code:
fsync(fd)
```

Flushes a single file's data through the guest kernel to Firecracker. In Writeback mode,
this ensures the data reaches Firecracker's host page cache — but does NOT trigger a
`VIRTIO_BLK_T_FLUSH`, so Firecracker may not yet have written it to the backing file.
Use `sync()` or a subsequent flush for full safety.

### 3. `syncfs` — flush a single filesystem

```bash
# Linux specific:
syncfs(fd_of_mount)
```

Like `sync` but scoped to a single mounted filesystem. Sends flush to the block device(s)
backing that mount only.

### When to use each

| Method | Scope | Flushes Firecracker? | Use case |
|---|---|---|---|
| `fsync(fd)` | Single file | ❌ (writes data to Firecracker, but no flush) | Application-level durability, partial safety |
| `syncfs(fd)` | One filesystem | ✅ Triggers VIRTIO_BLK_T_FLUSH on backing device | Targeted sync before stopping a specific volume |
| `sync` | All filesystems | ✅ Triggers flush on ALL block devices | **Best practice** before stopping the VM |

### In scripts

If you have a shell script that writes files and then stops the VM:

```bash
#!/bin/sh
mvm vm exec my-vm -- /setup.sh
mvm vm exec my-vm -- sync        # ensure data is on disk
mvm vm stop my-vm
```

Without the explicit `sync`, files written by `/setup.sh` could be lost in
Firecracker's host page cache when the VM stops.

## References

- [Firecracker block-caching documentation](https://github.com/firecracker-microvm/firecracker/blob/main/docs/api_requests/block-caching.md)
- `internal/service/vsockagent/exec.go` — `unix.Sync()` after exec commands
- `internal/service/vsockagent/file_transfer.go` — `unix.Sync()` after file transfers
