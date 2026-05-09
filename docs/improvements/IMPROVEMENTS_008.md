# Snapshot-Based Instant VM Cloning & Next-Level Optimizations

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

## 1. Snapshot-Based VM Cloning (The Big One)

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

### 1.2 Snapshot Domain

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

#### Disk Layout

```
~/.cache/mvmctl/snapshots/
├── <snapshot-name>/
│   ├── vm.mem                ← Raw guest RAM dump
│   ├── vm.vmstate            ← Serde bitcode VM state
│   ├── rootfs.ext4           ← Frozen root block device
│   ├── firecracker.json      ← Original Firecracker config
│   └── metadata.json         ← JSON metadata
└── <another-snapshot>/
    └── ...
```

#### CLI Commands

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

#### API Operations

```python
class SnapshotOperation:
    @staticmethod
    def create(inputs: SnapshotCreateInput) -> OperationResult[SnapshotItem]:
        """Create snapshot from a running VM."""

    @staticmethod
    def get(inputs: SnapshotInput) -> SnapshotItem:
        """Get snapshot details."""

    @staticmethod
    def list(inputs: SnapshotInput) -> list[SnapshotItem]:
        """List all snapshots."""

    @staticmethod
    def remove(inputs: SnapshotInput) -> BatchResult[SnapshotItem]:
        """Remove one or more snapshots (free disk space)."""
```

### 1.3 `vm create --snapshot`

Instead of `--image` / `--kernel`, a new VM can be created from a snapshot:

```
mvm vm create \
  --snapshot my-snapshot \          # REQUIRED (instead of --image + --kernel)
  --name my-clone \                 # REQUIRED: New VM name
  --ssh-key my-key \                # Inject SSH public key post-resume
  --hostname my-clone-host \        # Set hostname via vsock agent
  --network default \               # Which network bridge to attach to
  --tap auto \                      # Auto-allocate TAP device
  --ip auto \                       # Auto-allocate IP from pool
  --disk-size 5G \                  # Grow rootfs (CoW layer only)
  --user-data ./config.yaml \       # Re-apply cloud-init user-data
  --diff \                          # Create diff snapshot from current state
  --count 5 \                       # Batch: create 5 clones from same snapshot
  --atomic \                        # All-or-nothing batch creation
```

**What changes in the VM creation pipeline:**

The current `VMCreateContext.execute()` has these phases:
```
clone_image → provisioner → firecracker_spawn → console_setup
```

The snapshot path replaces the first four phases:
```
coow_rootfs → snapshot_load + network_overrides → post_resume_agent → console_setup
```

#### New `VMCreateContext` Path

```python
class VMCreateContext:
    # ... existing fields ...

    def execute_snapshot_clone(self) -> None:
        """Clone from snapshot instead of fresh boot."""

        # 1. Pre-allocate network resources
        tap = self._net_pool.acquire()
        self.tap_name = tap.name
        self.guest_mac = tap.mac
        self.guest_ip = tap.ip

        # 2. Create CoW rootfs from snapshot's frozen rootfs
        self.rootfs_path = CoWUtils.create_cow_view(
            base_path=self.resolved.snapshot.rootfs_path,
            cow_dir=self.vm_dir / "cow",
        )
        self.mark_created("rootfs")

        # 3. Pre-allocate socket paths, cgroup, jailer dir
        FsUtils.secure_mkdir(self.vm_dir, self.resolved.name)
        self.mark_created("vm_dir")

        # 4. Start Firecracker in snapshot mode (no --config-file)
        fc_config = self.build_firecracker_config(snapshot_mode=True)
        firecracker_spawner = FirecrackerSpawner(fc_config)
        self.set_firecracker_manager(firecracker_spawner)
        firecracker_spawner.write_logger_config_only()
        self.mark_created("firecracker")

        # 5. Load snapshot with network_overrides
        self.fc_manager.load_snapshot(
            mem_path=self.resolved.snapshot.mem_file_path,
            snapshot_path=self.resolved.snapshot.vmstate_file_path,
            resume=False,
            network_overrides=[
                {"iface_id": "eth0", "host_dev_name": self.tap_name}
            ],
        )

        # 6. Resume
        self.fc_manager.resume_vm()

        # 7. Post-resume: agent-based configuration
        if self.resolved.ssh_keys:
            self._post_resume_vsock_configure()
```

### 1.4 Network Uniqueness for Clones

#### The Problem

When you clone a snapshot, the frozen guest has:
- A fixed MAC address (from the original `network-interfaces` config)
- A fixed IP address (from DHCP lease or static config inside the guest)
- A fixed hostname

10 clones from the same snapshot = 10 VMs fighting for the same IP/MAC/hostname.

#### The Solution Stack

**Step 1: `network_overrides` (Firecracker v1.12.0+)**

The snapshot load API accepts remapping of host TAP devices:

```json
PUT /snapshot/load
{
    "snapshot_path": "./vm.vmstate",
    "mem_backend": {
        "backend_path": "./vm.mem",
        "backend_type": "File"
    },
    "network_overrides": [
        {"iface_id": "eth0", "host_dev_name": "vmtap-clone-42"}
    ]
}
```

This makes the guest's `eth0` point to `vmtap-clone-42` instead of the original TAP. The guest still has the same MAC/IP inside — but the host side is different.

**Step 2: Network Namespace per Clone**

Each clone's Firecracker process runs in its own netns:

```bash
ip netns add clone-42
ip link add veth-host-42 type veth peer name veth-clone-42
ip link set veth-clone-42 netns clone-42
ip link set veth-host-42 master br0    # Attach to bridge
ip link set veth-host-42 up
# Inside netns:
ip netns exec clone-42 ip tuntap add name tap0 mode tap
ip netns exec clone-42 ip link set tap0 master bridge0
ip netns exec clone-42 ip link set tap0 up
# Start Firecracker in this netns
ip netns exec clone-42 firecracker --api-sock /tmp/fc.sock ...
```

The `veth` pair gives each clone:
- Unique host-side interface on the bridge
- Unique MAC (auto-generated by `veth`) for host-side routing
- Same guest-side TAP name (`tap0`) — doesn't conflict because it's in a different netns

**Step 3: iptables NAT for Unique External IPs**

```bash
# Outbound (all clones share egress IP):
iptables -t nat -A POSTROUTING -s 10.0.0.0/24 -o eth0 -j MASQUERADE

# Inbound (route unique external ports/IPs to specific clones):
iptables -t nat -A PREROUTING -d <external-ip-1> -j DNAT --to-destination 10.0.0.2
iptables -t nat -A PREROUTING -d <external-ip-2> -j DNAT --to-destination 10.0.0.3
```

**Step 4: Guest-Side Reconfiguration (vsock Agent)**

After resume, a lightweight agent inside the guest (listening on vsock) performs:

```bash
# Called by vsock agent after resume:
ip addr del 10.0.0.2/24 dev eth0
ip addr add 10.0.0.42/24 dev eth0
ip route add default via 10.0.0.1
hostnamectl set-hostname clone-42
ssh-keygen -A
rm -f /var/lib/systemd/random-seed
systemctl restart sshd
```

#### Simpler Alternative: Same-IP Workers

If clones don't need unique inbound connectivity (e.g., they're all workers pulling from a queue):

1. All clones share guest IP `10.0.0.2`
2. Host uses `MASQUERADE` for outbound (all clones share host IP)
3. No guest reconfiguration needed for networking
4. Only need `network_overrides` for correct TAP routing

This is the **recommended starting point** — much simpler and covers most use cases.

### 1.5 Memory Determinism & Security

#### What's Duplicated vs What's Unique

When 10 clones resume from the same snapshot:

| Component | Identical? | Mechanism |
|-----------|-----------|-----------|
| **Kernel memory** | ✅ Identical (initially) | MAP_PRIVATE of same mem_file |
| **Kernel CSPRNG** | ❌ **Reseeded per clone** | VMGenID device (Firecracker 1.8+, Linux 5.18+) writes new 16-byte random ID → kernel reseeds `drivers/char/random.c` |
| **`/dev/urandom`** | ❌ **Different per clone** | Reseeded via VMGenID |
| **Userspace memory** | ✅ **Identical** | MAP_PRIVATE — until COW page faults |
| **SSH host keys** | ✅ **Identical** | Generated at first boot, frozen in snapshot |
| **`/var/lib/systemd/random-seed`** | ✅ **Identical** | File on disk, frozen |
| **TLS session keys in memory** | ✅ **Identical** | In userspace heap at snapshot time |
| **Application secrets in RAM** | ✅ **Identical** | Must be handled by application |
| **OpenSSL RNG state** | ✅ **Same seed (initially)** | Userspace PRNG, not touched by VMGenID |
| **File descriptors** | ❌ **Different** | New FDs per clone (TAP, socket) |
| **RDRAND/RDSEED** | ❌ **Different** | Hardware random per CPU |

#### The Critical Problem: SSH Host Keys

Every clone from the same snapshot has **identical SSH host keys**. This allows:
- MitM attacks between clones (host key collision)
- A compromise of one clone reveals the host key for all others

**Solution — Golden Snapshot Preparation:**

```bash
# Run INSIDE the VM BEFORE taking the snapshot:
rm -f /etc/ssh/ssh_host_*
rm -f /var/lib/systemd/random-seed
rm -f /var/lib/sss/db/*
rm -f /etc/machine-id
rm -f /var/lib/dbus/machine-id
# Clear bash history
> ~/.bash_history
> /root/.bash_history
# Clear temp files
rm -rf /tmp/*
# Zero out unused disk space for better sparse/diff snapshots
dd if=/dev/zero of=/zerofile bs=1M; rm -f /zerofile

# Then take the snapshot. On each clone's first boot after resume:
# (via vsock agent or init script)
ssh-keygen -A
systemd-machine-id-setup
echo "clone-42" > /etc/hostname
```

#### VMGenID — How It Works

Firecracker implements the **Virtual Machine Generation Identifier** device (since v1.8 x86_64, v1.9 ARM):

1. On snapshot load, Firecracker generates a **new cryptographically random 16-byte value**
2. Writes it to the VMGenID device MMIO region
3. **Injects an interrupt** to the guest
4. Linux kernel (5.18+) handles the interrupt in `drivers/char/random.c`:
   - Reads the new VMGenID value
   - Immediately forces a **CSPRNG reseed**
   - The reseed is mixed into `input_pool` and `crng`
5. After reseed: `getrandom()`, `/dev/urandom`, `/dev/random` all produce **unique output per clone**

**However**, VMGenID only reseeds the **kernel's** CSPRNG. Userspace PRNG state (Python's `random`, OpenSSL, libsodium, etc.) is NOT automatically reseeded unless they call `getrandom()`.

#### Secure Usage Patterns (per Firecracker Docs)

| Pattern | Description | Secure? |
|---------|-------------|---------|
| **Example 1** | Create snapshot → **terminate original** → resume exactly one clone | ✅ **Yes** — no duplication possible |
| **Example 2** | Create snapshot → original continues running → ALSO resume a clone | ❌ **No** — two instances sharing kernel/device state |
| **Example 3** | Create snapshot → **resume 10 clones** from same files | ❌ **No** — duplicate SSH keys, TLS state, PRNG |
| **mvmctl clone** | Create snapshot → resume multiple clones + post-resume agent cleans up unique state | **Mitigated** — needs careful agent implementation |

The mvmctl clone pattern (Example 3 + mitigation) is acceptable for development/testing. For production, the post-resume agent MUST:
1. Regenerate SSH host keys
2. Reset `/var/lib/systemd/random-seed`
3. Set unique hostname
4. Call `spsystemctl restart sshd`
5. Reset any application-level secrets

---

## 2. VM Hot-Standby Pool

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
|-----------|---------------------------|----------|
| 3 | 3 GB | Development / light load |
| 10 | 10 GB | Production (moderate) |
| 50 | 50 GB | High density (needs ballooning) |
| 100+ | 100+ GB | Requires memory overcommit + balloon |

### Fly.io Pattern

Fly.io separates "create" from "start":
- **Create** (slow): Image pull, host selection, DB persistence, resource allocation. User waits once.
- **Start** (fast): Just a message to the host to resume a pre-allocated machine. ~300ms cross-continent, ~10ms same-region.

The mvmctl pool replicates this: create happens once (snapshot taken, pool filled), start is instant (resume from pool).

---

## 3. Overlayfs / CoW Rootfs

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
|----------|-----------|------------|------------|
| Current (reflink tmpfs) | 100-500ms | Full copy per VM | ✅ None |
| Btrfs snapshots | **~1ms** | CoW, shared base | ⚠️ Needs btrfs |
| Device-mapper | **~1ms** | CoW, small COW file | ⚠️ More complex setup |
| OverlayFS | **~1ms** | CoW, shared base | ⚠️ Needs guest changes |

**Recommendation:** Start with **device-mapper** (no filesystem requirement, proven by Fly.io). Fall back to reflink if device-mapper is unavailable.

---

## 4. Implementation Roadmap

### Phase 1: Foundation (Week 1)

| Item | Effort | Depends On |
|------|--------|------------|
| Add `--python-flag=no_warnings` to build | 1 hour | Nothing |
| Huge pages: add to `FirecrackerConfig` + host docs | 1 day | Nothing |
| cgroup v2 + `kvm.nx_huge_pages=never` docs | 0.5 day | Nothing |
| Kernel boot args: add safe params (`no_timer_check`, `audit=0`, etc.) | 1 hour | Nothing |

### Phase 2: Snapshot Domain (Weeks 2-3)

| Item | Effort | Depends On |
|------|--------|------------|
| `SnapshotItem` model + `SnapshotRepository` | 1 day | Nothing |
| `snapshot create` (pause + snapshot API + rootfs copy) | 3 days | Firecracker snapshot API (✅ exists) |
| `snapshot list / get / rm` | 1 day | SnapshotRepository |
| snapshot CLI commands | 1 day | SnapshotOperation |
| Snapshot metadata + disk layout | 1 day | Nothing |
| **Total** | **~1 week** | |

### Phase 3: Clone from Snapshot (Weeks 3-5)

| Item | Effort | Depends On |
|------|--------|------------|
| CoW rootfs backend (device-mapper) | 3 days | Nothing |
| `vm create --snapshot` path in `VMCreateContext` | 3 days | Snapshot domain, CoW rootfs |
| `network_overrides` support in FirecrackerSpawner | 1 day | Firecracker v1.12+ |
| Pre-resource pool (TAP/IP/MAC) | 2 days | Nothing |
| vsock agent for post-resume config | 5 days | vsock support in VM images |
| **Total** | **~2 weeks** | |

### Phase 4: Hot-Standby Pool (Weeks 5-7)

| Item | Effort | Depends On |
|------|--------|------------|
| `VMHotStandbyPool` class | 3 days | Snapshot cloning (Phase 3) |
| Background replenisher | 2 days | VMHotStandbyPool |
| `mvm-d` daemon (persistent mode) | 5 days | VMHotStandbyPool |
| CLI integration (CLI → daemon UDS) | 3 days | mvm-d |
| Resource pool lifecycle (create/release) | 2 days | Everything above |
| **Total** | **~2 weeks** | |

### Expected Performance Trajectory

```
Current (cold boot):        3-10s

Phase 1 (kernel tuning):    2-7s       (-10-20%)
Phase 2 (snapshot clone):   50-200ms   (-95% from cold boot)
Phase 3 (CoW rootfs):       50-150ms   (-additional disk time)
Phase 4 (hot pool):         10-50ms    (-pop from pool)
```

The hot pool path:
```
acquire() → resume (3-8ms) → configure (5-20ms) → return
                                  ≈ 10-50ms total
```

Golden VM build (one-time):
```
boot (3-10s) → snapshot create (1-2s for mem dump) → store
                                  ≈ 5-15s total (paid once)
```
