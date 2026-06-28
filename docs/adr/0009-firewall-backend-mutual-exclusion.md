# Firewall Backend Mutual Exclusion — nftables vs iptables

**Status:** Active
**Date:** 2026-05-22

The project provides two independent firewall rule tracking backends: **nftables** (default) and **iptables** (legacy). These backends are **mutually exclusive** — a single session uses exactly one backend, never a combination. The `firewall_backend` setting acts as a toggle selector.

**Table of Contents**

- [Mutual Exclusion Rule](#mutual-exclusion-rule)
- [Independence](#independence)
- [No Mixing](#no-mixing)
- [Why Default to nftables](#why-default-to-nftables)
- [Related Decisions](#related-decisions)

## Mutual Exclusion Rule

Only one backend is active for a given session. The caller resolves the `firewall_backend` setting from config and passes it as a `model.FirewallBackendType` to `NewFirewallTracker()`:

1. Read the `firewall_backend` setting (from user settings / `OverridableDefaults`).
2. Map `"nftables"` to `model.FirewallBackendNFTables`, `"iptables"` to `model.FirewallBackendIPTables`.
3. `NewFirewallTracker(backend, xtcommentAvail, db)` switches on the typed constant to construct **NFTablesTracker** or **IPTablesTracker**.

The setting defaults to `"nftables"` in `internal/infra/constants.go` (`OverridableDefaults["settings"]["firewall_backend"] = "nftables"`). It can be changed via `mvm config set settings firewall_backend iptables`.

## Independence

Each backend lives in its own package under `internal/lib/firewall/` with its own tracker, repository, and rules. They never share runtime state:

| Aspect | nftables | iptables |
|--------|----------|----------|
| Mechanism | `nft -f -` with atomic batch files | Per-rule iptables calls |
| Implementation | `internal/lib/firewall/nftables.go` | `internal/lib/firewall/iptables.go` |
| Batch flush | Single atomic `nft -f -` | Individual rule execution |
| DB table | `nftables_rules` | `iptables_rules` |
| Tracker | `NFTablesTracker` | `IPTablesTracker` |
| Default | **Yes** (`firewall_backend: "nftables"`) | **No** (fallback when value is not `"nftables"`) |

## No Mixing

The `FirewallTracker` selects the backend once at construction time. All `EnsureRule()`, `RemoveRule()`, and batch operations go through the same backend for the lifetime of the tracker instance. Both DB tables (`iptables_rules`, `nftables_rules`) exist in the schema and are populated independently depending on which backend is active — they are never both used in the same session.

## Why Default to nftables

nftables is the modern replacement for iptables and is the default firewall on RHEL 9+, Debian 11+, Ubuntu 22.04+, and Arch Linux. nftables provides:
- **Atomic batch operations** via `nft -f -` — all rules in a batch are applied or none are, preventing partial-apply states.
- **Cleaner rule management** — no separate `iptables-save`/`iptables-restore` workflow.
- **Better performance** for large rule sets.

### UFW Compatibility

The nftables backend avoids the cross-table `accept`-is-not-terminal problem by using **non-hook chains** inside the system `ip filter` and `ip nat` tables, with jump rules inserted at **position 0** of the built-in chains (FORWARD, POSTROUTING, INPUT). This mirrors the iptables-nft approach: a `jump MVM-FORWARD` rule at the top of FORWARD ensures MVM rules evaluate before UFW's, and the `accept` verdict terminates processing within the table — UFW's `policy drop` is never reached.

When UFW reloads (e.g. `ufw reload`), it flushes the built-in chains and removes MVM's jump rules. These are re-created lazily on the next `mvm network` or `mvm vm create` operation. The iptables backend has the same limitation — both are recovered with idempotent `ensure_*` operations.

Users who prefer the iptables compatibility layer can switch:
```
mvm config set settings firewall_backend iptables
```

> **Implementation Note:** The `FirewallTracker` also reads an `iptables_xtcomment` user setting that, when enabled, adds comment tags to iptables rules for easier identification. This is supported alongside the primary mutual-exclusion pattern.

## Related Decisions

- ADR-0003: Provisioning backend mutual exclusion (same architecture pattern for rootfs provisioning).
- ADR-0005: Sudo privilege architecture — both backends use `PrivilegedBinaries` for privilege escalation.
