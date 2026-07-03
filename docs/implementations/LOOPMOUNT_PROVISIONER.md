# Service Binaries & Loop-Mount Provisioner

## Problem

Root filesystem provisioning for Firecracker VMs (SSH key injection, hostname setup, DNS config, cloud-init disable, filesystem resize) previously required a guestfs session, which launched a QEMU process taking ~2600ms. This guestfs launch overhead was the primary bottleneck in `vm create`. The loop-mount provisioner replaces guestfs with a direct kernel loop device approach that completes in ~200ms, saving ~2400ms per VM creation.

All service subprocesses (console relay, nocloud-net HTTP server, provisioner) run as separate processes for privilege isolation and PTY FD passing. They use a self-spawning pattern: the single `mvm` binary re-executes itself with a `mvm run <service>` subcommand.

## Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│  mvm (single binary, ~30MB)                                         │
│                                                                     │
│  Contains all service entry points:                                 │
│    • mvm run console relay    → console.Run()                      │
│    • mvm run nocloudnet serve → nocloudnet.Run()                   │
│    • mvm run provision        → loopmount.Run()                    │
│                                                                     │
│  Spawned via system.SpawnService():                                 │
│    • os.Executable() → current binary path                          │
│    • Runs: <exe> run <service> [args]                               │
│    • If Privileged: prepends sudo                                   │
│    • ExtraFiles[0] → FD 3 (for PTY pass-through)                   │
└─────────────────────────────────────────────────────────────────────┘
          │                    │                     │
          │ SpawnService()     │                     │
          ▼                    ▼                     ▼
   mvm run console      mvm run nocloudnet     mvm run provision
   relay                serve                  (via sudo)
          │                    │                     │
          │ (user)             │ (user)              │ (sudo -n)
          ▼                    ▼                     ▼
   console.Run()        nocloudnet.Run()       loopmount.Run()
   PTY↔socket relay     HTTP metadata server   JSON stdin/stdout
```

### Self-Spawning Pattern

`system.SpawnService()` in `internal/lib/system/spawn.go` gets the current binary path via `os.Executable()`, then runs `<mvm-binary> run <service> [args]`. If `Privileged: true`, it prepends `sudo -n`. `ExtraFiles` are passed starting at fd 3 (after stdin/stdout/stderr).

| Service | Subcommand | Runs as | Purpose |
|---------|-----------|---------|---------|
| **console_relay** | `mvm run console relay` | user | PTY-to-Unix-socket relay for serial console |
| **nocloud_server** | `mvm run nocloudnet serve` | user | HTTP server for cloud-init nocloud-net |
| **provisioner** | `mvm run provision` | **root** (sudo) | Loop mount provisioning (files, commands, resize) |

## Entry point

The provisioner is triggered by the API layer during `vm create`. In `pkg/api/operation.go`, the provisioner type is resolved once at startup: loop-mount by default, guestfs if `settings.guestfs_enabled` is true. The caller constructs a `Backend` via `provisioner.NewBackend()` in `internal/lib/provisioner/backend.go`, which returns a `LoopMountBackend` or `GuestfsBackend`.

The `LoopMountBackend` queues operations (files, commands, resize) via its builder methods, then calls `Run()` which marshals the operations to JSON and spawns the subprocess via `internal/service/loopmount/spawn.go`. The subprocess entry point is `Run()` in `internal/service/loopmount/entry.go`.

For the console relay and nocloud server, `system.SpawnService()` is called directly from their respective `spawn.go` files — they do not go through the provisioner backend.

## Happy path

### 1. Queue operations

The `LoopMountBackend` in `internal/lib/provisioner/loopmount/backend.go` provides builder methods: `SetupSSH()`, `SetHostname()`, `InjectDNS()`, `DisableCloudInit()`, `InjectCloudInit()`, `Resize()`, `InjectAgent()`. Each method validates its arguments and appends an operation to an internal queue.

### 2. Marshal and spawn

`LoopMountBackend.Run()` converts queued operations to a `WireInput` struct and calls `Spawn()` in `internal/service/loopmount/spawn.go`. `Spawn()` marshals the `WireInput` to JSON, calls `system.SpawnService()` with `Name: "provision"` and `Privileged: true`, pipes the JSON to the subprocess's stdin, and captures stdout.

### 3. Subprocess execution

The subprocess entry `loopmount.Run()` in `internal/service/loopmount/entry.go` reads JSON from stdin, parses it as `WireInput`, converts it to the internal `Op` type, and calls `Provisioner.Execute()`. The provisioner in `internal/service/loopmount/provisioner.go` executes the binary flow:

1. `losetup -f -P --show <image>` — set up loop device with partition scanning
2. Detect root partition — scans `/dev/loopNp1..p16` for Linux filesystems, picks the largest on tie, falls back to p1 then raw device
3. Detect filesystem type via `blkid` (fallback: ext4 or input hint)
4. `mount <root_partition> <mount_point>`
5. Write all files from `ops["files"]` with base64 decode, correct mode/uid/gid
6. Copy all directories from `ops["copy_dirs"]` from host to guest (recursive)
7. `chroot <mount_point> sh -c <cmd>` for each command in `ops["commands"]`
8. If resize.grow: truncate file, for ext4: `e2fsck -f -y + resize2fs`, for btrfs: `btrfs filesystem resize max /mnt`
9. If resize.shrink: `e2fsck -f -y + resize2fs -M`, `losetup -d` + truncate
10. `umount <mount_point>` and `losetup -d <loop_dev>`
11. Write JSON result to stdout

All steps are wrapped in `defer` cleanup — `umount` and `losetup -d` run even on error.

### 4. Result parsing

`Spawn()` parses the subprocess's stdout as `WireOutput`. On success, it returns the result. On error (status "error"), it returns an error with the step and message from the output.

## Wire protocol

The provisioner communicates via JSON on stdin/stdout. The protocol types are defined in `internal/service/loopmount/wire.go`.

### Input format

```json
{
  "image": "/path/to/rootfs.img",
  "action": "provision",
  "fs_type": "ext4",
  "operations": {
    "files": [
      {
        "path": "/etc/hostname",
        "data": "<base64>",
        "mode": 644,
        "uid": 0,
        "gid": 0
      }
    ],
    "copy_dirs": [
      {"src": "/tmp/cloud-init-dir", "dst": "/var/lib/cloud/seed/nocloud-net"}
    ],
    "commands": ["useradd -m myuser", "systemctl enable sshd"],
    "resize": {"action": "grow", "bytes": 8589934592}
  }
}
```

The `"action"` field supports `"provision"`, `"detect_os"`, and `"convert_fs"`.

### Output format

```json
{"status": "ok", "files_written": 5, "commands_run": 3}
```

On error:

```json
{"status": "error", "error": "Failed to mount: No such file or directory", "step": "mount"}
```

### Wire types

| Type | Fields | Purpose |
|------|--------|---------|
| `WireInput` | Image, Action, FsType, Debug, TargetFS, Shell, Ops | Input envelope |
| `WireOperations` | Files, CopyDirs, Commands, Resize | Operation container |
| `WireFileOp` | Path, Data (base64), Mode, UID, GID | File write operation |
| `WireCopyDirOp` | Src, Dst, Mode | Directory copy operation |
| `WireResizeOp` | Action (grow/shrink), Bytes, Headroom | Resize operation |
| `WireOutput` | Status, Error, Step, FilesWritten, CommandsRun, OsType, Note, NewFSType, NewSizeBytes | Result envelope |

## Provisioner backend selection

`provisioner.NewBackend()` uses a factory pattern to select `LoopMountBackend` or `GuestfsBackend` based on a `ProvisionerType` enum. The type is determined by the API layer based on the config setting `settings.guestfs_enabled`. Loop-mount is the default. There is no fallback chain — if loopmount fails, it does not fall back to guestfs.

## Performance

| Operation | Loop | Speedup vs guestfs |
|-----------|------|--------------------|
| Provision (SSH + DNS + hostname + user) | ~100ms | ~26x |
| Grow (e.g., 3GB → 8GB) | ~50ms | ~20x |
| Shrink | ~200ms | ~15x |
| **Total impact on `vm create`** | **~200ms added** | net **~2400ms saved** |

## Sudoers

The provisioner runs as root via sudo. `Spawn()` sets `Privileged: true`, and `system.SpawnService()` prepends `sudo -n`. The sudoers file is managed by `mvm host init` via `HostService.generateSudoersContent()`. The provisioner binary path is resolved at runtime via `os.Executable()`.

## Failure modes

### Loop device exhaustion

Linux defaults to 256 loop devices. If all are in use, `losetup` fails. The provisioner returns an error at the mount step. Future fallback to guestfs is possible but not implemented.

### Orphaned mounts on crash

The binary always calls `umount` and `losetup -d` in `defer` cleanup handlers. Even on error, the cleanup runs before exit.

### No-cloud server port conflict

The nocloud-net server scans for a free port in the range 8000-9000 before spawning.

### Binary version mismatch

All services are compiled into the same binary — there is no version mismatch risk across services.

### `systemctl enable` in chroot

Commands like `systemctl enable sshd` work correctly in a chroot because `systemctl enable` only creates symlinks in `/etc/systemd/system/` — it does not require a running systemd daemon.

### btrfs non-default subvolume

Archlinux images use a `@` subvolume. When the image was created by the mvmctl pipeline, `@` is the default subvolume, so a plain `mount -o loop` exposes it at the mount root. For non-default subvolumes, an optional `--subvol` flag is available for future use.

### Root partition detection failures

The provisioner scans `/dev/loopNp1..p16` for Linux filesystems. It tries p1 and p2 first, picks the largest on tie, and falls back to the raw loop device for raw filesystem images. If no filesystem is detected, the operation fails with a clear error.

## Key files

| File | Purpose |
|------|---------|
| `internal/service/loopmount/entry.go` | Subprocess entry: `Run()` reads JSON stdin, executes, writes JSON stdout |
| `internal/service/loopmount/provisioner.go` | `Provisioner.Execute()` — core engine dispatching to `doProvision()`, `doDetectOS()`, `doConvertFS()` |
| `internal/service/loopmount/spawn.go` | `Spawn()` — programmatic subprocess launch, JSON stdin/stdout |
| `internal/service/loopmount/wire.go` | JSON wire protocol types (`WireInput`, `WireOutput`) |
| `internal/lib/provisioner/loopmount/backend.go` | `LoopMountBackend` — builder pattern, queues ops, calls `runWireOp()` |
| `internal/lib/provisioner/loopmount/partition.go` | Partition parsing types |
| `internal/lib/provisioner/loopmount/utils.go` | sfdisk/parted parsers, `detectAndRenameFS()` |
| `internal/lib/provisioner/backend.go` | `Backend` interface, `NewBackend()` factory |
| `internal/infra/provcontent/content.go` | Shared operation types (`FileOp`, `ChrootOp`, `CopyDirOp`, `ResizeOp`) |
| `internal/lib/system/spawn.go` | `SpawnService()` — self-spawn subprocess infrastructure |
| `internal/cli/service.go` | `mvm run` command tree with subcommands |
| `internal/service/console/entry.go` | Console relay subprocess entry |
| `internal/service/console/spawn.go` | Console relay subprocess spawn |
| `internal/service/nocloudnet/entry.go` | NoCloud-net subprocess entry |
| `internal/service/nocloudnet/spawn.go` | NoCloud-net subprocess spawn |
| `pkg/api/operation.go` | Provisioner type resolution at startup |

## Design decisions

**Self-spawning single binary over separate binaries.** All services are subcommands of the same `mvm` binary. No separate compilation, no embedding, no extraction needed. This eliminates version mismatch between the host binary and service binaries.

**JSON stdin/stdout over CLI arguments for the provisioner.** The provisioner handles complex nested data (files with base64 content, directory copies, resize parameters) that doesn't fit in CLI flags. JSON provides a structured interface. Console relay and nocloud server use CLI flags because their configuration is flat.

**Loop-mount over guestfs as default.** Loop-mount is ~26x faster for provisioning (~100ms vs ~2600ms). Guestfs remains available as a configurable alternative for images that require its QEMU-based approach.

**No fallback chain.** If loop-mount fails, the operation fails immediately. Mutual exclusion avoids silent degradation where a fallback might succeed with different semantics.
