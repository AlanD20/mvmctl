# Isolated VM Execution and Snapshot Authority

**Status:** Accepted; implementation in progress, not qualified
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

## Consequences

Launch, API consumers, stop/recovery, exec peer checks, disk lifecycle, host reset, and snapshot operations need a
coordinated cutover. Old execution/storage layouts are not adopted. Unresolved or partial authority fails closed.
Identity allocation and protected backing details must be implemented explicitly; this ADR does not claim that
changing an ownership field alone completes the boundary. The previous snapshot implementation pause is superseded
by the user's explicit isolation approval and resumption of implementation.

No tests, builds, review passes, or runtime qualification are authorized in the current implementation-only pass.
Their later completion remains a release gate.
