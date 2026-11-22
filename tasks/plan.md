# v0.3.0 Clean-Break Security Release Plan

**Release branch:** `0.3.0-release`

**Feature branch:** `feature/guest-control-floating-ips`

**Status (2026-11-22):** The previously qualified baseline is complete. The user has expanded 0.3.0 to include
Guest Control, floating IPs and the HA Kubernetes example repair. Implementation, review and scoped feature
qualification are complete and prepared for integration into `0.3.0-release`. Integration-owner sign-off and
expanded release qualification remain pending. Merge to `main`, tagging and publication remain on hold.

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
   Qualification covers floating-IP association and disassociation on small VMs in the existing runner; running
   the Kubernetes cluster, testing quorum and provisioning a larger runner are explicitly outside this goal.
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
Host CLI parity, explicit lifetime authority and acknowledged guest alias synchronization are implemented and
reviewed. All 46 ordinary runtime cases (32 environment and 14 floating-IP) passed on QA source `0403ad4e`
with paired candidate SHA-256 `44e2147e11f413a95998c6b9a56e46f82fb979667f2c0fb557642f24f878cf1c`
from production source `4f490ff0`. No cases skipped or errored; all teardowns and full inventory cleanup passed
without intervention. The prescribed Go CI, complete root Jailer suite (923 passes and exactly two intentional
private-entrypoint skips), root agent alias suite (21 passes, no skips) and paired real-agent build passed.
Incremental real-agent restart/reconnect passes. After the startup and prior-boot recovery corrections, a fresh
actual host reboot passes PRE and POST with strict reports, owned cleanup and historical verification. Independent
VMM exit also passes: exact VMM and worker pidfds report exit before lifecycle cleanup. Deliberate worker-stall
packet fencing passes with unchanged 24-second leases and timing/traffic assertions, exact pidfd continuation
and preserved inventory. The explicit administrator transport pre-arms before any grant or association; no
persistent helper or grant is needed. Final integration Go CI and complete root Jailer/agent alias checks pass
on clean QA source `a84d8958`, including the production startup and prior-boot recovery corrections. The
feature is prepared for release-branch integration; integration-owner sign-off remains pending. No gate is waived.
The public
[Guest Control guide](../docs/GUEST_CONTROL.md) and [environment reference](../docs/ENV_SPEC_REFERENCE.md)
describe the implemented surface; website publication remains held.

Evidence: `.tmp/qa-guest-control-final-0403-nov22/runtime-verification.json` (SHA-256
`896b1f7c65bde77fcc7575b05e8d8ac0d09402739eaccfebd0d8aada662874c5`) and
`.tmp/guest-control-final-candidate-nov22/{paired-build-manifest,gate-summary}.json`.
Later proof: production source `f7666e5a`, paired candidate SHA-256
`0efe49b93ad9154e0bcaeec87a77421591eff7a87153c1c85136107cf3eadf1e`,
`.tmp/qa-host-reboot-recovery-f766-nov22/verification-summary.json` (PRE 37.339s, POST 83.588s), and
`.tmp/qa-vmm-exit-9c55-nov22/verification-summary.json` (QA source `9c55e34c`, unchanged candidate).
The VMM-exit check uses inner x86 BusyBox `reboot -f` with verified `reboot=k`; it observes VMM exit at 3.803s
and worker exit at 3.848s, independently of outer-runner reboot. These incremental results do not replay the
ordinary 46 cases or complete the expanded release qualification. Worker-stall proof on QA source `a84d8958`
with the same candidate: `.tmp/qa-worker-stall-prearmed-runtime-nov22/verification-summary.json`, SHA-256
`cca906ee1ff88402ff8219f4f0f18c0171f6f9ffc412abbbd001b30a548efa77`; one case passed in 98.86s,
with no skips, errors or deselections and successful canonical matrix/root-receipt verification. Final CI proof:
`.tmp/ci-final-guest-control-nov22/{summary,proof}.json`; every prescribed stage exits zero, complete root Jailer
has 923 top-level passes with the same two intentional private-entrypoint skips, and root alias/operator has
21 top-level passes with no skips. Source manifests match before/after; the installed candidate remains unchanged.

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
