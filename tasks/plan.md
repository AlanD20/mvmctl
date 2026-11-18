# v0.3.0 Clean-Break Security Release Plan

**Release branch:** `0.3.0-release`

**Feature branch:** `feature/guest-control-floating-ips`

**Status (2026-11-17):** The previously qualified baseline is complete. The user has expanded 0.3.0 to include
Guest Control, floating IPs and the HA Kubernetes example repair. These additions are in progress and not release-qualified;
the release is no longer ready to tag. Develop them on the feature branch, then integrate into `0.3.0-release`
after contract approval, implementation review and affected QA. Merge to `main`, tagging and publication remain on hold.

## Guest Control and floating IP scope

Guest Control provides one authenticated, versioned mechanism for guest-initiated operations, with named, typed
feature methods. Floating-IP association is the first feature. Both the operator CLI and the guest agent use the
same feature implementation with separate authorization: guest requests retain their delegated VM identity and
never inherit the operator's full authority. Do not introduce arbitrary CLI, shell, host-path or netlink execution.

Floating IPs are network-owned reservations separate from a VM's primary address. Transfers must fence the old
owner before authorizing the new one. The host remains authoritative even if the old guest is unresponsive.
The address need not be `.100`; allocation must respect the managed subnet and existing reservations.

### Delivery sequence

1. Propose ADRs for Guest Control and floating-IP ownership. Resolve connection identity and VM incarnation,
   delegation at privileged effects, host endpoint lifecycle, transfer recovery, traffic-policy semantics and
   automatic failover integration before implementing those contracts. A persistent host endpoint changes the
   current lifecycle architecture; keep the single installed binary and define reset, uninstall and upgrade behavior.
2. Implement typed floating-IP resource operations for the operator CLI: allocation, inspection, association,
   transfer, disassociation and removal, including root-authorized reservations and fencing.
3. Implement the shared control transport and delegated admission for those operations, with bounded requests,
   explicit errors, replay handling and revocation. Add no handlers for hypothetical future features.
4. Extend the guest agent to request the same operations and apply guest addresses only after host authorization.
   Define restart and restore behavior without treating guest claims as proof of identity.
5. Repair the HA example: three control-plane nodes, current supported binary requirements, explicit traffic and
   exec policies, current environment syntax, reliable helpers and host-authorized floating-IP failover.
6. Complete behavior-focused Go and Python system coverage, public Markdown and website documentation, and
   affected qualification in the named disposable runner. Review each slice once and verify correction deltas;
   run complete integration gates before declaring the expanded release ready.

Use isolated commits. Keep earlier qualification evidence as baseline evidence, not proof of these additions.

Contracts: [Guest Control authority](../docs/adr/0027-guest-control-authority.md) and
[floating-IP ownership](../docs/adr/0028-floating-ip-ownership.md). Reservation/delegation CRUD, exact traffic
policies, finite ownership effects, authenticated host session and guest gateway/address reconciliation are
implemented and reviewed. Version 1 environment declarations save resource, grant and policy identities before
effects. The retained worker, grant startup, start/resume recovery and lifecycle retirement are integrated;
focused checks and activation review pass. Stored permission is not session readiness.
Operator associations persist until explicit release, transfer or VM stop; guest leases remain finite.
Host CLI parity and acknowledged guest alias synchronization are being implemented. Full kernel packet fencing, live
control-path
bootstrap/expiry/failover and release qualification remain pending. Integration Go CI and the complete root Jailer
suite pass for the current production source. Thirteen new Python scenarios collect; this does not qualify their
runtime behavior. The public
[Guest Control guide](../docs/GUEST_CONTROL.md) and [environment reference](../docs/ENV_SPEC_REFERENCE.md)
describe the implemented surface; website publication remains held.

## Memory policy and sign-off

[ADR-0019](../docs/adr/0019-pressure-only-vm-memory-control.md) records the user's approved replacement of
finite VM-leaf hard limits: `memory.high` is guest RAM plus configured headroom; `memory.max=max` allows
exceeding that threshold. The earlier finite-envelope proposal and paused-work checklist are superseded.

Bounded installed-candidate Python saturation cases passed with and without swap. Separate guest-global
allocation exhaustion identified the guest allocator as the OOM victim, while the same Firecracker process
and fresh exec survived with unchanged observed host-side OOM counters. Later targeted observation confirmed
this distinction. These results satisfy the accepted bounded pressure-only behavior; they do not diagnose
the original incident or promise survival under arbitrary ancestor/host OOM. That limitation remains part
of the final release decision, not an unimplemented replacement policy.

## Implemented Baseline

The release provides a clean installation, one root-owned system binary with typed privileged dispatch,
isolated VM execution and protected backing, per-VM network namespaces, nftables-only enforcement, typed
traffic and exec policies, snapshot/volume lifecycle support, reconciliation and structured CLI results.
Legacy raw-sudo/public privileged services and obsolete service-access/remote-exec policy interfaces are retired.

Use [CONTEXT.md](../CONTEXT.md) for domain boundaries, state and lifecycle contracts. The detailed decisions remain
in their original records; this plan does not duplicate or replace them:

- [ADR-0016: privileged dispatch](../docs/adr/0016-root-owned-single-binary-privileged-dispatch.md).
- [ADR-0017: namespaces and typed policies](../docs/adr/0017-per-vm-network-namespaces-and-typed-policies.md).
- [ADR-0018: isolated execution and snapshot authority](../docs/adr/0018-isolated-vm-execution-and-snapshot-authority.md).
- [ADR-0019: pressure-only memory control](../docs/adr/0019-pressure-only-vm-memory-control.md).
- [ADR-0020: networkless placement](../docs/adr/0020-networkless-vm-placement.md).
- [ADR-0021: managed-neighbor invalidation](../docs/adr/0021-exact-managed-neighbor-invalidation.md).
- [ADR-0022: protected-cache retirement](../docs/adr/0022-explicit-protected-cache-retirement.md).
- [ADR-0023: protected authority layout](../docs/adr/0023-protected-branch-authority-layout.md).
- [ADR-0024: early guest-agent startup](../docs/adr/0024-early-openrc-agent-startup.md).
- [ADR-0025: immutable kernel lineage](../docs/adr/0025-immutable-kernel-cache-lineage.md).

## Accepted Qualification

The installed x86_64 candidate is `0.3.0-rc.kernel-details-ce5b0913`, produced from `ce5b0913`, SHA-256
`565be5a66432788f405e64337e6902a573da0dafe9efd0f4ef46480c5593060a`.
The approved source-bound evidence join passed **528/528**: 504 carried cases, 22 fresh kernel cases and two
stock host-reboot phases. Kernel/reboot source `a33e6623` and verifier `1122be66` retain their separate identities.
This is mixed-source acceptance, not one full latest-HEAD invocation. Go, root-Jailer, build and inner-install
gates remain accepted against unchanged relevant inputs.

The [coverage matrix](../tests/system/COVERAGE_MATRIX.md) records runtime coverage and provenance;
the [system-test execution guide](../docs/development/HOW_TO_RUN_SYSTEM_TESTS.md) defines evidence admission.
Keep original receipts and failures unchanged. Documentation-only edits do not require replaying successful
local qualification, builds or installations.

## Release Completion

1. Integrate the reviewed documentation cleanup while preserving unrelated working-tree changes.
2. Push the authorized release branch and observe remote CI for the resulting commit. The authoritative CI
   sequence is in [AGENTS.md](../AGENTS.md) and [the workflow](../.github/workflows/ci.yml);
   [run-go-ci-tests.sh](../scripts/run-go-ci-tests.sh) owns Go test selection.
3. Present the final diff, accepted local evidence, remote CI result and limitations for the user's release decision.
   Tagging/publication follows explicit approval and the [release guide](../docs/RELEASE.md).

Production and L0/L1 work belongs to the engineer; system QA and release verification to the qa-engineer;
plans, ADRs and documentation to the architect. The integration owner reviews actual diffs and preserves
candidate/controller separation. Follow [AGENTS.md](../AGENTS.md) and the
[parallel workflow](../docs/development/PARALLEL_AGENT_WORKFLOW.md) for scope, ownership and verification.

## Explicit Deferrals and Limits

- aarch64 runtime qualification is deferred because hardware is unavailable; it is not a v0.3.0 release blocker.
  Do not claim aarch64 runtime qualification from cross-build evidence.
- Pressure-only memory control permits the envelope to be exceeded. Bounded guest-saturation QA passed;
  the original memory incident remains unattributed and no guarantee covers every host/ancestor OOM.
- XDG state relocation, CNI, multiple VMMs, snapshot hot pools/device-mapper clones, network pre-allocation,
  resource batch grouping and root-volume boot remain outside this release.

The actionable checklist is [todo.md](todo.md). Historical implementation diaries remain in Git history.
