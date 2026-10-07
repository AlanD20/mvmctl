# Isolated VM Execution and Snapshot Authority

**Status:** Accepted; source integrated, release qualification incomplete
**Date:** 2026-09-19
**Supersedes:** The operator-as-Firecracker-execution-identity portion of [ADR-0016](0016-root-owned-single-binary-privileged-dispatch.md)

## Context

The operator is authorized to manage VMs through typed operations, but is not trusted to author root metadata or
Firecracker snapshot state. Running Firecracker with the operator's identity and exposing its control socket to that
operator does not establish exclusive snapshot capture: another process can resume execution or mutate drives while
the capture operation holds only mvmctl's advisory locks. Caller-writable disks and retained writable descriptors
also defeat a guarantee of consistent copies. Root ownership of the final snapshot alone does not establish origin.

This is an architectural authority gap, not a claim that a host exploit was reproduced. The user explicitly approved
the isolation prerequisite after previously pausing it for a separate design decision.

## Decision

- Keep one installed `mvm` binary and ordinary non-root operator commands. Privileged work remains closed typed
  operations through the authenticated dispatcher; no general API, command, or path forwarding is introduced.
- Separate durable operator ownership from an isolated per-VM execution identity. The latter must not be the operator
  or a shared execution identity across VMs. Allocation, collision handling, retention, and release are root authority.
  Use a root-generated, locked, non-login system account with no home creation, allocated through the host's existing
  account allocator. Do not invent a numeric UID pool or require a new operator-authored range configuration. Root
  intent precedes allocation; account identity and collisions are admitted before use. Accounts survive ordinary stop
  while backing still needs them. Removal requires exact account identity plus process/artifact absence, without forced
  account deletion or home removal. Ambiguous interrupted allocation is not automatically adopted.
- Broker Firecracker control through named root-authorized operations. The operator cannot directly control its API
  socket. Process/peer checks must compare execution identity without confusing it with operator authorization.
- Protect active writable backing before launch, without leaving caller-held writable aliases. Preserve explicit
  volume identity and sharing semantics rather than silently substituting disconnected copies for shared volumes.
  The implementation uses a root-controlled protected store within the managed cache, bound by a small root-owned
  anchor outside the caller's namespace. A missing or renamed store must not trigger silent reimport of stale source
  data. Named execution-identity access grants must be exact; filesystem inability to enforce them is an error, not
  permission to make the data generally accessible. Resize, removal, and guest provisioning must use authoritative
  protected backing after import rather than modifying the original caller-owned staging files. A raw source created by
  `mvm volume create` under the managed cache is mvm-owned, not an external staging file: final `volume rm` removes it
  only after root verifies its recorded inode, size, and change time. A missing source is harmless after protected
  deletion; a detected changed or replaced source is retained and removal fails closed. The protected backing and raw
  source share one durable removal lock and tombstone, so an interrupted deletion can be retried without reimporting
  source data.
- A root-owned live-volume mount transition joins only the VM's retained mount namespace. Its disposable worker
  unshares `CLONE_FS` before `setns`, because Go worker threads otherwise share filesystem attributes and Linux
  rejects mount-namespace entry. Root rechecks the retained process before and after the transition. A jailed
  namespace has no `/proc`; detach resolves the admitted volume basename relative to the pinned root-owned volume
  directory rather than using a `/proc/self/fd` pathname. The worker's OS thread is retired after unsharing, even
  when namespace restoration succeeds. Stopped-tree cleanup treats a durable `detached` journal as completed only
  after proving its jail target absent; it does not reopen a protected volume that `volume rm` may already have
  deleted. Transitional journals still reopen the exact retained backing to revoke access.
- `volume rm --force` does not hot-unplug a running guest. An attached volume may be removed only after root runtime
  observation proves its VM stopped; ambiguous or missing VM identity refuses removal. The same stopped-VM requirement
  applies to growth of an attached volume, without requiring detach. Unattached volumes may grow normally. Root
  protected-backing reference checks remain the final authority against a concurrent launch or retained writer.
- Require exclusive write ownership during capture. Refuse capture when another VM has writable access to any
  captured volume; coordinated multi-VM capture is not selected. Host writers and retained aliases must also be excluded.
- Capture one consistent state across Full memory/vmstate, rootfs, and attached data disks. Root metadata binds the
  exact release, machine/device configuration, network/IP/MAC, artifact identities, and guest-visible drive IDs.
- Use root-controlled snapshot storage under the managed cache and CLI-mediated removal. Restore uses independent
  writable disk copies while retaining immutable memory backing throughout the restored VM lifetime.
- Preserve the approved vmstate bound, exact trusted RAM extent, and no-new-storage-quota policy. Source identity,
  immutability, and consistency are additional requirements, not substitutes for those checks.

### VM-to-image import from authoritative backing

Accepted on 2026-10-07; implementation and installed-candidate qualification are pending. The user selected
running-VM import with pause/resume rather than requiring every source VM to be stopped.

The operator-visible staging rootfs is not the guest's current disk after protected-backing admission. Reading it
for VM-to-image import can discard guest changes, including packages installed by the reusable test-base builder.
Stopping the VM does not copy those changes back to staging. File import remains a separate caller-file operation.

- VM import uses a named typed root export operation against the current protected rootfs, not a caller-supplied
  protected path or a fallback to staging. Root admits the source generation, disk identity, exclusive writer
  ownership, and current runtime state under retained lifecycle, backing, and authenticated control authority.
- An originally running VM is paused for the disk copy and restored to running before accepting the output.
  Originally paused and stopped VMs retain their state. Copy failures and cancellation require bounded,
  cancellation-independent original-state recovery; extraction or optimization happens only after that recovery.
- The existing best-effort guest `sync` hint runs before acquiring root export locks. It is neither state authority
  nor a guarantee of application consistency. Pausing and exclusive disk access provide stable disk bytes, not
  application transaction atomicity or a Full memory snapshot.
- Durable root-owned export intent binds the exact source generation and state before a potentially applied pause
  or resume. After interruption, recovery re-admits that same source and obtains authoritative state before any
  mutation. It must not blindly replay control requests or resume a replacement or dead VM. Unresolved state is
  reported and prevents image publication; it is not represented as successful recovery.
- Pending recovery refuses competing control, capture, or rebind operations while allowing normal termination.
  Termination retires the pending operation without reviving its source. Independent output is accepted only after
  copy and state restoration are complete; failed output cleanup never deletes a changed or unrelated file.
- Temporary export copies live in root-controlled `/var/lib/mvmctl/vm-rootfs-export-data`, with operator read
  access and named typed cleanup. A caller-writable parent cannot provide exact deletion through a separate inode
  check and pathname unlink: the caller can replace the entry between them. Export publication and cleanup must
  therefore retain a root-controlled namespace. Protected VM disks and final images remain in the selected managed
  cache; export admission also accounts for space on the `/var/lib` filesystem, even when that is a different mount.
- Operator access includes admitted traversal of the root-owned `/var/lib/mvmctl` parent (`0711`), without directory
  listing or write permission. Admit and pin its ownership and identity before changing the mode. Private authority
  children remain `0700` and private files `0600`; this does not grant access to protected VM backing. Export-specific
  directory and file ACLs grant only the authenticated operator read/traversal access to independent copies.
- Image extraction reads rather than consumes the independent export. Named root cleanup releases it after extraction
  and before final-image publication, with bounded cancellation-independent cleanup on failure. Cleanup failures remain
  explicit. Ordinary removal of an image without an export stays local; an unprivileged presence hint is not deletion
  authority. Root cleanup derives ownership and source bindings from its own record, not a caller path or database row.
- If the importing CLI dies before creating a public image row, existing `image rm --force <exact-64-character-id>`
  provides export-only recovery after proving that no ordinary image selector matches. Names, prefixes, multiple
  selectors and database errors do not authorize that fallback. Root tombstones make retries idempotent. A live
  exporter is busy rather than forcibly killed; an already-open reader retains immutable bytes after unlink, so cleanup
  does not promise immediate physical reclamation. Releasing the copy does not clear uncertain source-state intent or
  claim VM recovery; report the source's pending recovery separately.
- This is an export-specific state-restoration contract. It does not change snapshot capture's existing failure
  behavior or introduce a generic privileged API, guest-command route, or arbitrary output-path capability.

Qualification must demonstrate current guest bytes surviving import and boot, running/paused/stopped state
preservation, stale staging exclusion, copy failure and cancellation recovery, interrupted root-operation recovery,
competing-control refusal, and exact output cleanup. A source inspection or a passing internal test alone does not
qualify the installed CLI or reusable-base builder.

### Protected kernel retirement and explicit reimport

Accepted on 2026-09-29; source integrated, installed-candidate qualification incomplete. Previously kernel removal
left an unused protected copy behind. Applying the mutable-disk tombstone unchanged would prevent reimport of the same
content-addressed kernel. The selected solution is a kernel-only, explicitly authorized retirement/import flow,
not an ordinary-launch fallback or a general resurrection mechanism.

- Named root kernel import and removal requests carry only the managed cache and kernel ID. Caller-supplied paths,
  digests, generations, ownership, and force flags cannot authorize protected storage changes.
- Removal requires both root-authoritative VM and snapshot reference sets to be empty. Before unlinking, root pins
  and hashes the protected bytes, rechecks identity, and durably records the digest. Referenced public force-removal
  remains a soft deletion that retains protected backing.
- Pull and explicit import authorize protected publication after the ID-derived caller leaf exists and before the
  public database projection is published. Launch and restore cannot reopen a retired kernel through caller state.
- A completed retirement may be explicitly reimported only from identical bytes, proven by hashing the new protected
  candidate against the retained root digest. Kernel-only intermediate phases retain candidate identity across retries;
  publication rechecks the digest and advances the backing generation once. Retained bindings compare generation
  when reopened, so a prior binding cannot select the new incarnation.
- A kernel that was never protected has no historical digest; that case has its own recorded retirement state.
  An incomplete initial import is not trusted retired content and refuses removal. Interrupted removal requires its
  pre-unlink digest rather than reconstructing authority from caller data.
- Existing owner, cache, store and logical-index bindings remain mandatory. Missing or renamed protected stores still
  fail closed. Rootfs, volume, cloud-init and snapshot tombstone behavior is unchanged.

Focused qualification must cover identical reimport and boot, changed-byte refusal, retained-reference refusal,
interrupted retirement/reimport, stale-generation rejection and unchanged mutable-backing anti-resurrection behavior.
The integrated implementation passed focused Go tests, the full root Jailer package and affected race checks in the
named disposable runner. Those internal-state checks do not prove an installed-CLI reimport-and-boot journey or
cross-filesystem power-loss recovery.

## Consequences

Launch, API consumers, stop/recovery, exec peer checks, disk lifecycle, host reset, and snapshot operations need a
coordinated cutover. Old execution/storage layouts are not adopted. Unresolved or partial authority fails closed.
Identity allocation and protected backing details must be implemented explicitly; this ADR does not claim that
changing an ownership field alone completes the boundary. The previous snapshot implementation pause is superseded
by the user's explicit isolation approval and resumption of implementation.

The implementation-only phase is historical; the user subsequently authorized Go and installed-candidate Python
qualification inside the named disposable runner. Focused full-snapshot and independent-volume source-crash journeys
passed there, as recorded in [the coverage matrix](../../tests/system/COVERAGE_MATRIX.md). Later installed-candidate
cases preserved one captured epoch across RAM, rootfs, and attached-volume bytes after source mutation/removal or
confirmed source crash. A real competing writable VM prevented capture; removing only that peer allowed the same
source and volume to capture successfully. These cases do not prove application transaction atomicity, exclusion of
all host writers, interruption during capture, broader fault consistency, or complete release qualification.
