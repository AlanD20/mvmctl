# mvmctl Backends

## Overview

mvmctl uses several backend systems for provisioning, infrastructure, and service management. This document describes each backend, its purpose, when it's used, and how to choose between alternatives.

## Table of Contents

- [Overview](#overview)
- [1. Provisioning Backends](#1-provisioning-backends)
- [2. Cloud-Init Modes](#2-cloud-init-modes)
- [3. Service Backends](#3-service-backends)
- [4. Firewall Backends](#4-firewall-backends)
- [5. Selection Guide](#5-selection-guide)
- [6. Performance Comparison](#6-performance-comparison)
- [7. Binary Embedding & Build](#7-binary-embedding--build)
- [8. Architecture Diagram](#8-architecture-diagram)

---

## 1. Provisioning Backends

Provisioning backends handle root filesystem operations: resizing, SSH key injection, hostname setup, DNS configuration, cloud-init disable/enable, and other rootfs modifications.

### 1.1 Loop-Mount Backend (Primary, ~200ms)

- **Type:** `model.ProvisionerTypeLoopMount`
- **Entry:** `mvm run provision` (compiled into the same `mvm` binary)
- **Architecture:**
  ```
  api.Operation.VMCreate() → vm.Service → provisioner.Run() → system.SpawnService("mvm", "run", "provision") → losetup/mount/chroot
  ```
- **Speed:** ~1.2s average end-to-end creation (provisioning + Firecracker boot), ~2.8s to SSH-ready (benchmark data)
- **Dependencies:** `losetup`, `mount`, `umount`, `blkid`, `blockdev`, `chroot`, `resize2fs`, `e2fsck`, `tune2fs`, `fstrim`, `btrfs`
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
8. chroot <mount_point> for each command  # tries multiple shell paths (60s timeout each)
9. Post-mount resize:
   - ext4 shrink: e2fsck + resize2fs -M → truncate
   - ext4 grow: e2fsck + resize2fs
   - btrfs grow: btrfs filesystem resize max
   - btrfs shrink: fstrim + btrfs filesystem resize → truncate
10. umount + losetup -d (always via finally)
```
- **Files:**
  - `internal/service/loopmount/provisioner.go` — Provisioning engine (losetup/mount/chroot)
  - `internal/service/loopmount/entry.go` — Config struct + `Run()` entry point
  - `internal/service/loopmount/spawn.go` — `Spawn()` via `system.SpawnService()`
  - `internal/service/loopmount/wire.go` — JSON wire protocol types
  - `internal/lib/provisioner/backend.go` — Backend interface
  - `internal/infra/provcontent/content.go` — Shared provisioning content builders

### 1.2 GuestFS Backend (Opt-in, ~2600ms)

- **Type:** `model.ProvisionerTypeGuestFS`
- **Binary:** libguestfs Go bindings + supermin QEMU appliance
- **Architecture:**
  ```
  api.Operation.VMCreate() → vm.Service → provisioner.Run() → guestfs.Backend → libguestfs (QEMU appliance)
  ```
- **Speed:** ~3.9s average end-to-end creation, ~5.8s to SSH-ready (benchmark data — exceeds 6s threshold on most images)
- **Dependencies:** `libguestfs` (system package), `supermin`, `qemu`, libguestfs fixed appliance
- **Sudo:** Requires passwordless sudo for `supermin`
- **Used when:** GuestFS is enabled via the `guestfs_enabled` setting (opt-in). Loop-mount is the default when GuestFS is not enabled.
- **Capabilities:** Same as Loop-Mount (same operations via different mechanism)
- **Key Differences from Loop-Mount:**
  - Uses `libguestfs` API instead of JSON subprocess protocol
  - OS detection reads `/etc/os-release` via guestfs `ReadFile()` instead of `chroot`
  - SSH setup detects init system: systemd/OpenRC/sysvinit
  - Supports user creation via `useradd` (with sudoers drop-in) instead of only root
  - Cloud-init inject copies files via guestfish `upload` command instead of directory copy
  - Shrink uses `ZeroFreeSpace()` + `e2fsck` + `Resize2fsSize()` with safety margin
  - Deblob uses shared `ProvisionerContent` builders for SSH config and first-boot scripts; fstab fix is inline in guestfs scripts
- **Appliance Management:**
  - Fixed appliance built by `guestfs.BuildAppliance()` via `libguestfs-make-fixed-appliance`
  - Cached at `~/.cache/mvmctl/appliance/` (requires `kernel`, `initrd`, `root` files)
  - `guestfs.KernelDetector.FindBestKernel()` selects a kernel with virtio drivers for appliance build
  - Stale state cleanup: orphaned QEMU processes, lock files, daemon sockets, cached appliances
  - Pruning via `guestfs.PruneAppliance()` (called by `mvm cache prune misc`)
- **Files:**
  - `internal/lib/provisioner/guestfs/provisioner.go` — All rootfs operations via guestfs API
  - `internal/lib/provisioner/guestfs/base.go` — Low-level wrapper: handle creation, mount, partition extraction
  - `internal/lib/provisioner/guestfs/utils.go` — Appliance building, stale state cleanup
  - `internal/lib/provisioner/guestfs/kernel_detector.go` — Finds suitable appliance kernel

### 1.3 Backend Selection

The provisioner backend is selected once at startup in `api.NewOperation()` by reading the `guestfs_enabled` setting:

```go
// In pkg/api/operation.go NewOperation():
op.ProvisionerType = provisioner.ResolveProvisionerType(settings.GuestfsEnabled)
```

All callers use `op.ProvisionerType` directly. The `Provisioner` struct in `internal/lib/provisioner/` wraps the backend with a unified interface.

### 1.4 Shared Provisioner Content

Both backends share provisioning operation definitions via `ProvisionerContent` in `internal/infra/provcontent/content.go`:

| Content Builder | Purpose |
|----------------|---------|
| `BuildHostnameOps(hostname)` | /etc/hostname + /etc/hosts entries |
| `BuildDNSOps(dnsServer)` | /etc/resolv.conf with nameserver |
| `BuildSSHOps(user, sshPubkeys)` | Authorized keys and user account setup |
| `BuildCloudInitDisableOps()` | Datasource blocking + service masking |
| `BuildCloudInitInjectOps(cloudInitDir)` | Copy cloud-init seed directory tree |
| `BuildResizeOps(targetSizeBytes)` | Grow filesystem to target size |
| `BuildShrinkOps(limitBytes)` | Shrink filesystem to minimum (pass 0 for minimum) |
| `BuildDeblobOps(osType)` | OS-specific cache cleanup (apt, yum, apk, pacman) |
| `BuildFixFstabOps()` | PARTUUID → /dev/vda in /etc/fstab |

The loop-mount and guestfs backends each consume these same builders but execute them differently (JSON subprocess vs. guestfs API).

---

## 2. Cloud-Init Modes

Cloud-init provisioning has four modes (ordered from most to least integrated):

| Mode | Flag | Mechanism | Speed | Use Case |
|------|------|-----------|-------|----------|
| **inject** | `--cloud-init-mode inject` | Direct injection into rootfs via loop-mount provisioner (or guestfs alternative) | ~200ms | Primary mode — no external dependencies, files persistent in rootfs |
| **net** | `--cloud-init-mode net` | HTTP server (nocloud-net datasource) | ~50ms | Dynamic cloud-init, no rootfs modification required |
| **iso** | `--cloud-init-mode iso` | Cloud-init seed ISO via `cloud-localds` | ~500ms | Legacy mode for specific images that require ISO datasource |
| **off** | `--cloud-init-mode off` | No cloud-init (datasources blocked, services masked) | 0ms | Minimal VM, no provisioning needed |

### Inject Mode Architecture

```
api.Operation.VMCreate()
  → cloudinit.Provisioner.Prepare()
    → Generate user-data, meta-data, network-config
    → cloudinit.Manager.WriteConfigFiles()
    → provisioner.InjectCloudInit(cloudInitDir)
      → loop-mount (or guestfs) backend copies files into rootfs
  → Continue with VM boot (files are baked into rootfs)
```

### Net Mode Architecture

```
api.Operation.VMCreate()
  → cloudinit.Provisioner.Prepare()
    → Generate user-data, meta-data, network-config
    → cloudinit.Manager.WriteConfigFiles()
    → nocloudnet.Spawn(ctx, cfg)
      → Validates port availability (8000-9000 range)
      → Spawns nocloud-net HTTP server via system.SpawnService()
      → Binds to bridge gateway IP (never 0.0.0.0)
      → Adds firewall rule allowing VM access (via the active backend — default nftables)
  → VM boots with ds=nocloud;seedfrom=http://<gateway>:<port>/ kernel parameter
  → Server stays running for the lifetime of the VM (stopped on VM removal)
```

### ISO Mode Architecture

```
api.Operation.VMCreate()
  → cloudinit.Provisioner.Prepare()
    → Generate user-data, meta-data, network-config
    → cloudinit.Manager.WriteConfigFiles()
    → cloudinit.Manager.CreateSeedISO() (via cloud-localds subprocess)
  → ISO attached as secondary drive to Firecracker VM
  → VM boots, cloud-init reads from ISO datasource
```

### Off Mode Architecture

```
api.Operation.VMCreate()
  → cloudinit.Provisioner.Prepare()
    → Returns CloudInitResult{Mode: CloudInitModeOff}
    → provisioner.DisableCloudInit() called separately
      → Writes datasource block file + masks services in rootfs
```

### Files

- `internal/core/cloudinit/provisioner.go` — Generates configs, routes to mode-specific provisioning
- `internal/core/cloudinit/manager.go` — Writes config files, creates seed ISO
- `internal/core/cloudinit/config.go` — Cloud-init provisioning parameters
- `internal/service/nocloudnet/entry.go` — HTTP server lifecycle
- `internal/service/nocloudnet/handler.go` — HTTP request handler

---

## 3. Service Backends

Long-running subprocess services compiled into the same `mvm` binary. Each service is
invoked via `mvm run <service>` and spawned in the background via `system.SpawnService()`.

### 3.1 Service Architecture

All three services are compiled into the **single `mvm` binary** — no separate multidist
binary. At runtime, `mvm run <service>` dispatches to the appropriate entry point:

| Service | Entry Point | Runs As | Purpose |
|---------|-------------|---------|---------|
| `mvm run console relay` | `console.Run(ctx, cfg)` | user | PTY-to-socket relay for serial console |
| `mvm run nocloudnet serve` | `nocloudnet.Run(ctx, cfg)` | user | HTTP server for cloud-init nocloud-net |
| `mvm run provision` | `loopmount.Run(ctx, cfg)` | **root** (sudo) | Loop-mount rootfs provisioning |

Each service follows a consistent three-function pattern:
- **`Config`** struct — holds all configuration for the service.
- **`Run(ctx, cfg)`** — runs the service in the foreground (blocking).
- **`Spawn(ctx, cfg, ...)`** — launches the service as a background subprocess via `system.SpawnService()`.

### 3.2 Console Relay

- **Entry:** `mvm run console`
- **Purpose:** PTY-to-socket relay for interactive serial console — reads from PTY master fd, forwards to both a Unix socket (for CLI attachment) and a log file
- **Files:**
  - `internal/service/console/entry.go` — Config struct + `Run()` entry point
  - `internal/service/console/spawn.go` — `Spawn()` via `system.SpawnService()`
  - `internal/service/console/relay.go` — PTY relay goroutine
  - `internal/service/console/client.go` — Console client for CLI attachment
- **Architecture:**
  ```
  console.Controller → console.Spawn(ctx, cfg) → system.SpawnService("mvm", "run", "console")
                                                                    │
                                              PTY master fd ← → Unix socket ← → Client (CLI)
                                                                    │
                                                              console.log
  ```
- **Speed:** Real-time (Go `select` + channels + `SetDeadline()` multiplexing between PTY, socket, and log file)
- **Signals:** SIGTERM/SIGINT for graceful shutdown (PID file + socket cleanup)
- **PID file:** `$MVM_CACHE_DIR/vms/<vm-id>/console.pid`
- **Socket:** `$MVM_CACHE_DIR/vms/<vm-id>/console.sock`
- **Log:** `$MVM_CACHE_DIR/vms/<vm-id>/firecracker.console.log`

### 3.3 NoCloud Server

- **Entry:** `mvm run nocloudnet`
- **Purpose:** HTTP server serving cloud-init meta-data/user-data/network-config to VMs
- **Files:**
  - `internal/service/nocloudnet/entry.go` — Config struct + `Run()` entry point
  - `internal/service/nocloudnet/spawn.go` — `Spawn()` via `system.SpawnService()`
  - `internal/service/nocloudnet/handler.go` — HTTP request handler
- **Architecture:**
  ```
  cloudinit.Provisioner → nocloudnet.Spawn(ctx, cfg) → system.SpawnService("mvm", "run", "nocloudnet")
                                                                          │
                                            HTTPServer(bind=gateway_ip:port) ← → VM guest (HTTP)
                                                                          │
                                                                    serves: meta-data
                                                                           user-data
                                                                           network-config
  ```
- **Port range:** 8000–9000 (auto-allocated via `socket.bind()` test)
- **Security:** Binds to bridge gateway IP only (never `0.0.0.0`), firewall rule in `MVM-NOCLOUDNET-INPUT` chain (via active backend)
- **PID file:** `$MVM_CACHE_DIR/vms/<vm-id>/nocloud-server.pid`
- **Headers:** Cache-disabling headers (`Cache-Control: no-cache, no-store, must-revalidate`)

### 3.4 Loop-Mount Provisioner Service

- **Entry:** `mvm run provision`
- **Purpose:** Rootfs provisioning via loop-mount (SSH keys, hostname, DNS, resize, cloud-init inject)
- **Files:**
  - `internal/service/loopmount/provisioner.go` — Provisioning engine (losetup/mount/chroot)
  - `internal/service/loopmount/entry.go` — Config struct + `Run()` entry point
  - `internal/service/loopmount/spawn.go` — `Spawn()` via `system.SpawnService()`
  - `internal/service/loopmount/wire.go` — JSON wire protocol types
- **Architecture:**
  ```
   vm.Service → provisioner.Run() → loopmount.Spawn(ctx, cfg, wireInput)
                                                        │
                                                 system.SpawnService("mvm", "run", "provision")
                                                        │
                                    JSON ops stdin → losetup/mount/chroot → JSON results stdout
  ```
- **Communication:** Receives JSON operation list on stdin, writes JSON results to stdout
- **Timeout:** 60 seconds per chroot command (hardcoded in `provisioner.go`)
- **Sudo:** Requires passwordless sudo via `/etc/sudoers.d/mvm-provision` drop-in
- **Speed:** ~1.2s average end-to-end (benchmark data)

### 3.5 Service Lifecycle

| Phase | Action | Component |
|-------|--------|-----------|
| **Init** | `mvm host init` creates sudoers drop-in, iptables chains, cache dirs | `host.Service.Init()` |
| **Create VM** | Start NoCloud server (net mode) or inject cloud-init (inject mode) | `nocloudnet.Spawn()` / `provisioner.InjectCloudInit()` |
| **Create VM** | Start console relay (unless `--no-console`) | `console.Spawn()` |
| **Runtime** | Provision rootfs via loop-mount or guestfs | `provisioner.Run()` (via `mvm run provision`) |
| **Remove VM** | Stop console relay + NoCloud server + clean firewall rules | `console.Stop()`, `nocloudnet.Stop()` |
| **Cache prune** | Clean up stale PID files + orphan processes | `cache.Service.Prune()` |

---

## 4. Firewall Backends

mvmctl supports two firewall backends for NAT, forwarding rules, and nocloud-net access control:

| Backend | Default | Files |
|---------|---------|-------|
| **nftables** | **Yes** (`firewall_backend: nftables`) | `internal/lib/firewall/tracker.go`, `nftables.go`, `nftables_repository.go` |
| **iptables** | Opt-in (`firewall_backend: iptables`) | `internal/lib/firewall/tracker.go`, `iptables.go`, `iptables_repository.go` |

A unified `FirewallTracker` in `internal/lib/firewall/tracker.go` delegates to the active backend, selected via the `firewall_backend` setting. The default is `nftables`. The nftables backend uses non-hook chains with jump rules at position 0 of the system `ip filter`/`ip nat` tables, ensuring `accept` verdicts are terminal within the table — matching the behavior users expect from iptables.

---

## 5. Selection Guide

| Scenario | Recommended Backend | Rationale |
|----------|-------------------|-----------|
| Normal VM creation (default) | Loop-Mount provisioning + inject cloud-init | Fastest path (~1.2s creation, ~2.8s to SSH) |
| No sudo for mvm-provision | GuestFS provisioning | Uses `guestfs_enabled` setting to opt-in (~3.9s creation) |
| Minimal VM, no customization | Off mode cloud-init | Fastest boot, no provisioning at all |
| Dynamic cloud-init needed | Net mode cloud-init | Config served over network, no rootfs modification |
| Custom ISO required | ISO mode cloud-init | Use pre-built cloud-init ISO via `cloud-localds` |
| Console access needed | Console Relay | Automatically started (disable with `--no-console`) |
| Development environment | Loop-Mount (direct) | Uses `mvm` binary directly, no separate service binaries |
| Image optimization | Loop-Mount partition extraction | ~500ms vs ~2000ms for guestfs extraction |

---

## 6. Performance Comparison

Benchmark data from `benchmarks/results.json` (2026-06-07, kernel official:7.0.11):

### End-to-End VM Creation (create_s — provisioning + Firecracker boot)

| Image | Go Loop-Mount | Python Loop-Mount | Go GuestFS | Python GuestFS |
|-------|--------------|-------------------|------------|----------------|
| alpine | 0.9s | 1.4s | 3.7s | 2.8s |
| ubuntu:24.04 | 1.4s | 2.2s | 4.0s | 3.4s |
| ubuntu-minimal:24.04 | 1.1s | 1.6s | 3.4s | 3.0s |
| archlinux | 1.5s | 2.1s | 4.0s | 3.4s |
| debian:12 | 1.5s | 2.1s | 4.0s | 4.6s |
| firecracker | 1.0s | 1.6s | 4.5s | 3.1s |
| **Average** | **1.2s** | **1.8s** | **3.9s** | **3.4s** |

### Full Lifecycle (total_s — create + SSH available)

| Image | Go Loop-Mount | Python Loop-Mount | Go GuestFS | Python GuestFS |
|-------|--------------|-------------------|------------|----------------|
| alpine | 2.0s | 1.8s | 4.8s | 3.2s |
| ubuntu:24.04 | 2.9s | 3.2s | 6.0s ❌ | 4.5s |
| ubuntu-minimal:24.04 | 2.3s | 2.2s | 6.5s ❌ | 3.6s |
| archlinux | 4.7s | 5.9s | 7.0s ❌ | 6.2s ❌ |
| debian:12 | 2.7s | 3.9s | 6.0s ❌ | 6.5s ❌ |
| firecracker | 2.2s | 2.1s | 6.6s ❌ | 3.7s |
| **Average** | **2.8s** | **3.2s** | **5.8s ❌** | **4.5s** |

❌ = exceeded 6s threshold

### Key Takeaways

- **Go loop-mount is the fastest path**: ~1.2s average creation, ~2.8s to SSH-ready
- **Go vs Python (loop-mount)**: Go is ~1.5x faster for creation, comparable for total lifecycle
- **GuestFS is 3x slower than loop-mount**: ~3.9s creation vs ~1.2s (Go), consistent across backends
- **Go GuestFS underperforms Python GuestFS**: Go guestfs averaged 5.8s total (5/6 failed threshold) vs Python's 4.5s — the Go guestfs integration needs optimization
- **Console relay startup**: ~10ms (negligible, no separate benchmark)
- **NoCloud server startup**: ~50ms (negligible, no separate benchmark)

---

## 7. Binary Build

The `mvm` binary is a standard Go binary that includes all services compiled in:

```bash
go build -o dist/mvm ./cmd/mvm
```

All three services (console relay, nocloud-net server, loopmount provisioner) are compiled
into the same binary. No separate service binaries, no symlinks, no extraction step.

### Service Invocation

Services are invoked via `mvm run <service>`:

```bash
mvm run console relay     # Start console relay
mvm run nocloudnet serve  # Start nocloud-net HTTP server
mvm run provision         # Start loopmount provisioner (requires sudo)
```

The CLI layer dispatches `mvm run <service>` to the appropriate service entry point.
Services are spawned in the background by the core layer via `system.SpawnService()`.

### Sudoers

Only `mvm run provision` requires passwordless sudo. Managed by `mvm host init` which
creates a drop-in at `/etc/sudoers.d/mvm` granting the `mvm` group passwordless sudo.

---

## 8. Architecture Diagram

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           CLI Layer (Cobra)                                  │
│  mvm vm create my-vm --cloud-init-mode inject                               │
└───────────────────────────┬─────────────────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                           API Layer (pkg/api)                                │
│  op.VMCreate(ctx, input)                                                    │
│    ├── cloudinit.Provisioner.Prepare()  (prepare configs)                   │
│    ├── provisioner.Run(ctx, provisionerType)  (select backend)              │
│    │   ├── LoopMount → loopmount.Spawn() → system.SpawnService("provision")│
│    │   │   └── losetup → mount → write files → chroot → resize → umount    │
│    │   └── GuestFS   → guestfs.Backend → libguestfs (QEMU appliance)       │
│    ├── console.Spawn()  (unless --no-console)                               │
│    └── nocloudnet.Spawn()  (if net mode)                                    │
└───────────────────────────┬─────────────────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                        Core Layer (internal/core)                            │
│                                                                             │
│  core/vm/          Controller, Service, Repository, SQLite, Resolver        │
│  core/network/     Controller, Service, Repository, SQLite, Resolver        │
│  core/volume/      Controller, Service, Repository, SQLite, Resolver        │
│  core/cloudinit/   Provisioner, Manager, Config                             │
│  core/image/       Controller, Service, Repository, SQLite, Resolver        │
│  core/kernel/      Controller, Service, Repository, SQLite, Resolver        │
│  core/host/        Controller, Service, Repository, SQLite, Detector, Probe │
│  core/config/      Service, Repository, SQLite, Constraints                 │
│  core/console/     Controller                                               │
│  core/logs/        Controller, Service                                      │
│  core/cache/       Service                                                  │
│  core/ssh/         Service, CP                                              │
│  core/binary/      Controller, Service, Repository, SQLite, Resolver        │
│  core/key/         Controller, Service, Repository, SQLite, Resolver        │
└───────────────────────────┬─────────────────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                      Service Layer (internal/service)                        │
│                                                                             │
│  All compiled into the single mvm binary                                    │
│    ├── mvm run console relay    → console relay (PTY proxy)                 │
│    ├── mvm run nocloudnet serve → nocloud-net HTTP server                   │
│    └── mvm run provision        → loopmount provisioner (requires sudo)     │
└─────────────────────────────────────────────────────────────────────────────┘
```
