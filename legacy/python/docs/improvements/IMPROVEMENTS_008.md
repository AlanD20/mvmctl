# Snapshot-Based Instant VM Cloning & Next-Level Optimizations

> **STATUS: Current — not implemented (as documented).** Building blocks exist (create_snapshot/load_snapshot, ParallelExecutor), but no snapshot domain, CLI commands, pool manager, or CoW rootfs.

> ## Status: ❌ NOT IMPLEMENTED (Design Document Only)
>
> None of the phases described here have been implemented. The snapshot API exists in `FirecrackerSpawner`/`VMController` (create_snapshot, load_snapshot), but no snapshot domain, CLI commands, pool manager, or CoW rootfs backend exists.
>
> | Section | Status |
> |---------|--------|
> | Snapshot Domain (model, repo, CLI) | ❌ |
> | vm create --snapshot path | ❌ |
> | VM Hot-Standby Pool | ❌ |
> | Overlayfs / CoW Rootfs | ❌ |
> | All phases (1-4) | ❌ |
>
> **What DOES exist (building blocks):**
> - `FirecrackerSpawner.create_snapshot()` / `load_snapshot()` ✅
> - `VMController.snapshot()` / `load_snapshot()` ✅
> - `VMStatus.PAUSED` in models ✅
> - `--reflink=auto` for CoW on btrfs/XFS ✅
> - `ParallelExecutor` for batch operations ✅
>
> **Last verified:** 2026-05-13

---

**Phase:** Multi-phase — spans 3-4 milestones  
**Complexity:** Very High  
**Depends on:** Host kernel config (huge pages, cgroup v2), Firecracker v1.12+

---

## Table of Contents

- [1. Snapshot-Based VM Cloning (The Big One)](#1-snapshot-based-vm-cloning-the-big-one)
  - [1.1 How Firecracker Snapshots Work](#11-how-firecracker-snapshots-work)
  - [1.2 Snapshot Domain](#12-snapshot-domain)
  - [1.3 `vm create --snapshot`](#13-vm-create---snapshot)
  - [1.4 Network Uniqueness for Clones](#14-network-uniqueness-for-clones)
  - [1.5 Memory Determinism & Security](#15-memory-determinism--security)
- [2. VM Hot-Standby Pool](#2-vm-hot-standby-pool)
- [3. Overlayfs / CoW Rootfs](#3-overlayfs--cow-rootfs)
- [4. Implementation Roadmap](#4-implementation-roadmap)

---

## 1. Snapshot-Based VM Cloning (The Big One) ❌ NOT IMPLEMENTED

### 1.1 How Firecracker Snapshots Work

#### The Two Files

When `PUT /snapshot/create` is called on a **paused** microVM, Firecracker produces two files:

```
/home/user/.cache/mvmctl/snapshots/my-snapshot/
├── vm.mem           ← Raw dump of ALL guest RAM pages
│                      Size ≈ VM configured RAM (1GB VM = ~1GB file)
│                      NOT compressed, NOT sparse (for full snapshots)
│
├── vm.vmstate       ← Serde bitcode binary format
│                      Size ≈ few KB to few hundred KB
│                      Contains:
│                        • 64-bit magic_id (Firecracker + arch)
│                        • Version field (MAJOR.MINOR.PATCH)
│                        • Bitcode blob: KVM vCPU registers,
│                          device emulation state, Firecracker
│                          internal state (vCPUs, virtio devices,
│                          i8042, serial, RTC, etc.)
│                        • Optional 64-bit CRC64 checksum
│
├── rootfs.ext4      ← Frozen rootfs image at snapshot time
│                      (copy of the VM's root block device)
│
└── metadata.json    ← Origin, kernel, image, timestamps
```

#### The mmap Secret: MAP_PRIVATE

When `PUT /snapshot/load` is called:

```c
// Firecracker does this internally:
mem_fd = open("vm.mem", O_RDONLY);
mmap(NULL, guest_ram_size, PROT_READ | PROT_WRITE,
     MAP_PRIVATE, mem_fd, 0);
close(mem_fd);
```

- **Pages are NOT read eagerly** — they're demand-paged (loaded on first access)
- The original `vm.mem` file is **never modified** — writes go to anonymous COW pages
- **Multiple processes can MAP_PRIVATE the same file simultaneously** — each gets independent COW
- Loading the snapshot takes **~3-8ms** (just the mmap syscall + metadata), not the full RAM size

#### UFFD (userfaultfd) for Huge Pages

When using 2MB huge pages, snapshot restore **requires** UFFD:
1. A dedicated handler process listens on a Unix domain socket
2. Firecracker gets a UFFD fd, mmaps guest memory anonymously, registers with UFFD
3. The handler privately mmaps the memory file
4. On page fault, handler issues `UFFDIO_COPY` to load the page
5. Only `File` and `Uffd` backend types are supported with huge pages

#### Full Snapshot → Clone Lifecycle

```
┌──────────────────────────────────────────────────────────────────┐
│ PHASE 1: Create Golden Snapshot (one-time)                       │
│                                                                  │
│ 1. Boot a base VM normally (--image ubuntu-24.04 --kernel ...)  │
│ 2. Wait for complete boot (cloud-init finishes, SSH ready)       │
│ 3. OPTIONAL: Clean up unique state (see §2.5)                    │
│ 4. PATCH /vm → {"state": "Paused"}       ← freeze vCPUs          │
│ 5. PUT /snapshot/create → {                                      │
│      "mem_file_path": "/snapshots/golden/vm.mem",                │
│      "snapshot_path": "/snapshots/golden/vm.vmstate"             │
│    }                                                             │
│ 6. Copy rootfs to snapshot dir                                   │
│ 7. Terminate the golden VM                                       │
│ 8. Store metadata (kernel, image, created_at, mem_size, etc.)    │
│                                                                  │
│ PHASE 2: Clone from Snapshot (per-VM, ~50-200ms)                 │
│                                                                  │
│ 1. Pre-allocate: TAP device, IP, MAC, socket path, cgroup       │
│ 2. Create CoW view of rootfs (see §4)                            │
│ 3. Start Firecracker WITHOUT --config-file (snapshot mode)       │
│ 4. PUT /logger, PUT /metrics (pre-boot config)                   │
│ 5. PUT /snapshot/load → {                                        │
│      "mem_file_path": "/snapshots/golden/vm.mem",                │
│      "snapshot_path": "/snapshots/golden/vm.vmstate",            │
│      "resume_vm": false,                                         │
│      "network_overrides": [                                      │
│        {"iface_id": "eth0", "host_dev_name": "vmtap-clone-01"}   │
│      ]                                                           │
│    }                                                             │
│ 6. PATCH /vm → {"state": "Resumed"}       ← vCPUs start          │
│ 7. Post-resume: via vsock agent → regenerate SSH keys,          │
│    set hostname, configure IP                                    │
│ 8. DB write: record VM with status RUNNING                       │
└──────────────────────────────────────────────────────────────────┘
```

### 1.2 Snapshot Domain ❌ NOT IMPLEMENTED

A new domain `snapshot` with full CRUD lifecycle.

#### SnapshotItem Model

```python
@dataclass
class SnapshotItem:
    """A frozen point-in-time copy of a VM."""
    id: str                         # Hash-based unique ID
    name: str                       # User-visible name (e.g. "ubuntu-base")
    source_vm_id: str | None        # VM this was taken from
    source_vm_name: str | None
    image_id: str                   # Base image used
    kernel_id: str                  # Kernel used
    memory_mib: int                 # Guest RAM size
    vcpus: int                      # vCPU count
    mem_file_path: str              # Path to vm.mem
    vmstate_file_path: str          # Path to vm.vmstate
    rootfs_path: str                # Path to frozen rootfs
    rootfs_size_bytes: int
    disk_size_bytes: int            # Original disk size
    created_at: str                 # ISO timestamp
```

#### CLI Commands ❌ NOT IMPLEMENTED

```
# Create a snapshot from a running VM
mvm snapshot create <name> --vm <vm-id> [--diff] [--leave-paused]

# List snapshots
mvm snapshot list [--json]

# Get/inspect a snapshot
mvm snapshot get <name> [--json]

# Remove a snapshot (free disk space)
mvm snapshot rm <name> [--force]
```

### 1.3 `vm create --snapshot` ❌ NOT IMPLEMENTED

### 1.4 Network Uniqueness for Clones ❌ NOT IMPLEMENTED

### 1.5 Memory Determinism & Security ❌ NOT IMPLEMENTED

---

## 2. VM Hot-Standby Pool ❌ NOT IMPLEMENTED

### What It Is

A pool manager that keeps N pre-booted, paused VMs ready to assign. When a VM is requested, it pops one from the pool, runs post-resume configuration, and hands it to the user. Pool replenishment runs in the background.

This is the Fly.io pattern: "Create ahead, start fast."

### Architecture

```
┌──────────────────────────────────────────────────────┐
│ mvm-d (daemon process)                                │
│                                                        │
│  ┌─────────────────────────────────────────────────┐  │
│  │ VM Hot-Standby Pool                              │  │
│  │                                                   │  │
│  │  ┌──────────┐  ┌──────────┐  ┌──────────┐       │  │
│  │  │ Paused   │  │ Paused   │  │ Paused   │  ...   │  │
│  │  │ Clone 1  │  │ Clone 2  │  │ Clone N  │       │  │
│  │  │ (golden  │  │ (golden  │  │ (golden  │       │  │
│  │  │  clone)  │  │  clone)  │  │  clone)  │       │  │
│  │  └────┬─────┘  └────┬─────┘  └────┬─────┘       │  │
│  │       │              │              │             │  │
│  │       └──────────────┴──────────────┘             │  │
│  │              ▲ Pool: queue.Queue()                 │  │
│  └─────────────────────────────────────────────────┘  │
│                                                        │
│  ┌─────────────────────────────────────────────────┐  │
│  │ Replenisher (background thread)                  │  │
│  │  • When pool drops below min, spawn new clone    │  │
│  │  • Load snapshot → configure → pause → push      │  │
│  └─────────────────────────────────────────────────┘  │
│                                                        │
│  ┌─────────────────────────────────────────────────┐  │
│  │ Resource Pools                                    │  │
│  │  • TAP devices (pre-created, pre-ruled)           │  │
│  │  • IP addresses (pre-allocated bitmap)             │  │
│  │  • MAC addresses (counter-based, pre-generated)    │  │
│  │  • Socket paths (pre-allocated)                    │  │
│  │  • CoW rootfs dirs (pre-created)                   │  │
│  └─────────────────────────────────────────────────┘  │
└──────────────────────────────────────────────────────┘
```

### Pool Manager Interface

```python
class VMHotStandbyPool:
    """Maintains a pool of pre-booted, paused microVMs.

    Pool sizing:
    - keep min_pool_size → max_pool_size VMs ready at all times
    - If acquire() depletes below min, background replenishment starts
    - If pool is empty, acquire() blocks and creates a fresh clone
    """

    def __init__(
        self,
        snapshot_name: str,
        *,
        min_pool_size: int = 3,
        max_pool_size: int = 20,
        network_pool_size: int = 50,
        db: Database,
    ):
        self._snapshot = SnapshotRepository(db).get_by_name(snapshot_name)
        self._pool: queue.Queue[PooledVM] = queue.Queue()
        self._net_pool = NetworkResourcePool(network_pool_size, db=db)
        self._replenisher = ThreadPoolExecutor(max_workers=2)
        self._prefill(min_pool_size)

    def acquire(self, name: str, ssh_keys: list[SSHKeyItem]) -> PooledVM:
        """Pop a ready VM. Returns immediately if pool non-empty."""
        if self._pool.empty():
            return self._create_clone(name, ssh_keys)  # slow path
        vm = self._pool.get_nowait()
        vm.configure(name=name, ssh_keys=ssh_keys)
        self._replenisher.submit(self._create_replacement)
        return vm

    def _create_replacement(self) -> None:
        """Background: create a new clone for the pool."""
        # 1. Pre-allocate network resources
        net = self._net_pool.acquire()
        # 2. Create CoW rootfs
        rootfs = CoWUtils.create_cow_view(...)
        # 3. Start Firecracker, load snapshot, resume
        fc = FirecrackerSpawner(...)
        fc.spawn()
        fc.load_snapshot(...)
        fc.resume_vm()
        # 4. Configure guest (init script handles uniqueness)
        fc.vsock_configure(hostname=f"pool-{uuid4().hex[:8]}")
        # 5. Pause for pooling
        fc.pause_vm()
        # 6. Push to pool
        self._pool.put(PooledVM(fc=fc, net=net, rootfs=rootfs))
```

### Sizing Guidelines

| Pool Size | Memory Usage (1GB RAM VMs) | Use Case |
|---|---|---|
| 3 | 3 GB | Development / light load |
| 10 | 10 GB | Production (moderate) |
| 50 | 50 GB | High density (needs ballooning) |
| 100+ | 100+ GB | Requires memory overcommit + balloon |

---

## 3. Overlayfs / CoW Rootfs ❌ NOT IMPLEMENTED

### The Problem

Current `image_clone` phase:
```
reflink copy from tmpfs: 100-500ms (400MB sparse image)
```
This must happen per VM. Even with reflink, the metadata + page cache overhead adds up.

### Solution: CoW Rootfs Views

Use a **read-only base image** + **writable overlay** per VM. Zero copy, O(1) creation.

#### Option A: Btrfs Subvolume Snapshots (Easiest)

```bash
# One-time setup:
btrfs subvolume create /mnt/vm-images/base-rootfs
# Decompress the golden rootfs into it
btrfs subvolume snapshot -r /mnt/vm-images/base-rootfs /mnt/vm-images/base-ro

# Per VM (instant):
btrfs subvolume snapshot /mnt/vm-images/base-ro /mnt/vm-images/vm-001
# Present /mnt/vm-images/vm-001 as the VM's root block device
```

- Requires btrfs on the host
- Snapshots are O(1), near-instant (~1ms)
- Can present via loop device or qemu-nbd to Firecracker

#### Option B: Device-Mapper Snapshot (Fly.io approach)

```bash
# One-time:
BASE=/path/to/base-rootfs.ext4
SIZE=$(blockdev --getsz $BASE)

# Per VM:
COW=/mnt/vm-cows/vm-001.cow  # small, grows with writes
truncate -s 1G $COW            # small COW file (grows as needed)
dmsetup create vm-001 --table "0 $SIZE snapshot $BASE $COW P 8"

# /dev/mapper/vm-001 is now a CoW block device
# Pass it directly to Firecracker as the root drive
```

- No special filesystem needed (works on ext4, XFS, any)
- COW file starts small, grows with writes
- What Fly.io uses in production
- Provides a proper block device — no loop/qemu-nbd needed

#### Option C: OverlayFS (requires guest cooperation)

```bash
# On the host: present the base image and a scratch file
# Inside the guest (via init script):
mount -t overlay overlay -o lowerdir=/base-ro,upperdir=/scratch/upper,workdir=/scratch/work /mnt
```

- Most flexible but requires guest-side init changes
- Cannot be directly used as Firecracker root block device
- Requires guest kernel 3.18+ (standard in modern kernels)

### Estimated Impact

| Approach | Clone Time | Disk Usage | Complexity |
|---|---|---|---|
| Current (reflink tmpfs) ✅ | 100-500ms | Full copy per VM | ✅ None |
| Btrfs snapshots ❌ | **~1ms** | CoW, shared base | ⚠️ Needs btrfs |
| Device-mapper ❌ | **~1ms** | CoW, small COW file | ⚠️ More complex setup |
| OverlayFS ❌ | **~1ms** | CoW, shared base | ⚠️ Needs guest changes |

**Recommendation:** Start with **device-mapper** (no filesystem requirement, proven by Fly.io). Fall back to reflink if device-mapper is unavailable.

---

## 4. Implementation Roadmap — ALL PHASES ❌ NOT IMPLEMENTED

### Phase 1: Foundation (Week 1) ❌

| Item | Effort | Depends On | Status |
|---|---|---|---|
| Add `--python-flag=no_warnings` to build | 1 hour | Nothing | ❌ |
| Huge pages: add to `FirecrackerConfig` + host docs | 1 day | Nothing | ❌ |
| cgroup v2 + `kvm.nx_huge_pages=never` docs | 0.5 day | Nothing | ❌ |
| Kernel boot args: add safe params | 1 hour | Nothing | ⚠️ Partial (basic args exist) |

### Phase 2: Snapshot Domain (Weeks 2-3) ❌

| Item | Effort | Status |
|---|---|---|
| `SnapshotItem` model + `SnapshotRepository` | 1 day | ❌ |
| `snapshot create` (pause + snapshot API + rootfs copy) | 3 days | ❌ (API methods exist, but no domain) |
| `snapshot list / get / rm` | 1 day | ❌ |
| snapshot CLI commands | 1 day | ❌ |

### Phase 3: Clone from Snapshot (Weeks 3-5) ❌

### Phase 4: Hot-Standby Pool (Weeks 5-7) ❌

### Expected Performance Trajectory

```
Current (cold boot):        3-10s

Phase 1 (kernel tuning):    2-7s       (-10-20%)
Phase 2 (snapshot clone):   50-200ms   (-95% from cold boot)
Phase 3 (CoW rootfs):       50-150ms   (-additional disk time)
Phase 4 (hot pool):         10-50ms    (-pop from pool)
```
