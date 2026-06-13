# Optimizations

This directory documents the performance optimizations implemented in the Go `mvmctl` codebase. Each document describes the problem, the Go implementation, code locations, and performance characteristics.

| Document | Focus | Status |
|---|---|---|
| [fast-durable-image-copy.md](fast-durable-image-copy.md) | sendfile(2) + io.Copy + dd fallback + fdatasync | ✅ Implemented |
| [guestfs-boot.md](guestfs-boot.md) | libguestfs appliance optimizations via guestfish CLI | ✅ Implemented |
| [network-sync-atomicity.md](network-sync-atomicity.md) | Atomic firewall rule replacement (nftables/iptables) | ✅ Implemented |
| [next-level-optimizations.md](next-level-optimizations.md) | Forward-looking roadmap for sub-100ms VM creation | ⏳ Planning |

> **Origin:** These documents were ported from the legacy Python codebase at `legacy/python/docs/optimizations/` and updated to reflect the actual Go implementation. Code references point to Go source files.
