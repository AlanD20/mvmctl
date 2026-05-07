# nftables for iptables at scale

**Phase:** Standalone — when iptables becomes a bottleneck
**Complexity:** High
**Depends on:** Nothing, but only matters at 50+ VMs

## Problem

At scale (50+ VMs on one bridge), iptables ruleset performance degrades:
- iptables does O(n) linear chain walk for each packet
- 50 VMs = 50 TAP devices = 100 FORWARD rules + 50 NAT rules
- nftables sets are O(1) lookup — no linear scan

## What changes

Refactor `core/_shared/_iptables_tracker/` to support an nftables backend.

**Current architecture:**
- `IPTablesTracker` manages iptables rules via `iptables` CLI subprocess calls
- Rule state tracked in `iptables_rules` DB table
- All rules use iptables chains (`MVM-FORWARD`, `MVM-POSTROUTING`, `MVM-NOCLOUDNET-INPUT`)

**Target architecture:**
- Abstract `IPTablesTracker` behind a `FirewallBackend` interface
- `IptablesBackend` — existing implementation (default)
- `NftablesBackend` — new implementation using `nft` CLI
- Auto-detect backend, or configurable via `mvm config`

## Why high complexity

- The `_iptables_tracker/` module has ~600 lines of iptables-specific logic
- nftables syntax is different (sets, maps, chains vs. tables)
- Rule tracking, cleanup, and orphan detection all need backend-aware paths
- Dual-maintenance until iptables is removed

## Why standalone

This is purely a performance optimization. Feature-complete without it. Only matters when users actually hit the scale where iptables becomes slow.
