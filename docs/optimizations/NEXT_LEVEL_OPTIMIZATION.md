# Next-Level Optimization: Sub-100ms VM Creation

> **Goal:** Reduce VM creation from ~3-10s (current) to <100ms for hot-pool VMs
> and <500ms for cold-start VMs, enabling high-density microVM operations.
>
> **Principle:** Python is NOT the bottleneck — Firecracker boot, disk I/O, and
> network setup are. Language choice (Go/Zig) would save <5% wall time. The real
> gains come from **architectural changes**.
>
> **Fly.io Model:** "Create ahead, start fast" — pay the cost upfront, yield the
> benefit on every subsequent VM start.

## Table of Contents

1. [Current State & Bottlenecks](#1-current-state--bottlenecks)
2. [Tier 1 — Transformative (The Big Levers)](#2-tier-1--transformative-the-big-levers)
3. [Tier 2 — High Impact](#3-tier-2--high-impact)
4. [Tier 3 — Medium Impact](#4-tier-3--medium-impact)
5. [Tier 4 — Low Impact / Micro-Optimizations](#5-tier-4--low-impact--micro-optimizations)
6. [Priority Matrix & Implementation Roadmap](#6-priority-matrix--implementation-roadmap)
7. [References](#7-references)

---

## 1. Current State & Bottlenecks

### 1.1 Existing Optimizations (Already Done)

These are already implemented in the codebase and will NOT be covered here:

| Optimization | Where | Doc |
|---|---|---|
| tmpfs ready pool (pre-decompress images to `/dev/shm`) | `core/image/_service.py` | [`fast-durable-image-copy.md`](fast-durable-image-copy.md) |
| reflink + sparse copy with `fdatasync()` | `core/image/_service.py:materialize_to()` | [`fast-durable-image-copy.md`](fast-durable-image-copy.md) |
| libguestfs: direct backend, `cachemode=unsafe`, minimal vCPU/mem | `core/_shared/_provisioner/_backend.py` | [`guestfs_boot.md`](guestfs_boot.md) |
| Fixed appliance, disabled recovery/autosync | `core/_shared/_guestfs/_base.py` | [`guestfs_boot.md`](guestfs_boot.md) |
| Loop-mount backend (mvm-provision binary, faster than guestfs) | `core/_shared/_loopmount/` | — |
| ThreadPoolExecutor for batch VM operations | `core/_shared/_parallel.py`, `core/vm/_service.py` | — |
| Firecracker snapshot/resume API (create_snapshot, load_snapshot) | `core/vm/_firecracker.py` | — |
| Progress reporting + built-in timing logs | `api/vm_operations.py` | — |
| Lazy CLI module loading (LazyMVMGroup) | `main.py` | — |
| Firecracker native PCI support (`--enable-pci`) | `core/vm/_firecracker.py` | — |

### 1.2 Current VM Creation Timing Profile

```
[timing] resolution:          ~50ms   (DB queries, validation)
[timing] network_setup:       ~300ms  (ip link, iptables, bridge setup)
[timing] image_clone:         ~500ms  (reflink copy from tmpfs, ~400MB)
[timing] provisioner_setup:   ~500ms  (guestfs or loop-mount resize)
[timing] provisioner_run:     ~500ms  (cloud-init injection, SSH keys, DNS)
[timing] firecracker_spawn:   ~500ms  (Firecracker process + kernel boot)
[timing] console_setup:       ~50ms   (PTY + socket relay)
─────────────────────────────────────────────────────
TOTAL:                        ~2.4s   (best case, Alpine, fast NVMe)
                              ~5-10s  (typical, Ubuntu with systemd)
```

### 1.3 Root Cause — What Actually Takes Time

| Phase | Time | Bottleneck | Language Impact |
|---|---|---|---|
| `image_clone` | 100-500ms | Disk I/O (copy ~400MB sparse image) | **None** — I/O bound |
| `provisioner_*` | 500-3000ms | Disk I/O (guestfs resize + write) | **None** — I/O bound |
| `firecracker_spawn` | 100-800ms | Kernel boot + init system | **None** — kernel time |
| `network_setup` | 50-300ms | Subprocess (ip, iptables) | **Minimal** — subprocess spawn |
| Python overhead | ~50ms | Import, resolution, orchestration | <5% of total |

---

## 2. Tier 1 — Transformative (The Big Levers)

These are the optimizations that can **radically change** the performance profile.
Each can reduce VM creation time by **50-90%** on their own.

### 2.1 Snapshot-Based VM Cloning

**What it is:** Boot a single "golden" VM with the complete application stack.
Pause it at the point where the guest OS has finished booting. Take a full
Firecracker snapshot (memory + state file). Create new VMs by loading that
snapshot instead of booting fresh.

**Why it helps:** This is the single biggest optimization possible. Firecracker
snapshot loading is optimized for speed via `MAP_PRIVATE` with on-demand page
loading. From the Firecracker docs: *"Loading a snapshot takes 3-8ms"* —
compared to 500-2000ms for a full boot with systemd.

**Bottleneck addressed:** Kernel boot + init system (500-2000ms)

**Feasibility:** Medium
- ✅ `create_snapshot()` and `load_snapshot()` already exist in `FirecrackerSpawner`
- ✅ `VMState.PAUSED` already exists in the model
- ✅ The Firecracker API `PUT /snapshot/create` and `PUT /snapshot/load` are already implemented
- ❌ Need: golden VM builder, snapshot storage, per-clone reconfiguration
- ❌ Need: per-VM uniqueness handling (MAC, IP, hostname, SSH keys)

**Estimated impact:** Transformative — 3000ms → 50-200ms

**What needs to happen:**

```
┌─────────────────────────────────────────────────────────┐
│ Phase 1: Golden VM creation (one-time, ~5-10s)          │
│                                                          │
│  1. Create a base VM with desired image + kernel         │
│  2. Boot it, let cloud-init finish                       │
│  3. Pause via PATCH /vm {"state": "Paused"}              │
│  4. Snapshot via PUT /snapshot/create                    │
│  5. Store {mem, state} files as "golden" template        │
│                                                          │
│ Phase 2: Clone creation (per-VM, ~50ms)                  │
│                                                          │
│  1. Pre-allocate: TAP, IP, MAC, socket path              │
│  2. Spawn Firecracker process with --no-boot             │
│  3. Load snapshot via PUT /snapshot/load                 │
│  4. Post-resume: reconfigure guest (agent-based)         │
│  5. Resume VM                                            │
└─────────────────────────────────────────────────────────┘
```

**Key considerations:**
- Snapshots require identical CPU microarchitecture on resume
- Memory files can be huge (guest RAM size). Use `uffd` (userfaultfd) for
  lazy page loading with Firecracker 1.9+ to defer page loading to page faults
- Guest wall-clock continues from snapshot time. Use `kvm-clock` with
  `clock_realtime: true` on host kernel >= 5.16
- Each clone needs unique network configuration via the `NetworkForClone`
  Firecracker feature (separate TAP devices, MAC addresses)
- Track dirty pages if differential snapshots are needed later
- cgroups v2 is strongly recommended (v1 has high snapshot restoration latency)

**References:**
- [Firecracker Snapshot Support](https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/snapshot-support.md)
- [Network for Clones](https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/network-for-clones.md)
- [Firecracker Boot Time Tests](https://github.com/firecracker-microvm/firecracker/blob/main/tests/integration_tests/performance/test_boottime.py)

### 2.2 VM Hot-Standby Pool

**What it is:** A pool manager that keeps N pre-booted, paused VMs ready to
assign. When a new VM is requested, the pool pops one, runs post-resume
configuration, and hands it to the user. Pool replenishment runs in the
background.

**Why it helps:** This is exactly how Fly.io achieves ~300ms cross-region VM
starts. The "create" path (slow: image pull, host selection, DB persistence) is
separated from the "start" path (fast: just resume a pre-allocated VM). The
critical path is just Firecracker snapshot resume (3-8ms) + minimal
configuration.

**Bottleneck addressed:** All of them — every slow path is done ahead of time

**Feasibility:** Hard (largest architectural change)
- Requires a persistent pool manager (daemon or background thread)
- Each pre-booted VM needs pre-allocated, unique resources (TAP, IP, MAC, socket)
- Post-resume guest reconfiguration (hostname, SSH keys, TLS certs, entropy)
- Pool replenishment must be async — create new replacement VMs after each pop
- Pool sizing strategy: min/max pool size, replenishment rate

**Estimated impact:** Transformative — sub-100ms "create" for existing pool,
~10-30ms for warm pool hit

**What needs to happen:**

```python
class VMHotStandbyPool:
    """Maintains a pool of pre-booted, paused microVMs.

    - Pre-boot: Creates N VMs on init, paused at snapshot-ready state
    - Acquire: Returns a ready-to-use VM, starts replenishment
    - Release: Destroys VM and replenishes the pool
    - Health: Periodically checks pool VMs and refreshes stale ones
    """

    def __init__(self, min_pool_size: int = 5, max_pool_size: int = 20):
        self._pool: queue.Queue[PooledVM] = queue.Queue()
        self._replenisher = ThreadPoolExecutor(max_workers=2)
        self._prefill(min_pool_size)

    def acquire(self) -> PooledVM:
        """Pop a ready VM. Returns immediately if pool non-empty."""
        vm = self._pool.get_nowait()
        self._replenisher.submit(self._create_replacement)
        return vm

    def _create_replacement(self) -> None:
        """Background: create a new golden snapshot clone for the pool."""
        # 1. Pre-allocate resources (TAP, IP, MAC, socket path)
        # 2. Load golden snapshot into Firecracker
        # 3. Configure guest (hostname, SSH keys, etc.)
        # 4. Resume, verify running
        # 5. Pause, snapshot again for clean state
        # 6. Push to pool
```

**Key considerations:**
- Each pool VM uses real memory (even paused). Memory overcommit is essential
- Use huge pages (2MB) to reduce memory overhead (see §3.1)
- cgroup v2 is CRITICAL for fast snapshot restore on Linux 6.1+ (see §3.2)
- Pool VMs should use minimal base image + overlayfs to minimize disk usage

**References:**
- [Fly Machines Blog](https://fly.io/blog/fly-machines/)
- [Fly Architecture](https://fly.io/docs/reference/architecture/)

### 2.3 Lightweight Init (Inside Guest)

**What it is:** Replace systemd (200-2000ms boot time) with a minimal init
system inside the VM rootfs. Options: BusyBox init, OpenRC, custom static
binary as PID 1, or `init=/path/to/app`.

**Why it helps:** systemd probes hardware, starts 40+ services, mounts
filesystems, waits for network, and runs timers. For a Firecracker VM running
a single application, >95% of this is wasted. A minimal init can boot the
userspace in 5-50ms instead of 200-2000ms.

**Bottleneck addressed:** Userspace init (200-2000ms)

**Feasibility:** Medium
- Requires building custom rootfs images
- For snapshot-based cloning (§2.1), this is partially mitigated — you only
  pay the init cost once (during golden VM boot)
- For non-snapshot cold starts, this is critical
- Option: Create a separate "mvm-minimal" image variant with BusyBox init

**Estimated impact:** Transformative for cold starts (200-2000ms → 5-50ms),
Low if snapshot cloning is the primary path

**Implementation options:**

```
Option A: init=/bin/sh + static binary
  - The app itself is PID 1
  - Must handle SIGCHLD reaping and signal forwarding
  - ~5ms userspace boot
  - Go or Rust static binary, ~5MB

Option B: BusyBox init
  - Tiny, handles reaping natively
  - /etc/inittab: just what you need
  - ~10-20ms userspace boot
  - ~1MB total

Option C: OpenRC
  - Traditional init with dependency-based service management
  - ~50-100ms boot
  - Familiar to Gentoo/Alpine users

Option D: systemd-minimal
  - systemd.unit=emergency.target or custom.target with 1-2 services
  - ~100-200ms boot
  - Compatibility with existing images
```

**References:**
- [Julia Evans: Firecracker in <1 second](https://jvns.ca/blog/2021/01/23/firecracker--start-a-vm-in-less-than-a-second/)
- [Firecracker Rootfs & Kernel Setup](https://github.com/firecracker-microvm/firecracker/blob/main/docs/rootfs-and-kernel-setup.md)

---

## 3. Tier 2 — High Impact

These optimizations save 30-50% each and compound with Tier 1.

### 3.1 Huge Pages (2MB/1GB)

**What it is:** Back VM guest memory with 2MB or 1GB huge pages instead of
standard 4KB pages. Set via Firecracker's `/machine-config` API with
`huge_pages: "2M"`.

**Why it helps:** Firecracker's own performance tests show **up to 50% boot
time improvement** with huge pages. Fewer TLB entries = less address translation
overhead. Also reduces KVM shadow page table overhead.

**Bottleneck addressed:** Memory virtualization overhead during boot

**Feasibility:** Easy
1. Pre-allocate huge pages on the host:
   ```bash
   # 2MB pages: echo <count> > /proc/sys/vm/nr_hugepages
   # For 2GB pool: echo 1024 > /proc/sys/vm/nr_hugepages
   # 1GB pages: echo <count> > /sys/kernel/mm/hugepages/hugepages-1048576kB/nr_hugepages
   ```
2. Set in Firecracker machine config:
   ```python
   "/machine-config": {"huge_pages": "2M", ...}
   ```
3. Can be added as a Drop-in config option in mvmctl

**Estimated impact:** High — up to 50% boot time reduction

**Limitations:**
- Dirty page tracking (`track_dirty_pages`) negates huge page benefits
  (KVM forces 4K page tables when tracking is enabled)
- Snapshot restore with huge pages requires UFFD (userfaultfd) support
- Balloon can't reclaim sub-2MB granularity from huge pages
- Cannot be used simultaneously with dirty page tracking (for differential
  snapshots) — this is a Firecracker limitation

**References:**
- [Firecracker Huge Pages](https://github.com/firecracker-microvm/firecracker/blob/main/docs/hugepages.md)
- [Firecracker Boot Time Tests](https://github.com/firecracker-microvm/firecracker/blob/main/tests/integration_tests/performance/test_boottime.py)

### 3.2 cgroup v2 + `favordynmods` + `kvm.nx_huge_pages=never`

**What it is:** Three related kernel-level optimizations that dramatically
affect Firecracker startup time, especially snapshot restore.

**Why it helps:**
- **cgroup v2**: Firecracker explicitly states *"High snapshot restoration
  latency when cgroups V1 are in use."* cgroup v2 has a simpler, unified
  hierarchy that avoids the 8.5ms `KVM_CREATE_VM` regression on Linux 6.1+
- **favordynmods**: A cgroup mount option that restores 5.10-level boot times
  on 6.1 kernels. `sudo mount -o remount,favordynmods /sys/fs/cgroup`
- **kvm.nx_huge_pages=never**: Disables NX huge page recovery, which adds
  overhead during `KVM_CREATE_VM`. Set in `/etc/modprobe.d/kvm.conf`

**Bottleneck addressed:** KVM + cgroup overhead (8.5ms for snapshot restore)

**Feasibility:** Easy — host-level configuration only

```bash
# /etc/modprobe.d/kvm.conf
options kvm ignore_msrs=1 min_timer_period_us=100 nx_huge_pages=never

# /etc/fstab or boot script
cgroup2 /sys/fs/cgroup cgroup2 rw,nosuid,nodev,noexec,relatime,favordynmods 0 0
```

**Estimated impact:** High — can reduce snapshot restore time from ~12ms to ~3ms

**References:**
- [Firecracker Prod Host Setup](https://github.com/firecracker-microvm/firecracker/blob/main/docs/prod-host-setup.md)

### 3.3 Overlayfs / btrfs / ZFS CoW Rootfs

**What it is:** Instead of copying the full rootfs image (even with reflink,
you still need to clone the data), use a copy-on-write filesystem to create
instant, zero-copy rootfs views per VM.

**Why it helps:** The `image_clone` phase (100-500ms for reflink of a 400MB
sparse image) becomes essentially instant (~1ms). The base image is shared
across all VMs, only modified blocks are stored per VM.

**Bottleneck addressed:** Rootfs materialization (100-500ms)

**Feasibility:** Medium

**Three approaches (in order of preference):**

#### Approach A: btrfs Subvolume Snapshots

```bash
# One-time: create a base subvolume
btrfs subvolume create /mnt/vm-images/base-rootfs
# Copy the decompressed image into it
# Mark as read-only
btrfs subvolume snapshot -r /mnt/vm-images/base-rootfs /mnt/vm-images/base-ro

# Per VM: instant clone
btrfs subvolume snapshot /mnt/vm-images/base-ro /mnt/vm-images/vm-001
# Present /mnt/vm-images/vm-001 as the VM rootfs
```

- Requires btrfs on the host
- Snapshots are O(1), near-instant
- Can be presented to Firecracker as raw block device via loop device or qemu-nbd

#### Approach B: ZFS Clones

```bash
# One-time
zfs create -p -o mountpoint=/mnt/vm-images zpool/vm-base
zfs snapshot zpool/vm-base@clean

# Per VM: instant clone
zfs clone zpool/vm-base@clean zpool/vm-001
```

- Requires ZFS on the host
- ZFS clones are true CoW with block-level dedup
- More memory-hungry than btrfs (~1GB recommended minimum)

#### Approach C: Device-Mapper Snapshot (Fly.io approach)

```bash
# One-time
dmsetup create base-image --table "0 $(blockdev --getsz /path/to/base.img) snapshot-origin /path/to/base.img"

# Per VM
dmsetup create vm-cow --table "0 $(blockdev --getsz /path/to/base.img) snapshot /path/to/base.img /path/to/cow.img P 8"
```

- More complex setup
- What Fly.io uses in production
- Device-mapper presents a proper block device — no loop/qemu-nbd needed

**Estimated impact:** High — reduces `image_clone` from 100-500ms to ~1ms

**References:**
- [Linux OverlayFS](https://www.kernel.org/doc/html/latest/filesystems/overlayfs.html)
- [Device Mapper Snapshot](https://www.kernel.org/doc/Documentation/device-mapper/snapshot.txt)
- [Julia Evans on Fly.io's approach](https://jvns.ca/blog/2021/01/23/firecracker--start-a-vm-in-less-than-a-second/)

### 3.4 API Daemon / Server Mode

**What it is:** Instead of running `mvm create vm-name` as a one-shot CLI
command (which pays Python import time, subprocess overhead, and cold cache
every time), run a persistent daemon that keeps everything warm.

**Why it helps:**
- **Zero CLI import time:** Libraries loaded once, memory resident
- **Zero subprocess overhead:** Resource pools maintained in daemon memory
- **Persistent connections:** Firecracker API sockets, DB connections, cgroup handles
- **Event-driven monitoring:** inotify/epoll instead of polling for VM state changes
- **Resource pools live in daemon:** TAPs, IPs, MACs, cached images

**Bottleneck addressed:** CLI startup (~50ms), cold resource allocation

**Feasibility:** Medium

**Architecture:**

```
┌──────────┐    UDS/HTTP    ┌──────────────────────┐
│ mvm CLI   │ ──────────►  │ mvm-d (daemon)        │
│ (thin)    │ ◄──────────  │                        │
└──────────┘               │  ┌─ VM Pool ───────┐  │
                           │  │ hot-standby VMs  │  │
                           │  └─────────────────┘  │
                           │  ┌─ Res Pool ──────┐  │
                           │  │ TAPs, IPs, MACs  │  │
                           │  └─────────────────┘  │
                           │  ┌─ Image Cache ───┐  │
                           │  │ tmpfs ready pool │  │
                           │  └─────────────────┘  │
                           │  ┌─ FC Connections ┐  │
                           │  │ persistent API   │  │
                           │  │ sockets per VM   │  │
                           │  └─────────────────┘  │
                           └──────────────────────┘
```

**What changes:**
1. `mvm-d` — A new Typer/Click daemon command (or systemd service)
2. The daemon starts: opens DB, pre-allocates resource pools, maintains
   hot-standby VM pool
3. The `mvm` CLI forwards requests to `mvm-d` via Unix domain socket
4. For users who don't want a daemon, the CLI falls back to direct execution
5. Daemon handles: create VM, list VMs, remove VM, reconcile

**Estimated impact:** High — 30-70% reduction in CLI-perceived latency

**References:**
- Docker daemon model (dockerd + docker CLI)
- Podman socket-activation model
- Fly.io Machines API model

### 3.5 Custom Minimal Kernel

**What it is:** Build a custom Linux kernel with only the drivers and features
needed for Firecracker. Firecracker provides recommended guest kernel configs
as a starting point.

**Why it helps:** A minimal kernel is:
- Smaller (faster to load into guest memory)
- Fewer device probes (faster boot)
- Less memory usage (smaller footprint)
- Faster decompression at boot

**Bottleneck addressed:** Kernel boot (50-200ms)

**Feasibility:** Medium — requires kernel compilation knowledge

**Config optimization targets:**
```
Disable:
  CONFIG_NR_CPUS=1          (if single vCPU)
  CONFIG_SMP=n              (if single vCPU)
  All filesystems except ext4 (or whatever you use)
  IPv6, bridging, netfilter (if not needed in guest)
  USB, SCSI, ATA, IDE drivers (not needed in Firecracker)
  Sound, graphics, DRM drivers
  All wireless networking
  All unused architecture support

Enable:
  CONFIG_CC_OPTIMIZE_FOR_SIZE=y
  CONFIG_KERNEL_LZO=y or CONFIG_KERNEL_ZSTD=y  (faster decompression)
  CONFIG_VIRTIO_BLK, CONFIG_VIRTIO_NET, CONFIG_VIRTIO_VSOCK
  CONFIG_EXT4_FS (or your filesystem)
```

**Estimated impact:** High — can reduce kernel boot from ~100ms to ~30-50ms

**What already exists:**
- `core/kernel/_service.py` already handles kernel building with custom configs
- The `kernel pull` command already supports official kernels
- Custom kernel config merging is supported

**References:**
- [Firecracker Guest Kernel Configs](https://github.com/firecracker-microvm/firecracker/blob/main/resources/guest_configs/)
- [Firecracker Kernel Policy](https://github.com/firecracker-microvm/firecracker/blob/main/docs/kernel-policy.md)
- [Firecracker Rootfs & Kernel Setup](https://github.com/firecracker-microvm/firecracker/blob/main/docs/rootfs-and-kernel-setup.md)

### 3.6 Network Resource Pre-creation

**What it is:** Pre-create TAP devices, allocate IPs, assign MACs, install
nftables/iptables rules in a background pool. When a VM is requested, it
just pops a pre-warmed network resource.

**Why it helps:** The `network_setup` phase (50-300ms) is serial and
subprocess-heavy. Pre-creating network resources moves this cost off the
critical path.

**Bottleneck addressed:** Network setup (50-300ms per VM)

**Feasibility:** Medium

```python
class NetworkResourcePool:
    """Pre-allocates and manages network resources.

    Each resource contains:
    - TAP device (created, attached to bridge)
    - IP address (from pre-allocated subnet pool)
    - MAC address (pre-generated)
    - nftables rules (pre-installed)
    """

    def __init__(self, pool_size: int = 50):
        self._pool: queue.Queue[NetworkResource] = queue.Queue()
        self._prefill(pool_size)

    def _create_resource(self) -> NetworkResource:
        """Background: create TAP, allocate IP, generate MAC, install rules."""
        tap = NetworkUtils.create_tap(prefix="pre-")
        ip = self._ip_pool.allocate()
        mac = NetworkUtils.generate_mac()
        NetworkUtils.attach_tap(tap, self._bridge)
        NetworkUtils.install_rules(tap, ip)
        return NetworkResource(tap=tap, ip=ip, mac=mac)

    def acquire(self, vm_id: str) -> NetworkResource:
        """Pop a pre-warmed resource, assign to VM."""
        resource = self._pool.get()
        NetworkUtils.rename_tap(resource.tap, tap_name_for(vm_id))
        LeaseRepository(self._db).assign(vm_id, resource.ip)
        self._replenish()
        return resource

    def release(self, resource: NetworkResource) -> None:
        """Return resource to pool (reset + recycle)."""
        NetworkUtils.reset_tap(resource.tap)
        self._ip_pool.release(resource.ip)
        self._pool.put(resource)
```

**Estimated impact:** High — removes 50-300ms from the critical path

---

## 4. Tier 3 — Medium Impact

These save 10-30% individually and are often easy to implement.

### 4.1 Kernel Boot Parameters Tuning

**What it is:** Additional kernel command-line parameters to disable unnecessary
hardware probing and security features in the guest.

**Why it helps:** Each feature probed at boot takes time. Disabling them is
zero-cost and adds up.

**Feasibility:** Very Easy — just append to `boot_args` in Firecracker config

**Current boot_args:**
```
reboot=k panic=1 nomodule 8250.nr_uarts=0 i8042.noaux i8042.nomux
i8042.dumbkbd swiotlb=noforce
```

**Additional params to add (with security trade-offs noted):**

| Parameter | Effect | Security Risk |
|---|---|---|
| `mitigations=off` | Disable all CPU vulnerability mitigations (Spectre, Meltdown, etc.) | **HIGH** — only for single-tenant |
| `nopti` | Disable kernel page table isolation | Medium — reduces KPTI isolation |
| `nokaslr` | Disable kernel ASLR | Medium — predictable kernel layout |
| `no_timer_check` | Skip timer IRQ check | None |
| `clocksource=kvm-clock` | Use kvm-clock directly | None |
| `audit=0` | Disable audit subsystem | None |
| `elevator=noop` | No I/O scheduler (best for virtio-blk) | None |
| `maxcpus=1 nr_cpus=1` | Restrict to single CPU | None — if single vCPU |
| `quiet loglevel=0` | Reduce console output | None |

**Safe defaults to add immediately (no security impact):**
```
no_timer_check clocksource=kvm-clock audit=0 elevator=noop quiet loglevel=0
```

**Estimated impact:** Medium — 10-50ms combined boot time reduction

**References:**
- [Firecracker Kernel Policy](https://github.com/firecracker-microvm/firecracker/blob/main/docs/kernel-policy.md)
- [Linux kernel-parameters.txt](https://www.kernel.org/doc/html/latest/admin-guide/kernel-parameters.html)

### 4.2 os.sendfile() / os.copy_file_range() for File I/O

**What it is:** Replace Python's `open() + read() + write()` with zero-copy
kernel-mediated file transfers.

**Why it helps:** `os.sendfile(out_fd, in_fd, ...)` transfers data between
file descriptors entirely in kernel space — zero user-space copies.
`os.copy_file_range()` is even better — it can use reflink (CoW) on
btrfs/XFS, making the copy O(1).

**Feasibility:** Very Easy

```python
# Current (already optimal for reflink):
# Uses cp --reflink=auto --sparse=always

# Additional optimization for non-reflink paths:
import os

def zero_copy_copy(src: str, dst: str) -> None:
    """Copy using copy_file_range for potential reflink on btrfs/XFS."""
    with open(src, 'rb') as src_f, open(dst, 'wb') as dst_f:
        # copy_file_range may return less than requested
        while True:
            copied = os.copy_file_range(
                src_f.fileno(), dst_f.fileno(), 65536
            )
            if copied == 0:
                break
```

**Estimated impact:** Low-Medium — The current `cp` approach is already
well-optimized. This helps on fallback paths where `cp` is not available.

### 4.3 nftables Migration

**What it is:** Replace `iptables` commands with `nft` (nftables) commands.
Firecracker's production docs recommend nftables over iptables.

**Why it helps:**
- Atomic chain updates (no "ruleset flapping")
- Better performance for large rulesets
- In-kernel processing without user-space round-trips
- `iptables-nft` (translation layer) is "no longer recommended" per Red Hat

**Feasibility:** Easy — nftables syntax is different but there's a 1:1 mapping
for the rules mvmctl creates. Firecracker docs provide both syntaxes.

**Estimated impact:** Low for small rulesets, Medium for large deployments
(100+ VMs = 100+ nftables rules)

**References:**
- [Firecracker Network Setup](https://github.com/firecracker-microvm/firecracker/blob/main/docs/network-setup.md)
- [Firecracker Prod Host Setup](https://github.com/firecracker-microvm/firecracker/blob/main/docs/prod-host-setup.md)

### 4.4 Nuitka Compilation Optimization

**What it is:** Tune Nuitka compilation flags for maximum performance, not just
fast builds.

**Why it helps:** The current mvmctl binary (35MB) is already compiled. With
the right flags, startup can be even faster and runtime performance improved.

**Feasibility:** Easy — flag tuning only

**Optimization flags to add/verify:**
```bash
nuitka \
  --onefile \
  --lto=yes \                    # Link-time optimization (already used in --optimize)
  --remove-output \              # Smaller binary
  --noinclude-unused-modules \   # Tree-shake unused imports
  --include-package=mvmctl \
  --python-flag=-OO \            # Strip docstrings + asserts
  --clang \                      # Use clang (better LTO than GCC)
  --jobs=$(nproc) \
  src/mvmctl/main.py
```

**Estimated impact:** Low — Nuitka already compiles, marginal additional gain

### 4.5 os.posix_spawn() for Firecracker Subprocess

**What it is:** Use `os.posix_spawn()` instead of `subprocess.Popen()` for
Firecracker process creation. `posix_spawn` can use `CLONE_VFORK` to avoid
copying page tables, making it significantly faster than `fork()` + `exec()`.

**Why it helps:** Firecracker process spawning (fork + exec + seccomp + jailer)
is ~20-50ms. `posix_spawn` can save ~2-5ms of fork overhead.

**Feasibility:** Easy — replace in `core/vm/_firecracker.py`:

```python
# Replace subprocess.Popen with:
import os

pid = os.posix_spawn(
    firecracker_binary,
    [firecracker_binary, ...args],
    env={...},
    file_actions=[
        (os.POSIX_SPAWN_CLOSE, 3),  # close extra fds
    ],
)
```

**Estimated impact:** Low-Medium — ~2-5ms per Firecracker spawn

### 4.6 Pre-Allocated Resource Pools (General)

**What it is:** Beyond network resources, pre-allocate all VM resources:
jailer chroot directories, socket paths, cgroup directories, tmpfs
workspaces, PID file locations.

**Why it helps:** Each `mkdir`, `chown`, `mknod`, `cgroup` operation in the
critical path adds latency. Pre-creating directories and allocating paths is
trivial.

**Feasibility:** Easy

**Resources to pre-allocate:**
- VM directories: `pre-alloc/vm-{vm_id}/` (jailer root, chroot)
- API socket paths: `/tmp/firecracker.{vm_id}.socket`
- Console socket paths: `/tmp/console.{vm_id}.sock`
- Log/metrics file paths: `pre-alloc/logs/{vm_id}/`
- PID file paths: `pre-alloc/pids/{vm_id}.pid`
- MAC addresses: sequential or counter-based pre-generation
- IP addresses: bitmap-based allocation (O(1))

**Estimated impact:** Low-Medium for individual savings, compounds across all
resource types to save 20-50ms total.

---

## 5. Tier 4 — Low Impact / Micro-Optimizations

These are low-effort optimizations that each save a few milliseconds but are
worth doing because they're easy.

### 5.1 Python Import Optimization

**What it is:** Profile and minimize Python import time. Use the existing
`LazyMVMGroup` pattern more aggressively.

```bash
python -X importtime -c 'from mvmctl.main import app'
```

**Estimated impact:** 10-30ms, especially for `--help` and simple commands

### 5.2 `__slots__` for Dataclasses

```python
# Change:
@dataclass
class VMInstanceItem: ...

# To:
@dataclass(slots=True)
class VMInstanceItem: ...
```

**Estimated impact:** ~15% faster attribute access, ~50% less memory per instance
(only matters at scale — thousands of VM objects)

### 5.3 asyncio for Concurrent Subprocess Management

**What it is:** For managing hundreds of concurrent VMs, `asyncio` has lower
overhead than `ThreadPoolExecutor` (no OS thread stack per concurrent op).
`asyncio.create_subprocess_exec()` handles subprocess management natively.

**Estimated impact:** Low for per-VM speed, Medium for scaling to 500+
concurrent operations

### 5.4 Connection Pooling for Firecracker API

**What it is:** Firecracker communicates via HTTP/1.1 over Unix domain sockets.
Enable HTTP keep-alive to reuse connections across API calls to the same VM.

**Estimated impact:** Low — only helps when making many sequential API calls
to the same Firecracker process

### 5.5 Event-Driven VM State Monitoring

**What it is:** Replace polling (e.g., `time.sleep(0.1)` while waiting for
socket) with epoll/inotify for immediate state change notification.

```python
# Current: polling with time.sleep
while not socket_exists(path):
    time.sleep(0.1)

# Better: inotify
import pyinotify
wm = pyinotify.WatchManager()
notifier = pyinotify.Notifier(wm)
wm.add_watch(str(path.parent), pyinotify.IN_CREATE)
notifier.event_ready()  # blocks until file appears
```

**Estimated impact:** Low — Firecracker already boots in ~500ms, the polling
adds ~50ms worst-case (0.1s * 0.5 expected)

### 5.6 Memory Ballooning

**What it is:** Enable Firecracker's virtio-balloon device to reclaim memory
from idle VMs. Allows higher VM density on a host.

**Estimated impact:** Not for creation speed — only for VM density
(Type: Density optimization, not speed)

### 5.7 CPU Pinning

**What it is:** Pin vCPU threads to specific physical CPU cores to prevent
cache thrashing and context-switching between VMs.

**Estimated impact:** Not for creation speed — only for workload performance
consistency (Type: Performance consistency, not speed)

---

## 6. Priority Matrix & Implementation Roadmap

### 6.1 Priority Matrix

```
Rank │ Optimization                    │ Impact       │ Effort    │ Dependencies
─────┼─────────────────────────────────┼──────────────┼───────────┼─────────────
  1  │ Snapshot-based VM cloning       │ Transformative│ 2-3 weeks │ cgroup v2, huge pages
  2  │ VM hot-standby pool             │ Transformative│ 3-4 weeks │ Snapshot cloning (#1)
  3  │ Huge pages (2MB)                │ High          │ 1 day     │ Host config
  4  │ cgroup v2 + kvm tuning          │ High          │ 0.5 day   │ Host config
  5  │ Lightweight init (guest)        │ Transformative│ 1-2 weeks │ Image building
  6  │ Overlayfs/btrfs CoW rootfs      │ High          │ 1-2 weeks │ Host filesystem
  7  │ API daemon/server mode          │ High          │ 2-3 weeks │ Pool (#2) needs this
  8  │ Network resource pre-creation   │ High          │ 1 week    │ Pool (#2) needs this
  9  │ Custom minimal kernel           │ High          │ 1-2 weeks │ Kernel building
 10  │ Kernel boot params tuning       │ Medium        │ 1 hour    │ None
 11  │ nftables migration              │ Medium        │ 3-5 days  │ Testing
 12  │ Nuitka optimization             │ Low           │ 1 day     │ Build pipeline
 13  │ os.posix_spawn()                │ Low-Medium    │ 1 day     │ None
 14  │ Pre-allocated resource pools    │ Low-Medium    │ 2-3 days  │ None
 15  │ os.sendfile/copy_file_range     │ Low-Medium    │ 1 day     │ None
 16  │ Python import optimization      │ Low           │ 1 day     │ None
 17  │ __slots__ for dataclasses       │ Low           │ 1 day     │ None
 18  │ asyncio migration               │ Low           │ 2 weeks   │ Testing
 19  │ Connection pooling              │ Low           │ 1 day     │ None
 20  │ Event-driven monitoring         │ Low           │ 2 days    │ None
```

### 6.2 Recommended Implementation Roadmap

```
Phase 1: Foundation (Week 1)
  ├── #3 Huge pages (1 day)
  ├── #4 cgroup v2 + kvm tuning (0.5 day)
  ├── #10 Kernel boot params tuning (1 hour)
  └── #13 os.posix_spawn() (1 day)
  → Result: 10-20% faster cold starts, foundation for snapshotting

Phase 2: Snapshot System (Weeks 2-4)
  ├── #1 Snapshot-based VM cloning (2-3 weeks)
  ├── #9 Custom minimal kernel (1-2 weeks, parallel)
  └── #5 Lightweight init (1-2 weeks, parallel)
  → Result: 50-200ms cold-start VM creation (90% reduction)

Phase 3: Hot-Standby Pool (Weeks 4-6)
  ├── #6 Overlayfs/btrfs CoW rootfs (1-2 weeks)
  ├── #8 Network resource pre-creation (1 week)
  ├── #2 VM hot-standby pool (3-4 weeks)
  ├── #7 API daemon/server mode (2-3 weeks, overlaps)
  └── #14 Pre-allocated resource pools (2-3 days, overlaps)
  → Result: Sub-100ms hot-pool VM creation

Phase 4: Polish (Weeks 6-8)
  ├── #11 nftables migration (3-5 days)
  ├── #12 Nuitka optimization (1 day)
  ├── #15 os.sendfile/copy_file_range (1 day)
  ├── #16 Python import optimization (1 day)
  ├── #17 __slots__ for dataclasses (1 day)
  ├── #20 Event-driven monitoring (2 days)
  └── #19 Connection pooling (1 day)
  → Result: Fully optimized system

Future (Density work, not speed)
  ├── #18 asyncio migration (for 500+ concurrent VMs)
  ├── Memory ballooning (for higher density)
  └── CPU pinning (for consistent performance)
```

### 6.3 Expected Performance Trajectory

```
Current:                   3-10s    VM creation (cold)
After Phase 1:             2-7s     (+10-20% from kernel tuning)
After Phase 2:             50-200ms (snapshot clone, minimal kernel, init)
After Phase 3:             10-50ms  (hot-standby pool, CoW rootfs, pre-alloc)
After Phase 4:             10-50ms  (+marginal from micro-optimizations)

Hot-pool VM creation:      10-50ms  (pop from pool + resume)
Cold-start VM creation:    50-200ms (snapshot clone)
Golden VM build (one-time): 3-10s   (first boot, paid once)
```

---

## 7. References

### Firecracker Official Documentation
- [Snapshot Support](https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/snapshot-support.md)
- [Network for Clones](https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/network-for-clones.md)
- [Production Host Setup](https://github.com/firecracker-microvm/firecracker/blob/main/docs/prod-host-setup.md)
- [Kernel Policy](https://github.com/firecracker-microvm/firecracker/blob/main/docs/kernel-policy.md)
- [Network Setup](https://github.com/firecracker-microvm/firecracker/blob/main/docs/network-setup.md)
- [Rootfs & Kernel Setup](https://github.com/firecracker-microvm/firecracker/blob/main/docs/rootfs-and-kernel-setup.md)
- [Huge Pages](https://github.com/firecracker-microvm/firecracker/blob/main/docs/hugepages.md)
- [Guest Kernel Configs](https://github.com/firecracker-microvm/firecracker/blob/main/resources/guest_configs/)
- [Boot Time Performance Tests](https://github.com/firecracker-microvm/firecracker/blob/main/tests/integration_tests/performance/test_boottime.py)

### Research Papers
- [Firecracker NSDI '20 Paper](https://www.usenix.org/conference/nsdi20/presentation/agache) — Academic paper detailing Firecracker architecture
- [How AWS Firecracker Works (Deep Dive)](https://unixism.net/2019/10/how-aws-firecracker-works-a-deep-dive/)

### Blog Posts & Case Studies
- [Fly Machines Blog](https://fly.io/blog/fly-machines/) — How Fly.io achieves fast VM starts
- [Fly Architecture](https://fly.io/docs/reference/architecture/) — Global architecture
- [Julia Evans: Firecracker in <1 Second](https://jvns.ca/blog/2021/01/23/firecracker--start-a-vm-in-less-than-a-second/) — Practical experiments
- [Firecracker Demo](https://github.com/firecracker-microvm/firecracker-demo) — Reference implementation

### Related Projects
- [Weave Ignite](https://github.com/weaveworks/ignite) (archived) — OCI images as Firecracker VMs
- [Flintlock](https://github.com/weaveworks-liquidmetal/flintlock) — Ignite successor
- [Firectl](https://github.com/firecracker-microvm/firectl) — Simple Firecracker CLI
- [AWS Lambda/Fargate](https://www.usenix.org/conference/nsdi20/presentation/agache) — Production Firecracker deployment

### Linux Kernel
- [Kernel Same-page Merging (KSM)](https://www.kernel.org/doc/html/latest/admin-guide/mm/ksm.html)
- [OverlayFS](https://www.kernel.org/doc/html/latest/filesystems/overlayfs.html)
- [Device Mapper Snapshot](https://www.kernel.org/doc/Documentation/device-mapper/snapshot.txt)
- [virtio-fs](https://www.kernel.org/doc/html/latest/filesystems/virtiofs.html)

### Python
- [Nuitka User Manual](https://nuitka.net/doc/user-manual.html)
- [Nuitka Performance](https://nuitka.net/doc/performance.html)
- [Python os.posix_spawn](https://docs.python.org/3/library/os.html#os.posix_spawn)
- [Python os.sendfile](https://docs.python.org/3/library/os.html#os.sendfile)
- [Python os.copy_file_range](https://docs.python.org/3/library/os.html#os.copy_file_range)
- [Python asyncio Subprocess](https://docs.python.org/3/library/asyncio-subprocess.html)

### Existing Project Docs
- [`fast-durable-image-copy.md`](fast-durable-image-copy.md) — Current image copy optimization
- [`guestfs_boot.md`](guestfs_boot.md) — Current guestfs optimization
- [`docs/analyses/pause_resume_implementation.md`](../analyses/pause_resume_implementation.md) — Pause/resume analysis
- [`docs/analyses/pause_resume_patterns.md`](../analyses/pause_resume_patterns.md) — Pause/resume patterns
- [`core/vm/_firecracker.py`](../../src/mvmctl/core/vm/_firecracker.py) — Firecracker spawner + snapshot API
- [`api/vm_operations.py`](../../src/mvmctl/api/vm_operations.py) — VM creation orchestration
