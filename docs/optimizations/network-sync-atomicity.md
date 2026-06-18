# Network Sync Atomicity — Zero-Downtime Firewall Rule Replacement

> **STATUS: Current — fully accurate.** All mechanisms described below are implemented in `internal/lib/firewall/`. The nftables backend is the default (`firewall_backend: "nftables"` at `internal/infra/constants.go:123`); iptables is the legacy fallback. See also [ADR-0009](../adr/0009-firewall-backend-mutual-exclusion.md) (firewall backend mutual exclusion) and `internal/infra/constants.go` for defaults.

## Overview

`mvm network sync` is the canonical way to restore networking state after a host reboot and to ensure the kernel's firewall rules match the database. It is safe to run at **any time** — during normal operation, after a crash, or as a periodic reconciliation — without disrupting running VMs.

Running it is a no-op when state is already consistent.

## How It Works — Three Phases

`NetworkSync()` in `pkg/api/network.go:307-385` orchestrates three phases for each network:

### Phase 1: Bridge Restoration (Post-Reboot Recovery)

For each network, if the bridge interface does not exist on the host, it is recreated with its correct IP address, and NAT rules are re-applied. This handles the common case where a host reboot destroys all kernel networking state but leaves the database intact.

**Code reference:** `pkg/api/network.go:329-350`

```go
if !libnet.DefaultNetOps.BridgeExists(ctx, net.Bridge) {
    bridgeAddr, calcErr := network.ComputeBridgeAddress(net.IPv4Gateway, net.Subnet)
    // ...
    if err := op.Services.Network.EnsureBridge(ctx, net.Bridge, bridgeAddr); err != nil { ... }
    if net.NATEnabled {
        if err := op.Services.Network.EnsureNAT(ctx, ...); err != nil { ... }
    }
}
```

### Phase 2: Bridge State Reconciliation

For every network, the `BridgeActive` field (originally named `is_present`) is reconciled against actual bridge existence in the kernel. This ensures that `mvm network ls` and downstream operations see accurate state.

**Code reference:** `pkg/api/network.go:353-358`

```go
bridgeActive := libnet.DefaultNetOps.BridgeExists(ctx, net.Bridge)
if bridgeActive != net.BridgeActive {
    _ = op.Repos.Network.UpdateBridgeActive(ctx, net.ID, bridgeActive)
}
```

### Phase 3: Firewall Rule Sync

The core sync logic lives in `SyncIPTablesRules()` at `internal/core/network/service.go:546-591`:

1. **Fetch active DB rules** for the network via `s.firewallTracker.GetByNetworkID(network.ID, true)`.
2. **Batch-ensure each rule** inside a `WithBatch()` context — all `EnsureRule` calls are queued and flushed atomically on context exit.
3. **Count orphaned host rules** that reference this network but have no matching active DB record (informational only — orphans are not removed).

**Code reference:** `internal/core/network/service.go:546-591`

```go
dbRules, err := s.firewallTracker.GetByNetworkID(ctx, network.ID, true)
if s.firewallTracker != nil && len(dbRules) > 0 {
    s.WithBatch(ctx, func() {
        for _, rule := range dbRules {
            result := s.firewallTracker.EnsureRule(ctx, *rule, "")
            if result.Success {
                if result.CommandExecuted == nil {
                    verified++
                } else {
                    added++
                }
            }
        }
    })
}
orphaned = s.firewallTracker.CountOrphanedRules(ctx, network)
```

## Backend-Specific Atomicity Mechanisms

The `FirewallTracker` at `internal/lib/firewall/tracker.go` dispatches to one of two backends. The batch context (`WithBatch()`) queues `EnsureRule` calls and flushes to the backend on context exit.

### nftables (default) — Atomic Batch via `nft -f -`

When inside a batch context, `EnsureRule` appends to an internal slice. On context exit, `flushBatch()` calls `NFTablesTracker.BatchEnsureRules()` which:

1. **Flushes MVM custom chains** (MVM-FORWARD, MVM-POSTROUTING, MVM-NOCLOUDNET-INPUT) — removes all existing rules.
2. **Inserts conntrack accept rules** at position 0 of filter chains — preserves established connections.
3. **Adds all DB rules** as `add rule ip <table> <chain> <expr>` statements.
4. **Pipes** the complete script to `nft -f -`.

**Code reference:** `internal/lib/firewall/nftables.go:556-620`

```go
for chain, table := range nftChainToTable {
    lines = append(lines, fmt.Sprintf("flush chain ip %s %s", table, string(chain)))
}
// Conntrack rule first
for chain, table := range nftChainToTable {
    if table == "filter" {
        lines = append(lines,
            fmt.Sprintf("add rule ip %s %s ct state established,related accept",
                table, string(chain)))
    }
}
// Add all batch rules
for i := range rules {
    // ... build nftExpr ...
    lines = append(lines, fmt.Sprintf("add rule ip %s %s %s", ...))
}
nftScript := strings.Join(lines, "\n") + "\n"
result, _ := system.DefaultRunner.Run(
    ctx, []string{"nft", "-f", "-"},
    RunCmdOpts{Privileged: true, Capture: true, Check: true, Input: nftScript},
)
```

**Why this is atomic:** `nft -f -` is implemented as a single Netlink transaction. The kernel either applies all rules or rejects the entire batch. There is no window where only half the rules are active.

**Conntrack accept rule at position 0** ensures that existing connections (SSH sessions, active HTTP streams) are preserved through the atomic swap. The conntrack entries remain valid, and packets matching established connections bypass the filter chains entirely.

**MVM chain flush is safe:** The flush targets only MVM custom chains — not the system's built-in chains (FORWARD, POSTROUTING, INPUT). The jump rules from built-in chains to MVM chains (inserted at position 0 by `Initialize()`) are never touched during sync.

### iptables (legacy) — Atomic Batch via `iptables-restore -n`

The iptables backend's `BatchEnsureRules` uses `iptables-restore -n` for atomic batch per table. The `-n` flag tells `iptables-restore` not to flush the table, only to add the rules from the input — this prevents clearing rules from other tools.

The Go implementation calls `iptables-restore -n` once per table (filter, nat) with a constructed restore input:

```
*{table}
:CHAIN - [0:0]         # Define MVM chain with zero counters
-F CHAIN               # Flush only MVM chain (not entire table)
-A CHAIN -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT   # conntrack rule
-A CHAIN ...            # All queued rules, one per line
COMMIT
```

**Why this is atomic per table:** `iptables-restore` applies the entire input as a single kernel transaction. The kernel either commits all changes or rejects the entire batch. The `-n` flag prevents the default table flush behavior, ensuring only the specified MVM chains are modified.

**Code reference:** `internal/lib/firewall/iptables.go` (lines 200+)

## Connection Safety

### Conntrack Independence

The Linux connection tracking system (`conntrack`) operates independently of netfilter filter rules. When a packet establishes a connection:

1. The first packet traverses the filter chains and creates a conntrack entry if it passes.
2. All subsequent packets in that flow match the existing conntrack entry and bypass the filter chains entirely.

This means:
- **Adding rules never interrupts existing flows.** New `accept` rules only affect new connection attempts.
- **NAT mappings** are stored in conntrack entries, not re-evaluated on subsequent packets. Existing masquerade sessions continue uninterrupted even if the MASQUERADE rule were momentarily absent.

### Replacement-Style Design (MVM Chains Only)

The sync uses a **replacement-style** approach within MVM custom chains:

- **MVM custom chains** (MVM-FORWARD, MVM-POSTROUTING, MVM-NOCLOUDNET-INPUT) are flushed and all active DB rules are re-added atomically.
- **Built-in chains** (FORWARD, POSTROUTING, INPUT) are never touched — their jump-to-MVM-chain rules survive untouched.
- **Conntrack accept rule** is inserted at position 0 of filter MVM chains, preserving established connections through the atomic swap (both backends).
- **Orphaned host rules** within MVM chains are **automatically removed** by the flush-and-rebuild cycle. Orphans outside MVM chains (in built-in chains) are reported but **not removed**, guaranteeing zero disruption to third-party firewall rules.

### Chains Structure

```
Built-in chains (never flushed):
  FORWARD ──[jump position 0]──> MVM-FORWARD
  POSTROUTING ──[jump position 0]──> MVM-POSTROUTING
  INPUT ──[jump position 0]──> MVM-NOCLOUDNET-INPUT

MVM custom chains (flushed and rebuilt from DB during sync):
  MVM-FORWARD          (ip filter) — FORWARD accept rules per TAP/NAT
  MVM-POSTROUTING      (ip nat)    — MASQUERADE rules per gateway
  MVM-NOCLOUDNET-INPUT (ip filter) — nocloud-net accept rules
```

The jump rules at position 0 are established once by `NFTablesTracker.Initialize()` (or `IPTablesTracker.Initialize()`) and are never modified during sync. This guarantees that MVM rules are always evaluated before any third-party rules (UFW, etc.) without needing to touch the system's built-in chains.

## Orphaned Rule Detection

Both backends implement `CountOrphanedRules()` to detect host rules that reference a network but have no matching active DB record:

- **nftables**: Lists each MVM chain via `nft -a list chain`, extracts comments via regex `comment\s+"([^"]+)"`, cross-references against DB.
- **iptables**: Reads `iptables-save` output, matches `-A MVM-` lines with comment containing the network name.

Orphans are counted and logged but **never removed**. The replacement-style flush-and-rebuild implicitly cleans orphans inside MVM chains.

## Subprocess Cost

`mvm network sync` minimizes subprocess invocations by batching all firewall rules into a single atomic call per network.

### nftables (~62 calls for 20 networks)

| Operation | Calls per network | Total (20 networks) |
|---|---|---|
| Bridge check (`ip link show`) | 1 | 20 |
| `nft -f -` batch (all rules in one call) | 1 | 20 |
| Orphan scan per chain (`nft -a list chain`) | 3 | 60 |
| **External DNS test** (if using traffic rules) | Optional | Optional |
| **Total subprocess calls** | **~3.1** | **~62** |

The nftables backend avoids per-rule subprocess calls by queuing rules in the `WithBatch()` context and issuing a single `nft -f -` for all rules. The orphan scan uses 3 `nft -a list chain` calls to enumerate all rules in the three MVM chains and cross-reference them against DB records in Go.

### iptables (~62-82 calls for 20 networks)

| Operation | Calls per network | Total (20 networks) |
|---|---|---|
| Bridge check (`ip link show`) | 1 | 20 |
| `iptables-restore -n` batch (filter table) | 1 | 20 |
| `iptables-restore -n` batch (nat table, if any nat rules) | 0-1 | 0-20 |
| Orphan scan (`iptables-save`) | 1 | 20 |
| **Total subprocess calls** | **~3-4** | **~62-82** |

The iptables backend uses `iptables-restore -n` per table, so the number of subprocess calls is **constant per network** regardless of how many rules need to be added.

### Key Insight

Both backends use atomic batch operations with comparable subprocess call counts for the sync operation (~62 calls for nftables vs ~62-82 for iptables for 20 networks). The number of subprocess calls is **constant per network** for both backends — `nft -f -` (nftables) and `iptables-restore -n` (iptables) each cover all rules in one atomic transaction per table.

## Related Files

- `internal/lib/firewall/tracker.go` — `FirewallTracker` dispatcher
- `internal/lib/firewall/nftables.go` — `NFTablesTracker` (789 lines)
- `internal/lib/firewall/iptables.go` — `IPTablesTracker` (920 lines)
- `internal/core/network/service.go` — `SyncIPTablesRules()` (line 546)
- `pkg/api/network.go` — `NetworkSync()` (line 307)
- `internal/infra/constants.go` — `firewall_backend` default (line 123)
- `docs/adr/0009-firewall-backend-mutual-exclusion.md` — ADR for firewall backend
- `docs/RUNTIME.md` — Backend system documentation (firewall section)
