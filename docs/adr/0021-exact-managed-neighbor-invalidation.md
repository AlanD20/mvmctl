# Exact Managed-Bridge Neighbor Invalidation Before VM Exposure

**Status:** Active
**Date:** 2026-09-27
**Supersedes:** [ADR-0006](0006-arp-cache-flush-before-vm-spawn.md)
**Related:** [ADR-0016](0016-root-owned-single-binary-privileged-dispatch.md), [ADR-0017](0017-per-vm-network-namespaces-and-typed-policies.md)

## Context

ADR-0006 addressed the delay when a new VM reused an IPv4 address while the host still held the old IP-to-MAC
neighbor entry. Its bridge-wide `ip neigh flush` used a raw privileged subprocess and discarded entries for other VMs.
That helper lost its caller during the 0.3.0 typed-network cutover. Per-VM namespaces do not make the old concern
irrelevant: managed VMs still attach through a shared host bridge with a connected subnet route. A new endpoint may
reuse the old address on that bridge.

The invoking user must not select a host interface, namespace, or arbitrary IPv4 deletion target. An administrator
may deliberately change kernel state; 0.3.0 trusts administrators but refuses detected foreign or incompatible state.

## Decision

During managed topology activation, after endpoint reservation and firewall grant and immediately before raising the
host veth, the authenticated root operation invalidates only the new VM's guest IPv4 neighbor on its registered
managed bridge. It derives the address from the admitted endpoint and the bridge from retained root network/topology
authority. The operation verifies the exact host namespace and bridge/link identities while the host veth is still
down; the managed-network L3 record and firewall endpoint are revalidated by the caller at that activation stage.

The root netlink adapter issues an exact IPv4 `RTM_GETNEIGH` for the bridge ifindex and guest address. Absence is a
successful no-op. Only a canonical dynamic unicast entry with ordinary flags may be removed; permanent, proxy,
foreign, and malformed entries fail closed. It then issues `RTM_DELNEIGH` for that exact key and performs a bounded
read-back proving absence. If kernel neighbor GC wins the race and deletion alone reports `ENOENT`, proven absence
still succeeds; a joined cancellation, transport, or close failure does not. Other errors or unproven deletion abort
launch and use the existing topology cleanup path. There is no new public neighbor command or generic privileged
netlink request.

## Consequences and validation

- Other guest addresses on the bridge are untouched; the old whole-bridge flush and its raw-sudo helper are removed.
- The operation is idempotent for an absent entry. It does not promise isolation against a trusted administrator
  changing neighbors concurrently after the read-back.
- Pinned Go 1.26.3 package tests, vet, and a race run passed in `mvmctl-030-test`. A bounded real-kernel probe in a
  disposable runner namespace removed one stale dynamic entry, rejected a permanent entry, and confirmed namespace
  teardown. This proves the netlink protocol on that kernel, not full managed VM/IP-reuse behavior.
- The current named runner supports the modeled nftables bridge family, and the network/nftables/policy Python domain
  passed there. A two-VM reused-IP journey has not yet proved this exact neighbor invalidation during real VM launch;
  that remains a release gate.

## Alternatives

- **Keep a whole-bridge flush:** rejected because it removes unrelated neighbors and needs the retired raw-sudo path.
- **Never reuse guest addresses:** wastes finite subnet capacity and changes lease semantics.
- **Rely on guest gratuitous ARP:** depends on guest behavior and would not provide the host-side pre-exposure proof.
- **Do nothing:** preserves the documented stale-neighbor first-connection risk.
