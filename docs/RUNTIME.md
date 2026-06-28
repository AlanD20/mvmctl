# mvmctl Runtime Architecture

This document describes the runtime backend systems that execute provisioning,
cloud-init, firewall, and service operations during VM creation and management.
Contributors and advanced users should read this to understand which backend
runs when, why multiple backends exist for the same task, and how a `mvm vm create`
request flows through the system.

## Table of Contents

- [1. Provisioning Backends](#1-provisioning-backends)
  - [1.1 Loop-Mount Backend (Default)](#11-loop-mount-backend-default)
  - [1.2 GuestFS Backend (Opt-in)](#12-guestfs-backend-opt-in)
  - [1.3 Backend Selection](#13-backend-selection)
  - [1.4 Shared Provisioner Content](#14-shared-provisioner-content)
- [2. Cloud-Init Modes](#2-cloud-init-modes)
  - [Inject Mode](#inject-mode)
  - [Net Mode](#net-mode)
  - [ISO Mode](#iso-mode)
  - [Off Mode](#off-mode)
- [3. Service Backends](#3-service-backends)
  - [3.1 Service Architecture](#31-service-architecture)
  - [3.2 Console Relay](#32-console-relay)
  - [3.3 NoCloud Server](#33-nocloud-server)
  - [3.4 Loop-Mount Provisioner Service](#34-loop-mount-provisioner-service)
  - [3.5 Service Lifecycle](#35-service-lifecycle)
- [4. Firewall Backends](#4-firewall-backends)
- [5. Selection Guide](#5-selection-guide)
- [6. Performance Comparison](#6-performance-comparison)
  - [End-to-End VM Creation (create_s)](#end-to-end-vm-creation-create_s)
  - [Notes](#notes)
- [7. Binary Build](#7-binary-build)
  - [Service Invocation](#service-invocation)
  - [Sudoers](#sudoers)
- [8. Architecture Diagram](#8-architecture-diagram)

## 1. Provisioning Backends

A **provisioning backend** modifies a VM root filesystem before first boot. Every
backend supports the same set of operations: resizing, SSH key injection, hostname
setup, DNS configuration, cloud-init inject, OS detection, package cache cleanup
("deblob"), fstab fix (PARTUUID → /dev/vda), and arbitrary file writes.

Two backends implement this interface — **Loop-Mount** and **GuestFS**. They are
mutually exclusive: the provisioner type is selected once at startup by reading
the `guestfs_enabled` setting. Loop-Mount is the default (~2.3s average); GuestFS
is the opt-in alternative (~8.4s+).

### 1.1 Loop-Mount Backend (Default)

The loop-mount backend mounts the VM's root filesystem image via a loop device,
operates on it directly with host tools, and unmounts when done. It is the primary
backend because it requires no additional system packages beyond standard Linux
utilities (`losetup`, `mount`, `umount`, `blkid`, `resize2fs`, `chroot`).

**Flow:**

```
pkg/api/operation.go: VMCreate()
  → provisioner.NewBackend(opts)      # Creates LoopMountBackend
    → backend.Resize() / SetHostname() / ...  # Queues operations
    → backend.Run()
      → runWireOp() → system.DefaultRunner.Run(["sudo", "mvm", "run", "provision"])
        → JSON ops on stdin → losetup/mount/chroot → JSON results on stdout
```

The API layer (`pkg/api/vm.go`) calls `provisioner.NewBackend()` with `BackendOpts`
that include the rootfs image path and filesystem type. Each `backend.SetHostname()`,
`backend.SetupSSH()`, etc. call queues a provisioning operation. When `backend.Run()`
is called, all queued operations are serialized to JSON, piped to an `mvm run provision`
subprocess via stdin, and results are read from stdout.

**Execution steps inside the subprocess:**

1. `truncate` the image file (for grow resize operations)
2. `losetup -f -P --show <image>` — attach loop device with partition scanning
3. Detect root partition (first partition, second, largest Linux filesystem, or raw device)
4. Detect filesystem type via `blkid` (ext4 fallback)
5. `mount <root_partition> <mount_point>`
6. Write all queued files (base64-decoded content with correct mode/uid/gid)
7. Copy directories from host to mount point (recursive)
8. `chroot <mount_point>` for each shell command (tries multiple shell paths, 60s timeout)
9. Post-mount resize: `e2fsck` + `resize2fs` for ext4; `btrfs filesystem resize` for btrfs
10. `umount` + `losetup -d` (always via `finally`)

**Key files:**

| File | Purpose |
|------|---------|
| `internal/service/loopmount/provisioner.go` | Provisioning engine — losetup, mount, chroot, resize |
| `internal/service/loopmount/entry.go` | Config struct + `Run()` entry point |
| `internal/service/loopmount/spawn.go` | `Spawn()` via `system.SpawnService()` |
| `internal/service/loopmount/wire.go` | JSON wire protocol types |
| `internal/lib/provisioner/backend.go` | `Backend` interface |
| `internal/infra/provcontent/content.go` | Shared provisioning content builders |

**Boundary:** The loop-mount backend requires passwordless sudo (set up by `mvm host init`).
It does NOT support images with non-standard partition layouts or damaged filesystems
that `e2fsck` cannot repair.

### 1.2 GuestFS Backend (Opt-in)

The GuestFS backend uses `libguestfs` Go bindings to manipulate the root filesystem
through a QEMU appliance. It exists for environments where loop-mount is impractical:
systems without sudo access, or images requiring advanced filesystem features.

**Flow:**

```
pkg/api/operation.go: VMCreate()
  → provisioner.NewBackend(opts)
    → guestfs.EnsureAppliance(cacheDir)   # Build or verify cached appliance
    → NewGuestfsBackend(rootfsPath, uid, gid)
      → libguestfs API → QEMU appliance microVM
```

The guestfs backend launches a small QEMU microVM that mounts the target filesystem
and exposes `libguestfs` API methods. Operations are called directly via Go API calls,
not through a JSON subprocess protocol.

**Key differences from loop-mount:**
- OS detection reads `/etc/os-release` via guestfs `ReadFile()` instead of `chroot`
- SSH setup detects the init system: systemd, OpenRC, or sysvinit
- Supports user creation via `useradd` with sudoers drop-in
- Cloud-init inject uses guestfish `upload` instead of directory copy
- Shrink uses `ZeroFreeSpace()` + `e2fsck` + `Resize2fsSize()` with safety margin

**Appliance lifecycle:**
- Built once by `guestfs.BuildAppliance()` via `libguestfs-make-fixed-appliance`
- Cached at `~/.cache/mvmctl/appliance/` (kernel, initrd, root files)
- `guestfs.KernelDetector.FindBestKernel()` selects a kernel with virtio drivers
- Stale state cleanup handles orphaned QEMU processes, lock files, daemon sockets
- Pruned by `mvm cache prune misc` calling `guestfs.PruneAppliance()`

**Key files:**

| File | Purpose |
|------|---------|
| `internal/lib/provisioner/guestfs/provisioner.go` | Rootfs operations via guestfs API |
| `internal/lib/provisioner/guestfs/base.go` | Handle creation, mount, partition extraction |
| `internal/lib/provisioner/guestfs/utils.go` | Appliance building, stale state cleanup |
| `internal/lib/provisioner/guestfs/kernel_detector.go` | Suitable appliance kernel selection |

**Boundary:** GuestFS requires `libguestfs`, `supermin`, and `qemu` system packages.
The appliance build may fail or hang if no kernel with `CONFIG_VIRTIO_PCI` is available.
This backend is approximately 3-4x slower than loop-mount.

### 1.3 Backend Selection

The provisioner type is resolved once at application startup in `api.NewOperation()`:

```go
provisionerType := provisioner.ProvisionerLoopMount
guestfsEnabled, _ := s.Config.GetBool(ctx, "settings", "guestfs_enabled")
if guestfsEnabled {
    provisionerType = provisioner.ProvisionerGuestFS
}
```

All callers reference `op.ProvisionerType`. The `provisioner.NewBackend()` factory in
`internal/lib/provisioner/backend.go` dispatches to the correct implementation based on
this type. Resolution happens once — there is no backend fallback at runtime.

### 1.4 Shared Provisioner Content

Both backends consume the same operation builders from `internal/infra/provcontent/content.go`:

| Builder | Purpose |
|---------|---------|
| `BuildHostnameOps(hostname)` | `/etc/hostname` + `/etc/hosts` |
| `BuildDNSOps(dnsServer)` | `/etc/resolv.conf` nameserver entry |
| `BuildSSHOps(user, sshPubkeys)` | SSH authorized keys and user setup |
| `BuildCloudInitDisableOps()` | Datasource blocking + service masking |
| `BuildCloudInitInjectOps(cloudInitDir)` | Copy cloud-init seed directory |
| `BuildResizeOps(targetSizeBytes)` | Grow filesystem |
| `BuildShrinkOps(limitBytes)` | Shrink filesystem (pass `0` for minimum) |
| `BuildDeblobOps(osType)` | OS cache cleanup (apt, yum, apk, pacman) |
| `BuildFixFstabOps()` | PARTUUID → /dev/vda migration |

The builders produce a generic `Operation` list. Each backend consumes the same list
but executes it differently — loop-mount via JSON subprocess, guestfs via direct API calls.

---

## 2. Cloud-Init Modes

**Cloud-init** is the standard mechanism for provisioning cloud images on first boot.
mvmctl supports four delivery modes, each with different trade-offs:

| Mode | Flag | Mechanism | Latency | Use Case |
|------|------|-----------|---------|----------|
| **inject** | `--cloud-init-mode inject` | Direct rootfs injection via provisioner backend | ~200ms | Primary mode — no runtime dependencies |
| **net** | `--cloud-init-mode net` | HTTP server (nocloud-net datasource) | ~50ms | Dynamic config, no rootfs modification |
| **iso** | `--cloud-init-mode iso` | Seed ISO via `cloud-localds` | ~500ms | Legacy images requiring ISO datasource |
| **off** | `--cloud-init-mode off` | Datasources blocked, services masked | 0ms | No provisioning needed |

### Inject Mode

```
VMCreate()
  → cloudinit.Provisioner.Provision()
    → Generate user-data, meta-data, network-config
    → manager.WriteConfigFiles()
    → provisioner.InjectCloudInit(cloudInitDir)
      → Backend copies files into rootfs
  → VM boots with files baked in
```

Files are written directly into the root filesystem before first boot. The files
persist for the lifetime of the VM and survive reboots. This is the default mode
because it requires no network server and no ISO generation.

### Net Mode

```
VMCreate()
  → cloudinit.Provisioner.Provision()
    → Generate configs
    → nocloudnet.Spawn(ctx, cfg)
      → Find free port (8000-9000 range)
      → Spawn HTTP server via system.SpawnService()
      → Bind to bridge gateway IP only
      → Firewall rule in MVM-NOCLOUDNET-INPUT chain
  → VM boots with ds=nocloud;seedfrom=http://<gateway>:<port>/
  → Server runs until VM removal
```

The HTTP server responds to cloud-init metadata, user-data, and network-config
requests. It binds exclusively to the bridge gateway IP — never to `0.0.0.0` —
to prevent external network access. A firewall rule in the active backend
(nftables by default) allows the VM's source IP to reach the server.

### ISO Mode

```
VMCreate()
  → cloudinit.Provisioner.Provision()
    → Generate configs
    → manager.CreateSeedISO() via cloud-localds subprocess
  → ISO attached as secondary Firecracker drive
  → VM boots, cloud-init reads from ISO
```

ISO mode generates a cloud-init seed ISO using the `cloud-localds` tool and
attaches it as a secondary block device to the Firecracker VM. This mode is
necessary for images that do not support the nocloud-net datasource.

### Off Mode

```
VMCreate()
  → cloudinit.Provisioner.Provision()
    → Returns CloudInitResult{Mode: CloudInitModeOFF}
    → provisioner.DisableCloudInit()
      → Writes datasource block file + masks services
```

"Off" mode physically disables cloud-init inside the rootfs by writing datasource
block files and masking cloud-init systemd services. The VM boots without any
provisioning step.

**Key files:**

| File | Purpose |
|------|---------|
| `internal/core/cloudinit/provisioner.go` | Config generation, mode dispatch |
| `internal/core/cloudinit/manager.go` | Config file writing, seed ISO creation |
| `internal/core/cloudinit/config.go` | Provisioning parameters |
| `internal/service/nocloudnet/entry.go` | HTTP server lifecycle |
| `internal/service/nocloudnet/handler.go` | Metadata request handler |

---

## 3. Service Backends

Service backends are long-running subprocesses compiled into the same `mvm` binary.
Each is invoked via `mvm run <service>` and spawned in the background by the core
layer via `system.SpawnService()`. The `vsockagent` is a special case — it runs
inside the guest VM, not on the host.

### 3.1 Service Architecture

Three foreground services and one embedded guest agent share the same binary.
No separate service binaries, no symlinks, no extraction step.

| Service | Entry Point | Runs As | Purpose |
|---------|-------------|---------|---------|
| `mvm run console relay` | `console.Run(ctx, cfg)` | user | PTY-to-socket relay for serial console |
| `mvm run nocloudnet serve` | `nocloudnet.Run(ctx, cfg)` | user | HTTP server for cloud-init nocloud-net |
| `mvm run provision` | `loopmount.Run(ctx, cfg)` | **root** (sudo) | Loop-mount rootfs provisioning |
| `vsockagent/` (embedded) | Guest agent binary | root (in-VM) | Command execution and file transfer inside the guest |

The vsock agent is cross-compiled at build time, zstd-compressed, embedded via
`//go:embed`, and injected into the VM at runtime through the vsock device. This
avoids the need for SSH or any guest-side package installation for `mvm exec`,
`mvm cp`, and console operations.

Each service follows a consistent pattern: a **`Config`** struct holds all parameters,
**`Run(ctx, cfg)`** runs the service in the foreground (blocking), and
**`Spawn(ctx, cfg, ...)`** launches it as a background subprocess.

### 3.2 Console Relay

The console relay converts a Firecracker VM's serial console into an interactive
terminal session accessible through `mvm console`. It exists because Firecracker
exposes the serial console as a PTY pair, and the relay multiplexes that PTY
between a Unix socket (for CLI attachment) and a log file.

```
console.Controller → console.Spawn(ctx, cfg) → system.SpawnService("mvm", "run", "console", "relay")
                                                                  │
                                            PTY master fd ← → Unix socket ← → Client (CLI)
                                                                  │
                                                            console.log
```

The relay entry point is `mvm run console relay`. The `Run()` function in
`entry.go` opens the PTY master file descriptor (inherited as FD 3), sets up
a Unix listener, and enters the I/O loop. The I/O loop reads from the PTY in
a goroutine (fed to a channel), accepts Unix socket connections from the CLI,
and uses Go `select` to multiplex data between the PTY, the connected client,
and the log file concurrently.

- **Signal handling:** SIGTERM/SIGINT trigger graceful shutdown with PID file and socket cleanup
- **PID file:** `$MVM_CACHE_DIR/vms/<vm-id>/console.pid`
- **Socket:** `$MVM_CACHE_DIR/vms/<vm-id>/console.sock`
- **Log:** `$MVM_CACHE_DIR/vms/<vm-id>/firecracker.console.log`

**Key files:**

| File | Purpose |
|------|---------|
| `internal/service/console/entry.go` | Config struct, `Run()`, and the `runRelayIO` I/O loop |
| `internal/service/console/spawn.go` | `Spawn()` via `system.SpawnService()` |
| `internal/service/console/relay.go` | `Relay` struct — PID management and lifecycle |
| `internal/service/console/client.go` | `RelayClient` — CLI-side console attachment |

**Boundary:** The relay supports one client connection at a time. If a client is
already attached, new connection attempts are refused. The detach sequence is
Ctrl+X followed by `d`.

### 3.3 NoCloud Server

The nocloud-net HTTP server delivers cloud-init metadata to VMs over HTTP.
It is the runtime component for the "net" cloud-init mode (see §2). The server
is started per-VM and lives for the VM's lifetime.

```
cloudinit.Provisioner → nocloudnet.Spawn(ctx, cfg) → system.SpawnService("mvm", "run", "nocloudnet")
                                                                        │
                                          HTTPServer(bind=gateway_ip:port) ← → VM guest (HTTP)
                                                                        │
                                                                  serves: meta-data
                                                                         user-data
                                                                         network-config
```

- **Port range:** 8000–9000, auto-allocated via `socket.bind()` probe
- **Security:** Binds to bridge gateway IP only (never `0.0.0.0`), firewall rule in `MVM-NOCLOUDNET-INPUT` chain
- **PID file:** `$MVM_CACHE_DIR/vms/<vm-id>/nocloud-server.pid`
- **Headers:** Cache-disabling headers (`Cache-Control: no-cache, no-store, must-revalidate`)

**Key files:**

| File | Purpose |
|------|---------|
| `internal/service/nocloudnet/entry.go` | Config struct + `Run()` entry point |
| `internal/service/nocloudnet/spawn.go` | `Spawn()` via `system.SpawnService()` |
| `internal/service/nocloudnet/handler.go` | HTTP request handler |

**Boundary:** The server binds to the bridge gateway IP, not to `0.0.0.0`.
VMs on a different subnet cannot reach it. This is intentional — cloud-init
data is per-VM and must not be accessible from outside the VM's network.

### 3.4 Loop-Mount Provisioner Service

The loop-mount provisioner is the execution engine for filesystem operations.
It is spawned as a root subprocess by the `LoopMountBackend` in the API layer.

```
API layer → provisioner.NewBackend() → LoopMountBackend
  → backend.Resize() / SetHostname() / ... (queues operations)
  → backend.Run() → runWireOp() → DefaultRunner.Run(["sudo", "mvm", "run", "provision"])
                                            │
                        JSON ops stdin → losetup/mount/chroot → JSON results stdout
```

- **Communication:** JSON operation list on stdin, JSON results on stdout
- **Timeout:** 60 seconds per chroot command (in `provisioner.go`)
- **Sudo:** Passwordless sudo via `/etc/sudoers.d/mvm` drop-in
- **Speed:** ~2.3s average end-to-end

**Key files:**

| File | Purpose |
|------|---------|
| `internal/service/loopmount/provisioner.go` | Provisioning engine — losetup, mount, chroot |
| `internal/service/loopmount/entry.go` | Config struct + `Run()` entry point |
| `internal/service/loopmount/spawn.go` | `Spawn()` via `system.SpawnService()` |
| `internal/service/loopmount/wire.go` | JSON wire protocol types |

**Boundary:** Requires root. The subprocess runs with full privileges and performs
raw block device operations. The JSON protocol ensures no state leaks between
invocations.

### 3.5 Service Lifecycle

| Phase | Action | Component |
|-------|--------|-----------|
| **Init** | Create sudoers drop-in, firewall chains, cache dirs | `host.Service.Init()` |
| **Create VM** | Provision rootfs via loop-mount or guestfs | `backend.Run()` |
| **Create VM** | Start NoCloud server (net mode) or inject cloud-init (inject mode) | `nocloudnet.Spawn()` / `backend.InjectCloudInit()` |
| **Create VM** | Start console relay (when `--console` is set) | `console.Spawn()` |
| **Remove VM** | Stop console relay + NoCloud server + clean firewall rules | `console.Stop()`, `nocloudnet.Stop()` |
| **Cache prune** | Clean up stale PID files + orphan processes | `cache.Service.Prune()` |

---

## 4. Firewall Backends

Firewall backends manage NAT rules, forwarding rules, and per-VM access control
for nocloud-net servers. Two backends are available, selected by the
`firewall_backend` setting in the database (default: `nftables`).

A **`FirewallTracker`** in `internal/lib/firewall/tracker.go` provides a unified
interface for both backends. It dispatches all operations to the active backend
via the `Tracker` interface. The tracker is created once at startup in
`api.NewOperation()` with a default nftables tracker. After `mvm host init`
resolves the actual backend setting, it replaces the tracker via
`SetFirewallTracker()`.

| Backend | Default | Files |
|---------|---------|-------|
| **nftables** | Yes (`firewall_backend: nftables`) | `tracker.go`, `nftables.go`, `nftables_repository.go` |
| **iptables** | Opt-in (`firewall_backend: iptables`) | `tracker.go`, `iptables.go`, `iptables_repository.go` |

Both backends manage the same three chain types: `MVM-FORWARD` (ip filter),
`MVM-POSTROUTING` (ip nat), and `MVM-NOCLOUDNET-INPUT` (ip filter). The nftables
backend adds jump rules at position 0 of the built-in chains, using non-hook
chains so that `accept` verdicts are terminal within the mvm table — matching
the behavior users expect from iptables.

**Why two backends?** Different Linux distributions ship with different firewall
defaults. Some use nftables natively; others use iptables or iptables-legacy.
mvmctl supports both to avoid conflicts with the host's existing firewall
configuration, particularly when Docker or other container runtimes are present.

---

## 5. Selection Guide

Each backend choice represents a trade-off between speed, dependencies, and
capability. This guide maps common scenarios to the recommended configuration.

| Scenario | Recommended Backend | Rationale |
|----------|-------------------|-----------|
| Normal VM creation | Loop-Mount + inject cloud-init | Fastest path (~2.3s creation) |
| No sudo available | GuestFS provisioning | Uses `guestfs_enabled` opt-in |
| Minimal VM, no customization | Off mode cloud-init | Fastest boot, no provisioning |
| Dynamic cloud-init config | Net mode cloud-init | Network-served, no rootfs modification |
| Legacy ISO requirement | ISO mode cloud-init | Pre-built ISO via `cloud-localds` |
| Console access | Console Relay | Opt-in via `--console` (disabled by default) |
| Image optimization | Loop-Mount partition extraction | ~500ms vs ~2000ms guestfs |

---

## 6. Performance Comparison

Benchmark data from `benchmarks/results.json` (2026-06-16, sequential run, 5s threshold).

Measured via `mvm exec -- echo ok` inside the VM — replaces the earlier
SSH-based probe. All images use the loop-mount provisioning backend (the default).

### End-to-End VM Creation (create_s)

| Image | create_s | total_s | Threshold |
|-------|----------|---------|-----------|
| alpine | 1.4s | 2.0s | 5s ✅ |
| ubuntu:24.04 | 2.0s | 2.4s | 5s ✅ |
| ubuntu-minimal:24.04 | 2.0s | 2.4s | 5s ✅ |
| archlinux | 2.4s | 3.9s | 5s ✅ |
| debian:12 | 4.3s | 4.6s | 5s ✅ |
| firecracker:v1.15 | 1.9s | 2.1s | 5s ✅ |
| **Average** | **2.3s** | **2.9s** | **100% pass** |

### Notes

- All six images passed within the 5s threshold in sequential mode.
- Parallel benchmark execution (use `--no-parallel` in `benchmarks/boot_time.py`)
  avoids I/O contention on the host for resource-heavy images (debian, archlinux).
- The probe now uses `echo ok` via `mvm exec` (vsock guest agent)
  instead of SSH — this measures end-to-end VM readiness (vsock agent responding).
- **Console relay startup**: ~10ms (negligible, no separate benchmark)
- **NoCloud server startup**: ~50ms (negligible, no separate benchmark)

---

## 7. Binary Build

The `mvm` binary is a standard Go binary that includes all services compiled in:

```bash
go build -o dist/mvm ./cmd/mvm
```

The three `mvm run` services (console relay, nocloud-net server, loopmount provisioner) plus the vsock guest agent binary are all compiled into the same binary. No separate service binaries, no symlinks, no extraction step.

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
│    ├── cloudinit.Provisioner.Provision()  (prepare configs)                   │
│    ├── provisioner.NewBackend(opts)  (select backend at startup)            │
│    │   ├── LoopMount → backend.Resize()/SetHostname()/... → backend.Run()  │
│    │   │   └── runWireOp() → DefaultRunner.Run(["mvm","run","provision"])  │
│    │   │       → losetup → mount → write → chroot → resize → umount        │
│    │   └── GuestFS   → backend.Resize()/SetHostname()/... → backend.Run()  │
│    │       → guestfs.Backend → libguestfs (QEMU appliance)                 │
│    ├── console.Spawn()  (if --console)                                      │
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
│  core/image/       Provisioner, Service, Repository, SQLite, Resolver       │
│  core/kernel/      Controller, Service, Repository, SQLite, Resolver        │
│  core/host/        Controller, Service, Repository, SQLite, Detector, Probe │
│  core/config/      Service, Repository, SQLite, Constraints                 │
│  core/console/     Controller                                               │
│  core/logs/        Controller, Service                                      │
│  core/cache/       Service                                                  │
│  core/ssh/         Service                                                  │
│  core/binary/      Controller, Service, Repository, SQLite, Resolver        │
│  core/key/         Controller, Service, Repository, SQLite, Resolver        │
│  core/vsock/       Client, Service, Repository, SQLite, Resolver            │
│  core/snapshot/    Repository, SQLite, Resolver                             │
└───────────────────────────┬─────────────────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                      Service Layer (internal/service)                        │
│                                                                             │
│  All compiled into the single mvm binary                                    │
│    ├── mvm run console relay    → console relay (PTY proxy)                 │
│    ├── mvm run nocloudnet serve → nocloud-net HTTP server                   │
│    ├── mvm run provision        → loopmount provisioner (requires sudo)     │
│    └── vsockagent               → embedded guest agent (injected into VM)   │
└─────────────────────────────────────────────────────────────────────────────┘
```
