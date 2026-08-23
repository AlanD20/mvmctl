# Root-Owned Single-Binary Privileged Dispatch

**Status:** Accepted — implementation in progress
**Date:** 2026-08-20
**See also:** [ADR-0002: Single Go Binary Architecture](0002-single-go-binary-architecture.md),
[ADR-0005: Sudo Privilege Architecture](0005-sudo-privilege-architecture.md),
[ADR-0015: Jailer Is the Canonical Firecracker Launcher](0015-jailer-canonical-launcher.md)

## Context

mvmctl is a Go CLI whose user-facing commands and subprocess services are compiled into one release artifact. The CLI
is intended to run as an unprivileged user while a small set of VM, filesystem, network, firewall, cgroup, and Jailer
operations require root.

The existing installation contract puts `mvm` at user-owned `~/.local/bin/mvm` and generates a passwordless sudoers
entry for that path with wildcard arguments. A member of the `mvm` group can replace the executable and then ask sudo to
run the replacement as root. Root ownership checks performed later by Jailer cannot repair a compromised executable
boundary.

The existing sudoers policy also grants raw argument access to host tools such as `ip`, `iptables`, `nft`, `modprobe`,
and `sysctl`. Those tools expose substantially more host authority than the named mvmctl operations that need them. The
policy therefore does not provide the targeted access claimed by ADR-0005.

Introducing a separately compiled privileged helper would provide a small executable trust boundary, but it would break
the project's single-binary product contract. A safe design must retain one artifact without allowing the ordinary CLI
initialization path or caller-controlled program bytes to execute as root.

## Decision

### One root-owned system installation

Manually built and release-script artifacts are installed at `/usr/local/bin/mvm` as `root:root`, mode `0755`. The
directory chain must be root-owned and not group/world writable. `/usr/bin/mvm` is reserved for a future installation
owned by a distribution package manager.

Normal users execute `/usr/local/bin/mvm` without sudo. The file contains both the public CLI and the internal privileged
dispatcher, preserving one release artifact and one installed executable. Replacing or upgrading the system executable
is an explicit administrator action; a normal user update cannot overwrite it.

Development builds may exist elsewhere, but they are never sudo targets. A development build that needs privileged
operations calls the compatible system installation and fails closed on a privileged-protocol mismatch.

### Administrator bootstrap and system-binary replacement

System installation is selected by the exact `host install-system` command before `app.Initialize()`, Cobra, signal
setup, user configuration, the user database, cache resolution, logging configuration, or service construction. The
bootstrap route clears the inherited environment, installs only fixed root-safe values, and then atomically copies the
running image to `/usr/local/bin/mvm`. Flags and extra arguments are rejected inside the same reserved route and cannot
fall through to the ordinary CLI.

The installer cannot prove whether an already-root process reached it through password-authenticated administrator sudo
or an unintended passwordless rule. It therefore inspects the project-managed sudoers drop-in before touching the
executable and refuses unrecognized active syntax. On a clean host, an administrator runs
`sudo <trusted-mvm-binary> host install-system` and then runs `sudo /usr/local/bin/mvm host init`. Installation is not an
implicit side effect of `host init`.

v0.3.0 does not use this installer to convert, adopt, or preserve a pre-v0.3 installation. Operators must remove the old
managed sudoers rule and clean the old runtime/state layout in an authenticated administrator session before installing
v0.3.0. Later replacement of an already-v0.3-compatible system binary remains an explicit administrator action and
must fail closed when the privileged protocol is incompatible.

Replacing the executable is descriptor-relative, no-follow, temporary-file plus file fsync plus rename plus directory
fsync. Failures after rename report that replacement occurred and whether directory durability is uncertain. A normal
`self-update` may update a user-owned artifact, but it refuses to overwrite the system installation and prints the
administrator bootstrap instruction.

### Early privileged dispatch

`cmd/mvm/main.go` recognizes one reserved, versioned privileged marker before signal setup, `app.Initialize()`, Cobra,
user configuration, the user database, logging configuration, or plugin/service discovery. Once selected, this path can
only enter the privileged dispatcher; it cannot return to or invoke an ordinary CLI command.

The dispatcher:

- requires effective UID 0 and derives the invoking identity from sudo's trusted execution context;
- verifies the system executable and its directory chain are root-owned and not group/world writable;
- verifies that the invoking user is currently authorized by the `mvm` group;
- sanitizes its environment and uses absolute paths for root-side subprocesses;
- accepts only a versioned, bounded, typed request for a named operation;
- rejects unknown actions, extra arguments or fields, invalid identifiers, unsupported versions, and unchecked paths;
- never accepts raw commands, arbitrary argv, shell fragments, raw firewall syntax, or raw cgroup key/value input.

The sudoers entry may contain a wildcard after the reserved marker because operation arguments vary, but that wildcard
can reach only the early dispatcher. It cannot select the public CLI. The dispatcher remains the authoritative
allowlist, and every handler performs exact argument-count and type checks before side effects.

Synchronous actions return one bounded, versioned response envelope on their control channel. Success payloads are
action-specific. Failures preserve the original `DomainError` code, class, entity, and bounded partial-state details so
the normal-user client can make safe retry and recovery decisions. Logs and subprocess exit status are diagnostic only;
they never replace the structured result or turn a malformed/missing response into a generic successful operation.

The control channel is one full-duplex Unix stream socket duplicated onto file descriptor 0 across `sudo -n`. File
descriptor 0 is control-only and is marked close-on-exec before any handler can launch a descendant. Standard output and
standard error are never parsed as authority. VM console input/output must use a separate typed relay owned by the launch
implementation; it cannot reuse the control channel. Using descriptor 0 avoids sudo policy for inherited descriptors,
while the socketpair permits the client to upload a bounded release archive and receive an early rejection concurrently.
Root requires descriptor 0 to be an `AF_UNIX` `SOCK_STREAM` socket and requires its Linux `SO_PEERCRED` UID/GID to match
the authenticated sudo caller. The peer PID is positive and audit-only; it is not an authorization identity.

Each request starts with fixed `MVMREQ01` magic, a network-order 32-bit JSON-header length, and a network-order 64-bit
payload length. The strict header is at most 64 KiB and contains schema version 1, the action repeated from argv, and the
action-specific body. The repeated action must match. Only release install may carry a payload: either zero bytes or an
exact stream of at most 128 MiB. The caller half-closes its write side after the declared bytes, and root requires EOF so
truncation or trailing bytes fail closed before any payload-dependent effect. Zero-payload operations likewise require
EOF before effects. Authentication, framing, and header failures may return an early error without draining an advertised
payload; the concurrent client treats the resulting uploader `EPIPE` as expected once it has a valid response. Root
applies bounded header and archive deadlines and reads no second request.

Each response starts with fixed `MVMRES01` magic and a network-order 32-bit JSON length followed by at most 64 KiB of
strict schema-version-1 JSON. It contains the matching action, a closed `success` or `error` status, and exactly one typed
result or error. Error envelopes carry code, stable string class, message, operation, entity, and normalized details;
they never serialize wrapped causes, stacks, stderr, or arbitrary Go values. Wire details permit only bounded booleans,
strings, signed integers, and string arrays. Unsupported details are omitted visibly. Root half-closes the control
socket's write side after the complete frame, so the client can validate response EOF independently of process exit. An
error response delivered after all effects and cleanup exits successfully at the process layer. A start failure has
known no-effect status; any started process with a missing, malformed, truncated, mismatched, oversized, or non-EOF
response returns `CodeProcessError` with `process_started: true` and `outcome_unknown: true`.

### Authorization roles

The root-owned executable establishes code integrity. The `mvm` Unix group remains the authorization role for
passwordless privileged VM operations. Ordinary non-privileged commands do not require group membership.

Membership in `mvm` authorizes management of mvmctl-owned VM infrastructure; it does not authorize arbitrary host root
commands. The sudoers drop-in grants only the reserved privileged entry point of the root-owned system executable. It
does not grant the public CLI or raw `ip`, `iptables`, `iptables-restore`, `iptables-save`, `nft`, `modprobe`, or `sysctl`
commands.

`host init`, system-binary installation/replacement, group membership changes, sudoers changes, and `host reset` modify the
authorization boundary itself. They remain explicit administrator operations under the administrator's normal sudo
policy and are not exposed through the `mvm` group's passwordless dispatcher.

The binary is never setuid and is not assigned broad Linux file capabilities.

### Typed privileged operation catalog

The initial dispatcher contains only the following named capabilities. Public callers retain domain-specific typed
methods; there is no generic privileged-operation interface.

| Capability | Allowed effect | Required binding |
|---|---|---|
| Trusted release install/remove | Verify and atomically manage an exact Firecracker/Jailer release pair | Fixed origin, version, architecture, root release manifest, active references |
| VM launch/abort/cleanup/reconcile | Create and recover one Jailer process, chroot, cgroup, and private mount namespace | Invoking UID, VM ID, exact release, root instance record, per-VM lock |
| Snapshot expose/remove | Mount one validated snapshot directory in the registered VM namespace | Invoking UID, VM ID, snapshot ID, pinned source/target descriptors |
| Volume expose/remove | Mount one validated volume at one typed drive ID in the registered VM namespace | Invoking UID, VM ID, volume/drive ID, pinned source/target descriptors |
| Image provision | Execute the existing typed provision operations inside one pinned managed image/rootfs | Invoking UID, managed resource identity, pinned root descriptor, bounded operation list |
| Bridge/TAP lifecycle | Create, configure, or remove an mvmctl-owned bridge or TAP | Invoking UID, network/VM identity, validated interface/address model |
| Neighbor flush | Flush neighbors only for an mvmctl-owned bridge | Invoking UID and registered network identity |
| Firewall inspect/reconcile/teardown | Render and atomically apply the complete typed mvmctl ruleset | Invoking UID, typed firewall models, fixed mvmctl tables/chains/comments |

Cgroup preparation and removal are part of VM launch/abort/cleanup rather than a public generic cgroup operation. Module
loading and sysctl mutation are administrator-owned host initialization steps, not operator dispatcher capabilities.

Adding a new privileged capability requires a new typed method, abuse cases, ADR-0016 review, and sudo threat-model
review. “Extensible for future use” is not a reason to add a generic action.

### Module placement and dependency direction

The privileged boundary follows the repository's existing service-subprocess architecture and does not create a second
application architecture:

| Package/file | Responsibility | Must not do |
|---|---|---|
| `cmd/mvm/main.go` | Detect the reserved marker and enter privileged dispatch before normal initialization | Parse domain requests, perform side effects, initialize the public CLI |
| `internal/service/privileged/` | Versioned envelope, bounded parsing, invoking identity, executable verification, and fixed action dispatch | Import `pkg/api`, `internal/cli`, or any `internal/core/*`; expose generic command execution or raw effect paths |
| `internal/service/jailer/` | Typed release, launch, namespace mount, cgroup, process, and cleanup handlers plus normal-user client calls | Orchestrate other core domains or trust user paths after validation |
| `internal/service/loopmount/` | Existing typed provisioning protocol and confined rootfs operations | Accept arbitrary host roots or unrelated unmount/PID targets |
| `internal/service/network/` | Typed root-side bridge, TAP, address, route, and neighbor mutations | Accept raw `ip` argv or own network business orchestration |
| `internal/service/firewall/` | Typed root-side firewall inspection and atomic managed-ruleset application | Accept raw nftables/iptables text from the caller |
| `internal/lib/model/` | Shared typed request/value models used across boundaries | Import services, core domains, API, or CLI |
| `internal/lib/system/` | Subprocess transport and low-level process helpers | Become a generic privileged-operation API or render domain commands |
| `internal/core/{domain}/` | Existing domain logic and typed backend interfaces | Import another core domain or perform cross-domain orchestration |
| `pkg/api/` | Sole cross-core orchestration and public input validation | Implement root-side handlers or bypass typed service methods |

The central dispatcher performs only fixed routing and security-boundary checks. Actual effects remain in
capability-specific service packages and files. Each public client method keeps a domain name and shape, such as
`jailer.Launch`, `jailer.Cleanup`, `network.EnsureTap`, and `firewall.Reconcile`; there is no public
`ApplyOperations([]Operation)` or `RunCommand([]string)` method.

Where an existing leaf package needs privileged effects, it defines the smallest typed interface in the owning layer and
receives a concrete service client through construction. Leaf packages do not gain imports of `pkg/api`, `internal/cli`,
or core domains. The one existing loopmount service import exception is not generalized without a separate architecture
review.

Normal API `Input.Validate`/`Resolve` remains the caller-facing validation layer. Because the privileged dispatcher
receives data from an untrusted process, it independently enforces authorization, ownership, bounds, path confinement,
and system-damage invariants. These are receiver trust-boundary checks, not duplicated business validation.

### Release trust

The caller supplies a validated release version and architecture and may stream an exact-length bounded archive body for
cache efficiency. Privileged release code constructs the permitted Firecracker release and checksum URLs and obtains the
checksum independently from the fixed official origin with a dedicated bounded HTTPS-only client and proxies disabled.
It hashes the complete archive body against that checksum before parsing it. If no body is supplied, root may fetch the
fixed archive itself. Root never opens a caller path or accepts a caller URL or checksum. The normal-user client may read
`MVM_ASSET_MIRROR`, but those streamed bytes are transport, never release authority.

Extraction occurs in a root-owned temporary directory with bounded compressed/decompressed input, a reviewed allowlist
for the complete upstream archive layout, and rejection of path traversal, symlinks, hardlinks, devices, sparse files,
duplicates, unexpected members, and size/count overflow. Only the exact Firecracker/Jailer pair is extracted. Their ELF
class and machine must match the selected architecture without executing downloaded code. The pair and strict root-owned
release manifest are fsynced and atomically renamed into the trusted store. A referenced release cannot be
force-replaced.

If upstream provides a stable verifiable signature or provenance mechanism, mvmctl should adopt it. Until then, the
fixed HTTPS origin and independently fetched checksum are an explicit supply-chain limitation, not a signature claim.

### Paths, process identity, and runtime state

Privileged code treats the normal CLI, arguments, environment, database, cache, manifests, and downloaded content as
untrusted. Managed filesystem objects are opened once through descriptor-relative no-symlink resolution and retained as
pinned descriptors through each operation. Validation followed by pathname reopen is prohibited.

`MVM_CACHE_DIR` remains the user-selected location for persistent user state and large VM artifacts. The privileged
protocol may carry exactly one typed managed-cache locator; it is a namespace locator, not authority for an individual
effect. The receiver requires one canonical absolute path, rejects dot components, NULs, symlinks, magic links, unsafe
owners/modes, and unsupported mount topology, and pins the directory before resolving any resource. All subsequent
inputs are typed IDs, closed format enums, and presence/access intent. A request never supplies a kernel, rootfs,
snapshot, volume, config, PID, socket, log, mount-target, or temporary-file path.

This exception is deliberately narrower than a generic path interface. Dropping custom cache roots would break the
documented large-filesystem use case, while a persistent root-owned path registry would still need safe pathname
resolution after reboot. Instead, each live instance record binds the pinned cache root's stable identity (device,
inode, and mount identity where available). A later operation fails closed if it cannot re-pin the same cache identity.
Descendants are opened relative to that pinned root with beneath/no-symlink/no-magic-link resolution and retained until
the privileged effect and rollback are complete.

Privileged-visible basenames are canonical and derived by the receiver. Volume storage is keyed by volume ID rather
than user-visible name; the VM rootfs uses fixed `rootfs.img`; kernel and image names use their canonical IDs plus only
enumerated representations; cloud-init, Firecracker runtime, and snapshot leaves use compile-time fixed names. The user
database may describe a resource, but it cannot choose the basename root will open. Snapshot create exposes the snapshot
read-write and writes its fixed `rootfs.img`, `memory`, and `vmstate` leaves. Restore exposes the snapshot read-only and
overlays the new VM's pinned `/rootfs` at `/snapshot/rootfs.img` inside that VM's private mount namespace; no persistent
phantom-rootfs symlink is created.

The v0.3 canonical privileged-visible layout is:

| Resource | Relative to the pinned managed cache root |
|---|---|
| VM directory | `vms/<vm-id>/` |
| VM rootfs | `vms/<vm-id>/rootfs.img` |
| Cloud-init ISO | `vms/<vm-id>/cloud-init.iso` |
| Firecracker runtime | Fixed `firecracker.json`, `firecracker.api.socket`, `firecracker.pid`, `firecracker.log`, `firecracker.console.log`, and `firecracker.metrics` leaves |
| Console/vsock runtime | Fixed `console.sock`, `console.pid`, and `vsock.sock` leaves |
| Volume | `volumes/<volume-id>.<raw|qcow2>` |
| Kernel | `kernels/<kernel-id>` |
| Snapshot | `snapshots/<snapshot-id>/{rootfs.img,memory,vmstate}` |
| Durable image | `images/<image-id>.zst` |
| Image staging | `images/staging/<image-id>/{source.raw,rootfs.img}` |

Warm-cache files are unprivileged accelerators and never root authority. Existing database path columns may remain as
derived display/local metadata during the clean break, but privileged handlers reconstruct these names from typed
identities and never authorize a path from SQLite.

Every privileged VM operation is serialized by a root-owned per-VM lock and authorized by a minimal root-owned instance
record. Process actions verify host boot ID, PID, start ticks, every real/effective/saved/filesystem UID and GID,
supplementary groups, expected cgroup membership, mount-namespace identity, and the pinned executable hash against the
exact release. Mutating operations require a pidfd, use `pidfd_send_signal`, and wait through that pidfd; a host without
the required pidfd support fails capability admission instead of falling back to a racy raw PID signal. PID reuse fails
closed. The receiver enforces its compiled group-drop policy and never permits a retained root supplementary group.

Release launch preparation acquires the `(version, architecture)` release-slot lock before resolving the root manifest.
The prepared value retains the verified manifest, pinned release directory, and pinned Firecracker/Jailer descriptors.
It supplies the full release identity to instance registration itself, then transfers the release lock into the launch
lease only after the owner-bound record is durable. The unprivileged caller never supplies executable hashes.

The launch path starts a blocked child in a new private mount namespace. Before the child can perform any externally
mutable effect, the parent pins its pidfd and namespace descriptor, records the complete identity, and retains the launch
lease. The parent then enters that namespace, makes mount propagation private, mounts descriptor-pinned resources, and
releases the child to exec the exact Jailer/Firecracker pair. Process and namespace allocation for a blocked child are
not considered an externally mutable launch effect; mounts, cgroup changes, links, and executable handoff are. Live
snapshot and volume operations reopen and verify `/proc/<pid>/ns/mnt` while the process is alive and enter only that
namespace. No persistent mount-namespace handle is retained after the operation, because doing so would retain every
mount after process exit. Cleanup never recursively traverses a tree that may contain a mount.

State is split by authority and lifetime:

| State | Location | Ownership/lifetime |
|---|---|---|
| User DB and VM artifacts | Existing `MVM_CACHE_DIR`, normally `~/.cache/mvmctl` | User-owned, persistent |
| Trusted release pairs and manifests | `/var/lib/mvmctl/binaries/<architecture>/<version>` | Root-owned, persistent |
| Privileged instance ownership/release records | `/var/lib/mvmctl/instances/<uid>` | Root-owned, persistent, minimal |
| Jailer chroot directories | `/var/lib/mvmctl/jailer` | Root-owned lifecycle state |
| Per-VM locks and launch handshakes | `/run/mvmctl/<uid>` | Root-owned, ephemeral; no persistent mount-namespace handles |
| Cgroups | `/sys/fs/cgroup/mvmctl/<vm-id>` | Kernel runtime state |
| `/run/mvm` | Inside the jail | Jail-visible bind mount, not host canonical state |

Moving durable user state out of `~/.cache/mvmctl` is a separate XDG migration decision. Full state must not move to
`/run` or `/var/run`.

### Failure and reconciliation contract

A root-owned instance record exists before the first externally mutable privileged launch side effect. A blocked
private-namespace child may exist first only while an inherited private handshake makes external mutation impossible.
Every post-spawn failure retains a live lifecycle handle and invokes a typed abort that verifies identity, terminates
the process through its pidfd, checks cgroup emptiness, and cleans namespace/chroot/instance state in order.

Startup and pre-launch reconciliation compare root instance records, processes, cgroups, and chroot directories. They do
not trust the user database as root authorization. Operations are idempotent and either converge to a known state or
return a precise error without deleting potentially mounted user data.

## Alternatives Considered

### Separate privileged helper binary

This provides the smallest executable trust boundary, but creates a second artifact and contradicts the product's
single-binary contract. Rejected in favor of early dispatch within one immutable system installation.

### User-owned CLI with a sudo command digest

Pinning a digest would make every user update require sudoers regeneration and would retain a user-owned path at the
root boundary. It also leaves the public CLI initialization path exposed to root execution. Rejected.

### Continue wildcard sudo for a root-owned public CLI

Root ownership fixes executable replacement but `mvm *` would still allow every public command to run as root and load
user state under root authority. Rejected. Only the reserved early dispatcher is allowed.

### Setuid or broad file capabilities

Both would expose privilege on every invocation and make the whole public CLI a security boundary. Required operations
also span mount, network, cgroup, and process privileges that do not reduce cleanly to a safe fixed capability set.
Rejected.

### Root daemon, polkit service, or systemd RPC

These can provide a strong authorization boundary but add a long-running service, protocol lifecycle, and additional
deployment artifacts. Rejected for the initial implementation; reconsider only if concurrency or policy needs exceed the
short-lived dispatcher model.

## Consequences

**Benefits:**

- The product retains one compiled and installed executable.
- Passwordless sudo can no longer execute user-selected program bytes or raw host-tool arguments.
- Root execution bypasses user configuration and the ordinary CLI initialization graph.
- The `mvm` group has a precise meaning: authorized mvmctl operator, not unrestricted root.
- Privileged state and user state have explicit owners and recovery responsibilities.

**Costs and limitations:**

- Installation and system-binary replacement require an explicit administrator step.
- Development binaries cannot perform privileged work unless a compatible system binary is installed.
- All raw privileged callsites must be replaced by typed operations before the final sudoers policy is enabled.
- The single executable contains more code than a dedicated helper, so early dispatch and import-graph isolation require
  strict tests and review.
- Multi-user ownership, crash recovery, release verification, namespace mounts, and firewall atomicity add implementation
  and L2 testing cost.

## Clean Installation and Release Gate

ADR-0005 remains a historical description of the pre-v0.3 policy. v0.3.0 provides no compatibility or automated
migration path for that policy, its user state, or its runtime resources. The implementation and release sequence is:

1. Add early privileged dispatch, the exact administrator bootstrap route, and negative tests while the final marker-only
   path is unreachable from sudoers.
2. Replace every supported privileged service/tool call with a typed operation.
3. On a clean host, install the trusted artifact as the root-owned system binary and atomically activate only the
   marker-scoped sudoers drop-in after `visudo` validation.
4. Run installation, replacement, privilege-abuse, multi-user, path-race, PID-reuse, crash-recovery, Jailer, cgroup,
   snapshot, hotplug, and firewall L2 tests through the installed binary.
5. Mark ADR-0005 superseded for v0.3.0, update all current-behavior documentation, and only then describe the new
   boundary as active.

No implementation phase is complete while documentation describes a different executable path, sudo policy, state
location, lifecycle guarantee, or known limitation.

## Related Decisions

- ADR-0002: One release artifact and one installed executable remain the product contract.
- ADR-0005: Pre-v0.3 group-based wildcard sudo policy; superseded when the v0.3.0 release gate passes.
- ADR-0012: Root boundary, namespace, cgroup, and recovery behavior require L2 verification.
- ADR-0015: Jailer remains canonical; its privilege-boundary and cleanup requirements are refined here.
