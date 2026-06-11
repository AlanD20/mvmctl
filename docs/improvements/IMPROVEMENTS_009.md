# Daemonless Production Readiness — Host Capacity Detection, Events & Admission Control

> **STATUS: Design Document — partially implemented.**
>
> | Section | Status |
> |---------|--------|
> | Host capacity detection (`mvm host info`) | ✅ Implemented — `internal/cli/host.go` backed by `HostOperation.Info()` |
> | HostDetector utility module | ✅ Implemented — `internal/lib/host/detector.go` |
> | host_state schema extension | ✅ Implemented — `host_state` schema includes all capacity columns |
> | Resource accounting table + admission control | ❌ Not implemented |
> | Event log table + `mvm events` command | ❌ Not implemented |
> | Rate-limited VM creation | ❌ Not implemented |
> | `mvm host reconcile` for crash recovery | ❌ Not implemented |
> | sysctl tuning in `mvm host init` | ❌ Not implemented |
> | WAL checkpointing + backup rotation | ❌ Not implemented |
>
> **Last verified:** 2026-06-10

---

**Phase:** Standalone — each section independently shippable
**Complexity:** Medium (low per-section)
**Depends on:** Nothing beyond current architecture

---

## 1. Rationale — Why Not a Daemon

**We keep the CLI-native, daemonless model and enhance it.** The daemon is optional, adds no value for basic operations, and is only needed for auto-healing + live event streaming.

| Concern | Daemon Model | Daemonless Model |
|---------|-------------|------------------|
| **Socket explosion** | Per-VM shim socket + daemon socket + FC socket + console socket | FC socket + console socket only (unchanged) |
| **IPC complexity** | gRPC or HTTP over UDS, serialization, client/server stubs | Go function calls (already works) |
| **Cold start** | Must wait for daemon | Instant |
| **Survival** | Daemon crash loses control plane | FC processes survive CLI exit (already true) |
| **Dev ergonomics** | Daemon must be running | `go run ./cmd/mvm` — always works |
| **Wrapping FC** | Proxy around FC's own UDS API | Work alongside it, not above it |

### What we actually need

1. **Visibility** — What's running? What resources are left?
2. **Safety** — Don't create VMs when resources are exhausted
3. **Recovery** — Detect and clean up after crashes
4. **Observability** — Log events so external tools can react

All four are solvable with SQLite + CLI commands. Zero new daemon infrastructure.

---

## 2. Host Capacity Detection (`mvm host info`) — ✅ IMPLEMENTED

### 2.1 The `--refresh` Flag & Staleness Contract

```
mvm host init          → ALWAYS refreshes static data (calls detect + writes to DB)
mvm host info          → Reads static from DB (stale), computes dynamic fresh
mvm host info --refresh → Re-detects static data, updates DB, computes dynamic fresh
```

### 2.2 Detection Method — Zero Subprocess Calls

**Every single target uses either Go stdlib or a `/proc` file read.** Total cost for all static targets combined: **~500μs**. Total for all dynamic targets combined: **~500μs**. Grand total: **~1-2ms**.

No `ip`, no `sysctl`, no `numactl`, no `free`, no `conntrack`, no `df`, no `lscpu`. All from stdlib + procfs.

---

## 3. Resource Accounting & Admission Control (Not Implemented)

### 3.1 Schema

Both tables are new. Added directly to migration SQL (pre-release).

```sql
CREATE TABLE resource_accounting (
    resource_type TEXT NOT NULL,
    total BIGINT NOT NULL,
    reserved BIGINT NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (resource_type)
);

CREATE TABLE vm_resource_allocations (
    vm_id TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    quantity BIGINT NOT NULL,
    PRIMARY KEY (vm_id, resource_type)
);
```

### 3.2 How It Works

**On VM creation** — Inside a single `BEGIN IMMEDIATE` transaction:
1. For each resource, atomic check-and-reserve
2. If `changes() == 0` for any resource → rollback → raise admission error
3. Write row to `vm_resource_allocations`
4. Proceed with VM creation

**On VM removal** — Release all allocations.

### 3.3 Race Condition Handling

Two terminals running `mvm vm create` simultaneously:
- First writer: `BEGIN IMMEDIATE` acquires write lock → succeeds
- Second writer: blocks on `busy_timeout=5000` (already configured)
- On timeout: retry once with 100ms backoff

Risk is low for a single-user CLI. The atomic `WHERE reserved + ? <= total` prevents silent overcommit even under contention.

---

## 4. Event Log System (Not Implemented)

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
```

### 4.2 `mvm events` CLI Command

```bash
mvm events                         # Last 50 events
mvm events --type vm.crashed       # Filter by type
mvm events --since 5m              # Last 5 minutes
mvm events --since 1h --json       # JSON output for scripts
mvm events --follow                # Poll every 2s (like tail -f)
```

---

## 5. Rate-Limited VM Creation (Not Implemented)

```go
var createSemaphore = make(chan struct{}, 5)

func create(inputs *inputs.VMCreateInput) (*OperationResult, error) {
    select {
    case createSemaphore <- struct{}{}:
        defer func() { <-createSemaphore }()
    case <-time.After(120 * time.Second):
        return nil, errs.New(errs.CodeOperationTimeout, "Too many concurrent VM creations. Try again.")
    }
    return doCreate(inputs)
}
```

Default: 5 concurrent creates. Configurable via `settings.max_parallel_vm_creates`.

---

## 6. Crash Recovery (`mvm host reconcile`) (Not Implemented)

Scans for three types of orphaned state:

| Phase | What It Checks | Action |
|-------|---------------|--------|
| 1 | Running VMs with dead PIDs | Mark as `crashed`, release resources |
| 2 | Active network leases with no VM | Release lease back to pool |
| 3 | TAP devices not owned by any VM | Delete TAP via `system.RunCmd` |

Idempotent. Safe to cron. Does NOT restart VMs or delete VM directories.

---

## 7. sysctl Tuning in `mvm host init` (Not Implemented)

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

## 8. SQLite Hardening (WAL Checkpointing & Backups) (Not Implemented)

- Periodic WAL checkpoint: `PRAGMA wal_checkpoint(PASSIVE)` every 5 min (daemon mode) or every 10th write (daemonless)
- Snapshot rotation: keep last 5 `.bak` files, delete oldest on new snapshot
- Existing `_take_snapshot()` uses SQLite's backup API (already implemented)

---

## 9. Implementation Roadmap

### Phase 1: Host Capacity Detection (1-2 weeks) ✅ DONE

### Phase 2: Resource Accounting + Events (1-2 weeks)

| Step | Effort |
|------|--------|
| Add `resource_accounting` + `vm_resource_allocations` tables | 0.5 day |
| Implement atomic check-and-reserve in `VMOperation.Create()` | 1 day |
| Add `EventLogger` + `event_log` table | 0.5 day |
| Add `mvm events` CLI command | 0.5 day |
| Add rate-limited creation (semaphore) | 0.5 day |
| **Total** | **~3 days** |

### Phase 3: Recovery + Hardening (1 week)

| Step | Effort |
|------|--------|
| Add `mvm host reconcile` | 1 day |
| Add sysctl tuning to `mvm host init` | 1 day |
| Add WAL checkpoint + snapshot rotation | 0.5 day |
| **Total** | **~2.5 days** |

**Total Phase 2-3: ~5.5 days.** All daemonless. All production value.

---

## Appendix: Architectural Decisions Log

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Daemon vs daemonless | Daemonless first, daemon optional | Socket explosion, no IPC, FC already survives |
| Storage format | SQLite direct, no new migration | Pre-release product, modify schema in place |
| Detection speed target | ~1-2ms total | All from /proc + stdlib, zero subprocess |
| Staleness contract | Static cached in DB, dynamic computed on read | Hardly matters (1-2ms to refresh both) |
| `recommended_max_vms` assumption | 512 MiB guest RAM, configurable | Matches default VM memory |
| Event persistence | SQLite table, not streaming | CLI exits, polling is free with index |
| Rate limiting | Semaphore(5) in VMOperation | Matches Firecracker max: 5 VMs/core/sec |
| sysctl tiers | dev/server/production | Simple progression, no over-engineering |
