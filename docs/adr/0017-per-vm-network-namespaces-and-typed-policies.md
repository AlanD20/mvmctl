# Per-VM Network Namespaces and Typed Connectivity Policies

**Status:** Accepted — v0.3.0 implementation pending
**Date:** 2026-08-23
**Last Updated:** 2026-08-28 (direct Go nftables adapter)
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

1. authenticate the caller and register the durable VM launch identity while holding the global claim and VM locks;
2. acquire the later network lock and verify the root-owned VM and network records;
3. create the namespace and namespace-local devices with links down;
4. create and attach the host veth to the exact managed bridge;
5. install the complete nftables generation and anti-spoofing rules;
6. bring the verified links up;
7. pass the pinned namespace handle to Jailer; and
8. persist exact namespace and process identity before reporting the VM active.

The complete lock order is `release -> global index -> VM -> network`. An operation may acquire a suffix or subset but
must never acquire an earlier class while holding a later one. This permits `RegisterLaunch` to return a VM-locked lease
before the first network effect without creating a two-phase ownership claim or importing network behavior into the
Jailer authority module.

Failure after the first effect invokes typed rollback while the same locks and lifecycle handle remain held. Cleanup
verifies namespace inode, interface ownership, process identity, and cgroup membership before removing anything.

### nftables is the only firewall backend

v0.3.0 requires nftables. The iptables backend, configuration option, repositories, renderers, fallback behavior, and
tests are removed.

The root-owned adapter in `internal/service/firewall/` programs the kernel through a reviewed, pinned version of
`github.com/google/nftables`. The module speaks the nftables netlink protocol directly; `github.com/mdlayher/netlink`
may remain its transitive implementation dependency but is not a second mvmctl adapter. Production does not execute or
parse the `nft` CLI, link `libnftnl`, depend on `nftables.service`, or fall back to a CLI implementation. Hosts still
require the applicable kernel nftables, bridge-family, connection-tracking, and NAT capabilities, and the privileged
receiver still requires the authority needed to program them.

Only mvmctl-owned typed intent crosses the privileged protocol. Types from `github.com/google/nftables` and
`github.com/mdlayher/netlink` remain private to the root-side firewall adapter. One reconciliation constructs a complete
generation and submits it in one `Conn.Flush` transaction. The adapter takes `context.Context` first, binds cancellation
and deadlines to the netlink operation, and maps failures to `pkg/errs.DomainError`; a cancelled operation cannot be
reported as successful.

Host readiness uses direct kernel capability probes rather than executable lookup, `nft --version`, or parsing CLI
diagnostics. The `nft` CLI may remain installed in disposable QA runners as an independent, read-only observation
oracle. It is not a production dependency or a sudo target.

One nftables implementation owns complete atomic generations for:

- bridge-family same-network filtering and source MAC/IP anti-spoofing;
- IP-family routed filtering between managed networks;
- VM-to-host input restrictions;
- established reply handling and required mvmctl host services; and
- managed internet egress/NAT.

Managed VM-to-VM traffic is default-deny whether the VMs share a network or use different networks. Required ARP and
system traffic are derived internally. Links are not made usable before the applicable deny and allow generation is
installed successfully.

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

The legacy `allow_remote_exec` VM field, CLI flags, configuration default, environment-spec property, all-to-all checks,
and documentation are removed without conversion.

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

Before production integration, a focused compatibility spike must prove the pinned Go module supports bridge-family
MAC/IP anti-spoofing, same-network and routed filtering, established/related state, TCP/UDP ranges, NAT masquerade,
stable mvmctl generation identity, namespace-scoped connections, context cancellation/deadlines, and structured
inspection. It must also prove that one invalid expression aborts the complete transaction and benchmark a
representative 1,000-VM generation within a reviewed budget.

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
