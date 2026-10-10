# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.3.0] - Unreleased

> **Focused qualification checkpoint (2026-10-10):** the counterfeit-console endpoint discovery case
> passed strict 1/1 on the installed Resume candidate. Discovery refused the exchanged decoy socket,
> restoration preserved the original relay and Firecracker identities, and typed cleanup restored the
> checked inventories. This is endpoint-discovery coverage, not qualification of every console case.
> Explicit PRE/POST host-reboot coverage is now implemented; 52 focused checks and a three-variant
> integration check passed, with both phase nodes collected. At that checkpoint, actual reboot qualification
> was pending; the completed layout-candidate journey is recorded below. Full release qualification requires
> a closed `--host-reboot-task` proof; ordinary tier batches
> do not implicitly reboot the runner or qualify that journey. A separate test-only fix removes an unsafe
> early subprocess-diagnostic read; the full privileged-package race run passed 311 tests/subtests.
> Kernel acquisition exposed a real teardown failure on root-owned protected cache parents. The failure
> remains recorded; [ADR-0022](docs/adr/0022-explicit-protected-cache-retirement.md) defines the approved
> typed retirement fix, now integrated with root-private atomic capture, parent synchronization and a durable
> nonterminal retry barrier. Focused API, authorization and root-race checks passed inside the named runner.
> Clean `752110fa` subsequently passed complete pinned Go CI, all 2,663 root-Jailer test/subtests and
> the real-agent release build inside `mvmctl-030-test`. Normal inner installation passed and preserved
> the checked original state except for the authorized canonical binary replacement. The installed candidate
> at that checkpoint was `0.3.0-rc.cache-752110fa` (`ea7bad33`). Permanent snapshot-lock and volume-removal
> residues required the fresh-layout contract in [ADR-0023](docs/adr/0023-protected-branch-authority-layout.md).
> `b254f010` implements it; formatting, vet, 149 focused root checks
> and the same 149 race checks passed inside the named runner. Clean `4bc51ee7` subsequently passed complete
> pinned CI, all 2,707 root-Jailer test/subtests and the real-agent release build. Its new layout candidate
> was subsequently installed normally in a fresh named runner. Shared-filesystem snapshot/volume/cache
> retirement passed, including independent restored data and unchanged permanent authority. The corrected
> distinct-filesystem case subsequently passed strict 1/1, including terminal cache and exact owned mount
> retirement. `d245d6a5` integrates the safe fixture without relaxing production admission or reusing reserved
> cache identities. Earlier failed invocations remain failed. No existing-layout migration or authority
> rebinding is added.
> The three acquisition cases now share one receipt-owned store, retire their kernels between cases,
> and clean that cache once at sequence completion. All three test bodies are unchanged. A subsequent setup
> failure exposed inventory access before private cache initialization; the fixture now uses normal cache
> initialization while preserving the retained cache and authority identities. Its 22 focused fixture checks
> and exact 22-node collection passed inside the named runner. `ed16366d` additionally admits only normal
> operator-owned audit files and schema 2–8 database snapshots created by initialization; wrong-owner,
> wrong-type and unknown entries still refuse. Its twelve affected checks passed. The corrected original
> three acquisition cases then passed on the installed cache candidate, including per-case kernel retirement
> and final typed cache cleanup. Checked original inventories were byte-identical afterward. The separate
> official-build journey remains pending; the focused shared/distinct branch-retirement evidence is not a
> full release pass.

> **Kernel default and official-fixture corrections (2026-10-10):** `4bc51ee7` propagates default-write
> errors and returns the authoritative persisted kernel row from `kernel pull --default`. Focused format,
> vet and 120 existing API checks passed in the named runner. `5f420009` gives the remaining eight official
> kernel cases one privately owned cache, exact publication receipts, per-case retirement and final typed
> cleanup. The combined official/retained-cache fixtures passed 34 focused checks and 22-node collection.
> `a6047716` initializes that private cache with the matching trusted default Firecracker/Jailer pair
> through normal `bin pull`, reusing the mirrored archive. Typed cleanup preserves the borrowed pair
> and original defaults. All 22 affected fixture checks passed inside the named runner with zero failures,
> errors or skips; this does not qualify the eight real official-kernel acquisition/build cases.
> Actual official builds and the installed default-result contract remain unqualified.

> **Official-build timeout correction (2026-10-10):** the first real build hit the old 630-second QA
> deadline while compiling Linux 7.2.9; this was not a demonstrated memory failure. `86f32738` gives only
> the eight official pulls a 2,400-second allowance and identity-checked private process-group cancellation,
> retaining uncertain outcomes rather than adopting them. All 30 affected fixture checks passed inside
> the named runner. No production memory policy or artificial QA RAM guard is introduced. Actual build
> qualification remains pending; the cached Linux source will be reused.

> **Named-runner reboot transport correction (2026-10-10):** the layout candidate passed the inner
> PRE case and the named runner really rebooted, but the legacy outer controller could not reconnect:
> its fresh Firecracker configuration omitted vsock. That attempt remains incomplete, not a memory
> failure or a release pass. `2a00f3e5` uses the unchanged controller's existing SSH command for only
> host-phase staging, orchestration and receipt retrieval; candidate-side VM exec/vsock assertions are
> unchanged. All twelve affected behavioral checks passed inside the named runner, with unchanged
> candidate and boot pins. That checkpoint did not yet qualify the corrected PRE/reboot/POST journey.

> **Subsequent installed-candidate QA (2026-10-10):** on the unchanged layout candidate, conntrack
> after network sync and cross-network TCP allow/revoke passed. The same invocation then failed at
> guest-user readiness before directional exec grants; pause/resume was unexecuted. Exact cleanup
> restored the baseline, but this was not a passing four-case batch. Separately, SSH-based reboot PRE
> passed and reconnected after the real reboot. POST ran no tests because staged source files lost
> their contents across the force reboot. `a90fcfb5` adds a checked guest-filesystem durability barrier
> before reboot and exact source-hash refusal after reconnect and before POST. Four focused checks
> passed inside the named runner; actual runtime qualification followed separately below. Neither failure
> demonstrates a memory-capacity problem or qualifies the complete release.

> **Agent-readiness diagnosis (2026-10-10):** `6af6c511` retains the last exec-readiness command
> failure without changing its deadline or success criteria. The directional singleton then failed
> because ordinary exec could not reach the vsock agent, before any destination or policy grant.
> The separately executed pause/resume singleton failed on its initial guest boot-ID exec, before
> any pause action. Both restored the exact baseline. These are unresolved agent-startup/transport
> failures, not passing policy or pause/resume journeys and not demonstrated RAM failures.

> **Completed layout-candidate host reboot (2026-10-10):** clean `74a916d3` with installed
> `0.3.0-rc.layout2-4bc51ee7` (`e6ff4283`) passed both explicit phase bodies: PRE strict 1/1
> and POST strict 1/1, with zero other outcomes. The named runner actually rebooted and all eleven
> staged source hashes survived. POST proved prior-boot stopped projection, same-ID restart with a
> new guest boot, ordinary exec reading the retained marker, exact owned removal and default-network
> reconstruction. Persistent inventories, administrator state and admitted artifacts were preserved.
> `74a916d3` fixes only the pre-repair assertion: both boot-derived presence flags must be false;
> durable network fields and restored presence remain checked. Earlier failed attempts stay failed.
> This closes that installed reboot journey, not the complete release matrix.

> **Narrowed OpenRC startup diagnosis (2026-10-10):** an independent Alpine diagnostic retained
> networking/IPv4LL and chronyd startup before `mvm-agent` started around 18 seconds after creation.
> A single ordinary five-second exec after passive boot observation succeeded immediately, with the
> agent running in the default runlevel. This supports delayed agent startup for that cloud-init-off,
> network-attached, skip-deblob guest. It does not qualify directional policy or pause/resume, establish
> the cause for every guest variant, or change production startup behavior or exec deadlines.

> **Standalone export-interruption qualification (2026-10-10):** the SIGINT survivor case passed alone
> on the installed cache candidate: native exit 0, strict 1/1 with zero other outcomes, 13.37 seconds.
> It interrupted an authenticated partial copy, refused release while the copy worker survived, preserved
> root authority, and restored the source state and guest-written bytes after settlement and discard.
> Typed owned cleanup and exact original/prepared inventory preservation passed. The earlier mixed
> invocation remains failed; this new singleton closes its SIGINT qualification gap, not the full release.

> **Resume-authority correction (2026-10-09):** `1300b64b` removes the cached Running/Starting success
> shortcut. Resume must first pass root-owned pending-export admission, then observe the authenticated
> Firecracker state: Running succeeds without another mutation, Paused receives one resume request, and
> prestart or malformed state is refused. Refusal leaves the database projection and export intent unchanged.
> Focused formatting, vet, behavior, race and root checks passed inside `mvmctl-030-test`, including
> 52 controller and 10 root-broker tests/subtests with zero failures/skips. The matching receiver-death test
> now checks the existing batch error envelope without weakening its recovery assertions. This correction
> passed complete pinned CI, the separate full root Jailer suite (2,642 passes, zero failures/skips) and the
> real-agent amd64 build. Candidate `0.3.0-rc.resume-1300b64b`, SHA-256
> `81c521ba65e82cdec0abc322b080aa3e0c38876d6313f359cf86713792720c57`, was installed only inside the named
> QA VM; preservation differed only in the authorized binary replacement. The affected receiver-death and
> public pause/resume journeys then passed strict 2/2 with zero other outcomes. Actual receiver death left
> the source paused; resume was refused until public recovery completed, after which source and clone retained
> the guest-written marker. Repeated normal resume preserved process/guest identity. Owned cleanup restored
> the checked original state. This closes the demonstrated Resume defect, not the full release matrix.

> **Cached-kernel lifecycle qualification (2026-10-09):** the existing default transfer/restoration and
> ID-prefix removal cases passed strict 2/2 on the Resume candidate, using the pinned local ELF without
> downloading or building a kernel. Typed cleanup restored the original default and removed the owned imports.
> Original backing, administrator state and inventories were preserved, except for the explicitly retained
> `kernel.updated_at` display timestamp advanced by default restoration. Kernel acquisition, producer and
> stopped-VM dependency cases remain separate.

> **Kernel registry and stopped-reference qualification (2026-10-10):** the remote-list singleton and
> stopped-VM kernel-deletion singleton each passed separately with zero other outcomes on the Resume candidate.
> Ordinary removal of the stopped VM's kernel returned `kernel.in_use`, preserving both records. Typed owned
> cleanup and exact original/prepared state preservation passed, including unchanged kernel timestamps.
> These do not qualify kernel acquisition or builds; those remain pending.

> **Current implementation and qualification checkpoint (2026-10-09):** the closed root-dispatch, isolated
> Jailer, per-VM managed/networkless namespace, direct-Go nftables, typed traffic/exec policy, protected raw-volume
> and snapshot paths are implemented. The current installed QA candidate is the Resume build above.
> Earlier candidate `0.3.0-rc.export-interrupt-bfdece17` was bound to `bfdece17` and SHA-256
> `d13f8c06335342dab47fc6e5399af7e195992ab16dcf1ad00c3289c692390e42`.
> Complete pinned Go CI and the separate full root Jailer suite passed on that clean source, including the new
> live-attachment and export-interruption changes: 2,632 root tests/subtests, zero failures/skips. Its real-agent
> amd64 candidate `0.3.0-rc.export-interrupt-bfdece17` built with Go 1.26.3 and unmodified VCS metadata,
> SHA-256 `d13f8c06335342dab47fc6e5399af7e195992ab16dcf1ad00c3289c692390e42`. Normal system installation
> passed inside the named QA VM only; independent before/after checks preserved existing state except the
> authorized binary replacement. Earlier installed `f909601d` evidence covers all 31 registered network nodes
> across five batches, all six host-exec cases, and all six VM-to-VM exec nodes across three invocations.
> All 69 lifecycle and 37 volume nodes match the current 106-node collection across eight separately bound
> passing batches, with strict zero-other outcomes and checked owned cleanup/preservation; these are not
> consolidated full-module or full-matrix runs. The real read-only administrator init/reset failure-and-retry
> case also passed on the earlier installed `f909601d` candidate. Eight of nine original hotplug cases have
> passing evidence; live reattachment
> failed because the device did not reappear in the guest. A subsequent diagnostic established a Firecracker/guest
> PCI BAR address mismatch; a successful explicit rescan did not restore the device. The dependency correction
> remains unadmitted and unqualified, and the current exact Firecracker/Jailer pin is still 1.16.2.
> Reusable-base provisioning, the remaining installed matrix, administrator interruption/reboot/fault journeys and final approval remain
> incomplete; aarch64 runtime and Tier 3 are deferred. See the
> [coverage matrix](tests/system/COVERAGE_MATRIX.md); v0.3.0 is not release-qualified.

> **Live-attachment completion implementation and earlier build (2026-10-09):** `7a4a99de` adds authenticated guest readiness
> for the root-derived disk serial/capacity and a final unchanged root attachment check before Running-VM
> attach succeeds. Paused attachment is staged with an explicit retryable deferred-readiness error; known-applied
> failures retain the attachment rather than reporting an available volume. Focused vet, behavioral and race
> checks passed inside the named runner. Complete ordinary Go CI passed on clean `046e8347`; its real
> embedded-agent amd64 release build then passed as `0.3.0-rc.readiness-046e8347`, SHA-256
> `70c7ceacefc79c80f2995b0255fd54626a4de96812aa3bcb2fb82425a5871c4f`, with unmodified VCS metadata.
> CI was not replayed after the initial private wrapper failed before building; its exact generated coverage
> files were preserved outside the checkout for the separate build-only continuation. This candidate is not
> installed or system-qualified and does not fix the diagnosed Firecracker BAR relocation failure.

> **Current installed attachment-readiness checkpoint (2026-10-09):** on installed `bfdece17`/`d13f8c06`,
> Running attach passed immediate disk/driver/capacity checks and filesystem I/O. After correcting only a test
> assumption about inspect fields, Paused and Stopped passed separately, strict 2/2 in 19.48 seconds.
> Paused staging retained its binding and retryable error; resume/same-attach retry and idempotent reattach
> produced usable guest I/O with unchanged process identity. Stopped attachment proved configuration only.
> Exact cleanup and original/prepared inventories passed. The earlier failed invocation remains a failure;
> these three passing cases span two runs and do not repair or qualify live detach/reattach.

> **Current local image checkpoint (2026-10-09):** four registered cases passed strict 4/4 in 7.76 seconds
> on installed `0.3.0-rc.partition-f909601d`: cached raw-file import, missing-path rejection, and normal/force
> removal of an unused imported image. Exact owned cleanup and original inventories passed without synthetic
> memory guards. This does not qualify boot from that local import, in-use force removal or the full image domain.

> **Current cached-image options checkpoint (2026-10-09):** seven further registered cases passed strict 7/7
> in 18.92 seconds on the unchanged installed candidate: cached slug/version/type selectors, default reselection
> and refusal, cached pull with `--default`, and raw import with `--default`. Owned temporary defaults and images
> were removed, restoring the exact original inventory and zero-default state. Remote acquisition and the
> complete image domain remain separate; later force-pull option results are recorded below.

> **Current force-pull options checkpoint (2026-10-09):** the three previously unqualified `--no-cache`,
> `--skip-optimization`, and `--disable-detector` replacement variants passed together, strict 3/3 in
> 87.76 seconds on installed `bfdece17`/`d13f8c06`. Mirrored Alpine pulls produced distinct replacement IDs,
> transferred the private cache's sole default and retired old backing. Typed private-image cleanup and
> literal original public/physical/administrator-state preservation passed; no shared default was changed.

> **Current image-dependency deletion checkpoint (2026-10-09):** both registered Running and Stopped
> VM-reference cases passed strict 2/2 in 17.75 seconds on the installed `d13f8c06` candidate.
> Ordinary image removal returned `image.in_use`, preserving the image record, backing identity and VM state.
> Exact owned cleanup restored the original and prepared inventories byte-for-byte. This does not qualify
> force removal of a referenced image or deletion/default-promotion behavior.

> **Current formerly-default image removal checkpoint (2026-10-09):** the registered explicit-replacement
> case passed separately, strict 1/1 with native exit 0. It selected and retained the replacement before
> deleting the formerly-default owned image; typed cleanup restored the exact original zero-default inventory.
> This does not test automatic default promotion. Prepared reports retain the owned default's timestamp change.

> **Current image-warming checkpoint (2026-10-09):** all four registered warm selectors/refusal cases
> passed strict 4/4 in 7.45 seconds on the unchanged installed candidate. Slug, prefix and `--all` warming
> matched complete independently decompressed bytes and reused the same ready files. `--all` included both
> Alpine and the retained Ubuntu image. Original backings, defaults and inventories remained unchanged;
> only newly owned Alpine and task-local warm/readback files were cleaned.

> **Current protected-rootfs import checkpoint (2026-10-09):** the Running VM-to-image journey passed
> strict 1/1 in 27.69 seconds; Paused and Stopped then passed strict 2/2 in 34.59 seconds on the same
> installed candidate, without artificial QA memory guards.
> Guest-written bytes survived import and boot of an independent clone; the source returned to Running,
> and exact owned cleanup plus original inventories passed. The five remaining local kernel-import
> option/default/error cases also passed strict 5/5 in 5.56 seconds using the cached ELF only.
> Paused/Stopped source states were preserved, and the Stopped case rejected stale staging contents as
> an export source. Export-interruption recovery remains a separate qualification requirement.

> **Current export copy-failure checkpoint (2026-10-09):** the registered mid-copy I/O-failure case passed
> strict 1/1 with native exit 0 on the same installed candidate. The authenticated root worker reached its
> imposed file-size limit; source state and guest marker survived, partial publication was refused, and typed
> cleanup restored the exact original inventories. The tested fixture correction over `b73d0462` is now
> integrated unchanged in `bfdece17`. The later SIGINT journey passed; abrupt receiver-death recovery remains
> unqualified, as detailed below.

> **Export-interruption correction (2026-10-09):** `3e00e00e` adds an export-only, payload-free transport
> that starts its privileged worker in a separate session and disconnects without killing it on cancellation.
> Ordinary actions and export Release retain their existing cancellation behavior. Focused transport/routing
> tests, vet and race checks passed inside the runner; the new survival regression failed against the old
> state machine. Root authorization, state restoration and failed-response cleanup are unchanged. The fix
> subsequently passed coherent ordinary CI, the complete root Jailer suite and a real-agent release build on
> `bfdece17`; normal inner installation also passed. The first installed attempt proved worker survival but
> failed a stale busy-removal public-envelope assertion. Test-only `0b3762d9` corrects that expectation while
> preserving every root lock/survival/recovery check. The corrected two-case invocation then recorded one pass
> and one failure: SIGINT proved busy-copy refusal, worker completion, source recovery and exact cleanup;
> receiver-death failed because public VM status was `running` where the fixture expected `paused` after the
> authenticated worker died. Both observers exited cleanly and original inventories were restored. This is not
> a passing two-case slice or release qualification; the status mismatch requires diagnosis.

> **Current snapshot failure/recovery checkpoint (2026-10-09):** the registered capture file-limit failure,
> source recovery and full rootfs/RAM/vmstate/data-volume restore case passed strict 1/1 in 18.39 seconds on
> the new installed candidate. Exact owned cleanup and literal original/prepared inventory restoration passed,
> including restoration of the original zero-image-default state. This qualifies that current capture path,
> not the entire snapshot domain or release matrix.

> **Current bounded memory checkpoint (2026-10-09):** installed `0.3.0-rc.partition-f909601d`
> passed all three registered bounded memory cases: near-saturation with host swap absent and present, and
> guest-global exhaustion with an identified allocator OOM victim and fresh exec survival. Each case records
> one pass, zero other strict outcomes and native exit 0, with checked typed cleanup and configuration/inventory
> preservation. Historical incident attribution, survival under arbitrary
> host/ancestor OOM and the full release matrix remain unqualified. See the
> [coverage matrix](tests/system/COVERAGE_MATRIX.md) for exact candidate binding; v0.3.0 is not release-qualified.

> **Earlier coherent checkpoint (2026-10-08):** protected VM-to-image exports remain integrated. Clean parent
> `992d2fd5` passed all nine phases of ordinary Go 1.26.3 CI inside `mvmctl-030-test`: 59 native package passes
> and ten no-test rows (receipt SHA-256 `318efd21`). This evidence carries for unchanged surfaces; it is not a
> complete CI run on `64079c3b`. Clean `64079c3b` separately passed the full root Jailer suite: 828 top-level
> tests and 1,799 child tests, zero failures/skips, native exit 0 and exact preservation (`39baf49b`). Its clean
> tree `cc100b39` produced the real-agent Go 1.26.3/PIE candidate with unmodified VCS metadata (`f77b5e34`),
> version `0.3.0-rc.integrated-64079c3b`, SHA-256
> `9aa3a92f049f282cc9bbd3b3c631939c1a7828c78ef60ff7f6630c70784ab948`.
> Normal `host install-system` installed it only inside the named runner. Scoped readiness was accepted from
> exact-JSON adjudication (`61cb1e01`): durable database bytes, identity and permissions, authority and mounts
> matched; only cache-directory mtime/ctime and database ctime differed. The raw failed install receipt remains
> retained, rather than relabeled as a passing run. Earlier CLI and runtime results retain their original candidate
> bindings. At that checkpoint, memory, reusable-base provisioning and the full installed T1/T2 matrix remained
> unqualified; GitHub CI, Tier 3, aarch64 runtime and final release approval remain unqualified.
> The outer controller is unchanged;
> v0.3.0 is not release-qualified. See the [coverage matrix](tests/system/COVERAGE_MATRIX.md).

> **Earlier journal-fixed checkpoint (2026-10-08):** protected VM-to-image exports are integrated. Clean journal-fixed source
> `1a2506e6` passed the complete pinned Go 1.26.3 ordinary local CI sequence inside `mvmctl-030-test`, including
> vet and the CI-selected tests. Independent preservation checks passed. The separate full root Jailer run was
> refused before execution by its original post-CI memory admission check; it is not a current full-suite pass.
> Consolidated root evidence covers 826 unchanged passes plus the corrected authority-path test's focused pass,
> not a repeated full-suite run. Clean source `497455a6` passed the real-agent release build and normal installation
> inside `mvmctl-030-test`, with independent preservation checks. Its phase-observed Running import reached
> mandatory OS inspection but failed because the copied ext4 journal needed replay on a read-only loop device.
> Signed `1a2506e6` now prepares only a new disposable VM-import inspection copy with unprivileged journal replay,
> preserving the exported source and root read-only inspection. Six new behavioral Go tests each passed twice,
> the selected cached-import API test and focused vet passed, and independent preservation checks passed inside
> the named runner; nine existing image regressions passed separately. The corrected real-agent candidate built
> and installed inside that runner. Its differentiated Running case completed import and restored the source to
> Running, then exceeded the unchanged runner memory safety threshold during clone boot. All six cleanup checks
> and independent preservation checks passed, with no OOM events. Clone content, interruption/recovery and complete
> import-journey qualification remain open; v0.3.0 is not release-qualified. The outer controller remains unchanged.
> The complete CLI module subsequently passed 24/24 once in 0.39 seconds against the installed journal-fixed
> candidate, with strict JSON/JUnit agreement and independent preservation checks. Current CLI/helper bytes
> matched the candidate's clean source. This refresh does not qualify global structured JSON, export recovery,
> the complete current-source matrix or release readiness.

> **Earlier QA checkpoint (2026-10-03):** v0.3.0 remains unreleased. The clean-break single-binary/root-dispatch,
> isolated Jailer, pressure-only cgroup, typed LoopMount, per-VM network namespace, direct-Go nftables,
> traffic/exec policy, protected volume, and consistent snapshot paths are implemented in source. VM launch
> requires the matching Firecracker/Jailer 1.16.2 pair. Exact clean source snapshot `b2c0a1f8` passed the complete
> pinned Go 1.26.3 local CI sequence inside `mvmctl-030-test`, plus the full root-effect Jailer
> package (7.743s) and all 240 Python orchestrator unit cases (7.41s). Earlier runner candidates passed network/nftables/policy
> (47/47), volume (37/37), exec (7/7), host (24/24), SSH (9/9), logs (11/11), console (5/5), copy (15/15),
> kernel import (8/8), Jailer (13/13), and rootfs persistence (1/1) Python domains with strict zero-skip reports at
> their recorded revisions. The subsequent registered Tier 1/Tier 2 run collected 415 cases across 26 files:
> 413 passed and two binary-test cases failed. After correcting a QA historical-journal descriptor leak, the complete
> 11-case binary file passed separately, completing consolidated coverage of those 415 cases across test revisions.
> Configuration and resource cleanup were checked, and both the failed and corrected reports remain preserved.
> This is not a single complete run against the latest source/candidate, complete Tier 3 or whole-release qualification.
> Host-reboot/exec recovery had one device-open failure and one successful controlled repeat; its cause remains
> unresolved. A later owned-guest reboot check stopped before restart: inspection rejected stale console boot
> identity, and typed VM/network cleanup refused prior-boot admission. Targeted console and schema-6 absence-proof
> fixes passed affected Go/root gates. The corrected installed runner candidate then reported that exact VM stopped
> and removed its VM/network through normal typed commands, preserving the cleaned root journal and leaving all four
> inventories empty. No records were manually deleted or adopted. One fresh, exact-candidate reboot journey then
> passed stopped-state reconciliation, one restart, new host/guest boot identities, fresh exec and checked cleanup.
> This bounded pass does not attribute the earlier intermittent KVM-open denial or qualify every recovery fault.
> Later complete SSH (11/11), config (12/12),
> keys (26/26), volumes (37/37), full journeys (15/15),
> and internal-service entry points (5/5) passed twice at their recorded exact test revisions. Three bounded
> memory cases also passed twice, including sustained measured cgroup pressure with and without swap;
> a separate guest-global RAM-exhaustion case now passed once with an identified allocator OOM victim, preserved
> Firecracker identity, successful fresh exec and unchanged observed runner/ancestor OOM counters. Original incident
> attribution and safety under arbitrary ancestor/global pressure remain unproved.
> Active console discovery/kill now uses root-verified relay authority and durable relay-only stop; affected
> Go/root/race gates pass. Its owned installed-candidate lifecycle journey passed twice (14.04/13.25s),
> proving relay-only stop preserves Firecracker/vsock and VM restart renews the relay. A later real-PTY
> attachment journey passed, then both console journeys passed twice (26.42/24.12s), including normal endpoint
> admission, unique guest serial bytes, detach, terminal restoration and preserved VM liveness. An added
> caller-PID-mirror decoy case passed, proving forged display metadata cannot redirect discovery or relay signaling.
> A subsequent preexisting counterfeit socket-peer case passed (9.75s), followed by the complete four-case console
> file (46.08s) on the corrected installed candidate, with strict zero-other-outcome reports and checked cleanup.
> This proves rejection before attachment, not resistance to endpoint replacement after observation; other hostile
> endpoint and race faults remain separate gates.
> Clean administrator/marker-only denial and reset, adversarial recovery/fault checks,
> reusable-base provisioning, Tier 3, aarch64 runtime, GitHub CI, and final
> release approval remain open. A fresh mirrored-source kernel producer completed with retained input/toolchain/
> config/ELF receipts, followed by a bounded exact-build-ID boot check with KVM device open and TUN/Btrfs evidence.
> Neither pass reconstructs older artifacts' provenance.
> On 2026-10-02, one bounded fresh-kernel workload case passed: the exact imported ELF's live build ID matched,
> KVM reported API version 12 and created an owned empty VM descriptor, and the production readiness probe
> applied/read back/removed disposable bridge and inet nftables rules. An over-strict QA timestamp guard initially
> preserved the seeds; its narrow correction then permitted normal typed cleanup and exact baseline restoration,
> without rerunning the test. This does not qualify reusable-base provisioning, traffic isolation or the full matrix.
> No candidate has been installed over the
> outer controller.
>
> The initial lifecycle/snapshot QA stopped at pause/resume-vsock failure, matching a confirmed upstream 1.16.1
> defect. After the approved 1.16.2 update, its exact regression and all fifteen affected lifecycle/snapshot/version
> cases passed. A subsequent ten-case group passed both independent-volume snapshot variants (normal removal and
> confirmed source crash) plus the complete eight-case snapshot module, including running-source `--pause` and
> exact naming/inspection. Checked teardown left VM/network/volume/snapshot inventories empty. These focused
> results are not the full current-source Python release matrix or whole-release qualification.

The detailed entries below retain implementation-stage history; a later entry or the status above supersedes an
earlier partial-state statement. They are not a substitute for the final release QA report.

On 2026-10-08, VM-to-image import was corrected to read the current protected root disk rather than stale staging.
Running sources pause for copying and resume before extraction; paused and stopped sources retain their state.
Root-controlled, operator-readable temporary exports live under `/var/lib/mvmctl/vm-rootfs-export-data`, separately
from protected disks and final images in the managed cache. Typed cleanup and exact-ID forced image removal provide
interrupted-import copy recovery without deleting caller files or claiming unresolved source-state recovery is complete.
Host reset now admits the shared base's safe `0700`/`0711`/`0755` modes while preserving exact private `0700` checks.
Two signed implementation chunks passed 31 focused Go tests, four intended old-behavior regressions, formatting,
backend compilation and affected vet inside the named QA runner. Clean source `98d1d248` subsequently passed the local
Go CI sequence with both atomic coverage profiles after restoring the harness's standard `sbin` PATH; passed earlier
phases were carried by hash rather than repeated. Full root-Jailer recorded 826 passes and one obsolete exact-open-count
assertion. Its test-only correction now checks that every opened path is the fixed authority root, and passed focused
format, vet and test checks. The 826 unchanged passes were retained; this is consolidated evidence, not a repeated
green full-package invocation. Clean source `497455a6` then passed the cached Go 1.26.3 real-agent release build,
with PIE and exact unmodified VCS identity. Normal `host install-system` installed
`0.3.0-rc.vmexport-497455a6` only inside the named QA runner; installed content matched the verified artifact,
canonical ownership/mode remained `root:root`/`0755`, and independent checks preserved configuration, inventories,
root authority and mirrored assets. The previous runner binary was retained and observed OOM counters stayed zero.
Installed import/recovery journeys remain pending. Original failures and preservation evidence remain recorded.
No outer controller installation was performed.

A registered image-import fault case now targets the authenticated client and root export receiver, checks exact
orphan retirement and idempotent removal, refuses unsafe pending-state resume, and verifies public recovery with
source and clone marker bytes. A second case interrupts the retained CLI while an authenticated root receiver
survives, checks busy cleanup refusal, then verifies source-state restoration and temporary-output discard.
Both fault cases and the three protected-disk state cases passed collection inside the named runner;
the selected strict-outcome runner unit passed. A third fault case now targets actual mid-copy file-size-limit
failure on the authenticated root receiver, checks its retained partial output reaches the imposed limit,
and requires source restoration, marker preservation, rejected partial publication and idempotent typed cleanup.
All six export nodes collected in 0.20 seconds after one fixed-diff review and a correction to match the actual
public JSON error contract; independent preservation checks passed. These fault bodies remain unqualified.
The SIGINT case does not claim cooperative cancellation of the root context or immediate restoration at CLI return.

A differentiated post-install cold restart preserved the same named runner, candidate, backing and controls.
Its Running case entered import but was interrupted by the unchanged 2 GiB host-memory-loss guard while host
available memory remained 6,847,385,600 bytes and the runner stayed below its cap-reserve ceiling. Cleanup and
independent preservation passed with no OOM events; this interrupted run proves neither import completion nor a
product failure. It does not revoke the earlier completed-import/source-restoration observation.

The first Running-state attempt exposed a marker-setup shell-argument error, now corrected in all three affected
test setups. A corrected attempt was interrupted by the unchanged outer-runner 2 GiB memory-growth guard,
not an OOM; no test completed and the import phase was not established. Cleanup and independent preservation
checks passed. This does not qualify export behavior; limits were not increased and the attempt was not repeated.

On 2026-10-03, exact clean source `a3d7b8d0`, including the cloud-init OFF and built-in-KVM corrections,
passed the complete local Go CI sequence inside `mvmctl-030-test`: tidy, architecture, formatting, generation,
vet and the full unprivileged selector, with complete atomic coverage. The selected Jailer tests passed in
5.954s; the accepted full root-Jailer result carried only for verified unchanged dependency/module/build inputs.
The real guest-agent release build passed with Go 1.26.3, PIE, exact VCS revision and `vcs.modified=false`.
Normal `host install-system` installed the matching `0.3.0-rc.a3d7b8d0` only inside the named runner, with
matching content SHA, root-owned `0755` canonical identity and checked baseline preservation. Earlier failed
harness and runtime reports remain failed evidence. This closes the recorded a3d7 local CI/build/install checkpoint,
not reusable-base provisioning, the remaining installed-candidate Python matrix, remote GitHub CI or release
approval. Markdown-only changes do not alter these checked inputs; the later reset-diagnostic Go change has
focused verification, not a refreshed complete CI result.

The reset-diagnostic source subsequently built successfully from clean `33fc5c1a` with the real guest agent,
cached Go 1.26.3, PIE and exact unmodified VCS identity. Normal `host install-system` installed
`0.3.0-rc.resetdiag-33fc5c1a` only in the named QA runner; installed content matched the artifact, canonical
ownership/mode remained `root:root`/`0755`, and configuration, inventories, authority and mirrored assets were
preserved. This is build/install evidence, not successful reset/re-init or refreshed complete CI qualification.

That candidate's registered administrator journey then identified a reset-admission defect: the guard expected
`0755` for private Jailer parents that administrator setup and VM lifecycle require to be `0700`. The correction
keeps the shared parent at `0755` and admits only exact private `0700` parents without changing permissions or
removing residual resources. Its root regression failed on the old code and passed all four admission/refusal
cases after the fix inside the named runner; Jailer vet and formatting passed.

Clean source `e034f4e0` subsequently produced the real-agent candidate
`0.3.0-rc.resetparent-e034f4e0`, installed only inside the named QA runner with verified content and
root-owned canonical identity. Its full root-Jailer package passed in 3.545s with Go 1.26.3 after correcting
only the test child's umask; the original fixture-permission failures and timeout remain failed evidence.
Candidate identity, persistent baseline and resource controls were preserved. The first administrator journey
passed reset but incorrectly expected denied network creation to discard its deliberately retained pending intent.
After that assertion was corrected, the complete registered journey passed on 2026-10-07: one case in
332.94 seconds, zero other outcomes, all twelve preservation checks and verified owned cleanup. This covers
clean-child enrollment, marker-only success/denials, quiescent reset, disabled admission, re-enrollment and
same-ID pending-network activation/removal. The shared runner was neither reset nor replaced, and original
resource guards and product policy were unchanged. Exact clean `e034f4e0` also
passed the complete pinned local Go CI sequence inside the named runner, with both atomic coverage profiles,
clean source and unchanged candidate/baseline/resource controls; selected Jailer tests passed in 3.243s.
Later Python-test/Markdown-only changes leave these Go inputs unchanged. At that checkpoint, filesystem
retries, reusable-base provisioning and the remaining release matrix were not yet qualified; the later current
filesystem-retry pass is recorded above.

The separate password-authenticated second-operator journey passed on the same installed candidate in
95.67 seconds: strict collected/passed 1/1, zero other outcomes and matching JUnit. It covers second-operator
enrollment and typed dispatch, repeated-init preservation, active-network reset refusal and eventual quiescent
reset. All twelve preservation checks and five owned-resource absence checks passed, including removal of the
temporary enrollment policy and empty QA task group. The corrected filesystem-retry journey subsequently
timed out during child package installation, before either read-only fault or retry assertion ran; its cleanup
and preservation passed. That attempt remains failed evidence; the later current filesystem-retry case passed
both real faults and their retries. Earlier failures remain recorded.

The same installed candidate subsequently passed the registered VM-to-VM exec interruption case in
21.77 seconds: one pass, zero other outcomes, six preservation checks and verified QA-task cleanup.
The test interrupts the retained CLI through its pidfd, verifies that the exact destination leader/shell/child
tasks are no longer live, and successfully opens a fresh authorized relay. Original resource guards and
controls stayed unchanged. Complete private-backend cleanup, other adversarial authority/deadline cases,
host reboot and snapshot-restore grants are not established by this case.

Earlier focused checks on installed `a3d7b8d0` passed six VM-free kernel-import cases, guest-OOM survival,
interrupted snapshot capture with full RAM/vmstate/rootfs/data-volume recovery and restore, cached-image default
selection, raw-image import/default selection, and one mirrored forced image replacement. Each result retains its
own strict outcomes and preservation evidence; these are not a complete current-candidate matrix.
The `no-cache` image variant was interrupted by its QA memory-growth guard, not qualified. The administrator
journey's earlier quiescent-reset and stale pending-intent failures are superseded by the complete corrected
first-journey result above. See the [coverage matrix](tests/system/COVERAGE_MATRIX.md) for
scoped results and remaining gates.

On 2026-10-02, clean source `b402c471` passed the complete local Go CI gates inside `mvmctl-030-test`
with Go 1.26.3: offline tidy, architecture, formatting, golines 0.13, generation, vet and the unprivileged selector,
with complete atomic coverage artifacts. The real-agent release build recorded the exact VCS revision and
`vcs.modified=false`; normal system installation placed the matching `0.3.0-rc.b402c471` candidate only inside
that runner, preserving its checked configuration and resource baseline. The full root Jailer result on
`6088c14c` (18.194s) was reused for unchanged Jailer-relevant source, module and build inputs. All 262 Python
orchestrator tests passed separately at their recorded revision. These local gates do not qualify GuestFS,
reusable-base provisioning, the remaining current-source Python matrix, remote GitHub CI or the release.

On the installed `0.3.0-rc.b402c471` candidate, the existing no-host-swap saturation case passed with a strict
one-case report and verified typed cleanup. A 224 MiB guest sustained 91.2–92.2% occupancy of usable RAM for
15.55 seconds; the cgroup exceeded `memory.high`, recorded 32 new high events and retained the same Firecracker
process and guest boot identity with no observed leaf/controller OOM-counter increase. This is bounded survival
evidence, not attribution of the original incident or protection against arbitrary ancestor/global OOM.
Historical swap-present and guest-OOM results remain separate from current-candidate matrix qualification.

A later reusable-base attempt lost the disposable QA host to its **legacy outer controller's** memory cgroup:
the kernel identified that runner's Firecracker process and exact cgroup as the OOM victim. Its 6 GiB guest had
equal `memory.high` and `memory.max` values of 6,576,668,672 bytes (6 GiB plus 128 MiB); its OOM-kill
counter recorded one kill. This attributes that observed runner failure, not the original historical incident,
and does not demonstrate a defect in the 0.3 candidate's pressure-only leaf policy inside the runner. Outer
runner limits remain an independent qualification prerequisite.

On installed candidate `0.3.0-rc.562b38de`, the registered enabled-GuestFS case passed inside `mvmctl-030-test`
in 68.18 seconds: cold appliance construction, actual guestfish launches, warm reuse, incomplete-appliance
repair and selected-cache cleanup/preservation. The strict report contains one pass and zero other outcomes;
typed teardown and independent absence checks passed. This follows correction of the probe-duration defect
and administrator setup of the exact package-owned Ubuntu kernel's readability, retaining the original failure
evidence without attributing it solely to either finding. Fresh reusable-base provisioning and the complete
current-candidate matrix remain separate gates.

Before the shared-state traversal fix, installed candidate `0.3.0-rc.8521b3bc` passed 118 bounded non-VM Python system
cases inside `mvmctl-030-test`, with strict zero-other-outcome reports and checked cleanup. Kernel-import fixtures
require the Alpine image only when creating a VM; all six non-VM import cases passed without an image prerequisite.
Success-list fixtures consume the structured JSON envelope, and fresh-cache initialization uses the required
matching 1.16.2 binaries.
These results do not qualify VM-dependent imports, fresh kernel production, reusable-base provisioning or the
complete current-source release matrix. The installed production binary was unchanged by these test-only fixes.

A subsequent VM-dependent kernel-import case failed before guest launch on that candidate: Jailer traversal incorrectly
applied the private `root:root 0700` rule to the shared `/var/lib/mvmctl` state base, contrary to its documented
shared-directory admission. Fixture teardown and normal removal of the owned image/kernel seeds restored the checked
baseline; the second VM-dependent import case was not run in that failed attempt. The integrated correction admits
safe shared-base metadata without changing it, while preserving exact private-directory permissions and descriptor,
mount, and identity checks across preparation, retention, launch-child reopening, and stopped-tree cleanup.
Its regression failed on the old code and passed on the fix; all eight focused Go tests, their race run, vet,
formatting, and the complete 804-test root Jailer package passed inside the named runner.

The corrected `0.3.0-rc.6088c14c` candidate then passed both VM-dependent import cases once each (7.58s and 9.96s),
with strict zero-other-outcome reports. These cases exercise guest exec, exact imported-kernel selection, stop/start
with a changed guest boot identity, and normal typed cleanup. Owned image/kernel seeds were removed through normal
commands, and final checked state matched the original baseline. The earlier failed reports remain preserved.
This closes the observed shared-base launch failure, not the full current-candidate matrix or whole-release gate.
The QA guide records a Go 1.26.3 nested-worktree VCS-stamping caveat: the corrected candidate was built from a clean
standalone checkout, verified with its actual source revision, and installed only inside the disposable runner.

The complete 38-case image module passed twice against an earlier installed runner candidate, covering mirrored
Ubuntu tar import and boot, Alpine import and boot, format detection, warm copies, defaults and dependency-aware
removal. Both strict reports have zero non-passing outcomes and checked configuration/inventory cleanup.

The complete 31-case basic network module passed twice against an earlier installed runner candidate, with exact
owned cleanup and strict zero-other-outcome reports. This includes an owned missing-bridge administrator fault,
fail-closed sync and typed removal. The nftables and traffic-policy modules also passed complete-file repeats at their
recorded revisions; adversarial firewall and recovery cases remain separate release gates.

### Added

- Added an installed-candidate snapshot producer-interruption journey with unpublished-state recovery and a
  complete RAM/vmstate/rootfs/data-volume restore control. The named-runner-only fault is bound to the isolated
  execution identity and retains the original pidfd for cleanup. Review, lint, formatting and exact-node collection
  passed. The current installed-candidate case subsequently passed interruption recovery and full restore with
  checked cleanup in 29.19 seconds. This does not qualify root-receiver death or power-loss recovery.
- Added a registered Tier 2 `host_admin` journey for first enrollment on a clean disposable child, usable
  marker-only authorization, refusal of public administrator/raw-sudo commands, quiescent reset and re-init.
  Added separate journeys for password-authenticated second-operator enrollment and typed dispatch, active-network
  reset refusal, and real read-only-filesystem failures with init/reset retries. The child owns administrator
  mutations; the shared runner is not reset. Lint, formatting and strict three-case collection passed; existing
  registry checks remain applicable. The corrected first enrollment/marker-only/reset/re-init journey subsequently
  passed on the installed reset candidate, including same-ID pending-network retry and checked cleanup.
  The separate password-authenticated second-operator/typed-dispatch/active-network refusal journey also passed
  with checked cleanup. The filesystem-retry case subsequently passed on the current partition candidate,
  including real first-init/reset faults, retries, same-ID pending intent and checked cleanup. Abrupt process
  interruption and ambiguous-runtime recovery remain unqualified.
- Official-kernel debug output now identifies the exact upstream base-config bytes successfully written before
  configuration mutation. Focused kernel tests, vet and formatting passed in the named runner. A subsequent fresh
  mirrored-source Linux 7.0.11 producer passed with the consumed base-config digest and final config/ELF hashes
  retained. A subsequent installed-candidate nested boot case passed with matching live GNU build ID, KVM device
  read/write open, TUN and Btrfs, and checked cleanup. KVM guest creation, child nftables application and reusable-base
  provisioning remain separate gates; older provenance is not reconstructed.
- A focused local reboot experiment captured the exact active jail/mount objects and KVM/TUN identities;
  private-root readlink text is informational, not process authority. Its owned guest prepared successfully before
  one controlled runner reboot, but recovery failed before the single permitted start. Later targeted fixes passed
  affected Go/root checks and installed-candidate inspection/typed cleanup. A new owned receipt subsequently passed
  the full stopped-state/restart/fresh-exec journey and exact cleanup on the corrected candidate. The original failed
  receipt remains failed; the earlier intermittent KVM-open denial remains unattributed.
- The bundled official-kernel nftables feature now requests bridge connection tracking as well as bridge-family
  nftables. Focused kernel Go tests and vet passed in the named runner. Its booted rebuilt kernel has both flags,
  but the older mirrored default does not; this source change does not rebuild or qualify every runner image.
- Added a real guest-global RAM-exhaustion case, without a synthetic child-cgroup limit or OOM-score protection.
  It passed once in 10.28s on the checked installed candidate: the guest killed only the identified anonymous
  allocator, then completed a fresh exec and allocation while Firecracker remained alive. Exact configuration,
  owned-resource cleanup and observed inner/outer OOM counters were checked. This does not attribute the original
  reported incident or guarantee survival under unrelated host pressure.
- The complete fifteen-case Jailer module passed twice on the checked installed candidate, covering isolated
  credentials/API access, lifecycle and vsock parity, raw volumes, independent memory/rootfs/data-volume restore,
  competing-writer refusal and missing-release recovery. Exact crash admission supports explicitly bound managed
  and networkless placements; it never authorizes signaling from cache PID files.
- The complete eight-case init and ten-case installer files each passed twice against the checked candidate
  inside the named runner. Coverage includes actual administrator-prompt decline, successful explicit bootstrap,
  canonical installation, update refusal and exact-identity read-only/symlink fault cleanup. This does not qualify
  clean administrator enrollment/reset or marker-only operator denial.
- The complete rootfs persistence journey passed twice on the installed runner candidate, proving
  capture independence after source modification/removal and byte-exact restored content. Full-ID
  receipts and checked partial cleanup preserve unrelated resources, configuration and defaults;
  capture interruption and power-loss recovery remain separate qualification gates.
- The complete five-case nftables module passed twice on the installed runner candidate, retaining NAT,
  conntrack, idempotent sync, real SSH and private-namespace IP-reuse checks with exact owned cleanup.
  Kernel observation failures cannot masquerade as absent rules; adversarial firewall faults remain unqualified.
- The complete three-case traffic-policy module passed twice on the installed runner candidate, proving
  same-/cross-network TCP allow and revoke plus exact-source directional UDP. Denial requires real guest
  probe execution and independent delivery checks, not merely a failed exec command.
- The complete 69-case VM lifecycle module passed twice on the installed runner candidate, covering
  SSH authentication, runtime transitions, cold snapshot/crash recovery, cloud-init, networking, volumes
  and multi-VM creation with exact owned cleanup. Host-reboot and capture/power-loss faults remain separate gates.
- The complete 23-case cache module passed twice with private cache selections, checked resource cleanup,
  creation-owned non-default release pruning, unprotected-cache removal/recovery and live-VM refusal checks.
  Borrowed releases, original assets, unrelated GuestFS state/processes and firewall policy remain preserved.
  Protected history is retained; administrator-level retirement is not qualified by these cases.
- Both console lifecycle and live-PTY attachment journeys passed twice on the installed runner candidate,
  proving serial delivery, clean detach/terminal restoration and preserved Firecracker/vsock identity.
- Added an installed-candidate console PID-mirror decoy test. Root-backed discovery and relay stop ignore the
  caller-written decoy PID; only the recorded relay exits, while the independently pinned decoy and Firecracker
  survive and fresh guest exec succeeds. The focused case and complete three-case file passed with checked
  mirror restoration, normal owned cleanup and unchanged configuration.
- Added an installed-candidate counterfeit console endpoint test. Root-backed discovery and attachment reject
  a preexisting same-caller socket whose peer is not the authorized relay. The case preserves pinned relay and
  Firecracker identities, proves fresh guest exec, and restores the exact endpoint before owned cleanup. The focused
  case and complete four-case console file passed; replacement after observation remains outside this proof.
- The complete 11-case logs module passed twice on the installed runner candidate with owned cleanup.
  Coverage verifies real guest identity and serial markers, byte-exact LF tails preserving carriage returns,
  VMM logging/follow, identifier lookup and retained logs after root-authoritative stop. It does not qualify
  successful interactive console attachment or the complete release matrix.
- The complete six-case peer-exec module passed twice with retained full-ID/process/file
  ownership and checked cleanup, including same-network, cross-network and networkless
  directionality and endpoint lifecycle. This does not complete release qualification.
- The complete eleven-case trusted-binary module passed twice, including remote metadata,
  exact creation-owned mirrored release installation/default transfer/removal, and live-VM
  release protection. Retained root manifest/executable receipts protect the borrowed pair.
- The complete 24-case host system-test file passed twice on the installed candidate in the named runner.
  First-capacity initialization and clean/reset refusal now check owned resources, unchanged administrator state
  and healthy guest access. This does not qualify administrator enrollment or successful clean-host reset.
- Added installed-candidate peer-exec cases for explicit opt-in and exact non-root target users, separate output
  streams and nonzero exit status, command-redacted audit correlation, destination process-group cancellation,
  and restored endpoints requiring a fresh grant. All four passed in the named runner with checked teardown;
  full-domain repeats and root allocation-exhaustion/absence qualification remain open.

- Qualified all eight preseeded kernel-import cases twice, including byte identity, default restoration, bounded
  networkless VM creation, stop/start, and same-content reimport. Six safe initialization cases also passed twice;
  the two administrator-enrollment cases remain excluded and unqualified, not silently skipped.

- Added real aggregate cache-preview regressions with an identity-checked guestfs sentinel outside the selected cache.
  The old candidate deleted that sentinel during dry-run; the corrected runner candidate passed both prune and clean
  previews twice, with checked cleanup and unchanged configuration. The later complete-cache gate above supersedes
  this partial checkpoint.

- Qualified eight isolated cache init, VM/network preview and validation cases twice with checked owned cleanup
  and unchanged main configuration. This selected gate did not cover aggregate preview or destructive cleanup;
  the later complete-cache gate above supersedes that limitation.

- Added acknowledged source-VM UDP policy QA: exact port delivery, sibling/adjacent-port/reverse denial, and fresh-flow
  denial after revocation. The installed-candidate case passed with checked cleanup in the named runner; spoofing,
  IP reuse, cross-network UDP, and reboot qualification remain open.

- Strengthened snapshot QA to restore one captured epoch across RAM, rootfs, and raw-volume bytes after source mutation
  and normal removal or confirmed crash. A separate installed-CLI case proves competing writable VM access refuses
  capture, with successful capture after removing only the peer. These cases passed in the named disposable runner;
  capture interruption and broader fault consistency remain open.

- Extended the independent raw-volume snapshot journey with confirmed source Firecracker crash recovery. The test
  authenticates the process and jail, signals through a retained pidfd, checks public crashed status, removes the source,
  and restores the captured volume bytes independently. Both variants passed against the 1.16.2 candidate in the named
  runner; this does not prove interruption during capture or competing-writer refusal.
- Updated the remaining snapshot flag and inspection cases to use small function-owned VMs, schema-1 JSON, and checked
  teardown. `snapshot create --pause` now starts with a running VM and proves the same process remains paused; naming
  and inspection assert exact captured identities and resources. The complete eight-case snapshot module passed.
- Added explicit `vm create --no-network` with a root-owned loopback-only per-VM namespace and no fabricated
  network/endpoint identity. A candidate installed only inside the disposable `mvmctl-030-test` runner passed
  create, stop, start, and host-to-guest vsock exec. A later focused installed-candidate test passed full networkless
  capture/restore with an attached raw volume and post-restore vsock exec; full Python system QA remains pending.
- Replaced two obsolete rootfs-copy/import persistence tests with one installed-candidate journey: `mvm cp` writes a
  guest rootfs file, a full snapshot captures it, the source changes and is removed, and `snapshot restore --resume`
  returns the original bytes. The one-case module passed in the named runner with checked VM/snapshot teardown;
  protected rootfs backing is never accessed through a caller path.
- `snapshot create` now emits its full request ID to stderr before root capture, including in JSON mode, so a caller
  retaining command output can inspect or remove a published capture after interruption. A focused same-network
  managed restore/guest-exec case passed in the named runner. An interrupted capture can still be absent from
  `snapshot ls` if its local projection was not stored; automatic discovery without the ID is not implemented.
- Completed the isolated Jailer startup handoff with root-prepared detached resource mounts, a private read-only
  sealed-configuration mount, exact pre/post-seal ownership checks, and read-only API readiness through the typed
  root broker. A Jailer root-ownership race found on repeated creation has a red-green Go regression and a second
  successful sequential create in the runner. These are focused runtime checks, not release qualification.

#### Implementation-only integration updates

- Protected-kernel removal and explicit same-content import use root-authorized retirement, retained SHA-256 and
  backing generations. VM or snapshot references refuse removal; ordinary launch cannot resurrect retired content.
  Reimport persists the candidate before recording its identity, rejects a missing or replaced recorded candidate,
  and publishes only bytes matching the retained root digest. Focused Go tests, the complete root Jailer package and
  affected race checks passed inside the named runner; installed-CLI reimport and boot remain unqualified.
- Typed guest sudo provisioning writes `/etc/sudoers.d/mvm-guest`, leaving the reserved `/etc/sudoers.d/mvm` path
  available for the nested controller's marker-only host policy. LoopMount behavioral tests passed in the named runner.
- QA volume inspection decodes the versioned JSON envelope and binds fresh mirror sources to their canonical volume
  IDs. Protected backing is reused without reseeding obsolete staging files. Explicit mirror rebuild delegates
  exact-ID removal to the controller and recreates only after success; retained references or uncertain source identity
  still refuse removal. Stale source objects left behind after cleanup prevent recreation.

- Generated cloud-init ISOs now receive checked `0600` permissions before protected import, rather than inheriting
  group-write access from the caller's umask. Behavioral Go regressions and the previously failing ISO-create case
  passed in the named runner. Retirement of already incomplete imports remains a separate release blocker.

- NoCloud prepares its inherited readiness pipe for deadline-aware writes before wrapping it. Real blocking-pipe
  Go tests cover readiness and error acknowledgments, and the formerly failing NET-create case passed inside the
  named runner. Gateway ownership, fixed TCP port, exclusive listener binding, grants and deadlines are unchanged.

- Retired unused raw-sudo network mutation helpers and the generic privileged service-spawn option. Ordinary-user
  console and NoCloud services still self-spawn; managed root effects remain on typed authenticated dispatch.
- Removed the obsolete routed service-access policy input, model, and result types. Migration `007` refuses populated
  legacy policy rows before dropping that transitional table; typed traffic and exec policies remain the public model.
- `cache clean` now uses the existing typed cleanup operations without checking for a raw `ip` tool or offering to
  rerun the ordinary command under sudo. Cleanup failures continue to propagate.
- Volume removal now reports one outcome per requested selector, including unresolved selectors and duplicates,
  so a mixed batch retains its partial failures in structured output.
- Snapshot restore now reserves the captured IPv4 address in the local lease repository before publishing its VM
  projection. A conflicting lease fails; an uncertain root restore keeps the reservation for recovery. Snapshot
  create/restore errors with a returned item now appear as partial structured output.

- Integrated schema-4 operator/execution identity separation, per-VM locked system accounts, private API/vsock runtime,
  named root-brokered control, and exact execution-UID access to persistent outputs. Ordinary exec, shell, copy, and
  agent upgrades use an authenticated connected-descriptor handoff rather than opening the private socket directly.
- Integrated protected logical backing, typed volume inspect/grow/remove, protected rootfs provisioning, and root-owned
  live-volume hotplug intent. Original caller files are staging inputs, not authoritative active disks after import.
  The mvm-created managed-cache raw volume source is removed with the protected backing only when root verifies its
  original inode, size, and change time; detected changed or replaced source files are retained. Removal is retryable
  after an interrupted protected-disk deletion. Recovery helpers bind logical cache identity and track access grants.
  This is not a runnable sign-off.
- Added the trusted snapshot capture/inspect/remove implementation, including exclusive protected data-volume capture,
  root-owned immutable artifact metadata, and recorded uncertain/paused capture progress. Stopped-source archive and
  immutable restore composition are now integrated. Restore prepares independent protected disk copies, retains
  immutable memory backing for a full restore, and keeps captured guest drive IDs and network identity. The named
  privileged restore route validates one typed request before invoking the root receiver. Networkless capture/restore
  now preserves endpoint-free placement. Snapshot RAM producer output stages in the admitted disk-backed jail root,
  not the smaller `/run` tmpfs; stopped cleanup validates only named producer artifacts. A focused installed-candidate
  networkless data-volume journey passed, while broader snapshot qualification remains open.

- Raw volume creation now allocates through an exclusive private `0600` descriptor, including under a permissive
  operator umask, so root admission accepts the resulting file without making group-writable backing authoritative.
- Volume creation rejects parsed zero-byte capacity as `validation.failed` before filesystem effects. If an opened
  raw file fails allocation, sync, close, or pre-publication cancellation, creation now attempts to remove that
  unpublished backing and retains any cleanup failure in the returned error. Pinned Go input/core-volume tests and
  25 non-VM volume lifecycle cases passed with the corrected installed candidate in the named runner; post-database-
  write uncertainty, disk-full behavior, and bridge-dependent attachment remain unqualified.
- Failed snapshot restore retains the protected snapshot reference whenever registration returns a live launch lease,
  including a post-publication durability error. For a root-confirmed failure before child spawn, or after an
  unregistered child is reaped and its snapshot reference removed, restore can remove its local VM projection and
  reserved lease. Transport failures and incomplete root cleanup still retain them for recovery. A focused
  installed-candidate Python regression proved the pre-spawn rejection without an orphan VM or leaked snapshot
  reference, and failed against the previous candidate for the intended `partial`/orphan behavior. Other
  partial-failure paths remain unqualified.

- Typed launch now admits PCI for the pinned Firecracker 1.16.1 profile, accounting for the usable PCI bus slots and
  MSI-X vectors of the complete generated device set. The authenticated child receives the transport choice and adds
  `--enable-pci`. x86 nested-virtualization configuration is carried through the sealed launch path; ARM nested launch
  remains rejected. Root-produced device metadata binds transport, generation, release, configuration digest, and
  initial volume identities for later live-volume admission. This implementation is not runtime-qualified.

- Added pressure-only cgroup policy v2: `memory.high` remains guest RAM plus configurable VMM headroom,
  `memory.max` is explicitly `max`, and swap/CPU/PID settings remain unchanged. Root admission and readback enforce
  the versioned policy; old/missing policy cannot silently launch or migrate. VMs may exceed the pressure threshold.
  Host/ancestor/userspace OOM risks remain; this does not diagnose or prove resolution of the reported saturation failure.
- `vm inspect --json` now obtains running-VM cgroup limits and usage through an authenticated, read-only root request.
  The request checks root VM ownership and process identity, then pins and verifies the fixed cgroup-v2 leaf; the
  operator cannot read its protected controls directly. A focused networkless installed-candidate system case passed
  create, inspection, stop, restart, and cleanup in the named runner. Later bounded pressure and guest-exhaustion
  qualification above supersedes this initial checkpoint; original incident attribution remains open.
- Cgroup configuration read failures now propagate instead of silently deriving zero headroom or other zero values.
- Removed unused public Jailer launch/cleanup actions, old manifest serialization, and raw snapshot/volume exposure
  operations after their typed consumers were introduced.

- Raw volumes now join typed VM launch through ordered path-free selections, release-derived MMIO admission,
  exact pinned file identity and size, sealed configuration, and descriptor-only child mounts at `/volumes/<id>.raw`.
  Read-only/cache intent is retained; cleanup removes only admitted empty jail placeholders, not source data.
  No new per-volume or aggregate storage quotas are added. This does not implement live hotplug or snapshots.

- Removed the superseded production iptables/nft CLI backends, tracker repositories, and backend selectors after
  their consumers moved to typed root services. Migration `007` refuses populated obsolete rule tables and retires
  empty tables and selector settings; it is not an adoption path for legacy host firewall state. Existing tests are
  intentionally untouched and need the later authorized verification/update pass.
- Host firewall readiness now uses one fixed typed probe that applies and reads back the modeled bridge/inet,
  connection-tracking, and NAT expressions inside a disposable network namespace. It does not test against live host
  tables. Namespace restoration failures retire the worker thread. The implementation has not been executed here.
- `host clean` discovery now observes the caller's root-owned VM inventory, including running records absent from
  SQLite. Incomplete observation fails the command. Cleanup still removes only locally registered caller networks
  through typed network removal; it neither force-stops VMs nor claims global administrator quiescence.
- Added repeated whole-state absence checks for pending-network cancellation and old-boot removal receipts. Cancellation
  publishes an identity tombstone without deleting speculative kernel state; removed/cancelled IDs cannot be reused.
- Managed VM activation now deletes only the admitted guest IPv4's stale dynamic neighbor on the exact root-owned
  bridge before host-veth exposure, rejecting static/proxy/foreign entries and proving absence. The unused raw-sudo
  whole-bridge ARP helper is removed. Pinned Go checks and a disposable runner-kernel netlink probe passed; an actual
  reused-IP managed-VM journey remains unqualified until bridge-family nftables works in the runner.
- Added shared privileged-operation and exclusive administrator host-admission leases, durable enabled/disabled state,
  and schema-2 administrator journaling. Init enables admission last, after host setup and enrollment. Old schema-1
  journals are incompatible clean-install state. The dispatcher retains shared admission through each request and
  cleanup; administrator reset retains exclusive admission and durable rollback receipts.
- Added bounded descriptor-relative deblob cleanup and distribution configuration. Fixed guest-boot actions handle
  Arch package-key initialization and Btrfs balance; they are queued, never executed on the host or reported completed
  merely because provisioning wrote them.
- Managed VM LoopMount provisioning now uses typed requests while preserving missing-account creation, root/requested
  account SSH keys, fixed seed injection, and automatic deblob. New non-root accounts use bounded UID/private-GID
  allocation, locked passwords, fixed homes/shells, bounded skeleton copying, and passwd publication last. Required
  guest-code execution is queued as fixed guest-boot work; typed failure never falls back to raw host provisioning.
- NoCloud now uses typed host-service authority and its fixed gateway TCP `8000` contract. Custom ports are rejected;
  each network supports one live batch listener. The grant represents a reserved service slot, not proof of listener
  process identity. Bind precedes grant; normal shutdown revokes while retaining the socket, then releases the port.
  Its last production legacy-tracker consumer is removed; residual legacy implementation retirement is separate work.
- Core VM launch and process control now use typed authenticated root lifecycle operations. Launch persists schema-3
  intent before effects, consumes pinned config/runtime/output resources in a fresh child-created jail mount tree, and
  commits exact active identity before close-only detach. Abort/cleanup/remove recover durable authority; same-boot
  topology and dead-process cleanup are implemented. No legacy launch fallback is used for unsupported selections.
- Typed graceful stop preserves `force=false`, requests shutdown only through an exact peer-bound Firecracker API,
  waits on the retained pidfd, and finishes authoritative cleanup. Forced cleanup uses the same admitted identity.
- The root-spawned console foundation uses a fixed system-binary marker, PTY, authenticated gate, and pinned output
  descriptors; it drops to the caller UID/GID with empty supplementary groups. Typed launch now composes this relay.
- VM list/get now propagate root observation errors; list observes the complete selection before applying a status
  filter. Paused display state is preserved only for a root-observed live process. Final VM removal runs root cleanup
  before local deletion, and public lifecycle no longer signals numeric PID projections.
- Traffic create now applies its exact root grant before local projection; remove proves revocation or exact absence
  before deleting the projection. Active and cleaned root endpoints are admitted. Stop preserves endpoint reservations
  and grants for restart; VM removal invalidates them. Root success/local projection failure is reported explicitly.
- Added typed managed-network create/remove receiver transactions. Creation applies the network firewall generation
  before raising the bridge. Core/API mutations now consume them; NAT uplink selectors and the old service-access
  production path are removed. Removal requires exact root ownership and absence of references, with durable retry
  receipts. A pending desired network retries only with exact matching inputs; missing root authority fails closed.
  NoCloud now uses its own fixed typed host-service authority; it no longer consumes the legacy tracker.
- Typed image optimization now composes bounded deblob and fstab repair with offline ext2/ext3/ext4 shrink under a
  root-owned `prepared`/`sized`/`complete` replay journal. Btrfs-to-ext4 conversion has a separate durable transaction,
  retained source snapshot, exact candidate identity, and publication recovery; conversion and shrink are separate
  requests rather than one ambiguous mutation.
- Caller staging recovery retains exact operation intent and source/directory identities, reissues the typed request,
  and publishes only a checked root result. Atomic exchange is required; the displaced original survives until
  publication durability completes. Unknown or changed remnants are preserved rather than adopted or recursively
  removed. The caller-owned recovery record is retry intent, never root authority.
- XFS now supports typed inspection, provisioning without resize, deblob, and fstab repair. Resize and shrink are
  refused before filesystem/backing mutation; XFS uses its existing capacity.
- Removed public `mvm run provision` and raw provisioning fallback. Legacy cache cleanup no longer performs arbitrary
  unmounts, and cleanup failures propagate to the CLI. Existing unprivileged archive construction remains separate.
- Deleted the four obsolete raw LoopMount entry/spawn/wire/provisioner source files; their history remains in Git.
- Sudoers generation now grants only `/usr/local/bin/mvm __mvm_privileged_v1 *`. Installation, publication, and init
  reject transitional policies. This source change was not activated on the host.
- Added global `--json` with one schema-1 success/error/partial envelope, typed result data, preserved structured
  errors, and per-item batch outcomes. Existing bare JSON reads now appear under `data`. Streaming/interactive exec,
  SSH, logs, completion, service `run`, init wizard, and console attachment reject this mode; console state/kill support
  it. Cache errors now propagate instead of becoming empty or successful results. Array-valued listings preserve `[]`
  under `data` when empty, and `env ls --json` no longer mixes human-readable output with its envelope.
- Added `mvm exec --allow-vm-exec` and API-owned directional policy enforcement, retained root authority, bounded
  source/destination relay, cancellation, and command-redacted audit. It requires schema-3 active VM identities and
  passed the installed-candidate directional policy journey in the named runner. Peer-identity attacks,
  capacity/deadline/cancellation, audit redaction, and reboot/restore remain unqualified at L2. Ordinary exec, shell,
  and copy do not enable the source session.
- Replaced the flat policy CLI with `policy traffic` and `policy exec`. Traffic list/inspect expose typed local
  projections for a source network or exact source VM; root records remain enforcement authority.
- Added early administrator host init with sudo-invoker or direct-root `--operator` selection, root-owned journal,
  resumable compiled mutation steps, operator enrollment, and typed partial results. Fixed root-owned per-operator
  records preserve prior membership for multiple operators alongside the original global state; an interrupted initial
  global transaction requires its original operator to recover first. The early parser supports JSON boolean forms,
  help, and argument boundaries. Sudo entry refuses transitional wildcard grants. Host clean uses caller root inventory;
  reset rejects live/ambiguous state and requires marker-only sudoers, then restores recorded state under exclusive
  admission. Admission remains disabled after reset or restoration failure.
- Image OS inspection copies to `images/staging/<image-id>/rootfs.img` and invokes typed read-only inspection. Existing
  staging work resumes only through its exact recovery intent and checked receiver result; unknown remnants are retained.
- Added private raw-volume identity admission, Firecracker 1.16.1 MMIO accounting, a 10,000,000-byte complete vmstate
  limit, and exact memory length from trusted captured RAM. Raw-volume launch without new storage quota caps is approved
  but its integration remains in progress. Snapshot bounds do not grant trusted capture or restore authority.
- Added retained topology/firewall endpoint transfer and detach foundations; these do not establish completion of the
  public network/lifecycle transaction.

These integration changes were implemented without adding or changing tests, running builds or tests, or performing
review or qualification. Earlier evidence below applies only to its stated earlier slices.

The following foundation entries describe earlier increments and their then-current integration boundaries. Current
composition is described above; the current incomplete-work inventory is under **Security work still pending**.

#### Canonical Firecracker Jailer launch
- VM create, start, reboot, and snapshot restore now require an exact Firecracker/Jailer release pair and run through a root-owned Jailer chroot with no direct-launch fallback.
- Release pairs are installed from a checksum-checked archive; each jail exposes only that VM's kernel, rootfs, volumes,
  runtime files, and selected snapshot. Independent privileged release verification remains a release blocker below.

#### Jailer ephemeral authority isolation
- The private Jailer instance authority now creates exact `root:root` mode-`0711` `/run/mvmctl` and exact `root:root`
  mode-`0700` `/run/mvmctl/authority`. Its global index lock, release-lock directory, per-UID authority directories, and
  per-VM locks live beneath that private authority tree; persistent instance records remain unchanged beneath
  `/var/lib/mvmctl/instances`.
- Existing managed objects are admitted only after exact ownership, mode, type, and no-symlink checks and are never
  repaired. L1 evidence covers exact modes, symlinks, concurrent creation, creation faults and descriptor cleanup,
  record scans, and lock placement.
- Added the cycle-free `internal/lib/authorityfs` leaf module and migrated Jailer's index, release, UID, and VM-lock
  authority to its closed `Jailer`, `JailerUID`, and `Lock` capabilities. Capability copies share pointer-held lifecycle
  state; checked release runs exactly once across copies, ignores cancellation, and returns the cached first cleanup
  result instead of retrying a descriptor that may have been reused. Child capabilities and acquired locks remain valid
  after their parent is released.
- Added the separate closed LoopMount UID authority and canonical VM/image lock capability beneath
  `/run/mvmctl/authority/<uid>/loopmount`. Its empty one-link `root:root` mode-`0600`
  `vm-<32-lowercase-hex>.lock` and `image-<64-lowercase-hex>.lock` files are exclusive, never unlinked during the boot,
  and serialize only the same UID, resource family, and canonical identity. Reviewed L1 evidence covers exact layout,
  unsafe-object rejection without repair, cancellation, release, contention, and unrelated-resource concurrency.
- Firewall consumes its separate closed authority capability only through the private generation
  authority/application coordinator, LoopMount registers only its exact provision/inspect internal privileged actions,
  and Network uses its family only for the private namespace and link effects described below. None of these foundations
  is a public/backend runtime cutover.
- Added a private Jailer runtime-directory lease that descriptor-relatively creates exact root-owned mode-`0711`
  `/run/mvmctl/runtime` and per-UID parents, then exclusively creates and pins one caller-owned mode-`0700` VM output
  directory. Existing VM directories and unsafe parents fail closed without repair; partial VM-directory preparation
  returns cleanup authority; copied leases share one cancellation-independent remove/release state; and cleanup removes
  only the exact empty directory after identity rechecks, with effectful-unlink and parent-sync retry reconciliation.
  Launch derives its owner and VM identity from the durable `launching` record, retains partial preparation authority,
  transfers that lease through activation or abort and active cleanup, and refuses to commit `cleaned` until the exact
  empty runtime directory is removed and released. Cleanup first unlinks only the five compiled socket/PID basenames
  through the retained directory descriptor, treats missing leaves as already clean, never follows a symlink or removes
  a directory, and leaves every unexpected name to block final removal. Fresh cleanup after a controller restart
  reopens only that exact existing hierarchy through root-controlled parents; an absent hierarchy is already clean and
  is never recreated, while unsafe state keeps the durable record in retryable `cleaning`. The directory is not yet
  mounted at `/run/mvm/runtime`; socket/PID admission, runtime relocation, and public lifecycle composition remain
  pending.

#### Enforced VM resource envelopes
- Jailed VMs now require cgroup v2 and launch with persisted, verified CPU, memory, swap, and PID limits; `mvm vm inspect` reports requested, observed, and current cgroup state.

#### Private persistent-output preparation and status observation

- Added retained preparation for fixed caller-owned one-link `0600` log/console and optional metrics files. It requires
  successful durable schema-3 launch registration with both children blocked, owns partial progress before mutation,
  truncates existing admitted files once, and publishes missing files through synced `O_TMPFILE` and no-replace links.
  Abort, activation, and cleanup transfer the same close-once capability; published files remain for inspection.
  Public launch and mount consumers remain pending, and no hard live-size cap or rotation is promised.
- Added a private batch observer for up to 1,000 canonical unique VM IDs with one ownership-index scan and at most one
  VM lock/process set. It returns closed `running`, `stopped`, and `crashed` observations or fails the complete read as
  unavailable; it never returns partial results, signals processes, or repairs records. Active checks compare persisted
  credentials, mount/root/executable metadata, and the derived cgroup path using retained pidfds without executable
  rehashing. Network namespace identity is only checked for stability between observations because schema 3 does not
  persist it. Same-boot `launching`/`cleaning` needs recovery. Public status remains unwired until public lifecycle
  operations write schema 3. The 1,000-record L1 fixture uses real files with fake process/lock effects, one owner, and
  no relays; its measurements do not qualify live 1,000-VM performance or host reboot.

#### Earlier routed-policy prototype (retired in v0.3.0)
- An intermediate `mvm policy` service-access model and dual-firewall implementation were replaced by the final
  `policy traffic`/`policy exec` families and the sole nftables backend. They are not active v0.3.0 interfaces.

#### Typed traffic and execution policy foundations
- Added separate SQLite allow-intent models and repositories for source-network traffic, exact source-VM traffic, and
  directional exact-user VM-to-VM execution grants. Schema constraints bind immutable identities, TCP/UDP port ranges,
  non-root target users, duplicate rejection, and foreign-key cleanup.
- VM invalidation removes both typed traffic families in one transaction, and execution authorization failures use a
  dedicated structured error code. Typed traffic create/list/inspect expose intent through `policy traffic`; later
  integration added root nftables application and revocation, with the complete 47-case network domain passing in
  the named runner at its recorded candidate revision.
- Added public directional execution-policy CRUD through
  `mvm policy exec create <source-vm> <destination-vm> <target-user>`, `mvm policy exec ls [--json]`, and
  `mvm policy exec rm <policy...> [--force]`. Creation resolves exact VM identities, rejects the same VM and root
  target, and derives one deterministic policy ID from source VM, destination VM, and target user without consulting
  network placement. List and removal remain isolated to the execution-policy family.
- The legacy guest-originated `remote_vm` relay was first severed before source lookup, payload parsing, destination
  lookup, vsock dial, or command logging, then removed with its generic host-frame/shared-connection path. Ordinary
  host execution and copy paths are unchanged.
- Added the protocol-only `internal/lib/vmexecwire` foundation. Every frame uses fixed `MVMEXEC1` magic, version 1,
  big-endian length, and a 64-KiB body limit. Strict closed JSON schemas separate guest-source requests from the exact
  host-built destination request, where only the latter carries the canonical lowercase UUIDv4 agent token. All
  messages bind one 32-lowercase-hex request ID and reject unknown, duplicate, case-colliding, trailing, or malformed
  values.
- The wire rejects root target users and commands over 32 KiB, caps combined stdout and stderr at 8 MiB, and caps the
  reported execution duration at five minutes. Its closed stream grammar permits `stdout` and `stderr` frames followed
  by exactly one `result` or `error` terminal and transport EOF; `cancel` is the sole request-bound control frame.
- Added the destination-agent side on the existing vsock port. Only exact `MVMEXEC1` magic enters it; ordinary
  host-exec and copy traffic retains the legacy protocol. It authenticates the configured canonical token before the
  effect, resolves an exact non-root account to numeric credentials and a clean absolute home, and runs `/bin/sh -c`
  with an explicit credential, fixed environment/working directory, and process-group cancellation.
- Destination requests, output, cancellation, and terminal delivery use effective vsock deadlines and the fixed wire
  bounds; terminal writes have a separate two-second ceiling. Generic errors and logs omit the command, token, and
  process cause. L1 covers routing, authentication order, account/credential admission, output/exit behavior, limits,
  cancellation, deadline interruption, partial-read shutdown, and redaction.
- Added an isolated source-session foundation on a dedicated already authority-admitted connection. Exact
  `MVMSRC01`/version-1 bootstrap and ready frames are capped at 512 bytes, authenticate the configured agent token
  before registration, and bind one random 32-lowercase-hex session ID to one bounded request/response stream. The
  named ordinary non-TTY exec path carries only that ID; normal exec, shell, and copy do not open a source session.
- Added `SourceSession.WaitCancel` after the source request is read. It claims the session's sole control reader and
  accepts only one exact `cancel` frame for that request ID. Malformed, non-cancel, wrong-ID, and context-cancelled
  partial input closes the transport fail-closed; explicit `Close` and context cancellation unblock a partial read.
  Separate read and write deadlines preserve concurrent response output while cancellation is awaited.
- Added a closed one-request `DestinationSession` over an already selected connection. Its sole `Open` validates and
  writes one host-built destination request, then binds a strict request-ID response reader with the fixed output
  limits. One sequential reader returns binary output but withholds either terminal until physical EOF; after open,
  one exact request-bound `Cancel` may run concurrently with response reads. Independent fixed directional deadlines,
  the opening context, and an absolute lifetime no later than five minutes bound even idle sessions. Context
  cancellation and `Close` interrupt partial I/O; protocol/transport failures close the connection, while cleanup
  runs once and returns its cached result.
- Added the process-bound source guest broker and `mvm-agent remote --user <non-root> <destination> -- <command>`.
  The outer source command may run as root or non-root and receives no token through its environment, arguments,
  filesystem, or local request. The mode-`0666` AF_UNIX
  socket authorizes through matching peer credentials, `SO_PEERPIDFD`, retained live pidfds, and a twice-stable bounded
  ancestry chain to the exact outer command. The session ID is correlation, not authority; unavailable pidfd/process
  evidence fails closed.
- Source admission is capped at eight concurrent attempts, initial local request reads at two seconds, ancestry at 64
  processes, and the complete wire at the fixed five-minute/32-KiB command/8-MiB output/64-KiB frame limits. Context
  cancellation interrupts blocked source reads/writes, outer-command cancellation kills its process group, host/local
  disconnect closes both relay ends, terminal delivery requires physical EOF, and agent shutdown releases every
  pending/bound session and pin. The CLI preserves binary output and emits only generic local failures.
- Removed the legacy `remote_vm` protocol, generic `OnHostFrame`, shared `activeConn`, and
  `internal/vsockhandler` indirection. Ordinary host execution and all copy paths remain on their existing protocol.
- Removed the VM-wide remote-exec trust bit from the fresh schema, VM model, configuration defaults, VM create
  flags/input/result, repository persistence, and environment specifications. Fresh databases now finish at schema
  version 6 without the removed column. Schema-version 3, 4, or 5 databases that still contain it fail before pending
  migration inspection or migration application with an explicit clean-install-required validation error and remain
  unchanged; no legacy bit is converted into a directional exec policy.
- Added a typed fixed-schema VM-exec audit sink that writes complete command-redacted JSONL records to the dedicated
  user-owned `vm-exec-audit.jsonl` cache file. Schema 1 binds the operation, request/source/destination identities,
  non-root target user, start time, duration, closed result, and result-consistent optional exit code. It never records
  the command, agent token, or process cause. Construction rejects an effective root caller because this user-cache
  pathname is not retained descriptor-relatively; the user can modify the file, so it is observability rather than
  tamper-proof root authority. Writes and close are serialized across copied sink values, incomplete or failed writes
  poison later records, and validation, write, and cleanup failures remain typed and visible.
- The API-owned opt-in relay now composes execution-policy CRUD, source/destination sessions, destination execution,
  retained root authority, directional policy enforcement, payload forwarding, and audit. It requires eligible
  schema-3 active identities; Python L2 qualification remains pending. The implemented cancellation boundaries are
  process-group
  guarantees, not per-command cgroup authority, and do not claim cleanup of descendants that deliberately leave their
  group.

#### Private VM-exec authorities, descriptor admission, and retained-sidecar foundation

- Added strict schema-1 root-owned capacity reservations at
  `/var/lib/mvmctl/allocations/exec/<32-lowercase-hex>.json`. Each one-link mode-`0600` record beneath exact mode-`0700`
  managed directories binds a root-generated reservation ID, non-root owner, exact source/destination VM IDs and launch
  generations, host boot ID, and `CLOCK_BOOTTIME` creation/expiration.
- Added the never-unlinked `/run/mvmctl/authority/capacity.lock`. It serializes the complete scan, stale cleanup,
  ceiling check, and no-replace publication across CLI processes and users. Corrupt or ambiguous state fails closed
  before canonical pruning; only admitted crash temporaries, prior-boot reservations, and expired reservations are
  removed and directory-synced.
- Publication uses exclusive temporary creation, stable metadata, file sync, checked close, no-replace rename, and
  parent sync. Creation, deletion, partial pruning, durability uncertainty, and cleanup-required results preserve
  explicit error evidence, including a releasable lease after post-rename creation failure.
- The future relay-stop deadline is reservation creation plus the fixed five-minute
  `vmexecwire.MaxExecutionDuration`; setup and suspend consume that budget. Record expiration adds a separate four-second
  terminal/cleanup grace.
- Added a private active-pair authority that acquires the instance index once, admits two distinct same-owner `active`
  records, opens their authorities separately, and locks canonical VM IDs in lexical order. It retains the shared
  roots, both active leases, and both exact pidfd-bound process sets; proves both complete identities and final
  liveness; reserves root-global capacity; then rechecks boot, both identities, and both final locator/liveness proofs
  before publishing an immutable owner/source/destination launch-generation admission.
- The pair lease cleans up once in reverse order: destination/source peer pidfds, capacity, destination/source process
  capabilities, descending lexical active leases, then pair roots.
- Added exact Firecracker peer authority for both retained VM-exec connections. Admission obtains `SO_PEERCRED` and
  `SO_PEERPIDFD` from the same socket, immediately owns and verifies the close-on-exec pidfd, matches PID and effective
  UID/GID to the admitted record, and proves exact PID/start, complete active identity, retained process identity, and
  liveness. Destination binding finishes with a bounded last-look sweep of both peer pidfds and both retained process
  sets before the final host-boot and relay-budget check.
- Selected a fixed production ceiling of eight host-global sessions and composed it with the active-pair and peer
  authorities behind the retained session server used by the exact registered action.
- Added a private absolute `CLOCK_BOOTTIME` relay-stop primitive backed by an absolute `timerfd`, bounded polling,
  cancellation, a single concurrent waiter with a cached result for later calls, close-once lifecycle, and
  cleanup-error evidence. The retained session server now arms it from the capacity admission deadline, and the handler
  closes both retained connections and releases authority when the stop completes.
- Added a private exact-one VM-exec descriptor transport. Its single-use sender puts one duplicated connection in one
  `SCM_RIGHTS` message on the first non-NUL frame byte, sends the remainder with no ancillary data through
  cancellation-bounded nonblocking I/O, and closes its duplicate exactly once even across copied owners and failures.
- Added strict structural descriptor admission. The receiver uses `MSG_CMSG_CLOEXEC` on every read, owns all installed
  descriptors before validation, rejects missing, extra, unknown, late, or truncated control data, and retains only
  one close-on-exec connected, non-listening `AF_UNIX` `SOCK_STREAM`. It exposes no transfer method and makes no peer
  process, Firecracker, VM, guest-port, or policy identity claim.
- Added the closed versioned `jailer-vm-exec-session` protocol and ordered retained process transport. One root child
  uses the existing system binary, privileged invocation, and fd-0 AF_UNIX control socket; source and destination
  duplicates accompany only their exact setup phases, while cancellation, process-group termination, waiting, and
  cleanup remain bounded and shared across copied session owners.
- Added the named Jailer VM-exec session client. It duplicates caller-owned source and destination `net.UnixConn`
  values for the sidecar, preserves the caller's originals, exposes only ordered open/bind/release/cancel operations,
  and requires a successful released outcome plus physical response EOF.
- Added the root handler lifecycle and phase-specific descriptor receivers. The handler retains source before
  acknowledgement, retains and binds destination before ready, cleans destination/source/effect authority once on
  explicit release, EOF, setup, or response failure, and reports released only after successful cleanup. It accepts no
  path, token, user, command, policy material, relay payload, or raw descriptor accessor.
- Registered only the exact `jailer-vm-exec-session` privileged action. Production construction composes its handler
  with the root-owned pair/capacity/peer authority and absolute stop. The caller UID/GID passed into that authority
  comes only from the authenticated control peer identity, never from action request bytes. Source and destination
  descriptors remain opaque and pinned by handler ownership during their sole synchronous binder calls. Once
  dispatched, the handler owns every action-specific response and all retained connection/authority cleanup; the outer
  entry owns authenticated pre-dispatch `MVMXSES1` failures.
- Authenticated pre-dispatch failures for this action use one bounded `MVMXSES1` terminal failure without draining
  request input; other actions retain the ordinary `MVMRES01` envelope. No API-owned relay listener/consumer invokes
  the registered action yet, and runtime directional-policy enforcement, audit-sink invocation, relay payload
  forwarding, a positive VM-to-VM path, and Python L2 qualification remain pending.

#### Typed nftables compiler foundation
- Pinned `github.com/google/nftables v0.3.0` and added a private typed compiler for fixed `mvmctl_bridge` and
  `mvmctl_inet` tables and their fixed prerouting, forward, input, and postrouting chains. Bridge prerouting uses raw
  priority for source MAC/IPv4/ARP anti-spoofing and unsupported-L3 guards; bridge forward compiles same-network ARP,
  established-reply, typed allow, and default-deny policy.
- The inet generation compiles routed typed allows, established replies, managed host-service access, inbound/outbound
  default deny, and conditional internet egress/NAT. A network with masquerade disabled receives neither an uplink
  egress allow nor a masquerade rule. Invalid resolved intent rejects overlapping managed prefixes, prefixes without
  usable hosts, network/broadcast addresses, and conflicting host/VM addresses.
- The caller no longer supplies generation identity. The compiler derives SHA-256 from its versioned canonical closed
  projection of normalized intent and the exact managed table, chain, rule, expression, order, and userdata schema;
  the userdata generation slot uses a fixed placeholder while hashing to break self-reference, then every rule is
  bound to the derived digest and its rule identity. Explicit empty intent produces an empty generation with no
  reserved table or rule output.
- The projection covers every field of exactly the eight `github.com/google/nftables v0.3.0` expression structs the
  compiler emits: `expr.Meta`, `expr.Masq`, `expr.Bitwise`, `expr.Cmp`, `expr.Ct`, `expr.Payload`, `expr.Range`, and
  `expr.Verdict`. Typed-nil and unknown expressions fail closed. Digest encoding streams through a fixed 64-KiB buffer
  directly into SHA-256; validation, encoding, and finalization errors surface to the compiler caller. A module
  upgrade or expression-set change requires an explicit schema audit and digest-version bump.
- ARP admission now requires Ethernet/IPv4 request/reply grammar and the complete 28-byte payload. Header and opcode
  guards precede fixed sender-address checks, and the private application validator verifies them. Malformed,
  truncated, spoofed, and valid cases have L1 coverage; live kernel packet qualification remains pending.
- At this compiler-foundation milestone, the representative 1,000-endpoint/1,000-grant fixture emitted 11,007 rules
  and used 243,345 allocations normally and 243,346 under race instrumentation, below its then-250,000 ceiling.
  The same-network inet companion added later in this release changes that measurement and ceiling; see Fixed below.
- The compiler itself remains effect-free. Its sealed output is now consumed by the separate private application and
  reconciliation foundation below; no public caller or production policy path invokes that foundation.

#### Firewall generation authority foundation
- Added the separate never-unlinked root-owned `/run/mvmctl/authority/firewall.lock` capability plus a strict
  committed/applying generation record and bounded codec at `/var/lib/mvmctl/firewall/generation.json`.
- Added descriptor-relative managed-tree admission and exclusive first publication with exact root ownership/modes,
  stable bounded reads, no-replace rename, fsync, and post-rename creation/durability evidence. The first-publication
  path never repairs or overwrites existing state.
- Added a private acquisition seam that requires earlier network authority, then admits only a record bound to
  the current boot and pinned host network namespace. Store, lock, and authority cleanup is pointer-shared, close-once,
  cancellation-independent, and caches its first result.
- Added evidence-bound atomic replacement of an admitted generation record. The store re-admits the exact record and
  file identity before rename, reads the replacement back afterward, preserves `record_replaced` and
  `durability_uncertain` evidence, and blocks later replacement through a lease whose outcome became ambiguous.
- The foundation deliberately rejects a missing record because only qualified kernel inspection may establish initial
  truth. Qualified initial inspection/adoption, privileged/public wiring, and system qualification remain pending.

#### Bounded raw nftables snapshot foundation
- Direct `github.com/mdlayher/netlink` use in the private firewall adapter supports strict read-only raw observation
  because the pinned structured decoder can omit unknown expressions. It also supports the retained application's
  socket lifecycle; `github.com/google/nftables` remains the sole compiler and rule-mutation engine, with library and
  netlink types private to the adapter.
- Added a private raw-netlink collector that brackets one logical observation with ruleset-generation reads and chooses
  only exact read-only lookups for the two managed tables, five fixed chains, their rules, and the absence of sets,
  objects, and flowtables.
- Each request uses a fresh checked transport while the complete snapshot shares a 32-MiB/20,000-message receive
  budget. The strict decoder rejects malformed or unknown attributes/expressions, partial managed state, inconsistent
  base sequences, generation drift, forbidden object classes, and cap violations.
- Added a retained namespace authority that requires the earlier network and generation capabilities to agree, pins
  and rechecks the exact current host network namespace, and supplies fresh strict `NETLINK_NETFILTER` transports to
  the collector through that retained descriptor.
- The collector and retained factory now serve the private application coordinator for already admitted records. No
  production path calls that coordinator, no real-kernel result is qualified, and no initial adoption, privileged
  action, public behavior, or enforcement follows from it.

#### Private nftables kernel application and reconciliation foundation

- Added a closed coordinator that retains the generation lease's application mutex across the complete
  record -> kernel -> record transaction. Copied lease applications, standalone record replacement, and generation-lease
  `Release` are serialized while the host-global firewall lock remains retained. Each observation or application
  connection separately revalidates the caller-supplied retained namespace lease under its own lifecycle lock. The
  application mutex neither retains that namespace lease nor serializes its release, so it may otherwise be released
  between calls.
- Before mutation, application validates the compiler-bound seal, exact resource inventories, table/chain/rule shape,
  terminal default-deny invariants, applying record, and exact observed-before snapshot. Fixed names or a digest alone
  never grant deletion authority.
- Added an exact-host-netns lasting `nftables.Conn`. Construction enters the retained namespace on a disposable locked
  OS thread, restores and verifies the original namespace before return, verifies 32-MiB socket buffers, and applies
  the earlier of the caller deadline or a 30-second bound. One replacement queues exact observed-table deletion and
  the complete target, calls `Flush` once, then closes the lasting connection.
- Cancellation expires only the retained socket's deadline while `Flush` owns it, preventing a transient connection
  from being redialed outside the selected namespace. Cancellation, interrupt, flush, and close failures are preserved,
  and cancellation cannot produce success.
- The durable applying record precedes mutation. Exact-before authorizes one flush, exact-target takes the
  record-completion path without replay, and unknown retains applying authority. After a commit attempt, one fresh
  cancellation-independent five-second inspection classifies before/target/unknown under the same transaction and
  firewall locks. That observation separately revalidates the caller-supplied namespace lease under its lifecycle lock.
  Exact-before attempts to restore the prior committed record and returns an error; exact-target attempts to commit
  while retaining any application error; unknown remains recoverable with explicit outcome evidence.
  Record-finalization failures remain explicit, and ambiguous committed replacement blocks later mutation.
- Applied empty means both reserved tables are absent. Exact empty-to-empty verifies absence through fresh raw-netlink
  observation without a mutation/`Flush` transaction; an exact admitted non-empty generation can delete both reserved
  tables in one batch. Partial, foreign, mismatched, corrupt, wrong-boot, wrong-namespace, missing-record, and ambiguous
  state cannot authorize deletion.
- This is private L0/L1 evidence only. Qualified initial inspection/adoption, the root/`CAP_NET_ADMIN` threat-model
  decision, production privileged/public composition, live policy and link enforcement, legacy backend/CLI removal,
  direct capability probes, real-kernel/Tier tests, and release qualification remain pending.

#### Persistent managed-network authority foundation
- Added a private strict schema-1 managed-network record, exact codec, and descriptor-relative store. Root persists only
  owner UID, network ID, current boot ID, and exact derived bridge name/ifindex/host-namespace identity at
  `/var/lib/mvmctl/networks/<uid>/<network-id>.json`; managed directories are root-owned mode `0700` and one-link records
  are root-owned mode `0600` with a 4-KiB ceiling.
- Atomic no-replace publication preserves created-record and durability-uncertain evidence after rename. A closed
  `authorityfs` Network family and private registration authority serialize the global network-ID index and each
  network, deny foreign or prior-boot claims, admit an exact same-owner/current-boot bridge replay, and release all
  retained authorities in exact reverse order with pointer-shared cached close-once cleanup.
- This foundation is inert. The registration entry point is private and unreferenced, does not receive the Jailer VM
  lock, and creates no persistent VM topology, namespace, link, TAP, route, firewall state, privileged action, or public
  behavior.

#### Per-VM network topology transaction foundation
- Added a private root-side transaction that derives the shared managed bridge from owner/network identity and every
  VM link and namespace handle from owner/VM identity. It requires the earlier VM-lock capability, retains the network
  lock and namespace pin, validates returned kernel identities, and rolls back mutation-plus-error states in reverse
  order without allowing cancellation to suppress cleanup.
- Only the exact committed state with both leases and every namespace/local-bridge/TAP/veth creation marker can expose
  topology authority. Building, cleaning, cleaned, missing-lease, and partial-rollback states remain unavailable even
  when they retain stale identity fields. Lock-acquisition `DomainError` identity/details survive rollback, with
  `cleanup_required=true` added only when retained authority still needs cleanup.
- The foundation remains intentionally inactive until the applicable Jailer VM-lock lease is transferred into network
  registration/topology and the nftables, route/address, link-activation, Jailer `--netns`, persistent VM-topology,
  and qualification stages are implemented.

#### Private canonical L3 and exact link-state effects

- Added private canonical prefix/gateway/guest/MAC admission and namespace-bound IPv4 address/route codecs.
- Added exact managed-bridge and down-loopback address preparation, state-dependent loopback route verification,
  and per-role raise/lower effects. Routes are observed as kernel-created state, not inserted directly.
- Re-admitted topology identity before and after address mutation, rejected a down shared managed bridge, and
  preserved partial/unknown-outcome and close-error evidence. Firewall-gated activation sequencing, production
  composition, and live-kernel/system qualification remain pending.

#### Private per-VM network-namespace authority and effect
- Added a closed Network authority capability for the exact root-owned
  `/run/mvmctl/authority/<uid>/netns/<vm-id>` placeholder. It retains and rechecks directory, leaf, and mount identity,
  returns cleanup authority after partial mutation, and removes/releases the exact target once with cached cleanup
  evidence.
- Added a private namespace effect that creates a network namespace on a disposable locked OS thread, pins and publishes
  its exact nsfs mount with `open_tree`/`move_mount`, verifies restoration of the host namespace, and performs
  identity-bound non-lazy unmount plus placeholder cleanup with explicit uncertain-outcome details.
- Added the private typed link adapter and composed it with the namespace effect behind the complete `TopologyEffects`
  boundary. It creates, attaches, inspects down, and identity-bound deletes the exact local bridge, veth pair, and TAP;
  every rtnetlink request is bound to an explicit retained namespace descriptor.
- TAP creation enters the retained VM namespace on a disposable locked thread, opens only fixed `/dev/net/tun`, and
  uses `TUNSETIFF` plus `TUNSETOWNER` with no group authority. It rechecks the exact owner, no-PI state, down state, and
  managed alias before `TUNSETPERSIST`, then verifies the persistent TAP through rtnetlink.
- Corrected managed TAP ownership for isolated Jailer launches: root binds the TAP to the VM's isolated Firecracker UID,
  not the operator UID, and persists that UID for activation, recovery, and exact cleanup checks.
- Fixed the pending production L3/authority contract: each per-VM namespace is an L2 extension of a canonical RFC1918
  prefix no longer than `/30`; only the managed bridge and guest are numbered; the default gateway is the first usable
  address; fabric links remain unnumbered; loopback is `127.0.0.1/8`; and routes have no explicit metric. New default
  guest MACs are the deterministic versioned `02:fc` owner/VM derivation. Callers supply no raw names, paths, routes,
  descriptors, netlink requests, or `ip` arguments.
- The accepted activation contract consumes the real Jailer VM lock's opaque `TopologyHold`, requires a freshly
  verified opaque firewall endpoint lease, and raises loopback, local bridge, TAP, namespace veth, then host veth.
  Network then firewall mutation locks remain held through apply and activation; a pre-release failure rolls back with
  the same locks. Success releases firewall then network while opaque authority remains. Later cleanup reacquires
  network then firewall under the retained VM lock. Durable root state retains selected L3/MAC values, boot-scoped
  observed identities, endpoint identity, and lifecycle progress while re-deriving names, paths, routes, and order.
- Fixed the target NAT authority: public intent retains only enabled/disabled and removes `NATGateways` interface-name
  selection. For enabled NAT, root discovers exactly one eligible IPv4 main-table default-route uplink through typed
  host-netns rtnetlink, persists its ifindex/name/kind/namespace identity, and revalidates it. Zero, multiple, changed,
  or multipath uplinks fail closed; multi-uplink selection is unsupported.
- The private foundations do not complete this runtime contract. Production route/address and link activation,
  firewall endpoint capability/application, `TopologyHold` and Jailer composition, durable reconciliation, public
  lifecycle cutover, `NATGateways` input/model/compiler removal and tests, and system QA remain pending. Incompatible
  `/31` or `/32`, non-private, schema-1, mutable-name, host-global, or ambiguous legacy state will be refused as a clean
  break rather than adopted or automatically deleted.

#### Typed LoopMount privilege foundations
- Added named `ProvisionVM`, `InspectImage`, and `OptimizeImage` clients with fixed actions and closed VM/image request,
  plan, and result types. VM resize is grow-only from the admitted size; results must match the request's identity,
  filesystem, and size semantics. Protocol admission bounds usernames, SSH keys, agent tokens/ports, sizes, and counts
  without exposing a generic call, command, mount target, operation list, or caller-selected resource path.
- A success or remote `DomainError` is authoritative only for a started process with actual response EOF. Valid remote
  error details win over later diagnostics; every non-authoritative transport outcome has stable `process_started` and
  `outcome_unknown` details.
- Added a private receiver-side authority that pins canonical managed-cache components and the fixed VM or image
  `rootfs.img` through retained descriptors. VM provisioning opens only `vms/<vm-id>/rootfs.img` read-write; image
  inspection and optimization open only `images/staging/<image-id>/rootfs.img` read-only or read-write respectively.
  Caller ownership, safe modes, one-link regular-file identity, exact bounded size, local-filesystem topology, mount
  identity, no-cross-mount descendant traversal, and reverse checked cleanup are enforced before a lease can become
  authority. L1 tests deterministically replace every caller-owned ancestor and leaf immediately before and after open.
- Added a deterministic rootfs mount-source policy over pathless receiver-derived probes. It accepts at most 16
  partition probes. A raw source is accepted only when no partitions exist and its filesystem exactly matches the
  request; otherwise exactly one partition must match. Malformed probes, reused or duplicate block identities,
  duplicate partition numbers, raw-plus-partition ambiguity, and missing or multiple matches fail closed. Receiver
  discovery now admits a stable bounded partition set from fixed sysfs, retains the selected block identities, probes
  filesystems only through fixed `/usr/bin/blkid` with bounded output/exit policy, and binds the selected source to the
  exact probed identity without exposing a path or descriptor.
- Added a pure loop-device kernel contract. It admits only `/dev/loop-control` as character device `10:237` and
  derives `/dev/loop<N>` only for a 20-bit index whose block identity is major `7` and minor exactly `N`. Its
  `unix.LoopConfig` binds the exact admitted rootfs descriptor with `LO_FLAGS_AUTOCLEAR|LO_FLAGS_PARTSCAN`, adds
  `LO_FLAGS_READ_ONLY` only for read-only access, and leaves every other field zero without legacy setup or direct I/O.
  Invalid values fail closed through the private LoopMount `DomainError` path. The pure constructor itself performs no
  I/O; the separate private capability below consumes the contract.
- Added a private loop-device attach/release capability. It opens only fixed `/dev/loop-control` and derived
  `/dev/loop<N>` leaves with `O_RDWR|O_CLOEXEC|O_NOFOLLOW`, then rechecks their exact identities with `fstat`. Each
  candidate comes from `LOOP_CTL_GET_FREE`; configure-time `EBUSY` is bounded to 16 attempts. One atomic
  `LOOP_CONFIGURE` binds the admitted rootfs descriptor and access mode. A failed configure is never followed by
  `LOOP_CLR_FD`, because a competing process may own that device.
- Successful attachment returns a private lease with no path or descriptor accessor. Its checked, idempotent,
  cancellation-independent release issues `LOOP_CLR_FD` and closes the loop descriptor, while `AUTOCLEAR` remains
  receiver-crash safety. If loop configuration succeeds but closing loop-control fails, the configured lease is still
  returned with the error so it can be released.
- Added a read-only image-inspection adapter. It enters a private mount namespace on a locked OS thread, makes
  propagation private, mounts the admitted source only at fixed `/run/mvmctl/rootfs` with
  `ro,nosuid,nodev,noexec`, revalidates the exact source/mount/image identities, and reads only bounded fixed
  `etc/os-release` or `usr/lib/os-release` leaves through no-symlink/no-cross-mount traversal. It unwinds root,
  unmount, source, partition, loop, and backing-image authority in reverse order, preserves the primary structured
  error with bounded cleanup evidence, and returns `unchanged` only after exact admitted/statx stability; uncertainty
  returns `outcome_unknown`.
- Added a private `ProvisionVM` adapter. All guest reads and writes remain descriptor-bound beneath the admitted
  mounted root. Its fixed content set covers hostname/hosts, DNS, existing-account SSH/sudo, fixed SSH service, embedded
  agent, fstab, root-only persistent `/etc/mvmctl/agent.token`, and exactly one admitted systemd `sysinit.target` or
  OpenRC default-runlevel enablement. Fixed ext-family/Btrfs grow tools have ext4/Btrfs convergence evidence from the
  exact old or target backing size; `/usr/bin/blkid` is capped at five seconds and grow tools at five minutes, with any
  earlier caller deadline winning. Reverse cleanup preserves the closed mutation, durability-uncertain, and
  unknown-outcome evidence.
- The production composition root now constructs only the provisioning/inspection server, binds the exact embedded
  agent accessor, and registers exact `loopmount-provision-vm` and `loopmount-inspect-image` receiver actions. The
  ordinary backend has not cut over, and live-kernel qualification remains pending.
- Added a private receiver-transaction foundation with two action-specific effect seams:
  `rootfsEffects` contains exactly `ProvisionVM` and `InspectImage`, while private `imageOptimizeEffects` contains only
  `OptimizeImage`. Each action validates only its effect dependency plus the shared rootfs authority and per-resource
  lock seam. The coordinator unwinds effect cleanup, rootfs authority, then the lock with cancellation detached;
  derives closed mutation, replacement, durability, and unknown-outcome evidence; and preserves primary structured
  errors through bounded cleanup diagnostics. VM grow replay admits only the exact old or target size and hands the
  stable actually admitted size to the effect seam.
- Added the reviewed private service adapter for that lock seam. It derives only the authenticated caller UID and the
  canonical VM/image key from the closed rootfs selection, maps cancellation and deadline failures as retryable, retains
  partially acquired authority for cleanup, and releases the lock before its parent capability exactly once. The
  production provision/inspect server uses this adapter.
- The optimize seam has no production implementation or construction, receiver handler, or dispatcher registration.
  Provisioning and read-only inspection are reachable only through their exact internal privileged actions; no
  live-kernel qualification, backend/public cutover, or legacy raw-sudo removal has landed. `OptimizeImage` stays
  unregistered until durable shrink and replacement replay authority exists; `mvm run provision` and its arbitrary
  unmount/cleanup surface remain transitional release blockers.

#### Root-owned single-binary installation
- Added the exact administrator bootstrap command `sudo <trusted-mvm-binary> host install-system`, which installs the
  same `mvm` artifact at `/usr/local/bin/mvm` as `root:root` mode `0755`.
- System installation uses descriptor-relative, no-symlink traversal; an exclusive temporary file; complete-copy and
  ownership verification; file and directory `fsync`; and atomic rename.
- `host install-system` is dispatched before ordinary application initialization and ignores user cache, configuration,
  database, logging, and environment overrides.
- `host init` now verifies the canonical system binary before reconciling the managed sudoers drop-in. System installation
  is deliberately separate from host initialization.
- Sudoers reconciliation now pins the fixed root-owned directory chain and one-link `0440` policy, validates the
  generated content through bounded absolute `visudo` on stdin with a fixed environment, and publishes by file fsync,
  atomic rename, and directory fsync. Failures retain replacement/durability evidence. The transitional grants are
  unchanged; early administrator enrollment and root journaling are now implemented, while trusted reset cleanup and
  marker-only policy activation remain pending.

#### Privileged-dispatch foundation
- Added the reserved, versioned privileged entry point to the root-owned `mvm` binary. Selection occurs before Cobra or
  normal CLI initialization and malformed reserved invocations fail closed instead of falling through.
- Added strict bounded JSON parsing with unknown, duplicate, case-colliding, trailing, oversized, and structurally invalid
  input rejection.
- Added the version-1 `MVMREQ01`/`MVMRES01` codec with 64 KiB JSON frames, receiver-bounded payload declarations,
  exact action/schema matching, read-only typed success/error outcomes distinct from protocol failures, and bounded
  `DomainError` normalization that omits wrapped causes and sensitive process-output details.
- Added the fixed fd-0 Unix socketpair client transport for
  `/usr/bin/sudo -n -- /usr/local/bin/mvm __mvm_privileged_v1 <fixed-action>`. It uses close-on-exec endpoints,
  concurrent bounded upload and response handling, actual peer EOF, early-response upload interruption, cancellation,
  bounded diagnostics, and bounded process reaping without extending the generic command runner. Trusted release now
  has a named capability client; the remaining privileged capabilities are still release blockers below.
- Added root-side fd-0 receiver admission. The production entry owns `os.Stdin` directly, sets and verifies `CLOEXEC`,
  rejects initially nonblocking or non-`AF_UNIX`/`SOCK_STREAM` descriptors, requires a positive `SO_PEERCRED` PID, and
  matches the peer UID/GID to the authenticated sudo caller before environment sanitization, executable verification,
  group authorization, or action dispatch.
- Caller and receiver now share one exact action grammar: 1 through 64 ASCII bytes, an alphanumeric first byte, then
  only alphanumeric bytes, `.`, `_`, or `-`. Malformed invocation and structural channel-admission failures remain
  unframed; after admission, failures return one bounded matching `MVMRES01` error without draining a rejected action.
- Root writes the complete response before `SHUT_WR` with raw `SendmsgN(MSG_DONTWAIT|MSG_NOSIGNAL)` and bounded
  `POLLOUT` waits against the fixed absolute response deadline. This remains deadline-safe if a duplicate descriptor
  later changes shared `O_NONBLOCK` state and makes response EOF observable independently of process exit.
- Removed the obsolete standalone EOF-delimited privileged JSON decoder. The strict length-framed codec is now the only
  request framing foundation.
- Added closed internal trusted-release install/remove values, a concrete root authority server, and directly tested
  receiver handlers. Nonzero archives stream once through Jailer-owned exact-length and EOF admission; zero-payload
  install and remove require EOF before effects. Raw nonblocking receive with absolute `POLLIN` deadlines provides
  bounded cancellation, results receive semantic authority checks, and each response gets a fresh bounded deadline
  before `SHUT_WR`.
- Added the named trusted-release install/remove client and registered only its two exact privileged actions. The client
  transfers valid archive ownership exactly once, requires actual response EOF, rejects impossible pre-start response
  evidence, preserves valid root `DomainError` details over process diagnostics, and repeats closed success-semantic
  validation. Dispatch distinguishes handler-owned response failures from unknown actions so it never emits a second
  frame; focused normal and race tests cover both routes and failure boundaries.
- Added authenticated sudo identity and current `mvm` group checks, fixed root environment sanitization, and executable
  device/inode verification against the root-owned `/usr/local/bin/mvm` image.
- Added ADR-0016, which defines the single-binary privilege boundary, trusted state layout, typed operation catalog,
  clean-install constraints, and fail-closed system-binary replacement behavior.

#### Root-owned VM authority substrate
- Added a private typed VM authority under `internal/service/jailer` with durable owner-bound `registered`, `cleaning`,
  and `cleaned` records beneath `/var/lib/mvmctl/instances/<uid>` and persistent ownership tombstones.
- Added descriptor-relative, no-follow traversal; strict bounded record decoding; atomic durable replacement; reusable
  root-owned runtime locks; and the fixed `release -> global index -> VM` lock order.
- Prepared release leases now lock the canonical architecture/version store slot before identity resolution. Launch
  registration transfers that same lease only after durable authority state; removal/replacement retains it while
  checking exact references. Competing identities cannot race through different hash-derived locks.
- Global VM-ID claims now fail closed on foreign, duplicate, corrupt, unreadable, or inconsistent authority records, and
  trusted release removal can acquire a lease only when no active authority record references the exact release.
- This substrate is private and not yet wired into public VM lifecycle operations; that integration remains a release
  blocker below.
- Replaced the private schema-1 record with the strict schema-3 record/codec and pure
  `launching -> active -> cleaning -> cleaned` transitions. Decoding rejects schema 1/2 and invalid lifecycle shapes;
  release identity is validated against the persisted manifest hashes.
- `preparedRelease.registerLaunching` now derives release identity internally and, after durable record replacement,
  transfers the pinned store, release-slot lock, executable admission, and VM lock into a launch lease. Prepared and
  launch values use pointer-shared mutex-protected close-once cleanup and cache the first result.
- Added private blocked-process authority for an already-created Firecracker child and optional relay. Fresh
  challenge/acknowledgement gates fail closed on EOF, retained pidfds require close-on-exec and derive exact PID/start
  locators, and release registration transfers them with the pinned receiver executable into durable `launching`.
- Added descriptor-pinned admission for an already-absent fixed Jailer destination and an immediate pre-handoff absence
  recheck. After Jailer creates the root and copied executable, private activation binds their exact identity, observes
  the retained task through bounded strict `/proc` parsing, seals the root/copy to `root:root 0755`, verifies the
  manifest digest, and can replace `launching` with `active` under the existing post-rename lease contract.
- Private activation repeatedly verifies boot ID, PID/start, pidfd liveness, all credentials, empty supplementary
  groups, exact derived cgroup, topology-selected network namespace, final mount namespace/root/executable, and optional
  relay identity. The executable-transition loop is bounded to 10 seconds with a 25-millisecond minimum interval and
  stops immediately on pidfd-observed exit.
- Added consuming private launch abort and active cleanup plus fresh-recovery cleanup bounded to the recorded
  Firecracker and optional relay. Cleanup signals only through pidfds, waits and reaps before writing `cleaned`, accepts
  exit readability and post-reap `POLLHUP` as dead, and keeps retryable failures in `cleaning` with their process and
  VM-lock authority. A boot mismatch opens and signals no pidfd; same-boot active cleanup requires the complete recorded
  identities and executable digests.
- Process, prepared-destination, destination, active, cleanup, and recovered-process capabilities use pointer-shared
  close-once state and cache their first cleanup result. This graph remains private: production child spawning and
  launch composition, topology/endpoint authority and mutation-lock composition, final live topology verification,
  full runtime cleanup and reconciliation, public wiring, and system qualification remain release blockers.
- Added a private fixed-name cgroup-v2 authority to the schema-3 lease graph without storing a cgroup path in the
  record. It pins and exactly checks the root, compiled parent, and VM leaf plus mount identity; refuses legacy-leaf
  adoption; proves fresh absence through the last handoff recheck; and requires successful Jailer-handoff provenance
  before binding. Activation proves the pidfd/start-tick-bound Firecracker is the sole member and preserves the same
  lease for retry. Authority transfers launch -> active -> cleanup; abort binds only after exact task reap; and cleanup
  removes only the exact empty unreplaced leaf before durable `cleaned`, with retryable removal and terminal
  post-removal close convergence. Fresh recovery and control integration are described below; same-boot dead-process
  reconciliation, replacement of `entry.go` path-based prepare/remove, production/public create/start/stop wiring, and
  live L2/Tier 3/system qualification remain pending. This private increment is not production host enforcement or
  release evidence.
- Integrated fresh cgroup cleanup admission into the private lifecycle. Recovery reopens the record's exact processes,
  accepts the active or launching Firecracker identity, durably transfers `cleaning` authority, and re-admits the fixed
  leaf only when that task is its sole member. Admission or final-check failure keeps the same cleanup lease and cgroup
  work pending for retry. A boot mismatch requires stable leaf absence and rejects a present or unstable leaf;
  launching recovery retains provisional absence through exact task reap. Only fresh-recovery cgroup admission
  callbacks run outside the `cleanupLease` mutex; ordinary `Complete` still serializes cleanup effects under it. Schema
  3 still stores no cgroup path. Same-boot dead-process reconciliation, raw `entry.go` replacement, public production
  wiring, and live/system qualification remain pending.
- Added private typed policy-v1 cgroup control for CPU quota/period/weight, memory high/max/swap max, and an exact PID
  maximum. Preparation proves fixed `cpu`, `memory`, and `pids` readiness at the root and then parent, enables only
  missing controllers additively with descriptor-relative readback, and repeats readiness immediately before Jailer
  handoff. Activation reads back all six fixed leaf values exactly before sole-membership verification and repeats
  limit plus membership checks on retry without replacing the retained authority. Recovery and launches without a
  cgroup intent perform no controller mutation. Relay and Firecracker handoff progress is independent, so retry after
  relay success resumes only the pending Firecracker release. Focused L1 evidence covers missing-only controller
  writes/readback, ordering, final readiness drift, strict limit parsing/mismatch, same-authority retry, no-mutation
  recovery/no-intent paths, and partial relay handoff. This remains private and is not production host enforcement.

#### Private trusted-release authority substrate

- Added root-owned, descriptor-pinned trusted-release admission with fixed source derivation, independently checked
  digests, a closed parser for the audited x86_64 archives, strict manifest decoding, full-file hashing, and ELF checks.
- Added private zero-payload root-origin archive retrieval and anonymous-stage admission. The receiver revalidates its
  derived source, uses a bounded HTTPS-only no-retry redirect chain over fresh HTTP/1 connections, and streams the final
  response once through exact-length, independent-digest, EOF, fsync, and checked-close admission.
- Composed private end-to-end installation from independent checksum authority, caller-stream or root-fetch archive
  admission, strict extraction/finalization, candidate assembly, and atomic publication. `allow replacement` is
  permission rather than replace-only: absent slots install, identical complete releases remain unchanged, and
  differing complete releases require permission and exact reference proof. The closed result contains only the
  outcome and fully re-admitted manifest metadata; precommit errors return zero results, while committed install or
  replacement results remain available with post-commit errors.
- Firecracker, Jailer, and the manifest are staged anonymously, assembled into one recoverable exact candidate, and
  durably published into an absent canonical version slot with descriptor-relative `renameat2(RENAME_NOREPLACE)`.
  Existing versions return unchanged only after exact three-leaf shared admission and an identical canonical manifest,
  without a reference scan. An absent-only request conflicts with a different complete release, while unsafe or corrupt
  state fails closed.
- Explicit replacement fully admits a different installed release, proves its exact manifest-derived identity
  unreferenced under the active release-slot lease, rechecks both directory bindings, and commits with
  descriptor-relative `renameat2(RENAME_EXCHANGE)` without an absent-install or non-atomic fallback. The candidate
  becomes close-only immediately after exchange; cancellation-independent retirement removes only the three fixed old
  leaves after the first architecture-directory fsync, fsyncs the retired directory, rechecks its name binding, removes
  it, and fsyncs the architecture directory again. Post-commit failures preserve the primary error and report
  `release_replaced`, `durability_uncertain`, and `retired_release_retained` state where applicable.
- Added private atomic release removal under the exact release-slot lease. Existing canonical state is fully admitted
  before recovery, exact references block removal, and the complete version directory moves to a validated reserved
  name through `renameat2(RENAME_NOREPLACE)` with eight bounded `EEXIST`-only attempts and no fallback. Rename success
  makes the target committed and close-only; cancellation-independent cleanup fsyncs the architecture, removes only
  the three fixed leaves through retained descriptors, verifies both reserved-name bindings, removes the retired
  directory, and fsyncs the architecture again. Post-commit errors preserve the primary error and report
  `release_removed`, `durability_uncertain`, and `retired_release_retained` precisely.
- Hardened reserved-candidate recovery by retaining each admitted directory's device/inode identity and rechecking its
  exact name binding before the first leaf unlink and again before `rmdir`, preventing cleanup from deleting a replaced
  directory name.
- The public CLI now consumes this authority through the typed client described below. The aarch64 archive audit and
  L2/system release qualification are still release blockers.

#### Architecture-scoped binary display projection

- Binary rows now carry the release architecture. List, prefix, type/version, latest, and default resolution are scoped
  to the runtime architecture, and VM migration pairs a Firecracker row only with a same-architecture Jailer row.
- Added atomic projection replacement for one exact `(architecture, version)` Firecracker/Jailer pair and exact-slot
  projection removal. Replacement preserves or explicitly selects each architecture-scoped default, and a failed
  pair update rolls back both members; another architecture at the same version is unchanged.
- Public `mvm bin pull` now resolves one exact release, optionally streams the exact local mirror archive, invokes root
  authority before reading SQLite, and reconciles the pair only from an authoritative installed/replaced/unchanged
  result. Root errors perform no projection write; a later idempotent retry repairs a failed local projection.
- Public `mvm bin rm` accepts exact versions and sends only version plus runtime architecture to root. `--force`
  suppresses local reference advisories but cannot alter root policy. Local list/default/prune are architecture-scoped
  projection reads and never verify root-owned paths.
- Removed the parallel local downloader, source-build, filesystem removal, and path-based Jailer installation routes.
  The internal `mvm run jailer install/remove-release` actions, their path/checksum result, and orphaned archive-remap
  and file-derived binary-ID helpers no longer exist. The typed trusted-release dispatcher is the sole release mutation
  path; transitional VM launch pair resolution remains pending the separate launch-authority cutover.

#### Descriptor-pinned managed-cache and base launch-resource substrates

- Added a private typed `MVM_CACHE_DIR` locator and lease under `internal/service/jailer`. Root opens the fixed
  filesystem root once, traverses canonical components with beneath/no-symlink/no-magic-link `openat2`, and retains the
  complete descriptor chain.
- Caller ownership and safe modes use synchronized metadata. Cache identity records device, inode, and a typed unique,
  legacy, or unavailable mount ID; later re-pin rejects any mismatch.
- The final cache root accepts ext2/ext3/ext4, XFS, Btrfs, F2FS, bcachefs, tmpfs, or ZFS. FUSE, network,
  overlay/stacked, and automount cache roots fail closed. Ancestor mounts are constrained by descriptor traversal,
  owner/mode, and automount checks without unnecessarily requiring the cache filesystem allowlist.
- Added a private typed base launch-resource lease for the fixed VM rootfs, Firecracker configuration, kernel, and
  optional cloud-init ISO. It opens every component relative to the pinned cache with beneath/no-symlink/no-magic-link/
  no-cross-mount resolution and never reopens an admitted resource by pathname.
- Base launch admission verifies caller ownership, regular-file type, exactly one hard link, resource-specific owner
  access, safe modes, synchronized stable metadata, and hard size limits: rootfs 128 MiB through 16 TiB, configuration
  1 byte through 1 MiB, kernel 1 byte through 1 GiB, and cloud-init ISO 1 byte through 256 MiB. The bounded expected
  rootfs size is an equality check, not authority to raise the receiver limit.
- Caller `firecracker.json` bytes are now admission evidence only. The private root path repeats the size bound before
  allocation, performs stable positioned reads plus an EOF probe, requires strict semantic equality with the complete
  root-built machine, network, CPU, logging, metrics, cloud-init, and vsock intent, then consumes the source descriptor.
- Successful preparation writes root-built canonical bytes into a root-owned mode-`0444` memfd and verifies CLOEXEC,
  zero offset, exact readback, and the full seal set preventing writes, growth, shrink, and future writable mappings.
  The retained capability exposes no path, byte slice, generic operation, or public descriptor accessor.
- Hardened private configuration admission with malformed UTF-8 rejection, exact descriptor flags, and cancellation
  checks throughout sealed readback. All required immutability seals remain mandatory while kernel-added
  `F_SEAL_EXEC` is accepted, with a real `MFD_NOEXEC_SEAL` regression test. Copied handles now share one synchronized,
  cancellation-independent close and return its cached result, including cleanup errors.
- Successful admission transfers the complete cache and resource descriptor chain into one private lease. Rejection
  leaves the cache lease reusable; release and failure cleanup attempt every retained descriptor in reverse order with
  an uncancelled context and preserve primary `DomainError` metadata on cleanup failure.
- These are private foundations only. The privileged request, cache identity in the root-owned instance record,
  descriptor-only sealed-configuration mount, remaining resource classes, and public VM lifecycle are not wired yet and
  remain release blockers below.

#### Installed release-candidate qualification
- For T1/T2, the Python system-test runner now treats `MVM_BINARY` as a distinct compatible outer controller and
  `MVM_CANDIDATE_BINARY` as the artifact under test. It rejects path, symlink, and hard-link aliases between them and
  never replaces the controller while building or provisioning a runner. Tier 3 remains a separate host-direct gate
  that is allowed only on a clean disposable qualification host.
- Runner images stage the candidate outside the canonical path, invoke the real `host install-system` administrator
  route, initialize through exact `/usr/local/bin/mvm`, and use that installed path for T1/T2 assertions.
- Added 10 L2 installer cases covering canonical metadata and content, safe ancestors, idempotence, malformed and
  unprivileged invocation, symlink and read-only-target failures, invoking-user ownership after privileged init,
  self-update refusal, restoration, and cleanup.
- The focused `system_install` domain passed inside a disposable nested-virt runner using `0.3.0-rc.1`; the outer host
  controller remained byte-for-byte unchanged.
- Tier 3 and `--all` now require explicit pre-mutation `--host-direct` consent. The dedicated
  `--release-qualification` gate additionally requires an unfiltered full matrix, a fresh explicit-version release
  build, and byte/version identity with exact root-owned `/usr/local/bin/mvm` before host resource preparation.
- The runner rejects unknown, repeated, mixed, zero-domain, or empty-file test selections before binary probes, builds,
  or resource mutation instead of warning and returning successful but incomplete evidence.
- The runner now validates its complete registry before any work: domain names and test files are unique, registrations
  are non-empty canonical regular files under `tests/system/`, and every discovered system-test file is registered
  exactly once. The missing Tier 2 `exec` registration is included and the duplicate Tier 3 `fresh_env` alias is gone.
- Every T1/T2/T3 domain now emits a strict bounded pytest outcome report. A domain passes only when collection is
  non-empty, the process/report exit statuses match, every selected item passed, and failures, errors, collection errors,
  deselections, skips, XFAIL, and XPASS are all zero. Missing, malformed, duplicate-field, oversized reports and report
  cleanup failures fail the domain while preserving pytest output and the validation reason.
- The system-test coverage matrix now recounts its 431 current CLI rows and separates transitional/legacy coverage from
  the still-missing v0.3 root-authority, namespace, nftables, traffic, exec-policy, reconciliation, and zero-leak release
  evidence. A legacy `Deep` row is no longer presented as clean-break security signoff.

#### Deterministic initialization
- Added `mvm init --binary-version <version>` to request an exact Firecracker/Jailer pair when no local pair exists.
  The explicit selection survives the administrator host-init interaction; existing local/default behavior is
  unchanged.

#### Core-domain import CI gate
- Added a deterministic architecture test that scans non-test Go sources beneath every immediate `internal/core/*`
  domain, permits imports within the same domain, and rejects imports of a sibling domain with stable file/line output.
- CI runs the focused gate as `go test ./internal/architecture -count=1` before formatting, vet, and the full test
  suite. This is gate implementation only; it is not full release-CI or QA evidence.

### Changed

- Typed agent provisioning accepts Ubuntu's usrmerged systemd layout without relaxing no-follow file admission.
  Incomplete OpenRC directory evidence no longer triggers an unrelated `/sbin/openrc` probe; complete OpenRC
  candidates retain strict admission, and ambiguous layouts are still rejected. A real old-source regression and
  focused Go/package/race checks pass. The original mirrored tar-import/warm/Ubuntu 24.04 boot regression also
  passes on the normally installed runner candidate, with checked cleanup; the complete image domain remains open.
- Console discovery and relay-only stop now consume root journal authority, never cache PID
  mirrors. Strict instance schema 6 retains relay `running|stopping|stopped` identity;
  schema-5 records are rejected without migration. Durable stopping precedes exact pidfd signaling,
  and VM status remains based on Firecracker independently. Lost stop replies preserve effect
  uncertainty. Affected Go/root/race checks pass; active-console runtime qualification is pending.
- Typed VM removal can cancel verified incomplete protected rootfs/cloud-init imports without
  touching original staging files. Exact ownership and absence checks precede deletion;
  interrupted cleanup resumes from durable intent and retains an anti-resurrection removal
  record. Focused root behavioral/race regressions pass; end-to-end failure qualification is pending.
- Live volume detach authenticates and removes the exact guest PCI disk before removing root
  backing access. Read-only observation does not create authority, and interrupted ACL revoke
  is recoverable only for the admitted exact volume/VM transition. Focused Go/race/root checks
  pass; the original live-detach system case also passes, proving guest-device removal while
  the VM stays running. Complete lifecycle repeat qualification remains pending.
- VM execution now requires the matching Firecracker/Jailer **1.16.2** pair, replacing 1.16.1 to address the
  upstream pause/resume-vsock RX-gate defect. Runner provisioning pins the same pair. Older audited releases remain
  available for binary management, not VM launch. Its x86_64 archive retains the exact 24-member names/modes but uses
  a separately admitted plain-GNU header profile instead of the older local-PAX profile. This does not authorize
  unaudited aarch64 packaging or substitute for runtime qualification.
#### Attached volume removal and growth

- `volume rm --force` now refuses an attached volume unless root runtime observation proves its VM stopped; `--force`
  does not hot-unplug a running guest. An attached volume may be grown after stopping the VM without detaching it.
  Root protected-backing reference checks still reject retained writers. The revised volume module passed 37/37
  installed-candidate system cases inside `mvmctl-030-test`; after a final error-class correction, the two changed
  cases passed again on the exact final candidate. This is not full release qualification.

#### Private VM control projection

- Removed the fabricated public/persisted `api_socket_path` field from VM create/list output, the VM model, and the
  fresh SQLite schema. Firecracker's API socket is in the isolated execution identity's private runtime and remains
  accessible only through the authenticated root broker; no operator-connectable socket path replaces this field.
  A focused installed-candidate Python case in `mvmctl-030-test` verified the real private socket exists, the operator
  receives access denial, and public stop still works. This is not full system-test qualification.

#### Repository task cache
- The repository-local `/.tmp/` task cache is now ignored by Git.

#### VM launch and release identity
- VM create, start, reboot, and snapshot restore no longer fall back to launching Firecracker directly when Jailer setup
  fails.
- VM launch now requires the exact matching Firecracker/Jailer 1.16.2 pair. Earlier audited archives remain manageable
  through `mvm bin` but are not launch fallbacks; release runner initialization and asset pulls pin 1.16.2.
- VM and snapshot state now carry the exact Firecracker/Jailer pair needed by jailed launch paths.
- Trusted release installation and Jailer launch are internal services compiled into the single `mvm` artifact.

#### Host setup and updates
- Initial setup is now two explicit administrator actions: install the trusted system image, then run
  `sudo /usr/local/bin/mvm host init`.
- A system-installed `/usr/local/bin/mvm` refuses in-place `self-update apply`. Administrators replace it by running
  `host install-system` from the newly downloaded trusted artifact.
- The generated sudoers policy now targets only the root-owned `/usr/local/bin/mvm` privileged marker. Public CLI,
  wildcard arguments, and raw host-tool grants are not part of the v0.3.0 operator policy.

#### System-test asset preparation
- Shared runner assets are seeded from `MVM_ASSET_MIRROR` with descriptor-pinned, no-follow access and
  `mkfs.ext4 -d`; the runner no longer loop-mounts and copies the mirror with raw sudo on the host.
- Builder asset pulls run sequentially with bounded connect/operation timeouts so image extraction cannot exhaust the
  constrained nested builder through concurrent pulls.
- Builder pulls capture and re-emit bounded diagnostics, require explicit local-mirror artifact-read evidence, and fail
  qualification on pull errors, checksum fallback, or HTTP-downloaded mirror auto-population. Fixed checksum/version
  metadata may still use its upstream source.
- Missing or stale `asset-mirror` database records are rebuilt only when the recorded backing path is the exact managed
  test-volume path; unexpected paths fail closed without mutation.

#### Managed storage identity
- Newly created volume backing files are named from the immutable 64-character volume ID rather than the user-visible
  display name and always use the fixed `.raw` suffix. `volume create --format` and `VolumeCreateInput.Format` are
  removed, including explicit `--format raw`; read-only, shareable, and writeback behavior is preserved. QCOW2 image
  import/conversion and environment image-format selection remain supported.
- Data-volume QCOW2 create/resize paths are removed. Existing non-raw records remain inspectable/removable, but VM
  create/start/reboot/common snapshot relaunch and volume attach/resize reject non-raw or incomplete volume
  selections before VM-specific or lifecycle effects. Persisted VM volume IDs must resolve exactly, with no name or
  prefix fallback. These normal-process input checks do not supply privileged descriptor admission or adopt numerical
  count/capacity limits.
- Kernel download, build, and import now stage files under the kernel service's configured managed directory and install
  final artifacts as `kernels/<kernel-id>`. Import identity is computed from the receiver-owned staged bytes, so a
  caller-controlled display name or a source-file replacement cannot select or escape the managed destination.
- VM artifacts now use a compile-time filename vocabulary: `rootfs.img`, `cloud-init.iso`, `firecracker.json`,
  `firecracker.log`, `firecracker.console.log`, optional `firecracker.metrics`, and fixed API/vsock/console socket and
  PID basenames. VM filesystem type no longer changes the managed rootfs leaf.
- Custom cloud-init ISO paths remain accepted as source data, but the ISO is copied into the VM's fixed
  `cloud-init.iso` leaf before launch. Internal console-relay filename flags and filename settings were removed.
- VM relaunch reconstructs rootfs, configuration, output, metrics, API, PID, and vsock paths from the managed VM
  directory instead of treating SQLite path columns as launch authority. Root Jailer admission also rejects
  alternate manifest basenames and constructs Firecracker API/config arguments from the compiled names.
- Snapshots now produce and restore `rootfs.img`, `memory`, and `vmstate`. Restore and removal derive the snapshot
  directory and leaves from the validated 64-character lowercase snapshot ID rather than stored file-path columns.
  Protected snapshot backing and independent restored writable disks replace the earlier phantom-rootfs staging
  mechanism; the old `phantom-rootfs.img` name is no longer in active source.

#### Host-to-guest exec input

- Host-to-guest `mvm exec --port` now rejects negative or out-of-range nonzero vsock ports before VM resolution;
  `0` retains the API's stored-port default. The earlier negative-port path silently used the default port and ran
  the guest command. The fixed candidate passed the six-case host-to-guest exec module twice in the named runner;
  this is not VM-to-VM policy or full release qualification.

#### Typed vsock relaunch state (partial)

- VM start, reboot, and snapshot restore now load a private typed relaunch state instead of depending on best-effort VM
  enrichment to attach the vsock relation. A missing vsock row is a valid device absence; a repository failure or an
  invalid persisted guest CID aborts before respawn. The loaded state preserves the CID and keeps the agent port and
  token in the normal process.
- Firecracker launch configuration now carries only the validated guest CID. The core VM/Jailer path derives the fixed
  host `vsock.sock` and jail `/run/mvm/vsock.sock` leaves. Snapshot restore preserves the CID recorded in vmstate and
  supplies the fixed jail-visible UDS override when vsock is enabled.
- The accepted snapshot-network contract also preserves the trusted snapshot-captured network ID, guest IPv4, and MAC
  and reserves that exact endpoint for the restored VM. Conflict, source ownership, or a different requested network
  fails; v0.3.0 permits no fresh IP/MAC allocation, cross-network restore, or simultaneous endpoint clone. A remapped
  host TAP must still compile anti-spoofing for the embedded guest address/MAC. Root snapshot metadata, composition,
  conflict tests, and live qualification remain pending.
- This is an L0/L1-backed partial lifecycle increment, not end-to-end confirmation that the reported `mvm exec`
  restart failure is resolved. VM create does not yet use the same `vmRelaunchState`; receiver-owned
  root-built descriptor-pinned `/run/mvm/runtime` activation backed by the host `/run/mvmctl/runtime` tree, verified
  stale-socket cleanup, transactional lifecycle handling, host-reboot reconciliation, and L2 stop/start, reboot,
  host-reboot, and snapshot-exec qualification remain pending.

#### Network isolation
- Managed same-network and cross-network VM traffic is denied unless an exact typed `policy traffic` grant permits
  the destination VM protocol and port. A grant carries neither vsock-exec nor host-command authority.
- VM-to-host traffic is limited to established replies and required managed services such as NoCloud; internet egress
  remains available.

### Breaking changes

- Snapshots created by Firecracker 1.16.0/1.16.1 are incompatible with 1.16.2 in both directions. Create fresh
  snapshots after upgrading; mvmctl does not convert or relabel older snapshot state. See the
  [upstream release notes](https://github.com/firecracker-microvm/firecracker/releases/tag/v1.16.2).
- v0.3.0 requires a clean installation. It does not migrate, backfill, adopt, or preserve legacy databases, running VMs,
  Jailer directories, cgroups, TAP devices, firewall state, policy rows, or remote-exec flags. Initialization must refuse
  ambiguous legacy runtime state rather than silently adopting or deleting it.
- Legacy schema-version 3, 4, and 5 databases that contain the removed `remote_exec` column are rejected before
  pending-migration inspection or migration application, including when every bit is false. The original schema and
  version remain unchanged. After stopping the old VMs and preserving desired artifacts, follow the final documented
  clean-install procedure to remove the old mvm state directory selected by `MVM_CACHE_DIR` (normally
  `~/.cache/mvmctl`) and initialize fresh v0.3.0 state; do not delete live state or infer typed exec policies from it.
- The fresh v0.3.0 `vm_vsock_config.guest_cid` column is no longer globally unique. Firecracker processes backed by
  separate UDS endpoints have independent CID namespaces, and a snapshot clone must preserve the CID embedded in its
  vmstate. A non-unique CID index remains for lookup. This schema change is part of the v0.3 clean-install break; no
  migration or adoption of a legacy unique constraint is provided.
- Operators must stop and remove the old installation, retain any desired user artifacts separately, clean legacy host
  runtime state, and recreate VMs and policies under v0.3.0. The exact fail-closed administrator cleanup procedure is
  still a release blocker and must be documented after `host reset`/cleanup hardening lands; operators must not improvise
  raw cleanup from this development changelog.
- The new administrator-controlled system installation is mandatory. A user-owned `~/.local/bin/mvm` is no longer a
  valid final sudo target.
- The system binary and an invoking user binary must support the same privileged protocol version. Version mismatch stops
  before side effects and requires an administrator-approved system-binary replacement.
- Jailed VM launch requires cgroup v2, the required controllers, and an exact Firecracker/Jailer release pair. Hosts that
  previously relied on direct Firecracker fallback no longer start VMs through that fallback.
- Same- and cross-network managed VM traffic that previously worked without an allow rule is denied. Operators
  must create explicit `mvm policy traffic` grants for required TCP or UDP services.
- Scripts that expect `mvm self-update apply` to replace `/usr/local/bin/mvm` must switch to the administrator bootstrap
  flow. Self-update remains available for user-owned candidate artifacts.
- API consumers can no longer set `output_dir`, `name`, or `output_path` on `KernelPullInput`; managed kernel placement
  is receiver-owned. The public CLI did not expose equivalent flags.
- Firecracker/cloud-init/console filename settings are removed. Existing `rootfs.<filesystem>`, alternate snapshot leaf,
  or custom runtime filename layouts are not adopted; clean-install VMs and snapshots use the fixed v0.3 names.
- Custom `MVM_CACHE_DIR` remains supported, but the final v0.3 privileged path requires its resource-bearing
  filesystem to be ext2/ext3/ext4, XFS, Btrfs, F2FS, bcachefs, tmpfs, or ZFS. FUSE, remote, overlay/stacked, and
  automount cache roots are intentionally unsupported.

### Fixed

- OpenRC guests enable `mvm-agent` in the boot runlevel before networking, chronyd and cloud-init,
  rather than delaying vsock management behind those services. The confined provisioner safely converges
  only the verified former default-runlevel agent link; SSH, systemd activation, authentication and exec
  deadlines are unchanged. Focused Go and root loopmount checks passed; installed cold-start and affected
  directional-exec/pause-resume qualification remain pending. See [ADR-0024](docs/adr/0024-early-openrc-agent-startup.md).

- Root-partition extraction falls back to `parted` when the `sfdisk` probe cannot execute, exits unsuccessfully,
  or returns malformed output, instead of treating probe failure as proof of an absent partition table. Failed
  probes retain their causes and diagnostics; cancellation stops extraction. Focused formatting, vet and
  byte-level extraction/preservation regressions passed in the named QA runner; this correction's candidate
  rebuild and installed runtime qualification remain pending.

- Image downloads verify SHA-512 sidecars, including Alpine's bare-token format, instead of silently skipping
  their verification. Malformed or unavailable configured checksum metadata fails before artifact destination
  changes. SHA-256 support and the existing valid-checksum stale-mirror fallback remain unchanged. Focused
  formatting, vet and download/image package regressions passed inside the named QA runner. The checksum-fixed
  candidate was installed there and its normal mirrored Alpine pull, inspection and exact-owned cleanup passed;
  reusable-base, pressure and full-release qualification remain pending.

- Image lookup accepts imported names and version selectors longer than a 64-character hash, including reusable
  runner image names containing a full kernel ID. Required identifiers and normal resolver errors are unchanged.
  The pre-fix rejection, six behavioral regressions, input-package tests, formatting and vet passed in the named
  QA runner. Installation and reusable-base qualification of this correction remain pending.

- Administrator reset errors now identify the failing stage and a bounded, printable underlying cause in
  both human-readable and JSON output. Deferred cleanup preserves the first failure; process-domain messages
  containing command/output text are omitted. Existing reset guards, restoration order and retry authority are unchanged.
  Two error-composition regressions, the complete host Go package, vet and formatting passed in the named runner.
  The optional focused race check was interrupted by a QA resource guard and remains unqualified. The two existing
  Python reset-refusal/retry cases collected successfully with stronger diagnostic assertions, but their bodies and
  successful administrator reset remain unqualified; this observability fix does not establish the original cause.

- Host initialization can now reach its existing built-in-KVM admission when a moduleless kernel has no
  `/proc/modules`. Only the first missing-inventory read with an uncancelled context and accessible KVM admits
  no-change success; other read errors and unavailable KVM remain failures. A real moduleless nested guest proved
  the pre-fix failure and fixed success after independent KVM API-version-12 admission, with unchanged change order
  and no module mutations. The affected host package, vet and formatting passed inside the named QA host, and the
  owned guest was removed with root-verified absence. This is not a full administrator enrollment/reset pass or
  attribution of the earlier generic initialization error; installed-candidate qualification remains open.

- LoopMount now honors cloud-init OFF independently of deblob, including `--skip-deblob`. Fixed guest configuration
  and supported systemd/OpenRC activation suppression preserve unrelated networking, service and cache settings.
  Conflicting disable/inject intent and mismatched receiver acknowledgements are rejected. The genuine pre-fix
  guest-tree regression failed on missing disable content; the fix, affected Go packages, scoped vet and formatting
  passed inside `mvmctl-030-test`. This does not establish the cause of the observed Alpine agent startup failure
  or qualify the installed-candidate snapshot fault journey; those runtime gates remain open.

- VM creation now preserves attempted IDs and structured primary/recovery causes in `error.details.failed_vms`,
  including failures before database publication. Mixed non-atomic batches return an error rather than hiding failed
  attempts; completed IDs appear in `created_vm_ids`, while CLI error output retains `data: null`. These selectors
  grant no cleanup authority, and atomic rollback is no longer described as guaranteed removal. Behavioral
  regressions, full API/CLI packages, focused race checks, vet and formatting passed inside the named QA runner;
  updated-candidate runtime qualification remains pending.
- Prior-boot VM cleanup no longer rejects a safe root-owned `0755` shared `/var/lib/mvmctl` directory.
  Private instance directories still require `0700`; record ownership/mode, no-follow traversal and the existing
  no-cross-mount boundary remain enforced. A real-filesystem regression failed on the original reader and passed
  after the correction, including unsafe-mode and mount-crossing refusals; the complete root `authorityfs` package,
  vet and formatting passed inside the named runner. Installed candidate `0.3.0-rc.fccff661` then recovered the
  retained interrupted creation with a strict one-case pass: the verified partial copy was removed, root authority
  retired, caller files retained, and unknown-ID removal denied. This does not qualify every recovery fault.
- Forced removal of one exact VM ID can now reach retained root authority when interrupted creation never
  published a caller database row. Ordinary registered selector/name resolution is preserved; root ownership
  checks remain authoritative. Successful recovery explicitly reports retained caller source, VM-directory and
  staging files rather than silently deleting them. The affected inputs/API/CLI Go packages, vet and formatting
  passed inside the named runner, followed by the bounded installed-candidate recovery result described above.
- Reusable-base preparation now uses the supported `network create <name> --subnet <cidr>` invocation,
  removing an obsolete prompt-suppression flag that stopped preparation before builder creation. The existing
  preparation-flow test passed with real network setup orchestration and a fake CLI boundary; actual fresh-base
  provisioning remains a separate runtime gate.
- GuestFS kernel-version probing now uses an explicit five-second timeout instead of a bare duration interpreted
  as five microseconds. Focused package compilation, vet and formatting passed, followed by the installed-candidate
  appliance build/launch/reuse/repair case described above. The ordinary product does not change `/boot` permissions.
- Reusable QA bases explicitly make verified package-owned GuestFS kernel images readable by the unprivileged
  runner. Package metadata/checksums and root ownership are checked; the external Firecracker boot kernel is
  unchanged. Two focused builder checks and an already-readable real-image check passed; fresh provisioning
  remains a separate gate.

- VM-to-VM exec now checks the session's cancellation state before sending source-retained or ready responses.
  This closes a race while cancellation propagates to the response context. Inside the QA runner, the existing
  two-stage regression passed 50 repetitions; full privileged-package tests and focused race checks also passed.
- Image pull/import now return the committed default-selection state instead of a stale `is_default` value,
  and propagate persistence/default-selection failures before old-image cleanup. A real SQLite regression
  reproduced the stale result and hidden write failure on the old code; the corrected full API package,
  vet and formatting passed inside the named runner. Immediate-result Python assertions are added to the
  existing cold-pull/forced-replacement journey; their installed-candidate execution remains pending.
- Fixed normal initialization rejecting an existing root-owned `0755` `/var/lib/mvmctl` state base. Persistent stores
  and administrator reset now admit the safe shared base unchanged while retaining exact private-directory and record
  permissions.
- Separated launch-child authentication from protected preparation and post-release setup. Slow parent-side disk
  preparation no longer consumes the child's 30-second setup budget; authenticated release, cancellation and
  parent-death refusal remain enforced. Behavioral RED/GREEN, the complete root Jailer package, focused race,
  vet and formatting passed in the named runner. Installed-candidate cold-launch qualification remains pending.
- Console authority failures without an underlying cause now retain the error code's default internal class instead
  of reporting an unknown class. Cause-bearing errors retain their existing inherited classification. The complete
  affected Jailer Go package passed as root in the named runner, followed by the installed-candidate counterfeit
  endpoint case and complete four-case console file.
- The QA runner-image builder now installs `pkg-config`, which the official-kernel producer requires even when
  development headers are present. The existing provisioning/cleanup unit path passed inside the named runner;
  the fresh producer subsequently passed after installing the missing tool only there. An actual rebuilt reusable
  base remains a separate qualification gate.
- Official-kernel builds now honor an explicit `MVM_TEMP_DIR` for both build scratch and compiled-cache selection,
  while unset or empty values preserve the spec's `build_dir`. Existing settings and flags remain. Three behavioral
  cache-hit cases, the complete kernel/infra packages, formatting and focused vet passed under Go 1.26.3 inside the
  named runner. This source gate does not qualify a newly compiled kernel or reconstruct historical provenance.
- Initialization no longer marks administrator setup complete after failed sudo execution. Both wizard paths
  use the fixed system installation for `host init`, without forwarding caller-selected root command paths or
  environment assignments; cancellation and process-start failures leave setup incomplete.
  Missing or malformed session-group evidence no longer silently skips the administrator prompt.
- Image import resolves the configured or explicit `auto` format before extraction, using the existing extension
  and magic-byte detection. Concrete configured formats and explicit overrides keep their precedence.
- GuestFS appliance building and cache pruning no longer kill same-user processes or delete global libguestfs
  locks, sockets, or temporary appliances based on guessed ownership. Selected-cache appliance pruning and normal
  GuestFS operations remain; automatic global orphan recovery is intentionally removed. The miscellaneous JSON
  result retains `guestfs_state: false`. A genuine old-code regression reproduced global sentinel deletion and
  unrelated-process termination; the corrected installed candidate preserved both and pruned selected artifacts
  twice inside the named runner. Scoped Go checks passed. Enabled-GuestFS provisioning remains a separate QA gate.
- Live-volume attach now clones the protected source mount in the host namespace before entering the retained VM
  namespace. Exact source/destination admission and durable mount identity checks remain. Real root namespace
  regressions failed against the previous ordering and passed after correction; the complete root Jailer package
  and the original installed-candidate live-attach case passed. Live-detach guest release remains a separate repair.

- Jail sealing now establishes the root and copied executable's final access modes before changing ownership,
  preserving the already-running isolated Firecracker task's access throughout the transition. Stopped cleanup admits
  the exact isolated-owned intermediate root only before active identity publication. Behavioral regressions failed
  against the previous ordering/admission and passed after correction; focused race checks, vet, formatting, and the
  complete root-effect Jailer package passed in the named runner. This fixes a reachable launch race, not a proved
  attribution of the earlier intermittent post-reboot KVM denial.
- Firecracker 1.16.2's official archive now imports through its exact plain-GNU profile, including bounded positive
  base-256 UID metadata, without repacking or broadening other numeric fields. Old-profile and UID regressions failed
  before correction and passed afterward; the complete root-effect Jailer package passed. The installed candidate
  successfully imported the official mirrored pair, and the formerly failing pause/resume-vsock case passed in the
  named runner. All fifteen affected lifecycle/snapshot/version cases then passed with strict zero-other-outcome
  evidence and empty inventories. Complete current-source CI and the broader system matrix remain separate gates.
- Disabled logging no longer forwards the inactive configured log level in the typed launch request. Root admission
  still rejects unexpected logger intent, and the operator's configured default is preserved. The disabled-logging
  installed-candidate regression passed in the named runner. The full resource/configuration group then exposed
  a separate Jailer startup race; after both fixes it passed 15/15 with checked teardown.
- Final process credential observation now waits until the pinned jail root and executable match the copied
  Firecracker. Transient Jailer's root credentials no longer abort that bounded transition; final invalid credentials
  still fail closed. The Go regression reproduced the reported error before the fix; the complete root-effect Jailer
  package and focused race checks passed in the named runner. Active/recovery observation is unchanged.

#### Rapid managed-network recreation

- New managed-network IDs now include a creation nonce before hashing, so removing and recreating the same name and
  subnet within one timestamp second cannot reuse a root-retired identity. Root removal receipts remain fail-closed.
  A real network case failed on the prior installed candidate and passed on the scoped fixed candidate in the
  disposable runner; the init system cases now clean their own networks and passed 8/8 with an empty network
  inventory. Clean source `15611a9a` subsequently passed the pinned local Go CI sequence and separate root-effect
  Jailer package; its checksum-matched installed build passed the combined network/traffic-policy/init cases 56/56
  with empty resource inventories. GitHub CI and the other release QA domains remain open.

#### Volume lifecycle safety

- Fixed the underlying root live-detach worker for explicit detach: it unshares filesystem attributes before joining
  the VM's mount namespace, retires that thread after the effect, and unmounts through the pinned volume directory
  without relying on `/proc` inside the jail. Later teardown recognizes a completed detached journal after backing
  deletion while still refusing a retained jail target. Broader hotplug and guest-I/O qualification remains open.

#### Prior-boot managed-network removal

- Corrected prior-boot VM absence admission to require the current schema-6 root journal rather than stale schema 5;
  legacy and unknown schemas still fail closed. Console observation classifies a valid prior-boot relay as stopped
  without opening a PID or endpoint, while signal authorization remains unchanged. Behavioral regressions failed on
  the old source; the complete authority package, full root Jailer package and scoped vet/format checks passed inside
  the named runner. Its normally installed candidate successfully inspected and removed the exact retained test VM
  and network, with independent cleaned-journal and empty-inventory checks. A fresh owned reboot journey then passed
  stopped ls/inspect and empty ps, typed stop, one restart, new host/guest boot identities, fresh exec and exact
  configuration/inventory restoration. The 14 bounded QA-driver unit cases also passed. This one current-artifact
  journey does not explain the earlier intermittent KVM-open denial or qualify unrelated failure paths.
- `mvm network rm` can now retire an exact root-owned prior-boot live network record without first recreating its
  bridge. Under the network and firewall locks, it proves the derived bridge absent, admits only matching cleaned
  topology history, retires the prior-boot firewall generation, rejects remaining endpoints or reservations, and
  moves the unchanged network record to a removal receipt. Old ifindices and namespace inodes never authorize
  deletion. The affected Go package and a focused topology-history regression passed in the named runner; an
  installed candidate then removed two test networks left by a runner reboot. Broader reboot reconciliation and
  repeated failure-path system qualification remain open.
- A focused installed-candidate Python case now exercises the typed cross-network TCP traffic policy through real
  guest connections: deny before grant, allow one destination port, deny the adjacent port, and deny after revoke.
  The fixed nftables and network JSON cases passed in earlier focused runs; the full policy/system matrix is not yet
  qualified.
- A clean `02a4a690` runner checkout passed the pinned offline Go CI sequence and separate full root-effect Jailer
  package. This is local code verification, not remote CI or complete Python/system release qualification.

#### Same-network traffic grants under bridge netfilter

- Fixed explicit same-network TCP/UDP traffic grants on hosts where `br_netfilter` also passes bridged IPv4 through
  inet forward. The compiler now emits an exact inet companion for each same-network VM- or network-source grant
  ahead of the managed-bridge default drop, while retaining the bridge-family endpoint gate and default deny.
  It does not change the host's bridge-netfilter setting. A live runner nft trace identified the second-pass drop;
  the installed candidate then passed focused same-network and cross-network TCP default-deny, exact-port allow,
  adjacent-port deny, and revoke cases, each 1/1 with zero skips and clean VM/network/policy teardown.
- The representative 1,000-same-network-grant compilation now includes 1,000 additional inet rules. On the named
  Go 1.26.3 runner, allocations rose from 243,345 to 279,352 and allocated bytes from 11,683,904 to 12,950,960.
  The fixed L1 allocation ceiling is 290,000; the 6x growth guard remains. Source-VM, UDP, spoof/reverse, reused-IP,
  reboot recovery, and the full Python/CI matrix still require qualification.

#### LoopMount image inspection

- The fixed filesystem probe now accepts `/usr/sbin/blkid` only when `/usr/bin/blkid` is absent, without consulting
  `PATH`. Read-only inspection opens its backing image with `O_NOATIME`, avoiding access-time drift that made strict
  post-inspection revalidation reject an otherwise unchanged image. The named runner's installed rc.7 candidate pulled
  and optimized mirrored Alpine 3.23 after both fixes; broader image and VM lifecycle QA remains pending.

#### Host capacity reporting

- `mvm host info` now detects and caches capacity on the first read after a clean installation; `--refresh` no longer
  fails because administrator setup intentionally left the caller's SQLite `host_state` row absent. Host info omits a
  setup verdict, and host status does not treat the caller-owned row as root-owned admission evidence. The 0.3 JSON
  envelope and first-read path are covered by installed-candidate Python checks in the named runner.

#### Trusted release checksum authority

- Disabled HTTP/2 for the private checksum client and retained its no-reuse policy, so each sidecar request uses a fresh
  HTTP/1 connection. This prevents Go's HTTP/2 transport from retrying a bodyless GET and preserves the implemented
  one-retrieval-attempt contract used by private end-to-end installation.

#### `mvm image inspect`
- Ambiguous ID prefix now reports `"Image ID is ambiguous"` instead of `"No images found"`.

#### `mvm inspect` (all — vm, image, network, key, etc.)
- Byte-size fields (`_size` keys) in tree output are now human-readable (e.g., `8589934592` → `8.0 GiB`).

#### Jailed VM cgroup verification
- VM create no longer fails when Jailer places the cgroup at `/sys/fs/cgroup/mvmctl/<vm-id>`; the check previously expected an intermediate `firecracker/` component that Jailer v1.16 does not create.
- Stop and remove now clean the VM's actual leaf cgroup instead of leaving stale ones behind.

#### Privileged initialization ownership
- `sudo /usr/local/bin/mvm host init` resolves a complete, internally consistent sudo identity before compiled host
  setup. Direct root requires an explicit non-root operator. The early administrator route does not create or load the
  invoking user's cache, configuration, or SQLite database.

### Security work still pending

The implementation map has source code and bounded runner evidence, but the release is not qualified. Before sign-off:

- Complete broader exact-current-candidate lifecycle/snapshot QA beyond the now-passing fifteen affected cases.
  The reproduced upstream 1.16.1 pause/resume-vsock blocker is resolved by the qualified 1.16.2 affected gate, not
  by changing retries/timeouts. Its snapshots remain incompatible with 1.16.0/1.16.1; fresh captures are required.
- Obtain the separate GitHub CI result. Matching current Go source has passed the pinned local CI sequence and
  separate complete root-effect Jailer package in the named runner; repeat affected gates when Go source changes.
- Complete the installed-candidate Python T1/T2 matrix with strict outcomes and leak checks. The two Jailer cases
  requiring an older release still need an authenticated official checksum fetch; a mirrored archive alone is not
  sufficient. Do not bypass the trusted-release receiver to make those cases green.
- Diagnose the intermittent jailed device-open failure after host reboot and prove a repeatable reboot/status/exec
  recovery journey. A successful repeat is not an explanation for the earlier failure.
- Attribute the original memory-saturation incident and establish the limits of the bounded pressure/exhaustion
  evidence above. Pressure-only `memory.max=max` removes one finite VM-leaf ceiling, not every OOM risk.
- Qualify administrator install/init/reset and destructive host clean only on a dedicated clean host. The shared
  runner's unprivileged refusal tests do not prove reset success.
- Finish public documentation and the clean-install procedure, report the aarch64 runtime limitation, and obtain
  human approval before tagging. Do not install the candidate on the outer controller as an incidental test step.

#### Earlier implementation checkpoints (historical)

The list below records the earlier boundary checklist. Several composition items were subsequently implemented in
the integration updates above; it is not the current incomplete-work inventory. Earlier test claims elsewhere in this
changelog apply only to their original slices.

- Activate the implemented strict schema-3 authority through the production VM lifecycle. Spawn and authenticate the
  blocked child/relay and persist the implemented owner/cache/release/boot and PID/start `launching` record before
  effects. Commit `active` only after Jailer has copied Firecracker, joined cgroup/netns, pivoted to its final mount
  namespace, dropped credentials, and execed the copy. Require empty supplementary groups, verify the final process
  root and copied inode, and persist a present relay's exact caller credentials, host namespace/root, and root-owned
  `/usr/local/bin/mvm` executable identity. Seal the jail root and copy to `root:root 0755` beneath `root:root 0700`
  ancestors and hash the copy against the exact release.
  Jailer must use neither `--daemonize` nor `--new-pid-ns`; `console.pid` remains display-only.
- Compose the implemented private process activation, abort, and cleanup/recovery authority into the production launch
  action. Carry the remaining mount, topology, and firewall endpoint resources through launch, active, and cleaning
  without releasing the VM lock. Hold network/firewall mutation locks through activation and same-lock rollback, then
  release them after success and reacquire network then firewall for later cleanup. Preserve returned post-rename
  authority on every failure.
- Compose the private fixed-name cgroup-v2 authority into production create/start/stop and other lifecycle actions. Add
  same-boot dead-process reconciliation, replace `entry.go` path-based prepare/remove, and complete live L2, Tier 3,
  and system qualification before treating it as host enforcement or release evidence.
- Compose the implemented exact-absent Jailer destination and pre-handoff recheck into production; add safe admission
  and descriptor-relative removal for only an exact non-mounted stale fixed tree after process absence. Create links
  down, apply and freshly verify the complete atomic nftables generation, then raise links; release the blocked child
  only after firewall and relay success.
- Extend the implemented close-once process/destination/output/active/cleanup foundation to remaining mount, network,
  and firewall resources. Add complete interrupted-`launching` recovery and compose the private batch observer only
  after public lifecycle operations write schema-3 authority; wire privileged VM actions to that lifecycle and VM lock.
- Keep managed-network registration in network creation, then consume the real Jailer VM lock's `TopologyHold` into a
  persistent per-VM topology lease during launch. Activate neither topology nor the production-composed VM-exec
  capacity authority through user SQLite state; user state must not authorize host-global namespace, link, firewall,
  process, mount, cgroup, or admission decisions.
- Carry the managed-cache locator and base launch selection through the privileged protocol, consume the private
  schema-3 cache-identity binding, and create/consume the implemented descriptor lease inside the root process and
  private mount-namespace launch path. Compose persistent-output preparation and complete snapshot capture/restore
  authority. Raw-volume identity admission, release-bound MMIO counts, and snapshot byte bounds are implemented
  privately; per-volume and aggregate storage capacity policy remain unresolved. QCOW2 image import/conversion remains
  supported.
- Keep the implemented closed Firewall capability, evidence-bound generation record replacement, retained host-netns
  snapshot factory, raw collector, and application/reconciliation coordinator private until qualified initial kernel
  inspection/adoption follows network authority; do not add generic paths, descriptors, or lock names.
  Then compose the private caller-owned socket/PID directory capability beneath `/run/mvmctl/runtime`; stop
  mounting the whole cache VM directory, pin persistent config/output leaves individually, truncate enabled `0600`
  log/console/metrics files only after durable launch registration, and retain them after stop. The private runtime and
  persistent-output capabilities are implemented; public mount and lifecycle consumption remain pending.
- Treat the current fixed-name Jailer checks as transitional defense-in-depth only: public Firecracker launch still
  reads a caller-writable configuration from the whole mounted VM directory. Wire the implemented sealed root-built
  configuration capability into the final descriptor-only mount transaction and derive/verify every jail-visible path
  after durable registration.
- Finish the aarch64 archive audit and CLI-level qualification. Public pull/remove use the registered typed
  trusted-release actions, reconcile the architecture-scoped projection only from authoritative results, and have no
  legacy downloader/build/path mutation fallback.
- Migrate Jailer, network, firewall, and supported host mutations to distinct typed privileged actions; complete
  live-kernel qualification and backend cutover for the now-registered LoopMount provision/inspect handlers, then remove
  the public root
  `mvm run jailer` and `mvm run provision` entry points.
- Compose the implemented private namespace, link, and canonical L3 effects into the owner-bound VM lifecycle,
  require the freshly verified opaque firewall endpoint lease, raise host veth last,
  make one namespace mandatory per VM, and pass its pinned handle to Jailer. Integrate retained activation, rollback,
  reconciliation, cleanup, root-selected single-uplink NAT authority, and snapshot endpoint preservation, then complete
  live L2, snapshot, reboot/failure, clean-break, and leak-free system QA.
- Use the private record-bound application/reconciliation coordinator in production only after resolving the pinned
  library's missing table/chain provenance, accepting the root/`CAP_NET_ADMIN` threat-model boundary, and composing
  qualified initial inspection/adoption. Prove namespace-scoped delete/recreate, invalid-batch recovery, both-table
  empty removal, cancellation/close behavior, and runtime scale against supported kernels before a privileged
  `Conn.Flush` application can make links usable. Only then remove the iptables backend and selector plus the
  production `nft` CLI subprocess/parser; the canonical compiler digest alone is not kernel authority.
- Complete root nftables application and revocation for the public typed traffic intent. Qualify the implemented
  API-owned opt-in exec relay, runtime directional policy enforcement, bounded payload forwarding, cancellation,
  audit, and retained root active-pair/capacity authority end to end after lifecycle cutover. The
  legacy guest relay, generic host-frame callback, shared guest connection, and VM-wide remote-exec field/flags are
  removed; the routed `ServiceAccessPolicy` and other transitional surfaces still require atomic removal.
- Replace the transitional wildcard/raw-tool sudoers entries with access only to the reserved privileged marker of the
  root-owned system binary.
- Complete trusted global cleanup before enabling host clean/reset, and qualify the early administrator journal,
  operator enrollment, typed results, and mutation/state-write failure handling.
- Make launch, abort, cleanup, reconciliation, snapshots, live volumes, cgroups, and firewall policy crash-consistent and
  fail closed under ownership, PID-reuse, path-race, and partial-failure conditions.
- Reconcile `mvm vm ls`, `mvm vm ps`, and `mvm vm inspect` against root-owned process identity after host reboot instead
  of reporting stale SQLite status. Complete the single typed relaunch state by bringing create into the start/reboot/
  snapshot-restore seam, activate root-built descriptor-pinned `/run/mvm/runtime` backed by the host
  `/run/mvmctl/runtime` tree, remove a stale fixed vsock socket only after verified process absence, and qualify
  host-to-guest `mvm exec` through every supported relaunch path in L2.
- Complete adversarial L1 coverage, Python `tests/system/` clean-install/fault qualification, leak audits, full CI, and
  the final documentation accuracy review. Go tests support but do not replace CLI-level L2 release signoff.
- Finish Tier 3 clean-host inventory and ownership-safe cleanup; unify every host-direct CLI call, preserve pre-existing
  resources, surface every teardown failure, and reject skips/xfails/omitted domains before accepting full `--all`
  release evidence.

## [0.2.0] - 2026-07-10

### Added

#### `mvm vm create`
- New `--allow-remote-exec` flag. When set, the VM can both issue and accept remote exec commands to/from other flagged VMs. Configurable via `defaults.vm.allow_remote_exec` (default `false`).

#### Vsock remote exec (VM → Host → VM relay)
- Guest agent (`mvm-vsock-agent`) now has a `remote <destination> -- <command>` subcommand that connects to the daemon's local Unix socket and requests execution on another VM.
- Guest agent daemon opens a local Unix socket (`/var/run/mvm-vsock-agent.sock`) for in-VM IPC. The daemon forwards `remote_vm` frames through the existing host→guest vsock connection.
- Host-side `Client.Exec()` read loop dispatches unknown frame types to `OnHostFrame` callback when set.
- New `internal/vsockhandler/` package receives guest-initiated frames, resolves target VMs, checks `RemoteExec` on both source and target, and performs a streaming relay (frame-by-frame, no buffering) via exported `vsock.SendFrame`/`vsock.ReadFrame`/`vsock.DialVM`.
- Protocol primitives in `internal/core/vsock/protocol.go` exported: `SendFrame`, `ReadFrame` (returns type + data bytes), `DialVM`. Internal `readFrameRaw` helper for typed reads.
- `RemoteVMRequest` and `RemoteVMResponse` types defined in `internal/service/vsockagent/protocol.go`.
- Both source and target VM must have `remote_exec = true`. Source is checked before parsing the request payload.
- Error codes added: `CodeUnauthorized`, `CodeVMNotRunning`, `CodeVsockConfigNotFound`.

#### `mvm vm inspect`
- Now shows the vsock agent configuration (guest CID, UDS path, port, agent version, and upgrade state) when a VM has a vsock record. The auth token and redundant `vm_id` are intentionally omitted, and `agent_version` is persisted at VM creation and corrected on first agent contact.
- The `networking.network` block no longer includes the network's full DHCP lease list.
- Now shows `allow_remote_exec` and `nested_virt` flags.

#### `mvm kernel pull`
- `--features` now accepts `all` or `*` as a wildcard to enable every feature defined in the selected kernel spec.
- Feature names are now validated against the spec's `features` map instead of a hardcoded list.
- Enabled features are persisted and shown in `mvm kernel inspect`.
- Kernel files are now stored with their content-addressed ID as the filename.
- New `--skip-checksum` flag to bypass SHA256 verification when the checksum server is unavailable.

#### `mvm env`
- New `image_import` step type for importing local images and VM rootfs in environment specs.
- Exec/SSH steps now support `ignore_errors: true` to continue on non-zero exit codes.
- `image_import` destroy is a no-op — imported images persist in the database and on disk.
- `env apply` now accepts a remote URL (`https://` or `http://`) in place of a spec file path. The spec is fetched over HTTP and parsed identically to a local file. `env diff` and `env destroy` also support URLs.
- `image_import` apply now always delegates to the API layer, enabling `force: true` to re-import and replace existing images.
- All steps now support `removes` field to destroy resources mid-pipeline after the step completes.
- New top-level `ephemeral: true` field — auto-runs `env destroy` on pipeline completion (success or failure). Zero cleanup overhead. See `docs/ENV_SPEC_REFERENCE.md`.
- `removes` now updates the workflow state after destroying each resource, so a subsequent `env destroy` doesn't try to tear down already-removed resources.
- `NetworkStep.Destroy` and `KeyStep.Destroy` now treat "not found" as success — already-deleted resources during destroy no longer abort the process.

#### `mvm image import`
- Renamed `source_path` to `source` in the input struct (breaking — no backward compat).
- Now runs `sync` on running source VMs via vsock before importing their rootfs.

#### `mvm self-update`
- New command to check for and apply updates from GitHub releases.
- `mvm self-update check` — compare current version against latest release.
- `mvm self-update apply` — download, verify SHA256, and atomic binary swap.
- `mvm self-update` — check + apply if newer.
- Supports `--force` to re-install same version.
- Refactored GitHub release fetching into reusable `download.Remote` struct.

#### `mvm completion`
- Removed PowerShell completion support.

#### `mvm network inspect`
- Now shows active firewall rules per network.

#### `mvm kernel pull`
- Friendlier error messages when checksum server is temporarily unavailable.

#### `mvm image ls`
- Now shows the `Version` column in the default listing.

### Changed

#### `mvm vm create`
- Renamed `--no-enable-logging` → `--disable-logging`, `--no-enable-metrics` → `--disable-metrics`. Added `--deny-remote-exec` (mutually exclusive with `--allow-remote-exec`).

#### Guest agent: `mvm-vsock-agent` renamed to `mvm-agent`
- Package directory moved from `internal/service/vsockagent/` to `internal/service/agent/`.
- In-VM binary: `/usr/bin/mvm-vsock-agent` → `/usr/bin/mvm-agent`.
- In-VM socket: `/var/run/mvm-vsock-agent.sock` → `/var/run/mvm-agent.sock`.
- Auth token: `/var/run/mvm-vsock-agent.token` → `/var/run/mvm-agent.token`.
- Systemd unit: `mvm-vsock-agent.service` → `mvm-agent.service`.
- OpenRC init: `/etc/init.d/mvm-vsock-agent` → `/etc/init.d/mvm-agent`.
- Go interface: `InjectVsockAgent()` → `InjectAgent()`, `BuildVsockAgentOps()` → `BuildAgentOps()`, error code `CodeVsockAgentUnreachable` → `CodeAgentUnreachable`.
- No backward compatibility — old paths will not work.

#### `kernels.yaml`
- Renamed `config_url_template` to `base_config_url_template` to clarify that it provides the base kernel `.config`.
- Removed the redundant duplicate URL from `config_fragments` in the bundled `kernel-official` spec.
- Added `CONFIG_IKCONFIG` and `CONFIG_IKCONFIG_PROC` to the `containers` feature enforce map.
- Added `CONFIG_NF_CONNTRACK` to the `iptables` feature enforce map.
- Added `CONFIG_NETFILTER_XT_TARGET_CT`, `CONFIG_IP_SET`, `CONFIG_IP_SET_HASH_IP`, `CONFIG_IP_SET_HASH_NET`, and `CONFIG_VXLAN` to the `iptables` feature enforce map.

#### `mvm env spec parsing`
- Replaced custom `UnmarshalYAML` with `yaml:",inline"` on `Steps` map for automatic parsing.

#### `mvm net / image / kernel / bin rm`
- `rm` on a soft-deleted resource (orphan) now hard-deletes it instead of returning "not found". The resolver chain now threads `includeDeleted` from input → resolver → repo, so remove operations can resolve orphaned resources.

#### Listing visibility for soft-deleted resources
- Networks, images, kernels, and binaries with `deleted_at` set are now shown in listings with a `[x]` suffix in red, instead of being hidden.
- Binaries show a `Status` column in long mode (`--long`) indicating "deleted".
- `ListAll` SQL no longer filters `WHERE deleted_at IS NULL` — returns all records.
- `GetByName` and `FindByPrefix` accept an optional `includeDeleted` parameter (default `false`). Resolvers thread this through so individual operations can opt in to resolving deleted resources.

#### Env spec format [Major]
- Spec format redesigned for clarity and consistency. Step sections are now maps (key = step name) instead of lists with a `name` field. An optional `name` field inside the params overrides the resource name (e.g., the bridge name for a network, the VM name). Cross-resource references use `@type:name` format (e.g., `"@network:default"`) with the `@` sigil making references visually distinct from literal values. Both `depends_on` and reference fields (`network`, `key`, `image`, `kernel`, `binary`) accept the new format. Backward compat for bare names is preserved.

  **Before:**
  ```yaml
  network:
    - name: default
      subnet: "172.27.0.0/24"
  vm:
    - name: dev-vm
      network: default
      depends_on:
        - network:default
  ```

  **After:**
  ```yaml
  network:
    default:
      subnet: "172.27.0.0/24"
  vm:
    dev-vm:
      network: "@network:default"
      depends_on:
        - "@network:default"
  ```

### Fixed

#### `mvm image pull` / `mvm kernel pull`
- Fixed version-blind cleanup that could delete a different version's files. Pulling `alpine:3.21` no longer soft-deletes `alpine:3.23` and removes its cached file. The lookup now uses `GetByVersionAndType` instead of `GetByType`, ensuring cleanup only touches the same version being pulled. Same fix applied to firecracker kernels.

#### `mvm env`
- Fixed state file structure: `state.spec` now stores the input spec fields (what the user configured), and `state.output` stores the created resource state (IDs, properties). Previously output was incorrectly stored in `state.spec` and `state.output` was never populated.

#### `mvm kernel`
- New `fqdn-proxy` feature set with `CONFIG_NETFILTER_XT_TARGET_TPROXY`, `CONFIG_NETFILTER_XT_TARGET_CT`, and `CONFIG_NETFILTER_XT_MATCH_SOCKET`.
- New `bandwidth` feature set with `CONFIG_NET_SCH_FQ` (Fair Queuing packet scheduler).
- Added eBPF/BTF features: `CONFIG_BPF_EVENTS`, `CONFIG_PERF_EVENTS`, `CONFIG_NET_CLS_BPF`, `CONFIG_NET_CLS_ACT`, `CONFIG_NET_SCH_INGRESS`.
- Added CNI overlay features: `CONFIG_GENEVE`, `CONFIG_FIB_RULES`.
- Added crypto features: `CONFIG_CRYPTO_SHA1`, `CONFIG_CRYPTO_USER_API_HASH`.
- New `iscsi-target` feature set with `CONFIG_CONFIGFS_FS`, `CONFIG_TARGET_CORE`, `CONFIG_ISCSI_TARGET`, `CONFIG_ISCSI_TCP`, `CONFIG_SCSI_ISCSI_ATTRS`, `CONFIG_BLK_DEV_SD`, `CONFIG_SCSI_CONSTANTS` (required by Longhorn block storage).
- New `ebpf-cni` feature set with eBPF/BTF + tunneling + iptables + L7 proxy + connection tracking configs (required by Cilium, Hubble, kube-proxy replacement).
- New `wireguard` feature set with `CONFIG_WIREGUARD`, `CONFIG_WIREGUARD_DEBUG`, `CONFIG_DST_CACHE`, `CONFIG_NET_UDP_TUNNEL`, `CONFIG_CRYPTO_LIB_CURVE25519`, `CONFIG_CRYPTO_LIB_CHACHA20POLY1305`, `CONFIG_CRYPTO_LIB_POLY1305`, `CONFIG_CRYPTO_LIB_UTILS` (modern VPN kernel module).

#### `mvm env`
- `env destroy` completion now shows workflow IDs from saved state alongside file paths (was previously blocked by `FilterFileExt` directive).
- `env destroy` and `removes` mid-pipeline cleanup now pass `IncludeDeleted: true` for network, image, kernel, and binary removes, so soft-deleted resources are properly hard-deleted instead of left orphaned.


#### `mvm image import`
- Fixed "target is busy" flakiness during image shrink/grow: when the first `umount` fails, `shrinkExt4` and `growExt4` now fall through to `CleanupMount` (which scans `/proc`, kills orphan processes, and retries) before returning an error.

#### `mvm inspect` (all — vm, image, network, key, etc.)
- Fixed scientific notation display for large numbers in tree dict output. Whole-number `float64` values are now formatted as plain integers (e.g., `3.827e+03` → `3826`).

#### `mvm vm create`
- `/etc/hosts` is now appended to instead of fully overwritten during provisioning, preserving entries from the base image.

#### `mvm image import`
- Fixed deduplication that silently skipped importing a different version of the same type.
- Image name is now automatically set to `type version` on import.
- Success output now shows the source path or VM name instead of the internal cached path.

#### `mvm cp`
- Recursive directory copies now follow symlinks and skip broken symlinks, non-regular files, and symlink cycles instead of aborting.
- Single-directory copies to a destination without a trailing slash (e.g. `mvm cp ./my-dir vm:/path/to/dest`) now create the destination as a directory.

#### `mvm vm create`
- Fixed `--allow-remote-exec` flag being silently ignored — `remote_exec` column was missing from the SQL upsert.

#### `mvm image import`
- Force import now hard-deletes the old DB record when no VMs reference it (instead of always soft-deleting).
- Progress message during image optimize changed to "Debloating and shrinking filesystem..." for clarity.

#### `mvm exec`
- Interactive shell sessions now forward the host terminal size and `SIGWINCH` resize events to the guest PTY, so TUI apps (vim, htop, etc.) draw correctly when the terminal or tmux pane is resized.
- Fixed resize frame handling in the guest agent so that `SIGWINCH` frames interleaved with stdin bytes are applied instead of being written to the shell as literal input.
- Fixed host-side concurrent writes to the vsock connection, which could split JSON resize frames and corrupt the byte stream seen by the guest agent.

#### `mvm console`
- Fixed duplicate error messages when the console relay is not running.

#### vsock agent upgrade
- Fixed a deadlock where `systemctl restart mvm-vsock-agent` waited for the agent to stop while the agent was waiting for the upgrade command to finish, causing a 30s timeout and a confusing EOF error.
- Fixed a shell syntax error in the restore/rollback command (`&;`).
- Upgrade and restore commands now detach the service restart with `nohup` and support both systemd and OpenRC.
- The DB upgrade lock is now cleared immediately when an upgrade fails, instead of forcing a 60s wait.
- Fixed version comparison for git-describe strings (`0.1.0-9-g<hash>`) so that random hex hashes are not compared lexicographically; only the tag distance is used for ordering.
## [0.1.0] - 2026-06-28

### Added

#### CLI Commands (18 top-level groups, 70+ subcommands)
- **`mvm vm`** -- Full VM lifecycle: ls, ps, create, rm, start, stop, reboot, pause, resume, inspect
- **`mvm console`** -- Interactive serial console access via PTY-over-Unix-socket relay with --state and --kill options
- **`mvm host`** -- Host configuration: init (KVM, modules, sysctl, mvm group, sudoers), status, info, clean, reset
- **`mvm network`** -- Named bridge networks with NAT: ls, default, create, rm, inspect, sync
- **`mvm key`** -- SSH key management: ls, create, import, rm, inspect, export, default
- **`mvm config`** -- Runtime configuration: get, set, ls, reset
- **`mvm init`** -- Interactive setup wizard with non-interactive mode
- **`mvm kernel`** -- Kernel management: ls, inspect, pull, default, import, rm
- **`mvm image`** -- Image management: ls, pull, default, rm, inspect, import, warm
- **`mvm bin`** -- Firecracker binary management: ls, pull, rm, default
- **`mvm exec`** -- Run commands inside VMs via vsock agent without SSH
- **`mvm cp`** -- Copy files between host and microVMs via vsock binary frame protocol
- **`mvm cache`** -- Cache lifecycle: init, prune (per-resource or all), clean
- **`mvm logs`** -- Log streaming: boot logs (serial console) and Firecracker OS logs with --follow
- **`mvm ssh`** -- SSH into VMs by name, ID, IP, or MAC with custom user, key, and connection timeout
- **`mvm volume`** -- Persistent data disk management: create, rm, ls, inspect, resize, attach, detach
- **`mvm env`** -- Environment workflow management: apply, diff, ls, destroy
- **`mvm snapshot`** -- Snapshot lifecycle: create, list, inspect, restore, remove

#### Architecture
- **Three-layer architecture** (CLI -> API -> Core) with strict import boundaries enforced by Go compiler
- **Cobra CLI framework** with root command and subcommand hierarchy
- **Controller / Service / Repository / Resolver** pattern across 16 core domains (not all domains implement all four; simpler domains have fewer components)
- **Input Validate/Resolve** pattern for type-safe, validated operations across 11 domains (ADR-0011)
- **Provisioning backend abstraction** (LoopMount vs GuestFS) with mutual exclusion
- **Firewall backend abstraction** (nftables vs iptables) with mutual exclusion
- **SQLite database** (`internal/lib/db/migrations/`) with migration system for persistent state (16 tables): images, kernels, binaries, volumes, networks, network_leases, vm_instances, host_state, host_state_changes, iptables_rules, nftables_rules, ssh_keys, user_settings, vm_vsock_config, snapshots, db_migrations
- **Relation enrichment** system with batch loading to prevent N+1 queries
- **Privilege delegation** model via `mvm` unix group and sudoers drop-in (no sudo for normal operations)
- **Single error type** (`pkg/errs.DomainError`) with Code, Class, Message, Op, Entity, Details, Err
- **Parallel execution** via `internal/infra/pool/` with bounded concurrency

#### VM Lifecycle
- Create VMs with configurable vCPUs, memory, disk size, PCI, nested virt, console, logging, and metrics
- Batch VM creation via `--count N` and all-or-nothing `--atomic` flag
- Snapshot and restore (memory + VM state) via Firecracker API socket
- Cloud-init provisioning in four modes: inject, net (nocloud-net HTTP server), iso, off
- Firecracker process lifecycle: spawn, monitor, signal (SIGTERM/SIGKILL), exit code tracking
- Per-VM isolated nocloud-net HTTP servers with source-based firewall rules

#### Networking
- Linux bridge and TAP device management for guest connectivity
- NAT/masquerade with nftables (default) or iptables (legacy) for outbound internet access
- IP lease management with automatic allocation and release
- Firewall rule tracking with FirewallTracker and backend-specific repositories
- Network reconciliation (sync DB state with live bridge state)
- UFW compatibility via non-hook chains with jump rules at position 0

#### Image Management
- Fetch images by type:version (ubuntu:24.04, archlinux, debian:12, alpine, firecracker)
- Import local image files with automatic format detection
- Format support: qcow2, raw, tar-rootfs, vhd, vhdx
- Automated conversion pipeline: download -> decompress -> format conversion -> root partition extraction -> filesystem optimization
- SHA256 checksum verification for downloaded images
- Image warm pool for fast VM creation (pre-extracted ready-to-copy images)
- Loop-mount provisioner backend for rootfs operations (default, no external dependencies)

#### Kernel Management
- Download pre-built Firecracker CI kernels (optimized, fast boot)
- Build official upstream kernels from source with Firecracker-compatible configs
- Configurable kernel features via YAML specs (e.g., kvm, nftables)
- Automatic architecture detection and kernel config application

#### Binary Management
- Download Firecracker and jailer binaries from GitHub releases
- Version management with default version selection

#### SSH Key Management
- Generate ED25519, RSA, and ECDSA keypairs via ssh-keygen
- Import existing public keys with fingerprint detection
- Set one or more default keys for automatic VM injection
- Export keypairs to standard ~/.ssh location

#### Host Initialization
- Enable IP forwarding and persist sysctl settings
- Load KVM kernel modules (kvm, kvm_intel/kvm_amd)
- Create mvm unix group and sudoers drop-in with passwordless access to privileged binaries
- Setup nftables/iptables chains for VM traffic management
- Idempotent -- safe to run multiple times
- Full reset: revert all host changes including networking, sysctl, sudoers, and group

#### Services (compiled into single `mvm` binary)
- **Console relay** (`mvm run console relay`) -- PTY-to-Unix-socket bridge for interactive serial console without SSH
- **nocloud-net server** (`mvm run nocloudnet serve`) -- Per-VM HTTP server for cloud-init datasource delivery
- **Loop-mount provisioner** (`mvm run provision`) -- Rootfs provisioning for SSH key injection, hostname setup, DNS config, cloud-init disable, and filesystem resize
- **Vsock guest agent** (embedded) -- Cross-compiled guest agent binary, zstd-compressed and embedded in the `mvm` binary, injected into VMs at runtime for vsock-based exec, file transfer, and console

#### Developer Experience
- **Go API** (`pkg/api/`) -- Operation struct with methods for each domain, sole cross-domain orchestrator
- **Go toolchain** -- Standard `go build`, `go vet`, `go test`
- **System test suite** -- Python-based black-box CLI tests in `tests/system/`
- **Build scripts** (`scripts/`): build.sh, bump-version.py, common.py, fresh_env.py, post-release.py, run-system-tests.py, setup-test-environment.py

#### Distribution
- Single statically-linked Go binary (no runtime dependencies)
- Distribution packages: .deb (Debian/Ubuntu), .rpm (RHEL/Fedora), PKGBUILD (Arch Linux)
- Man page (`docs/mvm.1`)
- Initial RPM release
- Distribution packages support

#### Performance
- VM creation ~2.3s average (loop-mount), ~3.9s (GuestFS) per benchmark data
- VM-ready ~2.9s average (loop-mount), ~5.8s (GuestFS) per benchmark data
- SQL-level computation (COUNT, WHERE IN) instead of fetch-all-then-filter

#### Testing
- Comprehensive test suite (~2500 tests: ~850 Go test functions + ~1185 Go subtests + ~520 Python system tests)
- System tests run in nested VM with unprivileged user
- Coverage matrix tracking every CLI subcommand and flag

[0.3.0]: https://github.com/AlanD20/mvmctl/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/AlanD20/mvmctl/releases/tag/v0.2.0
[0.1.0]: https://github.com/AlanD20/mvmctl/releases/tag/v0.1.0
