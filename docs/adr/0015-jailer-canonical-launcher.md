# Jailer Is the Canonical Firecracker Launcher

**Status:** Active
**Date:** 2026-08-09
**See also:** [Project Hosting with MicroVMs](../roadmaps/TENANT_MICROVM.md), [ADR-0005: Sudo Privilege Architecture](0005-sudo-privilege-architecture.md)

## Context

mvmctl downloads both Firecracker and Jailer from the same release but currently launches Firecracker directly. Direct launch preserves console file descriptors and simple PID tracking, but it omits Firecracker's recommended production process containment: chroot, privilege drop, namespace isolation, cgroup placement, and process resource limits.

Firecracker's production guidance states that Jailer and Firecracker must have matching versions and that all trusted executable and chroot paths must not be writable by unprivileged users. Jailer requires elevated permissions to prepare the jail and cgroups, while mvmctl's established contract is that users invoke the CLI without root and privileged work is delegated internally through the constrained service-subprocess pattern.

The launch mechanism affects VM creation, restart, snapshot restore, console descriptors, API and vsock sockets, logs, volume paths, PID tracking, cleanup, and performance. A fallback from failed Jailer launch to direct Firecracker would silently remove the expected security and resource guarantees.

## Decision

Jailer will be the canonical launcher for every normal mvmctl VM lifecycle operation.

- Firecracker and Jailer are resolved and validated as one exact-version pair. Missing or mismatched pairs fail before launch.
- Jailer launch replaces direct Firecracker execution; there is no silent fallback and no generic launcher extension interface.
- The existing VM-domain spawner remains the caller-facing seam and owns jailed path translation, process tracking, socket probing, and cleanup.
- Root-requiring setup is encapsulated in a privileged `mvm run` subprocess, following the loopmount precedent. Users continue to invoke `mvm` without sudo.
- Executable and chroot paths trusted by Jailer are root-owned and not writable by the invoking user. The ordinary per-user mvmctl database and cache model remains unchanged.
- Jailer uses cgroup v2 and receives typed resource limits derived from the persisted VM resource specification. Raw cgroup key/value input is not public.
- Launch fails closed if required cgroup controllers are unavailable or post-spawn verification does not match requested limits.
- Jailer does not daemonize initially, preserving serial-console standard descriptors. New PID and network namespaces are deferred until their lifecycle and TAP implications are designed separately.
- VM resources are exposed inside the jail through validated paths without duplicating large rootfs and volume files. Mount and cgroup cleanup is idempotent across failed starts, stop, remove, and host recovery.

## Consequences

**Benefits:**

- Every VM receives Firecracker's supported chroot, privilege-drop, and cgroup containment path.
- CPU, memory, swap, PID, and applicable I/O constraints become enforceable host limits rather than advisory guest configuration.
- One canonical launch path avoids a permanent direct-versus-jailed behavior matrix.
- VM create, start, reboot, and snapshot restore share the same security properties.

**Costs and limitations:**

- Existing host paths in Firecracker configuration must be translated into jail-visible paths while host controllers retain corresponding socket and artifact locations.
- Console, vsock, snapshots, hotplug volumes, PID handling, and partial-failure cleanup require full parity testing.
- Jailer adds fixed launch overhead and depends on the number of host mount points; benchmarks must track the regression budget.
- Memory cgroup limits require measured VMM headroom above guest-visible RAM.
- Network namespace isolation remains future work because the current TAP belongs to a host bridge.
- Hosts must rerun initialization when trusted launch assets or cgroup-v2 prerequisites are introduced.

## Related Decisions

- ADR-0002: Privileged Jailer work remains inside the single `mvm` binary through a service subcommand.
- ADR-0005: Normal-user operation and constrained internal privilege escalation remain mandatory.
- ADR-0012: Real Jailer/cgroup behavior requires L2 verification.
- ADR-0013: Socket readiness remains an internal probe, not a total launch timeout.
