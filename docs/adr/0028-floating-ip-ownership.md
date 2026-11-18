# Floating IP Ownership

**Status:** Reservations and secondary firewall backend implemented; ownership/failover not release-qualified
**Date:** 2026-11-17
**Amends:** [ADR-0017](0017-per-vm-network-namespaces-and-typed-policies.md), for explicitly authorized secondary IPv4 addresses
**Requires:** [Guest Control authority](0027-guest-control-authority.md)

## Context

The HA Kubernetes example currently assumes an unregistered keepalived address. Primary-only anti-spoofing
correctly rejects that address. Removing anti-spoofing or permitting arbitrary guest addresses would allow
impersonation. Firecracker's existing NIC can carry a guest-configured secondary address; no second NIC is needed.

## Decision

A floating IP is an operator-owned resource within one managed network, separate from a VM's primary address.
Names are unique within that network; immutable resource IDs identify grants and mutations.
Allocate an explicit usable IPv4 address or select an available one. Exclude the gateway, network, broadcast,
primary leases and every other floating reservation. Root serializes primary and floating reservations together;
a SQLite-only uniqueness check does not exclude privileged launch races.

The reservation foundation uses canonical records in the fixed per-operator `floating-ips` network-authority
directory. Creation retains index, network and global firewall mutation authority. Primary admission checks that
registry; allocation also checks durable stopped-primary reservations, not only current kernel topology. Removal
retains an immutable identity tombstone and advances the generation; a new identity may reuse the freed name or
address. Inventory is paginated to respect the existing privileged frame bound without capping resource count.

The current reservation-only schema grants no guest packet authority. It can therefore retire a prior-boot
reservation under retained index/network locks without recreating the network. Adding ownership must extend
this removal path with actual fencing; the reservation-only shortcut cannot retire an active guest association.
Network removal requires live reservations to be removed first; reset admits only verified retirement history.

The registered `floating-ip-create`, `floating-ip-list`, `floating-ip-get` and `floating-ip-remove` actions use
the existing enrolled-operator dispatch, authenticated caller UID, zero-payload framing and EOF-before-effects
contract. `mvm floating-ip` exposes reservation create/list/inspect/removal. Forced network cleanup retires its
reservations first; ordinary pruning retains networks with floating resources. Automatic VM primary allocation
excludes observed floating reservations, with final root admission still refusing concurrent conflicts.

The CLI allocates, inspects, associates, moves, disassociates and removes resources. Guest delegation permits
inspection and self-association, renewal or release of an exact resource, never allocation, grant creation or
selection of another owner. Eligible VMs must belong to the resource's network and enrolled operator.

### Ownership and fencing

Every ownership change advances a durable generation. Requests must match the observed generation; stale
requests refuse. A successful response describes the committed owner and generation. Requests after an unknown
outcome inspect current state before deciding what to do next.

Transfers first durably record intent, then revoke the old address authority, invalidate the exact old neighbor
and flow state, and only then admit the new incarnation. Failure after revocation may leave the IP unavailable;
it must never leave both owners authorized. Recovery reconciles the recorded transition and exact managed state.
No destructive recovery is authorized by a caller cache record or guest-provided interface name.

The compiler permits the primary address plus only root-authorized active secondary addresses for IPv4 and ARP.
These guards precede established-flow acceptance. Gratuitous ARP refreshes reachability but is not fencing proof.
Removal of a guest alias is best-effort hygiene after fencing, not a prerequisite for safe transfer.

Traffic policies remain directional and explicit. VM-targeted policy follows the VM's admitted addresses;
service access that must follow an IP across owners needs an explicit floating-IP destination in policy input.
The example must declare this access rather than relying on a network-wide allow or an implicit firewall exception.

### Automatic failover

Delegated failover requires expiring ownership; an eligible guest may not evict a healthy current owner just
because it requests association. Renewal is bound to the exact generation and incarnation. Expiration must
fence packets even when the userspace worker is dead or stalled. Kernel-enforced timed address authority is the
proposed mechanism; its interaction with complete firewall-generation replacement needs implementation proof.
Do not ship a userspace-only timer as a guarantee against split brain. Exact lease timings and the failover
controller interface remain to be established with the enforcement implementation.

The enforcement design uses a named timed set of host-veth/address pairs in the managed bridge table. Ordinary
firewall updates now retain the admitted tables and chains and replace their rules in one batch. Initial creation
still creates the tables; verified empty teardown deletes them. The compiler and strict collector now bind
set metadata, owner keys, IPv4/ARP guards and bridge-scoped destination fences. Readback admits expired elements
disappearing, but rejects foreign keys or a timeout that differs from the owner's policy. Ownership changes revoke
observed old keys; compilation never installs or renews an element. Root association and guest admission remain unwired.
Retention is necessary because the pinned nftables library writes relative element timeouts;
recreating an element from its observed remaining time could extend its lease by the delay before commit.

Kernel readiness exercises this model in a disposable namespace, including a retained rule update and exact
readback. The producer's nftables feature includes `CONFIG_NFT_BRIDGE_META` for bridge-scoped destination checks.
The current QA kernel lacks that option; its rebuild and packet qualification remain pending.

Source IPv4 and ARP checks must admit only primary addresses and live secondary pairs. A separate bridge
postrouting fence must reject floating-IP delivery through an unauthorized output, including host-originated
traffic and stale neighbor entries. This fence remains after lease expiry. Unrelated policy updates must preserve
the timed elements; only an admitted association or renewal may start a new timeout. Kernel packet tests must
prove these properties before secondary authority is enabled.

An operator may explicitly transfer ownership. Host reboot expires delegated ownership; no old monotonic lease
is revived. VM stop, deletion or restore fences its active secondary authority. Snapshot restore does not clone
ownership or delegation into another incarnation. Paused VMs cannot sustain delegated leases by guest renewal.

Guest address configuration occurs only after authorization. The HA example must replace independently assigned
keepalived VIPs with authorized ownership and three control-plane nodes. VRRP protocol 112 and unrestricted ARP
are not implicitly enabled by this feature.

## Alternatives

Guest-managed keepalived with no host association cannot enforce an exclusive owner. Merely adding another
address to a user-owned database cannot authorize network effects. A separately configured TCP load balancer is
valid but does not implement the requested general floating-IP feature.

## Verification

Exercise concurrent reservations, primary/floating collisions, stale generations, two simultaneous claimants,
crashes at transfer stages, failed neighbor/flow cleanup, old-owner spoofing and worker death past lease expiry.
Verify allowed and denied service traffic before and after movement, plus stop/restore/reboot cleanup. Run the
three-controller example only in a runner sized for it; codec or single-VM checks do not prove HA behavior.
