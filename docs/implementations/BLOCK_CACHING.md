# Block Device Caching Strategies

Choose a cache mode according to your workload's durability needs. Guest synchronization and host storage durability
are related, but they are not interchangeable guarantees.

## Table of Contents

- [Current configuration](#current-configuration)
- [Write path and cache modes](#write-path-and-cache-modes)
- [Vsock agent synchronization](#vsock-agent-synchronization)
- [Application and manual synchronization](#application-and-manual-synchronization)
- [Key files](#key-files)

## Current configuration

VM root disks default to `Unsafe`. Use `mvm vm create --writeback` or set
`defaults.firecracker.drive_cache_type` to `Writeback` to enable guest-requested backing-file flushes.
An explicit CLI flag overrides the configured default. Data volumes have their own cache setting;
`defaults.volume.cache_type` defaults to `Unsafe`. The read-only cloud-init drive remains `Unsafe`.

These choices are resolved in `pkg/api/inputs/vm_create.go` and `pkg/api/inputs/volume_create.go`.
The typed launch builder in `internal/service/jailer/managed_vm_config.go` writes each admitted drive's
cache type into the Firecracker configuration.

## Write path and cache modes

Writes cross several independently buffered layers:

```
Guest process → guest page cache → VirtIO block device → host page cache → backing storage
```

Stopping the VM can lose dirty guest pages. A host crash or power loss can also lose dirty host pages;
stopping Firecracker alone does not imply that the host page cache disappears.

| Mode | Flush behavior | Intended tradeoff |
|------|----------------|-------------------|
| `Unsafe` (default) | Does not advertise the VirtIO flush feature | Fewer flush operations, without guest-requested host durability |
| `Writeback` | Advertises VirtIO flush; an accepted guest flush causes backing-file `fsync` | Supports durability requests at additional I/O cost |

The guest must negotiate the flush feature. In Unsafe mode, guest synchronization still writes guest
dirty pages toward the device; it is not a complete no-op, but cannot request the same backing-file flush.
An acknowledged Writeback flush commits data according to the host storage stack's guarantees. It is
not an application transaction or a guarantee against every hardware or filesystem failure.
See the [Firecracker 1.17.0 caching contract](https://github.com/firecracker-microvm/firecracker/blob/v1.17.0/docs/api_requests/block-caching.md).

## Vsock agent synchronization

On its normal completion path, `handleExec()` calls `unix.Sync()` before sending the command result.
The guest file-transfer push handler calls it before sending transfer DONE. Both skip that final call
when `--no-sync` is requested. Early errors can return before synchronization.

This push behavior covers files received by the guest, not every `mvm cp` direction: the guest pull
handler streams files out without that final guest-wide sync.

```bash
# Skip final guest sync after receiving files
mvm cp --no-sync ./file.txt my-vm:/path/

# Skip final guest sync after command execution
mvm exec --no-sync my-vm -- echo quick
```

The final sync helps drain guest writes before completion; it does not upgrade an Unsafe drive to
Writeback or report an application-specific durability verdict. For a normal successful exec without
`--no-sync`, a second `mvm exec my-vm -- sync` is redundant with the agent's final sync.

## Application and manual synchronization

For writes performed outside the agent, such as through SSH, synchronize before stopping the VM.
Choose a scope appropriate to the application:

| Method | Scope |
|--------|-------|
| `fsync(fd)` | File data and metadata; synchronizes device caches as supported by the stack |
| `syncfs(fd)` | The filesystem containing the referenced file |
| `sync` | Filesystems across the guest |

Guest `fsync` is not categorically limited to copying data into the host cache. Its durability behavior
depends on the guest filesystem and negotiated device flush support. Persisting a new or renamed
directory entry may also require syncing the containing directory. Applications should check errors
from their own synchronization calls.
See [Linux fsync(2)](https://man7.org/linux/man-pages/man2/fsync.2.html).

## Key files

| File | Purpose |
|------|---------|
| `internal/service/agent/exec.go` | Final guest sync before the normal exec result |
| `internal/service/agent/file_transfer.go` | Guest push synchronization and pull streaming |
| `pkg/api/inputs/vm_create.go` | Root-disk Writeback selection |
| `internal/service/jailer/managed_vm_config.go` | Admitted drive cache configuration |
