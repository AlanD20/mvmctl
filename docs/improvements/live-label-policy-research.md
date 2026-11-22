# Live label policy research

**Status:** Research findings and design recommendation. Runtime qualification is pending.
**Date:** 2026-11-22.
**Scope:** Live VM label selectors for traffic and VM-to-VM exec in the existing single-binary, typed privileged model.
No VM-name wildcards, eBPF/CNI redesign, installation, migration, adoption, or compatibility layer is proposed.

## Recommendation

Materialize selector memberships when labels, policies, or VM lifecycle state change. Traffic uses root-compiled
nftables sets; exec admission uses a root-owned, generation-bound membership snapshot. Neither path evaluates label
expressions or queries the user database for each connection. Keep exact-VM grants as a useful explicit feature.

This is a recommendation inferred from the sources below, not a benchmark result. It fits the current
[typed policy and firewall boundary](../adr/0017-per-vm-network-namespaces-and-typed-policies.md) and
[single-binary privilege boundary](../adr/0016-root-owned-single-binary-privileged-dispatch.md). Extending those accepted
contracts requires a reviewed design. Clean v0.3.0 schemas and authority records should describe the final model directly.

## Primary evidence

The six sources below were inspected directly. Versioned source is pinned where available; documentation URLs without
a fixed revision are identified as live references. The kernel introduction documents general transport behavior,
not a cross-subsystem transaction guarantee.

| Verified fact | Direct source / revision | Consequence for this proposal |
|---|---|---|
| Named nftables sets permit membership changes; rules reference them. Ruleset evaluation and conntrack state are separate concepts. | [Netfilter nft manual, SETS, OVERALL EVALUATION, CONNTRACK EXPRESSIONS][nft] (live; page reports 07/02/2026) | Compile labels into endpoint membership; ordering must prevent established-state acceptance bypassing current authorization. |
| A ruleset file is applied as one transaction, rather than exposing each intermediate command. | [Netfilter atomic replacement, oldid 693, 2021-02-13][atomic] | Submit one complete owned generation through the existing Go adapter, not multiple independent changes. The CLI example establishes kernel semantics, not a production CLI dependency. |
| Netlink requests have correlated acknowledgements; a dump marked `NLM_F_DUMP_INTR` may be inconsistent and must be retried. | [Linux kernel Netlink introduction, message types and dump consistency][netlink] (live) | Inspect errors and retry inconsistent observation. Sending bytes or obtaining a dump is not by itself proof of a complete applied generation. |
| Conntrack entries can be deleted with filters for original/reply tuples and zone. | [Netfilter conntrack manual, commands and filter parameters][conntrack] (2019-08-09) | Targeted flow-state cleanup exists, but it is a separate operation from nftables publication. |
| Cilium caches selectors and selected identities, populates new selections from cached identities, and propagates identity changes to selections. Its update code explicitly handles overlapping selectors and mutated identities. | [Cilium v1.18.0 selectorcache.go][cilium], `SelectorCache`, `addIdentitySelector`, `UpdateIdentities` | Reuse the concept of materialized memberships and versioned updates; do not import Cilium or copy its agent architecture. |
| Kubernetes requirements are ANDed; `in` provides alternatives for one key. `!=` and `notin` include absent keys. Empty-selector semantics depend on API context. | [Kubernetes Labels and Selectors][labels] (live) | Define a small explicit grammar and empty-selector behavior. Negative selectors need particularly careful treatment for grants. |

## Options and scaling

The costs below are design estimates, not measured latency or throughput guarantees. Let V be VM count, S distinct
selectors, P policies, and M total selector memberships.

| Option | Work and failure behavior | Assessment |
|---|---|---|
| Evaluate selectors for every connection or exec | Repeated label access and matching; freshness and admission race with mutation. Traffic would require additional userspace interception or a different datapath. | Reject for this scope. |
| Materialize on typed mutation and lifecycle operations | Initial matching can cost O(V × S); one changed VM can be checked against S selectors. Store O(M) memberships. Packets perform kernel lookups; exec performs bounded snapshot lookups. | Select. Synchronous acknowledgement can promise that an operation has been applied and verified. |
| Add a persistent watcher/daemon | Can coalesce changes and recover pending work without a later CLI operation, but adds IPC, supervision, event ordering, replay, and another long-lived authority holder. Event loss still needs full reconciliation. | Defer unless independently changing metadata or unattended repair becomes a product requirement. A daemon could still use the same binary. |

Deduplicate identical selectors. For traffic, source and destination membership sets can avoid expanding a policy
into every permitted VM pair. For exec, test source/destination membership against relevant typed policies at admission;
do not precompute an unrestricted O(V²) pair matrix. A bounded O(P) policy scan with set lookups is a reasonable first
design; add indices only when measured policy counts justify them. Neither is selector evaluation per admission.

## Security and consistency contract to adopt

These are proposed requirements, not features proven by the upstream sources or current implementation.

- Labels used for grants need explicit owner-scoped mutation authority. A guest or another owner must not acquire
  access merely by asserting a label. Materialization must join immutable VM identity and admitted runtime generation;
  labels alone are not peer authentication. Preserve endpoint anti-spoofing and vsock source admission.
- Persist typed selector intent, not raw nftables expressions, names, addresses, or caller-selected host interfaces.
  Root-owned accepted label/policy state is authoritative for enforcement; user SQLite is a desired projection.
  Active traffic membership uses admitted endpoint identity. Exec membership also supports networkless VMs.
- Start with nonempty equality requirements combined by AND; values are literal. Reject an empty grant selector
  instead of silently treating it as all VMs. If negative/set operators are later approved, explicitly specify
  absent-key behavior. `environment!=dev` must not be casually interpreted as “has a non-dev environment.”
- Apply all relevant mutation paths: label changes, policy changes, VM create/remove, activation/restart, cleanup,
  endpoint replacement, and recovery. Snapshot restoration must not publish an active endpoint before its current
  membership and incarnation have been admitted.
- Serialize policy-affecting operations with existing typed authority and a defined lock order. Use a durable root
  generation/journal and verify the complete resulting kernel projection before reporting success. Do not treat an
  in-memory event callback as durable delivery.

### Existing flows and exec sessions

Removing an element from an allow set does not defeat an earlier unconditional `ct state established,related accept`:
that ordering follows directly from [nftables verdict evaluation][nft]. Make current authorization a required kernel
gate for managed packets, including established traffic, before any acceptance that could bypass it. Reverse/reply
direction and related flows require an explicit compiled design; do not grant arbitrary reverse initiation.

This performs a set lookup per packet, not label matching per connection. Prefer this security property over relying
only on [conntrack deletion][conntrack]. Conntrack cleanup can remove obsolete state, but is not an atomic revocation
barrier and must not broadly delete unrelated host flows. Exclude flow offload that could bypass the required gate
unless its invalidation is separately designed and qualified.

For exec, admission should load one current authoritative snapshot and bind its generation to authenticated source,
destination, and target user. A label change can deny subsequent requests without re-evaluating selectors. Decide
separately whether an already admitted command continues under its existing bounded lease or is actively cancelled;
network filtering and conntrack cleanup cannot undo a command's effects. Do not promise immediate command cancellation
unless relay termination and destination-process handling are implemented and qualified.

### Crash recovery and the limits of atomicity

[Atomic nftables replacement][atomic] does **not** commit user SQLite, root metadata, filesystem durability, exec
snapshots, conntrack deletions, VM link changes, or independent network namespaces together. It does not prevent two
writers from compiling different snapshots, validate labels as authority, or retract already delivered bytes. A timeout
or missing reply requires fresh observation, not an assumption that the operation did nothing; this is a protocol
design inference from separate request/reply and [acknowledgement handling][netlink].

Define the label/policy commit point explicitly. For a mutation that removes permissions, first establish and verify
a persistent restrictive fence for affected traffic and block relevant exec admission. Then record pending authoritative
state, materialize and verify the new generation, publish the matching exec snapshot, and remove the fence only when
both planes are coherent. A process-only mutex is not a persistent exec fence: another receiver must refuse admission
while the durable transition is pending. Crash before fencing leaves the old committed state; crash after fencing may
leave access unavailable but must not reopen obsolete grants. Additions must likewise remain unpublished until the
required generation is verified. Avoid whole-host fencing when affected endpoints/policies can be bounded safely.

On later typed operation/startup, reconcile pending transitions from root records and fresh admitted identities; never
adopt arbitrary observed kernel objects. Across reboot, endpoint activation must remain behind reconstruction and
verification. With no daemon, repair after a crashed operation can wait for the next explicit operation; a durable
restrictive fence preserves security at an availability cost. If the product requires automatic bounded-time repair
while the CLI is absent, a supervised reconciler becomes a separate explicit requirement.

## Remaining uncertainty and stop condition

No claim of “most efficient” is established without representative V/S/P/churn measurements. The implementation design
still needs to settle the exact multi-plane commit protocol and lock order, reverse/related-flow authorization,
selector bounds, label authority/storage, and active-exec revocation semantics. Supported kernel/library versions need
qualification of the selected set-key representation and complete-generation replacement, plus crash-injection evidence
for each durable transition boundary. This pass did not execute kernel probes, tests, benchmarks, or VM actions.

The evidence is sufficient to select event-driven materialization and reject per-connection label queries or an
unmotivated daemon. It is not sufficient to approve production code or claim immediate revocation across both planes.

[nft]: https://www.netfilter.org/projects/nftables/manpage.html
[atomic]: https://wiki.nftables.org/wiki-nftables/index.php?title=Atomic_rule_replacement&oldid=693
[netlink]: https://docs.kernel.org/userspace-api/netlink/intro.html
[conntrack]: https://netfilter.org/projects/conntrack-tools/conntrack-manpage.html
[cilium]: https://github.com/cilium/cilium/blob/v1.18.0/pkg/policy/selectorcache.go
[labels]: https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/
