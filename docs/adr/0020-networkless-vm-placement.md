# Networkless VM Placement with Isolated Namespace

**Status:** Accepted for v0.3.0; partially implemented, not qualified
**Date:** 2026-09-26
**Extends:** [ADR-0017](0017-per-vm-network-namespaces-and-typed-policies.md) and [ADR-0016](0016-root-owned-single-binary-privileged-dispatch.md)

## Context

ADR-0017 requires directional VM-to-VM exec to work without an IP network, but public VM creation currently resolves a
managed network even when the caller omits `--network`. The create API, Firecracker config, root launch wire, topology
transaction, restart, and snapshot paths all consume a nonempty network ID and guest address. Treating a nil network as
an exception at the input layer would either fail later or bypass the root-owned namespace and lifecycle authority.

The approved use case is an isolated VM that can use the authenticated vsock agent, including a directional exec grant,
without an IP data plane. It is not an implicit fallback when a default network is missing or unhealthy.

## Decision

- Add an explicit `mvm vm create --no-network` choice, with a corresponding closed public input field. Omission continues
  to mean the configured default managed network. Reject `--no-network` together with `--network`, `--ip`, `--mac`, or
  NoCloud NET mode before resource effects. The environment specification must carry the same explicit choice. A
  network named `none` remains an ordinary managed-network name, not a sentinel.
- Resolve placement into one typed managed-network or networkless variant. Do not use a nil `Network` pointer alone as
  authority to skip networking, and do not add raw namespace paths, interface names, or a generic topology callback.
  The root launch protocol admits exactly one variant. A managed variant requires the existing network ID, canonical
  IPv4, and MAC; a networkless variant forbids all three. Mixed or incomplete shapes fail before launch effects.
- Root creates and pins a fresh per-VM network namespace for the networkless variant, and Jailer joins that exact
  handle before executing Firecracker. It contains only loopback; there is no managed bridge, veth, TAP, guest NIC,
  data-plane route, NAT, NoCloud listener, or nftables VM endpoint. No process may fall back to the host network
  namespace. The namespace identity and placement variant are durable root authority for activation, status, cleanup,
  and recovery.
- The guest Firecracker configuration has no `network-interfaces` entries, kernel `ip=` argument, or cloud-init
  network-config. Vsock remains the management and VM-to-VM exec transport when configured; an exec policy cannot run
  without the vsock device and agent on both VMs. IP-based SSH, `mvm ssh`, host port access, any
  traffic policy involving that VM, and NoCloud NET are unavailable for this placement; they must fail explicitly
  rather than hang or infer connectivity.
- Persist the placement variant in the user VM row and root-owned lifecycle/snapshot authority. Start, reboot,
  reconciliation, and restore re-admit the same variant; none may invent a managed network or silently drop namespace
  isolation. Snapshot restore of a networkless VM captures no IP/MAC endpoint and allocates none. A networked snapshot
  cannot restore as networkless or vice versa.
- Directional exec policy remains independent of network placement. The authenticated relay, exact source and
  destination root identities, non-root target user, output/time bounds, and command-redacted audit remain mandatory
  for networkless pairs. This choice grants no implicit VM-to-VM exec policy.

## Authority and failure behavior

The root dispatcher selects the closed placement branch only after authenticating the operator and pinning the VM
launch authority. A networkless namespace has a distinct typed lease and durable state, not a fabricated network ID or
an empty managed-topology record. Root verifies its namespace inode, loopback-only contents, process membership, and
absence of mvm-owned host endpoint effects for that VM before activation and on same-boot recovery. Prior-boot recovery
verifies the recorded process and ephemeral namespace are gone before retiring authority. Ambiguous, changed, or
foreign state fails closed and retains retryable cleanup authority. Cleanup removes only the exact recorded namespace
after process and socket teardown; it never guesses by name or removes a managed network.

The existing managed-network launch path is unchanged. A failed networkless launch cannot fall back to managed
placement, and a missing default network cannot trigger networkless placement.

## Alternatives considered

- **Omit `--network` to mean networkless:** rejected because omission already selects the default managed network and
  would make configuration changes silently alter isolation.
- **Run without `--netns`:** rejected because it puts the jailed process in the host network namespace, even if no
  Firecracker NIC is configured.
- **Use a dummy managed network ID or loopback TAP:** rejected because it would create false IP-policy and cleanup
  authority, and could expose an unintended data plane.

## Implementation and qualification boundary

Implement this as a coordinated typed cutover across public input/CLI, user VM persistence, Firecracker configuration,
the root launch/namespace protocol, start/reboot/recovery, and snapshot capture/restore. The Firecracker config module
now accepts an explicit networkless placement and emits no NIC or kernel `ip=` argument, while retaining vsock and
rejecting managed endpoint fields, NoCloud NET mode, and caller-supplied `ip=` or `ds=` boot arguments. Generated
offline NoCloud arguments are limited to the fixed local ISO or injected-seed form; the old duplicated `ds=ds=` inject
argument was corrected. Offline cloud-init seed generation no longer requires a fabricated IP/gateway or writes a
network configuration. The fresh version-8 user VM schema persists explicit placement, retains the managed network
foreign key, and stores SQL
`NULL` rather than a fabricated network ID for networkless rows. Public create now selects networkless placement only
with explicit `--no-network`; omission continues to select the managed default. Its resolver rejects conflicting
endpoint and NoCloud NET fields, avoids default-network lookup for the explicit variant, and does not allocate a
TAP, MAC, or IP lease. Relaunch reconstructs and retains the persisted variant, rejecting malformed networkless state
before reboot stops the VM. Public SSH rejects networkless placement with a vsock-exec direction.
The public root launch wire requires an explicit managed or networkless tag and rejects omitted, unknown, or mixed
endpoint variants before effects.
The strict schema-5 instance codec rejects older-schema records, including cleaned tombstones. Separately, the private
root path now carries a typed networkless namespace lease through Jailer launch, activation, cleanup, and same-/prior-
boot recovery. Placement is sealed in the protected device and instance records; the managed topology journal and
lock order are unchanged. Snapshot capture and restore now retain the networkless placement without allocating an
endpoint. A focused installed-candidate journey in the named disposable runner passed full capture and restore with
an attached raw volume, independent restored bytes, no restored IP/MAC, post-restore vsock exec, and checked teardown.
Snapshot producer memory is staged in the admitted disk-backed jail root rather than the bounded `/run` tmpfs; stopped
jail cleanup accepts only validated capture directories and producer artifacts. Public create and relaunch are
source-wired; focused create/stop/start/host-to-guest-vsock-exec smoke also passed in that runner.
Preserve current networked behavior until both branches have complete lifecycle ownership.

Go tests should prove closed input combinations, exact wire-shape rejection, no-NIC Firecracker config, placement
round-trip, and fail-closed root effects. Installed-candidate Python tests in the approved runner must prove creation,
namespace isolation, absent IP/TAP/host exposure, host-to-VM vsock exec, same-/cross-/no-network directional VM-to-VM
exec authorization, stop/start/reboot/recovery, and snapshot restore. The runner kernel's missing bridge-family
nftables capability still blocks networked comparisons; passing isolated networkless cases cannot qualify the whole
placement and policy matrix. Focused Go config, persistence, input, namespace, and Jailer package tests passed in the
named runner. The focused snapshot journey does not establish full Python system qualification, networked comparison,
VM-to-VM authorization, host-reboot recovery, or release readiness.
