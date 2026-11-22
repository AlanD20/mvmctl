# Floating IP Ownership

**Status:** Accepted
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

The root record uses schema 3 with separate publication revision and ownership generation. Its closed phases
record explicit finite or persistent association, renewal, release and recovery. Recovery of an interrupted timed install must
fence before returning to unassociated state; journal replay cannot grant or renew a key. Phase validation is
implemented, together with guest coordinator and timed effects. Authenticated guest dispatch and the retained
worker are integrated. Earlier floating-IP record schemas are rejected.
Sealed firewall intent uses schema 2 with explicit lifetime; missing timeouts never infer persistent ownership.
The terminal-history reader admits only canonical committed-empty history: an empty `Target`, nil `PriorBoot`,
and at most dormant network metadata in `Before`, with no other intent fields. It requires exact boot/namespace
binding and fresh kernel absence proof. This preserves terminal history, not compatibility with old active authority.
Only a stable unassociated record may retire under retained index/network locks after reboot without recreating
the network. Owned or unfinished records require fencing before retirement.
Network removal requires live reservations to be removed first; reset admits only verified retirement history.

The registered `floating-ip-create`, `floating-ip-list`, `floating-ip-get` and `floating-ip-remove` actions use
the existing enrolled-operator dispatch, authenticated caller UID, zero-payload framing and EOF-before-effects
contract. `mvm floating-ip` exposes reservation create/list/inspect/removal. Forced network cleanup retires its
reservations first; ordinary pruning retains networks with floating resources. Automatic VM primary allocation
excludes observed floating reservations, with final root admission still refusing concurrent conflicts.

The CLI allocates, inspects, associates, moves, disassociates and removes resources. Guest delegation permits
inspection and self-association, renewal or release of an exact resource, never allocation, grant creation or
selection of another owner. Eligible VMs must belong to the resource's network and enrolled operator.

An environment grant step declares a list of exact VM selectors and a list of methods. The workflow persists
one exact grant ID per pair before publication. After the first receipt for a VM, subsequent grants compare
the saved boot, launch generation and execution UID under retained root admission before writing permission.
This keeps several methods in one workflow step without creating group authority. A retry uses the original
IDs, and teardown recovers or revokes only those grants.

### Ownership and fencing

The Jailer admission lease retains the owned VM lock and verifies its current boot, launch generation,
isolated execution identity, process locators and protected Firecracker API peer. It exposes a genuine child
topology hold for later network composition; an observed identity alone cannot authorize effects. Admission
requires no guest-agent connection and sends no API operation. This proves a live VMM, not an unpaused guest;
delegated renewal must separately call the implemented running-state check through the protected API. It accepts
only Running, never resumes a paused VM, and discards failed HTTP streams before another observation.
Delegated ownership dispatch uses this admission, exercised by the live floating-IP runtime suite.

Every ownership change advances a durable generation. Requests must match the observed generation; stale
requests refuse. A successful response describes the committed owner and generation. Requests after an unknown
outcome inspect current state before deciding what to do next.

Transfers first durably record intent, then revoke the old address authority, invalidate the exact old neighbor
and flow state, and only then admit the new incarnation. Failure after revocation may leave the IP unavailable;
it must never leave both owners authorized. Recovery reconciles the recorded transition and exact managed state.
No destructive recovery is authorized by a caller cache record or guest-provided interface name.

The firewall mutation lease now derives the cleanup address from admitted resource scope and generation,
requires observed secondary authority to be absent, and deletes only matching IPv4 conntrack entries using
original tuple, zone and flow identity. A second dump must prove absence before return. Interrupted or uncertain
effects refuse handover. This helper is independently reviewed and unit-tested. The private network transaction validates
its durable reservation and exact managed bridge, runs flow cleanup, reinspects the bridge and removes only
the selected dynamic neighbor. This does not require the old VM to be live or its primary veth to be down.
The runtime suite exercises owner movement with live traffic and stale-owner refusal.

The compiler permits the primary address plus only root-authorized active secondary addresses for IPv4 and ARP.
These guards precede established-flow acceptance. Gratuitous ARP refreshes reachability but is not fencing proof.
Removal of a guest alias is best-effort hygiene after fencing, not a prerequisite for safe transfer.

Traffic policies remain directional and explicit. VM-targeted policy follows the VM's admitted addresses;
service access that must follow an IP across owners needs an explicit floating-IP destination in policy input.
The example must declare this access rather than relying on a network-wide allow or an implicit firewall exception.
The compiler now supports VM-to-floating-IP and network-to-floating-IP grants, bound to the immutable resource
ID and exact address. Destination rules follow the active owner without admitting its primary or other aliases;
unassociated resources emit no delivery grant. Operator policy CRUD and root mutation are implemented and
reviewed. `policy traffic create --to-floating-ip` selects this destination family; `--to-network` resolves
the reservation scope. Root derives the address and network from its resource record. SQLite stores local
projections with exact claim/incarnation receipts, while root remains authoritative. Live packet tests cover
both destination families across owner movement.

### Automatic failover

Delegated failover requires expiring ownership; an eligible guest may not evict a healthy current owner just
because it requests association. Renewal is bound to the exact generation and incarnation. Expiration must
fence packets even when the userspace worker is dead or stalled. Kernel-enforced timed address authority is
implemented with retained firewall tables. Ordinary expiry, renewal and deliberate worker-stall packet
fencing tests pass.
Do not ship a userspace-only timer as a guarantee against split brain. Association and renewal grants require
explicit positive whole-second lease durations; there is no default.
A guest controller uses the typed inspect, associate, renew and release methods and current generations.
Operator associations use the persistent lifetime below; delegated leases remain finite.

### Operator-managed ownership

The operator's explicit association persists until release, transfer or VM stop. It does not require a guest
delegation or renewal loop. Origin and lifetime are explicit authority fields: persistent ownership is valid only
for an operator assignment; guest association and renewal always require a finite positive lease. The journal,
sealed firewall intent and kernel readback must preserve this distinction. A missing timeout must never be
interpreted as an expired finite lease. This extension requires a new closed journal schema; old records must
not acquire persistent authority through defaults or inference.

Association admits an unassociated resource. Transfer explicitly fences the old owner before assigning a new
one. The API observes the current root generation before a compare-and-swap; an optional CLI generation may
bind a caller's earlier observation. Compilation, reconnect and journal recovery never activate kernel authority.
Kernel permissions survive ordinary CLI or worker exit, but stop, replacement and reboot cannot revive them.

A valid release delegation may release the same VM's persistent assignment. Guest renewal cannot convert or
extend it, and guest association cannot silently change its lifetime. Revoking a grant fences only ownership
that grant sustained; it cannot cancel an independent operator assignment.

The authenticated worker synchronizes operator-owned aliases with the exact admitted guest incarnation.
Association and transfer report completion only after the matching alias acknowledgement. A bounded wait or
agent failure must report committed root ownership separately from incomplete alias synchronization; it must
not claim rollback. Live operator association, transfer, release and stop-fencing tests pass.

The enforcement design uses a named timed set of host-veth/address pairs in the managed bridge table. Ordinary
firewall updates now retain the admitted tables and chains and replace their rules in one batch. Initial creation
still creates the tables; verified empty teardown deletes them. The compiler and strict collector now bind
set metadata, owner keys, IPv4/ARP guards and bridge-scoped destination fences. Readback admits expired elements
disappearing, but rejects foreign keys or a timeout that differs from the owner's policy. Ownership changes revoke
observed old keys; compilation never installs or renews an element. Typed fencing, metadata publication,
activation and renewal effects are wired to authenticated guest dispatch through the retained worker.
Read-only lease observation verifies the complete graph and exact binding;
a missing binding is an error, while explicit ownerless state carries no timed permission.
Renewal removes the exact old key, proves absence and inserts the configured finite lease once, under retained
ownership serialization. It may briefly interrupt traffic. Repeating an add with the same timeout does not refresh
expiration with the pinned library/kernel protocol. Unknown outcomes require recovery rather than blind replay.
Focused tests and review cover these effects; the runtime suite verifies renewal across the original deadline
and fencing after expiry. Actual host-reboot recovery and deliberate worker-stall packet fencing also pass.
Retention is necessary because the pinned nftables library writes relative element timeouts;
recreating an element from its observed remaining time could extend its lease by the delay before commit.

Kernel readiness exercises this model in a disposable namespace, including a retained rule update and exact
readback. The producer's nftables feature includes `CONFIG_NFT_BRIDGE_META` for bridge-scoped destination checks
and `CONFIG_NF_CT_NETLINK` for exact flow invalidation. The replacement kernel passes readiness and all 14
floating-IP runtime cases, alongside all 32 environment cases, on QA source `0403ad4e` with the paired
production candidate from `4f490ff0`. Teardown and inventory cleanup pass without intervention. The later paired
host/agent candidate passes both phases across an actual host reboot, including delegation retirement and
explicit ownership recovery. Deliberate worker-stall qualification passes with unchanged lease deadlines and
traffic assertions, strict reports, exact continuation and preserved inventory. The prescribed Go CI and root
Jailer/agent alias checks pass on the final implementation source. Release-branch integration and release
sign-off remain separate.

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
floating-IP feature checks on small VMs in the existing runner. Full Kubernetes cluster bootstrap, quorum
testing and larger-VM qualification are outside the current goal; these checks do not prove HA behavior.
