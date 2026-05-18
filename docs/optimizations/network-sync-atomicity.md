# Network Sync Atomicity — Zero-Downtime Firewall Rule Replacement

> **STATUS: Current — corrected.** The iptables backend description was updated to reflect that `IPTablesTracker.batch_ensure_rules()` uses `iptables-restore` for atomic batch per table (not per-rule individual calls). All mechanisms described below are implemented in `src/mvmctl/`. The nftables backend is the default (`settings.firewall_backend: "nftables"`); iptables is the legacy fallback. See also [ADR-0010] (firewall backend mutual exclusion) and the `CONTEXT.md` firewall backend section.
>
> **Line numbers** in code references below match the current files at commit time.

## Overview

`mvm network sync` is the canonical way to restore networking state after a host reboot and to ensure the kernel's firewall rules match the database. It is safe to run at **any time** — during normal operation, after a crash, or as a periodic reconciliation — without disrupting running VMs.

This is important because firewall rule persistence to system files (iptables-save restore on boot) was intentionally removed. Instead of relying on distribution-specific boot scripts that may race or fail silently, `mvm network sync` is the single source-of-truth mechanism for restoring firewall state. Running it is a no-op when state is already consistent.

## How It Works — Three Phases

`NetworkOperation.sync()` in `src/mvmctl/api/network_operations.py` orchestrates three phases for each network:

### Phase 1: Bridge Restoration (Post-Reboot Recovery)

```
NetworkOperation.sync() → bridge restoration phase
```

For each network, if the bridge interface does not exist on the host, it is recreated with its correct IP address, and NAT rules are re-applied. This handles the common case where a host reboot destroys all kernel networking state but leaves the database intact.

```python
if not NetworkUtils.bridge_exists(network.bridge):
    bridge_addr = NetworkUtils.compute_bridge_address(
        network.ipv4_gateway, network.subnet
    )
    service.ensure_bridge(network.bridge, bridge_addr)
    if network.nat_enabled:
        service.ensure_nat(...)
```

### Phase 2: Bridge State Reconciliation

```
network_operations.py:667-672
```

For every network, the `is_present` flag in the database is reconciled against actual bridge existence in the kernel. This ensures that `mvm network ls` and downstream operations see accurate state without a separate refresh command.

### Phase 3: Firewall Rule Sync

```
NetworkOperation.sync()  →  NetworkService.sync_iptables_rules()
```

The core sync logic lives in `NetworkService.sync_iptables_rules()` in `src/mvmctl/core/network/_service.py`:

1. **Fetch active DB rules** for the network via `self._tracker.repo.get_by_network_id(network.id, active_only=True)`.
2. **Batch-ensure each rule** inside a `FirewallTracker.batch()` context — all `ensure_rule` calls are queued and flushed atomically on context exit (nftables) or individually (iptables).
3. **Count orphaned host rules** that reference this network but have no matching active DB record (informational only — orphans are not removed).

```python
db_rules = self._tracker.repo.get_by_network_id(network.id, active_only=True)
with self._tracker.batch():
    for rule in db_rules:
        result = self._tracker.ensure_rule(rule)
        # result.command_executed is None → rule already existed (verified)
        # result.command_executed is set → rule was newly added
orphaned = self._tracker.count_orphaned_rules(network)
```

## Backend-Specific Atomicity Mechanisms

The `FirewallTracker` abstraction at `src/mvmctl/core/_shared/_firewall_tracker.py` delegates to one of two backends. The batch context operates differently depending on the backend.

### nftables (default) — Atomic Batch via `nft -f -`

```
FirewallTracker.batch() context  →  NFTablesTracker.batch_ensure_rules()
```

When inside a batch context, `ensure_rule` queues the rule. On context exit, `NFTablesTracker.batch_ensure_rules()`:

1. For each queued rule, checks whether it already exists in the database (deduplication).
2. For rules that are truly new, generates an `add rule ip <table> <chain> <expr>` statement.
3. Pipes the complete nft script to a single `nft -f -` invocation:

```python
nft_script = "\n".join(lines) + "\n"
run_cmd(["nft", "-f", "-"], privileged=True, input=nft_script)
```

**Why this is atomic:** `nft -f -` is implemented as a single Netlink transaction. The kernel either applies all rules or rejects the entire batch. There is no window where only half the rules are active. This is the same mechanism `nftables` itself uses for `nft -f` file-based rule loading.

**No flush needed:** The batch is additive — it only inserts `add rule` statements for rules missing from the kernel. Existing rules are left untouched. This means:
- MVM custom chains (MVM-FORWARD, MVM-POSTROUTING, MVM-NOCLOUDNET-INPUT) are **never flushed** during sync.
- Only the system's built-in chains (FORWARD, POSTROUTING, INPUT) contain jump rules at position 0 — those are managed by `NFTablesTracker.initialize()` and are never touched during sync.

### iptables (legacy) — Atomic Batch via `iptables-restore`

```
FirewallTracker.batch() context  →  IPTablesTracker.batch_ensure_rules()
```

The iptables backend's `batch_ensure_rules` **does** use `iptables-restore` for atomic batch per table:

```python
def batch_ensure_rules(self, rules: list[FirewallRule]) -> FirewallRuleResult:
    filter_rules = [r for r in rules if r.table_name == FirewallTable.FILTER]
    nat_rules = [r for r in rules if r.table_name == FirewallTable.NAT]
    try:
        if filter_rules:
            restore_input = self._build_restore_input(filter_rules, "filter")
            run_cmd(["iptables-restore"], input=restore_input, privileged=True)
        if nat_rules:
            restore_input = self._build_restore_input(nat_rules, "nat")
            run_cmd(["iptables-restore"], input=restore_input, privileged=True)
    except ProcessError as e:
        return FirewallRuleResult(success=False, error_message=str(e))
    return FirewallRuleResult(success=True)
```

The `_build_restore_input` method constructs an `iptables-restore`-compatible input stream for each table:
1. `*{table}` header.
2. Define MVM custom chains with zero counters (`:CHAIN - [0:0]`).
3. Flush only MVM chains (`-F CHAIN`), not the entire table.
4. Add conntrack `ESTABLISHED,RELATED` accept as the first rule in filter chains (preserves established connections during the atomic swap).
5. Append all queued rules as `-A CHAIN ...` lines.
6. `COMMIT` to apply the transaction.

**Why this is atomic per table:** `iptables-restore` applies the entire input as a single kernel transaction. The kernel either commits all changes or rejects the entire batch. There is no window where only half the rules are active. The conntrack accept rule at position 0 ensures existing flows are never disrupted during the atomic swap.

**MVM chain flush is safe:** The flush targets only MVM custom chains (MVM-FORWARD, MVM-POSTROUTING, MVM-NOCLOUDNET-INPUT), not built-in chains (FORWARD, POSTROUTING, INPUT). Jump rules from built-in chains are left untouched — established by `IPTablesTracker.initialize()` and never modified during sync.

## Connection Safety

### Conntrack Independence

The Linux connection tracking system (`conntrack`) operates independently of netfilter filter rules. When a packet establishes a connection:

1. The first packet traverses the filter chains (FORWARD, etc.) and creates a conntrack entry if it passes.
2. All subsequent packets in that flow match the existing conntrack entry and bypass the filter chains entirely (`NOTRACK` / established path).

This means:
- **Adding rules never interrupts existing flows.** New `accept` rules only affect new connection attempts.
- **Removing rules (not done by sync) would not interrupt existing flows** either — conntrack bypasses filter rules for established connections.
- **NAT mappings** are stored in conntrack entries, not re-evaluated on subsequent packets. Existing masquerade sessions continue uninterrupted even if the MASQUERADE rule were removed.

### Additive-Only Design

The sync is **strictly additive** — it ensures that every rule in the database is present in the kernel. It never deletes or modifies existing kernel rules. This is the key design choice that makes the operation safe to run while VMs are active:

- Existing traffic continues to pass through rules that were already in place.
- New traffic benefits from rules that were missing (e.g., after a reboot).
- Orphaned host rules (present in kernel but absent from DB) are detected and reported but **not removed**, guaranteeing zero disruption.

### Chains Structure

```
Built-in chains (never flushed):
  FORWARD ──[jump 0]──> MVM-FORWARD
  POSTROUTING ──[jump 0]──> MVM-POSTROUTING
  INPUT ──[jump 0]──> MVM-NOCLOUDNET-INPUT

MVM custom chains (add rules here during sync):
  MVM-FORWARD          (ip filter) — FORWARD accept rules per TAP/NAT
  MVM-POSTROUTING      (ip nat)    — MASQUERADE rules per gateway
  MVM-NOCLOUDNET-INPUT (ip filter) — nocloud-net accept rules
```

The jump rules at position 0 are established once by `NFTablesTracker.initialize()` (or `IPTablesTracker.initialize()`) and are never modified during sync. This guarantees that MVM rules are always evaluated before any third-party rules (UFW, etc.) without needing to touch the system's built-in chains.

## Subprocess Cost

`mvm network sync` was optimized to minimize subprocess invocations. The costs below assume 20 networks with 10 firewall rules each (typical for a NAT gateway + a few TAP-attached VMs).

### nftables (~62 calls total)

| Operation | Calls per network | Total (20 networks) |
|-----------|------------------:|--------------------:|
| Bridge check (`ip link show`) | 1 | 20 |
| `nft -f -` batch (all new rules in one call) | 1 | 20 |
| Orphan scan per chain (`nft -a list chain ip <table> <chain>`) | 3 | 60 |
| **Active rule check (DB lookup only)** | **0 subprocess** | **0** |
| **Total subprocess calls** | **~3.1** | **~62** |

The nftables backend avoids per-rule subprocess calls by deduplicating against the database in-process and issuing a single `nft -f -` for all new rules. The orphan scan uses 3 `nft -a list chain` calls to enumerate all rules in the three MVM chains and cross-reference them against DB records in Python.

### iptables (~62-82 calls total)

| Operation | Calls per network | Total (20 networks) |
|-----------|------------------:|--------------------:|
| Bridge check (`ip link show`) | 1 | 20 |
| `iptables-restore` batch (filter table) | 1 | 20 |
| `iptables-restore` batch (nat table, if any nat rules) | 0-1 | 0-20 |
| Orphan scan (`iptables-save`) | 1 | 20 |
| **Total subprocess calls** | **~3-4** | **~62-82** |

The iptables backend uses `iptables-restore` per table, so the number of subprocess calls is **constant per network** regardless of how many rules need to be added — the `iptables-restore` call covers all rules in one atomic transaction per table. This makes the iptables backend's sync cost comparable to nftables (~62-82 calls vs ~62 calls for 20 networks).

### Key Insight

Both backends now use atomic batch operations and have comparable subprocess call counts for the sync operation (~62 calls for nftables vs ~62-82 for iptables for 20 networks). The number of subprocess calls is **constant per network** for both backends, regardless of how many rules need to be added — `nft -f -` (nftables) and `iptables-restore` (iptables) each cover all rules in one atomic transaction per table.

## Related Documents

- **ADR-0010** — Firewall backend mutual exclusion (iptables vs nftables).
- **CONTEXT.md** — Firewall backend section, domain language, chain naming.
- **`next-level-optimizations.md` section 4.3** — Original nftables migration design.
- **`src/mvmctl/core/_shared/_firewall_tracker.py`** — Unified tracker abstraction.
- **`src/mvmctl/core/_shared/_nftables_tracker/_tracker.py`** — nftables backend.
- **`src/mvmctl/core/_shared/_iptables_tracker/_tracker.py`** — iptables backend.
- **`src/mvmctl/core/network/_service.py`** — `sync_iptables_rules()` entry point.
- **`src/mvmctl/api/network_operations.py`** — `NetworkOperation.sync()` orchestrator.
