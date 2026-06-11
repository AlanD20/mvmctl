> **STATUS: Current — fully accurate.** All paths, patterns, and architecture described match the current codebase. The loop-mount provisioner lives at `internal/service/loopmount/provisioner.go`, backend at `internal/lib/provisioner/loopmount/backend.go`, and wire protocol at `internal/service/loopmount/wire.go`.
>
> **Last verified:** 2026-06-10

# Service Binaries & Loop-Mount Provisioner

## Problem

All service subprocesses need to run as separate processes (for privilege isolation, PTY FD passing, etc.). In the Python version, each service was compiled as a standalone Nuitka binary. In Go, we use a **self-spawning pattern**: the single `mvm` binary re-executes itself with a `mvm run <service>` subcommand.

## Solution

The `mvm` binary contains all three service entry points as Cobra subcommands under `mvm run`. When a service needs to run as a subprocess, `system.SpawnService()` calls `os.Executable()` to get the current binary path, then runs `<mvm-binary> run <service> [args]`.

### Why this matters for performance

The `vm create` command previously spent ~2600ms inside a guestfs session doing SSH key injection, hostname setup, DNS config, cloud-init disable, and filesystem resize. This guestfs launch overhead is the #1 bottleneck. Replacing it with a ~200ms loop-mount binary saves ~2400ms per `vm create`.

## Services

All 3 services are subcommands of the same `mvm` binary. No separate compilation, no embedding, no extraction needed.

| Service | Subcommand | Runs as | Purpose |
|---------|-----------|---------|---------|
| **console_relay** | `mvm run console relay` | user | PTY-to-Unix-socket relay for serial console |
| **nocloud_server** | `mvm run nocloudnet serve` | user | HTTP server for cloud-init nocloud-net |
| **provisioner** | `mvm run provision` | **root** (sudo) | Loop mount provisioning (SSH, DNS, grow/shrink) |

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

## Self-Spawning Pattern

Every service uses the same spawn infrastructure:

```go
// internal/lib/system/spawn.go

type SpawnConfig struct {
    Name       string     // must match "mvm run <name>" Cobra command
    ExtraFiles []*os.File // FDs starting at 3 (for PTY pass-through)
    Privileged bool       // runs via sudo when true
    Stdin      io.Reader
    Stdout     io.Writer
    Stderr     io.Writer
    Args       []string   // additional args to "mvm run <name>"
}

func SpawnService(ctx context.Context, cfg SpawnConfig) (*exec.Cmd, error) {
    exe, err := os.Executable()
    if err != nil {
        return nil, err
    }

    args := []string{"run", cfg.Name}
    args = append(args, cfg.Args...)

    cmd := exec.CommandContext(ctx, exe, args...)
    cmd.ExtraFiles = cfg.ExtraFiles
    cmd.Stdin = cfg.Stdin
    cmd.Stdout = cfg.Stdout
    cmd.Stderr = cfg.Stderr
    cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

    if cfg.Privileged && os.Getuid() != 0 {
        // Prepend sudo
        sudoArgs := append([]string{"-n", exe}, args...)
        cmd = exec.CommandContext(ctx, "sudo", sudoArgs...)
        cmd.ExtraFiles = cfg.ExtraFiles
        cmd.Stdin = cfg.Stdin
        cmd.Stdout = cfg.Stdout
        cmd.Stderr = cfg.Stderr
        cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
    }

    return cmd, cmd.Start()
}
```

This means:
- **No separate binaries** — single binary, multiple entry points
- **No embedding/extraction** — no `//go:embed` for service binaries
- **No binary-first fallback** — mutual exclusion, not preference
- **Privilege escalation** — `Privileged: true` auto-prepends `sudo`

## Old vs. New

### Before (Python — Nuitka compiled binaries)

```python
# console_relay/manager.py
bin_dir = CacheUtils.get_bin_dir()
binary = bin_dir / "mvm-console-relay"
if binary.exists():
    relay_cmd = [str(binary), "--pty-controller-fd", str(fd), ...]
else:
    # Development mode fallback
    relay_cmd = [sys.executable, "-m", "mvmctl.services.console_relay.process", ...]

proc = subprocess.Popen(relay_cmd, pass_fds=[pty_controller_fd])
```

### After (Go — self-spawning)

```go
// internal/service/console/spawn.go
func Spawn(ctx context.Context, cfg Config, ptyFile *os.File) (*SpawnResult, error) {
    spawnCfg := system.SpawnConfig{
        Name: "console relay",
        Args: []string{
            "--vm-id", cfg.VMID,
            "--vm-path", cfg.VMPath,
            "--pty-fd", "3",
        },
        ExtraFiles: []*os.File{ptyFile},
        Privileged: false,
    }

    cmd, err := system.SpawnService(ctx, spawnCfg)
    if err != nil {
        return nil, err
    }

    return &SpawnResult{
        SocketPath: filepath.Join(cfg.VMPath, "console.sock"),
        PID:        cmd.Process.Pid,
    }, nil
}
```

## Service Binary Contract

Every service entry point follows these rules:

1. **Zero external dependencies** — stdlib only (`encoding/json`, `os`, `net`, `syscall`, etc.)
2. **No mvmctl imports from service packages** — completely standalone (except `internal/service/loopmount/wire.go` which defines wire protocol types)
3. **JSON on stdin/stdout** for structured communication (provisioner only; console relay and nocloud server use CLI args)
4. **CLI argument interface** — Cobra flags for configuration
5. **Compiled into main binary** — no separate build step

## Provisioner Details

### Source

```
internal/service/loopmount/       ← subprocess entry point (stdlib only)
├── entry.go                      ← Run(ctx, Config) — reads JSON stdin, executes, writes JSON stdout
├── provisioner.go                ← Provisioner.Execute() — core engine (1214 lines)
├── spawn.go                      ← Spawn() — programmatic subprocess launch
└── wire.go                       ← WireInput/WireOutput — JSON protocol types

internal/lib/provisioner/loopmount/ ← caller-side backend
├── backend.go                    ← LoopMountBackend — builder pattern, queues ops, calls runWireOp()
├── partition.go                  ← Partition parsing types
└── utils.go                      ← sfdisk/parted parsers, detectAndRenameFS()

internal/lib/provisioner/         ← Public abstraction (used by API)
├── backend.go                    ← Backend interface, NewBackend() factory, BackendOpts
└── guestfs/                      ← GuestFS backend (alternative)
    ├── backend.go
    ├── provisioner.go
    ├── base.go
    └── utils.go

internal/infra/provcontent/       ← Shared operation types
└── content.go                    ← FileOp, ChrootOp, CopyDirOp, ResizeOp, content builders
```

### How the provisioner is selected

The loop-mount vs guestfs decision is made once at startup in `pkg/api/operation.go`:

```go
provisionerType := provisioner.ProvisionerLoopMount
guestfsEnabled, _ := s.Config.GetBool(ctx, "settings", "guestfs_enabled")
if guestfsEnabled {
    provisionerType = provisioner.ProvisionerGuestFS
}
```

`provisioner.NewBackend()` is a factory that constructs the correct backend based on this type:

```go
// internal/lib/provisioner/backend.go

type Backend interface {
    Resize(ctx context.Context, targetSizeBytes int64) error
    SetHostname(ctx context.Context, hostname string) error
    InjectDNS(ctx context.Context, dnsServer string) error
    SetupSSH(ctx context.Context, user string, sshPubkeys []string) error
    DisableCloudInit(ctx context.Context) error
    InjectCloudInit(ctx context.Context, cloudInitDir string) error
    DetectOS(ctx context.Context) (string, error)
    Deblob(ctx context.Context, osType *string) error
    FixFstab(ctx context.Context) error
    Shrink(ctx context.Context) error
    ExtractPartition(ctx, rawPath, outputPath string, partition int, disabledDetectors []string) (string, error)
    ConvertTo(ctx context.Context, targetFS string) error
    Run(ctx context.Context) error
}

func NewBackend(ctx context.Context, opts BackendOpts) (Backend, error) {
    switch opts.ProvisionerType {
    case ProvisionerLoopMount:
        return loopmount.NewBackend(opts.RootfsPath, opts.FSType, opts.CacheDir)
    case ProvisionerGuestFS:
        return guestfs.NewBackend(opts.RootfsPath, opts.FSType)
    default:
        return nil, fmt.Errorf("unknown provisioner type: %s", opts.ProvisionerType)
    }
}
```

| Current (guestfs) | New (loop) |
|-------------------|------------|
| `GuestfsBackend(rootfsPath, ...)` | `LoopMountBackend(rootfsPath, fsType, cacheDir)` |
| `.SetupSSH(user, pubkeys)` | `.SetupSSH(user, pubkeys)` — same API via shared content builders |
| `.SetHostname(name)` | `.SetHostname(name)` |
| `.InjectDNS(dnsServer)` | `.InjectDNS(dnsServer)` |
| `.DisableCloudInit()` | `.DisableCloudInit()` |
| `.InjectCloudInit(dir)` | `.InjectCloudInit(cloudInitDir)` |
| `.Resize(bytes)` | `.Resize(targetSizeBytes)` |
| `.Run()` → **2600ms** QEMU launch | `.Run()` → **~200ms** loop mount + operations |

### JSON Protocol (stdin → stdout)

**Input:**

```json
{
  "image": "/path/to/rootfs.img",
  "fs_type": "ext4",
  "operations": {
    "files": [
      {
        "path": "/etc/hostname",
        "data": "<base64>",
        "mode": 644,
        "uid": 0,
        "gid": 0
      },
      {
        "path": "/root/.ssh/authorized_keys",
        "data": "<base64>",
        "mode": 600,
        "uid": 0,
        "gid": 0
      }
    ],
    "copy_dirs": [
      {
        "src": "/tmp/cloud-init-dir",
        "dst": "/var/lib/cloud/seed/nocloud-net"
      }
    ],
    "commands": [
      "useradd -m myuser",
      "ssh-keygen -A",
      "systemctl enable sshd"
    ],
    "resize": {
      "action": "grow",
      "bytes": 8589934592
    }
  }
}
```

The binary also supports `detect_os` and `convert_fs` actions:

```json
{
  "image": "/path/to/rootfs.img",
  "action": "detect_os",
  "fs_type": "ext4"
}
```

```json
{
  "image": "/path/to/rootfs.img",
  "action": "convert_fs",
  "fs_type": "ext4",
  "target_fs": "btrfs"
}
```

**Output (success):**

```json
{"status": "ok", "files_written": 5, "commands_run": 3}
```

**Output (error):**

```json
{"status": "error", "error": "Failed to mount: No such file or directory", "step": "mount"}
```

The binary exits with code 0 on success, 1 on error.

### Wire Protocol Types

Defined in `internal/service/loopmount/wire.go`:

```go
type WireInput struct {
    Image    string         `json:"image"`
    Action   string         `json:"action"`           // "provision", "detect_os", "convert_fs"
    FsType   string         `json:"fs_type,omitempty"`
    Debug    bool           `json:"debug,omitempty"`
    TargetFS string         `json:"target_fs,omitempty"`
    Shell    string         `json:"shell,omitempty"`
    Ops      WireOperations `json:"operations"`
}

type WireOperations struct {
    Files    []WireFileOp    `json:"files,omitempty"`
    CopyDirs []WireCopyDirOp `json:"copy_dirs,omitempty"`
    Commands []string        `json:"commands,omitempty"`
    Resize   *WireResizeOp   `json:"resize,omitempty"`
}

type WireFileOp struct {
    Path string `json:"path"`
    Data string `json:"data"`        // base64-encoded content
    Mode int    `json:"mode,omitempty"`
    UID  int    `json:"uid,omitempty"`
    GID  int    `json:"gid,omitempty"`
}

type WireCopyDirOp struct {
    Src  string `json:"src"`
    Dst  string `json:"dst"`
    Mode int    `json:"mode,omitempty"`
}

type WireResizeOp struct {
    Action   string `json:"action"`    // "grow" or "shrink"
    Bytes    int64  `json:"bytes,omitempty"`
    Headroom int    `json:"headroom,omitempty"`
}

type WireOutput struct {
    Status       string `json:"status"`
    Error        string `json:"error,omitempty"`
    Step         string `json:"step,omitempty"`
    FilesWritten int    `json:"files_written,omitempty"`
    CommandsRun  int    `json:"commands_run,omitempty"`
    OsType       string `json:"os_type,omitempty"`
    Note         string `json:"note,omitempty"`
    NewFSType    string `json:"new_fs_type,omitempty"`
    NewSizeBytes int64  `json:"new_size_bytes,omitempty"`
}
```

### Binary Flow

```
1. losetup -f -P --show <image>       # Set up loop with partition scanning
2. Detect root partition:
   - Scans /dev/loopNp1..p16 for Linux filesystems (ext4, btrfs, xfs)
   - Tries p1, p2 in order first
   - If multiple Linux filesystems found, picks the largest by device size
   - Falls back to p1, then to raw loop device for raw filesystem images
3. Detect filesystem type via blkid (fallback: ext4, or use fs_type hint from input)
4. mount <root_part> <mount_point>
5. Write all ops["files"] with base64 decode, correct mode/uid/gid
6. Copy all ops["copy_dirs"] from host src to guest dst (recursive os.walk)
7. chroot <mount_point> sh -c <cmd> for each ops["commands"]
8. if resize.grow:
     - truncate file to target size (before loop setup)
     - for non-btrfs: unmount, e2fsck -f -y + resize2fs
     - for btrfs: btrfs filesystem resize max /mnt (while mounted)
9. if resize.shrink:
     - e2fsck -f -y + resize2fs -M  (ext4, capture new size)
     - btrfs filesystem resize ...  (btrfs)
     - umount + losetup -d + truncate file to new size
10. umount <mount_point>
11. losetup -d <loop_dev>
12. Output JSON result
```

All steps wrapped in `defer` cleanup — `umount` and `losetup -d` run even on error.

### Operations Supported

| Operation | ext4 | btrfs |
|-----------|------|-------|
| Write files | Yes | Yes |
| Copy directories | Yes | Yes |
| Chroot commands | Yes | Yes |
| Grow | `e2fsck -f` → `resize2fs` | `btrfs filesystem resize max /mnt` |
| Shrink | `e2fsck -f` → `resize2fs -M` → truncate | `btrfs filesystem resize` → truncate |
| Symlinks | via chroot `ln -sf` | via chroot `ln -sf` |
| File deletion | via chroot `rm` | via chroot `rm` |
| Convert FS | via `convert_fs` action | via `convert_fs` action |

**btrfs subvolume note:** Archlinux images use a `@` subvolume. When the image was created by the mvmctl pipeline, `@` is the default subvolume, so a plain `mount -o loop` exposes it at the mount root. For non-default subvolumes, an optional `--subvol` flag can be added to the provisioner binary in the future. Guestfs abstracts this away; the binary makes the same assumption as the image build pipeline.

**`systemctl enable` in chroot note:** Commands like `systemctl enable sshd` work correctly in a chroot environment. `systemctl enable` only creates symlinks in `/etc/systemd/system/` — it does not require a running systemd daemon. Similarly, `useradd -m` creates passwd/shadow entries and a home directory without needing systemd's user manager.

### Performance

| Operation | guestfs | loop | Speedup |
|-----------|---------|------|---------|
| Provision (SSH + DNS + hostname + user) | ~2600ms | ~100ms | **26x** |
| Grow (e.g., 3GB → 8GB) | ~1000ms | ~50ms | **20x** |
| Shrink | ~3000ms | ~200ms | **15x** |
| **Total impact on `vm create`** | ~2600ms removed | **~200ms added** | net **~2400ms saved** |

### Backend Selection

`provisioner.NewBackend()` uses a factory pattern to select `LoopMountBackend` or `GuestfsBackend` based on a `ProvisionerType` enum. The type is determined by the caller (API layer) based on:
- Config setting `settings.guestfs_enabled` — if true, use GUESTFS
- Else — use LOOP_MOUNT (default)

**Key difference from Python:** No fallback chain. Mutual exclusion, not preference. If loopmount fails, it does NOT fall back to guestfs.

## Sudoers (provisioner only)

The provisioner needs sudo. Console relay and nocloud server run as the user.

In Go, sudo is handled automatically by `system.SpawnService()` when `Privileged: true`:

```go
spawnCfg := system.SpawnConfig{
    Name:       "provision",
    Args:       []string{"--input", "-"},
    Stdin:      strings.NewReader(jsonInput),
    Stdout:     &stdout,
    Privileged: true, // auto-prepends sudo
}
```

The sudoers file is managed by `mvm host init` via `HostService._generate_sudoers_content()`. The provisioner binary path is resolved at runtime via `os.Executable()`.

## Changes Made

| File | Change |
|------|--------|
| `internal/service/loopmount/entry.go` | **New** — subprocess entry: `Run(ctx, Config)` reads JSON stdin, executes, writes JSON stdout |
| `internal/service/loopmount/provisioner.go` | **New** — core engine (1214 lines): `Provisioner.Execute()` dispatches to `doProvision()`, `doDetectOS()`, `doConvertFS()` |
| `internal/service/loopmount/spawn.go` | **New** — `Spawn()` for programmatic subprocess launch |
| `internal/service/loopmount/wire.go` | **New** — JSON wire protocol types (`WireInput`, `WireOutput`) |
| `internal/lib/provisioner/loopmount/backend.go` | **New** — `LoopMountBackend` — builder pattern, queues ops, calls `runWireOp()` |
| `internal/lib/provisioner/loopmount/partition.go` | **New** — Partition parsing types |
| `internal/lib/provisioner/loopmount/utils.go` | **New** — sfdisk/parted parsers, `detectAndRenameFS()` |
| `internal/lib/provisioner/backend.go` | **New** — `Backend` interface, `NewBackend()` factory, `BackendOpts` |
| `internal/infra/provcontent/content.go` | **New** — Shared operation types (`FileOp`, `ChrootOp`, `CopyDirOp`, `ResizeOp`) and content builders |
| `internal/lib/system/spawn.go` | **New** — `SpawnService()` — self-spawn subprocess infrastructure |
| `internal/cli/service.go` | **New** — `mvm run` command tree with three subcommands |
| `internal/service/console/entry.go` | **New** — Console relay subprocess entry |
| `internal/service/console/spawn.go` | **New** — Console relay subprocess spawn |
| `internal/service/nocloudnet/entry.go` | **New** — NoCloud-net subprocess entry |
| `internal/service/nocloudnet/spawn.go` | **New** — NoCloud-net subprocess spawn |
| `pkg/api/operation.go` | Modified — resolves provisioner type at startup |
| `pkg/api/vm.go` | Modified — uses `provisioner.NewBackend()` instead of direct guestfs |

## Key Differences from Python Version

| Aspect | Python | Go |
|--------|--------|-----|
| **Service binaries** | 3 separate Nuitka-compiled binaries | Single binary, 3 `mvm run` subcommands |
| **Binary embedding** | `//go:embed` + extraction to `~/.cache/mvmctl/bin/` | No embedding — self-spawning via `os.Executable()` |
| **Binary-first fallback** | Try compiled binary, fall back to `sys.executable -m` | No fallback — mutual exclusion, not preference |
| **Build pipeline** | `scripts/build_services.py` with Nuitka multidist | Standard `go build` — no separate build step |
| **Sudoers management** | `PRIVILEGED_SERVICE_BINARIES` list in constants | `SpawnConfig.Privileged` auto-prepends sudo |
| **Wire protocol** | JSON stdin/stdout | JSON stdin/stdout (identical) |
| **Actions** | `provision`, `detect_os` | `provision`, `detect_os`, `convert_fs` (new) |
| **Backend selection** | Config + binary availability | Config only (mutual exclusion) |
| **Partition detection** | Python `os.scandir()` | Go `parsePartitionsSfdisk()` / `parsePartitionsParted()` |

## Risks

| Risk | Mitigation |
|------|-----------|
| Loop device exhaustion | Fall back to guestfs. Linux default is 256 loop devices. |
| Orphaned mounts on crash | Binary always `umount` + `losetup -d` in `defer`. |
| nocloud-server port conflict | Manager scans ports 8000-9000 (already implemented). |
| Binary version mismatch | Single binary — always in sync. |
| sudoers file management | Written by `sudo mvm host init`. Remove via `sudo rm /etc/sudoers.d/mvm-provision`. |
| btrfs non-default subvolume | Current assumption: `@` is default subvolume. Future: add `--subvol` flag to binary. |
| `systemctl enable` in chroot | Works correctly — only creates symlinks, no systemd daemon needed. |
