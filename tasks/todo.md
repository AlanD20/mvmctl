# v0.3.0 Release Tasks

This checklist implements `tasks/plan.md`. Go production and L0/L1 work belongs to the `engineer`; Python
`tests/system/` and release verification belongs to the `qa-engineer`; ADRs, plans, and documentation belong to the
`architect`. The architect reviews the actual diff after every implementation slice.

## Accepted memory policy

- [x] Implement the explicitly approved pressure-only policy from [ADR-0019](../docs/adr/0019-pressure-only-vm-memory-control.md).
- [x] Verify literal unlimited `memory.max`, configured `memory.high`, swap/CPU/PID controls and authenticated readback.
- [x] Exercise bounded sustained guest saturation with and without swap, preserving VMM identity and responsiveness.
- [x] Distinguish an identified guest-global OOM victim from a VMM kill, then verify fresh exec and allocation.
- [x] Document that host/ancestor OOM remains possible and that the original incident is still unattributed.

The obsolete finite-envelope proposal is superseded by the user's accepted ADR-0019 decision. These checked
items describe bounded verified behavior, not attribution or OOM immunity. The original incident remains
an explicit limitation for final release approval; a recurrence requires fresh process/counter/kernel evidence.

## Accepted Work (2026-10-16)

- [x] Integrate the approved security/lifecycle implementation and recorded decisions linked from [plan.md](plan.md).
- [x] Accept Go CI, root-Jailer, build and inner-install gates against unchanged relevant candidate inputs.
- [x] Accept candidate565's approved mixed-source runtime join: 528/528, comprising 504 source-bound carried cases,
  22 fresh kernel cases and two stock host-reboot phases. Preserve actual execution sources and original receipts.
- [x] Reconcile final operator/QA documentation through `8690e916`; preserve qualification limits and provenance.

Candidate identity and detailed evidence are in the
[coverage matrix](../tests/system/COVERAGE_MATRIX.md) and
[system-test execution guide](../docs/development/HOW_TO_RUN_SYSTEM_TESTS.md).
Successful local QA, builds and installations need no replay for documentation-only changes.

## Final integration before tagging

- [x] Integrate operator documentation and accepted decision updates, preserving unrelated workspace changes.
- [x] Implement and verify the post-baseline host-policy, image-import recovery, networkless launch and neighbor fixes.
- [ ] Run the complete Go CI sequence on the final clean integration checkout and retain the root-Jailer result.
- [ ] Update [release PR #7](https://github.com/AlanD20/mvmctl/pull/7), push the release branch and confirm its current CI.
- [ ] Present the exact source, local qualification, current remote CI and known limits for final release approval.

The live PR checklist records completed remote CI without treating an older successful run as current evidence.
Tagging, publication and the AUR metadata update follow approval and [RELEASE.md](../docs/RELEASE.md).

## Deferred, Not Release Blockers

- aarch64 runtime qualification awaits available hardware; cross-builds do not qualify runtime support.
- The other explicit feature deferrals and memory-policy limits remain in [plan.md](plan.md).

Historical unchecked implementation tasks are superseded by the accepted work above. Use Git history for prior
progress records and the linked ADRs for design contracts; do not restart obsolete implementation or QA diaries.
