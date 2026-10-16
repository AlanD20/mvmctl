# v0.3.0 Clean-Break Security Release Plan

**Branch:** `0.3.0-release`

**Status (2026-10-16):** Implementation, local qualification and documentation complete. Authorized branch push,
remote CI and the user's release decision remain. v0.3.0 is unreleased.

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
