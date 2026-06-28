# Block Device Caching Strategies

## Problem

When a process inside the microVM writes data to a file, the data travels through several layers before reaching persistent storage: guest process → guest kernel page cache → VirtIO block device → Firecracker → host page cache → disk. Each layer may acknowledge the write before the data is actually on disk. If the VM is stopped immediately after a write-heavy operation, data in Firecracker's host page cache can be lost. Firecracker offers two caching strategies that determine when data is flushed to the backing file, and the vsock agent provides automatic synchronization after write operations.

## Architecture

```
Guest process → Guest kernel page cache → VirtIO block device → Firecracker → Host page cache → Disk
```

The caching strategy determines when Firecracker flushes its internal buffer to the backing file. The vsock guest agent calls `unix.Sync()` after every `mvm exec` command and every `mvm cp` file transfer, unless `--no-sync` is passed.

## Caching strategies

### Writeback (default)

When a block device is configured with `cache_type: Writeback`, Firecracker advertises the VirtIO `flush` feature to the guest driver. If the guest negotiates this feature, the guest kernel can send `VIRTIO_BLK_T_FLUSH` requests. When Firecracker receives a flush request, it calls `fsync(2)` on the backing file, committing all data in the host page cache to disk.

```
Guest process → Guest kernel page cache → VirtIO write (data acknowledged by Firecracker)
                                                              ↓
Guest sync() → VIRTIO_BLK_T_FLUSH → Firecracker fsync → Disk
```

**Integrity guarantee:** Once a flush request is acknowledged by Firecracker, the data is on physical storage.

**Performance cost:** Each `sync()` syscall from the guest triggers an `fsync()` on the backing file.

### Unsafe

When configured with `cache_type: Unsafe`, Firecracker does NOT advertise the VirtIO `flush` feature. The guest `sync()` syscall becomes a no-op for the block device — Firecracker never `fsync`s the backing file. Data may remain in the host page cache indefinitely.

```
Guest process → Guest kernel page cache → VirtIO write (data acknowledged by Firecracker)
                                                              ↓
Guest sync() → VIRTIO_BLK_T_FLUSH → ⛔ ignored by Firecracker → data stays in host page cache
```

**Integrity risk:** If the host crashes or loses power, data in the host page cache is lost.

**Performance benefit:** Zero flush-related syscalls. Maximum throughput for ephemeral workloads.

## Vsock agent sync integration

The vsock guest agent automatically calls `unix.Sync()` after every `mvm exec` command (in `internal/service/vsockagent/exec.go`) and after every `mvm cp` file transfer (in `internal/service/vsockagent/file_transfer.go`), unless `--no-sync` is passed.

In **Writeback** mode (default), this ensures data reaches physical storage before the operation is considered complete. This prevents data loss when the VM is stopped immediately after a write-heavy operation.

In **Unsafe** mode, the `sync()` call is still performed by the agent, but Firecracker ignores it, so it's effectively a no-op.

## CLI flags

Both `mvm cp` and `mvm exec` support `--no-sync` to skip the automatic `unix.Sync()` call:

```bash
# Skip sync after file transfer (faster, riskier)
mvm cp --no-sync ./file.txt my-vm:/path/

# Skip sync after command
mvm exec --no-sync my-vm -- echo quick
```

Without `--no-sync`, the agent calls `unix.Sync()` automatically, which:
1. Flushes the guest kernel page cache to the VirtIO block device
2. Sends `VIRTIO_BLK_T_FLUSH` to Firecracker
3. Causes Firecracker to `fsync()` the backing file
4. Ensures data is on physical storage before the command returns

## When to use each mode

| Mode | Use case | Data integrity |
|------|----------|----------------|
| **Writeback** (default) | Persistent VMs, databases, file servers, any workload where data matters | Full integrity — sync() guarantees data on disk |
| **Unsafe** | Ephemeral VMs, CI runners, serverless, scratch compute | No integrity — data lost on host crash |

## Manual sync

For files written via other methods (SSH, direct processes inside the VM), sync manually before stopping the VM:

### 1. `sync` — flush everything (recommended)

```bash
sync
```

Calls `sync(2)` which sends `VIRTIO_BLK_T_FLUSH` to all block devices, causing Firecracker to `fsync()` all backing files.

### 2. `fsync` — flush a single file

```bash
fsync(fd)
```

Flushes a single file's data through the guest kernel to Firecracker. In Writeback mode, this ensures the data reaches Firecracker's host page cache — but does NOT trigger `VIRTIO_BLK_T_FLUSH`, so Firecracker may not yet have written it to the backing file.

### 3. `syncfs` — flush a single filesystem

```bash
syncfs(fd_of_mount)
```

Like `sync` but scoped to a single mounted filesystem. Sends flush to the block device(s) backing that mount only.

| Method | Scope | Flushes Firecracker? | Use case |
|--------|-------|---------------------|----------|
| `fsync(fd)` | Single file | (writes data to Firecracker, but no flush) | Application-level durability |
| `syncfs(fd)` | One filesystem | Triggers VIRTIO_BLK_T_FLUSH on backing device | Targeted sync before stopping a specific volume |
| `sync` | All filesystems | Triggers flush on ALL block devices | Best practice before stopping the VM |

### In scripts

```bash
#!/bin/sh
mvm exec my-vm -- /setup.sh
mvm exec my-vm -- sync        # ensure data is on disk
mvm vm stop my-vm
```

Without the explicit `sync`, files written by `/setup.sh` could be lost in Firecracker's host page cache when the VM stops.

## Key files

| File | Purpose |
|------|---------|
| `internal/service/vsockagent/exec.go` | `handleExec()` — calls `unix.Sync()` after command execution |
| `internal/service/vsockagent/file_transfer.go` | `handleFTPush()` — calls `unix.Sync()` after file transfer |

## References

- [Firecracker block-caching documentation](https://github.com/firecracker-microvm/firecracker/blob/main/docs/api_requests/block-caching.md)
