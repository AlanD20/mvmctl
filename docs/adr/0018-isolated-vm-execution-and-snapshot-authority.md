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
- Broker Firecracker control through named root-authorized operations. The operator cannot directly control its API
  socket. Process/peer checks must compare execution identity without confusing it with operator authorization.
- Protect active writable backing before launch, without leaving caller-held writable aliases. Preserve explicit
  volume identity and sharing semantics rather than silently substituting disconnected copies for shared volumes.
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
