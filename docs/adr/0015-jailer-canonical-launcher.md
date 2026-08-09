# Jailer Is the Canonical Firecracker Launcher

**Status:** Active
**Date:** 2026-08-09
**See also:** [Project Hosting with MicroVMs](../roadmaps/TENANT_MICROVM.md), [ADR-0005: Sudo Privilege Architecture](0005-sudo-privilege-architecture.md)

## Context

mvmctl originally downloaded both Firecracker and Jailer but launched Firecracker directly. Direct launch preserved console file descriptors and simple PID tracking while omitting Firecracker's recommended production process containment.

Firecracker's production guidance states that Jailer and Firecracker must have matching versions and that all trusted executable and chroot paths must not be writable by unprivileged users. Jailer requires elevated permissions to prepare the jail and cgroups, while mvmctl's established contract is that users invoke the CLI without root and privileged work is delegated internally through the constrained service-subprocess pattern.

The launch mechanism affects VM creation, restart, snapshot restore, console descriptors, API and vsock sockets, logs, volume paths, PID tracking, cleanup, and performance. A fallback from failed Jailer launch to direct Firecracker would silently remove the expected security and resource guarantees.

## Decision

Jailer will be the canonical launcher for every normal mvmctl VM lifecycle operation.

- Firecracker and Jailer are resolved and validated as one exact-version pair. Missing or mismatched pairs fail before launch.
- Jailer launch replaces direct Firecracker execution; there is no silent fallback and no generic launcher extension interface.
- The existing VM-domain spawner remains the caller-facing seam and owns jailed path translation, process tracking, socket probing, and cleanup.
- Root-requiring setup is encapsulated in a privileged `mvm run` subprocess, following the loopmount precedent. Users continue to invoke `mvm` without sudo.
- Executable and chroot paths trusted by Jailer are root-owned and not writable by the invoking user. The ordinary per-user mvmctl database and cache model remains unchanged.
- Every launch requires cgroup v2 with the CPU, memory, and PID controllers. mvmctl persists a typed resource envelope, derives fixed Jailer arguments internally, and fails the launch if post-start membership or values differ. Raw cgroup key/value input is not public.
- Jailer does not daemonize initially, preserving serial-console standard descriptors. New PID and network namespaces are deferred until their lifecycle and TAP implications are designed separately.
- VM resources are exposed inside the jail through validated paths without duplicating large rootfs and volume files. Mount cleanup is idempotent across failed starts, stop, remove, and host recovery.

## Consequences

**Benefits:**

- Every VM receives Firecracker's supported chroot and privilege-drop path.
- The canonical Jailer path enforces one finite cgroup-v2 envelope for every VM lifecycle path.
- One canonical launch path avoids a permanent direct-versus-jailed behavior matrix.
- VM create, start, reboot, and snapshot restore share the same security properties.

**Costs and limitations:**

- Existing host paths in Firecracker configuration must be translated into jail-visible paths while host controllers retain corresponding socket and artifact locations.
- Console, vsock, snapshots, hotplug volumes, PID handling, and partial-failure cleanup require full parity testing.
- Jailer adds fixed launch overhead and depends on the number of host mount points; benchmarks must track the regression budget.
- The initial memory maximum is guest-visible RAM plus a configurable 128 MiB VMM headroom; versioned L2 measurements must validate or revise that conservative default.
- Network namespace isolation remains future work because the current TAP belongs to a host bridge.
- Release pairs are reinstalled into the trusted store by `mvm bin pull`; hosts without cgroup v2 and the CPU, memory, and PID controllers cannot launch VMs.

## Related Decisions

- ADR-0002: Privileged Jailer work remains inside the single `mvm` binary through a service subcommand.
- ADR-0005: Normal-user operation and constrained internal privilege escalation remain mandatory.
- ADR-0012: Real Jailer/cgroup behavior requires L2 verification.
- ADR-0013: Socket readiness remains an internal probe, not a total launch timeout.
