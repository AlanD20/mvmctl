# v0.3.0 Release Tasks

This checklist implements `tasks/plan.md`. Go production and L0/L1 work belongs to the `engineer`; Python
`tests/system/` and release verification belongs to the `qa-engineer`; ADRs, plans, and documentation belong to the
`architect`. The architect reviews the actual diff after every implementation slice.

## Guest Control and floating IP expansion (2026-11-17)

Work on `feature/guest-control-floating-ips`, then integrate into `0.3.0-release` after the gates below.
Earlier checked items describe the qualified baseline; they do not qualify this expansion.

- [ ] Approve Guest Control and floating-IP ADRs, including identity, delegation, endpoint lifecycle and failover.
- [x] Draft [Guest Control authority](../docs/adr/0027-guest-control-authority.md) and
  [floating-IP ownership](../docs/adr/0028-floating-ip-ownership.md) contracts, recording unresolved runtime details.
- [x] Implement strict typed Guest Control request framing, including duplicate/unknown-field refusal,
  exact integer decoding, bounds, cancellation checks and partial-write handling; verify with race tests and vet.
- [x] Implement pure floating-IP address selection against canonical network authority and supplied reservations;
  verify gateway, collision, exhaustion, corrupt-state and subnet boundaries in the named QA runner.
- [ ] Implement operator-facing floating-IP resources, reservation ownership, fencing and transfer recovery.
- [ ] Implement the authenticated control endpoint and typed delegated operations without operator impersonation.
- [ ] Implement agent access to the same operations and guest address reconciliation.
- [ ] Repair the HA Kubernetes example and its policies, helpers and three-controller topology.
- [ ] Complete affected Go and Python system tests in `mvmctl-030-test`, plus documentation and website updates.
- [ ] Review and integrate the feature into `0.3.0-release`; complete integration CI and release qualification.
- [ ] Obtain the user's separate authorization before merging the release PR, tagging or publishing.

The codec and address selector are unregistered foundations. No Guest Control session, persistent floating-IP
reservation or secondary-address firewall permission is active yet. Root reservation publication is the next
implementation slice; it must share existing primary-admission locks and participate in cleanup and inventory.

## Administrator uninstall (2026-11-14)

- [x] Add a confirmed root-run uninstall utility using typed per-operator cleanup and native host reset.
- [x] Admit terminal networkless history and committed empty prior-boot firewall history during reset, retaining
  current namespace/table absence checks and strict record binding.
- [x] Verify filesystem refusal/preservation tests and the affected Go packages inside the named QA runner.
- [x] Exercise a real running-VM removal, fresh reinstall with guest exec, and two-operator cleanup with custom
  cache/config paths; preserve external mirrors and original files.
- [x] Complete one independent security review and document installation/removal scope.

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
- [x] Complete the Go CI sequence and full root-Jailer package in the clean QA checkout (`49282ff8`); retain both results.
  The subsequent closeout changes only ignore generated coverage and update this checklist; current remote CI
  and candidate identity are recorded in the PR.
- [x] Prepare [release PR #7](https://github.com/AlanD20/mvmctl/pull/7) and the release branch for current CI.
  The live PR checks must pass on its pushed head before tagging.
- [x] Assemble the source, local qualification and known limits for the final approval checklist in the PR.
- [ ] Obtain the user's final release approval before merging, tagging or publication.

The live PR checklist records completed remote CI without treating an older successful run as current evidence.
Tagging, publication and the AUR metadata update follow approval and [RELEASE.md](../docs/RELEASE.md).

## Deferred, Not Release Blockers

- aarch64 runtime qualification awaits available hardware; cross-builds do not qualify runtime support.
- The other explicit feature deferrals and memory-policy limits remain in [plan.md](plan.md).

Historical unchecked implementation tasks are superseded by the accepted work above. Use Git history for prior
progress records and the linked ADRs for design contracts; do not restart obsolete implementation or QA diaries.
