# Next-Level Optimization: Sub-100ms VM Creation

> **STATUS: Partially outdated** — Section 4.3 (nftables migration) is now implemented. The `_nftables_tracker/` module and `FirewallTracker` abstraction at `core/_shared/_firewall_tracker.py` provide nftables support. Default firewall backend is `"nftables"`. All other sections remain accurate as forward-looking optimizations.
> 
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

### 1.1 Existing Optimizations (Already Done) ✅

These are already implemented in the codebase and will NOT be covered here:

| Optimization | Where | Doc |
|---|---|---|
| tmpfs ready pool (pre-decompress images to `/dev/shm`) ✅ | `core/image/_service.py` | [`fast-durable-image-copy.md`](fast-durable-image-copy.md) |
| reflink + sparse copy with `fdatasync()` ✅ | `core/image/_service.py:materialize_to()` | [`fast-durable-image-copy.md`](fast-durable-image-copy.md) |
| libguestfs: direct backend, `cachemode=writeback`, minimal vCPU/mem ✅ | `core/_shared/_provisioner/_backend.py` | [`guestfs-boot.md`](guestfs-boot.md) |
| Fixed appliance, disabled recovery/autosync ✅ | `core/_shared/_guestfs/_base.py`, `_service.py` | [`guestfs-boot.md`](guestfs-boot.md) |
| Loop-mount backend (mvm-provision binary, faster than guestfs) ✅ | `core/_shared/_loopmount/` | — |
| ThreadPoolExecutor for batch VM operations ✅ | `core/_shared/_parallel.py`, `core/vm/_service.py` | — |
| Firecracker snapshot/resume API (create_snapshot, load_snapshot) ✅ | `core/vm/_firecracker.py` | — |
| Progress reporting + built-in timing logs ✅ | `api/vm_operations.py` | — |
| Lazy CLI module loading (LazyMVMGroup) ✅ | `main.py` | — |
| Firecracker native PCI support (`--enable-pci`) ✅ | `core/vm/_firecracker.py` | — |

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

### 2.1 Snapshot-Based VM Cloning ⏳ NOT IMPLEMENTED

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
- ✅ `create_snapshot()` and `load_snapshot()` already exist in `FirecrackerSpawner` and `VMController`
- ✅ `VMState.PAUSED` already exists in the model
- ✅ The Firecracker API `PUT /snapshot/create` and `PUT /snapshot/load` are already implemented
- ❌ Need: golden VM builder, snapshot storage, per-clone reconfiguration
- ❌ Need: per-VM uniqueness handling (MAC, IP, hostname, SSH keys)
- ❌ Need: Snapshot domain (model, repository, CLI commands)

**Estimated impact:** Transformative — 3000ms → 50-200ms

**See also:** [`IMPROVEMENTS_008.md`](../improvements/IMPROVEMENTS_008.md) for full design

### 2.2 VM Hot-Standby Pool ⏳ NOT IMPLEMENTED

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

### 2.3 Lightweight Init (Inside Guest) ⏳ NOT IMPLEMENTED

**What it is:** Replace systemd (200-2000ms boot time) with a minimal init
system inside the VM rootfs. Options: BusyBox init, OpenRC, custom static
binary as PID 1, or `init=/path/to/app`.

**Bottleneck addressed:** Userspace init (200-2000ms)

**Feasibility:** Medium — requires building custom rootfs images

---

## 3. Tier 2 — High Impact

### 3.1 Huge Pages (2MB/1GB) ⏳ NOT IMPLEMENTED

**What it is:** Back VM guest memory with 2MB or 1GB huge pages instead of
standard 4KB pages. Set via Firecracker's `/machine-config` API with
`huge_pages: "2M"`.

**Bottleneck addressed:** Memory virtualization overhead during boot

**Feasibility:** Easy
1. Pre-allocate huge pages on the host
2. Pass through `HUGE_PAGES` config or Drop-in option
3. Set in Firecracker machine config

**Estimated impact:** High — up to 50% boot time reduction

### 3.2 cgroup v2 + `favordynmods` + `kvm.nx_huge_pages=never` ⏳ NOT IMPLEMENTED

**What it is:** Three related kernel-level optimizations that dramatically
affect Firecracker startup time, especially snapshot restore.

**Bottleneck addressed:** KVM + cgroup overhead (8.5ms for snapshot restore)

**Feasibility:** Easy — host-level configuration only

### 3.3 Overlayfs / btrfs / ZFS CoW Rootfs ⏳ NOT IMPLEMENTED

**What it is:** Instead of copying the full rootfs image (even with reflink,
you still need to clone the data), use a copy-on-write filesystem to create
instant, zero-copy rootfs views per VM.

**Bottleneck addressed:** Rootfs materialization (100-500ms)

**Feasibility:** Medium

**Current approach:** The codebase uses reflink (`--reflink=auto`) which already
provides CoW on btrfs/XFS. However, it still creates a full copy-on-write clone,
not a true view into the same file. btrfs subvolume snapshots or device-mapper
would be the next step.

### 3.4 API Daemon / Server Mode ⏳ NOT IMPLEMENTED

**What it is:** Instead of running `mvm create vm-name` as a one-shot CLI
command, run a persistent daemon that keeps everything warm.

**Bottleneck addressed:** CLI startup (~50ms), cold resource allocation

### 3.5 Custom Minimal Kernel ⏳ NOT IMPLEMENTED

**What it is:** Build a custom Linux kernel with only the drivers and features
needed for Firecracker. Firecracker provides recommended guest kernel configs
as a starting point.

**Bottleneck addressed:** Kernel boot (50-200ms)

**What already exists:**
- `core/kernel/_service.py` already handles kernel building with custom configs
- The `kernel pull` command already supports official kernels
- Custom kernel config merging is supported

### 3.6 Network Resource Pre-creation ⏳ NOT IMPLEMENTED

**Bottleneck addressed:** Network setup (50-300ms per VM)

---

## 4. Tier 3 — Medium Impact

### 4.1 Kernel Boot Parameters Tuning ✅ PARTIALLY IMPLEMENTED

**What it is:** Additional kernel command-line parameters to disable unnecessary
hardware probing and security features in the guest.

**Status:** ✅ PARTIALLY IMPLEMENTED

The current `boot_args` in `constants.py` (line 45):
```
console=ttyS0 reboot=k panic=1 net.ifnames=0 rw rootwait quiet loglevel=3
```

Additional safe params still pending (`no_timer_check`, `audit=0`, `clocksource=kvm-clock`, `elevator=noop`).

### 4.2 os.sendfile() / os.copy_file_range() for File I/O ⏳ NOT IMPLEMENTED

**What it is:** Replace Python's `open() + read() + write()` with zero-copy
kernel-mediated file transfers.

**Current approach:** The `cp` command with `--reflink=auto --sparse=always` is
already well-optimized. This is only relevant for fallback paths.

### 4.3 nftables Migration ✅ IMPLEMENTED

**What it is:** Replaces `iptables` commands with `nft` (nftables) commands.

**Status:** ✅ IMPLEMENTED — `core/_shared/_nftables_tracker/` provides a full NFTablesTracker implementation. `core/_shared/_firewall_tracker.py` abstracts both backends. The default `firewall_backend` in config is `"nftables"`. nftables is the default; iptables is the fallback when the nft_chain_nat kernel module is unavailable.

### 4.4 Nuitka Compilation Optimization ✅ IMPLEMENTED

**What it is:** Tune Nuitka compilation flags for maximum performance, not just
fast builds.

**Status:** ✅ IMPLEMENTED — The project uses `nuitka` with `--onefile`, `--lto=yes`, `--clang`, and `--jobs=$(nproc)`.

### 4.5 os.posix_spawn() for Firecracker Subprocess ⏳ NOT IMPLEMENTED

**What it is:** Use `os.posix_spawn()` instead of `subprocess.Popen()` for
Firecracker process creation.

**Current approach:** Uses `subprocess.Popen()` in the firecracker spawner.

### 4.6 Pre-Allocated Resource Pools (General) ⏳ NOT IMPLEMENTED

---

## 5. Tier 4 — Low Impact / Micro-Optimizations

### 5.1 Python Import Optimization ✅ PARTIALLY IMPLEMENTED

**Status:** ✅ PARTIALLY IMPLEMENTED — `LazyMVMGroup` lazy-loads CLI modules. Further optimization possible.

### 5.2 `__slots__` for Dataclasses ⏳ NOT IMPLEMENTED

### 5.3 asyncio for Concurrent Subprocess Management ⏳ NOT IMPLEMENTED

### 5.4 Connection Pooling for Firecracker API ⏳ NOT IMPLEMENTED

### 5.5 Event-Driven VM State Monitoring ⏳ NOT IMPLEMENTED

### 5.6 Memory Ballooning ⏳ NOT IMPLEMENTED

### 5.7 CPU Pinning ⏳ NOT IMPLEMENTED

---

## 6. Priority Matrix & Implementation Roadmap

### 6.1 Priority Matrix — Current Implementation Status

```
Rank │ Optimization                    │ Impact       │ Effort    │ Status
─────┼─────────────────────────────────┼──────────────┼───────────┼─────────────
  1  │ Snapshot-based VM cloning       │ Transformative│ 2-3 weeks │ ❌ Not done
  2  │ VM hot-standby pool             │ Transformative│ 3-4 weeks │ ❌ Not done
  3  │ Huge pages (2MB)                │ High          │ 1 day     │ ❌ Not done
  4  │ cgroup v2 + kvm tuning          │ High          │ 0.5 day   │ ❌ Not done
  5  │ Lightweight init (guest)        │ Transformative│ 1-2 weeks │ ❌ Not done
  6  │ Overlayfs/btrfs CoW rootfs      │ High          │ 1-2 weeks │ ❌ Not done
  7  │ API daemon/server mode          │ High          │ 2-3 weeks │ ❌ Not done
  8  │ Network resource pre-creation   │ High          │ 1 week    │ ❌ Not done
  9  │ Custom minimal kernel           │ High          │ 1-2 weeks │ ❌ Not done
 10  │ Kernel boot params tuning       │ Medium        │ 1 hour    │ ⚠️ Partial
  11  │ nftables migration              │ Medium        │ 3-5 days  │ ✅ Done
 12  │ Nuitka optimization             │ Low           │ 1 day     │ ✅ Done
 13  │ os.posix_spawn()                │ Low-Medium    │ 1 day     │ ❌ Not done
 14  │ Pre-allocated resource pools    │ Low-Medium    │ 2-3 days  │ ❌ Not done
 15  │ os.sendfile/copy_file_range     │ Low-Medium    │ 1 day     │ ❌ Not done
 16  │ Python import optimization      │ Low           │ 1 day     │ ⚠️ Partial
 17  │ __slots__ for dataclasses       │ Low           │ 1 day     │ ❌ Not done
 18  │ asyncio migration               │ Low           │ 2 weeks   │ ❌ Not done
 19  │ Connection pooling              │ Low           │ 1 day     │ ❌ Not done
 20  │ Event-driven monitoring         │ Low           │ 2 days    │ ❌ Not done
```

### 6.2 Expected Performance Trajectory

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

### Existing Project Docs
- [`fast-durable-image-copy.md`](fast-durable-image-copy.md) — Current image copy optimization ✅
- [`guestfs-boot.md`](guestfs-boot.md) — Current guestfs optimization ✅
- [`docs/analyses/pause_resume_implementation.md`](../analyses/pause_resume_implementation.md) — Pause/resume analysis
- [`core/vm/_firecracker.py`](../../src/mvmctl/core/vm/_firecracker.py) — Firecracker spawner + snapshot API
- [`api/vm_operations.py`](../../src/mvmctl/api/vm_operations.py) — VM creation orchestration
