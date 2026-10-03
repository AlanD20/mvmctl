# mvmctl Runtime Architecture

This document describes the v0.3.0 runtime design for operators and contributors. v0.3.0 is still under
qualification: the architecture described here is not a claim that the full release test matrix or clean-host
installation gate has passed. See [RC_QA.md](RC_QA.md) for the release gates and
[CONTEXT.md](../CONTEXT.md) for package and domain ownership.

## One binary, two authority levels

The installed `/usr/local/bin/mvm` is one root-owned executable. An operator runs its public CLI without `sudo`.
`host install-system`, `host init`, and `host reset` are separate administrator operations; `host init` enrolls the
operator in the `mvm` group. The managed sudoers entry grants that group only the versioned, closed privileged entry
point of the root-owned binary. It does not grant the public CLI, a general shell, `ip`, `nft`, or raw host tools.

Host initialization supports KVM built directly into a kernel without loadable-module support. If the first
`/proc/modules` read reports that the inventory is absent, setup accepts the existing accessible `/dev/kvm` without
recording module loads. Missing or inaccessible KVM, cancellation and other inventory-read errors still fail setup;
the ordinary loadable-module path is unchanged. This does not make an incomplete administrator transaction enrolled.

An ordinary request follows `internal/cli/` → `pkg/api/` → the owning `internal/core/` domain. Public inputs are
validated in `pkg/api/inputs/`; `pkg/api/` coordinates multiple domains. When an operation requires root, its typed
client calls the early authenticated dispatcher in `internal/service/privileged/`. The receiver independently checks
caller authorization, resource identity, bounds, and effect-specific invariants. It does not accept a generic command
or arbitrary host path. Jailer, LoopMount, network, firewall, and snapshot effects have their own closed handlers.

The public `mvm run provision` and `mvm run jailer` commands were removed. Ordinary-user console and NoCloud
subprocesses still use the same installed binary. The embedded guest agent runs *inside* a VM for vsock execution and
file transfer; it is not a second host release artifact. See [ADR-0016](adr/0016-root-owned-single-binary-privileged-dispatch.md)
and [ADR-0018](adr/0018-isolated-vm-execution-and-snapshot-authority.md) for the trust boundary.

## State and process layout

| Location | Owner and purpose |
|---|---|
| `/var/lib/mvmctl` | Durable root authority: installation, trusted release, host admission, VM and network records, and Jailer state. |
| `/run/mvmctl/authority` | Root-only transient locks and handshakes; not durable truth. |
| `/run/mvmctl/runtime/<uid>/<vm-id>` | Ephemeral API/vsock/console sockets and display-only PID mirrors. |
| `MVM_CACHE_DIR` (normally `~/.cache/mvmctl`) | Operator configuration, SQLite projection, original images, managed disk inputs, and output artifacts. Root-protected active backing and snapshots are pinned under the selected cache but are not made operator-writable. |

The operator's SQLite rows and PID mirrors are not root authorization. Root effects pin the managed cache and their
resources by identity; a renamed cache, replaced file, stale process, or conflicting managed topology must not be
silently adopted. Listing and inspection use bounded root observation to distinguish live VMs from stale recorded
PIDs. A reboot does not imply the old Firecracker process survived. Cleanup and relaunch must reconcile retained
records before effectful reuse, and ambiguous state fails closed rather than being guessed away.

## VM launch and resource control

`mvm bin pull` installs an exact matching Firecracker/Jailer release in root-controlled storage. For a new install, a
local `MVM_ASSET_MIRROR` can supply the archive, but the root receiver still obtains and checks the official release
checksum sidecar; a local archive alone is not a trust assertion. v0.3.0 VM launch requires Firecracker/Jailer
**1.16.2**. Older pairs may be managed as artifacts but cannot launch VMs. This release fixes the upstream
pause/resume-vsock defect. Its snapshot format is incompatible in both directions with 1.16.0/1.16.1: create fresh
snapshots after upgrading; mvmctl does not convert older snapshot state. See the
[upstream release notes](https://github.com/firecracker-microvm/firecracker/releases/tag/v1.16.2).

Jailer is the only VM launcher. The authenticated root launch path prepares a fresh, pinned jail tree, admits the
selected kernel and disk resources, installs an isolated per-VM execution identity, and brokers the private
Firecracker API. Firecracker does not run as the operator, and the operator cannot open its control socket directly.
The launch path checks its process identity, cgroup membership, resource mounts, and network placement. There is no
direct-Firecracker fallback.

The VM leaf uses cgroup v2 for CPU, memory, swap, and PID policy. `memory.high` is guest RAM plus the configurable
VMM headroom; `memory.max` is `max`. This is pressure/reclaim control, **not a hard memory ceiling or an OOM-proof
guarantee**. A host or ancestor limit, host-wide pressure, or guest OOM can still terminate processes. `mvm vm inspect`
exposes requested and observed cgroup values and usage through root-brokered observation. See
[ADR-0019](adr/0019-pressure-only-vm-memory-control.md).

## Root filesystem provisioning and cloud-init

LoopMount is the default rootfs provisioner. It queues named operations through
`internal/lib/provisioner/loopmount/` and executes them via the closed, root-authorized LoopMount receiver. The
receiver works on admitted managed backing; it does not run a public root service with caller-supplied shell commands.
The typed operations include filesystem growth, hostname/DNS/SSH account setup, cloud-init injection, and automatic
deblob. The optional `guestfs_enabled` backend uses `guestfish`; selecting it does not bypass administrator host setup
or the VM launch authority. See [DEPENDENCIES.md](DEPENDENCIES.md) for required host tools.

Cloud-init delivery has four modes. `inject` writes seed data into the admitted rootfs before boot. `net` serves
NoCloud metadata through an ordinary-user service bound to the managed network's root-derived gateway on fixed TCP
**8000**; a custom port or bind collision is refused. `iso` creates a seed ISO using `cloud-localds` and requires
that optional package. `off` disables delivery and independently suppresses the supported guest cloud-init services,
including when `--skip-deblob` is set. LoopMount writes the fixed disabled marker and datasource configuration,
masks the four supported systemd units, and removes only the fixed cloud-init OpenRC activation links, including
Alpine's boot-local and default hotplug services. It does not change unrelated networking, service or cache settings.
A networkless VM cannot use `net`; it has no guest IP or
MAC and uses vsock rather than SSH for guest access. The applicable default and input conflicts are documented in
[REFERENCES.md](REFERENCES.md#mvm-vm).

## Networking, policy, and guest execution

Every managed networked VM gets its own root-created network namespace. A networkless VM gets an isolated
loopback-only placement. Managed same-network and cross-network VM traffic starts denied. The root firewall adapter
uses `github.com/google/nftables` to compile and submit fixed nftables generations to the kernel. It does not execute
or parse the `nft` CLI, use iptables, or offer a backend switch. Managed networking needs kernel nftables `inet` and
`bridge` support, connection tracking, and NAT capabilities; installing a command-line tool cannot provide missing
kernel support. Detected foreign or changed managed firewall state is refused rather than overwritten.

There are two public policy families:

| Policy | Grants | Does not grant |
|---|---|---|
| `mvm policy traffic` | Exact source network or source VM to destination VM TCP/UDP ports, after resolving current managed identities. | Vsock command execution or host commands. |
| `mvm policy exec` | Directional source-VM to destination-VM execution as an exact non-root target user, independent of network membership. | IP connectivity, reverse execution, VM-to-host execution, or destination root execution. |

The host-to-guest `mvm exec` and `mvm cp` paths use the embedded vsock agent. VM-to-VM execution additionally passes
through an authenticated host relay that binds the live source and destination identities and enforces the typed
exec grant. The relay bounds its command, output, duration, and concurrency and writes a command-redacted audit
record. See [ADR-0017](adr/0017-per-vm-network-namespaces-and-typed-policies.md).

## Volumes, snapshots, and recovery

Attached VM data volumes are raw-only; QCOW2 remains supported for image import/conversion, not as an attached
data-volume format. Active writable disks are root-protected rather than reused from an operator-writable staging
file. An attached volume can grow while its VM is stopped without detaching it. `volume rm --force` likewise
requires the attached VM stopped; it does not hot-unplug a running guest.

A full snapshot captures rootfs, memory/vmstate, and attached data volumes under exclusive write ownership. Its
artifacts are root-controlled under the selected cache, with CLI-mediated removal. Restore creates independent
writable disks while retaining immutable captured memory backing; it preserves the recorded release, drive IDs, and
managed network identity. A conflicting writer or unresolved artifact identity blocks capture or restore. The
approved vmstate limit is 10,000,000 bytes and captured RAM length comes from trusted memory state; v0.3.0 does not
invent a data-volume size or aggregate-storage quota. See [ADR-0018](adr/0018-isolated-vm-execution-and-snapshot-authority.md)
and [the resource-limit decision](improvements/volume-snapshot-resource-limits.md).

VM stop, remove, reboot, and restore use typed cleanup and reconciliation rather than deleting a PID file as proof
of process death. `host clean` is caller-scoped and refuses running or incompletely observed VMs; administrator
`host reset` is a separate, quiescence-checked operation and is not an old-state migration command. The exact
host-reboot/exec recovery and sustained memory-saturation journeys remain release QA gates, not guarantees inferred
from a single successful smoke test.

## Performance evidence

[`benchmarks/results.json`](../benchmarks/results.json) records an older measured workload. Its values predate the
v0.3.0 root broker, per-VM namespaces, protected backing, and direct-Go firewall path, so they must not be used as
v0.3.0 boot-time or throughput promises. Rebenchmark the release candidate before publishing performance numbers.
