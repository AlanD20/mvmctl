# Daemonless Production Readiness — Host Capacity Detection, Events & Admission Control

> **STATUS: Design Document — partially implemented.** All improvements work identically in both daemonless and daemon modes.
>
> | Section | Status |
> |---------|--------|
> | Host capacity detection (`mvm host info`) | ✅ Implemented at `cli/host.py:279` backed by `HostOperation.info()` |
> | HostDetector utility module | ✅ Implemented at `core/host/_detector.py` |
> | host_state schema extension | ✅ Implemented — `host_state` schema includes all capacity columns |
> | Resource accounting table + admission control | ❌ Not implemented |
> | Event log table + `mvm events` command | ❌ Not implemented |
> | Rate-limited VM creation | ❌ Not implemented |
> | `mvm host reconcile` for crash recovery | ❌ Not implemented |
> | sysctl tuning in `mvm host init` | ❌ Not implemented |
> | WAL checkpointing + backup rotation | ❌ Not implemented |
>
> **What IS implemented (status detail):**
> - Host capacity detection (`mvm host info`) — ✅ Completed. CLI command at `cli/host.py:279`, API orchestration via `HostOperation.info()` / `HostOperation.refresh_capacity()`, core detection via `HostDetector` at `core/host/_detector.py`.
> - HostDetector — ✅ Completed at `core/host/_detector.py` (no subprocess calls, reads /proc + stdlib). Three static methods: `detect_hardware()`, `detect_limits()`, `detect_resources()`. Returns `HostHardware`, `HostLimits`, `HostResources` dataclasses from `models/host.py`.
> - `host_state` schema — ✅ Completed. Extended with CPU, memory, storage, limits columns. Populated on `mvm host init` and `mvm host info --refresh`.
>
> **What DOES exist (building blocks, additional context):**
> - SQLite with WAL mode, busy_timeout, foreign keys ✅
> - Database migration system + snapshot/rollback ✅
> - PID-based VM state tracking ✅
> - `_graceful_read` decorator for degraded operation ✅
> - Operation class structure with clean separation ✅
> - Existing `HostOperation`, `HostService`, `HostStateItem` ✅
>
> **Last verified:** 2026-05-18

---

**Phase:** Standalone — each section independently shippable
**Complexity:** Medium (low per-section)
**Depends on:** Nothing beyond current architecture

---

## Table of Contents

- [1. Rationale — Why Not a Daemon](#1-rationale--why-not-a-daemon)
- [2. Host Capacity Detection (`mvm host info`)](#2-host-capacity-detection-mvm-host-info)
  - [2.1 The `--refresh` Flag & Staleness Contract](#21-the---refresh-flag--staleness-contract)
  - [2.2 Detection Targets (Static → DB)](#22-detection-targets-static--db)
  - [2.3 Detection Targets (Dynamic → Computed Per-Call)](#23-detection-targets-dynamic--computed-per-call)
  - [2.4 Detection Method — Zero Subprocess Calls](#24-detection-method--zero-subprocess-calls)
  - [2.5 Recommended Max VMs Calculation](#25-recommended-max-vms-calculation)
  - [2.6 HostDetector Utility Module](#26-hostdetector-utility-module)
  - [2.7 `host_state` Schema Extension](#27-host_state-schema-extension)
  - [2.8 CLI Output — `mvm host info`](#28-cli-output--mvm-host-info)
  - [2.9 `mvm host info --json` Output](#29-mvm-host-info---json-output)
  - [2.10 Integration with `mvm host init`](#210-integration-with-mvm-host-init)
- [3. Resource Accounting & Admission Control](#3-resource-accounting--admission-control)
  - [3.1 Schema](#31-schema)
  - [3.2 How It Works](#32-how-it-works)
  - [3.3 Race Condition Handling](#33-race-condition-handling)
- [4. Event Log System](#4-event-log-system)
  - [4.1 Schema](#41-schema)
  - [4.2 Emitting Events](#42-emitting-events)
  - [4.3 `mvm events` CLI Command](#43-mvm-events-cli-command)
  - [4.4 Why Not Streaming / Pub-Sub](#44-why-not-streaming--pub-sub)
- [5. Rate-Limited VM Creation](#5-rate-limited-vm-creation)
- [6. Crash Recovery (`mvm host reconcile`)](#6-crash-recovery-mvm-host-reconcile)
- [7. sysctl Tuning in `mvm host init`](#7-sysctl-tuning-in-mvm-host-init)
- [8. SQLite Hardening (WAL Checkpointing & Backups)](#8-sqlite-hardening-wal-checkpointing--backups)
- [9. Dual-Mode Architecture — How Daemonless and Daemon Coexist](#9-dual-mode-architecture--how-daemonless-and-daemon-coexist)
- [10. Implementation Roadmap](#10-implementation-roadmap)

---

## 1. Rationale — Why Not a Daemon

**We keep the CLI-native, daemonless model and enhance it.** The daemon is optional, adds no value for basic operations, and is only needed for auto-healing + live event streaming.

| Concern | Daemon Model | Daemonless Model |
|---------|-------------|------------------|
| **Socket explosion** | Per-VM shim socket + daemon socket + FC socket + console socket | FC socket + console socket only (unchanged) |
| **IPC complexity** | gRPC or HTTP over UDS, serialization, client/server stubs | Python imports (already works) |
| **Cold start** | Must wait for daemon | Instant |
| **Survival** | Daemon crash loses control plane | FC processes survive CLI exit (already true) |
| **Dev ergonomics** | Daemon must be running | `uv run mvm` — always works |
| **Wrapping FC** | Proxy around FC's own UDS API | Work alongside it, not above it |

### What we actually need

1. **Visibility** — What's running? What resources are left?
2. **Safety** — Don't create VMs when resources are exhausted
3. **Recovery** — Detect and clean up after crashes
4. **Observability** — Log events so external tools can react

All four are solvable with SQLite + CLI commands. Zero new daemon infrastructure.

---

## 2. Host Capacity Detection (`mvm host info`)

### 2.1 The `--refresh` Flag & Staleness Contract

```
mvm host init          → ALWAYS refreshes static data (calls detect + writes to DB)
mvm host info          → Reads static from DB (stale), computes dynamic fresh
mvm host info --refresh → Re-detects static data, updates DB, computes dynamic fresh
```

**Staleness is by design.** Static data (CPU model, total RAM, kernel limits) changes on hardware upgrades or sysctl tuning. It's populated by `mvm host init` and stays stable between inits. Users who want fresh data pass `--refresh` — which takes ~1-2ms.

### 2.2 Detection Targets (Static → DB)

Set by `mvm host init` or `mvm host info --refresh`. Stored in `host_state`.

| Target | How to Detect | Notes |
|--------|--------------|-------|
| `hostname` | `socket.gethostname()` | stdlib, ~1μs |
| `cpu_model` | Parse `model name` from `/proc/cpuinfo` | 1st line, all cores identical |
| `cpu_vendor` | Parse `vendor_id` (x86) or `CPU implementer` hex (ARM) from `/proc/cpuinfo` | Maps to: intel, amd, arm, apple, qualcomm, etc. |
| `cpu_cores` | `os.cpu_count()` | Total logical cores incl. HT |
| `cpu_architecture` | `platform.machine()` | `x86_64`, `aarch64` |
| `numa_nodes` | Count `/sys/devices/system/node/node*` dirs | No `numactl` needed |
| `memory_total_mib` | Parse `MemTotal` from `/proc/meminfo` | Convert kB → MiB |
| `storage_total_bytes` | `shutil.disk_usage(cache_dir).total` | VM directory mount point |
| `kernel_version` | `os.uname().release` | e.g. "6.8.0-31-generic" |
| `os_release` | Parse `PRETTY_NAME` from `/etc/os-release` | e.g. "Ubuntu 24.04 LTS" |
| `pid_max` | Read `/proc/sys/kernel/pid_max` | Default: 4194304 |
| `fd_max` | Read `/proc/sys/fs/file-max` | ~1-2 million typical |
| `conntrack_max` | Read `/proc/sys/net/netfilter/nf_conntrack_max` | 0 if module not loaded |
| `tap_devices_max` | Read `/sys/module/tun/parameters/max_tap_devices` | 0 = unlimited |
| `ip_local_port_range` | Read `/proc/sys/net/ipv4/ip_local_port_range` | e.g. "32768 60999" |

### 2.3 Detection Targets (Dynamic → Computed Per-Call)

Computed fresh on every `mvm host info` invocation. NOT stored in DB.

| Target | How to Detect | Notes |
|--------|--------------|-------|
| `memory_available_mib` | Parse `MemAvailable` from `/proc/meminfo` | Same file as MemTotal |
| `tap_devices_used` | Count `/sys/class/net/*/tun_flags` existences | ~200μs, no subprocess |
| `pids_current` | Count numeric dirs in `/proc` | ~100μs |
| `fd_current` | Read 1st field of `/proc/sys/fs/file-nr` | Currently allocated FDs |
| `conntrack_current` | Read `/proc/sys/net/netfilter/nf_conntrack_count` | 0 if module not loaded |
| `arp_current` | Count lines in `/proc/net/arp` minus header | ~50μs |
| `storage_free_bytes` | `shutil.disk_usage(cache_dir).free` | `f_bavail` (available to user) |
| `recommended_max_vms` | Formula from Section 2.5 | Pure computation, ~1μs |

### 2.4 Detection Method — Zero Subprocess Calls

**Every single target uses either Python stdlib or a `/proc` file read.** Total cost for all static targets combined: **~500μs**. Total for all dynamic targets combined: **~500μs**. Grand total: **~1-2ms**.

Files are read once and parsed for multiple values:
- `/proc/meminfo` read once → extract `MemTotal` (static) + `MemAvailable` (dynamic)
- `/proc/cpuinfo` read once → extract `cpu_model` (static) + count processors
- `/sys/class/net/*/tun_flags` scanned once → count active TAP devices

No `ip`, no `sysctl`, no `numactl`, no `free`, no `conntrack`, no `df`, no `lscpu`. All from stdlib + procfs.

### 2.5 Recommended Max VMs Calculation

```python
def recommended_max_vms(cpu_cores: int, memory_available_mib: int, 
                        tap_devices_available: int, pid_max: int,
                        conntrack_max: int) -> tuple[int, str | None]:
    """Returns (max_vms, limiting_resource_name_or_None)."""
    limits: dict[str, int] = {}
    
    # CPU: leave 1 core for OS
    if cpu_cores > 1:
        limits["cpu"] = cpu_cores - 1
    
    # Memory: reserve 2 GiB OS, 50 MiB per-VM overhead + 512 MiB guest RAM
    # 512 MiB is the default assumption — configurable
    usable_mib = memory_available_mib - 2048
    per_vm_mib = 50 + 512  # overhead + default guest RAM
    limits["memory_mib"] = max(1, usable_mib // per_vm_mib)
    
    # TAP devices
    limits["tap"] = tap_devices_available
    
    # PIDs: reserve 200 for OS, ~3 per VM
    limits["pid"] = max(1, (pid_max - 200) // 3)
    
    # Conntrack: ~64 entries per VM for burst traffic
    if conntrack_max > 0:
        limits["conntrack"] = max(1, conntrack_max // 64)
    
    limiting = min(limits, key=limits.get)
    return limits[limiting], limiting
```

The result is labeled: "Assumes ~512 MiB guest RAM per VM. Actual capacity depends on workload."

### 2.6 HostDetector Utility Module

**Location:** `src/mvmctl/core/host/_detector.py`

```python
@dataclass
class HostHardware:
    hostname: str
    cpu_model: str
    cpu_vendor: str      # 'intel', 'amd', 'apple', 'qualcomm', 'arm', etc.
    cpu_cores: int
    cpu_architecture: str
    numa_nodes: int
    memory_total_mib: int
    storage_total_bytes: int
    kernel_version: str
    os_release: str

@dataclass
class HostLimits:
    pid_max: int
    fd_max: int
    conntrack_max: int
    tap_devices_max: int
    ip_local_port_range: tuple[int, int]

@dataclass
class HostResources:
    """Dynamic — always live-detected, never stored."""
    memory_available_mib: int
    tap_devices_used: int
    pids_current: int
    fd_current: int
    conntrack_current: int
    arp_current: int
    storage_free_bytes: int
    recommended_max_vms: int
    limiting_resource: str | None


class HostDetector:
    """Detect host hardware, limits, and live resource usage.
    
    All methods read from /proc or stdlib — zero subprocess calls.
    Total detection time: ~1-2ms for all targets combined.
    """
    
    @staticmethod
    def detect_hardware(vm_dir: str | None = None) -> HostHardware: ...
    @staticmethod
    def detect_limits() -> HostLimits: ...
    @staticmethod
    def detect_resources(vm_dir: str, hardware: HostHardware, 
                         limits: HostLimits) -> HostResources: ...
```

File follows the project's `core/{domain}/_detector.py` pattern (part of the host domain in `core/host/`). Uses only `pathlib`, `re`, `os`, `socket`, `platform`, `shutil`. Not imported eagerly — consumed via lazy import through `mvmctl.core.host`.

### 2.7 `host_state` Schema Extension

**Direct modification of `001_initial_schema.sql`.** Pre-release, no migration needed. Add columns to existing `CREATE TABLE host_state`:

```sql
-- HOST_STATE: Host initialization state + capacity (singleton, always id=1)
CREATE TABLE host_state (
    id INTEGER PRIMARY KEY,
    -- Existing init-state columns (preserved)
    initialized INTEGER DEFAULT 0 NOT NULL,
    mvm_group_created INTEGER DEFAULT 0 NOT NULL,
    sudoers_configured INTEGER DEFAULT 0 NOT NULL,
    default_network_created INTEGER DEFAULT 0 NOT NULL,
    initialized_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,

    -- Host identity (set by mvm host init / mvm host info --refresh)
    hostname TEXT,
    cpu_model TEXT,
    cpu_vendor TEXT,        -- 'intel', 'amd', 'apple', 'qualcomm', etc.
    cpu_cores INTEGER,
    cpu_architecture TEXT,
    numa_nodes INTEGER DEFAULT 1,
    memory_total_mib INTEGER,
    storage_total_bytes INTEGER,
    kernel_version TEXT,
    os_release TEXT,

    -- Kernel limits (stable between sysctl changes, refreshed on init)
    pid_max INTEGER,
    fd_max INTEGER,
    conntrack_max INTEGER,
    tap_devices_max INTEGER,
    ip_local_port_range TEXT,

    -- Metadata
    detected_at TIMESTAMP
);
```

`HostStateItem` model in `models/host.py` gets the new fields added.

### 2.8 CLI Output — `mvm host info`

```
$ mvm host info
Host:          devbox-01
OS:            Ubuntu 24.04 LTS (6.8.0-31-generic)
CPU:           Intel(R) Xeon(R) Gold 6248R @ 3.00GHz (intel, 16 cores, 2 NUMA nodes)
Memory:        63.5 GiB total / 47.2 GiB available
Storage:       892 GiB free (VM directory)

Limits:
  TAP devices:  4096 max / 5 used / 4091 free
  PIDs:         4194304 max / 842 used
  File handles: 2097152 max / 15423 used
  Conntrack:    524288 max / 12345 used (module loaded)
  ARP entries:  8192 thresh / 342 current
  IP ports:     32768-60999 (28231 available)

Recommended max VMs: ~28
  ⛔ Bottleneck: memory (14.0 GiB available → ~28 VMs at 512 MiB/VM)
  ✓ TAP:         supports 4091 VMs
  ✓ PIDs:        supports ~1.4M VMs
  ✓ Conntrack:   supports ~8192 VMs
  ✓ CPU:         supports 15 VMs
  (assumes ~512 MiB guest RAM per VM; actual capacity depends on workload)

Setup:
  mvm init:      completed 2026-05-17T10:00:00Z
  mvm group:     created
  sudoers:       configured
  default net:   created

Run 'mvm host info --refresh' to re-detect hardware and kernel limits.
```

On first run (no data in `host_state` yet):
```
$ mvm host info
Host information not yet collected. Run 'mvm host init' first.
```

### 2.9 `mvm host info --json` Output

```json
{
  "detected_at": "2026-05-17T10:00:00Z",
  "hostname": "devbox-01",
  "os": {
    "kernel_version": "6.8.0-31-generic",
    "os_release": "Ubuntu 24.04 LTS"
  },
  "cpu": {
    "model": "Intel(R) Xeon(R) Gold 6248R @ 3.00GHz",
    "vendor": "intel",
    "cores": 16,
    "architecture": "x86_64",
    "numa_nodes": 2
  },
  "memory": {
    "total_mib": 65024,
    "available_mib": 48321
  },
  "storage": {
    "vm_dir_free_bytes": 957788356608,
    "vm_dir_total_bytes": 1073741824000
  },
  "limits": {
    "tap_devices": { "max": 4096, "used": 5 },
    "pid_max": 4194304,
    "pids_current": 842,
    "fd_max": 2097152,
    "fd_current": 15423,
    "conntrack_max": 524288,
    "conntrack_current": 12345,
    "arp_thresh3": 8192,
    "arp_current": 342,
    "ip_local_port_range": [32768, 60999]
  },
  "capacity": {
    "recommended_max_vms": 28,
    "limiting_resource": "memory_mib",
    "limiting_detail": "14.0 GiB available / 512 MiB per VM = ~28 VMs",
    "assumptions": "512 MiB guest RAM per VM"
  },
  "setup": {
    "initialized": true,
    "mvm_group_created": true,
    "sudoers_configured": true,
    "default_network_created": true,
    "initialized_at": "2026-05-17T10:00:00Z"
  }
}
```

### 2.10 Integration with `mvm host init`

`mvm host init` ALWAYS calls `HostDetector.detect_hardware()` + `detect_limits()` and writes results to `host_state`. This means:

1. First-time users get host info populated automatically
2. Re-running `mvm host init` freshens the data
3. Users who never run `init` get the "not yet collected" message
4. `mvm host info --refresh` is the same detection path, just exposed separately

No special "first run" logic needed — detection is idempotent and fast (~1ms).

---

## 3. Resource Accounting & Admission Control

### 3.1 Schema

Both tables are new. Added directly to `001_initial_schema.sql` (pre-release).

```sql
-- RESOURCE_ACCOUNTING: Host-level resource pool (one row per resource type)
CREATE TABLE resource_accounting (
    resource_type TEXT NOT NULL,      -- 'vcpu', 'memory_mib', 'tap', 'disk_gb'
    total BIGINT NOT NULL,            -- host capacity at last refresh
    reserved BIGINT NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (resource_type)
);

-- VM_RESOURCE_ALLOCATIONS: Per-VM allocations for release on delete/crash
CREATE TABLE vm_resource_allocations (
    vm_id TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    quantity BIGINT NOT NULL,
    PRIMARY KEY (vm_id, resource_type)
);
```

Populated on `mvm host init`:
```sql
INSERT OR REPLACE INTO resource_accounting (resource_type, total, reserved)
VALUES
    ('vcpu', 16, 0),
    ('memory_mib', 65024, 0),
    ('tap', 4096, 0),
    ('disk_gb', 892, 0);
```

### 3.2 How It Works

**On VM creation** — Inside a single `BEGIN IMMEDIATE` transaction:
1. For each resource, atomic check-and-reserve:
   ```sql
   UPDATE resource_accounting 
   SET reserved = reserved + ?, updated_at = datetime('now')
   WHERE resource_type = ? AND reserved + ? <= total
   ```
2. If `changes() == 0` for any resource → rollback → raise admission error
3. Write row to `vm_resource_allocations`
4. Proceed with VM creation

**On VM removal** — Release all:
```sql
DELETE FROM vm_resource_allocations WHERE vm_id = ?;
-- Per-type release happens via trigger or application logic
```

**On `mvm host reconcile`** — In Phase 1 (process check), release resources for orphaned VMs.

### 3.3 Race Condition Handling

Two terminals running `mvm vm create` simultaneously:
- First writer: `BEGIN IMMEDIATE` acquires write lock → succeeds
- Second writer: blocks on `busy_timeout=5000` (already configured)
- On timeout: retry once with 100ms backoff

Risk is low for a single-user CLI. The atomic `WHERE reserved + ? <= total` prevents silent overcommit even under contention.

---

## 4. Event Log System

### 4.1 Schema

```sql
CREATE TABLE event_log (
    id TEXT PRIMARY KEY,
    event_type TEXT NOT NULL,
    vm_id TEXT,
    source TEXT NOT NULL DEFAULT 'cli',
    payload TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_event_log_created_at ON event_log(created_at);
CREATE INDEX idx_event_log_type ON event_log(event_type);
CREATE INDEX idx_event_log_vm_id ON event_log(vm_id);
```

### 4.2 Emitting Events

```python
class EventLogger:
    @staticmethod
    def emit(event_type: str, vm_id: str | None = None,
             payload: dict[str, Any] | None = None, source: str | None = None) -> None:
        db.execute(
            "INSERT INTO event_log (id, event_type, vm_id, source, payload) VALUES (?, ?, ?, ?, ?)",
            (str(uuid.uuid4()), event_type, vm_id, source or "cli",
             json.dumps(payload or {}))
        )
```

Idempotent by design: UUID v7 event ID → `PRIMARY KEY` prevents duplicates. Same event emitted twice is silently ignored.

### 4.3 `mvm events` CLI Command

```bash
mvm events                         # Last 50 events
mvm events --type vm.crashed       # Filter by type
mvm events --since 5m              # Last 5 minutes
mvm events --since 1h --json       # JSON output for scripts
mvm events --follow                # Poll every 2s (like tail -f)
```

Polling-based (not push). With an index on `created_at`, each poll is a single index scan (~100μs). 2-second interval is sufficient for non-realtime use cases.

---

## 5. Rate-Limited VM Creation

```python
_CREATE_SEMAPHORE = threading.Semaphore(5)

def create(inputs: VMCreateInput) -> OperationResult[VMInstanceItem]:
    if not _CREATE_SEMAPHORE.acquire(timeout=120):
        raise OperationTimeout("Too many concurrent VM creations. Try again.")
    try:
        return _do_create(inputs)
    finally:
        _CREATE_SEMAPHORE.release()
```

Default: 5 concurrent creates. Configurable via `settings.max_parallel_vm_creates`.

---

## 6. Crash Recovery (`mvm host reconcile`)

Scans for three types of orphaned state:

| Phase | What It Checks | Action |
|-------|---------------|--------|
| 1 | Running VMs with dead PIDs | Mark as `crashed`, release resources |
| 2 | Active network leases with no VM | Release lease back to pool |
| 3 | TAP devices not owned by any VM | Delete TAP via `run_cmd` |

Idempotent. Safe to cron. Does NOT restart VMs or delete VM directories.

---

## 7. sysctl Tuning in `mvm host init`

Three tiers:

```bash
mvm host init                      # Dev — no sysctl changes
mvm host init --tier server        # Tune for 100-300 VMs
mvm host init --tier production    # Server + daemon setup + cgroups
```

Server tier sysctl:
```
fs.file-max = 2097152
kernel.pid_max = 4194304
net.ipv4.ip_local_port_range = 1024 65535
net.ipv4.neigh.default.gc_thresh3 = 8192
net.netfilter.nf_conntrack_max = 524288
net.netfilter.nf_conntrack_buckets = 131072
vm.overcommit_memory = 2
vm.overcommit_ratio = 80
```

All idempotent. Skip if already at/above target. Never crash on failure — warn and continue.

---

## 8. SQLite Hardening (WAL Checkpointing & Backups)

- Periodic WAL checkpoint: `PRAGMA wal_checkpoint(PASSIVE)` every 5 min (daemon mode) or every 10th write (daemonless)
- Snapshot rotation: keep last 5 `.bak` files, delete oldest on new snapshot
- Existing `_take_snapshot()` uses SQLite's backup API (already implemented)

---

## 9. Dual-Mode Architecture — How Daemonless and Daemon Coexist

The daemon is a simple watch loop, NOT a command proxy:

```python
def daemon_main():
    while running:
        for vm in get_running_vms():
            if not process_exists(vm.pid):
                EventLogger.emit("vm.crashed", ...)
                release_resources(vm)
        db.execute("PRAGMA wal_checkpoint(PASSIVE)")
        handle_event_stream_socket()
        time.sleep(5)
```

- One UDS socket (for `events --follow`)
- No gRPC, no protobuf, no shim management
- CLI detects daemon via socket existence, but only for event streaming
- All CRUD operations work identically with or without daemon
- Daemon binary: new symlink in `mvm-services` multi-dist binary

---

## 10. Implementation Roadmap

### Phase 1: Host Capacity Detection (1-2 weeks)

| Step | Files | Effort |
|------|-------|--------|
| Create `src/mvmctl/core/host/_detector.py` | New file | 1 day |
| Extend `host_state` schema + `HostStateItem` | `db/migrations/001_initial_schema.sql`, `models/host.py` | 0.5 day |
| Add `mvm host info` CLI command | `cli/host.py` | 0.5 day |
| Add `--json` output formatter | `cli/host.py` | 0.5 day |
| Wire into `mvm host init` | `core/host/` | 0.5 day |
| **Total** | | **~3 days** |

### Phase 2: Resource Accounting + Events (1-2 weeks)

| Step | Files | Effort |
|------|-------|--------|
| Add `resource_accounting` + `vm_resource_allocations` tables | `001_initial_schema.sql` | 0.5 day |
| Implement atomic check-and-reserve in `VMOperation.create()` | `api/vm_operations.py` | 1 day |
| Add `EventLogger` + `event_log` table | New `utils/_event_logger.py`, schema | 0.5 day |
| Add `mvm events` CLI command | New `cli/events.py` | 0.5 day |
| Add rate-limited creation (semaphore) | `api/vm_operations.py` | 0.5 day |
| **Total** | | **~3 days** |

### Phase 3: Recovery + Hardening (1 week)

| Step | Files | Effort |
|------|-------|--------|
| Add `mvm host reconcile` | `core/host/_service.py`, `cli/host.py` | 1 day |
| Add sysctl tuning to `mvm host init` | `core/host/_service.py` | 1 day |
| Add WAL checkpoint + snapshot rotation | `core/_shared/_db.py` | 0.5 day |
| **Total** | | **~2.5 days** |

### Phase 4: Optional Daemon (If Needed)

| Step | Files | Effort |
|------|-------|--------|
| `mvm-daemon` binary | New `services/daemon/` | 2 days |
| `mvm daemon start/stop/status` | `cli/daemon.py`, `core/daemon/` | 1 day |
| Auto-healing | Daemon watches event_log for vm.crashed | 1 day |
| **Total** | | **~4 days** |

**Total Phase 1-3: ~8.5 days.** All daemonless. All production value.

---

## Appendix: Architectural Decisions Log

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Daemon vs daemonless | Daemonless first, daemon optional | Socket explosion, no IPC, FC already survives |
| Storage format | SQLite direct, no new migration | Pre-release product, modify schema in place |
| Detection speed target | ~1-2ms total | All from /proc + stdlib, zero subprocess |
| Staleness contract | Static cached in DB, dynamic computed on read | Hardly matters (1-2ms to refresh both) |
| `recommended_max_vms` assumption | 512 MiB guest RAM, configurable | Matches default VM memory |
| `tap_devices_max` when tun built-in | 0 = unlimited | Kernel default, no limit |
| Refresh trigger | `mvm host init` always refreshes; `mvm host info --refresh` same path | Consistent behavior, no special cases |
| Event persistence | SQLite table, not streaming | CLI exits, polling is free with index |
| Rate limiting | Semaphore(5) in VMOperation | Matches Firecracker max: 5 VMs/core/sec |
| sysctl tiers | dev/server/production | Simple progression, no over-engineering |
