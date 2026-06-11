# Snapshot-Based Instant VM Cloning & Next-Level Optimizations

> **STATUS: Design Document — not implemented.** Building blocks exist (`CreateSnapshot`/`LoadSnapshot` in `FirecrackerClient`, `Snapshot`/`LoadSnapshot` in `VMController`), but no snapshot domain, CLI commands, pool manager, or CoW rootfs.
>
> **Last verified:** 2026-06-10

**Phase:** Multi-phase — spans 3-4 milestones
**Complexity:** Very High
**Depends on:** Host kernel config (huge pages, cgroup v2), Firecracker v1.12+

---

## 1. Snapshot-Based VM Cloning

### 1.1 How Firecracker Snapshots Work

When `PUT /snapshot/create` is called on a **paused** microVM, Firecracker produces two files:

```
/home/user/.cache/mvmctl/snapshots/my-snapshot/
├── vm.mem           ← Raw dump of ALL guest RAM pages
├── vm.vmstate       ← Serde bitcode binary format (vCPU state, device state)
├── rootfs.ext4      ← Frozen rootfs image at snapshot time
└── metadata.json    ← Origin, kernel, image, timestamps
```

**The mmap secret (MAP_PRIVATE):**
- Pages are NOT read eagerly — they're demand-paged (loaded on first access)
- The original `vm.mem` file is **never modified** — writes go to anonymous COW pages
- **Multiple processes can MAP_PRIVATE the same file simultaneously** — each gets independent COW
- Loading the snapshot takes **~3-8ms** (just the mmap syscall + metadata), not the full RAM size

### 1.2 Snapshot Domain (Not Implemented)

A new domain `snapshot` with full CRUD lifecycle.

**SnapshotItem Model:**
```go
type SnapshotItem struct {
    ID              string
    Name            string
    SourceVMID      string
    SourceVMName    string
    ImageID         string
    KernelID        string
    MemoryMiB       int
    VCPUs           int
    MemFilePath     string
    VMStateFilePath string
    RootfsPath      string
    RootfsSizeBytes int64
    DiskSizeBytes   int64
    CreatedAt       string
}
```

**CLI Commands (Not Implemented):**
```
mvm snapshot create <name> --vm <vm-id> [--leave-paused]
mvm snapshot list [--json]
mvm snapshot get <name> [--json]
mvm snapshot rm <name> [--force]
```

### 1.3 `vm create --snapshot` (Not Implemented)

### 1.4 Network Uniqueness for Clones (Not Implemented)

### 1.5 Memory Determinism & Security (Not Implemented)

---

## 2. VM Hot-Standby Pool (Not Implemented)

A pool manager that keeps N pre-booted, paused VMs ready to assign. When a VM is requested, it pops one from the pool, runs post-resume configuration, and hands it to the user. Pool replenishment runs in the background.

This is the Fly.io pattern: "Create ahead, start fast."

---

## 3. Overlayfs / CoW Rootfs (Not Implemented)

### The Problem

Current `image_clone` phase:
```
reflink copy from tmpfs: 100-500ms (400MB sparse image)
```
This must happen per VM. Even with reflink, the metadata + page cache overhead adds up.

### Solution: CoW Rootfs Views

Use a **read-only base image** + **writable overlay** per VM. Zero copy, O(1) creation.

**Option A: Btrfs Subvolume Snapshots (Easiest)**
- Requires btrfs on the host
- Snapshots are O(1), near-instant (~1ms)

**Option B: Device-Mapper Snapshot (Fly.io approach)**
- No special filesystem needed (works on ext4, XFS, any)
- COW file starts small, grows with writes
- Provides a proper block device — no loop/qemu-nbd needed

**Option C: OverlayFS (requires guest cooperation)**
- Most flexible but requires guest-side init changes
- Cannot be directly used as Firecracker root block device

### Estimated Impact

| Approach | Clone Time | Disk Usage | Complexity |
|---|---|---|---|
| Current (reflink tmpfs) ✅ | 100-500ms | Full copy per VM | None |
| Btrfs snapshots ❌ | **~1ms** | CoW, shared base | Needs btrfs |
| Device-mapper ❌ | **~1ms** | CoW, small COW file | More complex setup |
| OverlayFS ❌ | **~1ms** | CoW, shared base | Needs guest changes |

**Recommendation:** Start with **device-mapper** (no filesystem requirement, proven by Fly.io). Fall back to reflink if device-mapper is unavailable.

---

## 4. Implementation Roadmap (All Phases Not Implemented)

### Phase 1: Foundation (Week 1)

| Item | Status |
|---|---|
| Huge pages: add to `FirecrackerConfig` + host docs | ❌ |
| cgroup v2 + `kvm.nx_huge_pages=never` docs | ❌ |
| Kernel boot args: add safe params | ⚠️ Partial (basic args exist) |

### Phase 2: Snapshot Domain (Weeks 2-3)

| Item | Status |
|---|---|
| `SnapshotItem` model + `SnapshotRepository` | ❌ |
| `snapshot create` (pause + snapshot API + rootfs copy) | ❌ (API methods exist, but no domain) |
| `snapshot list / get / rm` | ❌ |
| snapshot CLI commands | ❌ |

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
