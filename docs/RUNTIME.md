# mvmctl Runtime Architecture

This document describes the v0.3.0 runtime design for operators and contributors. v0.3.0 is still under
qualification: the architecture described here is not a claim that the full release test matrix or clean-host
installation gate has passed. See [RC_QA.md](RC_QA.md) for the release gates and
[CONTEXT.md](../CONTEXT.md) for package and domain ownership.

v0.3.0 requires a clean installation and fresh managed state. It does not adopt or migrate old user,
runtime, or protected-storage layouts.

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
| `/var/lib/mvmctl/vm-rootfs-export-data/<operator-uid>` | Root-controlled independent disk copies for VM-to-image import, with operator read access and typed cleanup. Requires space on this filesystem as well as the managed cache; see [VM image import](REFERENCES.md#mvm-image). |
| `/run/mvmctl/authority` | Root-only transient locks and handshakes; not durable truth. |
| `/run/mvmctl/runtime/<uid>/<vm-id>` | Caller-facing console endpoints and display-only PID mirrors. |
| `/run/mvmctl/vmm-runtime/<uid>/<vm-id>` | Private Firecracker API and vsock sockets owned by the isolated VM execution identity; mounted at `/vmm-runtime` inside the jail. |
| `MVM_CACHE_DIR` (normally `~/.cache/mvmctl`) | SQLite settings/state projection, original images, managed disk inputs, and output artifacts. Root-protected active backing and snapshots are pinned under the selected cache but are not made operator-writable. |
| `MVM_CONFIG_DIR` (normally `~/.config/mvmctl`) | Operator configuration files, including SSH keys under `keys/`; separate from the managed cache and root authority. |

The operator's SQLite rows and PID mirrors are not root authorization. Root effects pin the managed cache and their
resources by identity; a renamed cache, replaced file, stale process, or conflicting managed topology must not be
silently adopted. Listing and inspection use bounded root observation to distinguish live VMs from stale recorded
PIDs. A reboot does not imply the old Firecracker process survived. Cleanup and relaunch must reconcile retained
records before effectful reuse, and ambiguous state fails closed rather than being guessed away.

Permanent snapshot locks and volume-source removal receipts live in the root anchor outside the managed cache.
Snapshot artifacts and temporary raw-volume capture stay on the selected cache filesystem. Typed cache cleanup
can retire admitted terminal data branches while preserving those permanent authority records; it refuses held
operations, retained backing or restore references, and unknown or old layouts. See
[ADR-0023](adr/0023-protected-branch-authority-layout.md).

## VM launch and resource control

`mvm bin pull` installs an exact matching Firecracker/Jailer release in root-controlled storage. For a new install, a
local `MVM_ASSET_MIRROR` can supply the archive, but the root receiver still obtains and checks the official release
checksum sidecar; a local archive alone is not a trust assertion. The approved v0.3.0 launch contract requires the
exact Firecracker/Jailer **1.17.0** pair, the latest stable upstream release selected for this dependency update.
Affected installed-candidate runtime qualification remains pending. Older audited pairs may be managed as artifacts
but cannot launch VMs. See the [1.17.0 release notes](https://github.com/firecracker-microvm/firecracker/releases/tag/v1.17.0).

Release updates select the latest stable official matching pair, review its complete archive and runtime contract,
and qualify the affected behavior before release. The deployed contract remains an exact version pin; it does not
automatically advance with upstream releases or admit an unreviewed archive profile or minimum-version range.

The earlier 1.16.2 update fixed the upstream 1.16.1 pause/resume-vsock defect and introduced a snapshot format
incompatible in both directions with 1.16.0/1.16.1; see the
[1.16.2 release notes](https://github.com/firecracker-microvm/firecracker/releases/tag/v1.16.2).
Create fresh 1.17.0 snapshots and prove restore with the matching 1.17.0 pair. Earlier snapshot results do not qualify
this pair, cross-version compatibility is not asserted, and mvmctl neither converts state nor rewrites captured
release identities.

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

The host-to-guest `mvm exec` and `mvm cp` paths use the embedded vsock agent. Host-originated `mvm exec` defaults
to the guest's root user; this does not grant VM-to-VM root execution. VM-to-VM execution additionally passes
through an authenticated host relay that binds the live source and destination identities and enforces the typed
exec grant. The relay bounds its command, output, duration, and concurrency and writes a command-redacted audit
record. See [ADR-0017](adr/0017-per-vm-network-namespaces-and-typed-policies.md).

## Volumes, snapshots, and recovery

Attached VM data volumes are raw-only; QCOW2 remains supported for image import/conversion, not as an attached
data-volume format. Active writable disks are root-protected rather than reused from an operator-writable staging
file. An attached volume can grow while its VM is stopped without detaching it. `volume rm --force` likewise
requires the attached VM stopped; it does not hot-unplug a running guest.

The attached-volume count is derived from the selected release, architecture, transport, and complete device set.
MMIO admission subtracts VMGenID, VMClock, rootfs, network, optional cloud-init and vsock, and the applicable ARM
RTC/serial reservations from the interrupt pool. There is no uniform 13-volume product ceiling. PCI uses separate
slot and MSI-X accounting. These admission checks do not qualify maximum device counts; aarch64 runtime
qualification remains deferred.

`volume attach` on a Running VM succeeds only after the authenticated guest agent observes the exact
root-derived disk serial/capacity bound to `virtio_blk`, followed by an unchanged root attachment identity.
On a Paused VM it stages the attachment and returns a retryable deferred-readiness error: resume the VM and
retry the same attach. Stopped VMs record the attachment for their next start. A readiness failure does not
undo a known-applied attachment or make its volume available; inspect the state and retry the same operation.
Installed-candidate running and paused/resume-retry readiness journeys passed with usable guest I/O; see the
[coverage matrix](../tests/system/COVERAGE_MATRIX.md) for the accepted source and candidate bindings.
It detects an unusable attachment; it cannot repair the PCI BAR relocation failure observed on Firecracker 1.16.2.
The approved 1.17.0 dependency update includes upstream
[BAR/MMIO relocation changes](https://github.com/firecracker-microvm/firecracker/blob/v1.17.0/src/vmm/src/devices/virtio/transport/pci/device.rs#L657-L715)
selected to address that failure. Same-volume live detach/reattach passed on the current 1.17.0 project candidate;
these bounded journeys do not establish compatibility with every guest or complete release qualification.

A full snapshot captures rootfs, memory/vmstate, and attached data volumes under exclusive write ownership. Its
artifacts are root-controlled under the selected cache, with CLI-mediated removal. Restore creates independent
writable disks while retaining immutable captured memory backing; it preserves the recorded release, drive IDs, and
managed network identity. A conflicting writer or unresolved artifact identity blocks capture or restore. The
approved limit for the complete `vmstate` file, including its CRC, is 10,000,000 bytes. The memory artifact must
have exactly trusted captured RAM in MiB × 1,048,576 bytes of logical length, including sparse extents. v0.3.0 adds
no data-volume size or aggregate-storage quota and reserves no physical storage. See
[ADR-0018](adr/0018-isolated-vm-execution-and-snapshot-authority.md)
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
