# Firewall Backend Mutual Exclusion — nftables vs iptables

**Status:** accepted

The project provides two independent firewall rule tracking backends: **nftables** (default) and **iptables**. These backends are **mutually exclusive** — a single session uses exactly one backend, never a combination. The `firewall_backend` setting acts as a toggle selector.

## Mutual Exclusion Rule

Only one backend is active for a given session. The selection logic in `FirewallTracker.__init__()`:

1. Read the `firewall_backend` setting (from user settings / `OVERRIDABLE_DEFAULTS`).
2. If the value is `"nftables"` → use **NFTablesTracker**.
3. Else → use **IPTablesTracker**.

The setting defaults to `"nftables"` in `constants.py` (`OVERRIDABLE_DEFAULTS["settings"]["firewall_backend"] = "nftables"`). It can be changed via `mvm config set settings firewall_backend iptables`.

## Independence

Each backend lives in its own directory under `core/_shared/` with its own tracker, repository, and resolver. They never share runtime state:

| Aspect | nftables | iptables |
|--------|----------|----------|
| Mechanism | `nft -f -` with atomic batch files | Per-rule `iptables` / `iptables-save` calls |
| Implementation | `NFTablesTracker` → `NFTablesRuleRepository` | `IPTablesTracker` → `IPTablesRuleRepository` |
| Batch flush | Single atomic `nft -f -` | Individual rule execution |
| DB table | `nftables_rules` | `iptables_rules` |
| Resolver | `NFTablesRuleResolver` | `IPTablesRuleResolver` |
| Default | **Yes** (`firewall_backend: "nftables"`) | **No** (fallback when value is not `"nftables"`) |

## No Mixing

The `FirewallTracker` selects the backend once at construction time. All `ensure_rule()`, `remove_rule()`, and batch operations go through the same backend for the lifetime of the tracker instance. Both DB tables (`iptables_rules`, `nftables_rules`) exist in the schema and are populated independently depending on which backend is active — they are never both used in the same session.

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

## Related Decisions

- ADR-0006: Provisioning backend mutual exclusion (same architecture pattern for rootfs provisioning).
- ADR-0009: Sudo privilege architecture — both backends use `PRIVILEGED_BINARIES` for privilege escalation.
