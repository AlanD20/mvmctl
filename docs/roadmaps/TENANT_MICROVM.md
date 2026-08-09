# Project Hosting with MicroVMs

**Status:** Draft
**Date:** 2026-08-09
**Audience:** mvmctl contributors and operators using microVMs as isolated project-service runtimes.
**Related ADRs:** [ADR-0014: Routed Service-Access Policies](../adr/0014-routed-service-access-policies.md), [ADR-0015: Jailer Is the Canonical Firecracker Launcher](../adr/0015-jailer-canonical-launcher.md).

## 1. Goal and ownership boundary

mvmctl remains a low-level, normal-user VM manager. It does not own projects, tenants, releases, service placement, credentials, or application orchestration. An external orchestrator maps each environment to an mvmctl network and chooses whether tightly coupled services share one microVM or run in separate microVMs on that network.

Shared infrastructure runs in dedicated microVMs on separate mvmctl networks. Workload networks reach an exact shared service only through explicit protocol-and-port policies. The primary example is an application network reaching TCP port 5432 on a shared PostgreSQL VM; the model applies equally to other shared infrastructure.

The microVM is the process, filesystem, and resource-isolation unit. The environment network is the communication trust zone unless a future same-network VM policy narrows it.

## 2. Deployment model

An environment maps to one network. Small environments may run application, worker, scheduler, and cache processes in one Docker-capable microVM. Services move to separate microVMs when they need an independent failure domain, resource envelope, deployment cadence, or stronger isolation. Service VMs on one environment network communicate directly.

Shared infrastructure uses a separate network and dedicated microVMs with persistent volumes. The host routes between networks, but a default deny permits only persisted service-access policies. Database authentication, role separation, and application-aware backup remain the external orchestrator's responsibility.

Direct inbound ports are not required when the workload VM runs an outbound tunnel such as cloudflared. Host port forwarding remains a separate future capability.

## 3. Current capabilities and gaps

| Capability | Current state | Required change |
|---|---|---|
| Same-network communication | Works through one bridge; intentionally unfiltered | None for the environment trust-zone model |
| Cross-network routing | Host forwarding and ACCEPT policy permit it | Persist explicit service allows and default-deny other managed inter-network traffic |
| VM-to-host traffic | INPUT remains open except for explicit NoCloud allow rules | Preserve required host services, then deny other traffic from managed VM networks |
| Firecracker launch | Canonical Jailer launch and exact trusted release pairing implemented; L2 execution remains pending | Add cgroup-v2 resource enforcement and complete runner verification |
| Host resource enforcement | Guest vCPU and RAM are configured; no Jailer cgroup limits are applied | Apply and verify typed cgroup-v2 limits through Jailer |
| IP identity | Explicit IP assignment and collision-safe IPAM exist | Policies bind to network/VM IDs and resolve current addressing during reconciliation |
| Persistent storage | Volumes and snapshots exist | External orchestration supplies application-consistent backup and restore procedures |
| Docker workload | Guest images and kernels are customizable | Validate a versioned Docker-capable image/kernel template |

## 4. Implementation milestones

### M1 — Canonical jailed launch

- Resolve Firecracker and Jailer as an exact-version pair.
- Run privileged setup through a constrained `mvm run` service while preserving normal-user CLI operation.
- Install trusted launch executables at root-owned, non-user-writable paths.
- Translate kernel, rootfs, volume, socket, log, metrics, and snapshot paths into the jail.
- Preserve console descriptors, PID/start-time tracking, vsock, stop, and cleanup.
- Do not daemonize and do not enable PID or network namespaces in this milestone.

**Exit:** create, start, stop, reboot, console, exec, cp, volumes, and snapshot restore have L2 parity under Jailer, with no direct-launch fallback.

### M2 — Enforced resource envelope

- Require cgroup v2 and the necessary controllers during host readiness checks.
- Persist typed CPU, memory, swap, PID, and applicable I/O limits with each VM.
- Derive Jailer cgroup arguments internally; do not expose raw cgroup files or values.
- Measure Firecracker host-memory overhead before selecting the default memory headroom.
- Verify actual cgroup membership and values after launch; fail closed on mismatch.
- Surface requested, actual, and current resource state in VM inspection.

**Exit:** every normally launched VM has verified cgroup-v2 constraints and cleanup removes its cgroup.

### M3 — Routed service-access policies

- Persist source-network → destination-VM → protocol/port intent in the network domain.
- Resolve cross-domain identities in the API layer.
- Compile explicit allows into both existing IP-family firewall backends.
- Add deterministic connection-state, allow, and default-deny ordering in dedicated managed chains.
- Restrict VM-to-host INPUT while preserving NoCloud and explicitly approved host services.
- Reconcile desired rules after reboot, UFW reload, resource deletion, and `network sync`.

**Exit:** an environment network can reach an exact shared service port, cannot reach other ports or managed networks, retains internet egress, and cannot reach unauthorized host services.

### M4 — Workload proof and recovery

- Validate a Docker-capable guest kernel and versioned base image.
- Run one workload environment and one shared-infrastructure VM on separate networks.
- Verify reboot ordering, health probes, persistent volumes, and application-aware backups.
- Document cold restore onto a clean host.
- Benchmark jailed launch and firewall reconciliation overhead.

**Exit:** the external orchestrator can reproduce, stop, restore, and diagnose the topology without mvmctl owning project concepts.

### M5 — Optional same-network segmentation

- Add a typed source-VM → destination-VM service policy only when an environment needs it.
- Compile same-bridge policies to nftables bridge-family rules.
- Preserve the policy identity model and routed compiler from M3.

**Exit:** service-per-VM environments can narrow their internal trust zone without policy migration.

## 5. Administrator quality of life

The highest-value mvmctl additions are infrastructure diagnostics, not project orchestration:

1. Policy list, inspect, remove, sync, and connectivity-check commands.
2. VM inspection showing Jailer pair, jail path, cgroup enforcement, and current usage.
3. Host readiness output for Jailer, trusted binaries, cgroup v2, KVM, and required controllers.
4. Desired-versus-installed firewall rule diagnostics.
5. Idempotent cleanup of stale jails, mounts, cgroups, sockets, and derived rules.
6. Clear resource-exhaustion and policy-denial errors rather than generic launch/connectivity failures.

## 6. Maintenance risks and controls

| Risk | Control |
|---|---|
| Jailer path and mount complexity | One VM-domain path translator; no caller-built jailed paths |
| User-writable privileged executables | Root-owned verified Firecracker/Jailer installation |
| Memory-limit regressions after Firecracker upgrades | Versioned benchmarks and explicit overhead setting |
| Firewall rule ordering | Dedicated managed chains and deterministic full reconciliation |
| Policy drift after UFW reload | Idempotent sync and desired/actual inspection |
| VM proliferation | External orchestrator uses hybrid placement rather than one VM per trivial process |
| Shared-service blast radius | Dedicated volume, capacity reserve, service-native auth, backups, and health checks |
| Subnet waste | External orchestrator allocates small environment subnets where appropriate |
| Troubleshooting layers | Typed policy checks, resource inspection, and lifecycle audit logs |

## 7. Non-goals

- Tenant or project entities in mvmctl.
- A root-owned mvmctl database, daemon, RBAC, billing, or web UI.
- Docker Compose orchestration inside mvmctl.
- Jailer network namespaces in the initial launch implementation.
- Live VM migration.
- Same-network security groups before a concrete environment requires them.
- Application-consistent database backup inside mvmctl.

## 8. References

- ADR-0002: Single Go binary and privileged service-subcommand pattern.
- ADR-0005: Normal-user sudo privilege architecture.
- ADR-0009: nftables/iptables mutual exclusion.
- ADR-0010: API layer as sole cross-domain orchestrator.
- ADR-0011: Typed input validation and resolution.
- ADR-0012: L0/L1/L2 verification requirements.
- [ADR-0014: Routed Service-Access Policies](../adr/0014-routed-service-access-policies.md).
- [ADR-0015: Jailer Is the Canonical Firecracker Launcher](../adr/0015-jailer-canonical-launcher.md).
- Firecracker production guidance: `docs/prod-host-setup.md` and `docs/jailer.md` in the Firecracker repository.
