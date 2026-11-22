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
  review and pass focused QA vet/race checks. Ordinary guest grant and fencing journeys pass; worker-stall QA remains.
- [x] Implement and review pinned mutual TLS authentication with real handshake, refusal and cancellation tests.
- [x] Implement backend-preserving root credential preparation, expected-import receipts, snapshot lineage
  and restartable credential retirement. Focused QA and existing workflow regressions pass.
- [x] Implement guest ownership coordination, lifecycle fencing, bootstrap projection and removal guards;
  resolve the present-zero takeover review finding and pass focused integration checks.
- [x] Implement delegated association ownership, kernel-enforced finite authority, fencing and transfer recovery.
- [ ] Qualify live ownership transfer, kernel expiry and recovery under process/packet failures.
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
- [ ] Qualify live worker bootstrap, reconnect, process exit and lifecycle retirement.
- [x] Implement the authenticated guest gateway and typed `mvm-agent control floating-ip` commands;
  independently review and pass focused named-runner tests, race checks and vet; integrate the retained host worker.
- [x] Implement and review guest alias reconciliation with exact owned receipts, bounded local lifetime,
  expiry/session-loss/restart cleanup and refusal of foreign addresses.
- [x] Document public reservation/delegation commands, guest control, exact traffic destinations and version 1
  environment fields and ownership semantics. Prepare the website source update for review; publication remains held.
- [ ] Qualify the composed control path and publish the separately reviewed website update.
- [x] Repair the HA Kubernetes example and its policies, helpers and three-controller topology.
- [x] Test floating-IP association and disassociation with small VMs in the existing named QA runner.
  Full Kubernetes cluster bootstrap, quorum testing and a larger runner are explicitly out of scope.
- [x] Add and register Python scenarios for guest control, floating-IP policy, renewal, lifecycle,
  stalled-worker fencing, memory restore and environment ownership; pass collection and affected registry checks.
- [x] Review the stalled-worker observer and fix cleanup to attempt continuation through its retained pidfd
  even when the final process observation fails; pass non-signaling helper checks.
- [x] Extend the existing host-reboot continuation with floating-IP and delegation recovery coverage; pass
  receipt/preservation helper checks and collection from the exact staged payload.
- [ ] Run both reboot phases across an actual host reboot and qualify delegation retirement and explicit recovery.
- [x] Pass integration Go CI, complete root Jailer and root agent alias suites for `945bea5b`; build the
  paired host and guest agent and install the candidate inside the named QA VM with inventories preserved.
- [ ] Complete affected Go and Python system tests in `mvmctl-030-test`, plus documentation and website updates.
- [ ] Review and integrate the feature into `0.3.0-release`; verify subsequent production changes and complete
  release qualification.
- [ ] Obtain the user's separate authorization before merging the release PR, tagging or publishing.

The complete floating-IP domain passed all 14 runtime cases with source `789214bc` and the paired `8dd32bf8`
candidate in the named QA runner. It covers host and guest ownership, packet isolation during owner movement,
renewal and expiry, stop/start, pause and memory restore. Cleanup passed without manual neighbor intervention.
Environment ownership, stalled-worker fencing and actual reboot continuation remain separate qualification gates.
The QA-only kernel-binding correction is integrated at `ded3199c`; its 17 focused checks pass, but environment
collection is not runtime qualification. These results do not qualify the full Kubernetes example or the release.

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
