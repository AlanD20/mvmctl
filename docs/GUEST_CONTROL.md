# Guest Control and floating IPs

A floating IP reserves one private IPv4 address in a managed network. A delegated guest can claim that address
for itself, renew its finite lease and release it. Clients keep the same destination when ownership moves.
The VM's primary address stays separate.

**Development feature:** delegated guest control and persistent host assignments are implemented. Runtime tests
cover association, transfer, release, packet fencing, expiry, ordinary VM lifecycle changes and environment
ownership. Deliberate worker-stall and actual host-reboot qualification remain pending. The Kubernetes
example has not been qualified end to end.
A stored grant alone does not establish a working Guest Control session.

## Reserve an address

For an existing managed network named `cluster`, reserve an available address and inspect it:

```bash
mvm floating-ip create api --network cluster
mvm floating-ip inspect api --network cluster --json
mvm floating-ip ls --network cluster --long
```

Use `--ip 10.25.0.100` at creation to request that exact usable address when it belongs to the network's subnet.
Allocation excludes the gateway, network and broadcast addresses, primary VM reservations (including stopped VMs),
and other floating IPs. Names are unique within a network. Inspection returns the immutable resource ID and current
generation; reuse of a name or address after removal creates a different resource.

## Assign ownership from the host

For eligible running VMs `controller-1` and `controller-2` in `cluster`, associate the unowned reservation,
explicitly transfer it, or release it:

```bash
mvm floating-ip associate api controller-1 --network cluster
mvm floating-ip transfer api controller-2 --network cluster
mvm floating-ip release api --network cluster
```

Host assignments persist until explicit release, transfer or VM stop. They require neither a delegation nor a
renewal loop. Guest associations and renewals always use finite leases. Association cannot replace an owner;
transfer fences the old owner before admitting the new one. `release` also accepts the alias `disassociate`.

All three commands accept optional `--generation N` to bind a previous observation. When omitted, the command
observes the root generation immediately before its compare-and-swap; a concurrent change can still conflict.
Fresh `mvm floating-ip inspect api --network cluster --json` reports `phase`, `lifetime`, `owner_origin`,
`owner_vm_id`, `authority_state`, `lease_remaining_ms` and `alias_synchronization` alongside identity/generation.
A persistent lifetime with zero remaining lease is not expired. Interpret lifetime and current authority together.
For current, stable ownership, inspection observes kernel authority. Unassociated, retired or prior-boot records
report `authority_state: unobserved`; this is not a kernel-absence claim. A successful release reports `absent`
after completing its fencing checks.

Association and transfer complete only after the exact target guest acknowledges its alias. An agent or bounded
wait failure can occur after kernel ownership commits; it does not promise rollback. Inspect fresh ownership
before deciding whether to retry. Inspection reports alias synchronization as `unobserved`; it does not contact
the guest or establish its current readiness.

The agent announces newly installed ownership on the local network to refresh peers' cached MAC addresses.
Announcements do not grant access or replace host fencing. A failed announcement prevents a success
acknowledgment; a fresh authorized retry can finish it without extending a finite lease on its own.

## Delegate exact methods

For an eligible running VM named `controller-1` in `cluster`, grant each required method separately:

```bash
mvm floating-ip delegation grant api --network cluster --vm controller-1 --method inspect
mvm floating-ip delegation grant api --network cluster --vm controller-1 --method associate --lease 10s
mvm floating-ip delegation grant api --network cluster --vm controller-1 --method renew --lease 10s
mvm floating-ip delegation grant api --network cluster --vm controller-1 --method release
mvm floating-ip delegation ls api --network cluster --long
```

`10s` is an example, not a default. Association and renewal grants require an explicit positive whole-second
Go duration, such as `10s` or `1m`. Inspect and release grants forbid a lease. Inspecting or storing a grant does
not claim the address, renew ownership or prove worker readiness.

Each grant binds one resource, method and current VM incarnation under the same operator and network. A guest
cannot allocate resources, create grants, select another owner or call arbitrary host commands. Restart or restore
does not transfer a grant to a new incarnation. Use the complete lowercase grant ID from inspection to inspect or
revoke a delegation; prefixes and names are not accepted for those commands. Revocation fences ownership sustained
by that exact grant and can finish after its VM stops. Revoking an unrelated grant does not fence an independent
operator assignment. A current release grant for the owning VM can explicitly release its persistent assignment.
A revoked ID cannot be reactivated.

## Request ownership from the guest

Guest root uses `mvm-agent control floating-ip`. The local socket is `/run/mvm-agent/control.sock`, accessible only
to root. Every application with guest-root access can exercise the VM's grants; this is not isolation between
applications inside one guest. Do not broaden socket permissions to make an ordinary guest user a controller.

The command forms below use `RESOURCE_ID` for the full immutable resource ID obtained on the host and `N` for
the generation returned by a fresh inspect. They are syntax, not literal arguments:

```text
mvm-agent control floating-ip inspect RESOURCE_ID
mvm-agent control floating-ip associate --generation N RESOURCE_ID
mvm-agent control floating-ip renew --generation N RESOURCE_ID
mvm-agent control floating-ip release --generation N RESOURCE_ID
```

Results are JSON with `resource_id`, `ipv4`, `generation`, `ownership` (`unassociated`, `self` or `other`),
`lifetime` and `lease_remaining_ms`. `finite` ownership expires; `persistent` with zero remaining milliseconds does
not. Remaining finite time is measured at the host; response delivery does not restart it. Guest association
can claim an unowned address but cannot evict a healthy owner. Renewal retains the generation and requires the
exact finite owner, incarnation and grant. It cannot convert or extend persistent ownership. Ownership changes
advance the generation. A paused guest cannot renew.

Inspect before each decision. Renew only while the local service is healthy, and stop renewal when it is not.
A generation conflict requires another inspection. A timeout or `outcome_unknown` means the effect may have
committed: inspect before deciding on another mutation. Blindly replaying an old request is not recovery.

The agent applies host-authorized aliases through the authenticated session. For finite ownership, it tracks
its own alias for cleanup on release, local expiry, session loss and restart. Inspect alone does not install an
address or extend its finite lifetime. Guest cleanup can fail or be delayed, so the host kernel enforces finite expiry
independently of both the agent and host worker. Old owners lose source IPv4/ARP authority and delivery to the
floating address before a new owner is admitted. Gratuitous ARP and removal of a guest alias are not fencing proof.

Guest Control uses a dedicated, authenticated vsock session bound to the live VMM and launch incarnation.
It needs neither SSH nor a guest-to-host TCP firewall exception. It grants no VM-to-VM command execution permission.

## Allow service traffic separately

Delegating ownership grants no client access. For existing `client`, `cluster` and `api` resources, allow TCP 6443
to the floating address from one VM or, alternatively, every VM in the selected source network:

```bash
mvm policy traffic create --from-vm client --to-floating-ip api --to-network cluster --protocol tcp --port 6443
# Alternative source scope:
mvm policy traffic create --from-network cluster --to-floating-ip api --to-network cluster --protocol tcp --port 6443
```

This policy follows the exact floating resource across owners, stays dormant while unassociated, and does not
admit the owner's primary address or its other aliases. A `--to-vm` policy instead follows that VM's admitted
addresses. Policy permission and ownership are separate requirements. Host-originated access still depends on
host routing and firewall rules, and floating-address delivery fencing still applies.

## Define and remove an environment

Version `"1"` environment specs support [`floating_ip`, `floating_ip_grant` and floating-IP traffic destinations](ENV_SPEC_REFERENCE.md#floating_ip).
Typed references resolve saved identities; command strings do not interpolate outputs. Preserve environment state:
retries and teardown use the exact saved resource, grant and policy ownership, including after uncertain effects.

Stop lease controllers and release ownership before removing their resources. Revoke obsolete grants, remove exact
service policies, then remove the reservation with `mvm floating-ip rm api --network cluster`. `--force` skips its
confirmation, not ownership checks. Ordinary network removal requires floating reservations to be removed first.
VM stop, deletion and restore fence secondary authority. Host reboot does not restore either kind of ownership.

The [Kubernetes HA example](../examples/workflows/simple-k8s-cluster-ha/README.md) uses three control-plane nodes
and an application health loop. The loop manages finite ownership only and leaves manual persistent assignments
alone. Kernel leases enforce finite expiry. Its example
lease is not a product failover guarantee.
