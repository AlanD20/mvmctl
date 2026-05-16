# mvmctl Backends

## Overview

mvmctl uses several backend systems for provisioning, infrastructure, and service management. This document describes each backend, its purpose, when it's used, and how to choose between alternatives.

---

## 1. Provisioning Backends

Provisioning backends handle root filesystem operations: resizing, SSH key injection, hostname setup, DNS configuration, cloud-init disable/enable, and other rootfs modifications.

### 1.1 Loop-Mount Backend (Primary, ~200ms)

- **Factory name:** `ProvisionerType.LOOP_MOUNT`
- **Binary:** `mvm-provision` (symlink to combined `mvm-services` multidist binary)
- **Architecture:**
  ```
  VMProvisioner → ProvisionerBackend.get_vm() → _LoopMountBackend → LoopMountProvisioner → LoopMountManager → mvm-provision binary (subprocess via sudo -n)
  ```
- **Speed:** ~200ms per VM (full provisioning: SSH keys, DNS, hostname, resize)
- **Dependencies:** `losetup`, `mount`, `umount`, `blkid`, `chroot`, `resize2fs`, `e2fsck`, `tune2fs`, `btrfs` (stdlib-only Python in the compiled binary)
- **Sudo:** Requires passwordless sudo for `mvm-provision` (via `mvm host init`)
- **Communication:** JSON operation list on stdin, JSON results on stdout
- **Capabilities:**
  - Resize ext4 and btrfs filesystems (grow and shrink)
  - Set hostname + update /etc/hosts
  - Inject DNS resolver (/etc/resolv.conf)
  - Setup SSH authorized keys + generate host keys
  - Disable cloud-init datasources and mask services
  - Inject cloud-init seed directory (copy directory tree into rootfs)
  - OS detection (/etc/os-release parsing)
  - Deblob (clean package caches) + fix fstab (PARTUUID → /dev/vda)
  - Write arbitrary files with mode/uid/gid (base64-encoded content)
  - Execute chroot commands (tries multiple shell paths: /bin/sh, /bin/bash, /bin/dash, /bin/ash, /usr/bin/sh, /usr/bin/bash, /bin/busybox, /usr/bin/busybox)
- **Binary Flow:**
  ```
  1. truncate file (pre-loop for grow resize)
  2. losetup -f -P --show <image>          # Set up loop with partition scanning
  3. Detect root partition                  # p1/p2/largest Linux fs/raw device
  4. Detect filesystem type via blkid       # fallback: ext4
  5. mount <root_part> <mount_point>
  6. Write all files with base64 decode, correct mode/uid/gid
  7. Copy directories from host to guest (recursive os.walk)
  8. chroot <mount_point> for each command  # tries multiple shell paths
  9. Post-mount resize:
     - ext4 shrink: e2fsck + resize2fs -M → truncate
     - ext4 grow: e2fsck + resize2fs
     - btrfs grow: btrfs filesystem resize max
     - btrfs shrink: fstrim + btrfs filesystem resize → truncate
  10. umount + losetup -d (always via finally)
  ```
- **Files:**
  - `core/_shared/_loopmount/_provisioner.py` — `LoopMountProvisioner` (queues operations, serializes to JSON)
  - `core/_shared/_loopmount/_manager.py` — `LoopMountManager` (binary resolution, JSON subprocess execution)
  - `services/loopmount/process.py` — Standalone binary entry point (stdlib only, no mvmctl imports)
  - `core/_shared/_provisioner/_backend.py` — `_LoopMountBackend` (adapter, delegates to `LoopMountProvisioner`)
  - `core/_shared/_provisioner/_content.py` — `ProvisionerContent` (shared operation builders)

### 1.2 GuestFS Backend (Opt-in, ~2600ms)

- **Factory name:** `ProvisionerType.GUESTFS`
- **Binary:** libguestfs Python module + supermin QEMU appliance
- **Architecture:**
  ```
  VMProvisioner → ProvisionerBackend.get_vm() → _GuestfsBackend → GuestfsProvisioner → libguestfs (QEMU appliance)
  ```
- **Speed:** ~2600ms per VM (QEMU appliance launch is the dominant cost)
- **Dependencies:** `python3-libguestfs` (system package, not on PyPI), `supermin`, `qemu`, libguestfs fixed appliance
- **Sudo:** Requires passwordless sudo for `supermin`
- **Used when:** GuestFS is enabled via the `guestfs_enabled` setting (opt-in). Falls back to loop-mount when GuestFS is not enabled.
- **Capabilities:** Same as Loop-Mount (same operations via different mechanism)
- **Key Differences from Loop-Mount:**
  - Uses `libguestfs` Python API instead of JSON subprocess protocol
  - OS detection reads `/etc/os-release` via guestfs `read_file()` instead of `chroot`
  - SSH setup is more elaborate (detects init system: systemd/OpenRC/sysvinit)
  - Supports user creation (passwd/shadow/sudoers) instead of only root
  - Cloud-init inject copies files via guestfs `write()` instead of directory copy
  - Shrink uses `zero_free_space()` + `e2fsck` + `resize2fs_size()` with safety margin
  - Deblob/fstab-fix uses `ProvisionerContent` builders (shared code with loop-mount)
- **Appliance Management:**
  - Fixed appliance built by `GuestfsService.build_appliance()` via `libguestfs-make-fixed-appliance`
  - Cached at `~/.cache/mvmctl/appliance/` (requires `kernel`, `initrd`, `root` files)
  - `KernelDetector.find_best_kernel()` selects a kernel with virtio drivers for appliance build
  - Stale state cleanup: orphaned QEMU processes, lock files, daemon sockets, cached appliances
  - Pruning via `GuestfsService.prune_appliance()` (called by `mvm cache prune misc`)
- **Files:**
  - `core/_shared/_guestfs/_provisioner.py` — `GuestfsProvisioner` (all rootfs operations via guestfs API)
  - `core/_shared/_guestfs/_base.py` — `OptimizedGuestfs` (low-level wrapper: handle creation, mount, partition extraction)
  - `core/_shared/_guestfs/_service.py` — `GuestfsService` (appliance building, stale state cleanup)
  - `core/_shared/_guestfs/_kernel_detector.py` — `KernelDetector` (finds suitable appliance kernel)
  - `core/_shared/_provisioner/_backend.py` — `_GuestfsBackend` (adapter)

### 1.3 Backend Selection

The `ProvisionerBackend` factory selects the backend based on `ProvisionerType`:

```python
from mvmctl.models.provisioner import ProvisionerType
from mvmctl.core._shared._provisioner._backend import ProvisionerBackend

# VM provisioning
backend = ProvisionerBackend.get_vm(
    rootfs_path=...,
    provisioner_type=ProvisionerType.LOOP_MOUNT,  # or GUESTFS
    fs_type="ext4",
)

# Image optimization (partition extraction, shrinking)
backend = ProvisionerBackend.get_image(
    image_path=...,
    provisioner_type=ProvisionerType.LOOP_MOUNT,
)
```

The `VMProvisioner` class in `core/vm/_provisioner.py` wraps the backend with a unified builder API:

```python
from mvmctl.core.vm._provisioner import VMProvisioner

p = VMProvisioner(rootfs_path=..., provisioner_type=..., fs_type="ext4")
p.resize(target_size_bytes)
p.set_hostname("my-vm")
p.setup_ssh("root", ["ssh-ed25519 AAA..."])
p.inject_dns(dns_server="1.1.1.1")
p.disable_cloud_init()
p.run()  # Execute all queued operations in a single session
```

### 1.4 Shared Provisioner Content

Both backends share provisioning operation definitions via `ProvisionerContent` in `core/_shared/_provisioner/_content.py`:

| Content Builder | Purpose |
|----------------|---------|
| `build_hostname_ops(hostname)` | /etc/hostname + /etc/hosts entries |
| `build_dns_ops(dns_server)` | /etc/resolv.conf with nameserver |
| `build_ssh_ops(user, pubkeys)` | Authorized keys, sshd config, first-boot installer, host key generator |
| `build_cloud_init_disable_ops()` | Datasource blocking + service masking |
| `build_cloud_init_inject_ops(dir)` | Copy cloud-init seed directory tree |
| `build_resize_ops(target_size)` | Grow filesystem to target size |
| `build_shrink_ops(limit_bytes)` | Shrink filesystem to minimum (0) or limit bytes |
| `build_deblob_ops(os_type)` | OS-specific cache cleanup (apt, yum, apk, pacman) |
| `build_fix_fstab_ops()` | PARTUUID → /dev/vda in /etc/fstab |

The `_LoopMountBackend` and `_GuestfsBackend` each consume these same builders but execute them differently (JSON subprocess vs. guestfs Python API).

---

## 2. Cloud-Init Modes

Cloud-init provisioning has four modes (ordered from most to least integrated):

| Mode | Flag | Mechanism | Speed | Use Case |
|------|------|-----------|-------|----------|
| **inject** | `--cloud-init-mode inject` | Direct injection into rootfs via loop-mount provisioner (guestfs fallback) | ~200ms | Primary mode — no external dependencies, files persistent in rootfs |
| **net** | `--cloud-init-mode net` | HTTP server (nocloud-net datasource) | ~50ms | Dynamic cloud-init, no rootfs modification required |
| **iso** | `--cloud-init-mode iso` | Cloud-init seed ISO via `cloud-localds` | ~500ms | Legacy mode for specific images that require ISO datasource |
| **off** | `--cloud-init-mode off` | No cloud-init (datasources blocked, services masked) | 0ms | Minimal VM, no provisioning needed |

### Inject Mode Architecture

```
VMOperation.create()
  → CloudInitProvisioner.prepare()
    → Generate user-data, meta-data, network-config
    → CloudInitManager.write_config_files()
    → VMProvisioner.inject_cloud_init(cloud_init_dir)
      → _LoopMountBackend.inject_cloud_init()
        → LoopMountProvisioner.inject_cloud_init(dir)
          → copies files into rootfs via mvm-provision binary
  → Continue with VM boot (files are baked into rootfs)
```

### Net Mode Architecture

```
VMOperation.create()
  → CloudInitProvisioner.prepare()
    → Generate user-data, meta-data, network-config
    → CloudInitManager.write_config_files()
    → NoCloudNetServerManager.start()
      → Validates port availability (8000-9000 range)
      → Spawns nocloud_server/process.py (HTTP server subprocess)
      → Binds to bridge gateway IP (never 0.0.0.0)
      → Adds iptables rule allowing VM access (MVM_NOCLOUD_NET_INPUT_CHAIN)
  → VM boots with ds=nocloud-net kernel parameter
  → Server stays running for the lifetime of the VM (stopped on VM removal)
```

### ISO Mode Architecture

```
VMOperation.create()
  → CloudInitProvisioner.prepare()
    → Generate user-data, meta-data, network-config
    → CloudInitManager.write_config_files()
    → CloudInitManager.create_seed_iso() (via cloud-localds subprocess)
  → ISO attached as secondary drive to Firecracker VM
  → VM boots, cloud-init reads from ISO datasource
```

### Off Mode Architecture

```
VMOperation.create()
  → CloudInitProvisioner.prepare()
    → Returns CloudInitProvisionResult(mode=CloudInitMode.OFF)
    → VMProvisioner.disable_cloud_init() called separately
      → Writes datasource block file + masks services in rootfs
```

### Files

- `core/cloudinit/_provisioner.py` — `CloudInitProvisioner` (generates configs, routes to mode-specific provisioning)
- `core/cloudinit/_manager.py` — `CloudInitManager` (writes config files, creates seed ISO)
- `services/nocloud_server/manager.py` — `NoCloudNetServerManager` (HTTP server lifecycle)
- `services/nocloud_server/process.py` — HTTP server subprocess (stdlib-only, `SimpleHTTPRequestHandler`)

---

## 3. Service Backends

Long-running subprocess services managed by the core layer. All three are compiled into a single multidist `mvm-services` binary and extracted via `mvm init`.

### 3.1 mvm-services Combined Binary

All three service binaries are compiled into a **single combined Nuitka multidist binary** (`mvm-services`). At runtime, `argv[0]` determines which `main()` entry point is dispatched:

| Symlink | Entry Point | Runs As | Purpose |
|---------|-------------|---------|---------|
| `mvm-console-relay → mvm-services` | `console_relay.process.main()` | user | PTY-to-socket relay for serial console |
| `mvm-nocloud-server → mvm-services` | `nocloud_server.process.main()` | user | HTTP server for cloud-init nocloud-net |
| `mvm-provision → mvm-services` | `loopmount.process.main()` | **root** (sudo -n) | Loop-mount rootfs provisioning |

**Binary-first Fallback Pattern** — Every manager tries the compiled binary first, falls back to a development-mode invocation:

| Service | Compiled | Dev fallback |
|---------|----------|-------------|
| console-relay | `mvm-console-relay` | `sys.executable -m mvmctl.services.console_relay.process` |
| nocloud-server | `mvm-nocloud-server` | `sys.executable -m mvmctl.services.nocloud_server.process` |
| mvm-provision | `mvm-provision` (via `sudo -n`) | `sudo -n sys.executable <path>/services/loopmount/process.py` |

```python
bin_dir = CacheUtils.get_bin_dir()
binary = bin_dir / "mvm-<service-name>"
if binary.exists():
    cmd = [str(binary), ...]        # Compiled mode
else:
    cmd = [...]                     # Dev mode (varies per service)
```

### 3.2 Console Relay

- **Binary symlink:** `mvm-console-relay` → `mvm-services`
- **Purpose:** PTY-to-socket relay for interactive serial console — reads from PTY master fd, forwards to both a Unix socket (for CLI attachment) and a log file
- **Files:**
  - `services/console_relay/manager.py` — `ConsoleRelayManager` (lifecycle: start/stop/terminate)
  - `services/console_relay/process.py` — PTY relay subprocess (stdlib-only)
  - `services/console_relay/client.py` — `ConsoleRelayClient` (socket connection for CLI)
- **Architecture:**
  ```
  ConsoleController → ConsoleRelayManager → spawns → console_relay/process.py
                                                                     │
                                             PTY master fd ← → Unix socket ← → Client (CLI)
                                                                     │
                                                               console.log
  ```
- **Speed:** Real-time (`select.select()` multiplexing between PTY, socket, and log file)
- **Signals:** SIGTERM/SIGINT for graceful shutdown (PID file + socket cleanup)
- **PID file:** `$MVM_CACHE_DIR/vms/<vm-id>/console.pid`
- **Socket:** `$MVM_CACHE_DIR/vms/<vm-id>/console.sock`
- **Log:** `$MVM_CACHE_DIR/vms/<vm-id>/firecracker.console.log`

### 3.3 NoCloud Server

- **Binary symlink:** `mvm-nocloud-server` → `mvm-services`
- **Purpose:** HTTP server serving cloud-init meta-data/user-data/network-config to VMs
- **Files:**
  - `services/nocloud_server/manager.py` — `NoCloudNetServerManager` (lifecycle: start/stop/terminate, port allocation, orphan cleanup)
  - `services/nocloud_server/process.py` — HTTP server subprocess (stdlib-only, `HTTPServer` + `SimpleHTTPRequestHandler`)
- **Architecture:**
  ```
  CloudInitProvisioner → NoCloudNetServerManager → spawns → nocloud_server/process.py
                                                                             │
                                                 HTTPServer(bind=gateway_ip) ← → VM guest (HTTP)
                                                                     │
                                                               serves: meta-data
                                                                      user-data
                                                                      network-config
  ```
- **Port range:** 8000–9000 (auto-allocated via `socket.bind()` test)
- **Security:** Binds to bridge gateway IP only (never `0.0.0.0`), iptables firewall rule in `MVM_NOCLOUD_NET_INPUT_CHAIN`
- **PID file:** `$MVM_CACHE_DIR/vms/<vm-id>/nocloud-server.pid`
- **Headers:** Cache-disabling headers (`Cache-Control: no-cache, no-store, must-revalidate`)

### 3.4 Loop-Mount Provisioner Service

- **Binary symlink:** `mvm-provision` → `mvm-services`
- **Purpose:** Rootfs provisioning via loop-mount (SSH keys, hostname, DNS, resize, cloud-init inject)
- **Files:**
  - `services/loopmount/process.py` — `Provisioner` class + `main()` entry point (stdlib-only)
  - `services/loopmount/__init__.py` — Package marker
- **Architecture:**
  ```
  VMProvisioner → ProvisionerBackend → _LoopMountBackend → LoopMountProvisioner → LoopMountManager
                                                                                         │
                                                                                  sudo -n mvm-provision
                                                                                         │
                                                     JSON ops stdin → losetup/mount/chroot → JSON results stdout
  ```
- **Communication:** Receives JSON operation list on stdin, writes JSON results to stdout
- **Timeout:** 60 seconds (configurable via `LOOP_MOUNT_TIMEOUT` Python constant in `core/_shared/_loopmount/_manager.py`)
- **Sudo:** Requires passwordless sudo via `/etc/sudoers.d/mvm-provision` drop-in
- **Speed:** ~200ms per VM (full provisioning: SSH + DNS + hostname + resize)

### 3.5 Service Lifecycle

| Phase | Action | Component |
|-------|--------|-----------|
| **Init** | Extract combined `mvm-services` binary + create symlinks | `BinaryService.extract_service_binaries()` |
| **Init** | Create sudoers drop-in for `mvm-provision` | `HostService._generate_sudoers_content()` |
| **Create VM** | Start NoCloud server (net mode) or inject cloud-init (inject mode) | `NoCloudNetServerManager.start()` / `VMProvisioner` |
| **Create VM** | Start console relay (unless `--no-console`) | `ConsoleRelayManager.start()` |
| **Runtime** | Provision rootfs via loop-mount or guestfs | `VMProvisioner.run()` |
| **Remove VM** | Stop console relay + NoCloud server + clean iptables rules | `ConsoleRelayManager.stop()`, `NoCloudNetServerManager.stop()` |
| **Cache prune** | Clean up stale PID files + orphan processes | `cleanup_orphans()` methods |

---

## 4. Firewall Backends

mvmctl supports two firewall backends for NAT, forwarding rules, and nocloud-net access control:

| Backend | Default | Files |
|---------|---------|-------|
| **nftables** | **Yes** (`firewall_backend: nftables`) | `core/_shared/_nftables_tracker/` (tracker, repository, resolver) |
| **iptables** | Opt-in (`firewall_backend: iptables`) | `core/_shared/_iptables_tracker/` (tracker, repository, resolver) |

A unified `FirewallTracker` in `core/_shared/_firewall_tracker.py` delegates to the active backend, selected via the `firewall_backend` setting. The default is `nftables`. The nftables backend uses non-hook chains with jump rules at position 0 of the system `ip filter`/`ip nat` tables, ensuring `accept` verdicts are terminal within the table — matching the behavior users expect from iptables.

---

## 5. Selection Guide

| Scenario | Recommended Backend | Rationale |
|----------|-------------------|-----------|
| Normal VM creation (default) | Loop-Mount provisioning + inject cloud-init | Fastest path (~200ms provisioning, no external deps) |
| No sudo for mvm-provision | GuestFS provisioning | Falls back automatically if `mvm-provision` binary unavailable |
| Minimal VM, no customization | Off mode cloud-init | Fastest boot, no provisioning at all |
| Dynamic cloud-init needed | Net mode cloud-init | Config served over network, no rootfs modification |
| Custom ISO required | ISO mode cloud-init | Use pre-built cloud-init ISO via `cloud-localds` |
| Console access needed | Console Relay | Automatically started (disable with `--no-console`) |
| Development environment | Loop-Mount (dev fallback) | Falls back to running via `sys.executable` when binary not extracted |
| Image optimization | Loop-Mount partition extraction | ~500ms vs ~2000ms for guestfs extraction |

---

## 6. Performance Comparison

| Operation | Loop-Mount | GuestFS | Improvement |
|-----------|-----------|---------|-------------|
| Full provisioning (SSH + DNS + hostname + resize) | ~200ms | ~2600ms | **13x faster** |
| Partition extraction | ~500ms | ~2000ms | **4x faster** |
| Filesystem grow (e.g., 3GB → 8GB) | ~50ms | ~1000ms | **20x faster** |
| Filesystem shrink (ext4) | ~100ms | ~1500ms | **15x faster** |
| Filesystem shrink (btrfs) | ~200ms | ~1500ms | **7.5x faster** |
| Console relay startup | ~10ms | N/A | — |
| NoCloud server startup | ~50ms | N/A | — |
| OS detection | ~100ms | ~600ms | **6x faster** |

The loop-mount backend is the default and preferred path. GuestFS is an opt-in alternative enabled via the `guestfs_enabled` setting.

---

## 7. Binary Embedding & Build

All service binaries are compiled into a single `mvm-services` binary via Nuitka's multidist feature:

```bash
python scripts/build_services.py                    # Build everything (release, default)
python scripts/build_services.py --fast             # Development build — minimal flags, no optimization
python scripts/build_services.py --release          # Release build — Nuitka with LTO, tree-shaking, minimal size
```

The build script creates temporary symlinks in `build/symlinks/` (since all three sources are named `process.py`) and compiles them into a single `mvm-services` binary. The main `mvm` binary embeds this via `--include-data-dir`.

### Extraction (mvm init, Step 5)

`extract_service_binaries()` handles three scenarios:

1. **Compiled mode (Nuitka build):** The combined binary is embedded via `--include-data-dir`. It is copied from the embedded path to the cache `bin/` directory.
2. **Dev mode with prior build:** If running via `uv run` without embedding, the method falls back to `dist/services/mvm-services` (created by `scripts/build_services.py`).
3. **Neither:** If the binary is neither embedded nor built, extraction is skipped with a debug log. Service processes fall back to `sys.executable -m ...` at runtime.

Symlinks are always freshly created — the old symlink is removed first with `missing_ok=True`:

```python
# In core/binary/_service.py:
combined_src = BinaryService._get_embedded_path("mvm-services")

# Step 1: Copy the combined binary — compiled mode (embedded) vs dev mode (dist/services/)
if combined_src is not None:
    combined_dest.unlink(missing_ok=True)
    shutil.copy2(str(combined_src), str(combined_dest))
    combined_dest.chmod(0o755)
else:
    dev_src = Path("dist/services") / "mvm-services"
    if dev_src.exists():
        combined_dest.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(str(dev_src), str(combined_dest))
        combined_dest.chmod(0o755)
    else:
        logger.debug(
            "Combined service binary not found at dist/services/mvm-services, "
            "skipping copy (dev mode without build), run scripts/build_services.py"
        )

# Step 2: Always recreate symlinks (works in compiled and dev mode)
for name in SERVICE_BINARY_NAMES:
    link_path = bin_dir / name
    link_path.unlink(missing_ok=True)
    link_path.symlink_to("mvm-services")
```

### Sudoers

Only `mvm-provision` requires passwordless sudo. Managed by `HostService._generate_sudoers_content()` which resolves paths via `CacheUtils.get_bin_dir()` and creates a drop-in at `/etc/sudoers.d/mvm-provision`.

---

## 8. Architecture Diagram

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           CLI Layer (Click + Typer)                          │
│  mvm vm create -n my-vm --cloud-init-mode inject                            │
└───────────────────────────┬─────────────────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                           API Layer (orchestration)                          │
│  VMOperation.create()                                                       │
│    ├── CloudInitProvisioner.provision()  (prepare configs)                  │
│    ├── VMProvisioner (select backend via ProvisionerType)                   │
│    │   ├── LOOP_MOUNT → _LoopMountBackend → LoopMountProvisioner            │
│    │   │   └── LoopMountManager → sudo -n mvm-provision                     │
│    │   │       ├── losetup → mount → write files → chroot → resize → umount │
│    │   │       └── JSON in/out                                              │
│    │   └── GUESTFS   → _GuestfsBackend → GuestfsProvisioner                 │
│    │       └── OptimizedGuestfs → libguestfs (QEMU appliance)              │
│    ├── ConsoleRelayManager.start()  (unless --no-console)                   │
│    └── NoCloudNetServerManager.start()  (if net mode)                      │
└───────────────────────────┬─────────────────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                        Core Layer (isolated domains)                        │
│                                                                             │
│  core/vm/          VMController, VMService, VMRepository                    │
│  core/network/     NetworkController, NetworkService, NetworkRepository     │
│  core/volume/      VolumeController, VolumeService, VolumeRepository        │
│  core/cloudinit/   CloudInitProvisioner, CloudInitManager                   │
│  core/_shared/_loopmount/   LoopMountProvisioner, LoopMountManager          │
│  core/_shared/_guestfs/     GuestfsProvisioner, OptimizedGuestfs, GuestfsService │
│  core/_shared/_provisioner/ ProvisionerBackend, ProvisionerContent           │
└───────────────────────────┬─────────────────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                      Service Layer (subprocess binaries)                    │
│                                                                             │
│  mvm-services (combined multidist binary)                                   │
│    ├── mvm-console-relay   → console_relay/process.py                      │
│    ├── mvm-nocloud-server  → nocloud_server/process.py                     │
│    └── mvm-provision       → loopmount/process.py                          │
└─────────────────────────────────────────────────────────────────────────────┘
```
