# Guest Control Authority

**Status:** Proposed; implementation in progress, not release-qualified
**Date:** 2026-11-17
**Amends:** [ADR-0016](0016-root-owned-single-binary-privileged-dispatch.md), for the retained Guest Control worker

## Context

A guest needs to request specific mvmctl features without becoming an operator. Floating-IP association is the
first use case. A raw CLI proxy would give a compromised guest access to unrelated operations. Reusing the
command-bound VM-exec relay would also make background availability depend on an interactive host command.

The existing console and NoCloud services establish the single-binary subprocess precedent. The retained
VM-exec sidecar establishes exact Firecracker-peer binding. Neither already implements Guest Control.

## Decision

Provide a versioned Guest Control session with closed, typed feature methods. Both the operator CLI and guest
agent exercise the floating-IP implementation. Operator admission and guest delegation remain separate.
Adding another feature requires a named method and its own authorization contract, not a plugin or command hook.

### Connection and identity

The host establishes a dedicated connection to the agent through the VM's protected Firecracker vsock socket.
The guest submits requests on that connection; it does not provide a VM ID, owner UID or incarnation as identity.
Before accepting requests, root verifies the live VMM peer and current launch incarnation using the existing
process, socket and active-record authorities. A guest token alone, numeric CID or cache record is insufficient.
The session bootstrap must authenticate the agent endpoint as well as the VMM socket before it becomes ready.

Endpoint authentication uses TLS 1.3 with mutually pinned, role-specific Ed25519 certificates and a dedicated
application protocol. Pins identify provisioned keys; certificate subjects and clocks do not establish VM identity.
The host private key stays in root authority. The guest receives only its own private material and the public
host pin. A TLS-protected acceptance acknowledgement confirms mutual authentication before the host returns.
The leaf authentication, backend-preserving credential preparation and root snapshot-reference integration
are implemented and reviewed. Worker integration and live provisioning qualification remain pending. Memory
snapshots require a root-verified credential lineage. Each restored VM receives a fresh process/incarnation
principal and no inherited grants or session.

Cold credential preparation preserves the selected provisioning backend from start to finish. A typed,
owner-authenticated preparation creates root material for the exact pending VM and returns only its agent
material. The existing backend installs that material at a fixed guest-root-only path during normal cold
provisioning; GuestFS continues to operate on caller staging without a privileged appliance or native mounts.
Root then imports and protects the disk and binds the expected immutable credential version to that import.
Neither a protected backing descriptor nor the host private key is returned to the provisioner.

The operator already controls guest root and can read its own guest's agent key. Relaying that key to its cold
provisioner adds no operator authority. This deliberately permits agent material in caller staging; it does not
permit caller-selected root pins or host keys. The receipt proves expected lineage and protected import, not
successful injection or endpoint readiness. Readiness still requires mutual TLS and the exact live VMM proof.
Memory-snapshot inheritance requires root capture authority; cold imports always receive fresh root material.

A managed worker of the installed `mvm` binary maintains the session while the VM is running and delegated
features are enabled. No separate helper executable or broad sudoers permission is introduced. This is a new
retained-lifetime exception: it must be registered explicitly, bounded per VM, and stopped by authenticated
lifecycle cleanup. It must not retain a global network mutation lock while waiting for guest input.

The exact worker spawn, recovery and bootstrap implementation must be reviewed before dispatch registration.
It must survive an ordinary CLI exit, reconnect only to the same admitted incarnation, stop on VMM exit and
invalidate the old session on stop, restore or replacement. Failed setup grants no authority.

### Delegation and privileged effects

An operator grants exact operations on exact resource IDs to an eligible VM. Delegations live in root-controlled
authority; SQLite and caller-provided policy documents are display or desired-state inputs, never proof of a grant.
Requests carry a guest principal derived from the retained session through to the privileged effect. They must
not be converted into requests authorized merely because the worker runs as root or belongs to an enrolled operator.

The effect checks the current incarnation, grant, resource ownership and operation precondition while holding
the relevant mutation lock. Revocation and mutation share that serialization point. Revocation stops later
effects and fences any ownership that the revoked grant sustained. Other root/CAP_NET_ADMIN administrators
remain trusted; detected foreign state continues to refuse rather than being overwritten.

Root grant records now bind one immutable grant ID to an exact resource, VM incarnation and method. Revocation
moves through recorded revoking and revoked states; old IDs cannot reactivate. Ownership records retain the
exact sustaining grant revision, so revocation does not cancel operator ownership or a different grant's lease.
Association and renewal grants require an explicit finite whole-second lease policy; inspect and release do not.
Persistence, comparison helpers and guest ownership effects are implemented; public grant dispatch and the
authenticated worker remain unwired.

Guest root can exercise every delegation granted to that VM. Local agent permissions restrict ordinary guest
users but cannot isolate an application from guest root. The implemented gateway uses a dedicated vsock port
and a root-only local Unix socket. It forwards closed typed requests over one pinned TLS session and retires ambiguous or disconnected sessions. The
`mvm-agent control floating-ip` commands expose inspect, associate, renew and release; they require the
host worker before operations can succeed. Guest address reconciliation and live transport qualification remain
pending. The local control socket is privileged by default.

### Protocol

The transport is distinct from VM-exec and ordinary privileged-dispatch frames. Version 1 uses `MVMGCTL1`, a
big-endian uint16 wire version and uint32 JSON-body length. Bodies are limited to 16 KiB. Requests contain the
exact fields `schema_version`, `request_id`, `feature`, `operation` and `input`. Request IDs are canonical UUIDv4.
The first feature is `floating_ip`; its initial guest methods are `inspect`, `associate`, `renew` and `release`.
Its input names an exact resource ID and expected ownership generation. A guest can associate only itself.

Unknown fields, duplicate fields, invalid UTF-8, unknown versions/methods, trailing JSON and invalid typed inputs
refuse before effects. There are no paths, commands, arbitrary arguments, caller identities or raw kernel payloads.
Decoding is structural admission, not authorization. The session owner supplies transport deadlines and closes
the connection on cancellation; a context check alone cannot interrupt a blocked generic reader.

One request is in flight per session. Bounded request IDs correlate results; they do not promise exactly-once
execution. A timeout leaves the outcome unknown. Ownership generations and inspection support recovery without
replaying a stale mutation. Reusing an old expected generation cannot reverse a newer ownership decision.
Error responses use bounded public projections of `DomainError`, excluding host paths, tokens and wrapped causes.

The implemented response codec binds the request ID, operation and resource to the outstanding request. Its
envelope contains `schema_version`, `request_id`, `feature`, `operation` and exactly one of `result` or `error`.
A result reports the resource ID, private IPv4 address, generation, ownership relative to the guest
(`unassociated`, `self` or `other`) and remaining lease milliseconds. Lease time is measured at the host's
observation; receiving a response does not restart it. Association and renewal success require self ownership;
renewal retains the generation, while release reports an unassociated resource with no remaining lease.
Errors contain only a closed public code: `invalid`, `denied`, `conflict`, `unavailable`, `internal` or
`outcome_unknown`. The host must select that projection explicitly rather than serialize an internal error.
These codecs do not establish a session, authorize an operation or configure a guest address.

### Layer placement

- `internal/lib/guestcontrolwire`: closed request/result framing, shared by host and embedded agent.
- `pkg/api/inputs`: operator input validation and resolution.
- `pkg/api`: operator orchestration across VM, network and policy domains.
- `internal/core/network`: floating-IP desired-state repositories and network feature services.
- `internal/service/jailer`: retained VMM process, socket and incarnation proof.
- `internal/service/network`: root floating-IP resources, delegation and fencing under existing network locks.
- `internal/service/privileged`: explicitly registered typed root dispatch and retained-worker boundary.
- `internal/service/agent`: session client, local admission and guest address reconciliation.

Services do not import the API layer. Core domains do not import each other. The shared root feature implementation
accepts an admitted principal; the Guest Control worker is not an alternative cross-domain API orchestrator.

## Consequences

Background guest control introduces a managed process lifecycle. Host init, reset, cache cleanup, VM removal,
uninstall and binary replacement must account for workers and revoke their authority before deleting records.
No enabled delegation means no new background worker is needed. Guest Control does not grant guest-to-host
network reachability or VM-to-VM exec permission.

## Verification

Verify codec refusal and framing boundaries, then guest spoofing, stale incarnations, revoked grants, worker
failure, bounded concurrency and privileged-effect admission in the disposable runner. Registration is gated on
the identity/bootstrap review and those tests. A codec implementation alone is not a working control plane.
