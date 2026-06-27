# Next-Level Optimization: Sub-100ms VM Creation

> **STATUS: Current — Go codebase.** This document describes forward-looking optimizations for the Go `mvmctl` implementation.
>
> **Goal:** Reduce VM creation from ~2-5s (typical) to <100ms for hot-pool VMs and <500ms for cold-start VMs, enabling high-density microVM operations.
>
> **Principle:** Go is NOT the bottleneck — Firecracker boot, disk I/O, and network setup are. The real gains come from **architectural changes**.
>
> **Fly.io Model:** "Create ahead, start fast" — pay the cost upfront, yield the benefit on every subsequent VM start.

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

These are already implemented in the Go codebase:

| Optimization | Where | Doc |
|---|---|---|
| tmpfs ready pool (pre-decompress images) ✅ | `internal/core/image/service.go:EnsureCached()` | [`fast-durable-image-copy.md`](fast-durable-image-copy.md) |
| sendfile(2) + io.Copy + fdatasync ✅ | `internal/core/image/service.go:MaterializeTo()` | [`fast-durable-image-copy.md`](fast-durable-image-copy.md) |
| libguestfs: direct backend, minimal vCPU/mem, tmpfs cache, kernel detection ✅ | `internal/lib/provisioner/guestfs/base.go` | [`guestfs-boot.md`](guestfs-boot.md) |
| Fixed appliance, disabled recovery/autosync, QEMU_LOCKING=off ✅ | `internal/lib/provisioner/guestfs/utils.go` | [`guestfs-boot.md`](guestfs-boot.md) |
| Loop-mount backend (faster than guestfs, primary) ✅ | `internal/lib/provisioner/loopmount/` + `internal/service/loopmount/` | `docs/RUNTIME.md` |
| Atomic firewall rule sync (nftables default, iptables fallback) ✅ | `internal/lib/firewall/` | [`network-sync-atomicity.md`](network-sync-atomicity.md) |
| Firecracker snapshot/resume API (SnapshotCreate, SnapshotRestore) ✅ | `internal/core/vm/controller.go` | — |
| Compiled binary (Go) — no interpreter startup overhead ✅ | `cmd/mvm/main.go` | — |
| Boot args builder with user overrides ✅ | `internal/core/vm/firecracker.go:bootArgsBuilder` | — |
| Kernel building with custom configs ✅ | `internal/core/kernel/service.go` | — |

### 1.2 Current VM Creation Timing Profile (Go)

```
[resolution]          ~5ms     (DB queries, validation)
[network_setup]       ~250ms   (ip link, bridge setup, firewall)
[image_clone]         ~300ms   (sendfile from tmpfs, ~400MB)
[provisioner_setup]   ~200ms   (loopmount subprocess)
[provisioner_run]     ~300ms   (tar injection, SSH keys, DNS, resize)
[firecracker_spawn]   ~400ms   (Firecracker process + kernel boot)
[console_setup]       ~30ms    (PTY + socket relay)
─────────────────────────────────────────────────────
TOTAL:                ~1.5s    (best case, Alpine, fast NVMe)
                      ~3-5s    (typical, Ubuntu with systemd)
```

### 1.3 Root Cause — What Actually Takes Time

| Phase | Time | Bottleneck | Language Impact |
|---|---|---|---|
| `image_clone` | 100-300ms | Disk I/O (sendfile ~400MB sparse image) | **None** — I/O bound |
| `provisioner_*` | 500-2000ms | Disk I/O (loopmount resize + write) | **None** — I/O bound |
| `firecracker_spawn` | 100-800ms | Kernel boot + init system | **None** — kernel time |
| `network_setup` | 50-250ms | Subprocess (ip, iptables/nftables) | **Minimal** — subprocess spawn |
| Go overhead | ~2ms | Compiled binary, no GC pause at startup | Already minimal |

---

## 2. Tier 1 — Transformative (The Big Levers)

These are the optimizations that can **radically change** the performance profile. Each can reduce VM creation time by **50-90%** on their own.

### 2.1 Snapshot-Based VM Cloning ⏳ NOT IMPLEMENTED

**What it is:** Boot a single "golden" VM with the complete application stack. Pause it at the point where the guest OS has finished booting. Take a full Firecracker snapshot (memory + state file). Create new VMs by loading that snapshot instead of booting fresh.

**Why it helps:** This is the single biggest optimization possible. Firecracker snapshot loading is optimized for speed via `MAP_PRIVATE` with on-demand page loading. From the Firecracker docs: *"Loading a snapshot takes 3-8ms"* — compared to 500-2000ms for a full boot with systemd.

**Bottleneck addressed:** Kernel boot + init system (500-2000ms)

**Feasibility:** Medium
- ✅ `create_snapshot()` and `load_snapshot()` already exist in `VMController` (`internal/core/vm/controller.go`)
- ✅ `model.VMState.PAUSED` already exists in the model
- ✅ The Firecracker API `PUT /snapshot/create` and `PUT /snapshot/load` are already implemented
- ❌ Need: golden VM builder, snapshot storage, per-clone reconfiguration
- ❌ Need: per-VM uniqueness handling (MAC, IP, hostname, SSH keys)
- ❌ Need: Snapshot domain (model, repository, CLI commands)

**Estimated impact:** Transformative — 3000ms → 50-200ms

### 2.2 VM Hot-Standby Pool ⏳ NOT IMPLEMENTED

**What it is:** A pool manager that keeps N pre-booted, paused VMs ready to assign. When a new VM is requested, the pool pops one, runs post-resume configuration, and hands it to the user. Pool replenishment runs in the background.

**Why it helps:** This is exactly how Fly.io achieves ~300ms cross-region VM starts. The "create" path (slow: image pull, host selection, DB persistence) is separated from the "start" path (fast: just resume a pre-allocated VM). The critical path is just Firecracker snapshot resume (3-8ms) + minimal configuration.

**Bottleneck addressed:** All of them — every slow path is done ahead of time

**Feasibility:** Hard (largest architectural change)
- Requires a persistent pool manager (daemon or background thread)
- Each pre-booted VM needs pre-allocated, unique resources (TAP, IP, MAC, socket)
- Post-resume guest reconfiguration (hostname, SSH keys, TLS certs, entropy)
- Pool replenishment must be async — create new replacement VMs after each pop
- Pool sizing strategy: min/max pool size, replenishment rate

**Note:** The Go codebase already has the `internal/core/vm/controller.go` pause/resume infrastructure. What's missing is the pool orchestration layer. The API daemon (Tier 2, #3.4) is a prerequisite for an efficient hot-standby pool.

**Estimated impact:** Transformative — sub-100ms "create" for existing pool, ~10-30ms for warm pool hit

### 2.3 Lightweight Init (Inside Guest) ⏳ NOT IMPLEMENTED

**What it is:** Replace systemd (200-2000ms boot time) with a minimal init system inside the VM rootfs. Options: BusyBox init, OpenRC, custom static binary as PID 1, or `init=/path/to/app`.

**Bottleneck addressed:** Userspace init (200-2000ms)

**Feasibility:** Medium — requires building custom rootfs images

---

## 3. Tier 2 — High Impact

### 3.1 Huge Pages (2MB/1GB) ⏳ NOT IMPLEMENTED

**What it is:** Back VM guest memory with 2MB or 1GB huge pages instead of standard 4KB pages. Set via Firecracker's `/machine-config` API with `huge_pages: "2M"`.

**Bottleneck addressed:** Memory virtualization overhead during boot

**Feasibility:** Easy
1. Pre-allocate huge pages on the host
2. Pass through configuration or Drop-in option
3. Set in Firecracker machine config via `internal/core/vm/firecracker.go` FirecrackerConfigManager

**Estimated impact:** High — up to 50% boot time reduction

### 3.2 cgroup v2 + `favordynmods` + `kvm.nx_huge_pages=never` ⏳ NOT IMPLEMENTED

**What it is:** Three related kernel-level optimizations that dramatically affect Firecracker startup time, especially snapshot restore.

**Bottleneck addressed:** KVM + cgroup overhead (8.5ms for snapshot restore)

**Feasibility:** Easy — host-level configuration only

### 3.3 Overlayfs / btrfs / ZFS CoW Rootfs ⏳ NOT IMPLEMENTED

**What it is:** Instead of copying the full rootfs image (even with sendfile(2), the full sparse file is still written), use a copy-on-write filesystem to create instant, zero-copy rootfs views per VM.

**Bottleneck addressed:** Rootfs materialization (100-300ms)

**Feasibility:** Medium

**Current approach:** The Go codebase uses `sendfile(2)` for in-kernel zero-copy transfer, which avoids userspace copies but still writes all the data. The warm pool on tmpfs means the first access is from RAM, but the destination write to disk is unavoidable with non-CoW filesystems. btrfs subvolume snapshots or device-mapper would be the next step.

### 3.4 API Daemon / Server Mode ⏳ NOT IMPLEMENTED

**What it is:** Instead of running `mvm create vm-name` as a one-shot CLI command, run a persistent daemon that keeps everything warm.

**Bottleneck addressed:** CLI startup (~2ms, already minimal in Go), cold resource allocation

**Feasibility:** Medium — the Go binary is already compiled, so startup overhead is tiny (~2ms). The main benefit is keeping network resources, cached kernel configs, and connection pools warm between commands. The service subprocess infrastructure (console relay, nocloudnet server, loopmount provisioner) already exists in `internal/service/`.

### 3.5 Custom Minimal Kernel ⚠️ PARTIALLY IMPLEMENTED

**What it is:** Build a custom Linux kernel with only the drivers and features needed for Firecracker. Firecracker provides recommended guest kernel configs as a starting point.

**Bottleneck addressed:** Kernel boot (50-200ms)

**What already exists:**
- `internal/core/kernel/service.go` already handles kernel building with custom configs
- The `kernel pull` command already supports official kernels
- Custom kernel config merging (user config overrides on top of base config) is supported
- ❌ No minimal kernel config is maintained in the project — users must provide their own

### 3.6 Network Resource Pre-creation ⏳ NOT IMPLEMENTED

**Bottleneck addressed:** Network setup (50-250ms per VM)

---

## 4. Tier 3 — Medium Impact

### 4.1 Kernel Boot Parameters Tuning ✅ PARTIALLY IMPLEMENTED

**What it is:** Additional kernel command-line parameters to disable unnecessary hardware probing and security features in the guest.

**Status:** ✅ PARTIALLY IMPLEMENTED

**Current boot args** (`internal/infra/constants.go:77`):
```
console=ttyS0 reboot=k panic=1 net.ifnames=0 rw rootwait quiet loglevel=3 no_timer_check clocksource=kvm-clock systemd.show_status=false
```

The following have been added since the original design:
- `no_timer_check` — skip PIT timer check
- `clocksource=kvm-clock` — use KVM clock
- `systemd.show_status=false` — suppress systemd status output
- `quiet loglevel=3` — suppress kernel logs

Additional safe params that could still be added: `audit=0`, `elevator=noop`.

### 4.2 sendfile(2) for File I/O ✅ IMPLEMENTED

**What it is:** Use `sendfile(2)` for zero-copy kernel-mediated file transfers instead of userspace read+write.

**Status:** ✅ IMPLEMENTED — `internal/infra/io.go:copyViaSendfile()` uses `unix.Sendfile()` as the primary copy mechanism in `MaterializeTo()`. Falls back to `io.Copy`. Both paths end with `fdatasync`.

### 4.3 nftables Migration ✅ IMPLEMENTED

**What it is:** Replaces `iptables` commands with `nft` (nftables) commands.

**Status:** ✅ IMPLEMENTED — `internal/lib/firewall/nftables.go` provides a full `NFTablesTracker` implementation. `internal/lib/firewall/tracker.go` abstracts both backends as `FirewallTracker`. The default `firewall_backend` in config is `"nftables"`. nftables is the default; iptables is the legacy fallback configured via the `firewall_backend` setting.

### 4.4 Compiled Binary (Go) ✅ IMPLEMENTED

**What it is:** The project is now written in Go, producing a single compiled binary. This replaces the Python+Nuitka compilation model entirely.

**Status:** ✅ IMPLEMENTED — `cmd/mvm/main.go` produces `mvm` via `go build`. No interpreter startup overhead, no import time cost, immediate command execution.

### 4.5 os.posix_spawn() for Firecracker Subprocess ⏳ NOT IMPLEMENTED

**What it would be (Go equivalent):** Use `syscall.ForkExec()` or `os.StartProcess()` with pre-configured file descriptors instead of `exec.Command()` for Firecracker process creation.

**Current approach:** Uses `system.RunCmd` / `system.RunCmdOpts` (wrapping `exec.Command`) in the Firecracker spawner (`internal/core/vm/firecracker.go`). The spawner uses `SysProcAttr.Pdeathsig` for process lifecycle management.

**Feasibility:** Low impact — Go's `os/exec` is already efficient for process spawning. The overhead is negligible compared to QEMU/Firecracker boot time.

### 4.6 Pre-Allocated Resource Pools (General) ⏳ NOT IMPLEMENTED

---

## 5. Tier 4 — Low Impact / Micro-Otimizations (Not Applicable in Go)

These legacy optimizations are **not applicable** to the Go codebase:

| Legacy Optimization | Why Not Applicable |
|---|---|
| Import optimization (LazyMVMGroup) | Go compiles to a single binary — no import time cost |
| Nuitka compilation | Replaced by native Go compilation |
| `__slots__` for dataclasses | Go structs are already compile-time fixed-layout |
| `asyncio` for concurrent subprocess management | Go uses goroutines + `context.Context` natively |
| `os.sendfile() / os.copy_file_range()` | Already using `unix.Sendfile()` — see Tier 3 above |

Instead, the Go equivalents are:

| Go-Specific Micro-Optimization | Status |
|---|---|
| Connection pooling for Firecracker API client (`internal/lib/firecracker/client.go`) | ❌ Not done — single-use HTTP client per VM |
| Event-driven VM state monitoring (vs polling) | ❌ Not done |
| Memory ballooning (Firecracker API) | ❌ Not done |
| CPU pinning (Firecracker machine config) | ❌ Not done |

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
  9  │ Custom minimal kernel           │ High          │ 1-2 weeks │ ⚠️ Partial
 10  │ Kernel boot params tuning       │ Medium        │ 1 hour    │ ⚠️ Partial
 11  │ nftables migration              │ Medium        │ 3-5 days  │ ✅ Done
 12  │ sendfile(2) for file I/O        │ Medium        │ 1 day     │ ✅ Done
 13  │ Compiled binary (Go)            │ Low           │ N/A       │ ✅ Done
 14  │ Pre-allocated resource pools    │ Low-Medium    │ 2-3 days  │ ❌ Not done
```

### 6.2 Expected Performance Trajectory

```
Current:                   1.5-5s   VM creation (cold)
After Phase 1:             1-3s     (+10-20% from kernel tuning)
After Phase 2:             50-200ms (snapshot clone, minimal kernel, init)
After Phase 3:             10-50ms  (hot-standby pool, CoW rootfs, pre-alloc)
After Phase 4:             10-50ms  (+marginal from micro-optimizations)

Hot-pool VM creation:      10-50ms  (pop from pool + resume)
Cold-start VM creation:    50-200ms (snapshot clone)
Golden VM build (one-time): 1-5s    (first boot, paid once)
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
- [`network-sync-atomicity.md`](network-sync-atomicity.md) — Firewall sync optimization ✅
- `internal/core/vm/controller.go` — VM controller with pause/resume/snapshot API
- `internal/core/vm/firecracker.go` — Firecracker spawner + boot args builder
- `docs/improvements/IMPROVEMENTS_008.md` — Snapshot-based VM cloning design
