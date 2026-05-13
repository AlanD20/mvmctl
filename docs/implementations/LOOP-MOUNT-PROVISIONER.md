# Service Binaries & Loop-Mount Provisioner

## Problem

All `src/mvmctl/services/` subprocesses currently use:

```python
relay_cmd = [sys.executable, "-m", "mvmctl.services.console_relay.process", ...]
```

This breaks when the app is compiled with Nuitka — `sys.executable` is no longer a Python interpreter that can do `-m` module resolution.

## Solution

Build **every service `process.py`** as a standalone compiled binary (Nuitka `--onefile`) and embed it inside the main `mvm` binary via `--include-data-dir`. Extract to `~/.cache/mvmctl/bin/` on `mvm init`. Managers use a **binary-first fallback pattern**: try the compiled binary, fall back to `sys.executable -m ...` in development mode.

### Why this matters for performance

The `vm create` command currently spends ~2600ms inside a guestfs session doing SSH key injection, hostname setup, DNS config, cloud-init disable, and filesystem resize. This guestfs launch overhead is the #1 bottleneck. Replacing it with a ~200ms loop-mount binary saves ~2400ms per `vm create`.

## Binaries

All 3 services are compiled into a **single combined binary** using Nuitka's multidist feature (`--main`). At runtime, the binary dispatches to the correct service based on `sys.argv[0]`. At extraction time, we copy the combined binary and create symlinks for each service name.

| Service | Symlink name | Runs as | Size | Purpose |
|---------|-------------|---------|------|---------|
| **console_relay** | `mvm-console-relay → mvm-services` | user | **~2.5MB total** (all 3) | PTY-to-socket relay for serial console |
| **nocloud_server** | `mvm-nocloud-server → mvm-services` | user | (shares runtime) | HTTP server for cloud-init nocloud-net |
| **provisioner** (new) | `mvm-provision → mvm-services` | **root** (sudo) | (shares runtime) | Loop mount provisioning (SSH, DNS, grow/shrink) |

Size comparison: 3 separate binaries would be ~5-7MB (each with own ~1MB runtime). Multidist is ~2-3MB total (~55% savings).

## Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│  mvm (compiled binary, ~15MB + ~2.5MB embedded mvm-services)       │
│                                                                     │
│  Embedded binary:                                                   │
│    • mvm-services  (~2.5MB, 3 entry points via multidist)          │
│                                                                     │
│  Extracted to ~/.cache/mvmctl/bin/ on `mvm init` (Step 5)          │
└─────────────────────────────────────────────────────────────────────┘
         │                    │                     │
         │ copy + symlinks    │                     │
         ▼                    ▼                     ▼
  mvm-console-relay     mvm-nocloud-server     mvm-provision
  (symlink → mvm-services)  (symlink → mvm-services)  (symlink → mvm-services)
         │                    │                     │
         │ (user)             │ (user)              │ (sudo -n)
         ▼                    ▼                     ▼
  Shared mvm-services binary dispatches via argv[0]
  ├── argv[0]="mvm-console-relay"  → console_relay main()
  ├── argv[0]="mvm-nocloud-server" → nocloud_server main()
  └── argv[0]="mvm-provision"      → provisioner main()
```

## Binary-first Fallback Pattern

Every manager uses the same pattern:

```python
# In console_relay/manager.py, nocloud_server/manager.py
# In core/_shared/_loopmount/_manager.py (for provisioner)
from mvmctl.utils.common import CacheUtils

bin_dir = CacheUtils.get_bin_dir()
binary = bin_dir / "mvm-<service-name>"
if binary.exists():
    cmd = [str(binary), ...]
else:
    # Development mode fallback
    cmd = [sys.executable, "-m", "mvmctl.services.<service>.process", ...]
```

This means:
- **Compiled mode**: binary exists at `BIN_DIR` → used
- **Development mode**: binary doesn't exist → falls back to `sys.executable -m ...`
- For the provisioner, both paths use `sudo -n` prepended to the command

## Old vs. New

### Before (current — breaks with compiled binary)

```python
# console_relay/manager.py
proc = subprocess.Popen(
    [sys.executable, "-m", "mvmctl.services.console_relay.process", ...],
)

# nocloud_server/manager.py
proc = subprocess.Popen(
    [sys.executable, "-m", "mvmctl.services.nocloud_server.process", ...],
)
```

### After (binary-first fallback)

```python
# All managers use the same pattern:
from mvmctl.utils.common import CacheUtils

bin_dir = CacheUtils.get_bin_dir()
binary = bin_dir / "mvm-console-relay"

if binary.exists():
    relay_cmd = [str(binary), "--pty-controller-fd", str(fd), ...]
else:
    relay_cmd = [sys.executable, "-m", "mvmctl.services.console_relay.process", ...]

proc = subprocess.Popen(
    relay_cmd,
    stdin=subprocess.DEVNULL,
    stdout=subprocess.DEVNULL,
    stderr=subprocess.DEVNULL,
    start_new_session=True,
    pass_fds=[pty_controller_fd],
)
```

## Service Binary Contract

Every service `process.py` follows these rules:

1. **Zero external dependencies** — stdlib only (`json`, `os`, `sys`, `subprocess`, `socket`, `select`, `signal`, `argparse`, `tempfile`, `shutil`, `base64`)
2. **No mvmctl imports** — completely standalone (except `console_relay/process.py` which imports `CONST_CONSOLE_READ_BUFFER_SIZE` from `_defaults.py`)
3. **JSON on stdin/stdout** for structured communication (provisioner only; console relay and nocloud server use CLI args)
4. **CLI argument interface** — `argparse` for flags
5. **Compiled with Nuitka** — `--onefile --lto=yes --enable-plugin=anti-bloat`

## Provisioner Details

### Source

```
src/mvmctl/services/loopmount/      ← standalone binary (stdlib only, no mvmctl imports)
├── __init__.py
└── process.py                      ← Standalone binary (stdlib only)

src/mvmctl/core/_shared/_loopmount/ ← Python-side lifecycle management
├── __init__.py
├── _manager.py                     # LoopMountManager — binary resolution, JSON, subprocess
└── _provisioner.py                 # LoopMountProvisioner — high-level operation builder

# LoopMountError / LoopMountTimeoutError defined in src/mvmctl/exceptions.py

src/mvmctl/core/_shared/_provisioner/ ← Public abstraction (used by API)
├── __init__.py
├── _backend.py                     # ProvisionerBackend factory + _LoopMountBackend + _GuestfsBackend
└── _content.py                     # ProvisionerContent — shared file/command/template definitions
```

### How the provisioner is selected

The loop-mount vs guestfs decision is made at the API level via `ProvisionerType` enum.
`ProvisionerBackend` is a factory that constructs the correct backend based on this type:

```python
# In ProvisionerBackend (core/_shared/_provisioner/_backend.py):
@staticmethod
def get_vm(
    rootfs_path: Path,
    *,
    provisioner_type: ProvisionerType,
    fs_type: str,
    ...
) -> _LoopMountBackend | _GuestfsBackend:
    if provisioner_type == ProvisionerType.LOOP_MOUNT:
        return _LoopMountBackend(rootfs_path, fs_type)
    elif provisioner_type == ProvisionerType.GUESTFS:
        ...
```

Binary availability is checked via `LoopMountManager.is_binary_available()`, and
the `ProvisionerType` enum is set based on whether the binary exists and whether
guestfs is enabled in config.

| Current (guestfs) | New (loop) |
|-------------------|------------|
| `GuestfsProvisioner(rootfs_path, ...)` | `LoopMountProvisioner(rootfs_path, fs_type)` |
| `.setup_ssh(user, pubkeys)` | `.setup_ssh(user, pubkeys)` — same API via shared content builders |
| `.set_hostname(name)` | `.set_hostname(name)` |
| `.inject_dns(dns_server)` | `.inject_dns(dns_server=dns_server)` |
| `.disable_cloud_init()` | `.disable_cloud_init()` |
| `.inject_cloud_init(dir)` | `.inject_cloud_init(cloud_init_dir)` |
| `.resize(bytes)` | `.resize(target_size_bytes)` |
| `.run()` → **2600ms** QEMU launch | `.run()` → **~200ms** loop mount + operations |

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

The binary also supports a `detect_os` action (no operations needed):

```json
{
  "image": "/path/to/rootfs.img",
  "action": "detect_os",
  "fs_type": "ext4"
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

All steps wrapped in `try/finally` — `umount` and `losetup -d` run even on error.

### Operations Supported

| Operation | ext4 | btrfs |
|-----------|------|-------|
| Write files | ✅ | ✅ |
| Copy directories | ✅ | ✅ |
| Chroot commands | ✅ | ✅ |
| Grow | `e2fsck -f` → `resize2fs` | `btrfs filesystem resize max /mnt` |
| Shrink | `e2fsck -f` → `resize2fs -M` → truncate | `btrfs filesystem resize` → truncate |
| Symlinks | via chroot `ln -sf` | via chroot `ln -sf` |
| File deletion | via chroot `rm` | via chroot `rm` |

**btrfs subvolume note:** Archlinux images use a `@` subvolume. When the image was created by the mvmctl pipeline, `@` is the default subvolume, so a plain `mount -o loop` exposes it at the mount root. For non-default subvolumes, an optional `--subvol` flag can be added to the provisioner binary in the future. Guestfs abstracts this away; the binary makes the same assumption as the image build pipeline.

**`systemctl enable` in chroot note:** Commands like `systemctl enable sshd` work correctly in a chroot environment. `systemctl enable` only creates symlinks in `/etc/systemd/system/` — it does not require a running systemd daemon. Similarly, `useradd -m` creates passwd/shadow entries and a home directory without needing systemd's user manager.

### Performance

| Operation | guestfs | loop | Speedup |
|-----------|---------|------|---------|
| Provision (SSH + DNS + hostname + user) | ~2600ms | ~100ms | **26x** |
| Grow (e.g., 3GB → 8GB) | ~1000ms | ~50ms | **20x** |
| Shrink | ~3000ms | ~200ms | **15x** |
| **Total impact on `vm create`** | ~2600ms removed | **~200ms added** | net **~2400ms saved** |

### Binary Availability Check

The `LoopMountManager.is_binary_available()` method checks whether the compiled binary (`CacheUtils.get_bin_dir() / "mvm-provision"`) exists, or whether the development-mode fallback (`services/loopmount/process.py`) is present.

### Backend Selection

`ProvisionerBackend` uses a factory pattern to select `_LoopMountBackend` or `_GuestfsBackend` based on a `ProvisionerType` enum. The type is determined by the caller (API layer) based on:
- Config setting `settings.guestfs_enabled` — if false, prefer LOOP_MOUNT
- Binary availability via `LoopMountManager.is_binary_available()`

## Binary Embedding

### Compiler

**Nuitka** (`--onefile --lto=yes --enable-plugin=anti-bloat`). Both `nuitka` and `pyinstaller` are in `pyproject.toml` `[dependency-groups] build`; Nuitka is the chosen compiler for production builds.

### Build pipeline — Multidist

A dedicated Python script handles the build:

```bash
uv run python scripts/build_services.py
```

The script uses **Nuitka's multidist** feature to compile all 3 services into a single `mvm-services` binary. Since all 3 source files are named `process.py` (would collide on basename), the script creates temporary symlinks with unique names in `build/symlinks/`:

```
build/symlinks/
├── mvm-console-relay   → src/mvmctl/services/console_relay/process.py
├── mvm-nocloud-server  → src/mvmctl/services/nocloud_server/process.py
└── mvm-provision       → src/mvmctl/services/loopmount/process.py
```

Then builds with:

```bash
nuitka --onefile --lto=yes --enable-plugin=anti-bloat ... \
  --main=build/symlinks/mvm-console-relay \
  --main=build/symlinks/mvm-nocloud-server \
  --main=build/symlinks/mvm-provision \
  --output-dir=dist/services --output-filename=mvm-services
```

The main binary includes the combined service binary via `--include-data-dir=dist/services=mvmctl/services`.

The existing `Taskfile.yml` `build-nuitka` task handles the main binary only. The script handles the full build chain.

Supports `--services`, `--service <name>`, and `--mvm` flags for partial builds.

### Extraction (`mvm init`, Step 5)

Extraction happens in `BinaryService.extract_service_binaries()`, called from `InitOperation._step_service_binaries()`.

```python
# In core/binary/_service.py

combined_src = BinaryService._get_embedded_path("mvm-services")
if combined_src is not None:
    # Copy the combined binary once
    shutil.copy2(str(combined_src), str(bin_dir / "mvm-services"))
    (bin_dir / "mvm-services").chmod(0o755)

    # Create DB entries and symlinks for each service
    for name in SERVICE_BINARY_NAMES:  # ["mvm-console-relay", "mvm-nocloud-server", "mvm-provision"]
        link_path = bin_dir / name
        if not link_path.exists():
            link_path.symlink_to("mvm-services")
        # Create BinaryItem DB record
        repo.upsert(BinaryItem(id=sha256, name=name, path=name, ...))
```

Development mode (`sys.frozen` is False) is a no-op — binaries don't exist, managers fall back to `sys.executable -m ...`.

All service binary names are defined in `constants.py`:
```python
SERVICE_BINARY_NAMES: Final[list[str]] = [
    "mvm-console-relay",
    "mvm-nocloud-server",
    "mvm-provision",
]

# Subset that needs sudo access
PRIVILEGED_SERVICE_BINARIES: Final[list[str]] = [
    "mvm-provision",
]
```

### Sudoers (provisioner only)

Managed through the existing `PRIVILEGED_BINARIES` mechanism. The service binary names that need sudo are listed in `PRIVILEGED_SERVICE_BINARIES` in `constants.py`. During `HostService._generate_sudoers_content()`, these paths are resolved at runtime via `CacheUtils.get_bin_dir()` and included in the sudoers drop-in alongside the system privileged binaries.

Only the provisioner needs sudo — console relay and nocloud server run as the user.

## Changes Made

| File | Change |
|------|--------|
| `services/loopmount/process.py` | **New** — standalone stdlib binary, JSON stdin/stdout, loop mount + provision |
| `core/_shared/_loopmount/_manager.py` | **New** — `LoopMountManager.execute()` and `detect_os()` methods, binary dev fallback |
| `core/_shared/_loopmount/_provisioner.py` | **New** — `LoopMountProvisioner` — accumulates ops and delegates to `LoopMountManager.execute()` |
| `exceptions.py` | **New** — `LoopMountError`, `LoopMountTimeoutError` in shared hierarchy |
| `core/_shared/_provisioner/_backend.py` | **New** — `ProvisionerBackend` factory, `_LoopMountBackend`, `_GuestfsBackend` |
| `core/_shared/_provisioner/_content.py` | **New** — `ProvisionerContent` — shared provisioning templates |
| `services/console_relay/manager.py` | Binary-first fallback: try `mvm-console-relay`, fall back to `sys.executable -m` |
| `services/nocloud_server/manager.py` | Binary-first fallback: try `mvm-nocloud-server`, fall back to `sys.executable -m` |
| `api/vm_operations.py` | Replaced `GuestfsProvisioner` with `ProvisionerBackend` dispatching based on `ProvisionerType` enum |
| `api/init_operations.py` | Step 5 (`_step_service_binaries()`) delegates to `BinaryService.extract_service_binaries()` |
| `core/binary/_service.py` | Added `extract_service_binaries()` and `_get_embedded_path()` — handles combined multidist binary + symlinks |
| `core/host/_service.py` | `_generate_sudoers_content()` includes `PRIVILEGED_SERVICE_BINARIES` paths resolved at runtime |
| `constants.py` | Added `SERVICE_BINARY_NAMES` and `PRIVILEGED_SERVICE_BINARIES` |
| `scripts/build_services.py` | **New** — multidist Nuitka build for combined `mvm-services` + main mvm |

## Risks

| Risk | Mitigation |
|------|-----------|
| Loop device exhaustion | Fall back to guestfs. Linux default is 256 loop devices. |
| Orphaned mounts on crash | Binary always `umount` + `losetup -d` in `finally`. |
| nocloud-server port conflict | Manager scans ports 8000-9000 (already implemented). |
| Binary version mismatch | Single distribution, embedded together, extracted on first use. Always in sync. |
| sudoers file management | Written by `sudo mvm host init`. Remove via `sudo rm /etc/sudoers.d/mvm-provision`. |
| btrfs non-default subvolume | Current assumption: `@` is default subvolume. Future: add `--subvol` flag to binary. |
| `systemctl enable` in chroot | Works correctly — only creates symlinks, no systemd daemon needed. |
