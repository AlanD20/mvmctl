# Per-VM Network Namespaces and Typed Connectivity Policies

**Status:** Accepted — v0.3.0 implementation in progress
**Date:** 2026-08-23
**Last Updated:** 2026-09-01 (public directional exec-policy CRUD)
**Supersedes:** [ADR-0014: Routed Service-Access Policies](0014-routed-service-access-policies.md)
**See also:** [ADR-0016: Root-Owned Single-Binary Privileged Dispatch](0016-root-owned-single-binary-privileged-dispatch.md)

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

Namespace handles and locks are ephemeral root-owned state under `/run/mvmctl/authority/<uid>/`. VM-created sockets and
display-only PID mirrors use the separate non-authoritative `/run/mvmctl/runtime/<uid>/<vm-id>/` tree defined by
ADR-0016. Persistent minimal VM and network ownership records remain under `/var/lib/mvmctl/`. The user database stores
desired logical configuration but never authorizes a root namespace, device, bridge, route, or firewall mutation.

The privileged launch transaction must:

1. authenticate the caller and consume the prepared release through `preparedRelease.registerLaunching`, durably
   recording `launching` while retaining the release and VM locks;
2. acquire and retain the later network lease, then verify the root-owned VM and network records;
3. create the namespace, namespace-local devices, host veth, and exact managed-bridge attachment with every link down;
4. acquire and retain the firewall lease, apply the complete nftables generation and anti-spoofing rules in one atomic
   transaction, then verify the exact generation through a fresh inspection;
5. only after firewall verification, bring the exact links up and reverify their identity and state;
6. only after those stages and relay readiness, release the blocked child to Jailer with the pinned namespace handle;
   and
7. persist exact namespace and final process identity through `launchLease.activateVerifiedProcess` before reporting
   the VM active.

The complete lock order is `release -> global index -> VM -> network -> firewall`. An operation may acquire a suffix or
subset but must never acquire an earlier class while holding a later one. The schema-3 launch lease is the durable
two-phase ownership claim and retains the VM, network, and firewall leases across application, activation, rollback,
and cleanup without unlocking and reacquiring the VM. Network behavior remains in the network service and nftables
behavior in the firewall service rather than the Jailer authority module. The firewall lock is host-global because one
generation owns both reserved tables across all users and managed networks.

Failure before link activation leaves links down and the Jailer child blocked. Failure after verified activation first
lowers and reverifies the exact links, then invokes the launch lease's typed abort while the same locks and lifecycle
handle remain held. An unknown firewall outcome never raises a link or releases the child. Cleanup verifies namespace
inode, interface ownership, process identity, and cgroup membership before removing anything.

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
TAP/no-PI/exclusive flags, and calls `TUNSETOWNER` for the authenticated non-root UID without setting a group. The
effect rechecks the temporary TAP, sets its managed alias, and reverifies alias, type, no-PI, owner, absent group,
down state, ifindex, and non-persistent state before final `TUNSETPERSIST`; the outer link effect then verifies the
persistent TAP.

Neither the composed private topology effect nor the private service has a production caller. VM-lock transfer,
route/address and verified link-up stages, firewall application, durable VM-topology lifecycle, production construction,
Jailer handoff, public cutover, and live qualification remain pending.

### nftables is the only firewall backend

v0.3.0 requires nftables. The final implementation removes the iptables backend, configuration option, repositories,
renderers, fallback behavior, and tests.

The final root-owned adapter in `internal/service/firewall/` must program the kernel through a reviewed, pinned version
of `github.com/google/nftables`. The module speaks the nftables netlink protocol directly;
`github.com/mdlayher/netlink`
may remain its transitive implementation dependency but is not a second mvmctl adapter. Final production must not
execute or parse the `nft` CLI, link `libnftnl`, depend on `nftables.service`, or fall back to a CLI implementation.
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
forward chain holds same-network policy. Inet forward/input/postrouting cover routed policy, established replies,
managed host services, default deny, conditional uplink egress, and NAT. Masquerade-disabled networks receive no uplink
egress allow. The compiler rejects overlapping prefixes and unusable IPv4 authorities and orders output
deterministically. The caller no longer supplies generation identity. Explicit empty intent compiles to an empty
generation with no reserved tables or rules.

The compiler derives SHA-256 from a versioned canonical closed projection of normalized intent and the exact managed
tables, chains, rules, expressions, order, and userdata schema. The userdata generation slot uses a fixed placeholder
while hashing to break self-reference; the derived digest and rule identity are then bound into every emitted rule.
The projection covers every field of exactly the eight expression structs emitted from
`github.com/google/nftables v0.3.0`: `expr.Meta`, `expr.Masq`, `expr.Bitwise`, `expr.Cmp`, `expr.Ct`, `expr.Payload`,
`expr.Range`, and `expr.Verdict`. Typed-nil and unknown expressions fail closed. Digest encoding streams through a fixed
64-KiB buffer directly into SHA-256, and validation, encoding, and finalization failures surface as compiler errors.
Any nftables module upgrade or emitted-expression change requires an explicit field-coverage audit and digest-schema
version bump.

On the isolated representative 1,000-VM compiler benchmark, the observed range was roughly 23–28 ms with about 10 MB
and 179,000 allocations. L1 retains ratio-based growth evidence and enforces a 250,000-allocation regression ceiling.

This remains an inert compiler foundation. The separate generation authority, evidence-bound record replacement, raw
snapshot collector, and retained exact host-netns transport factory described below are implemented, but there is no
qualified initial inspection or adoption, `nftables.Conn`/`Flush`, actual empty-generation deletion/application,
mutation or recovery orchestration, privileged action, public policy cutover, legacy `nft` CLI removal, or L2
qualification. No kernel-application, atomicity, enforcement, or release-readiness claim follows from these
foundations.

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
still requires an accepted threat model for another root or `CAP_NET_ADMIN` actor, root-record-bound exact-state
authority within that model, application composition, and live-kernel evidence. Ambiguity always blocks `DelTable`.

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
Until those gates pass, no adapter, privileged action, link activation, public cutover, or legacy-backend removal may
use the compiler foundation.

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
property, all-to-all checks, and documentation without conversion.

Implementation note (2026-09-01): the first exec cutover slice severs the legacy relay before the replacement is
activated. Every legacy `remote_vm` request receives one terminal unauthorized response before source lookup, request
parsing, destination lookup, vsock dial, or command logging. Ordinary host execution and copy paths remain unchanged.
The second slice exposes directional intent management as
`mvm policy exec create <source-vm> <destination-vm> <target-user>`, `ls [--json]`, and
`rm <policy...> [--force]`. The API resolves exact source and destination VM identities, rejects the same VM and root
target, derives a deterministic identity from source, destination, and user, and neither resolves nor constrains
network placement. This is desired-state CRUD only. The closed wire, destination capability, authenticated API-owned
relay, legacy flag removal, root runtime/capacity authority, and positive system qualification remain pending;
VM-to-VM execution is unavailable until those dependencies land.

### Layer placement

- `pkg/api/` remains the sole orchestrator of VM and network domain identities and owns public input validation.
- `internal/core/network/` owns typed traffic intent and compilation, without importing another core domain.
- The VM-to-VM exec policy module receives already-resolved shared model identities; it does not import multiple core
  domains through `internal/vsockhandler/`.
- `internal/service/network/` owns typed privileged namespace, bridge, veth, TAP, route, and link effects.
- `internal/service/firewall/` owns typed privileged nftables application.
- `internal/service/jailer/` consumes a verified namespace handle as part of typed launch; it does not create a generic
  network-operation interface.
- Shared policy and resolved-runtime values live in `internal/lib/model/` only when multiple domains require them.

All side-effecting functions take `context.Context` first and return `pkg/errs.DomainError` failures. Batch operations
report per-item results explicitly; partial success is never collapsed into a nil error.

## Clean-Installation Contract

v0.3.0 does not read, migrate, backfill, or preserve legacy policy rows, iptables state, host-global TAP ownership, or
`allow_remote_exec` values. Initialization refuses detected legacy runtime resources and gives the administrator a
precise cleanup command or diagnostic. It never silently adopts or deletes ambiguous host state.

Operators must stop and remove old VMs, retain any desired user artifacts separately, clean the old host runtime, and
install v0.3.0 on a clean state layout. Only the new schema and topology are supported after initialization.

## Alternatives Considered

### Keep routed policies and trust same-network placement

Rejected. Network placement would remain an implicit authorization mechanism, and same-bridge traffic would bypass the
managed IP-family policy path.

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

### Build directly on `github.com/mdlayher/netlink`

Rejected. It would make mvmctl own low-level nftables message and expression encoding already provided by
`github.com/google/nftables`. The lower-level module remains an implementation detail unless a proven missing capability
requires a separate decision.

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
