# v0.3.0 Release Tasks

This checklist implements `tasks/plan.md`. Go production and L0/L1 work belongs to the `engineer`; Python
`tests/system/` and release verification belongs to the `qa-engineer`; ADRs, plans, and documentation belong to the
`architect`. The architect reviews the actual diff after every implementation slice.

## Guest Control and floating IP expansion (2026-11-17)

Work on `feature/guest-control-floating-ips`, then integrate into `0.3.0-release` after the gates below.
Earlier checked items describe the qualified baseline; they do not qualify this expansion.

- [x] Approve Guest Control and floating-IP ADRs, including identity, delegation, endpoint lifecycle and failover.
- [x] Draft [Guest Control authority](../docs/adr/0027-guest-control-authority.md) and
  [floating-IP ownership](../docs/adr/0028-floating-ip-ownership.md) contracts, recording unresolved runtime details.
- [x] Implement strict typed Guest Control request framing, including duplicate/unknown-field refusal,
  exact integer decoding, bounds, cancellation checks and partial-write handling; verify with race tests and vet.
- [x] Implement request-bound typed responses with a closed result/error union, ownership consistency checks and
  bounded lease durations. Verify independent frames, ambiguous input refusal and error-detail exclusion.
- [x] Implement pure floating-IP address selection against canonical network authority and supplied reservations;
  verify gateway, collision, exhaustion, corrupt-state and subnet boundaries in the named QA runner.
- [x] Implement root reservation records, generation-checked retirement, persistent tombstones and paginated inventory.
  Include primary/stopped-primary collision checks, network-removal/reset guards and publication failure tests.
- [x] Wire closed root CRUD, the operator API/CLI and cache cleanup; exclude floating reservations from primary
  allocation. Focused named-runner L2 passed all 11 cases across the initial run and one corrected-oracle retry.
- [x] Retain admitted firewall tables across ordinary atomic rule updates. Focused vet/race checks, paired build
  and both nested floating-IP lifecycle cases passed in the named QA runner.
- [x] Implement strict secondary-set metadata, tuple-key and lookup grammar plus typed guard helpers. Confirm
  concatenated lookup, retained timeout and expiry in an isolated QA network namespace; vet/race checks pass.
- [x] Connect secondary ownership to the sealed compiler, typed guards, exact set/element collection and atomic
  application. Preserve unchanged keys, revoke changed authority and bind VM policies to their owner's addresses.
- [x] Build the named QA runner's replacement kernel from cached source with bridge metadata and conntrack
  netlink enabled; retain source/config/toolchain receipts and verify the implementation symbols in the ELF.
- [x] Boot the replacement kernel, pass modeled nftables readiness and qualify floating-IP packet fencing
  through the complete 14-case runtime domain.
- [x] Implement retained root VM admission through the protected Firecracker API peer, incarnation rechecks
  and genuine topology holds; independently review and run focused Jailer vet/race checks in the named runner.
- [x] Qualify that admission against live Firecracker processes through the floating-IP runtime suite.
- [x] Implement address-scoped conntrack invalidation after secondary authority is absent, using exact tuple,
  zone and flow identity with absence readback. Independent review and focused QA vet/race checks pass.
- [x] Compose flow and exact neighbor cleanup under retained network authority, with durable reservation and
  bridge identity/L3 checks; independently review and run focused QA tests, vet and race checks.
- [x] Qualify ownership transitions and real flow/neighbor cleanup with the replacement kernel, including
  policy-preserving owner movement and refusal of stale-owner traffic.
- [x] Implement the canonical ownership journal with revision CAS, closed finite-lease transitions and
  non-replaying recovery states; independently review and pass focused QA vet/race checks.
- [x] Compile VM/network grants to exact floating-IP destinations with stable resource-based policy identity;
  verify owner movement, dormancy, destination exclusions, canonical hashing and sealing in focused QA.
- [x] Wire floating-IP policy CRUD through operator inputs, repositories and authenticated root mutation;
  independently review and pass focused QA tests, vet, race and existing policy regressions. Live policy movement passes.
- [x] Implement and review typed fencing, owner publication and finite lease activation/renewal effects;
  pass focused QA tests, vet and race checks without claiming live renewal timing.
- [x] Add retained target-VM network admission, read-only endpoint/lease verification and authoritative Running
  checks. Run genuine hold refusal/release tests and qualify successful admission in the runtime domain.
- [x] Implement root delegation persistence and exact sustaining-grant provenance with explicit lease policy;
  verify the correction rejecting impossible ownership revisions.
- [x] Wire operator delegation CRUD, exact-ID ensure and receipt-guarded resumable revocation; independently
  review and pass focused QA vet/race checks. Ordinary guest grant, fencing and worker-stall journeys pass.
- [x] Implement and review pinned mutual TLS authentication with real handshake, refusal and cancellation tests.
- [x] Implement backend-preserving root credential preparation, expected-import receipts, snapshot lineage
  and restartable credential retirement. Focused QA and existing workflow regressions pass.
- [x] Implement guest ownership coordination, lifecycle fencing, bootstrap projection and removal guards;
  resolve the present-zero takeover review finding and pass focused integration checks.
- [x] Implement delegated association ownership, kernel-enforced finite authority, fencing and transfer recovery.
- [x] Qualify live ownership transfer, kernel expiry and recovery under process/packet failures.
- [x] Decide that operator associations persist until release, transfer or VM stop while guest leases remain finite.
- [x] Implement and review host association, transfer and release with explicit lifetime authority and
  acknowledged guest aliases; pass focused QA.
- [x] Complete integration CI and root Jailer/agent alias suites for persistent ownership at `945bea5b`.
- [x] Complete live alias and packet qualification for persistent ownership, including association, transfer,
  release and VM-stop fencing.
- [x] Implement the owned host session with one protected VMM admission, expected credential lineage,
  pinned agent TLS and independent exit descriptor. Review and focused QA vet/race checks pass.
- [x] Wire the retained root worker and typed delegated operations without operator impersonation, including
  grant startup, start/resume recovery and lifecycle retirement; pass activation review and focused unit/vet/race checks.
- [x] Qualify ordinary live worker bootstrap and incarnation retirement through stop/start and memory restore.
- [x] Qualify real-agent restart/reconnect and independent VMM/worker process exit before lifecycle cleanup.
- [x] Complete deliberate worker-stall fencing qualification using the explicit administrator STOP/CONT mode,
  pre-armed before grants or association; preserve the original lease deadlines and traffic assertions.
- [x] Implement the authenticated guest gateway and typed `mvm-agent control floating-ip` commands;
  independently review and pass focused named-runner tests, race checks and vet; integrate the retained host worker.
- [x] Implement and review guest alias reconciliation with exact owned receipts, bounded local lifetime,
  expiry/session-loss/restart cleanup and refusal of foreign addresses.
- [x] Document public reservation/delegation commands, guest control, exact traffic destinations and version 1
  environment fields and ownership semantics. Prepare the website source update for review; publication remains held.
- [x] Qualify the composed control path, including reconnect, VMM exit, host reboot and stalled-worker fencing.
- [ ] Publish the separately reviewed website update — HELD for separate user authorization.
- [x] Repair the HA Kubernetes example and its policies, helpers and three-controller topology.
- [x] Test floating-IP association and disassociation with small VMs in the existing named QA runner.
  Full Kubernetes cluster bootstrap, quorum testing and a larger runner are explicitly out of scope.
- [x] Add and register Python scenarios for guest control, floating-IP policy, renewal, lifecycle,
  stalled-worker fencing, memory restore and environment ownership; pass collection and affected registry checks.
- [x] Review the stalled-worker observer and fix cleanup to attempt continuation through its retained pidfd
  even when the final process observation fails; pass non-signaling helper checks.
- [x] Extend the existing host-reboot continuation with floating-IP and delegation recovery coverage; pass
  receipt/preservation helper checks and collection from the exact staged payload.
- [x] Run both reboot phases across an actual host reboot and qualify delegation retirement and explicit recovery.
- [x] Complete final integration Go CI and full root Jailer/agent alias checks after the production startup
  and prior-boot recovery corrections, on clean QA source `a84d8958`.
- [x] Pass integration Go CI, complete root Jailer and root agent alias suites for `945bea5b`; build the
  paired host and guest agent and install the candidate inside the named QA VM with inventories preserved.
- [x] Complete affected Python system qualification in `mvmctl-030-test` and update public Markdown guidance.
- [x] Prepare and review the website source update; source commit and publication remain HELD.
- [x] Integrate the reviewed feature into `0.3.0-release` at `a84d8958`, with final CI and integration-owner
  feature sign-off complete.
- [ ] Complete expanded whole-release sign-off — HELD separately from feature qualification and integration.
- [ ] Obtain the user's separate authorization before merging the release PR to `main`, tagging or publishing
  — all three remain HELD.

All 46 ordinary runtime cases passed (32 environment and 14 floating-IP) on QA source `0403ad4e`, using
candidate SHA-256 `44e2147e11f413a95998c6b9a56e46f82fb979667f2c0fb557642f24f878cf1c`
from production source `4f490ff0`. There were no skips, errors or interventions; all teardowns and full inventory
cleanup passed. This includes environment apply/reapply/destroy and exact ownership preservation/refusal.
The prescribed Go CI and paired real-agent build passed. Complete root Jailer results are 923 passes with exactly
two intentional private-entrypoint skips (`TestProtectedKernelSuccessorCrashWorker` and
`TestProtectedKernelSnapshotProducerCrashWorker`); root alias tests are 21 passes with no skips.
Evidence: `.tmp/qa-guest-control-final-0403-nov22/runtime-verification.json`, SHA-256
`896b1f7c65bde77fcc7575b05e8d8ac0d09402739eaccfebd0d8aada662874c5`, and
`.tmp/guest-control-final-candidate-nov22/{paired-build-manifest,gate-summary}.json`.
Incremental real-agent restart/reconnect passes. Production source `f7666e5a` and paired candidate SHA-256
`0efe49b93ad9154e0bcaeec87a77421591eff7a87153c1c85136107cf3eadf1e` pass actual host-reboot
PRE (37.339s) and POST (83.588s), strict reports, owned cleanup and historical verification. Evidence:
`.tmp/qa-host-reboot-recovery-f766-nov22/verification-summary.json`. Independent VMM/worker exit before cleanup
also passes on QA source `9c55e34c` with the unchanged candidate; evidence:
`.tmp/qa-vmm-exit-9c55-nov22/verification-summary.json`. These runs do not replay the ordinary 46 cases.
Deliberate worker-stall qualification passes on QA source `a84d8958` with the unchanged candidate: one case
passed in 98.86s, no skips/errors/deselections, canonical matrix/root-receipt verification and equal before/after
inventory. Evidence: `.tmp/qa-worker-stall-prearmed-runtime-nov22/verification-summary.json`, SHA-256
`cca906ee1ff88402ff8219f4f0f18c0171f6f9ffc412abbbd001b30a548efa77`. Explicit administrator pre-arming
precedes grants/association; the original 24-second leases and all timing/traffic assertions remain unchanged.
No persistent observer helper or grant is needed. Final integration Go CI passes on clean QA source `a84d8958`;
every prescribed stage exits zero. Complete root Jailer has 923 top-level passes and the same two intentional
private-entrypoint skips; root alias/operator has 21 top-level passes with no skips. Before/after source
manifests match and the installed candidate is unchanged. Evidence:
`.tmp/ci-final-guest-control-nov22/{summary,proof}.json`. Feature qualification and integration-owner feature
sign-off are complete; `0.3.0-release` was fast-forwarded to `a84d8958`. Expanded whole-release sign-off
remains separate and held. No gate is waived.
Full Kubernetes bootstrap, quorum testing and larger-VM qualification remain outside this goal. These results
do not qualify the release.

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
