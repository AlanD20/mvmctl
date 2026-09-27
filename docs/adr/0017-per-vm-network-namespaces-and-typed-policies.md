# Per-VM Network Namespaces and Typed Connectivity Policies

**Status:** Accepted — v0.3.0 implementation in progress
**Date:** 2026-08-23
**Last Updated:** 2026-09-27 (isolated TAP execution identity)
**Supersedes:** [ADR-0014: Routed Service-Access Policies](0014-routed-service-access-policies.md)
**See also:** [ADR-0016: Root-Owned Single-Binary Privileged Dispatch](0016-root-owned-single-binary-privileged-dispatch.md)
**No-IP placement:** [ADR-0020: Networkless VM Placement](0020-networkless-vm-placement.md) specifies the separate
loopback-only namespace variant required for the no-IP exec case; it is not implemented or qualified yet.

## Context

The initial managed-network implementation creates TAP devices and Linux bridges in the host network namespace. Its
service-access policy protects routed traffic between different managed bridges, but traffic between VMs on the same
bridge remains unfiltered. Firecracker therefore shares the host network view, interface names are host-global, and
same-network placement implicitly grants connectivity.

Canonical Jailer launch improves filesystem, process, privilege, and cgroup isolation, but Jailer does not create or
configure networking. Its `--netns` option only joins a network namespace that the trusted caller has already created.
The namespace handle and every device placed inside it are consequently privileged lifecycle resources.

The existing VM-to-VM remote-exec model is also too broad. A boolean on each VM creates an all-to-all trust mesh among
enabled VMs, bypasses IP firewall policies through vsock, and can execute as root when no target user is supplied.

v0.3.0 is a clean installation with no legacy database, runtime-state, policy, or firewall compatibility requirement.
This is the appropriate point to replace the host-global and dual-backend design rather than preserve it as a fallback.

## Decision

### One network namespace per VM

Every networked VM receives one root-created network namespace. Jailer must join that namespace through a root-owned
namespace handle before it drops privileges and executes Firecracker. Direct launch in the host network namespace is
not supported.

The namespace topology is fixed by mvmctl:

```text
host namespace
└── managed network bridge
    └── host veth
        └── VM network namespace
            ├── namespace veth
            ├── local bridge
            └── TAP opened by jailed Firecracker
```

The host bridge remains the logical managed network. Each VM namespace contains only its loopback device, its side of
the veth pair, a local bridge, and the Firecracker TAP. Interface names, namespace paths, and topology are derived from
the invoking UID and immutable resource IDs; callers cannot provide raw interface names, namespace paths, `ip` argv, or
netns commands.

#### Canonical L2 and L3 contract

The per-VM topology is an L2 extension of the managed bridge, not a routed hop. A managed network accepts only a
canonical masked IPv4 prefix wholly contained in RFC1918 space and no longer than `/30`. Its resolved gateway is a
usable address in that prefix; when the public input omits it, the fixed default is the first usable address. A guest
address is also usable, differs from the gateway, and is neither the network nor broadcast address. The resolved
prefix, gateway, and guest address are immutable typed values before privileged composition.

Only the host managed bridge and guest `eth0` carry data-plane addresses. The bridge owns `gateway/prefix` and the
kernel-created main-table connected route for the exact prefix, with no gateway and no explicit route metric. The host
veth, namespace veth, namespace-local bridge, and TAP are unnumbered bridge members and own no route. Namespace
loopback is `127.0.0.1/8` with its kernel-created local and connected routes. Guest `eth0` owns `guest/prefix`, its
connected route, and `0.0.0.0/0` through the resolved gateway; neither guest route has an explicit metric. The trusted
launch graph derives the Firecracker kernel `ip=` value and, when enabled, cloud-init network configuration from those
typed values. There is no per-VM host route or namespace route and no IPv6 authority in the v0.3.0 contract.

Default guest MACs use the locally administered unicast prefix `02:fc` followed by the first four bytes of
`SHA-256("mvmctl:guest-mac:v1\0" || decimal-owner-uid || "\0" || canonical-vm-id)`. The version tag and NUL framing
are part of the algorithm. An explicitly requested EUI-48 MAC remains permitted only when it is canonical, unicast,
and globally unique. The selected MAC is persisted; a collision fails closed and is never resolved by hidden salting.
Existing fabric-link MACs are incidental kernel state rather than caller input or durable identity.

The existing `mvb`, `mvh`, `mvn`, `mvl`, and `mvt` names remain deterministically derived from the authenticated owner,
immutable network or VM ID, and fixed role. Root re-derives and compares them at every admission. No protocol, public
input, persistent desired-state row, or recovery request may provide a raw interface name, namespace path, route,
descriptor, netlink request, or `ip` argument.

#### Root-selected NAT uplink

The v0.3.0 public network contract retains only the NAT enabled/disabled choice. The unprivileged API, configuration,
user model, and privileged request expose no `NATGateways` list, interface name, ifindex, route, or uplink selector.
When NAT is disabled, no uplink authority is required and the firewall compiler emits neither uplink egress nor NAT.

When NAT is enabled, root uses a typed rtnetlink authority pinned to the exact host network namespace to discover one
eligible main-table IPv4 default route. Eligibility requires one unicast `0.0.0.0/0` route with exactly one output
interface, no multipath next hops, and a resolved link that is neither loopback nor mvm-owned. Root does not rank
several candidates by metric: zero or multiple eligible routes fail closed because multi-uplink selection is not
supported in v0.3.0.

The admitted managed-network authority persists the selected uplink's exact ifindex, name, link kind, and host
network-namespace device/inode. Root reopens and revalidates both route and link immediately before compilation,
application, and topology activation. A missing route, changed interface or namespace identity, replacement, or
additional eligible uplink blocks activation and preserves cleanup authority. Only this retained typed identity may
enter the private compiler; a caller-provided raw link name never does. Removal of the current `NATGateways`
input/model surface and raw compiler uplink field, plus focused and live tests, remains pending.

#### Retained activation authority

Managed-network registration is a network-creation transaction under the ownership-index and per-network locks. VM
launch does not re-enter registration after acquiring the VM lock. Instead, the real Jailer VM lease creates its
opaque `authorityfs.TopologyHold`; topology preparation validates its owner/VM binding and atomically consumes it into
one retained topology lease. After that consumption, every error returns the non-nil prepared or cleaning lease with
the error. The hold and namespace descriptor remain owned through the launch or cleanup transaction,
so the parent Jailer VM lock cannot release while that transaction has live or retryable topology
authority. After both the instance and topology are durably active, a named close-only
`DetachActive` transition transfers lifetime responsibility to the root-owned records and the
root-owned nsfs mount. It releases operation-local descriptors and locks without lowering links,
removing rules, unmounting the namespace, or performing cleanup. The synchronous dispatcher can
then exit; v0.3.0 does not introduce a per-VM root supervisor.

Later operations reacquire VM, network, and firewall locks in order and reopen the exact
persisted namespace target, mount, links, endpoint, and process identities. Both exposed nsfs
identity and hidden placeholder/directory identity must survive in the root topology record.
Detach is unavailable until this identity-checked recovery path exists. Failure before durable
activation retains rollback authority instead. This clarifies the daemonless lifetime model:
an in-process flock cannot be retained for the entire VM lifetime while permitting later
status, exec, and stop operations to acquire the same VM lock.

All links are created and attached down. Firewall application then returns an opaque, non-zero
`VerifiedVMEndpointLease` only after one complete generation is applied and freshly inspected in the pinned host
network namespace. The lease has no public constructor or fields and is privately bound to boot and host-namespace
identity, authenticated owner, network and VM IDs, canonical prefix and gateway, guest address and MAC, managed bridge,
host veth, and the verified endpoint identity. It retains endpoint cleanup authority without retaining the host-global
firewall lock for the VM lifetime.

Topology activation consumes that exact firewall lease, compares its private binding with retained network/topology
authority, and raises links in this order: namespace loopback, namespace-local bridge, TAP, namespace veth, then host
veth. The host veth is the exposure gate and is always last. Each step rechecks exact namespace, ifindex, kind, alias,
master, address, route, and `IFF_UP` through a fresh strict `NETLINK_ROUTE` connection opened against an explicitly
retained namespace descriptor. Carrier and `IFF_RUNNING` are not activation requirements. Only after all link state is
freshly verified may the pinned namespace handle pass to Jailer. Production route/link activation, the firewall-ready
capability, its Jailer composition, and system QA remain pending.

#### Durable topology and reconciliation

The production managed-network record persists the authenticated owner, network ID, boot and host-namespace identity,
canonical prefix and gateway, exact observed managed-bridge evidence, NAT enabled/disabled, and the exact selected
uplink evidence only when NAT is enabled. One root-owned per-VM topology record beneath
`/var/lib/mvmctl/networks/<uid>/topologies/<vm-id>.json` persists schema and lifecycle, owner/network/VM IDs, boot and
launch generation, selected guest address and MAC, namespace mount evidence, exact observed link names/ifindices and
namespace identities, network-record binding, stable firewall endpoint identity, and completed mutation markers.

Names and namespace paths, loopback state, bridge and guest routes, link ordering, and expected unnumbered-link state
remain derived. Observed names are persisted only as replacement evidence and must equal their derivation. A firewall
generation digest may be audit evidence but is not retained endpoint or kernel ownership authority because later
policy reconciliation legitimately replaces generations. User SQLite state remains desired configuration only.

Lifecycle is `preparing_down -> activating -> active -> cleaning -> cleaned`. Root durably records `preparing_down`
before the first kernel effect and advances exact progress after inspecting every success or error. Desired state after
an error records committed progress and returns the non-nil target lease with that error; prior state remains retryable;
replacement, extra L3 state on an mvm-owned fabric link, or ambiguous state enters `cleaning` and never raises the host
veth or releases the blocked child.

Activation rollback lowers and reverifies the host veth first, then lowers the other links in reverse order. Cleanup
durably enters `cleaning`, removes exposure first, reacquires the network then firewall lock, removes and freshly
verifies the endpoint generation, deletes exact links and the namespace in reverse dependency order, and releases the
firewall endpoint lease, namespace pin, and `TopologyHold` only after effects are proven absent. Completed stages are
skipped on retry. An effect or durability failure retains retryable cleaning authority; a terminal close failure after
proven removal is cached and never retries a stale descriptor.

Same-boot recovery reopens only the derived namespace target and admits the recorded inode and exact links/L3 state.
Prior-boot recovery requires recorded processes and the ephemeral namespace target to be absent before retiring the
record and performs no speculative host mutation. An active topology whose exact firewall endpoint is no longer
verified immediately lowers the host veth and enters cleaning.

#### Snapshot restore endpoint identity

A snapshot's associated network ID and guest-visible IPv4 address and MAC are captured state. Restore must obtain them
from admitted root-owned snapshot metadata and reserve those exact values for the new VM endpoint before any launch
effect. It fails closed when the address or MAC is already reserved, when the source VM still owns that endpoint, or
when the requested destination network differs. v0.3.0 performs no fresh address or MAC allocation during restore and
supports neither cross-network restore nor simultaneous endpoint cloning.

The restored VM receives newly derived host topology identities, so its TAP name may change. Its guest address and MAC
must not: the freshly compiled anti-spoof endpoint must exactly match the state embedded in the guest snapshot. User
SQLite rows and caller-supplied network values are not restore authority. The strict root snapshot metadata record,
reservation/launch composition, conflict and ownership tests, and live snapshot qualification remain pending.

This is a clean-break contract. `/31` and `/32` networks, prefixes not wholly private, network/broadcast/gateway address
conflicts, non-canonical or multicast MACs, schema-1 identity-only records, mutable network-name-derived bridges,
host-global legacy TAPs, and unowned routes or firewall state are not adopted, migrated, repaired, or silently deleted.
Initialization or launch reports clean-install/recovery-required state before mutation. Operators must remove
incompatible state through the documented administrator procedure and recreate it under v0.3.0.

Namespace handles and locks are ephemeral root-owned state under `/run/mvmctl/authority/<uid>/`. VM-created sockets and
display-only PID mirrors use the separate non-authoritative `/run/mvmctl/runtime/<uid>/<vm-id>/` tree defined by
ADR-0016. Persistent minimal VM and network ownership records remain under `/var/lib/mvmctl/`. The user database stores
desired logical configuration but never authorizes a root namespace, device, bridge, route, or firewall mutation.

The privileged launch transaction must:

1. authenticate the caller and consume the prepared release through `preparedRelease.registerLaunching`, durably
   recording `launching` while retaining the release and VM locks;
2. consume the VM lock's `TopologyHold` into the topology lease, acquire the later network mutation lock, and verify
   the root-owned VM and network records;
3. create the namespace, namespace-local devices, host veth, and exact managed-bridge attachment with every link down;
4. acquire and retain the firewall generation lock through activation, apply the complete nftables generation and
   anti-spoofing rules in one atomic transaction, freshly inspect it, and mint the endpoint lease;
5. only after firewall verification, bring the exact links up and reverify their identity and state;
6. only after those stages and relay readiness, release the blocked child to Jailer with the pinned namespace handle;
   and
7. persist exact namespace and final process identity through `launchLease.activateVerifiedProcess` before reporting
   the VM active.

The complete lock order is `release -> global index -> VM -> network -> firewall`. An operation may acquire a suffix or
subset but must never acquire an earlier class while holding a later one. The schema-5 launch lease is the durable
two-phase ownership claim and retains the VM lock, topology lease, and endpoint cleanup authority across application,
activation, rollback, and cleanup without unlocking and reacquiring the VM. The per-network and host-global firewall
mutation locks remain held through their application/activation checkpoint, then release; active lifetime authority
does not serialize unrelated VMs. Cleanup reacquires the network then firewall suffix under the still-retained VM lock.
Network behavior remains in the network service and nftables behavior in the firewall service rather than the Jailer
authority module. The firewall mutation lock is host-global because one generation owns both reserved tables across all
users and managed networks.

Failure before link activation leaves links down and the Jailer child blocked. Failure after links rise but before the
activation checkpoint releases its mutation locks first lowers and reverifies the exact links, then invokes the launch
lease's typed abort while those same locks and lifecycle handle remain held. An unknown firewall outcome never raises a
link or releases the child. A later active stop/failure enters cleanup under the retained VM lock and reacquires network
then firewall. Cleanup verifies namespace inode, interface ownership, process identity, and cgroup membership before
removing anything.

Implementation note (2026-08-31): a private strict schema-1 managed-network authority record, codec, persistent store,
and inert registration authority are implemented. The record is at
`/var/lib/mvmctl/networks/<uid>/<network-id>.json` and contains exactly schema version, owner UID, 64-lowercase-hex
network ID, host boot ID, and the derived managed bridge's name, ifindex, and host namespace device/inode. The strict
4-KiB codec rejects missing, unknown, duplicate, case-colliding, nested, trailing, and invalid values. Root-owned
managed directories are exact mode `0700`; one-link records are exact mode `0600`. Descriptor-relative no-symlink
admission, stable before/after reads, exclusive temporary creation, file sync, checked close, no-replace rename, and
parent sync define publication. A post-rename parent-sync failure returns the created record with
`record_created=true` and `durability_uncertain=true`.

Registration takes the global network ownership-index lock before enumerating all owner directories, then takes the
owner-bound per-network lock. It denies a foreign or duplicate claim, rejects a prior-boot record as stale, returns an
exact same-boot bridge record unchanged, and rejects a different bridge identity. Registration releases per-network
lock, lock UID, store UID, store root, index lock, then lock root in exact reverse order under cancellation-independent
cleanup. Capability releases are pointer-shared, close once, and repeat the first cached cleanup result; cleanup failure
preserves the primary `DomainError` and adds bounded `cleanup_required` evidence.

Implementation update (2026-09-27): production network authority uses a later strict schema. Creating a managed
network may rebootstrap exact prior-boot logical intent with a new bridge identity; removal has a separate
cleanup-only path for an exact old-boot live record. It takes the same ownership-index and network locks, proves the
current derived bridge absent and any matching topology journal cleaned, retires the old firewall generation under
current-boot host-namespace authority, rejects remaining endpoint or reservation intent, repeats absence checks,
and publishes the unchanged record as a removal receipt. The old bridge ifindex and namespace inode are historical
evidence only. A named-runner post-reboot cleanup of two test networks passed; comprehensive reboot/failure QA remains
required. This update does not change the older inert-registration foundation described above.

Registration remains deliberately inert: no production constructor calls it, no real Jailer VM-lock lease is
transferred into it or the topology transaction, and no namespace or link effect consumes the managed-network record.
The separate topology-preparation foundation exposes topology authority only from its exact committed in-memory state
while the network lock, namespace pin, and all creation markers remain present. Building, cleanup-in-progress, cleaned,
missing-lease, and partially rolled-back values return no authority.

A separate implemented Network authority capability creates the exact root-owned
`/run/mvmctl/authority/<uid>/netns/<vm-id>` placeholder and retains its directory, leaf, and mount identities. The
private namespace effect creates a network namespace on a disposable locked OS thread, publishes the exact nsfs mount
through `open_tree`/`move_mount`, verifies restoration of the pinned host namespace, and performs identity-bound
non-lazy unmount and placeholder removal with explicit unknown-outcome evidence.

A separate private link adapter now completes the `TopologyEffects` boundary. It creates and inspects the exact
namespace-local bridge, host/namespace veth pair, and TAP down; attaches only identity-checked endpoints to the verified
local and managed bridges; and identity-bound deletes the exact veth pair, TAP, and childless local bridge. Every
rtnetlink inspection and mutation opens a fresh strict connection against an explicit retained network-namespace
descriptor, applies any context deadline, closes on cancellation, and checks mutation acknowledgements.

TAP creation does not synthesize an rtnetlink `tun` create request. On a disposable locked thread it rechecks and enters
the retained VM namespace, opens only fixed `/dev/net/tun` as character device `10:200`, calls `TUNSETIFF` with exact
TAP/no-PI/exclusive flags, and calls `TUNSETOWNER` for the root-selected isolated Firecracker execution UID without
setting a group. This UID is distinct from the authenticated operator UID under
[ADR-0018](0018-isolated-vm-execution-and-snapshot-authority.md), and the root-owned topology journal retains it for
exact activation and cleanup checks. The effect rechecks the temporary TAP, sets its managed alias, and reverifies
alias, type, no-PI, owner, absent group, down state, ifindex, and non-persistent state before final `TUNSETPERSIST`;
the outer link effect then verifies the persistent TAP.

The root Jailer lifecycle composes this private effect with its retained VM hold, durable topology journal, firewall
application, ordered link activation, and exact cleanup/recovery. This source wiring does not itself prove networked
runtime qualification; the installed-candidate system suite remains a release gate.

### nftables is the only firewall backend

v0.3.0 requires nftables. The final implementation removes the iptables backend, configuration option, repositories,
renderers, fallback behavior, and tests.

The final root-owned adapter in `internal/service/firewall/` must compile and mutate nftables rules through the reviewed,
pinned `github.com/google/nftables`. Direct `github.com/mdlayher/netlink` use is permitted within that private adapter
for the strict read-only raw decoder, collector, and namespace-bound transport because the pinned structured decoder
can omit unknown expression grammar. It also supports buffer configuration, deadlines, cancellation, and cleanup of the
socket retained by `google/nftables`; rule mutation still uses only `google/nftables`. These uses do not introduce a
second firewall backend or mutation adapter. Final production must not execute or parse the `nft` CLI, link `libnftnl`,
depend on `nftables.service`, or fall back to a CLI implementation.
Hosts still require the applicable kernel nftables, bridge-family, connection-tracking, and NAT capabilities, and the
privileged receiver still requires the authority needed to program them.

Only mvmctl-owned typed intent may cross the final privileged protocol. Types from `github.com/google/nftables` and
`github.com/mdlayher/netlink` must remain private to the root-side firewall adapter. One reconciliation must construct a
complete generation and submit it in one `Conn.Flush` transaction. The adapter must take `context.Context` first, bind
cancellation and deadlines to the netlink operation, and map failures to `pkg/errs.DomainError`; a cancelled operation
cannot be reported as successful.

Final host readiness must use direct kernel capability probes rather than executable lookup, `nft --version`, or
parsing CLI diagnostics. The `nft` CLI may remain installed in disposable QA runners as an independent, read-only
observation oracle. It is not a production dependency or a sudo target.

The final nftables implementation must own complete atomic generations for:

- bridge-family same-network filtering and source MAC/IP anti-spoofing;
- IP-family routed filtering between managed networks;
- VM-to-host input restrictions;
- established reply handling and required mvmctl host services; and
- managed internet egress/NAT.

Managed VM-to-VM traffic must be default-deny whether the VMs share a network or use different networks. Required ARP
and system traffic are derived internally. Links must not become usable before the applicable deny and allow generation
is installed successfully.

Implementation note (2026-08-30): `go.mod` now pins `github.com/google/nftables v0.3.0`, and the private
`internal/service/firewall/` compiler builds fixed bridge/inet tables and chains from closed resolved intent. Its bridge
prerouting chain has raw priority and holds source MAC/IPv4/ARP anti-spoofing plus unsupported-L3 guards; the bridge
forward chain holds same-network policy. Inet forward also carries an exact companion for each same-network grant
ahead of managed-bridge default drops: when host `br_netfilter` sends a bridged IPv4 packet through IP forward,
both logical interfaces are the managed host bridge. The companion does not replace the bridge-family endpoint
and ingress anti-spoof gates, which remain effective when `br_netfilter` is disabled. The host-wide bridge-netfilter
setting is not changed: doing so would affect unrelated bridges, and the
[kernel bridge-netfilter check](https://kernel.googlesource.com/pub/scm/linux/kernel/git/stable/linux-stable.git/+/refs/tags/v6.1.125/net/bridge/br_netfilter_hooks.c)
allows an enabled global bit to override a per-bridge disabled bit. Inet forward/input/postrouting cover routed
policy, established replies, managed host services,
default deny, conditional uplink egress, and NAT. Masquerade-disabled networks receive no uplink
egress allow. The compiler rejects overlapping prefixes and unusable IPv4 authorities and orders output
deterministically. The caller no longer supplies generation identity. Explicit empty intent compiles to an empty
generation with no reserved tables or rules.

Implementation note (2026-09-13): ARP ingress now rejects a non-Ethernet/IPv4 header or an opcode outside request/reply
before the fixed sender-address checks. The ARP allow rule separately requires that supported header, opcode, and the
complete 28-byte payload. The private application validator also verifies the new ingress guards. L1 covers malformed
headers/opcodes, truncated payloads, spoofed senders, and valid request/reply packets; live kernel packet qualification
remains pending.

The compiler derives SHA-256 from a versioned canonical closed projection of normalized intent and the exact managed
tables, chains, rules, expressions, order, and userdata schema. The userdata generation slot uses a fixed placeholder
while hashing to break self-reference; the derived digest and rule identity are then bound into every emitted rule.
The projection covers every field of exactly the eight expression structs emitted from
`github.com/google/nftables v0.3.0`: `expr.Meta`, `expr.Masq`, `expr.Bitwise`, `expr.Cmp`, `expr.Ct`, `expr.Payload`,
`expr.Range`, and `expr.Verdict`. Typed-nil and unknown expressions fail closed. Digest encoding streams through a fixed
64-KiB buffer directly into SHA-256, and validation, encoding, and finalization failures surface as compiler errors.
Any nftables module upgrade or emitted-expression change requires an explicit field-coverage audit and digest-schema
version bump.

Before the same-network inet companion, the representative 1,000-endpoint/1,000-grant fixture emitted 11,007 rules
and used 243,345 allocations normally and 243,346 under race instrumentation. With 1,000 additional exact inet
rules, the named Go 1.26.3 runner measured 279,352 allocations and 12,950,960 bytes, compared with 11,683,904
bytes before the change. The L1 fixed ceiling is now 290,000 allocations; its 6x growth guard remains. A live
disposable-runner nft trace showed a denied same-network SYN enter `mvmctl_inet` forward with both logical interfaces
equal to the managed bridge and hit its default drop. The installed candidate then passed focused same-network and
cross-network TCP allow/deny/revoke cases with checked teardown. Other policy variants and full system qualification
remain open.

The compiler remains effect-free. The separate private generation authority, evidence-bound record replacement, raw
snapshot collector, retained exact host-netns transport, and application/reconciliation coordinator described below
are implemented, including typed `nftables.Conn`/`Flush` application and empty-generation deletion. Qualified initial
inspection, production topology composition, privileged/public policy cutover, legacy `nft` CLI removal, and L2
qualification remain pending. Private implementation and L1 evidence do not establish live kernel enforcement,
atomicity qualification, or release readiness.

#### Fixed NoCloud host-service slot

The user selected a fixed TCP port bound to each managed network's root-derived gateway for NoCloud.
The implementation uses the existing default port, `8000`, and rejects custom ports. The privileged
request selects only the owned network identity; it cannot select a host address, port, protocol,
or arbitrary service. The resulting grant permits only this reserved gateway service slot.

One live NoCloud batch occupies that gateway listener. A bind collision fails before granting access;
it does not authorize adoption or termination of the existing listener. Normal shutdown revokes the
grant while retaining the listener's socket, then releases the port. An interrupted process may leave
the durable reserved-slot grant for a subsequent invocation or network teardown. This is a fixed-slot
policy, not authenticated proof of the current listener process; that distinction must remain visible
in operational documentation. Live-kernel qualification remains deferred.

#### Firewall generation authority and inspection gates

The implemented private authority foundation provides the never-unlinked root-owned
`/run/mvmctl/authority/firewall.lock` and strict persistent record at
`/var/lib/mvmctl/firewall/generation.json`. The enclosing root-owned directories are mode `0700`; the record and lock
are mode `0600`. The record binds schema and lifecycle, host boot ID, host network-namespace device/inode, an empty flag,
the last committed generation, and the exact observed `before` and compiled `target` identities while applying.

Its bounded exact codec rejects missing, unknown, duplicate, case-colliding, trailing, and invalid committed/applying
state. The descriptor-relative store's first-publication path creates the managed tree and initial record exclusively,
never repairs or overwrites existing state, and preserves post-rename creation/durability evidence. A private
acquisition seam requires
the earlier network authority before opening the global firewall capability, then admits only a same-boot,
same-host-network-namespace record and retains store, lock, and authority through pointer-shared cached close-once
cleanup. It deliberately rejects a missing record because qualified kernel inspection must establish initial truth.
No production consumer invokes this seam.

An admitted generation lease can now atomically replace its record. The store writes and fsyncs a private exact-mode
temporary record, then immediately re-admits both the expected record value and its retained device/inode/metadata
evidence before the replacing rename. Post-rename directory fsync and exact readback return the new file evidence.
Errors preserve `record_replaced=true` and, when applicable, `durability_uncertain=true`; a committed replacement with
no new evidence makes the lease mutation-ambiguous and blocks another replacement rather than acting through stale
authority. This is durable-record mutation only, not nftables mutation or recovery orchestration.

The private raw-netlink snapshot collector is also implemented but deliberately inert. It brackets one logical
observation with ruleset-generation reads; chooses exact read-only lookups for the two managed tables and five fixed
chains; dumps only their rules, sets, objects, and flowtables; and rejects any set, object, flowtable, partial table
state, generation drift, or inconsistent base sequence. Each request owns a fresh checked transport, while a shared
32-MiB/20,000-message budget bounds the complete observation. The strict raw decoder rejects malformed/unknown
attributes and expressions rather than relying on `github.com/google/nftables` structured decoding.

A retained namespace authority now requires the earlier network identity and admitted generation record to agree before
it opens `/proc/self/ns/net`. It admits that descriptor only as the same exact network namespace, duplicates and retains
it close-on-exec, and rechecks it before every fresh strict `NETLINK_NETFILTER` connection returned to the snapshot
collector. The retained lease and factory use checked close-once cleanup. No production application or recovery path
obtains the factory or invokes the collector, and no real-kernel result is qualified.

The implemented generation identity is the compiler-derived canonical digest described above; callers provide no
identifier. The digest alone is neither kernel ownership nor deletion authority. An applied empty generation must mean
both `mvmctl_bridge` and `mvmctl_inet` are absent: a pristine empty target performs no netlink transaction, while
removal of an existing generation requires exact committed authority and deletes both reserved tables in the same
qualified batch. The compiler's empty output does not implement either case. Missing, corrupt, wrong-boot,
wrong-namespace, mismatched, partial, or foreign fixed-name state fails closed and is never adopted or deleted
automatically.

`github.com/google/nftables v0.3.0` cannot by itself prove that a fixed-name table or chain belongs to mvmctl: table and
chain values expose no userdata or equivalent provenance. Rule userdata is necessary evidence but does not establish
ownership of its containing table. Its structured `GetRules` path can also omit expression types the library does not
understand, so re-marshalling parsed expressions cannot prove that no extra kernel expression exists. The inert raw
collector and retained host-netns factory close the decoder and namespace-selection seams, but production activation
requires root-record-bound exact-state authority, application composition, and live-kernel evidence.
On 2026-09-19 the user accepted other root or `CAP_NET_ADMIN` programs as trusted administrators.
mvmctl fails closed on detected foreign, replaced, or changed state; it does not promise isolation
against an administrator deliberately changing kernel rules concurrently. Rule userdata and exact
record-bound inspection remain necessary within this model. Ambiguity always blocks `DelTable`.

Application writes a durable `applying` record before kernel mutation, queues the complete delete/create/chain/rule
replacement on one fresh explicitly namespace-bound lasting connection, and calls `Flush` once. It then closes that
connection and verifies the exact target through a fresh inspection before committing the record. Cancellation closes
the lasting connection; a failed or cancelled flush is never retried on that connection. While retaining the same
global lock and namespace pin, one bounded fresh recovery inspection may classify the kernel as exact `before`, exact
`target`, or unknown. Unknown state retains recoverable `applying` authority and keeps links down.

One `Flush` protocol batch is not yet proof that delete-and-recreate behaves atomically on every supported kernel. A
disposable isolated-network-namespace compatibility gate must prove that a valid replacement changes both reserved
tables together, a late invalid command leaves the exact prior generation, an empty target removes both or neither,
explicit namespace selection affects only that namespace, and every emitted expression survives exact inspection.
The user authorized writing the remaining adapter, privileged action, link-activation, public-cutover,
and legacy-removal implementation on 2026-09-19 without running validation or writing/running tests.
That implementation-only exception changes the work order, not the runtime ownership checks or
the release gate: live activation is not qualified until the isolated-kernel evidence above exists.

### Two public policy families

The public policy namespace contains two typed policy families:

```text
mvm policy traffic ...
mvm policy exec ...
```

There is no generic rule, action, selector registry, firewall-expression, or privileged-operation policy.

#### Traffic policies

A traffic policy grants TCP or UDP access to an exact destination VM port or port range. It has one of two closed,
explicit source forms:

- source managed network to destination VM; or
- exact source VM to exact destination VM.

The CLI may present these forms under the common `traffic` namespace, but API and network-domain interfaces use
separate typed inputs and methods. Persisted intent stores immutable resource identities, protocol, and port bounds—not
IP addresses, MAC addresses, interface names, nftables expressions, or commands. The compiler resolves current
root-verified identities and emits the required bridge- or IP-family rules.

A traffic policy applies to the IP data plane only. It grants neither vsock execution nor host command execution.

#### Exec policies

An exec policy grants one exact source VM permission to request command execution on one exact destination VM through
the host vsock relay. It is directional, independent of network membership, and required for both same-network and
cross-network VMs.

Exec policies require a non-root destination user. v0.3.0 does not provide VM-to-VM root execution or VM-to-host
execution. The relay authenticates the source from the active host-side vsock session and root-owned instance identity,
then enforces the exact source, destination, owner, and target user before contacting the destination agent.

The relay bounds request size, frame size, output bytes, duration, and concurrent executions. Audit records contain the
source, destination, target user, timing, and result but not the complete command. An exec policy grants no TCP/UDP
connectivity.

The clean cutover removes the legacy `allow_remote_exec` VM field, CLI flags, configuration default, environment-spec
property, all-to-all checks, and active documentation without conversion.

Implementation note (2026-09-01): the first exec cutover slice severs the legacy relay before the replacement is
activated. Every legacy `remote_vm` request receives one terminal unauthorized response before source lookup, request
parsing, destination lookup, vsock dial, or command logging. Ordinary host execution and copy paths remain unchanged.
The second slice exposes directional intent management as
`mvm policy exec create <source-vm> <destination-vm> <target-user>`, `ls [--json]`, and
`rm <policy...> [--force]`. The API resolves exact source and destination VM identities, rejects the same VM and root
target, derives a deterministic identity from source, destination, and user, and neither resolves nor constrains
network placement. This is desired-state CRUD only.

The third slice adds the protocol-only `internal/lib/vmexecwire` foundation. Its frames use fixed `MVMEXEC1` magic,
version 1, a big-endian length, and a 64-KiB body limit. Strict closed schemas separate the guest-source request from
the exact host-built destination request; only the destination request may carry the canonical lowercase UUIDv4 agent
token. A 32-lowercase-hex identity binds every request and stream frame. The wire requires a non-root target user,
bounds the command at 32 KiB, combined stdout and stderr at 8 MiB, and reported execution duration at five minutes, and
accepts only `stdout`/`stderr` output followed by exactly one `result` or `error` terminal and transport EOF. `cancel`
is the sole request-bound control frame.

The fourth slice adds the destination side without activating the source path. The existing guest-agent vsock listener
routes only an exact `MVMEXEC1` prefix to the closed destination decoder and preserves every legacy host-exec/copy
prefix. Before an execution effect, it requires a configured canonical agent token and compares it in constant time.
It resolves the exact account name to a nonzero numeric UID, numeric primary and supplementary groups, and a clean
absolute home; then it invokes `/bin/sh -c` with the explicit credential, that home as the working directory, and a
fixed environment. It creates a process group and kills that group on context timeout or matching cancellation.

Accepted vsock streams are close-on-exec and nonblocking so their `net.Conn` deadlines are effective. After exact magic
routing, destination-request decoding has a five-minute deadline; authenticated execution gets its own fixed
five-minute context, whose expiry interrupts blocked I/O, and the terminal write receives a separate two-second bound.
Output is streamed through the wire's combined limit, invalid cancellation or writer failure cannot become a success
result, and terminal/log messages omit the command, token, and process cause. This is process-group cancellation, not
per-command cgroup authority; a descendant that deliberately leaves the group is outside this slice's guarantee.

The destination side remains dormant for VM-to-VM use. At that slice, no source session or API-owned relay listener
constructed its request, and root runtime admission, Task 15 active-session/capacity authority, runtime policy
resolution/enforcement, and positive Python system qualification were still pending.

The fifth slice implements the closed source-session and guest process boundary without activating the host relay. A
host `SourceSession` owns one caller-opened source connection, authenticates the source guest through exact
`MVMSRC01`/version-1 framing and its canonical agent token, and carries one random 32-lowercase-hex session ID plus one
bounded source request. Bootstrap and ready frames are limited to 512 bytes. The ordinary exec request carries only the
session ID through a named `ExecWithSourceSession` path; normal exec/copy behavior is unchanged and creates no second
connection. The session ID is correlation, not authority, and the guest command receives no token through its
environment, arguments, filesystem, or local request. The later root-retained Firecracker-peer admission remains
mandatory before any destination effect.

The guest source broker is mode `0666` by design so a root or non-root outer command can reach it, but filesystem access
does not authorize a request. The agent binds the pending session to the exact outer command's atomically acquired
pidfd, credentials, and stable PID/start identity. A local AF_UNIX peer must provide matching `SO_PEERCRED` and
`SO_PEERPIDFD` evidence and twice prove a stable ancestry chain to that live command before its single claim. Missing
pidfd support, exited/zombie state, identity drift, multiple matches, or an unavailable observation fails closed.
Admission is capped at eight attempts, the initial local request at two seconds, and ancestry at 64 processes.

After admission, the broker hands the dedicated source transport from disconnect monitoring to the one local request.
The same `MVMEXEC1` command, output, duration, terminal, cancellation, and EOF limits apply. A terminal closes the
physical source transport; context cancellation closes blocked reads and writes; the first relay disconnect closes
both ends; agent shutdown releases every session and pin. The outer command runs in a new session/process group and
that group is killed on cancellation with a two-second wait bound. This is not per-command cgroup authority and does
not claim control of a descendant that deliberately escapes the process group.

The host source session now exposes `WaitCancel` only after `ReadRequest` has bound the accepted request ID. Exactly one
waiter may claim the control reader, and it accepts only one `cancel` frame for that ID. Malformed, non-cancel,
wrong-ID, and context-cancelled partial input closes the source transport fail-closed; explicit `Close` and context
cancellation unblock a partial control read. The waiter sets only a read deadline and response operations set only
write deadlines, so source response writes remain full-duplex while cancellation is awaited. This adds no destination
session, relay, runtime-policy, or audit effect.

This slice also removes the generic `OnHostFrame`, shared `activeConn`, legacy `remote_vm` protocol, and
`internal/vsockhandler` package. The local `mvm-agent remote --user <non-root> <destination> -- <command>` client emits
a strict source request, preserves binary output, requires terminal plus physical EOF, and redacts local failures.

At the end of the fifth slice, the source and destination foundations remained dormant for positive VM-to-VM use. The
API-owned relay listener, root source-connection-FD admission, production composition of the private
active-pair/capacity authority, runtime policy resolution/enforcement, redacted audit, and positive Python L2
qualification were still pending. No production path opened a `SourceSession` or constructed an authorized destination
request at that slice.

The sixth slice removes the VM-wide trust flag without attempting compatibility. Fresh databases end at schema version
6 with no `remote_exec` column. The VM model, configuration default, create input/result, repository persistence, CLI
flags, and environment-spec examples no longer expose it. Before either pending-migration inspection or migration
application, schema-version 3, 4, or 5 databases that still contain the removed column fail with an explicit
clean-install-required validation error and remain unchanged. The guard treats column presence as legacy state even
when every stored bit is false; no value is converted into a directional policy.

The seventh slice implements Task 15's private capacity substrate without activating the relay. Root stores strict
schema-1 reservations at `/var/lib/mvmctl/allocations/exec/<32-lowercase-hex>.json` beneath exact `root:root` mode-`0700`
managed directories; each one-link record is exact `root:root` mode `0600`. A record binds its root-generated identity,
non-root owner UID, exact source and destination VM IDs plus launch generations, current host boot ID, and exact
creation/expiration times from `CLOCK_BOOTTIME`. The never-unlinked global
`/run/mvmctl/authority/capacity.lock` serializes the complete scan, stale cleanup, limit check, and no-replace rename
across users and processes.

The strict 4-KiB codec and descriptor-relative store reject missing, unknown, duplicate, case-colliding, nested,
trailing, unsafe, corrupt, and ambiguous state. A complete scan occurs before canonical pruning. Only admitted crashed
temporary files, prior-boot reservations, and reservations whose expiration is at or before the current boottime are
removed; partial cleanup is directory-synced without cancellation. Atomic creation uses exclusive temporary state,
stable metadata, file sync, checked close, no-replace rename, and parent sync. Creation, deletion, pruning,
`durability_uncertain`, and `cleanup_required` evidence survives post-effect and cleanup failures, and a post-rename
creation failure still returns a releasable lease.

Reservation creation fixes the future relay-stop time at creation plus `vmexecwire.MaxExecutionDuration`, so setup and
suspend consume the same five-minute `CLOCK_BOOTTIME` budget. Expiration adds a separate four-second grace for the
protocol's forced-process cleanup and terminal-frame delivery bounds.

The eighth slice composes this substrate behind a private active VM-exec pair authority without activating the relay.
One opening of the instance authority and one index acquisition locate two distinct same-owner records. The authority
opens each VM authority separately, takes the two VM locks in canonical lexical ID order, requires both records to be
`active`, releases the index, and retains the shared roots separately from both active leases. For each exact record it
reopens and retains the recorded processes through pidfds, proves the complete active identity, and performs a final
pidfd locator/liveness proof. Only then does it reserve root-global capacity. After that reservation it rechecks the
host boot ID, both complete active identities, and both pidfd locator/liveness proofs before publishing an immutable
admission containing the owner, source/destination IDs and launch generations, reservation ID, boot ID, and fixed
relay-stop deadline.

The pair lease is close-once and cleans up without reacquiring a lock: destination/source peer pidfds first, capacity
next, destination/source process capabilities, active leases in descending lexical order, and the retained pair roots
last. Admission or cleanup failures preserve the primary classified error and bounded cleanup evidence. At the end of
the eighth slice, the capacity and pair authorities remained private and inert behind a production constructor that
fixed the host-global ceiling at eight; no production caller invoked it yet.

The ninth slice adds one retained authority sidecar without moving the relay into root. Its structural foundation was
initially implemented without registration. The exact privileged action is `jailer-vm-exec-session`; the process
transport starts one root child through the existing system binary, sudo invocation, and fd-0 AF_UNIX control socket.
The final API composition may start it only after `pkg/api` has authenticated and read the strict source request,
resolved the exact destination, and allowed the directional source/destination/non-root-user policy. There is no helper
binary, daemon, extra inherited descriptor, sudoers expansion, generic privileged action, or generic descriptor
channel.

The eventual normal API process will perform the source Firecracker UDS `CONNECT` handshake and complete the
`SourceSession` token/bootstrap before root admission. Its initial closed action request names the exact source and
destination VM IDs and attaches exactly one duplicate of the live source connection with `SCM_RIGHTS` on the first
request byte. Root receives ancillary
data with `MSG_CMSG_CLOEXEC`, rejects missing, extra, unknown, late, or truncated control messages, and requires one
connected, non-listening `AF_UNIX` `SOCK_STREAM` descriptor with close-on-exec. The private authority already fixes a
host-global ceiling of eight sessions and implements binding the received connection to the retained source record
through `SO_PEERCRED`, `SO_PEERPIDFD`, exact PID/start identity, matching effective UID/GID, complete active-process
identity, retained-process proof, and final liveness. The registered root composition now invokes that exact authority
rather than recreating any of those checks; the API listener/consumer that will call it remains pending.

That future socket proof establishes the exact source Firecracker peer, not the selected guest port. The independent
`SourceSession` token/bootstrap handshake remains the source agent-and-port proof. After directional policy approval,
the API acquires only the authenticated destination Firecracker UDS connection through the fixed root broker, before
opening the retained pair session. This ordering is required because the retained pair holds both VM locks while the
root broker needs the destination VM lock to admit a socket; attempting that broker operation afterward waits on a
lock held by the pair. Raw socket acquisition sends no guest `CONNECT`, token, command, or request. After pair
admission, the API performs the destination `CONNECT` handshake and sends exactly one destination-connection duplicate
in a closed second phase. Root applies the same peer-pidfd, record, identity, and final-liveness proof before returning
ready, so a destination that changes between early socket acquisition and binding is refused. Extending the retained
sidecar protocol to hand a destination descriptor back after admission would preserve the original order but add a
second descriptor-transfer direction; the approved 0.3.0 design uses the narrower early raw-socket acquisition.
After both expensive identity proofs, the implemented authority performs one bounded last-look sweep of both
peer pidfds and both retained process sets before its final host-boot and relay-budget check. Only then does the API send
the token-and-command-bearing `DestinationRequest`, which the destination agent must validate before any effect. Root
receives no UDS path, agent token, target user, command, policy material, or response payload.

The registered root child retains the pair/capacity lease and duplicates of both admitted connections for the entire
relay. An explicit release, control EOF, API crash, setup failure, or the absolute `CLOCK_BOOTTIME` relay-stop deadline
shuts
down the shared socket descriptions and performs cancellation-detached reverse cleanup. Capacity creation starts the
five-minute relay budget before destination setup; the separate four-second cleanup/terminal grace never extends
execution. The action uses a closed, versioned, action-specific leased-session protocol over fd 0. File descriptor 0
remains control-only. This is the sole narrow exception to the ordinary privileged action's one-response-and-exit
lifetime; the child still handles one request and one relay lease and never becomes a daemon.

The selected production ceiling is the following compiled root policy:

```go
const productionVMExecMaxSessions uint32 = 8
```

It is global across caller UIDs and has no configuration, environment, or CLI override. Eight matches the
existing guest-side `maxSourceAdmissions` bound and conservatively caps the retained host/guest transports, process
pins, VM locks, process groups, and minimum-guest pressure until live qualification supports a different fixed policy.
The production retained-session server composes that ceiling with active-pair and exact peer authority, and the exact
registered handler invokes it. No API relay invokes the handler yet.

The absolute `CLOCK_BOOTTIME` stop uses an absolute close-on-exec, nonblocking `timerfd`, bounded polling,
cancellation, shared close-once lifecycle, and cleanup-error evidence. It permits exactly one in-flight waiter: a
simultaneous second waiter is rejected rather than sharing that wait, while calls after completion receive its cached
result. The production retained-session server now arms it from the admitted deadline; stop completion makes the
handler close both retained connections and release authority.

The production-composed retained sidecar provides the closed versioned phase codec, bounded safe failure projection,
exact-one
ancillary transfer, ordered process lifecycle, named Jailer client, and private handler. The client duplicates source
and destination connections while leaving caller originals owned by the API process. The handler transfers each
installed descriptor into a retained private owner only after its exact phase decodes, exposes only borrowed connection
capabilities to its effect seam, cleans destination, source, and the effect lease once, and sends `released` only after
explicit-release cleanup succeeds. Control EOF and failures clean without claiming a successful release. The handler
accepts no path, token, user, command, policy, relay payload, or raw descriptor accessor. Central dispatch registers
only this exact action. The caller UID/GID supplied to Jailer derives exclusively from the authenticated control peer
identity, never request fields. Each handler-owned source/destination descriptor stays opaque and pinned under its
owner lock for the sole synchronous binder call. Authenticated failures before dispatch emit one bounded `MVMXSES1`
terminal failure without draining request input; once dispatch begins, the handler owns all action-specific responses
and cleanup. Registration does not create a positive VM-to-VM path because no API relay calls the action.

The tenth slice adds the closed host-side destination-session primitive without activating the relay. A
`DestinationSession` consumes an already selected connection and permits one validated `Open` carrying one host-built
`DestinationRequest`. Its strict request-ID-bound response reader enforces the wire output limits through one
sequential reader and withholds both `Result` and `Error` until physical EOF. After open, the session permits at most
one exact matching `Cancel` while the response reader remains active. Independent fixed read/write deadlines share one
absolute session bound set at open; an earlier opening-context deadline wins, and the lifetime timer closes an idle
session no later than five minutes. Operation-context cancellation and explicit `Close` interrupt partial I/O.
Protocol and transport failures close the connection, while cleanup runs once and returns its cached first result.
The primitive performs no dialing, path selection, policy or VM lookup, root effect, audit, relay forwarding, or API
composition, so it creates no positive VM-to-VM path.

The eleventh slice adds the typed audit sink without activating the relay. It writes one complete fixed schema-1 JSON
record per line to the dedicated user-owned `vm-exec-audit.jsonl` cache file. The closed record contains the operation,
request/source/destination identities, non-root target user, UTC start time, bounded duration, result, and
result-consistent optional exit code, but never the command, agent token, or process cause. Writes and close are
serialized across copied sink values; an incomplete or failed write poisons later records; validation, I/O, and cleanup
failures are typed and surfaced. Effective-root construction is rejected because the user-cache pathname is not
retained descriptor-relatively. The resulting user-modifiable file is observability, not tamper-proof root authority.
No `pkg/api` relay constructs or invokes the sink, so this slice creates neither a runtime audit record nor a positive
VM-to-VM execution path.

Implementation remains fail-closed and proceeds in these independently reviewable increments:

1. [x] Add exact-one-descriptor ancillary primitives and structural FD admission, without registering an action.
2. [x] Add the typed `jailer-vm-exec-session` client/server protocol and retained root-side lifecycle, still
   unregistered.
3. [x] Add exact Firecracker peer/pidfd binding, the fixed global ceiling, and the absolute-boottime stop primitive
   behind private, unregistered boundaries.
4. [x] Compose those boundaries into the retained sidecar, enforce expiry, then register the exact privileged action.
5. Complete the remaining relay primitives without activating the relay:
   - [x] Add the exact request-ID-bound, single-reader `SourceSession.WaitCancel` lifecycle described above.
   - [x] Add a closed destination session.
   - [x] Add a typed error-reporting audit sink.
6. [ ] Add the explicit public trigger and API relay, then qualify same-network, cross-network, and no-IP-network
   behavior plus denial, cancellation, crash, reboot, ceiling, and cleanup paths in Python L2.

VM-to-VM execution remains unavailable until all six increments and L2 qualification are complete.

The relay must not treat audit as best effort. Every terminal path invokes the typed sink and surfaces or joins an audit
failure with the relay result. Because cancellation and timeout terminal paths may run after the operation context is
done, those paths derive a fresh bounded context through `context.WithoutCancel` before calling `Record`; the sink does
not detach caller cancellation itself.

### Layer placement

- `pkg/api/` remains the sole orchestrator of VM and network domain identities and owns public input validation.
- `internal/core/network/` owns typed traffic intent and compilation, without importing another core domain.
- The VM-to-VM exec policy module receives already-resolved shared model identities. The removed
  `internal/vsockhandler/` multi-domain callback path must not return.
- `internal/lib/vmexecwire/` owns only the closed bounded framing, request validation, and stream grammar. It performs
  no policy lookup, relay, destination-agent execution, audit, or runtime-capacity effect.
- `internal/core/vsock/` owns the single-use source and destination transports only after a caller supplies an
  authority-admitted source connection or already selected destination connection. It performs no VM/policy lookup,
  socket-path resolution, dialing, runtime admission, or capacity effect.
- `internal/service/agent/` owns the destination request handler and explicit non-root process effect. It performs no
  host source-VM authentication, policy lookup, host relay, audit, or runtime-capacity allocation. Its local source
  broker proves that one local peer descends from one host-bound outer command; it does not authorize a VM identity.
- `internal/service/network/` owns typed privileged namespace, bridge, veth, TAP, route, and link effects.
- `internal/service/firewall/` owns typed privileged nftables application.
- `internal/service/jailer/` consumes a verified namespace handle as part of typed launch and owns the exact
  active-pair/capacity and Firecracker-peer-FD authority. It does not create a generic network-operation or descriptor
  interface; the FD authority is exposed only through the fixed leased VM-exec action.
- Shared policy and resolved-runtime values live in `internal/lib/model/` only when multiple domains require them.

All side-effecting functions take `context.Context` first and return `pkg/errs.DomainError` failures. Batch operations
report per-item results explicitly; partial success is never collapsed into a nil error.

## Clean-Installation Contract

v0.3.0 does not read, migrate, backfill, or preserve legacy policy rows, iptables state, host-global TAP ownership, or
VM-wide remote-exec values. The fresh database schema is version 6 and contains no such column. Pending-migration and
migration admission reject schema-version 3 through 5 databases that still contain it before schema work, leave the
database unchanged, and return an explicit clean-install-required diagnostic. Initialization never silently adopts or
deletes ambiguous host state.

Operators must stop and remove old VMs, retain any desired user artifacts separately, remove the old mvm state
directory selected by `MVM_CACHE_DIR` (normally `~/.cache/mvmctl`) through the documented clean-install procedure,
clean the old host runtime through the documented administrator path, and install v0.3.0 on a clean state layout. A
live state directory must never be deleted as an incidental migration step. Only the new schema and topology are
supported after initialization.

## Alternatives Considered

### Keep routed policies and trust same-network placement

Rejected. Network placement would remain an implicit authorization mechanism, and same-bridge traffic would bypass the
managed IP-family policy path.

### Put the VM-exec data plane in root

Rejected. A root relay would unnecessarily parse guest commands and output, handle agent tokens and policy material,
dial the destination, and duplicate the multi-domain orchestration assigned to `pkg/api`.

### Use a root daemon, extra inherited descriptor, or verify-and-exit helper

Rejected. A daemon adds installation, upgrade, shared-failure, and authorization lifecycle that the single-binary
one-shot model avoids. An fd above 2 requires `sudo -C` or policy changes and violates the fd-0-only contract. A root
process that verifies descriptors and exits releases the VM locks and capacity before the relay, so its proof is not a
retained authority. Root path dialing and descriptor return are also unnecessary: the normal API already owns both
handshakes, while root can prove the exact Firecracker peers from duplicates of the live connections without accepting
caller-selected paths.

### One namespace per managed network

Rejected. It reduces host interface clutter but places several Firecracker processes in the same host network view. The
microVM, not the managed network, is the isolation unit.

### Optional namespace and legacy bridge backends

Rejected. Two lifecycle paths double the privileged attack surface, cleanup states, and system-test matrix. v0.3.0 has
no compatibility requirement that justifies retaining the weaker path.

### CNI plugins as an additional backend

Rejected for v0.3.0. Root-executed plugin binaries and configurable plugin chains enlarge the trusted computing base and
introduce a generic backend before a second required topology exists. CNI may be reconsidered only with a concrete use
case, root-owned verified plugins/configuration, and a separate security decision.

### Preserve iptables parity

Rejected. Same-network enforcement requires bridge-family filtering, while maintaining two renderers and reconciliation
paths creates security-sensitive duplication. nftables is the single supported implementation.

### Keep the `nft` CLI as the production adapter

Rejected. Rendering privileged command text and parsing human-oriented output retains an avoidable root subprocess,
adds quoting and version-dependent error surfaces, and weakens typed inspection. The existing `nft -f -` path is already
atomic, so the direct adapter is selected for a smaller and more structured effect boundary rather than for new
atomicity.

### Build the compiler or mutation adapter directly on `github.com/mdlayher/netlink`

Rejected. It would make mvmctl own low-level nftables mutation and expression encoding already provided by
`github.com/google/nftables`. The narrow private read-only observation and retained-socket lifecycle uses described
above address inspection and transport requirements without adding another rule encoder or mutation engine.

### Generic policy selectors and actions

Rejected. A generic policy engine would mix enforcement planes and make unsupported combinations appear valid. New
capabilities require new typed policy families and explicit enforcement contracts.

## Consequences

### Benefits

- Firecracker no longer shares the host network namespace.
- Same-network placement grants no implicit application access.
- Policy meaning is stable across IP, MAC, veth, TAP, and bridge-name changes.
- Network and vsock authorization cannot accidentally grant one another.
- One firewall backend reduces privileged code, reconciliation branches, and qualification scope.
- Snapshot cloning and future network pre-allocation can build on an owner-bound namespace lifecycle.

### Costs

- Every VM adds one namespace, veth pair, and namespace-local bridge.
- Launch and cleanup perform more privileged steps and require transactional rollback.
- Hosts must provide network namespaces and nftables with bridge-family support.
- The selected Go module is pre-v1 and must remain exactly pinned, reviewed, and covered by compatibility tests before
  an upgrade.
- Direct netlink expressions are less convenient for operators to inspect than rendered rules, so QA retains an
  independent read-only observation path.
- Existing installations and automation must be recreated for v0.3.0.
- Troubleshooting must distinguish guest, namespace, host bridge, routed, nftables, and vsock planes.
- Source-session guests require `SO_PEERPIDFD`; custom kernels without that capability fail closed instead of falling
  back to numeric PID-only authorization.

## Release Gate

The private topology-effect tests cover exact derived names, namespace ownership and retention, down-state inspection,
managed aliases, reciprocal veth identity, bridge attachment, ownership-bound deletion, cancellation, cleanup, and TAP
creation through fixed `/dev/net/tun` with exact ioctl order and namespace restoration. This is L0/L1 evidence only;
there is no live topology qualification or production activation.

The private compiler tests now cover bridge/IP/NAT expression construction, compiler-derived canonical generation
identity, strict digest-bound userdata, deterministic ordering, explicit empty output, validation, all fields of the
closed eight-type expression set, typed-nil and unknown-expression rejection, and bounded allocation growth for a
representative 1,000-VM generation.

The private snapshot tests cover the closed read-only request catalog, strict raw unknown-expression detection,
forbidden object classes, generation consistency, malformed replies, cancellation/close faults, aggregate caps, and the
retained host-netns factory's exact authority ordering and descriptor lifecycle. Before production integration, the
remaining work must explicitly accept the root/`CAP_NET_ADMIN` threat-model boundary, compose the collector and its
factory into root-record-bound initial inspection and application/recovery, and prove the emitted expressions plus
namespace binding, cancellation/close behavior, delete/create and invalid-batch atomicity, actual empty-generation
deletion/application, and representative 1,000-VM runtime against supported kernels. `nftables.Conn`, `Flush`,
privileged application, public policy cutover, legacy CLI removal, and L2 qualification all remain pending.

The VM-exec wire tests cover strict source/destination request separation, source bootstrap/ready framing,
malformed-frame rejection, random request/session identity, fixed limits, response transitions, combined output,
cancellation, one terminal, and required EOF. Destination-agent L1 tests additionally cover exact protocol routing
without host-exec/copy regression, token-before-effect ordering, numeric non-root account/home admission, fixed command
credentials/environment, binary output and nonzero exit, redaction, output overflow, matching/malformed cancellation,
process-group cancellation, effective vsock deadlines, bounded terminal writes, and shutdown of partial reads.

Source-side L1 covers dedicated-transport isolation, ordinary-exec non-regression, one-request sessions, duplicate and
partial bootstrap, the exact request-bound single cancel reader, malformed/wrong-request cancellation rejection,
cancelled and explicitly closed partial control reads, concurrent full-duplex response writes with directional
deadlines, cancellation of blocked reads/writes, terminal close/EOF, concurrent-session isolation, bounded local
admission/read time, host disconnect and shutdown cleanup, root/non-root outer accounts, exact AF_UNIX peer/pidfd
checks, PID-reuse and zombie rejection, stable bounded process ancestry, one claim, process-group cancellation, binary
CLI output, and redacted failures. This does not qualify API/root source-VM admission, relay policy enforcement/audit,
capacity composition, audit integration, detached-descendant cleanup, a kernel lacking `SO_PEERPIDFD`, or a live
VM-to-VM path.

Destination-session L1 covers the single validated open, request-ID binding and strict response/output limits, binary
output, one sequential reader, terminal withholding through physical EOF, exact single cancellation, concurrent
full-duplex response/cancel operation with independent fixed directional deadlines, opening and operation-context
cancellation, absolute idle lifetime, partial-I/O interruption, failure cleanup, and cached close-once results. It does
not qualify destination dialing or selection, runtime policy, root admission, audit, relay forwarding, API
composition, or a positive VM-to-VM path.

Capacity L1 covers the strict record/codec/store, exact root-owned layout, global lock serialization across independent
authority instances, cross-owner ceiling enforcement, collision handling, stale-boot/expiration pruning, corrupt-state
refusal, atomic publication and durability evidence, owner-bound idempotent release, and close-once cleanup. It does not
authorize a production source FD or qualify a relay. Active-pair/peer L1 covers one-index acquisition,
same-owner active-record admission, canonical VM lock order, retained roots and per-VM/process capabilities, both exact
pidfd/full-identity proofs, post-capacity boot/identity/final-liveness revalidation, immutable admission, and close-once
reverse cleanup. It also covers same-socket peer credentials and pidfd acquisition, close-on-exec ownership, exact
PID/effective UID/GID and locator matching, both complete peer identities, and a final both-peer/both-process liveness
sweep under the fixed global ceiling. The absolute-boottime stop L1 covers expiry, cancellation, rejection of a
simultaneous second waiter, cached completed results, descriptor close/reuse races, and cleanup evidence. Production
composition is registered but still does not provide an API relay, runtime policy enforcement, audit-sink invocation,
payload forwarding, or end-to-end execution.

Retained-sidecar L1 covers closed phase schemas and ordering, bounded failure projection, physical terminal EOF,
exact-one descriptor ownership and structural admission, source/destination duplicate ownership, copied-session shared
lifecycle, cancellation races, process-group termination, bounded wait/reap behavior, explicit-release and implicit-EOF
cleanup, cleanup-error evidence, no duplicate terminal response, production pair/capacity/peer/stop composition,
descriptor pinning during the sole synchronous binder, exact action registration, authenticated caller propagation,
and `MVMXSES1` pre-dispatch failures without request draining. It does not qualify an API relay, runtime policy/audit,
relay payload forwarding, a positive VM-to-VM path, or Python L2 behavior.

Audit-sink L1 covers the exact command-redacted schema and closed result/exit-code combinations, record validation,
dedicated user-owned path, privileged-construction denial, whole-record serialization, context cancellation,
write/short-write poisoning, copied close-once lifecycle, and surfaced typed failures. It does not qualify relay
integration, runtime records, root-controlled tamper resistance, or a positive VM-to-VM path.

This decision is not complete until L1 and L2 tests prove those adapter properties plus namespace ownership, cross-UID
denial, anti-spoofing, same-network and cross-network default deny, exact traffic allows, exec-policy directionality,
non-root enforcement, atomic reconciliation, crash recovery, reboot cleanup, and absence of leaked processes,
namespaces, links, cgroups, mounts, sockets, and nftables rules.

## Related Decisions

- ADR-0005: The final sudo policy exposes only the typed root-owned dispatcher.
- ADR-0009: Firewall backend mutual exclusion is superseded by the nftables-only decision here.
- ADR-0010: Cross-domain resolution remains in `pkg/api/`.
- ADR-0011: Public policy inputs retain typed `Validate` and `Resolve` behavior.
- ADR-0012: Namespace and firewall behavior require real-host L2 evidence.
- ADR-0015: Jailer remains the only Firecracker launch path.
- ADR-0016: Namespace, network, and firewall mutations use the same root-owned typed privilege boundary.
