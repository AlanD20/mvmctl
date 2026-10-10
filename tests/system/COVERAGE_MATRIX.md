# System Test Coverage Matrix

**Purpose:** Accountability document. Every CLI command and flag must have
a documented test status. Update this matrix when CLI flags are added,
removed, or when test coverage changes.

**Legend:**

| Status | Meaning |
|--------|---------|
| ✅ Deep | L3 Option C verification |
| ⚡ Shallow | L0-L2 verification (returncode, output, JSON) |
| 🟡 Partial | Coverage exists, but required behavior remains unproved and/or conditional |
| 🔴 Missing | No test exists |
| ⏭️ Skip | Test defined but always skips (broken setup) |
| **?** | Needs investigation — coverage unclear |

## v0.3.0 release-security qualification overlay

**Focused console and reboot checkpoint (2026-10-10):**
`test_console_counterfeit_endpoint_rejected_by_discovery` passed alone on installed
`0.3.0-rc.resume-1300b64b`/`81c521ba`: strict 1/1, zero other outcomes, 7.38 seconds.
The case exchanged a real same-caller decoy socket, proved discovery/attach refusal, restored the original
socket and retained relay/Firecracker process identities. Typed cleanup restored byte-identical checked
original and prepared inventories; source, candidate, kernel, mirror and administrator pins were preserved.
Terminal SHA-256 `e4a8d97e4b5bda5b2ae55490c447c419f110c3d80deaf93244e5d9ddc8b6e81d`;
JUnit `deee30bb13a41e5bce734fdb8b388c4792851df9496eee26275a249af18bd8de`.
This closes the missing counterfeit-discovery node, not all console coverage.

Signed `ac69171f` implements explicit `test_host_reboot_pre`/`test_host_reboot_post` phases and
closed proof consumption through `--host-reboot-task`. The corrected source passed 52 focused runner
checks; the integrated source separately passed only the three selection/proof-consumption variants and
exact two-node collection. Integration JUnit SHA-256
`e6849035de6f87d22acf1d8833403a79432b10c02ab33416b9ae162567e43e10`.
No phase body or actual reboot ran in those checks: host-reboot status/start/exec/cleanup remains unqualified.
Ordinary tier batches exclude explicit phase execution and cannot substitute for completed PRE+reboot+POST.

The first Firecracker acquisition body succeeded, but its teardown failed when ordinary `cache clean`
encountered root-only protected parents. That invocation remains failed; the other two acquisition bodies
were not executed. [ADR-0022](../../docs/adr/0022-explicit-protected-cache-retirement.md) records the
approved typed retirement contract. Signed `0469195e` integrates the implementation and accepted capture,
parent-sync and nonterminal publication-sync corrections; focused API/auth/root-race checks passed.
Complete integration CI/build and normal inner installation subsequently passed, as recorded below.
Installed public cleanup qualification remains pending. Permanent snapshot-lock and volume-removal residues
remain an explicit separate gap; [ADR-0023](../../docs/adr/0023-protected-branch-authority-layout.md)
records the fresh-layout contract.
The design is now implemented in `b254f010`. Named-runner formatting and vet passed, then 149 focused
root tests/subtests and the same 149 race checks passed with no failures or skips. Complete integration
CI/root/build subsequently passed on `4bc51ee7`, as recorded below. New-candidate installation and public
snapshot/volume/cache-cleanup journeys remain pending. Old layouts
are refused, not migrated or rebound.
Signed `af285c1b` integrates the reviewed three-case acquisition sequence fixture: one receipt-owned store,
typed per-case kernel retirement, a single final cache cleanup and per-function cache/mirror environment.
All three acquisition bodies remain byte-identical; the original named-QA checks passed 13 pure cases with zero other
outcomes and collected all 22 kernel nodes. JUnit SHA-256
`b823c7b3e3e2b2bf061886ba86f00166111a1454dfc6fec5fdb621afaef94aef`.
No acquisition, download or candidate operation ran during those fixture checks.

The next three-case invocation failed in setup before any acquisition body: the retained cache had no
initialized database when inventory was requested. Signed `7fa57a7b` corrects the fixture to use normal
`cache init` before inventory, checking the same retained parent, cache, configuration, protected-store
and key identities before and after initialization. Named-runner checks passed all 22 focused fixture
cases with native exit 0 and strict zero-other outcomes; all 22 kernel nodes collected. JUnit SHA-256
`11cced12e8cb7b3dc523d5b8c56925a00555d4e9d20ee93da2c0b0dd1eda3476`.
The setup failure remains recorded. The corrected three acquisition bodies and final public cache cleanup
still require runtime qualification; passing fixture checks do not close that gap.

A subsequent setup failure followed successful normal initialization: the fixture rejected its audit
files and schema migration snapshots. Signed `ed16366d` admits only operator-owned regular `audit.log`,
`audit.log.lock` and database snapshots for schemas 2–8. Wrong ownership/type and unknown entries still
refuse. Only the twelve affected initialization checks were run; all passed with strict zero-other
outcomes, and all 22 kernel nodes collected. No acquisition body ran in those checks. The successful
initialization outputs and the failed runtime receipt are retained; acquisition/cleanup remains pending.

Signed `5f420009` gives the separate eight official-kernel cases one private cache, strict publication
receipts, per-case typed kernel retirement and one final typed cache cleanup. Actual default receipts must
report `is_default: true`; failed or ambiguous publications are not adopted by inventory difference.
The combined official and retained-cache fixture tests passed 34/34 inside the named runner with strict
zero-other outcomes; the unchanged 22 kernel nodes collected. JUnit SHA-256
`f5b853507354935d3f6fa628704d1cffa16adc070b82013d58debbd7bd61042b`.
Signed `4bc51ee7` also corrects the API's ignored default-write error and stale pull response; formatting,
vet and 120 existing API checks passed. Official acquisition/build flags and the installed default result
still need their real system journeys; these focused checks do not qualify them.

**Current coherent branch-layout Go/build checkpoint (2026-10-10):** clean `4bc51ee7`, tree `0be46ce2`,
passed complete pinned Go 1.26.3 ordinary CI, the separate full root Jailer suite and the real embedded-agent
amd64 release build inside `mvmctl-030-test`. Literal root JSON counts 2,707 test/subtest passes, one package
pass and zero failures/skips. CI log SHA-256 `4211c06c`; root JSON `c72fbcef`; terminal verification `46053ac8`.
Both atomic coverage profiles and all 48 manifested source inputs were checked. Candidate
`0.3.0-rc.layout2-4bc51ee7`, SHA-256
`e6ff428319a6d8ef981b6311c38a4e43b08b9cdfb0da0bc19fdd04f6bb61848e`, records clean exact VCS,
Go 1.26.3, Linux/amd64, CGO disabled, PIE and trimpath. Original state and installed `752110fa` binary
remained unchanged. No synthetic memory admission/abort guards or Go memory/concurrency throttles were
used. No installation or layout transition ran; installed public journeys remain separate requirements.

**Earlier coherent cache Go/build/install checkpoint (2026-10-10):** clean `752110fa`, tree `02198101`,
passed complete pinned Go 1.26.3 ordinary CI, the separate full root Jailer suite and the real embedded-agent
amd64 release build inside `mvmctl-030-test`. Independent literal root JSON counts 2,663 test/subtest passes,
one package pass and zero failures/skips. CI log SHA-256 `6e7eafcd`; root JSON `de1f2458`;
root counts `493b8e38`; terminal verification `b83aa2c0`. Both atomic coverage profiles and 36 exact
committed source inputs were checked. Candidate `0.3.0-rc.cache-752110fa`, SHA-256
`ea7bad330e625ff6374e34e6c65604266e93d8c524c702945e5e7fa4d74c87f5`, records the exact clean VCS revision,
Go 1.26.3, Linux/amd64, CGO disabled, PIE and trimpath. Normal inner `host install-system` passed;
the root-owned `0755` canonical binary matches the candidate. Installation terminal SHA-256 `ba092b7f`;
actual canonical identity `b1e3e74e`. Independent exact PRE/POST preservation admits only the declared clean
build source and, separately, that canonical replacement. No earlier installed system pass is relabeled
as a run on this build. Public cache cleanup, fresh branch-layout handling and full release qualification
remain separate requirements.

**Standalone SIGINT survivor checkpoint (2026-10-10):**
`TestImageImportCreateVM::test_vm_import_sigint_survivor_refuses_release_then_restores_and_discards`
passed alone on the installed `752110fa`/`ea7bad33` candidate: native exit 0, strict 1/1, zero other
outcomes, 13.37 seconds. The authenticated copy reached 31,457,280 bytes before caller interruption;
release was refused while the worker survived, root authority remained unchanged, and settlement/discard
restored the source state and guest-written bytes. Typed owned-image cleanup passed; both original and
prepared raw inventory snapshots matched exactly after their respective cleanup phases. Terminal SHA-256
`72b19993`; JSON `d428b33a`; exact singleton JUnit `70afcf72`. The earlier mixed invocation remains
native exit 1 and is not relabeled. This closes the standalone SIGINT gap without replaying receiver-death,
copy-I/O or normal-state cases; full release qualification remains separate.

**Earlier coherent Resume Go/build/install checkpoint (2026-10-09):** clean `1300b64b`, tree `1eb819b5`,
passed complete pinned ordinary Go 1.26.3 CI, the separate full root Jailer suite and the real embedded-agent
amd64 release build inside `mvmctl-030-test`. Independent literal root JSON counts 2,642 test/subtest passes,
one package pass and zero failures/skips. CI log SHA-256 `45b43ff8`; root JSON `02fbaa53`; build log `e8790bf1`.
Candidate `0.3.0-rc.resume-1300b64b`, SHA-256
`81c521ba65e82cdec0abc322b080aa3e0c38876d6313f359cf86713792720c57`, records exact VCS revision,
`vcs.modified=false`, Go 1.26.3, Linux/amd64 and PIE. Normal inner `host install-system` passed; its root-owned
`0755` canonical binary matches the candidate. Independent preservation proved original state unchanged,
apart from the admitted clean build source and the authorized canonical replacement. The affected receiver-death
and public pause/resume pair subsequently passed, as recorded below; no earlier installed pass is relabeled as
a run on this build.

**Earlier coherent Go/build checkpoint (2026-10-09):** clean `bfdece17`, tree `84dc90a1`, passed the complete
ordinary pinned Go 1.26.3 CI sequence, the separate full root Jailer suite and the real embedded-agent amd64
release build inside `mvmctl-030-test`. The literal root JSON independently counts 2,632 test/subtest passes,
one package pass and zero failures/skips. CI receipt SHA-256 `cea49dfc`; root JSON `b255ba5d`; build receipt
`424330be`. Candidate `0.3.0-rc.export-interrupt-bfdece17`, SHA-256
`d13f8c06335342dab47fc6e5399af7e195992ab16dcf1ad00c3289c692390e42`, records that exact revision,
`vcs.modified=false`, Go 1.26.3, Linux/amd64 and PIE. Independent literal before/after comparisons preserved
all preexisting runner inventories, administrator state, assets and file/source identities; only the authorized
fresh task source and manifest were admitted separately. Normal installation subsequently passed inside the
named runner: the root-owned canonical binary matches the candidate hash/version, and a second literal
preservation comparison differs only in the authorized canonical replacement. Installed `f909601d` evidence
below still qualifies only its matching unchanged inputs; it is not relabeled as a run on this new candidate. Remote GitHub CI,
remaining installed T1/T2 journeys and final release approval remain separate.

**Live-attachment completion source (2026-10-09):** `7a4a99de` integrates authenticated root-derived guest
disk readiness, retained known-applied attachment projection, explicit paused staging and a final root identity
recheck. Focused agent/vsock/API behavior checks, supporting VM/error/CLI packages, affected vet and the selected
agent/vsock/API race checks passed inside `mvmctl-030-test`; the integration's 12 source files matched the tested
manifest exactly. Complete ordinary Go 1.26.3 CI passed on clean `046e8347` inside the named runner, including
architecture, formatting, generation, vet and the selected test/coverage gates. The initial private wrapper
ended before building; its full CI log and both coverage profiles were retained, and a build-only continuation
preserved the exact generated profiles outside the checkout without replaying CI. The real embedded-agent
amd64 build then passed as `0.3.0-rc.readiness-046e8347`, SHA-256
`70c7ceacefc79c80f2995b0255fd54626a4de96812aa3bcb2fb82425a5871c4f`, with Go 1.26.3, PIE and
`vcs.modified=false` on that exact revision. Native producer traces and source/tool/artifact preservation
were independently checked. This candidate is not installed or system-qualified. Existing installed
`f909601d` evidence qualifies only unchanged relevant inputs, not this new live-attachment behavior;
the separate earlier full root-Jailer result retains its original source binding.
Readiness detects rather than repairs the separately diagnosed Firecracker BAR relocation failure.

**Installed live-attachment readiness (2026-10-09):** the new behavior is now exercised on installed
`0.3.0-rc.export-interrupt-bfdece17`/`d13f8c06`. The Running format/mount case passed immediate block-device,
`virtio_blk` driver and exact-capacity checks followed by filesystem I/O, without post-success polling.
Its invocation then failed in the newly added Paused test because inspect does not expose process start time;
the Stopped node was unexecuted. That failed receipt is retained, not relabeled as a passing three-case run.
Test-only `06e83dc4` retains full PID/start-time identity via `vm ls` and supported fields via inspect.
The corrected Paused and previously-unexecuted Stopped cases then passed together, strict 2/2 in 19.48 seconds.
Paused staging proved its retryable deferred error, retained binding, resume/same-attach retry, immediate
readiness, real I/O and idempotent reattach without detach, retaining process identity. Stopped proved persisted
configuration only, not guest readiness. Exact typed cleanup and both literal original/prepared inventories
matched; candidate/source/kernel/mirror/boot pins passed. No Running replay or Go CI/build replay was needed.
Terminal SHA-256 `d2b6d1b4bcc73a4bc24acce3cc44449c16cfb56a5f7b57f51143a59fca696b3b`; JSON
`835d953f295296ceee6e4f837c6d374f3ad8da8b8c6c21668e199081029639cf`; JUnit
`512078300918183998f4ff5c4fd571aa61334884a5b4f819701df2c4c09ba23c` identifies exactly Paused and Stopped.
The three readiness cases span two invocations; live detach/reattach remains the separate BAR failure.

**Current image-dependency deletion (2026-10-09):** the two registered Running/Stopped variants of
`TestImageDependencyDeletion::test_delete_image_used_by_stopped_vm_fails` passed strict 2/2 in 17.75 seconds,
native exit 0 and zero other outcomes, on installed `d13f8c06` and frozen clean `10b929f5` inside
`mvmctl-030-test`. Each used an owned imported Alpine image and networkless VM. Ordinary `image rm`
returned `image.in_use`, preserving the image row, backing identity and corresponding VM state.
Exact typed VM/image cleanup and identity-verified removal of the nondefault mirrored seed passed.
Both prepared and original public/physical/administrator inventories matched byte-for-byte;
source/candidate/kernel/mirror/boot/default pins were preserved. JSON/JUnit identify exactly these two nodes.
Terminal SHA-256 `5ded3c87e1376044dc2c5eedbd5ffc8a7380ad7a9a537110f2d02681cd5efa94`; JUnit
`023d13c5f84b451b07307d0bd7a2a8814c6b776a5e68e20cd90f46f155c9fc4b`.
This does not qualify referenced-image force removal, deletion/default-promotion behavior or the full image domain.

**Current formerly-default image removal (2026-10-09):**
`TestImageDependencyDeletion::test_delete_default_image_promotes_other_or_clears` passed separately,
strict 1/1, native exit 0 and zero other outcomes, on installed `d13f8c06` and clean `47967bc7`.
Despite its historical name, the body explicitly reselects the replacement before deleting the formerly-default
owned import; it does not exercise automatic promotion. Exact typed cleanup restored the byte-identical original
inventory and zero-default state. Raw prepared reports retain the two legitimate `SetDefault` timestamp updates;
only that newly owned Alpine record's `updated_at` differs, with every other field and backing identity preserved.
Source/candidate/kernel/mirror/boot/default pins passed. Terminal SHA-256
`dc56776fdb84086000872e40aa88fa67379b5e364005738a2aef2a4f4766e6e7`; exact-node JUnit
`276fcfe2933f1d5c6bee51741a831efb6a87bc39d01a156b0800aff78f7cecb5`.

**Current local image import/removal (2026-10-09):** four registered cases passed together, strict 4/4 in
7.76 seconds with native exit 0 and no other outcomes, on installed `0.3.0-rc.partition-f909601d` and clean
`b73d0462` inside `mvmctl-030-test`: local cached raw-file import, nonexistent-path refusal, and normal/force
removal of an unused imported image. JSON/JUnit agree on those exact nodes. Typed owned-image cleanup and
independent original/prepared public and physical inventory comparisons passed; kernel, defaults, mirror,
source and candidate identities remained unchanged. No VM launch, download or synthetic memory guard was used.
The local import checks ext4/Alpine metadata, not boot or guest contents. Force removal here does not prove
in-use removal. This is bounded unchanged-path evidence, not qualification of new guest readiness or the full
image domain.

**Current cached image selectors/defaults (2026-10-09):** seven further registered nodes passed together,
strict 7/7 in 18.92 seconds with native exit 0 and zero other outcomes, on the unchanged installed `f909601d`
candidate and clean `b73d0462` suite. Three cached slug/version/type-override selectors returned the exact
prepared Alpine record. The other cases checked default reselection, missing-default refusal, cached pull
with `--default`, and selecting a freshly imported raw image as the sole default. The wrapper admitted only
a newly owned mirrored Alpine as a temporary default after proving the original had none. Existing test
cleanup restored that prepared default; its `updated_at` display timestamp alone was excluded from the
prepared comparison, with raw values retained. After exact no-force owned-Alpine removal, the complete
original public/physical inventories and zero-default state matched without normalization. Source, kernel,
mirror and candidate pins were preserved. No downloads or artificial memory thresholds were used.
This does not qualify remote acquisition, force-repull variants, guest boot or the complete image domain.

**Current complete image-warming class (2026-10-09):** all four `TestImageWarm` nodes passed together,
strict 4/4 in 7.45 seconds with native exit 0 and zero other JSON/JUnit outcomes, on unchanged installed
`f909601d` and clean `b73d0462` inside `mvmctl-030-test`. Slug, prefix and `--all` checked complete hashes
against independent decompression and exact ready-file reuse; the missing selector returned `image.not_found`.
`--all` covered both the newly mirrored 200-MiB Alpine image and the existing 479-MiB Ubuntu image.
Both full prepared/original public and physical inventories matched without normalization. The original
Ubuntu compressed backing's metadata and content hash, every default, kernel, mirror and candidate pin
were preserved. Typed removal deleted only the owned nondefault Alpine; identity-bound cleanup removed
only fresh task-local warm/readback files. No VM, download, installation or artificial memory guard was used.
Terminal receipt SHA-256: `e014cdf2b22bdddd5ffb6d2ec0970e56b4d0beaba6aebb9377de093c72c418cd`.
This completes the current warm class, not remote producers, force-repull, guest boot or the full image domain.

**Current protected-rootfs VM image import (2026-10-09):**
`images/test_images.py::TestImageImportCreateVM::test_vm_import_preserves_guest_contents_and_source_state[running]`
passed strict 1/1 in 27.69 seconds, native exit 0 and zero other outcomes, on installed
`0.3.0-rc.partition-f909601d` and clean `b73d0462` inside `mvmctl-030-test`.
The unchanged module SHA-256 is `1d0d4fafeced2b22b4e3464762450128ccc4175996d78408ba60e66c75f1ce09`.
The source guest wrote and synced a unique marker; import captured its protected disk, restored Running state,
and an independently booted clone read the exact guest-written bytes. Running state was checked again after
clone removal. Strict JSON/JUnit, original and prepared public/physical inventories, exact source/kernel/mirror
identities, and typed source/clone/imported-image/nondefault-Alpine cleanup were independently checked.
Assets came from the existing mirror; no artificial RAM admission or abort guard was used.
The initial private launcher required a lazily created export directory prematurely and executed no test;
its seven-line preflight correction left creation to the typed product operation, with no manual root repair.
The same registered method's `[paused]` and `[stopped]` parameters subsequently passed strict 2/2 in
34.59 seconds on the same source and installed candidate, with zero other outcomes. Each source retained
its original state, and each independent clone read the exact guest-written marker. The Stopped case also
wrote contradictory bytes to its caller-owned staging disk, proving import used protected backing rather
than that stale copy. Strict JSON/JUnit and original/prepared inventory equality were independently checked;
typed cleanup removed only each owned VM/image and the wrapper's nondefault Alpine acquisition. Assets,
kernel, defaults and source pins were preserved; no synthetic memory guards were used.
This supersedes the earlier memory-guard-interrupted normal import results for the unchanged data path,
not interruption/recovery, reusable-base or full image-domain qualification.

**Current export copy-I/O failure (2026-10-09):**
`images/test_images.py::TestImageImportCreateVM::test_vm_import_mid_copy_io_failure_restores_source_and_discards_partial`
passed strict 1/1, with native exit 0 and zero other JSON/JUnit outcomes, inside `mvmctl-030-test` on the same
installed `f909601d` candidate. Test inputs were a single-file fixture correction over clean `b73d0462`,
subsequently integrated unchanged in `bfdece17`, SHA-256
`026e99baf6d041551824fd212f85a077df1222c963457d75a0d8960ada9b2d3d`.
The observer authenticated the exact root receiver and partial copy, imposed a file-size limit, and proved
the failed copy reached that limit. The source returned to its original state with its guest marker preserved;
partial image publication was refused and typed removal was idempotent. The observer exited 0 with empty
stderr. Exact owned cleanup and both complete original/prepared public and physical inventories matched;
source, candidate, boot, kernel and mirror pins remained unchanged. Assets were mirrored and no artificial
memory guard was used. Literal terminal receipt SHA-256:
`a6c47d763d47f5a3fbcde0674ed96288a7127dfaa7a81a0e99bb49b3496a91ad`.
This qualifies this real copy-failure path only. Earlier SIGINT and abrupt caller-death attempts failed root
receiver-survival checks before later recovery assertions. The correction and later installed result are
recorded below; those earlier failed receipts are not relabeled as passes.

**Export-interruption source correction (2026-10-09):** `3e00e00e` integrates the named, fixed-action,
payload-free export transport. Its worker uses `Setsid`, while cancellation closes caller-owned control resources
without a fatal group signal. Ordinary actions retain `Setpgid` and their existing kill/reap behavior. Physical-EOF
authority, pre-start refusal, unknown-outcome diagnostics, the bounded caller wait and one eventual owned reaper
are unchanged; root restoration and failed-response Release are untouched. Focused Go 1.26.3 transport and
Jailer routing tests, affected vet and race checks passed inside `mvmctl-030-test`; every integrated Go source
hash matched the tested six-file manifest. One isolated pre-fix survival regression failed because the worker
was killed; the fixed test proved survival, deliberate later completion and exactly one reaping wait.
The fixture-only `bfdece17` also passed exact six-node collection and the selected missing-admission runner unit.
Both changes are now installed inside the named runner after coherent ordinary CI, full root-Jailer and
real-agent build passed on `bfdece17`. The first installed attempt observed the authenticated stopped worker
surviving caller SIGINT, then failed a stale busy-removal public-code assertion. Its follow-on cleanup observer
also failed after the assertion, although original/prepared inventories were restored exactly. Receiver-death
was unexecuted because of fail-fast. Test-only `0b3762d9` now asserts the outer `image.import.failed` envelope,
retryable class, exact message and pending-copy details; every independent root lock/survival/recovery proof
is unchanged. Exact two-node collection and the selected admission unit passed.

The corrected installed invocation subsequently collected and ran both registered nodes: one passed, one
failed, zero errors/skips/deselections/XFAILs/XPASSes. The SIGINT case passed its complete busy-copy refusal,
unchanged authority, worker settlement, original-state/marker restoration and typed cleanup journey. The
receiver-death case failed at `test_images.py:1813`: its authenticated dead-worker receipt retained pending
source recovery, but public `vm ls` reported `running` instead of the expected `paused`. This observation does
not establish whether the defect is production behavior or the test boundary; diagnosis remains open.
Both root observers exited 0 with empty stderr; complete original and prepared inventories matched literally,
and exact owned-image removal and final pins passed. Native exit was 1, not a passing slice. Terminal receipt
SHA-256 `5ef6114ab249f304be268c7f05c82421a5c0a9c0f9eebd5cc860ebbf8a0e9f4c`; outcome JSON
`340bec4558f0fbcc5dec7c2160930ae9e2cd254a0420fe014f26f84d51f365ac`; JUnit
`c7744d818b3ccd2425b2407a91da4bbad52482b5b3905c6c6b60f72b5b74665e` identifies both exact nodes.
Installed candidate/source remain `d13f8c06`/`bfdece17`; Python test source was clean `0b3762d9`.
Do not replay the passed SIGINT case or the unchanged failing receiver-death command.

**Resume-authority source correction (2026-10-09):** after the test boundary was corrected to observe actual
root-authenticated CPU state, the receiver-death singleton reached Paused and released-copy idempotence,
then failed because public `vm resume` returned success while root export recovery remained pending.
`1300b64b` removes the cached Running/Starting bypass; the existing root export-idle check now precedes
authenticated state observation and any resume mutation. Actual Running is an authorized no-op, Paused
receives one fixed PATCH, and prestart/malformed state is refused. The public ID/Applied result contract is
unchanged. Test-only `e2fd9881` checks the existing `cli.batch.failed` / item `vm.resume_failed` /
`image.import.failed` response rather than expecting a data-less error.
All seven integrated Go files matched the named-runner tested manifest. Focused Go 1.26.3 formatting,
vet and behavior/race checks passed; controller ordinary and race runs each report 52 test/subtest passes,
and the root broker race run reports 10 with zero failures/skips, including real pending-export journal
refusal and unchanged source authority. Literal root log SHA-256
`db9669a84f9d8c6b770f108651080b1e30fba985d63cd9cf95ddb43ff74d3c89`.
The Python correction collected one node and passed the existing missing-admission unit separately.
The later coherent CI/root/build and normal inner installation passed on `1300b64b`, as recorded above.
The focused checks and installation are separate from the later affected installed runtime result below.

**Installed Resume correction qualification (2026-10-09):** clean `1300b64b` and installed `81c521ba`
passed exactly `TestImageImportCreateVM::test_vm_import_receiver_death_retires_orphan_and_recovers_source`
and `TestVMStateTransitions::test_pause_resume_preserves_process_and_guest`, strict 2/2 in 32.30 seconds,
with zero failures, errors, skips, deselections, collection errors, XFAILs or XPASSes. The first case killed the
actual receiver during its partial copy, retained pending recovery, twice observed root-authenticated Paused
state and proved idempotent copy release. Public resume returned the required batch/item refusal, then public
import recovery succeeded with source and independent-clone marker readback. The second case proved actual
Running no-op and Paused resume with unchanged process and guest boot identity. Exact native owned cleanup
passed; prepared and original raw baselines each matched byte-for-byte, and source, candidate, assets and
defaults retained their pins. Terminal SHA-256 `9389e068`; JSON `835d953f`; JUnit `1e3a86fd`.
The old receiver failures remain failures; the passed SIGINT case was not replayed. This qualifies the
demonstrated correction, not the complete image/lifecycle modules, reusable base or full release.

**Empty kernel-list singleton (2026-10-09):** the previously unqualified registered
`kernel/test_kernel.py::TestKernelLifecycle::test_kernel_list_empty` passed strict 1/1 with zero other outcomes
on installed `bfdece17`/`d13f8c06`, clean Python source `e2fd9881`, before the Resume installation.
Private config/cache initialization used the existing trusted 1.16.2 pair. Typed selected-cache cleanup passed;
the complete original public/physical/administrator baseline matched byte-for-byte, with asset identities and
defaults preserved. Terminal SHA-256 `a688c53c`; JSON `d428b33a`; JUnit `ab4f821b`. This does not qualify
kernel acquisition, producers, the later Resume artifact or the complete kernel domain.

**Cached-kernel default/removal remainder (2026-10-09):** installed `81c521ba`, clean `1300b64b`, passed
`TestKernelLifecycle::test_kernel_set_default` and `TestKernelRemove::test_kernel_rm_by_id`, strict 2/2 with
zero other outcomes. Both imported the pinned existing ELF under unique QA versions; default restoration
and exact typed imported-ID cleanup passed. Normalized before/after public/physical/administrator inventories
matched exactly, retaining raw timestamp maps: only original `kernel.updated_at` advanced during default
restoration. Every other original field, default ID, backing identity, mirror, boot, candidate and source pin
was unchanged. Native pytest completed in 1.323 seconds. Terminal SHA-256 `6af46f48`; JSON `835d953f`;
JUnit `287c4299`. No download, VM launch or accepted-case replay occurred; acquisition, producer and
stopped-VM dependency qualification remain separate.

**Kernel registry and stopped-reference remainder (2026-10-10):** two separate singleton invocations on
installed `81c521ba`, clean `1300b64b`, each passed strict 1/1 with zero other outcomes:

- `TestKernelLifecycle::test_kernel_ls_remote`: native pytest 0.946 seconds; the public registry returned
  nonempty structured metadata. Full original public/physical/administrator state and raw timestamps matched
  exactly. Terminal SHA-256 `513c9d73`; JSON `d428b33a`; JUnit `0faf30e1`. No asset acquisition occurred.
- `TestKernelStoppedVMDeletion::test_delete_kernel_used_by_stopped_vm_is_denied`: native pytest 6.416 seconds;
  a stopped owned VM retained its kernel reference. Ordinary removal returned the expected `kernel.in_use`
  batch-item error, and both records remained. Typed VM/kernel/network and owned-image cleanup passed;
  original and prepared raw baselines each matched exactly, including kernel timestamps. Terminal SHA-256
  `b317358f`; JSON `d428b33a`; JUnit `f4e7e61f`.

The stopped-test private launcher had one stale report filename corrected before execution; only that
correction received static delta verification, with no failing runtime or test-body replay. These passes do
not qualify acquisition, official builds, reusable-base provisioning or the complete kernel domain.

**Current snapshot producer-failure recovery (2026-10-09):**
`vm/test_jailer.py::TestNetworklessJailerLifecycle::test_snapshot_capture_file_limit_failure_recovers` passed
strict 1/1 in 18.39 seconds on installed `0.3.0-rc.export-interrupt-bfdece17`, clean `bfdece17` and unchanged
registered test bytes. This refresh was required by the newer capture-idle admission check, not a replay of
an unchanged passing path. It proves real file-limit capture failure, source recovery and subsequent full
rootfs/RAM/vmstate/data-volume restoration. Native exact collection/runtime exited 0; JSON/JUnit identify
only that node, with zero other outcomes. Independent original/prepared inventories matched literally,
typed owned cleanup restored zero image defaults, and candidate/source/kernel/mirror/boot pins matched.
Terminal receipt SHA-256 `49c8b165`; JUnit `4153e709`. This is not whole-snapshot or full-release qualification.

**Current administrator filesystem recovery (2026-10-09):**
`host/test_host_admin.py::TestCleanHostAdministration::test_filesystem_failure_preserves_init_and_reset_retry_authority`
passed strict 1/1 in 44.08 seconds inside `mvmctl-030-test`, with native exit 0 and zero other outcomes.
The hash-bound overlay on clean `b73d0462` is integrated unchanged in `da68d816`; it exercised installed
`0.3.0-rc.partition-f909601d`, SHA-256 `13425005db12c4c13f34a5ca140ec2979dd8471d2c3e1bd271946d28a4364b35`.
Actual read-only sysctl faults proved partial first-init journal retention and retry, disabled admission during
failed reset, successful reset retry, same-ID pending-network activation after re-enrollment, and typed removal.
Both fault mounts restored their exact original identities; child cleanup and independent original
public/physical/administrator inventories matched. This supersedes the earlier package-setup limitation for
this case, not the separate administrator journeys, abrupt interruption or complete release qualification.

**Current bounded hotplug results (2026-10-09):** Eight of the nine registered
`volume/test_volume_hotplug.py` cases have passing evidence on the same installed candidate in the named runner.
Separate invocations proved initial guest device discovery, format/mount/write/read, live unplug, no-PCI refusal,
cold attach/detach, stopped-only force removal, and duplicate-attach idempotency. The final singleton passed
strict 1/1 in 6.35 seconds; the exact ownership-aware test overlay is integrated in `ed8a27dd`.
Owned-resource removal and original inventories/defaults/mirror preservation were checked independently.
**Live reattachment failed:** after successful attach, detach and reattach commands, the same-sized guest block
device did not reappear within ten seconds. This was run with expected-failure handling disabled; it is not a
passing XFAIL. A subsequent owned-VM diagnostic established a PCI BAR mismatch: initial BAR `0x4000180000`,
reattached Firecracker BAR `0x4000200000`, and Linux reassignment to the old address. Guest dmesg recorded the
failed BAR update and `virtio_blk` probe error `-22`. An explicit PCI rescan returned exit 0, but the disk remained
absent. Five snapshots, command statuses, exact owned cleanup and original inventory restoration were retained;
successful diagnostic delivery is not successful reattachment.
The released [Firecracker 1.17.0 BAR relocation implementation](https://github.com/firecracker-microvm/firecracker/blob/v1.17.0/src/vmm/src/devices/virtio/transport/pci/device.rs#L657-L715)
and [unplug resource retirement](https://github.com/firecracker-microvm/firecracker/blob/v1.17.0/src/vmm/src/device_manager/pci_mngr.rs#L155-L204)
address those source-level gaps. That pair has not been admitted or runtime-qualified by this project;
the current exact launch pin remains 1.16.2. The module remains unqualified pending correction.
These direct named-runner slices are not a consolidated module invocation or host-direct Tier 3 qualification.

**Current read-only kernel results (2026-10-09):** Six local cases passed together, strict 6/6 in 0.19 seconds
on clean `b73d0462` and the same installed candidate: local list/JSON, the complete `TestKernelInspect` class,
and nonexistent-kernel removal refusal. The existing imported default satisfied prerequisites without pulling,
importing or changing defaults. Independent JSON/JUnit identities, original public/physical inventories,
kernel and mirror-volume identity checks passed. These are bounded direct named-runner results, not kernel
producer, remote registry, full-domain or host-direct Tier 3 qualification.

**Current local kernel import options (2026-10-09):** The remaining auto-version, default-selection and three
error/idempotency cases passed together, strict 5/5 in 5.56 seconds, native exit 0 and zero other outcomes.
They used the same installed `0.3.0-rc.partition-f909601d` and clean `b73d0462` suite; the current module SHA-256
is `1c246cc0fe8145e70556f6c9995b19d5f024e8d49977741a04ed72298336874e`.
Only the existing cached ELF was consumed, with no download or VM launch. Exact owned imports were removed,
the original default and kernel file identity were restored, and original public/physical inventories matched.
Comparison excludes only the existing import fixture's observational kernel `updated_at` field; raw before/after
timestamps were retained. All other fields, default selection, inode/hash/file timestamps and mirrors remained exact.
These cases supplement the three separately passed kernel-import lifecycle cases, not remote acquisition,
kernel builds, full-domain or reusable-base qualification. Previously passed nodes were not replayed.

**Current image/kernel import checkpoint (2026-10-09):** Complete classes
`images/test_images.py::TestImageImportAdvanced` (eight expanded nodes) and
`kernel/test_kernel_import.py::TestKernelImportLifecycle` (three nodes) passed together, strict 11/11
in 33.04 seconds with native exit 0, inside `mvmctl-030-test`. Clean suite `b73d0462` exercised the same
installed `0.3.0-rc.partition-f909601d`, SHA-256 `13425005db12c4c13f34a5ca140ec2979dd8471d2c3e1bd271946d28a4364b35`.
Cases covered QCOW2 conversion and actual boot, force replacement, explicit partition selection and detector
options, automatic format/version/optimization options, imported-kernel boot and stop/start identity.
Independent JSON/JUnit checks proved exact case identities and zero other outcomes; exact owned cleanup and
original public/physical inventories, defaults, existing mirror-volume source and kernel identities matched.
This completes these two classes, not the entire image/kernel domains, build/export faults or release matrix.

**Current complete lifecycle/volume node coverage (2026-10-09):** All 69 registered nodes in
`vm/test_vm_lifecycle.py` and all 37 in `volume/test_volume.py` now have passing installed-candidate
evidence on clean suite `b73d0462` inside `mvmctl-030-test`. The same `0.3.0-rc.partition-f909601d`
candidate, SHA-256 `13425005db12c4c13f34a5ca140ec2979dd8471d2c3e1bd271946d28a4364b35`, passed the
last 21 lifecycle/list/SSH/volume and cold-snapshot cases together in 92.75 seconds, then the sole missing
multi-identifier removal case in 6.29 seconds. Both native invocations exited 0 with strict JSON/JUnit
outcomes and zero failures, errors, skips or expected failures. Current runner collection matched the
106 unique passed node identities across eight separately bound batches; no earlier passing node was replayed.
Exact owned cleanup, original public/physical inventories, configuration, defaults and mirror preservation
passed. No synthetic RAM-abort guards were used. This completes registered-node coverage for these two files,
not a consolidated invocation, stronger behavior than their assertions, the separate hotplug module,
host-reboot/fault journeys, reusable-base provisioning or complete installed T1/T2 release qualification.

**Current stopped-removal hotplug correction (2026-10-09):** The corrected
`volume/test_volume_hotplug.py::TestVolumeHotplugDestructive::test_hotplug_force_remove_attached_volume`
collected exactly once and passed strict 1/1 in 5.84 seconds inside `mvmctl-030-test`, with zero other
outcomes and native exit 0. The installed candidate remained `0.3.0-rc.partition-f909601d`, SHA-256
`13425005db12c4c13f34a5ca140ec2979dd8471d2c3e1bd271946d28a4364b35`. The frozen test overlay on
clean base `b73d0462` was explicitly hash/diff bound and is integrated unchanged in `0a4fb4ea`.
The real hotplugged device remained guest-visible while running force-removal was refused; backing identity,
attachment and VM state stayed intact. Stopping then permitted exact removal without detach, removing the
managed source and VM reference. The case cleaned exact owned VM/volume/key/network resources; independent
original public/physical PRE/POST inventories, defaults and mirror matched after verified nondefault Alpine
removal. An initial QA source-clone permission mismatch stopped before collection; explicit task-local
umask 022 corrected that staging cause without changing the test or candidate. No synthetic RAM guards
were used. This does not qualify the other hotplug cases, full module, reusable base or complete release matrix.

**Current public-create checkpoint (2026-10-09):** Installed `0.3.0-rc.partition-f909601d`,
SHA-256 `13425005db12c4c13f34a5ca140ec2979dd8471d2c3e1bd271946d28a4364b35`, passed 27 registered
`vm/test_vm_lifecycle.py` cases together on clean signed suite `b73d0462` inside `mvmctl-030-test`:
strict 27/27, zero other outcomes, native exit 0, 96.65 seconds. The selected classes cover advanced
creation flags and SSH filepath/account setup, cloud-init modes, fixed NoCloud port behavior and custom-port
refusals, static IP/MAC and named-network behavior, console creation, count/atomic-count and count refusals,
force creation, and an explicit vsock port. The existing user-data-script case checks generated seed bytes;
its pass does not prove execution of that script inside the guest. Existing ownership fixtures removed exact
VM/network/key generations and preserved configuration. The wrapper removed only its identity-proven
nondefault mirrored Alpine image without force. Independent literal JSON/JUnit node identities and original
public/physical PRE/POST equality passed, with defaults and source/candidate/boot/mirror bindings preserved.
No synthetic RAM guards were used. Remaining lifecycle/list/SSH/volume options, host reboot, fault journeys,
reusable-base provisioning and complete installed T1/T2 qualification remain separate.

**Current VM-dependent volume checkpoint (2026-10-09):** Installed `0.3.0-rc.partition-f909601d`,
SHA-256 `13425005db12c4c13f34a5ca140ec2979dd8471d2c3e1bd271946d28a4364b35`, passed the nine remaining
registered `volume/test_volume.py` nodes together on clean signed suite `b73d0462` inside `mvmctl-030-test`:
strict 9/9, zero other outcomes, native exit 0, 48.49 seconds. These cover attachment-state invariants,
retention of a changed caller source, three attach/detach/restart journeys, cross-VM attachment refusal,
running-volume deletion refusal, and nonexistent attach/detach errors. Existing fixtures removed their exact
owned VM/volume/key/network resources. The wrapper removed only its identity-proven nondefault mirrored
Alpine image, without force; image defaults stayed unset. Independent literal JSON/JUnit identity checks,
original public/physical PRE/POST equality and exact preservation of the existing 6 GiB mirror-volume source
passed, with all source/candidate/boot/default bindings preserved. Together with the separately bound
26 non-VM and two stopped-operation cases, all 37 currently registered nodes in this file have pass evidence
across three invocations. This is not one consolidated module invocation, the separate hotplug module,
reusable-base provisioning, or complete installed T1/T2 qualification. No synthetic RAM guards were used.

**Current VM-options checkpoint (2026-10-09):** Installed `0.3.0-rc.partition-f909601d`,
SHA-256 `13425005db12c4c13f34a5ca140ec2979dd8471d2c3e1bd271946d28a4364b35`, passed the complete
15-node `vm/test_vm_lifecycle.py::TestVMConfigOptions` class together on clean signed suite `b73d0462`
inside `mvmctl-030-test`: strict 15/15, zero other outcomes, native exit 0, 43.43 seconds.
Guest observations proved CPU/RAM/root-disk allocation, config-versus-CLI precedence, custom boot arguments,
both PCI transport choices, and logging/metrics enablement. Five invalid-resource variants were refused
with typed errors before a VM projection appeared. The precedence case restored the exact original CPU
configuration override. Existing fixtures removed exact owned VM generations; the wrapper removed only
its identity-proven nondefault mirrored Alpine image, without force or default changes. Independent literal
JSON/JUnit identities and original public/physical PRE/POST equality passed, with source/candidate/boot,
defaults and mirror bindings preserved. No synthetic RAM guards were used. This completes this class,
not the remaining lifecycle/host-reboot/fault journeys, reusable-base or complete installed T1/T2 matrix.

**Current non-VM volume checkpoint (2026-10-09):** Installed `0.3.0-rc.partition-f909601d`,
SHA-256 `13425005db12c4c13f34a5ca140ec2979dd8471d2c3e1bd271946d28a4364b35`, passed 26 registered
`volume/test_volume.py` cases together on clean signed suite `b73d0462` inside `mvmctl-030-test`:
strict 26/26, zero other outcomes, native exit 0, 9.89 seconds. These cover the 25 non-VM
`TestVolumeLifecycle` nodes and `TestRawVolumeAuthoritativeStorage::test_create_grow_inspect_and_remove`:
raw creation and options, list/inspect JSON, duplicate and invalid-input refusals, removed format-flag
refusals, growth and shrink refusal, exact removal, multiple removal and partial failure.
Native fixtures cleaned their owned volumes; no VM, image, default or kernel setup was needed.
Independent literal JSON/JUnit checks proved exact case identities and original public/physical PRE/POST
equality. The existing 6 GiB asset-mirror volume and its exact source identity were preserved; source,
candidate and runner-boot bindings matched. No synthetic RAM guards were used. VM-dependent attachment,
hotplug, broader fault paths, reusable-base provisioning and the complete installed T1/T2 matrix remain separate.

**Current volume/snapshot contract checkpoint (2026-10-09):** Installed `0.3.0-rc.partition-f909601d`,
SHA-256 `13425005db12c4c13f34a5ca140ec2979dd8471d2c3e1bd271946d28a4364b35`, passed five registered
cases together on clean signed suite `b73d0462` inside `mvmctl-030-test`: strict 5/5, zero other outcomes,
native exit 0, 52.639 seconds. `TestVolumeRunningVMDependency` proved force removal requires stopping the VM
and an attached data volume can grow while stopped without detach. `TestNetworklessJailerLifecycle` proved
independent restored data-volume bytes after both normal source removal and an exact source crash, plus
refusal while another VM holds writable access and successful capture after that writer is removed.
Existing fixtures cleaned exact owned VM/snapshot/volume/key/network resources. From an initial zero-default
image state, the wrapper temporarily selected only its new mirrored Alpine image; exact no-force removal
restored the original zero-default state. Its warm-image pool was task-local, not shared. Independent original
public/physical PRE/POST equality, mirror and source/candidate/boot/default-kernel bindings passed. No synthetic
RAM guards were used. This is not complete volume/snapshot, managed-network restore, interruption/fault,
reusable-base or installed T1/T2 release qualification.

**Current host-exec checkpoint (2026-10-09):** The same installed `0.3.0-rc.partition-f909601d`
candidate, SHA-256 `13425005db12c4c13f34a5ca140ec2979dd8471d2c3e1bd271946d28a4364b35`, passed all six
registered `exec/test_exec.py` cases together on clean signed suite `b73d0462` inside `mvmctl-030-test`:
strict 6/6, zero other outcomes, native exit 0, 4.909 seconds. The existing cases cover basic commands,
PTY interactive shell, explicit agent port, nonroot user, missing VM and invalid 32-bit port refusal.
The shared fixture removed its exact owned VM; the wrapper removed only its verified mirrored nondefault
Alpine image without force. Independent original public/physical PRE/POST equality, defaults and all artifact,
source, boot and mirror bindings passed. No synthetic RAM guards or default changes were used.
This completes this registered file, not host-reboot recovery, reusable-base provisioning or full T1/T2 qualification.

**Current lifecycle/status checkpoint (2026-10-09):** Installed `0.3.0-rc.partition-f909601d`, SHA-256
`13425005db12c4c13f34a5ca140ec2979dd8471d2c3e1bd271946d28a4364b35`, passed five registered
`vm/test_vm_lifecycle.py::TestVMStateTransitions` cases together on clean signed suite `b73d0462`
inside `mvmctl-030-test`: strict 5/5, zero other outcomes, native exit 0, 44.740 seconds.
Pause/resume preserved the process and guest; three stop/start cycles checked idempotence and fresh working
guest identity; graceful and forced reboots checked changed process/boot identity. The crash journey used
exact pidfd-bound task admission, proved `crashed` in list/inspect and exclusion from `ps`, then verified
typed stop/start and working vsock exec. Exact owned VM/network removal passed under the current explicit
qualification-host admission. Independent full original public/physical PRE/POST equality and verified
nondefault mirrored-image removal passed, with existing defaults, source/candidate/boot pins and mirror preserved.
No synthetic RAM guards were used. This does not qualify guest-initiated shutdown, outer-host reboot,
the remaining lifecycle options, reusable-base provisioning or the complete installed T1/T2 matrix.

**Current exec restart/restore checkpoint (2026-10-09):** Installed
`0.3.0-rc.partition-f909601d`, SHA-256
`13425005db12c4c13f34a5ca140ec2979dd8471d2c3e1bd271946d28a4364b35`, passed five further registered
`exec/test_vm_to_vm_exec.py` cases inside `mvmctl-030-test` using clean signed suite `b73d0462`.
The endpoint restart/destination reboot journey passed strict 1/1 in 24.522 seconds; the opt-in/exact-nonroot-user,
stream-result/redacted-audit, interruption/reaped-destination-group and restored-endpoint/new-grant cases
passed together strict 4/4 in 40.975 seconds. Both native invocations exited 0 with zero other outcomes.
The durable grant admitted fresh endpoint process/boot identities after restart/reboot, while stopped and
revoked access was denied. Full restore retained guest state but required a new VM identity and grant.
Registered fixtures removed their exact VM/policy/capture resources, and the wrapper removed only its verified
nondefault mirrored Alpine image without force. Independent comparisons confirmed exact original public/physical
PRE/POST inventories, with defaults, mirror and source/candidate/boot bindings preserved. No synthetic RAM
guards were used. Alongside the separately bound directional journey, all six currently registered VM-to-VM
exec nodes now have pass evidence across three invocations; this is not one complete-module run, host-to-guest
exec qualification, host reboot, reusable-base provisioning or full installed T1/T2 release sign-off.

**Current traffic-policy checkpoint (2026-10-09):** Installed `0.3.0-rc.partition-f909601d`, SHA-256
`13425005db12c4c13f34a5ca140ec2979dd8471d2c3e1bd271946d28a4364b35`, passed all three registered
`network/test_policies.py` nodes together inside `mvmctl-030-test` in 73.20 seconds: strict 3/3, zero other
outcomes and native exit 0. The same-network and cross-network TCP journeys prove default denial, exact
grant, adjacent-port denial and revocation; the source-VM UDP journey also checks sibling-source and reverse
denial with receiver-side delivery evidence. The signed `276fc27c` traffic bodies and used helper functions
are unchanged through `b73d0462`; this is not a full execution of that later suite. The normal runner-local
`pytest-timeout` 2.2.0 prerequisite was installed with no package upgrades or removals before fixture admission.
Public/physical PRE/POST inventories, existing kernel/network defaults, source/candidate/boot pins and mirror
matched. Registered fixtures cleaned their exact owned resources; only the identity-proven nondefault Alpine
image was removed without force afterward. No synthetic memory guards were used. Reusable-base provisioning,
the remaining exec/lifecycle/fault journeys and complete installed T1/T2 qualification remain pending.

**Current directional-exec checkpoint (2026-10-09):** Installed `0.3.0-rc.partition-f909601d`,
SHA-256 `13425005db12c4c13f34a5ca140ec2979dd8471d2c3e1bd271946d28a4364b35`, passed the registered
`exec/test_vm_to_vm_exec.py::test_directional_vm_to_vm_exec_policy` inside `mvmctl-030-test` in
31.76 seconds: strict 1/1, zero other outcomes and native exit 0. The unchanged signed `276fc27c`
journey covers same-network, cross-network and networkless placement, proving denied destination effects
before grant, exact nonroot-user execution after grant, reverse denial and denial after revocation.
Its exact owned VM/policy/network cleanup passed. The wrapper removed only its identity-proven mirrored
nondefault Alpine image without force; final public/physical inventory matched the prepared baseline, with
mirror, source, boot and candidate pins preserved. Before that baseline, typed default selection of the existing
hash-bound imported kernel was recorded as deliberate retained QA setup; the original nondefault kernel state
was not restored. This is one registered multi-placement journey, not the complete exec domain, traffic-policy
qualification, reusable-base provisioning or full T1/T2 release sign-off.

**Exact qualification-runner admission correction (2026-10-09):** `a091b4e6` replaces literal named-runner
checks at the bridge, image-import receiver and Firecracker fault boundaries with explicit nonempty exact
`MVM_TEST_QUALIFICATION_HOST` / `MVM_TEST_VM` admission. T1/T2 forwarding binds the value to the provisioned
runner; Tier 3 receives no automatic grant. Existing resource authority and cleanup checks remain intact.
Inside `mvmctl-030-test`, 15 focused Python runner behavior tests passed in 0.91 seconds, including missing,
empty and mismatched admission refusals at all three boundaries. Collection of the three affected system files
passed with 144 nodes in 0.59 seconds; collection is not runtime qualification. Earlier fault passes retain
their original module bindings; the correction does not claim new complete T1/T2 or fault-runtime evidence.

**Current CLI and atomic-firewall checkpoint (2026-10-09):** Installed candidate
`0.3.0-rc.partition-f909601d`, SHA-256
`13425005db12c4c13f34a5ca140ec2979dd8471d2c3e1bd271946d28a4364b35`, passed the complete
registered `cli/test_cli.py` file inside `mvmctl-030-test`: strict 24/24, zero other outcomes,
native exit 0 and 0.42 seconds. Public resource/configuration inventories and exact source, binary and boot
bindings were preserved. The CLI suite created no VM or asset and changed no defaults.

Three registered `network/test_nftables.py::TestAtomicRuleSync` cases passed separately on the same candidate:
strict 3/3, zero other outcomes, native exit 0 and 2.71 seconds. These prove established/related rule retention,
repeat-sync rule equality and masquerade preservation. Existing typed fixtures removed their exact owned
networks; full public and physical PRE/POST inventories, active default, mirror and source/artifact pins matched.
Both runs used the clean signed Python suite `276fc27c`; its executable inputs match `066a02af`.
They used no synthetic memory admission or abort thresholds. These are separate installed-candidate slices,
not normal reusable-base provisioning, VM traffic/exec policy qualification or complete T1/T2 release sign-off.

**Current network fixture checkpoint (2026-10-09):** Installed `0.3.0-rc.partition-f909601d`,
source `f909601d` and candidate SHA-256 `13425005db12c4c13f34a5ca140ec2979dd8471d2c3e1bd271946d28a4364b35`,
passed three existing `network/test_network.py::TestNetworkLifecycle` cases together in `mvmctl-030-test`:
`test_network_create_no_nat`, `test_network_set_default` and `test_network_create_with_default_flag`.
The corrected inactive-original-default fixture was integrated as `f5d6aa1e`; the strict report records
three passes in 2.83 seconds, zero other outcomes and native exit 0. Fixture PRE/POST existing inventories
matched, with defaults, configuration, policies, links, routes, mounts, firewall and mirror preserved.
Cleanup used the existing fixture; this is not independent whole-root absence proof. The original setup
failure remains failed, and its no-NAT body did not run.

A separate batch of 23 other existing owned-network CRUD/rejection cases passed on the same installed
candidate and corrected fixture in 19.12 seconds: strict 23/23, zero other outcomes and native exit 0.
Fixture PRE/POST full inventories matched; mirror and source/artifact pins were preserved, with no preservation
errors.

Three existing `TestNetworkVMDependency` cases passed separately in 34.44 seconds using the explicit kernel
ID/digest pair, a non-default Alpine image and serial 256 MiB VMs. The tested module (SHA-256 prefix
`f61ae839`) was subsequently integrated as `4563e8a7`; production candidate identity was unchanged.
Fixture cleanup and exact owned-image
CLI removal without force restored the original full baseline, mirror and pins. A separate existing
`TestNetworkSyncMissingBridge` case passed in 1.38 seconds using the unchanged signed `f5d6aa1e` module:
exact owned-bridge deletion made sync fail closed, followed by typed logical retirement and full baseline
restoration. The reports record strict 3/3 and 1/1 respectively, zero other outcomes and native exit 0.

The final `TestNetworkSync::test_network_sync_preserves_all_networks` case passed separately in 2.53 seconds:
strict 1/1, zero other outcomes and native exit 0 on the unchanged candidate and signed `4563e8a7` module.
Before fixture admission, normal exact-ID sync activated the existing QA default network. Its configured
identity, non-network inventories, boot, source/candidate pins and mirror were preserved. Full POST matched
the prepared active baseline; the default intentionally remained active, so the original inactive baseline
was not restored.

Full current-module collection found 31 nodes in 0.13 seconds; the unique union of the five batch selections
matches those 31 nodes exactly. All 31 currently registered network nodes therefore have current-candidate
passes across five separate native batches, with their recorded test-module bindings; this is not one
consolidated full-module invocation. Historical failures remain retained. Reusable-base provisioning,
connectivity/policy qualification and the full installed Python matrix remain pending; no independent
whole-root absence proof is claimed.

**Current bounded memory qualification (2026-10-09):** Installed candidate
`0.3.0-rc.partition-f909601d`, source `f909601d`, SHA-256
`13425005db12c4c13f34a5ca140ec2979dd8471d2c3e1bd271946d28a4364b35`, passed both registered
`test_guest_sustains_near_saturation_under_pressure_only_memory_control` variants inside `mvmctl-030-test`:
host-swap-absent (36.74 seconds) and host-swap-present (27.02 seconds). The registered guest-global exhaustion
case also passed (8.996 seconds), identifying the allocator as the guest OOM victim and proving fresh exec
survival. All three strict reports record one pass, zero other outcomes and native exit 0; checked typed cleanup,
configuration and inventory preservation passed. This closes those three bounded cases only. Historical
incident attribution, survival under arbitrary host/ancestor OOM and the full release matrix remain unqualified.

**Earlier installed-candidate checkpoint (2026-10-08):** The signed image-selector correction `020cd232`
passed its intended pre-fix regression, six corrected behavioral cases, the input-package suite and focused
format/vet checks inside `mvmctl-030-test` (`19cb3d83`). Clean tree `7628b6f5` then passed the production
real-agent Go 1.26.3/PIE build (`270d8fdc`). Candidate `0.3.0-rc.image-selector-020cd232`, SHA-256
`cd2b1cdaf5bb84b6858fa4953f09bc8dc3fc1380e74db4d0258d76172b595d47`, was installed through
`host install-system` only inside the named runner (`5ff322ca`), with exact independent preservation checks.
The normal orchestrator's mirror-volume preparation also passed (`9e0a7d2f`); reusable-base provisioning did not
run. Neither result qualifies the memory-pressure cases or the remaining installed T1/T2 matrix. The earlier
ordinary CI and full-root results below carry only unchanged surfaces; they are not relabeled as complete
CI/root executions on `020cd232`. The outer installed controller and other VMs remain unchanged.

**Explicit-kernel memory QA preparation (2026-10-08):** `vm/test_cgroup.py` accepts the existing paired
runner kernel ID/digest and passes the verified kernel explicitly at its three VM-create sites; without the
pair, the default-kernel requirement is unchanged. Test workloads, runtime assertions and cleanup are unchanged.
The selected host-swap-absent saturation case collected successfully inside the named runner; the existing
imported-kernel runner-binding unit passed 1/1 with strict JSON/JUnit outcomes (`df714230`). Collection-only
items have no runtime call phase and are classified as `errors` by the outcome hook; this is not a collection
failure or runtime pass. Two failed private-launcher receipts remain failures. At that checkpoint the installed
pressure case had not run; the preparation itself did not close memory qualification.

**Earlier coherent Go/build/install checkpoint (2026-10-08):** Clean parent `992d2fd5` passed the nine-phase
ordinary Go 1.26.3 CI sequence inside `mvmctl-030-test`, with 59 native package passes and ten no-test rows
(receipt SHA-256 `318efd21`). This is carried evidence for unchanged surfaces, not a complete CI execution on
`64079c3b`. The separate full root Jailer suite on clean `64079c3b` passed 828 top-level tests and 1,799 child
tests, with zero failures/skips, native exit 0 and exact preservation (`39baf49b`). Clean tree `cc100b39`
then produced the real-agent Go 1.26.3/PIE build with exact revision and `vcs.modified=false` (`f77b5e34`).
Candidate `0.3.0-rc.integrated-64079c3b`, SHA-256
`9aa3a92f049f282cc9bbd3b3c631939c1a7828c78ef60ff7f6630c70784ab948`, was installed through normal
`host install-system` only inside the named runner. Scoped readiness adjudication (`61cb1e01`) confirmed exact
durable database content/identity/permissions, authority and mounts; only cache-directory mtime/ctime and database
ctime differed. Native version/status/binary-list checks passed, but the raw timestamp-assertion installation
failure is retained. This adjudication is a scoped QA expectation correction, not a general timestamp exclusion.
Earlier CLI/runtime passes retain their original source/candidate bindings. At that checkpoint, memory,
reusable-base provisioning and the full installed T1/T2 matrix remained unqualified. Remote GitHub CI, Tier 3,
aarch64 runtime and final release approval are also unqualified. Build, Go and readiness results do not close
those gates; the outer controller remains unchanged.

**Current cached-image prerequisite (2026-10-08):** The same installed `64079c3b` candidate completed a
mirrored `ubuntu-minimal:noble` pull and subsequent inspection inside `mvmctl-030-test`; both commands exited 0.
The published image is 143,959,773 bytes compressed and 502,267,904 bytes uncompressed. Before/after VM
inventories were empty, candidate and boot identities were preserved, and the bounded supervisor completed
without a guard trip (`df353ca3`). Only this runner's enclosing legacy cgroup received an approved temporary
8 GiB hard/pressure envelope and a 7.5 GiB QA stop threshold; guest RAM remained 6 GiB. Stored current-memory
samples peaked at 6,089,854,976 bytes, with zero swap and hard-limit/OOM events. This resolves the earlier
cached-image watchdog stop, not a diagnosed production OOM defect, a minimum-memory requirement, reusable-base
provisioning, or the installed system-test matrix. Production pressure-only memory policy is unchanged.

**Earlier journal-fixed Go CI checkpoint (2026-10-08):** Two standalone clean role checkouts bind exact source
`1a2506e6`/tree `6e0c387f`, with 1,879 source hashes each; their Go/build inputs match release HEAD `3f99e483`.
The ordinary pinned Go 1.26.3 sequence passed tidy/clean, architecture, gofmt, golines, generation/clean, vet,
and the complete CI selector. Its literal output has 69 package rows: 59 `ok` and ten packages without tests;
the selected unprivileged Jailer subset passed in 5.919 seconds. Both atomic coverage profiles are retained.
Generation used the existing CI compile-only embedded inputs; the installed real-agent candidate was unchanged.
There was no guard trip. The separate full root Jailer launch failed original admission before SSH or Go:
current 5,097,504,768 + 2,147,483,648 allowance + 536,870,912 reserve = 7,781,859,328 bytes, exceeding the
unchanged 6,576,668,672-byte ceiling. Zero root tests executed; no root pass/fail/skip totals can be claimed.
Independent POST and receipt collection passed, preserving exact candidate/artifact metadata, sources/tools,
controller/backing and boot/VMM identities, persistent baseline and controls; VM/jobs inventories were empty and
OOM events remained zero. No restart, reclaim, weaker guard, installation or export body was used. This closes
the current-source ordinary local Go CI gate only, not the combined full-root gate, Python matrix or release approval.

**VM-to-image export coverage preparation (2026-10-08):** The corrected image module adds
`TestImageImportCreateVM::test_vm_import_receiver_death_retires_orphan_and_recovers_source` for authenticated
client/receiver death, exact orphan removal and retry, pending-control refusal, and recovered source/clone bytes.
It also adds `TestImageImportCreateVM::test_vm_import_sigint_survivor_refuses_release_then_restores_and_discards`:
interrupt the retained CLI, prove the authenticated root receiver survives with unchanged copy authority, require
typed cleanup to refuse while that receiver owns the copy, then verify restoration and temporary-output discard.
These two nodes and the three existing running/paused/stopped protected-disk import nodes collected successfully inside
`mvmctl-030-test`; the selected strict-outcome runner unit passed. Module SHA-256 is
`a7c406e66c19f61a21c9e103708dd880df28e0a87587c5908f48338ac9800947`.
All five remain **partial/unqualified**, despite the verified real-agent build and installation of
`0.3.0-rc.vmexport-497455a6`. The SIGINT case targets a surviving receiver's finish/restore/discard behavior,
not cooperative cancellation of its root context or immediate restoration when the CLI returns. Real copy-I/O
failure remains a separate coverage gap. Independent checks preserved the runner baseline.

The first Running-state execution failed before import at an incorrectly nested shell command in its marker
setup; the three marker setups were corrected to use the CLI's command-string contract. The corrected module
(`c42345377509dc24834b12edb4c63ea70e6f52ace4186d49f300eba22c837b8a`) collected the Running case, but the
unchanged outer-runner 2 GiB memory-growth guard interrupted it after 12.92 seconds: first observed growth was
2,219,978,752 bytes. No test completed, and the retained quiet output does not establish whether marker writing
or import was reached. This is **not an export failure or pass**. All six cleanup checks and the independent
post-check passed; VM/job inventories were empty, bindings and cgroup controls/events were unchanged. No limits
were raised and no unchanged retry was performed.

Phase-observed module `7077b51209e46da0c4304d6b923f513555b83d858f19f1700f47ea722a150e2d` subsequently
proved source boot, marker write and source-state checks before reaching import. A deliberately reviewed QA-only
hard-cap-minus-reserve guard replaced the earlier relative growth guard; the outer hard cap, reserve and remaining
protections stayed unchanged. This attempt failed once in 11.97 seconds at mandatory read-only OS inspection,
with no resource trip. The matching kernel log confirms ext4 journal recovery required writes that the read-only
loop device refused. The import and clone assertions did not pass. All six cleanup checks and independent POST
passed with empty VM/job inventories and unchanged baseline/controls.

Signed `1a2506e6` implements unprivileged journal-only replay on a new disposable ext4 VM-import inspection copy,
using a retained writable descriptor before the unchanged root read-only inspection. Six new Go behavioral tests
each passed twice inside `mvmctl-030-test`, covering source/copy isolation, partial failure and cancellation cleanup,
replacement refusal, named inspection cleanup, changed mode/size/link refusal, and unchanged ordinary/non-ext4
imports. The selected cached-import API test, focused image/API vet, and independent preservation checks passed.
Nine existing image regressions passed in an earlier run; these separate results are not a fresh consolidated
15-test package pass. The fixtures use bounded child tests with parent-owned private caches, because cache resolution
is process-wide; retained refusal-test files stay inside that owned temporary namespace. These Go tests use a fake
journal subprocess and alone do not prove actual `e2fsck` descriptor access, root mounting, installed VM import, or
clone bytes. Skipping journal replay is not a workaround.

The corrected real-agent `0.3.0-rc.vmexport-journal-1a2506e6` candidate (SHA-256
`d13c2a25fefe65a592e773de099a23e5fcbd2ce3e35cee77948fef4aa967f706`) built and installed only inside the
named runner, with independent preservation checks. The unchanged image module's differentiated Running body
completed marker setup, import and source Running-state restoration, then entered clone boot. The safety guard
interrupted before clone readiness/readback: outer charge 6,083,108,864 bytes exceeded the 6,039,797,760-byte
hard-cap-minus-reserve threshold. This was not an OOM: swap and all memory events remained zero. Strict outcome
was collected 1, passed 0, errors 1, pytest exit 2 after KeyboardInterrupt; it is not a complete test pass. All six
owned cleanup checks and independent POST passed, with baseline, empty VM/job inventories, candidate/source,
outer controller, boot and VMM preserved. Actual installed import and Running restoration therefore have partial
runtime evidence; clone content, Paused/Stopped and interruption/recovery journeys remain unqualified. The run
did not emit literal journal-tool arguments, so it is not separate syscall-level proof of the descriptor path.

One normal post-install stop/start preserved the same named runner/candidate/backing/configuration and reduced
outer charge to 499,208,192 bytes without reclaim commands, cap changes or a rebuild. Fresh admission passed;
the differentiated cold Running body entered import but stopped at the original fixed 2 GiB host-loss guard:
available host memory fell by 2,296,807,424 bytes while 6,847,385,600 remained available, and outer charge
5,063,606,272 stayed below the cap-reserve threshold. Strict outcome was collected 1, passed 0, errors 1, exit 2;
all six cleanup checks and independent POST passed with unchanged events/swap and preserved state. This is not
import completion or a reproduced product failure, and it does not erase the earlier partial positive evidence.

The new `test_vm_import_mid_copy_io_failure_restores_source_and_discards_partial` case reuses the authenticated
stopped-receiver/pidfd bindings, pins its anonymous partial-output inode and applies only that receiver's soft
file-size limit. It requires real growth to the imposed limit below the source extent, normal receiver settlement,
Running-state/process/marker preservation, released-copy and absent-source-journal evidence, no public image,
and twice-idempotent typed exact-ID cleanup. It asserts the actual top-level public import error and image ID,
not an unavailable nested errno transcript. The reviewed module SHA-256
`39fa4787d47343a6ddb9c5b85a1f44c2ebb0e24d61b8ae6761f690b3a4f08538` passed static syntax/Ruff checks;
all six export nodes collected in 0.20 seconds inside the named runner, with zero collection errors and exit 0.
Independent POST passed all 13 preservation comparisons. No fault body ran: collection's six unexecuted items
are not runtime passes, and actual file-limit behavior/restoration/cleanup qualification remains open.

**Enabled GuestFS focused qualification (2026-10-02):** Installed candidate `0.3.0-rc.562b38de` passed
`cache/test_guestfs_appliance.py::test_enabled_build_reuse_and_incomplete_rebuild` inside `mvmctl-030-test`
in 68.18 seconds. The strict report contains one pass and zero failures, errors, skips, deselections, XFAIL or
XPASS. Cold construction, real guestfish launches, warm reuse, incomplete-appliance repair and selected-cache
pruning/preservation passed. Typed teardown and independent absence checks confirmed cleanup. The corrected
probe timeout and administrator-prepared kernel readability supersede the earlier cold-build failure; neither
alone is claimed as its sole cause. This closes the registered local GuestFS case, not fresh reusable-base
provisioning or the remaining current-candidate matrix.

**Complete installed-candidate T1/T2 matrix before the added console decoy case (2026-09-29):** The exact registered selection passed
all 415 cases across 26 files against installed candidate `8a6fb1bf` (`0.3.0-rc.priorboot`) inside only
`mvmctl-030-test`. Configuration was byte-for-byte unchanged after both tiers; final VM/network/volume/snapshot
inventories were empty and no runner swap remained. Every file passed strict outcome validation with zero other
outcomes. Reports are retained in `matrix-b2c0a1f8`; the corrected binary file uses `bin-observer-84eb` instead of
the preserved earlier failed report. Test source is the clean `b2c0a1f8` snapshot plus the binary correction integrated
as `c43cf9ec`. This identifies the test-source selection, not an independently established VCS revision for
the historical installed binary. The outer controller remained unchanged. This closes that recorded T1/T2 selection;
later changed product paths still require scoped current-candidate qualification. It does not qualify
clean-host/Tier 3, remote CI, enabled GuestFS, adversarial fault/recovery, kernel producer/base provenance,
original incident attribution, aarch64 runtime or release approval.

**Caller PID-mirror console gate (2026-09-29):** Console source `ed288ee6`, integrated as `84ba3730`,
adds one case against the same installed candidate. The focused case passed in 10.13 seconds and the complete
three-case console file passed in 33.19 seconds, with strict zero-other outcomes, byte-identical configuration
and empty VM/network/volume/snapshot inventories afterward. The case changes only the admitted caller-owned
`/run/mvmctl/runtime/<uid>/<vm-id>/console.pid` contents to a directly spawned decoy's PID: discovery still reports
the recorded relay, typed stop exits that relay, the independently pinned decoy and Firecracker survive, and guest
exec succeeds. The original mirror contents are restored through the retained descriptor before normal fixture
cleanup. Reports are `console-decoy-054d/{focus,full}.{json,xml}`. The earlier 415-case matrix was not rerun;
this additive file gate does not qualify hostile console socket replacement or other pending fault scenarios.

The binary file initially exposed a QA observer leak after the first seven files passed all 113 cases:
repeated scans retained historical-journal descriptors and exhausted the unchanged 1,024 soft descriptor limit.
This is not evidence of a production descriptor leak. The corrected file SHA-256
`84eb64b50257d5d3f7c175dfe02310ebd60dcec1b793b9a300820ba736630fb4` passed the original failing case
(8.36 seconds) and all eleven binary cases (23.15 seconds), with strict zero-other outcomes and four empty
inventories afterward. The remaining eighteen files subsequently passed on the same installed candidate.

The scoped read-only scanner gate observed 320 real cleaned schema-6 journals over ten scans with a constant
eight descriptors and unchanged limits, and rejected an in-memory digest-baseline mismatch. Two simulated cleanup
failures exercised the actual lifecycle method: both original defaults were attempted independently, uncertain
release removal was refused, observer close ran last, and primary plus cleanup errors were retained. Normal typed
removal recovered the earlier test-created release only after exact equality with its saved creation receipt;
original root releases and default identities were preserved. These helper gates do not qualify production fault
recovery or erase the earlier failed matrix report. Reports are `bin-observer-84eb/bin-{focus1,full11}.{json,xml}`.

**Historical coherent local CI gate (2026-09-28):** An isolated clean source snapshot of `b2c0a1f8` passed offline
tidy with no tracked changes, whole-tree gofmt/golines, guest-agent generation, full vet, the complete unprivileged
Go CI selection and its coverage checks under pinned Go 1.26.3. The separate full root Jailer package passed
(7.743 seconds), followed by all 240 Python orchestrator unit cases (7.41 seconds), inside only `mvmctl-030-test`.
The unit XML has zero failures, errors or skips. This closes the feasible local source gate at that revision,
not the complete installed-candidate T1/T2 matrix, remote GitHub CI or release approval.

**Earlier local CI/build/install checkpoint (2026-10-03):** Clean source `a3d7b8d0` passed the complete
pinned Go 1.26.3 sequence inside `mvmctl-030-test`, including the unprivileged selector and both coverage
profiles. Selected Jailer tests passed in 5.954 seconds; complete root-Jailer evidence carries only for verified
unchanged relevant dependency, module and build inputs. The real-agent PIE build and normal system installation
passed. Installed `0.3.0-rc.a3d7b8d0`, SHA-256
`98f44bd2c6a803a78d96d153c10df7bc6e9a11915c65881ae1c15da1e2866bba`, has clean VCS provenance and a
root-owned `0755` canonical identity. Later `ff74b015` production Go/build inputs match; its administrator test
module changed. This does not qualify reusable-base provisioning, the remaining installed Python matrix,
remote GitHub CI or release approval. Previous failed reports remain failed.

**Current Go/reset-candidate checkpoint (2026-10-03):** Clean `e034f4e0` produced the real-agent
`0.3.0-rc.resetparent-e034f4e0` candidate with Go 1.26.3, PIE and unmodified VCS provenance; normal installation
changed only the named QA runner. Its root-owned `0755` canonical content SHA-256 is
`1678ae447fe8bc536e556db9258e375daaa8e14f9f73e9f6ed1747e8e047c689`.
That clean source passed the complete pinned local Go CI sequence: tidy/clean, gofmt/golines,
generation/clean, vet and the complete unprivileged selector. Selected Jailer tests passed in 3.243 seconds;
both atomic coverage profiles and the gate log were preserved. The separate complete root-Jailer package
passed in 3.545 seconds against hash-checked equivalent Go/module/build inputs. The original root run failed
because the harness's inherited umask changed exact fixture modes and a race-test setup failure timed out;
only the Go child's umask was corrected to `022`, without relaxing production admission or private task modes.
Candidate, persistent baseline and resource controls were preserved. Subsequent Python-test/Markdown-only
changes do not alter those Go/CI inputs. These Go results alone do not qualify administrator runtime; the later
first-journey result is recorded below. Remaining installed-candidate Python matrix, reusable-base provisioning,
GitHub CI and release approval remain separate gates.

**Current exec interruption checkpoint (2026-10-07):** installed `0.3.0-rc.resetparent-e034f4e0`
passed `exec/test_vm_to_vm_exec.py::test_vm_to_vm_exec_interrupt_reaps_destination_group` in
21.77 seconds: strict collected/passed 1/1, all other counts zero, JUnit 1/0/0/0. The case pins the
retained CLI's pidfd, records exact destination leader/shell/child identities, sends SIGINT, verifies those
tasks are no longer live and proves a fresh authorized relay succeeds. All six independent preservation
checks passed; baseline, candidate, boot, mirror and source stayed exact. The QA worker was reaped and
its pinned empty task group removed; resource guards did not trip and enclosing controls/OOM counters
were unchanged. The retained observer explicitly leaves complete private-backend cleanup unobserved.
This closes this interruption boundary, not other authority/deadline/cancellation faults, host reboot,
snapshot-restore grants or the complete exec domain.

**Current image-result qualification checkpoint (2026-10-03):** Six existing force-pull and default-result
cases were selected on installed `a3d7b8d0`. The first pull failed before its image/default assertions because
the QA adapter placed its private cache below a group-writable checkout ancestor; managed-cache admission
correctly refused it. The remaining five cases did not execute. Independent baseline, backing, candidate,
mirror, root-authority and source preservation checks passed. A task-only private-workspace correction was
reviewed without changing product code or admission checks; its invocation was refused by initial resource
admission before SSH or test effects. Neither attempt qualified its selected cases; no refusal is an image pass.

A separate cached-default invocation passed its initial admission, but stopped before pytest when parent
filesystem free space fell by 146,636,800 bytes, exceeding its 64 MiB job allowance. No memory guard tripped;
the responsible writer is not established by the receipts. All six independent preservation checks passed,
including exact post-kernel inventory equality. That attempt did not run the cached-default body.

The subsequent corrected cached-default invocation passed
`images/test_images.py::TestImagePullAdvanced::test_pull_cached_image_with_default_sets_default`
on installed `0.3.0-rc.a3d7b8d0`, using module SHA-256
`3c6979dd330d9c7d02b636e078decd628b96d361fce7b47b965c04fd060349c4`.
Strict JSON and JUnit agree on one pass, zero other outcomes, in 6.25 seconds. The QA supervisor measures
the pinned named-runner disk's allocated-byte growth separately from shared-filesystem free-space decline;
its original own-growth limit, existing shared-space reserve and resource floors remain enforced.
All six independent preservation checks passed, with unchanged image backing bytes, root authority,
kernel, candidate and mirror. Only the expected Alpine default-selection timestamp advanced.
No resource guard tripped. This qualifies the cached-default case only.

The subsequent import/default invocation passed
`images/test_images.py::TestImageImportSetDefault::test_image_import_with_set_default`
on the same installed candidate and image-module SHA above. Strict JSON and JUnit agree on one pass,
zero other outcomes, in 0.264 seconds. The case imports a raw ext4 image with `--default` and
`--skip-optimization`, checks that its returned ID is the sole default, removes the owned image and
restores the original default. All six independent preservation checks passed; image backing bytes,
kernel, candidate, mirror and root authority were unchanged. Only the restored Alpine default-selection
timestamp advanced. No resource guard tripped. This does not prove imported-image boot or every import
format/failure path. Earlier failed/refused reports remain preserved.

The subsequent `TestImagePullAdvanced::test_pull_cached_image_with_force_redownloads[force]` case passed
on the same installed candidate and image module. Strict JSON and JUnit agree on one pass, zero other
outcomes, in 24.85 seconds. It checks a mirrored Alpine replacement's distinct ID, retained sole-default
selection, new backing and retired old backing. A QA-only task scope used a 512 MiB soft pressure boundary
with no hard memory ceiling; pressure and direct reclaim counters advanced. The original outer memory,
owned-disk growth and resource-floor guards stayed clear, and all six shared-preservation checks passed.
The owned child was reaped and its empty task group removed. This does not establish a hard outer-memory
bound, every helper's placement, or complete private-workspace cleanup. At that checkpoint the `no-cache`,
`skip-optimization` and `disable-detector` variants remained unqualified; the two earlier interrupted force
attempts are not passes.

**Later force-pull option qualification (2026-10-09):** those three remaining variants subsequently passed
together on installed `bfdece17`/`d13f8c06`, with clean Python source `0b3762d9`: exact collection 3, strict
runtime 3/3, zero other outcomes and native exit 0 in 87.76 seconds. JUnit identifies exactly
`TestImagePullAdvanced::test_pull_cached_image_with_force_redownloads[no-cache]`, `[skip-optimization]`,
and `[disable-detector]`. Each private-cache journey proves a distinct mirrored Alpine replacement ID,
sole-default transfer, new backing and retired old backing. Existing typed cleanup emptied each private image
inventory; identity-bound task-temp cleanup and complete literal original public/physical/administrator
baseline preservation passed. Kernel/mirror/boot/source/candidate pins remained unchanged; no global
Alpine/default setup or VM was needed. Terminal SHA-256
`456c803a4b98cdae762655e44677526b34ed5e25b0cb23d9d2d4ac844f2d2a64`; JSON
`ddcb5c2207887984b83208520c14367078a8968718dd93f34541cf37d0166027`; JUnit
`74fd74e5df60e4e08f55f66cedddbb48be3129c75b1d916ae7748ff636baafdd`.
The earlier `[force]` evidence retains its original binding; no complete image-domain claim follows.

**Current VM-free kernel-import gate (2026-10-03):** All six existing VM-free cases in
`kernel/test_kernel_import.py` passed on installed `a3d7b8d0` inside `mvmctl-030-test`, with test source SHA-256
`1c246cc0fe8145e70556f6c9995b19d5f024e8d49977741a04ed72298336874e`.
Strict JSON and JUnit agree on six passes in 6.17 seconds, zero other outcomes. Coverage includes raw import,
filename-derived version, default selection, missing-path/empty-name refusal and same-byte content-ID reuse.
Typed fixture cleanup and seven independent preservation checks passed. The prepared kernel's caller backing
and root-owned record/backing identities, bytes and generation were unchanged; only its public default-selection
timestamp advanced. The test-owned kernel finished as a removed generation-4 tombstone without disk/pending
backing. Resource guards did not trip. The two VM-dependent cases, changed-byte refusal and broader retirement
failure paths are not qualified by this six-case pass; the complete root-Jailer evidence remains separate.

**Owned host-reboot recovery gate (2026-09-28):** The corrected installed candidate `8a6fb1bf`
(`0.3.0-rc.priorboot`) passed one fresh, local two-phase Python journey: running projections and guest exec,
one controlled runner reboot, stopped ls/inspect and empty ps, typed stop, exactly one restart, new host/guest
boot identities and successful fresh exec. The driver binds exact journal/process/jail/mount/device identities,
retains a consumed resume claim and checks configuration plus complete inventory restoration after exact owned
cleanup. Its 14 bounded unit cases passed in 0.25 seconds. Prior-boot console and schema-6 absence regressions
failed on old production source and passed with the targeted fixes. An earlier failed receipt remains failed;
no root journals were manually deleted or adopted. This local experiment does not attribute the older intermittent
KVM-open denial, integrate a generic reboot stage into the orchestrator, or qualify every recovery fault.

**Guest-global exhaustion gate (2026-09-28):** Source `93f2be2c` added one real guest-global RAM-exhaustion case
to `vm/test_cgroup.py`. It passed once in 10.28 seconds against recorded candidate `886cb7e5`: the guest OOM
killer terminated the independently identified anonymous allocator, Firecracker retained its identity, and a
fresh exec and allocation succeeded. Observed runner/ancestor OOM counters, configuration and owned inventories
were unchanged. This is separate from the three earlier measured-pressure cases and does not attribute the
original incident or guarantee safety under arbitrary ancestor/global host pressure.

**Current-candidate guest-global exhaustion gate (2026-10-03):** The same registered guest-OOM case passed
on installed `0.3.0-rc.a3d7b8d0` inside `mvmctl-030-test`, with test source SHA-256
`2765445a547affc4e929e2f20f62337d528efae41a976fde0af6e40f675a99ee` integrated as `ce9ea175`.
Strict JSON and JUnit agree: one pass in 11.03 seconds, zero other outcomes. In the 224 MiB guest,
the kernel identified the allocator as the global-OOM victim (exit 137); a fresh exec completed a 1 MiB
allocation with the same guest boot and Firecracker identity. The VM's pressure-only policy and observed
leaf, visible ancestor and enclosing runner OOM counters were unchanged. Checked typed fixture removal
and independent configuration, inventories, candidate, boot, mirror, root-pair and source preservation passed.
The first post-create exec now uses an explicit 30-second connection budget under ADR-0013; allocator,
survival and cleanup assertions are unchanged. This closes this current-candidate case, not host saturation,
with-swap variants, historical incident attribution or the complete release matrix.

**Complete Jailer repeat gate (2026-09-28):** Source SHA-256
`c539b5cfb61623f7e819179e9411d2b101a51b7459f79a639836177d70ace839` passed all fifteen expanded cases
twice (196.32/201.71 seconds) against installed candidate `886cb7e5` inside only `mvmctl-030-test`.
Both strict reports have zero other outcomes, unchanged full configuration and four empty inventories after
checked cleanup. All fourteen methods remain, with owned creation/partial-publication receipts and retained
asset/default baselines. Coverage includes isolated credentials and API denial, real exec/copy/console,
networkless restart/reboot, default deblob, unqualified-release refusal, raw-volume attachment, competing-writer
capture refusal and missing-release restore cleanup. Both normal-removal and source-crash snapshot variants
verify captured RAM/boot identity, rootfs bytes and independent data-volume content after source mutation/removal.
The reused crash helper at SHA-256 `538cf4cf81790b16db7ab7143d004e8677ac35effc6758c444f381194a986693`
requires a closed, explicit expected placement; managed remains the default. Root journal, process/executable,
credentials, generation and pidfd checks remain strict. Its managed crash journey also passed in the five-case
delta gate (92.61 seconds). That does not rerun or qualify the whole lifecycle file at this newer helper hash.
Reports are `qa-jailer-session-c539-selected5.{json,xml}` and
`qa-jailer-session-c539-full15-pass{1,2}.{json,xml}`. Capture interruption/power loss, host-reboot attribution,
hostile endpoints, larger-memory exhaustion and the complete current-source matrix remain separate gates.

**Complete init and installer repeat gates (2026-09-28):** Init source SHA-256
`4709839298066c505bd5864bc94905896761c48f620335d152f1e9a66b0b2028` passed all eight cases twice
(6.95/7.50 seconds); installer source `c4f98f26f7eba57e9b7545303d5dc0ec9607ec0601e3b99b32478856e8056117`
passed all ten cases twice (7.57/7.66 seconds). Both used installed/staged candidate `886cb7e5`, built from
clean `3ae3a89c` as `0.3.0-rc.init-session`, inside only `mvmctl-030-test`. Each strict report has zero other
outcomes, unchanged full configuration and four empty inventories after checked cleanup. The fixtures retain
the fuller resource/default/policy baseline and exact file/mount receipts, including effects observed after
subprocess timeouts. Coverage includes actual interactive decline, explicit administrator success, canonical
installation, update refusal, read-only destination and symlink rejection/restoration. The independent
runner administrator bootstrap is not a marker-only grant. Clean enrollment/reset and marker-only denial
remain unqualified. Reports are `qa-init-session-4709-full8-pass{1,2}.{json,xml}` and
`qa-install-session-c4f9-full10-pass{1,2}.{json,xml}`.

**Initialization source corrections (2026-09-28):** `75ddcb3d` stops failed administrator setup and uses
the closed canonical route. `3ae3a89c` requires confirmed inactive-session evidence before silently skipping
the prompt. The new behavior regressions and complete CLI/common packages passed with vet/format checks.
The clean `3ae3a89c` checkout then passed the complete pinned Go 1.26.3 local CI sequence, full root Jailer
package (7.147 seconds), 240 Python orchestrator unit cases (7.63 seconds, zero other outcomes), and release
build. Evidence is `go-init-session-focused.log` and `full-ci-build-init-3ae3a89c.log`. These results do not
qualify the complete current-source Python matrix, remote GitHub CI or the release.

**Clean administrator journey source checkpoint (2026-10-02):** The registered Tier 2 `host_admin` domain
adds `TestCleanHostAdministration::test_clean_enrollment_marker_only_reset_and_reinit` in
`host/test_host_admin.py`. It creates a clean root-user child and then a separate non-root operator, verifies
first enrollment and usable typed dispatch, denies public administrator/raw-sudo commands, and checks quiescent
reset, disabled admission and re-init. Cleanup uncertainty is reported if child creation never yields an admitted
identity. Signed integration `df69c47e` adds separate password-authenticated second-operator enrollment with
typed dispatch, active-network reset refusal, and real read-only-filesystem init/reset failure-and-retry cases.
All three exact nodes collected in the named runner in 0.36 seconds, without fixtures. This is authored coverage,
not a runtime pass: the first journey failed during child creation, before enrollment. Abrupt interruption and
ambiguous-runtime reset refusal remain gaps. The shared runner is never the reset target.
Signed integration `0dcbb418` passed strict one-case collection in the named runner (0.28 seconds); two existing
registry checks passed separately (0.31 seconds), and lint/format checks passed. No fixture ran during collection.

**Administrator runtime checkpoint (2026-10-03):** After the four generated network-prefix corrections in
`ff74b015`, the single enrollment/marker-only/reset journey ran on installed `a3d7b8d0`. Network lifecycle and
marker-only denial controls succeeded, but quiescent `host reset --operator hostqa --force --json` returned
the generic `host.reset.failed` restoration message, with the journal retained. Strict outcomes
were collected 1, failed 1, passed 0; JUnit agreed, with no errors, skips or resource guard trips. Typed fixture
cleanup had no reported errors; independent child jail/runtime/netns and parent bridge/rule absence plus all
twelve preservation checks passed. The underlying reset cause remains unknown; successful reset/re-init and
the complete administrator domain are not qualified. Earlier failures remain retained.

The subsequent diagnostic journey on the same installed candidate reached the same quiescent-reset refusal,
with strict JSON and JUnit agreeing on one failure and zero other outcomes. No resource guard tripped.
Its immediate post-error snapshot retained enabled admission and an initialized journal without a reset phase
or step. The generic error message therefore does not establish that restoration began; the failing call and
underlying cause remain unknown. Typed cleanup and all twelve preservation checks passed. This narrows the
diagnosis without qualifying reset/re-init or justifying a restoration fix or weakened guard.

**Reset diagnostic source correction (2026-10-03):** Signed `2e4a05f6` exposes the named failure stage and
a bounded printable cause in the reset error's message and details, preserving the original failure through
deferred cleanup without changing reset guards or effects. Both new error-composition Go regressions, the complete
host package, vet and formatting passed as the unprivileged user inside the named runner. An optional focused
race check was interrupted by the QA memory-growth guard before a test result; it is not a pass.
Signed `748dc599` strengthens the existing active-network refusal and real read-only-filesystem reset/retry
cases while retaining their journal, admission and cleanup checks. Both exact nodes collected in 0.20 seconds
with no fixtures or test bodies executed. The collection-only outcome hook reports two unrun-item errors with
exit status zero; this is not a strict runtime pass. The original node-prefix receipt-check failure was corrected
against retained reports without rerunning pytest. Clean `33fc5c1a` subsequently passed a real-agent Go 1.26.3
amd64/PIE build with exact VCS identity and `vcs.modified=false`, then normal installation as
`0.3.0-rc.resetdiag-33fc5c1a` only inside the named QA runner. Installed content matched the artifact and
root-owned `0755` canonical identity; configuration, inventories, authority and mirrors were preserved.
The first registered administrator journey on that candidate reproduced the quiescent-reset failure and exposed
the exact Jailer guard stage and directory-mode cause. Strict JSON/JUnit recorded one failure, with no resource
trips; all twelve preservation checks, owned process/cgroup cleanup and child-resource absence passed.

**Private reset-parent correction:** Signed `84a78139` aligns reset admission with administrator setup and the
existing VM lifecycle: the shared parent stays exact `0755`, while `jailer` and `firecracker` must be exact
`0700`. It performs no permission repair or resource deletion. A root filesystem regression failed on the old
code at the intended private-tree admission check. All four corrected cases then passed, covering legitimate
private parents, rejection of public-mode parents and residual VM resources, with unchanged metadata/content.
Jailer vet, gofmt and golines also passed inside the named runner. A verifier-prefix mismatch after RED was
resolved against the retained output; RED was not rerun. These are focused root checks, not a public reset/re-init
pass. That installed reset-diagnostic candidate predates this correction; the later current Go/reset-candidate
checkpoint above supersedes the pending build/install, complete CI and full root-Jailer status.

**Administrator pending-intent correction:** The later journey on installed `e034f4e0` passed quiescent reset,
the complete reset-journal/disabled-admission assertions and restoration checks. It then failed a stale assertion
expecting denied network creation to leave no caller-cache row. Root state remained unchanged; the inactive
pending intent is deliberate and allows exact-input retry. Strict JSON/JUnit recorded one failed case, not a
complete reset/re-init pass; all twelve preservation checks and owned-resource cleanup passed.
Signed `f5f478f2` now checks inactive intent retention and, after re-enrollment, same-ID activation and typed
removal. Exact one-node collection passed; its receipt parser correction reused retained output rather than
recollecting, and two existing runner-unit cases passed. Neither collection nor those units ran this journey.
A normal named-runner restart preserved the candidate/baseline; the first corrected invocation was refused
before launch by its unchanged parent-disk prerequisite. After storage became available, the same corrected
first journey passed on 2026-10-07 against installed `0.3.0-rc.resetparent-e034f4e0`: strict collected/passed 1/1,
all other outcomes zero, JUnit 1/0/0/0, in 332.94 seconds. This proves clean-child enrollment, marker-only
success/denials, quiescent reset and restoration, disabled admission, re-enrollment and same-ID pending-network
activation/removal. All twelve preservation checks passed; owned child jail/runtime/netns and parent network
effects were absent, the exact pytest worker was reaped and its empty QA task group removed. Resource guards
did not trip and enclosing controls/OOM counters were unchanged. The shared runner was not reset or replaced.
The separate multioperator/refusal result below does not qualify the filesystem-retry body; abrupt interruption
and ambiguous-runtime recovery are not covered by either journey.

**Administrator second-operator qualification (2026-10-07):** The registered
`TestCleanHostAdministration::test_second_operator_authenticated_enrollment_and_active_reset_refusal`
passed against installed `0.3.0-rc.resetparent-e034f4e0`: strict collected/passed 1/1, all other outcomes zero,
JUnit 1/0/0/0, in 95.67 seconds. Signed `c225e504` corrects only the admission inode observation after the
expected atomic rewrite and the separate first-init partial-result contract. This runtime proves
password-authenticated second-operator enrollment and typed dispatch, repeated-init content/security-state
preservation, active-network reset refusal and subsequent quiescent reset. All twelve baseline-preservation
checks and five owned-resource absence checks passed; the temporary enrollment policy was removed, the exact
worker was reaped and the empty QA task group removed. Independent POST found no VM/test jobs and preserved
candidate, source, controller, backing identity, persistent baseline and unchanged resource controls/OOM counters.
The earlier stale-inode failure remains failed evidence; its active-network assertions had not been reached.

**Earlier administrator filesystem-retry limitation (superseded by the current pass above):** The corrected
`test_filesystem_failure_preserves_init_and_reset_retry_authority` invocation recorded strict/JUnit one failure
in 337.21 seconds. Child `apt-get update/install` exceeded its 330-second setup timeout before either read-only
fault or retry assertion ran; the underlying package timeout cause remains unproven. Fixture states/faults were
empty. All twelve preservation and five owned-resource absence checks, worker/group cleanup and independent
POST passed. This is a setup failure, not a successful filesystem-retry journey or a diagnosed product defect.
The earlier first-init `partial` expectation failure also remains recorded; no unchanged command was retried.

Signed `58165c85` now retains the reset command's full result and immediate root state before assertion and
fixture cleanup, including bounded administrator-directory metadata. All three exact nodes collected successfully
in 0.35 seconds with the candidate and baseline preserved. This improves failure evidence; it does not diagnose
the retained reset error or qualify any administrator test body.

**Snapshot producer-interruption source checkpoint (2026-10-02):** Signed `d63f8e38` adds
`TestNetworklessJailerLifecycle::test_snapshot_capture_file_limit_failure_recovers` in `vm/test_jailer.py`.
The named-runner-only case binds its fault to an isolated-credential worker, retains the original producer pidfd,
requires an unpublished interrupted capture, and checks public recovery followed by a complete captured
RAM/vmstate/rootfs/data-volume restore after source mutation and removal. Fixed-diff review, lint, formatting and
exact-node collection passed (one node, 0.59 seconds, no fixtures). The expected producer termination, recovery
and restore control are not runtime-qualified; root-receiver death and power loss remain separate gaps.

**Snapshot interruption runtime checkpoint (2026-10-03):** The registered case ran once on installed
`a3d7b8d0` and failed at its first guest exec: the agent was unreachable after 50 handshake EOF attempts within
five seconds. Strict outcomes were collected 1, failed 1, passed 0, exit 1; JUnit agreed, with no other outcomes
or resource guard trip. Capture interruption, unpublished-capture recovery and the full restore control were
not reached. Independent exact-owned absence and complete baseline-preservation checks confirmed cleanup.
The agent-unavailability cause remains unresolved; no snapshot-fault or restore pass is claimed.

A subsequent same-profile observation found the first exec failing within five seconds but succeeding against
the same VMM at process age 20 seconds, with the guest agent started. This supports startup timing for that
observation, not attribution to a particular boot service. Signed `9b30573e` gives only the capture case's first
post-create exec an explicit 30-second connection budget under ADR-0013; all interruption and full-restore
assertions remain unchanged. Exact-node collection passed in 0.54 seconds with the candidate and baseline
preserved. The changed runtime invocation was refused by local resource admission before SSH or pytest;
no snapshot outcome was produced. Capture failure/recovery and full restore remain unqualified.

**Snapshot interruption and restore gate (2026-10-03):** The next invocation reached the interrupted,
unpublished capture and its cleanup, but failed at the first exec after a normal source-VM restart using the
default five-second connection budget. Signed `151a7426` adds the same explicit 30-second connection budget
only to that cold-start exec. With test source SHA-256
`d15d4ad3ffe40cdd628d8feeebbdfb2f3ccc85b450687bb029731aa4c905ff1e`, the complete registered case
passed on installed `a3d7b8d0`: strict JSON and JUnit agree on one pass in 29.19 seconds, zero other outcomes.
The file-limit interruption, unpublished-capture recovery and subsequent captured RAM/vmstate/rootfs/data-volume
restore assertions all passed. Typed fixture teardown, independent exact-owned path absence and full baseline
preservation passed with unchanged resource limits and no observed enclosing OOM increments. Earlier failed
runs remain failed. This closes this fault/recovery case, not root-receiver death, power loss or the entire matrix.

**Complete nftables repeat gate (2026-09-28):** Source SHA-256
`5e0105dfeb4fc2ac41bee37fa6c8d0fdfda7497609afda7e7685cc7a61c72133` passed all five cases
twice (33.15/29.30 seconds) against installed candidate `e23b1261` inside only `mvmctl-030-test`.
Both strict reports have zero other outcomes, unchanged configuration and four empty inventories after checked
cleanup. Function-owned fixtures retain full identities and complete baseline inventories/defaults, including
saved environments. Read-only kernel rule observations reject tool/permission/transport failures; missing
chains require independently confirmed absence. Coverage retains real SSH, NAT/masquerade, conntrack,
idempotent sync and private-namespace IP reuse through fixed gateway-bound NoCloud, with an unrelated control
neighbor preserved. The reuse journey proves natural recovery; guest boot ARP may refresh the binding, so it
does not independently prove pre-exposure invalidation ordering. Reports are
`qa-nft-auto-5e01-full5-pass{1,2}.{json,xml}`. This qualifies the current five-case file, not late-invalid batch
rollback, unknown outcomes, foreign-generation faults, anti-spoof enforcement or reboot recovery.

**Complete traffic-policy repeat gate (2026-09-28):** Source SHA-256
`805d0debbb303ac75aac1b3bb241645232528d8976b686877dadcbda72a03c1e` passed all three cases
twice (77.90/79.05 seconds) against installed candidate `e23b1261` inside only `mvmctl-030-test`.
Both strict reports have zero other outcomes, unchanged configuration and four empty inventories after checked
cleanup. Same- and cross-network TCP journeys require actual guest probe execution, exact nonce/boot/endpoint
results, acknowledgements, independent destination delivery logs and live listeners: broker or guest failures
cannot count as denied traffic. They check default deny, exact TCP 8080 grants, TCP 8081 denial and revocation.
The unchanged UDP journey additionally proves exact VM source, sibling exclusion, direction and fresh tuples.
Fixtures use preseeded assets, attempted-create/full-ID receipts, successful-create readiness and typed cleanup
of partial pending networks without root repair. Reports are `qa-policy-auto-805d-full3-pass{1,2}.{json,xml}`.
This qualifies the current traffic-policy file, not peer-exec, adversarial firewall faults or the full release matrix.

**Complete VM lifecycle repeat gate (2026-09-28):** Source SHA-256
`a754ed0c6697fc18c7d43f3669c2c65c734c3f1a015c674ffd469906c590238a` passed all 69 expanded cases
twice (464.22/467.72 seconds) against installed candidate `e23b1261` inside only `mvmctl-030-test`.
Both strict reports have zero other outcomes, unchanged configuration and four empty VM/network/volume/snapshot
inventories after checked cleanup. All 56 methods remain; function-owned fixtures admit attempted creations,
retain complete resource identities and separately account for the new VM identity after cold restore.
Coverage includes real SSH key-file authentication, create/list/inspect flags, resources, logging/metrics,
pause/resume, repeated stop/start, both VM reboot variants, cold snapshot restore, crash recovery, cloud-init,
fixed-port refusals, networking, volume operations and multi-VM creation. Crash injection is an independent
runner-administrator fixture: it verifies the schema-6 root record and process/executable identity and retains
a pidfd before signaling; it is not a public crash API or permission to signal cache PID files.
Reports are `qa-lifecycle-auto-a754-full69-pass{1,2}.{json,xml}`. This supersedes the local lifecycle-file gap,
not host-reboot recovery, snapshot capture/power-loss faults or the complete release matrix and approval.

**Complete cache repeat gate (2026-09-28):** Source SHA-256
`1a095b836d828b3403b45e10b20ada7a59c8b3145f232ec1b9e37837c64eea34` passed all 23 cases
twice (206.83/188.53 seconds) against installed candidate `e23b1261` inside only `mvmctl-030-test`.
Both strict reports have zero other outcomes, unchanged main configuration and four empty main inventories after
checked owned cleanup. All 23 case names remain; the seventeen previously modernized bodies are unchanged.
The six remaining cases now use private selections and receipts: forced misc removal and empty retry, exact
creation-owned non-default root release pruning, aggregate resource removal with protected history retained,
two unprotected-cache removal/recovery journeys, and a live VM absent from another cache's SQLite projection.
The latter independently proves root-authoritative host CLI refusal and the earlier cache process-scan refusal,
with the same guest boot/process/namespace afterward; it does not claim that the legacy comm scanner is authority.
Image pruning consumes warm files before the later misc step; absence remains checked even when the misc result
does not report those earlier removals. Borrowed release manifests/executables, original assets, unrelated
GuestFS appliance/process receipts and complete baseline firewall policy are preserved. Success cases never
erase root-protected history. Reports are `qa-cache-auto-1a09-full23-pass{1,2}.{json,xml}`.
This supersedes earlier local-file gaps, not enabled GuestFS appliance construction/reuse, administrator-level
protected-history retirement, the full release matrix or approval.

**Complete trusted-binary repeat gate (2026-09-28):** Source SHA-256
`bf5bf278d6260ae5ff31be4e993d26955016b59db041a9100f63943ab9b88eda` passed all eleven cases
twice (21.18/27.26 seconds) against installed candidate `e23b1261` inside only `mvmctl-030-test`.
Both strict reports have zero other outcomes, unchanged configuration and four empty inventories after checked
owned cleanup. Remote metadata passed separately; the new release lifecycle passed alone in 9.37 seconds.
A read-only root oracle retains no-follow parent/default/target descriptors, requires schema-6 journals cleaned
and the target slot absent, and matches the installed 1.16.1 manifest/executables to the mirrored archive before
default transfer. Cleanup restores full original default IDs and removes only the admitted test-owned release;
partial or uncertain installations are preserved, not removed speculatively. The other ten case bodies remain
unchanged, including live-VM release refusal. Reports are `qa-bin-auto-bf5b-full11-pass{1,2}.{json,xml}`.
This supersedes the earlier remote/destructive binary gaps, not the complete release matrix or approval.

**Complete image repeat gate (2026-09-28):** Source SHA-256
`4efb233daf0703b2d83efbd67b781b5a76288fc9a3a092c8d2661bd84b59f40a` passed all 38 cases
twice (406.63/412.24 seconds) against installed candidate `e23b1261` inside only `mvmctl-030-test`.
Both reports have strict zero-other outcomes, unchanged configuration and four empty inventories after checked
owned cleanup. Coverage includes mirrored Ubuntu tar import/warm/actual Ubuntu 24.04 boot, Alpine import/boot,
raw and QCOW2 format resolution, defaults, warm copies, inspection and dependency-aware removal. Positive raw
filesystem fixtures carry the OS metadata required by typed inspection; production admission remains unchanged.
Reports are `qa-images-auto-4efb-full38-pass{1,2}.{json,xml}`. This supersedes the earlier complete-image gap,
not the remaining current-source Python matrix or release approval.

**Complete basic network repeat gate (2026-09-28):** Source SHA-256
`6bf44c15a18c9a0572d95196e836615778f2cc6c6372dc71f0602ddd0965a591` passed all 31 cases
twice (52.96/50.45 seconds) against installed candidate `e23b1261` inside only `mvmctl-030-test`.
Both reports have strict zero-other outcomes, unchanged configuration and four empty inventories after checked
owned cleanup. The previously unqualified administrator fault now admits the exact fresh network receipt,
childless bridge, ifindex, alias, MAC, host namespace and boot identity before deletion. Exact-ID sync refuses
the disappeared bridge without changing the post-fault inventory; typed nonforce removal retires only the
admitted missing bridge's logical/firewall state. Replacement identities are refused, not deleted or repaired.
The original 30 cases and shared helpers remain byte-identical. Reports are
`qa-network-auto-6bf4-full31-pass1-pass1.{json,xml}` and `qa-network-auto-6bf4-full31-pass2.{json,xml}`.
This supersedes the earlier basic-file missing-bridge gap; separate current nftables/traffic-policy complete-file
gates are recorded above. Adversarial firewall and reboot-recovery qualification remains open.

**Automatic image format and coherent CI gate (2026-09-28):** Commit `dae5510c` resolves the configured or explicit
`auto` policy through the existing extension/magic detection before extraction. Three behavioral resolver rows
failed against unchanged production; all five then passed (0.010 seconds), including concrete-default and explicit
override precedence. Full inputs/API/image packages passed (0.013/0.032/0.196 seconds), focused race passed
(1.025 seconds), and scoped format/vet passed inside only `mvmctl-030-test`.
The clean exact `dae5510c` checkout also passed pinned Go 1.26.3 offline tidy, architecture, whole-tree
gofmt/golines 0.13.0, agent generation, full vet, the complete CI-selected Go suite, full root Jailer package
(9.190 seconds), and Python runner units (240/240, 7.21 seconds). The ordinary gate used umask 022;
the exact-mode sudoers fixture correction `d92fd383` separately passed the complete host package under both
077 and 022 (0.200/0.204 seconds), without weakening production checks.
Its clean candidate was normally built/installed only inside that runner, SHA-256
`e23b126180f573d45b70a01c800cd43269cca829b0c1a7976f582f3aacf59347` (`0.3.0-rc.auto`).
Evidence is `image-auto-{red,green}.log`, `host-fixture-44f1e702-green.log`,
`full-ci-build-auto-dae5510c.log` and `python-unit-dae5510c.xml`. The image fixture's generic metadata correction
(`4efb233d`) and installed resolver fix then passed all 13 affected or previously unexecuted image cases
(37.95 seconds), with strict zero-other-outcome reports and exact config/four empty inventory cleanup:
`qa-images-auto-4efb-delta13-pass1.{json,xml}`. The later complete-image repeat gate above supersedes this
focused evidence; the current-source Python matrix remains open. This is not GitHub CI or release approval.

**Live terminal console repeat gate (2026-09-28):** Exact console source SHA-256
`307ab71e8298d347e6365cb1a6a250f8f067217852922abea7cf3b93d01facfb` retains the existing
owned relay lifecycle journey and adds one successful interactive PTY journey. The new case passed alone
(10.12 seconds); both cases then passed twice (26.42/24.12 seconds) against candidate `a5518257` inside only
`mvmctl-030-test`, with strict zero-other-outcome reports and exact config/four empty inventory cleanup.
The real installed CLI obtains raw terminal mode through normal endpoint admission, receives a unique
guest-agent-emitted serial marker, detaches via Ctrl-X then d, restores terminal attributes and exits cleanly.
Its retained child pidfd proves exit; original Firecracker identity, root relay observation and vsock responsiveness
remain unchanged. Reports are `qa-console-307a-two-pass{1,2}.{json,xml}`.
This supersedes the earlier successful-attachment/serial-byte gap for this local file; hostile endpoint faults,
the complete release matrix and clean-host qualification remain separate gates.

**Complete logs repeat gate (2026-09-28):** Exact logs source SHA-256
`46c9d7f32f89e837e9ec6d048bce7db3066a0dfbb405da4939ed9f8c207fae09` passed all 11 cases
twice (74.26/86.98 seconds) against installed candidate `a5518257` inside only `mvmctl-030-test`.
Both strict reports have zero other outcomes and exact config/four empty inventories after checked owned cleanup.
The fixture proves actual Alpine 3.23 guest identity and unique serial output rather than requiring the login
banner deliberately removed by image optimization. Tail checks capture actual CR-containing output as bytes,
preserve blank and CR-containing LF records, and compare exact final 5/50 records. Other cases cover kernel versus
VMM logs, live follow, cancellation, missing VM, full-ID/name/IP lookup and retained stopped-VM logs.
Stopped-state proof uses root-observed status, inactive relay and absence from `vm ps`, not the historical PID
display projection. Reports are `qa-logs-46c9-full11-pass{1,2}.{json,xml}`.
This closes this local file's repeat gate, not interactive console attachment or the full release matrix.

**Ubuntu init-layout source gate (2026-09-28):** Commit `d5bf3656` delays OpenRC binary admission until
both required OpenRC directories exist, preserving no-follow confinement and ambiguous-layout refusal.
The new Ubuntu usrmerge publication test failed against unchanged production at `open managed guest file parent`.
Fixed systemd/Ubuntu/OpenRC and missing/ambiguous/unsafe-parent regression cases then passed (0.036 seconds),
the complete LoopMount package passed (0.223 seconds), and focused race checks passed (1.051 seconds).
Scoped gofmt/golines 0.13.0 and vet passed under Go 1.26.3 inside only `mvmctl-030-test`.
Evidence is `ubuntu-init-{red,green,race}.log` and `ubuntu-loopmount-full.log`.
The exact clean `d5bf3656` candidate was built with its embedded guest agent and installed through the normal
installer only inside that runner, SHA-256 `a5518257545d2851d116ee9324a1e0e693babcf9923525523b5bab89d5a5e34c`.
The original mirrored Ubuntu tar-import/warm/VM-boot regression then passed 1/1 in 122.65 seconds, with actual
guest Ubuntu 24.04 evidence, mirror preservation and exact config/four empty inventory cleanup. Strict report:
`qa-images-ubuntu-d5bf-tar-pass1.{json,xml}`. This does not qualify the complete image module or full release matrix.

**Basic network lifecycle repeat gate (2026-09-28):** Commit `8af4839a` modernizes the basic
network file while retaining its separate missing-bridge fault case. Exact file SHA-256
`65c608e4322e53617fc6afc0a0199a8fd315d127d3f66803e04258784d34ac00` passed the 30 safe
cases twice (41.15/41.04 seconds) against installed candidate `833a32a0` inside only
`mvmctl-030-test`. Both strict reports have zero other outcomes and exact config/four empty
inventories after checked owned cleanup. Assertions retain bridge/namespace/lease identities,
configured port state/delay, routes and firewall policy; comparisons exclude elapsed timers,
handles and live counters. A duplicate-name oracle now asserts the actual conflicting name,
stable error code and conflict class. A targeted diagnostic proved the omitted bridge-port
forward-delay countdown was the only changing field before adding it to the elapsed-timer
projection. Reports are `qa-network-65c6-safe30-pass{1,2}.{json,xml}`. The missing-bridge
administrative fault remains unqualified; this is not the whole network/nftables/policy matrix.

**Current-source Go/Python runner checkpoint (2026-09-28):** A clean checkout at `b2566f8a`
passed pinned Go 1.26.3 offline tidy, architecture, whole-tree gofmt/golines 0.13.0, generation,
vet, every CI-selected Go test and complete root Jailer tests (5.214 seconds) inside the freshly
replaced `mvmctl-030-test`. The Python orchestrator unit suite passed 240/240 in 8.42 seconds.
Evidence is `full-go-ci-b2566f8a.log` and `python-orchestrator-units-b2566f8a.{log,xml}`.
The schema-6 candidate initially refused the reused base image's legacy broad sudoers policy.
After explicit runner-only administrator cleanup, normal system installation and host initialization
succeeded; the canonical policy grants only the typed privileged dispatcher. Unused legacy flat
binary copies were removed, and fresh operator config/database and trusted 1.16.2 assets were
initialized. No old root VM journals or operator database were adopted. These local source
gates are not GitHub CI or the complete installed-candidate Python release matrix.

**Console relay lifecycle repeat gate (2026-09-28):** Commit `431b35c1` replaces five permissive
console cases with one owned lifecycle journey, exact source SHA-256
`40458e24434ecb67e48408a38f973ed45a75000d66e022fe9ac8c6b1197b2d8d`. It passed twice
(14.04/13.25 seconds) against installed candidate `833a32a0` inside only `mvmctl-030-test`.
The journey checks root console/VMInspect agreement, full-ID and real-IP selection, relay-only
stop with unchanged live Firecracker identity and working vsock exec, repeated-stop refusal,
inactive attachment refusal, and relay renewal after VM stop/start. Both strict reports have
zero other outcomes and exact config/four empty inventories after checked cleanup. Reports
are `qa-console-4045-pass{1,2}.{json,xml}`. Successful interactive TTY attachment, serial-byte
delivery and its endpoint admission remain separate gates; this journey does not qualify them.

**Root console authority source checkpoint (2026-09-28):** Production commits `0d627628` and
`b306f996`, with test-only corrections `ba7afecd`/`82156ad3`, implement typed root observation
and relay-only stop, strict schema 6, existing-only authority admission, durable stopping intent
before exact pidfd signaling, exit-only stopped publication, independent Firecracker status and
root-backed public console/VMInspect projections. Untrusted stop replies preserve process-start
and outcome uncertainty. Pinned Go 1.26.3 scoped format/vet, complete API tests (0.027 seconds),
complete privileged tests (0.204 seconds), complete root Jailer tests (13.598 seconds) and focused
root console race tests (1.184 seconds) passed inside `mvmctl-030-test`. The two intermediate
failures were typed-nil and schema-5 string-target test fixtures; their corrected invariants passed
separately before the final package gate. Evidence is `ci-console-f2bd/.tmp/console-479f-corrected-root.log`.
At this source checkpoint, no console candidate had been installed. The later relay lifecycle
repeat gate above qualifies survival, stop retry and clean restart; successful interactive
attachment/SO_PEERPIDFD admission remains open. Schema 5 is not adopted.

**Complete peer-exec repeat gate (2026-09-28):** Commit `4ce01436` replaces name-based cleanup
in the two original peer-exec journeys with retained full-ID, creation, backing-file and process
receipts; the four newer cases are unchanged. Exact module SHA-256
`a92fd17cd00a6d1ea1251d110d174f48b8dadd84b28b6f7882660fb44df8df3b` passed all six cases
twice (148.03/136.04 seconds) inside only `mvmctl-030-test`, against candidate `7441189d`.
Both strict reports have zero other outcomes and exact configuration/four empty inventory checks.
The original directional case exercises same-network, cross-network and networkless placement;
the module also covers endpoint lifecycle, opt-in/target-user denial, output/audit handling,
interruption and restore requiring a new endpoint grant. Read-only asset admission and checked
typed cleanup preserve caller baselines. Reports `qa-peer-a92f-six-pass{1,2}.{json,xml}` are in
`qa-release-72696719/.tmp`. This closes this local file repeat gate, not the final current-source
matrix or reboot/root-fault qualification.

**Interrupted VM backing-import cleanup source checkpoint (2026-09-28):** Commit `19376ffa`
allows the existing typed VM removal to cancel only verified incomplete VM-owned rootfs/cloud-init
copies after retained root VM authority proves process/tree/grant absence. The original staging file
is untouched. Cancellation refreshes durable intent before exact unlink, syncs the empty backing
directory, and retains the initialized removed record to prevent re-import. Unknown, changed,
nonempty unrecorded, mounted or foreign-granted files fail closed. Pinned Go 1.26.3 scoped format,
vet and root behavioral regressions (0.233 seconds), including interruption replay and actual mount
refusal, passed in `mvmctl-030-test`; the root race selection passed in 1.522 seconds. Evidence is
`ci-import-06c6/.tmp/import-06c6-focused-root.log`. Tests construct admitted backing fixtures;
that source checkpoint did not qualify end-to-end failed creation, public `VMRemove` or actual power loss.

The later exact-ID public-removal correction connects this existing root cleanup to forced removal when the
caller database row was never published. It preserves caller artifacts and ordinary registered resolution.
Signed integration `5c340074` matches the focused-tested source: inputs/API/CLI packages and vet passed on
Go 1.26.3 inside the named runner, with formatting and exact source hashes checked.
The installed `0.3.0-rc.fccff661` subsequently passed the exact retained interrupted-import recovery case:
strict 1/1, with zero other outcomes. Public exact-ID forced removal cleaned root authority and removed
the verified partial protected copy while retaining caller artifacts; an unknown root ID was denied without
mutation. Caller identity, extents and bounded content samples, unrelated inventories and configuration were
preserved. Receipts are `body-03-final-receipt.md` and `body-03-evidence.tar` (SHA-256
`8859e2f2eafcba0e435b568a7272c2e22d1a657b37a74899812479f0482bbd6e`). This qualifies one retained incident,
not all failed-creation paths, a complete caller-file hash, actual power loss or the full VM domain.

**Trusted-binary safe repeat gate (2026-09-28):** Commit `f4d7adb1` retains all eleven binary
cases and exact archive/release identity and live-lease ownership checks. File SHA-256
`de6739143ab76be1694c696d01cd9f53e665b39f4e59b4ed3f9df8b9b543f6c2` passed the nine
safe cases twice (15.94/15.64 seconds) inside only `mvmctl-030-test`, against candidate
`d28985b6`; the live-lease refusal first passed separately in 10.96 seconds. All eleven
collected. Both strict repeat reports have zero other outcomes; complete configuration and
VM/network/volume/snapshot inventories were preserved. The fixture uses the existing trusted
1.16.2 pair without installing another release or changing defaults. Remote metadata and the
destructive release/default-transfer case remain unqualified. Reports are
`qa-bin-de67-live-focus` and `qa-bin-de67-safe9-pass{1,2}.{json,xml}` in
`qa-release-72696719/.tmp`. This is not the complete binary-domain or release gate.

**Exact live-detach source checkpoint (2026-09-28):** Production commits `0728487a` and
`65344c35`, with fixture/format corrections `42c0bab6` and `c37fc5c9`, authenticate the
selected guest PCI disk removal before root detach and preserve exact interrupted ACL-revoke
recovery. Observation opens existing authority only; it does not create missing directories or
locks or repair a pending journal. Pinned Go 1.26.3 generation, scoped format/vet, agent/vsock
behavior and race tests, root existing-authority tests, focused root Jailer regressions and the
complete root Jailer package (12.850 seconds) passed inside `mvmctl-030-test`. The original
live-detach case then passed 1/1 in 9.94 seconds against installed runner candidate
`0.3.0-rc.detach` (SHA-256 `7441189d6821ac1d5b9e88ac6f92eda492410f962e7635dbaf97c9852c4e5056`).
It proves actual guest-device disappearance and a still-running VM, with strict zero-other
outcomes and exact configuration/four empty inventory checks after cleanup. Report
`qa-lifecycle-detach-c37f-focus.{json,xml}` is in `qa-release-72696719/.tmp`.
This is not the complete lifecycle repeat gate or complete current-source CI.

**Owned cache-prune repeat gate (2026-09-28):** Commit `a6a6f384` modernizes six aggregate-preview,
default-image retention, and actual VM/network/kernel/image-prune cases; all other seventeen methods,
including the global GuestFS preservation regression, are unchanged. File SHA-256
`67684f022df8d2b0fc1ecd64e5f7b41d52db0bd44b43a00cc7bfe25e6f013cb4` passed 6/6 twice
(88.61/87.63 seconds) inside only `mvmctl-030-test`, against candidate `d28985b6`; all 23 cases collected.
Both strict reports have zero other outcomes. Complete configuration and main/private inventory and
policy baselines were checked. Fixtures retain exact root namespace, bridge, mount and file receipts;
preview preserves guest responsiveness and boot identity. Firewall comparison preserves complete
policy and ordering, excluding only handles/live counters; no foreign-table fixture was present.
Bridge comparisons retain the raw receipt and exclude only the independently observed `gc_timer`
countdown. Private imports use distinct content-ID inputs without replacing baseline assets.
Reports `qa-cache-6768-six-pass{1,2}.{json,xml}` are in `qa-release-72696719/.tmp` and ignored local
evidence. This is not the whole-cache repeat gate or administrator-level cache-clean qualification.
Pytest reported old-directory removal warnings: typed VM removal intentionally retains root-controlled
backing directories/tombstones and unreferenced protected kernel copies. No protected tree or external
authority was recursively deleted; those inactive fixture artifacts remain until runner disposal.

**Ordinary-exec repeat gate (2026-09-28):** Commit `2dbf63a4` retains all six ordinary-exec
test bodies while replacing their shared fixture with exact full-ID, creation and process-identity
ownership and read-only preseeded-asset admission. File SHA-256
`b177e793689d6dbd6b29cf46e23dc59f8ab02c7df9192a2e97759f557fb3c79b` passed 6/6 twice
(5.05/8.59 seconds) inside only `mvmctl-030-test`, against candidate `d28985b6`; all six collected.
Both strict reports have zero other outcomes. Full configuration and eleven inventory baselines were
preserved, with checked owned VM/network cleanup. The fixture selects the present Alpine 3.23 image
by full ID without assuming it is the default; no pull or default-asset change occurs. Reports
`qa-exec-b177-six-pass{1,2}.{json,xml}` are in `qa-release-72696719/.tmp`. This closes the local
ordinary-exec file repeat gate, not the separate peer-exec repeat gate or release qualification.

**Host-domain repeat gate (2026-09-28):** Commit `b596fb2c` modernizes first-capacity observation and
ordinary-user clean/reset refusal ownership; the other 21 methods are unchanged. Exact file SHA-256
`8ff215defb4a5ce53bd349de6698b5514fb5d6c3e2a13e509e8a03eb1b52f0c4` passed all 24 cases twice
(17.30/13.78 seconds) inside only `mvmctl-030-test`, against candidate `d28985b6`. Both strict reports
have zero other outcomes; complete configuration and VM/network/volume/snapshot inventories were
preserved. The three changed cases first passed 3/3 after shortening only an overlong fixture network
name; its earlier failure occurred before network creation. The existing root 1.16.2 pair was compared
with exact mirrored archive members before testing; no installation, enrollment or successful reset
was exercised. Capacity initialization uses private cache/config paths, and guard cases retain exact
full-ID, creation and process identity plus healthy guest access and administrator-state baselines.
All 24 collected. Reports `qa-host-8ff-focus` and `qa-host-8ff-full-pass{1,2}` are in
`qa-release-72696719/.tmp` and ignored local evidence. This closes the local host-file repeat gate,
not clean-host administrator enrollment/reset, reboot attribution or complete release qualification.

**Global GuestFS cleanup removal and live mount repair (2026-09-28):** Production commits `c0f41890`
and `fe7e636e` remove guessed global GuestFS cleanup and prepare live-volume source mounts before VM
namespace entry, respectively. Candidate `0.3.0-rc.volume-guestfs`, SHA-256
`d28985b69ec8401c51151095f192835b40c8c25ea61903d7d1b9f257f3fcfc59`, was built and installed only
inside `mvmctl-030-test`. Its Go source is the root-tested mount commit `4a4c5749` plus the reviewed
GuestFS source `5fec2a2c`, equivalent to those integrated production changes.

The existing misc-actual cache case, frozen file SHA-256
`1f0836f7066991cf8238718898db880e785e45c73851ec2ba9dd6e9c48114cca`, genuinely failed against the
previous installed binary: selected-cache pruning deleted an owned out-of-cache appliance and killed an
unrelated same-user process matching the old guestfish predicate. That RED was admitted only after
refusing all unrelated global cleanup targets. It had one assertion failure and no teardown errors.
The corrected candidate passed the same case twice (0.82/0.84 seconds), removing selected appliance,
service-binary and warm artifacts while preserving exact global file and pidfd-held process identities.
Owned cleanup, complete configuration and four empty resource inventories were checked. Cache collection
retains 23 cases; the other 22 methods are unchanged. Reports `guestfs-misc-old-red` and
`guestfs-misc-green-pass{1,2}` are in `ci-volume-4a4c/.tmp` and ignored local evidence. This is one newly
qualified actual-prune case, not whole-cache or enabled-GuestFS provisioning qualification.

The mount change separately passed a genuine old-order root RED, five new root namespace subcases,
scoped format/vet and the complete root Jailer package (12.385 seconds). The original lifecycle
`TestVMVolumeIntegration::test_attach_volume_to_running_vm_succeeds` passed on the installed candidate
in 6.94 seconds with strict zero-other-outcome evidence and unchanged configuration/empty inventories;
report `qa-live-attach-fe7-d289-pass1` is in `qa-release-72696719/.tmp`. These checks do not close
live-detach guest removal, lifecycle's whole-file repeat gate, or complete current-source CI.

**Additional peer-exec behavioral gate (2026-09-28):** Commit `54148a8d` adds four installed-candidate cases;
file SHA-256 `211d4954d7f76c32fb1a4cef1a9ea69c1abdc2a7a4b29dfde570a7e339f3e311` passed 4/4 in 55.66 seconds
inside only `mvmctl-030-test`, against candidate `47ce6fda` (Go source `37d399d4`). The strict report has zero
other outcomes; complete configuration was unchanged and VM/network/volume/snapshot inventories were empty.
The cases prove opt-in and exact non-root target-user denial without destination side effects, separate output
streams/nonzero exit status, exact session-correlated command-redacted audit output, cancellation of the observed
destination process group, and full restore requiring a new endpoint grant. Ownership uses returned full IDs,
creation metadata and the capture request-ID receipt; ambiguous cleanup refuses deletion. Reports
`qa-exec-211d-new4.{json,xml}` are in `qa-release-72696719/.tmp` and ignored local evidence. Existing directional
and restart/reboot cases were unchanged. This is a focused one-pass gate, not complete exec-domain repeats,
root session-allocation absence/exhaustion proof, or whole-release qualification.

**Isolated cache selection (2026-09-28):** Commit `e3412121` modernizes eight existing cache init,
VM/network preview and argument-rejection cases in per-case cache/config/temp paths. All eight passed
twice in 7.03 and 6.49 seconds inside only the named runner, against candidate `c460278d`.
Exact file SHA-256 is `da23d310e9d273c657d8db30068e4cdb5c63d9478e68053ab4d6dc60e565abe8`;
reports `qa-cache-selected-da23-pass{1,2}.json` are in `qa-release-72696719/.tmp` and local ignored evidence.
Both strict reports have zero other outcomes, complete main config stayed unchanged and four resource
inventories were empty. These cases check selected-cache file hashes/identities and exact key/network
teardown; all twenty-three methods remain, with fifteen unchanged cases explicitly unqualified.
Aggregate previews, actual prune/clean and live-VM failure safety are not part of this eight-case gate.
Their audit found `CachePruneMisc` invoking global guestfs cleanup during dry-run; production guard
`578254f2` passed pinned focused API/CLI formatting, vet and Go tests in the runner, but its candidate
installation and owned out-of-cache sentinel regression are pending. No outer controller was changed.

**Console qualification blocker (2026-09-28):** Public console lookup still derives cache PID/socket
paths while the authenticated launch publishes the relay endpoint in the caller runtime directory.
Existing inactive-relay cases do not qualify active state, attach or kill. Merely changing paths is
insufficient: PID mirrors are not signal authority, and a relay-only kill currently conflicts with
the recorded VM liveness contract. A typed root-authorized durable relay-stop design awaits approval;
no console production correction or active-relay qualification is claimed.

**SSH-domain repeatability (2026-09-28):** Commit `66a233cb` modernizes the nine older SSH cases and
preserves both relocated key journeys unchanged. The complete eleven-case file passed 11/11 twice in
94.37 and 99.41 seconds inside only `mvmctl-030-test`, against candidate `c460278d` and Go source
`19c55345`. The exact test-file hash is
`8f002be181e7da10cd265bae1eb4e04ab6c42762a65c10560e856e42b7996fdc`; executed checkout was clean
`72696719` plus this sole reviewed SSH overlay. Both strict reports have zero other outcomes, all four
resource inventories were empty and complete config was unchanged. Reports `qa-ssh-72696719-pass{1,2}.json`
are in the runner's `qa-release-72696719/.tmp` directory and local ignored evidence. Cases now require
actual authentication through stored, named and exported keys, exact root command output, unauthorized
key rejection with healthy authorized access before/after, and a three-second guest command succeeding
with a two-second connect budget. Function owners check seven resource/asset inventories and config,
excluding only kernel/key reconciliation/default-restoration timestamps. Guests use preseeded Alpine
3.23, 512 MiB disk, 256 MiB RAM and one CPU; no pulls or config/default asset changes occur.
This closes the whole SSH-domain local repeat gate, not console lifecycle, other domains or release approval.

**Internal-service entry-point repeatability (2026-09-28):** The five retained `run` cases collected and
passed 5/5 twice in 0.07 and 0.06 seconds inside only `mvmctl-030-test`, against candidate `c460278d`.
The runner checkout was clean exact commit `72696719`; production source matched `19c55345`.
Test-file SHA-256 is `fff27af800dde6a7d94a9ec00caf5e4b4b106a985a6b758d947f6b66cefdd4fe`.
Both strict reports have zero other outcomes, complete config was unchanged, and VM/network/volume/snapshot
inventories were empty. Reports `qa-run-72696719-pass{1,2}.json` are under the runner's
`qa-release-72696719/.tmp` directory and in local ignored evidence. This proves supported help and
missing-identity rejection for console/NoCloud service entry points, not relay discovery, serving or cleanup.

The command tables below describe tests for the currently implemented CLI, including transitional behavior that v0.3.0
will remove. A `✅ Deep` row proves only the behavior named in that row; it is not evidence that the clean-break security
architecture is release-qualified. The authoritative release gate is `tasks/todo.md` Task 17 plus `docs/RC_QA.md`.

**Read-only invariant-domain repeatability (2026-09-28):** The ten retained default/JSON/flag cases passed
10/10 twice in 0.53 and 0.51 seconds inside only `mvmctl-030-test`, against candidate `c460278d`.
Executed source was exact `0f95b7bc` plus the sole reviewed invariant-file cleanup, SHA-256
`2f10282b563bc961aa98501017b70e7a073613d475ccb370a0aacae32efb1471`, now committed as `53ea1464`.
Both strict reports have zero other outcomes and all four resource inventories stayed empty. Reports
`qa-invariants-0f95b7bc-pass1.json` and `qa-invariants-0f95b7bc-pass2.json` are under the runner's
`qa-release-0f95b7bc/.tmp` directory. Two misplaced legacy cache cases were removed: they decoded the old JSON
shape, could restart a deleted VM, counted destructive success as a refusal pass, and used broad process-kill
cleanup. Their source never established the claimed surviving-process precondition. Cache clean lifecycle and
failed-kill safety remain cache-owner qualification work; retiring false-positive tests does not qualify them.

**Measured-pressure module repeatability (2026-09-28):** Commit `76fc9b3d` replaces the false fixed-fill
pressure assumption with measured physical tmpfs staging and an optional, bounded four-MiB calibration.
The formerly failing swap-present selector passed 1/1 in 28.97 seconds; the complete three-case module then
passed 3/3 twice in 60.43 and 60.81 seconds inside only `mvmctl-030-test`, against candidate `c460278d`
and Go source `19c55345`. Exact test-file SHA-256 is
`f2a41614f942d06ec251357426e8cdc3adaefff76721f260b4b6c4083684d0e3`.
Both swap variants held at least 80% usable guest-RAM occupancy for at least 15 seconds with fresh
`memory.events.high`, historical peak above the 224 MiB high threshold, unchanged process/boot identity,
responsive exec, unchanged policy and no increase in visible OOM counters. The leaf retained literal
`memory.max=max`; successful reclaim was allowed to lower current usage below high. All four inventories
were empty afterward, complete config JSON matched the baseline, and only the test-owned swapfile was
disabled and removed. Reports `qa-memory-calibration-a93c02db-present.json` and
`qa-memory-calibration-a93c02db-full{1,2}.json` are in the runner's `qa-consistency-7073592b/.tmp` directory.
Every run reached pressure during initial staging, so the nonzero optional calibration branch remains
unexecuted. The focused swap-present run observed 8278016 leaf swap bytes; swap availability does not
require actual swapping in every repeat. This closes the bounded sustained-pressure/module-repeatability
gate, not guest allocation-failure handling, the original reported incident, full RAM exhaustion,
host/global/outer-ancestor OOM safety, snapshot memory policy, or release qualification.

**Config-domain repeatability (2026-09-28):** Commit `1d7f6502` preserves all twelve public config behaviors
and restores exact prior overrides, including absence, after key, category and global resets. All twelve
passed twice in 0.85 seconds per run inside only the named runner. Source-file SHA-256 is
`f977acf8194ad735f639173753f2517534e0bff6024ac194833895e4f3aaa919`; candidate remains `c460278d`.
Both strict reports recorded zero other outcomes, complete config JSON equalled its pre-run baseline,
and all four resource inventories remained empty. Reports `qa-config-a93c02db-pass{1,2}.json` are in
`qa-release-0f95b7bc/.tmp`. Invalid requests assert error envelopes and unchanged configuration;
category/global reset cases retain the default/type metadata and verify every affected override is null.
This closes the config domain's local repeated-run gate, not the entire Python matrix or release approval.

**Full-journey repeatability (2026-09-28):** Commit `b92e995a` modernizes all fifteen original journey selectors
for schema-1 JSON, typed traffic policy, bounded guests and exact owned cleanup. The frozen module passed
15/15 twice in 204.33 and 210.10 seconds inside only `mvmctl-030-test`, with zero other strict outcomes,
empty VM/network/volume/snapshot inventories and unchanged complete config JSON. Exact file SHA-256 is
`9e4048fc5a5a10d00e9604b05b5762389d7b2a358cb9018d398c894a81785da1`; candidate remains `c460278d`.
Reports `qa-full-journeys-a93c02db-final{1,2}.json` are in the runner's `qa-release-0f95b7bc/.tmp` directory.
The first pre-final batch passed eight cases and exposed a test-input error: repeated string `--ssh-key`
flags retain only the last argument. The sole input correction uses one comma-joined argument; the failed
multi-key case and six previously unexecuted cases then passed 7/7 in 129.93 seconds before full repeats.
Both keys are exercised through actual SSH. The historical ping selector now proves same-network TCP
default denial, exact source-VM permission, echoed delivery and revocation while the listener stays healthy.
Other journeys verify guest IPs, lifecycle process/boot changes, three 16 MiB raw block-device extents,
five sequential create/remove cycles, and ten distinct creations/SSH sessions in cohorts of at most three
live guests. Cached Alpine 3.23 requires 360 MiB rootfs, so each guest explicitly uses 512 MiB disk,
256 MiB RAM and one CPU. Canonical Jailer cases are no longer duplicated through this module's import.
This closes the full-journey domain's installed-candidate L2 repeat gate, not ten-way simultaneous load,
Tier 3, host-reboot recovery, unrelated matrix domains or release approval. Runner free disk remained 2.4 GiB.

**Key ownership and tier placement (2026-09-28):** Commit `a3f4d71f` leaves twenty-six VM-free cases in
the Tier 1 keys domain and relocates its two VM-dependent journeys into the already-selected Tier 2 SSH
module. The complete keys file passed 26/26 twice in 6.81 and 6.59 seconds inside the named runner;
the two relocated cases passed 2/2 in 15.49 seconds. Exact file hashes are
`7304b0e4d6b08321b145e2391c59287d0180a4606d711e09cffc847cdab02b1b` (keys) and
`b7b3c79b9bc9e04a4c4507328cbea0a718c1078f7361cb4d4b7fb5c0206d3845` (SSH), against candidate `c460278d`.
All strict reports have zero other outcomes; four resource inventories were empty and config unchanged.
Key owners restore the exact original default-ID set and compare all stable inventory fields; `updated_at`
is excluded because public default restoration intentionally changes it. Owned cache files and exports
receive identity/absence checks, cleanup failures are aggregated, and unrelated artifacts are preserved.
The relocated cases prove real SSH before/after cached-key removal, plus both default key identities in
guest authorized_keys and separate authentication using each exported private key. These are small,
preseed-only guests; they do not qualify the nine unchanged SSH cases or the full SSH-domain repeat gate.
Reports `qa-keys-fc614785-pass{1,2}.json` and `qa-key-ssh-fc614785.json` are in
the runner's `qa-release-0f95b7bc/.tmp` directory. No asset pulls, config/default asset changes or outer install.

**NoCloud correction and next bounded QA gates (2026-09-28):** Clean committed `9aaee216` passed the complete
pinned Go 1.26.3 local CI sequence and full root-effect Jailer package (22.090s). The CI script's own
`coverage-jailer.out` was preserved under ignored `.tmp` before the clean-status check; no passed gate was
rerun to handle that artifact. These logs are `go-ci-9aaee216.log` and `root-jailer-9aaee216.log`.
Subsequent lifecycle QA advanced 41 passes, then failed NoCloud NET creation. The retained child log proved
`file type does not support deadline`: the inherited blocking readiness pipe could not send either startup
acknowledgment. Fix `0d450874` restores nonblocking mode before `os.NewFile`; independent real-pipe success/error
JSON and invalid-FD Go tests, scoped format/vet, and the original NET-create selector passed (6.86s).
The new runner-only candidate `0.3.0-rc.nocloud-ready` has SHA-256
`47ce6fda8c8a063f4989682e4384c519edca179df5079f6a45d0a910cd5565a9`.
This NoCloud source change is beyond the complete-CI checkpoint; full current CI remains a later milestone.

Kernel-import commit `cfd0c561`, file SHA
`97e0b2225c43981bc59d9eb127f9fa990d196701398104613cc88391a4ff48ba`, passed all 8/8 cases twice
in 26.98 and 23.45 seconds. Preseed-only, function-owned cases retain complete baselines, prospective content-ID
collision refusal before effects, exact same-ID reimport backing renewal, source preservation, independent default
restoration, and one bounded networkless guest. Initialization commit `b6249045`, file SHA
`7218655005e8b82ffb62c9996a334e7813a78a41ef404200b1b75648b0865238`, passed the six explicitly selected
safe cases twice in 3.81 and 3.61 seconds. Two non-skip-host administrator-enrollment cases remain unchanged,
excluded and unqualified; this is not an eight-case initialization pass. Only source-proven kernel/network
observational `updated_at` fields are normalized; other identity/default/config fields remain checked.
All reports have zero other outcomes; final configuration matched exactly and four resource inventories were empty.
Reports `qa-nocloud-ready-fixed`, `qa-init-7218-pass{1,2}`, `qa-kernel-97e0-pass{1,2}` and
`qa-next-focused.log` are in the runner's `qa-release-72696719/.tmp` checkout and ignored local evidence directory.
Whole lifecycle repeats, active console/logs, incomplete-import retirement, and the remaining release matrix stay open.

**Aggregate cache-preview and generated-ISO correction (2026-09-28):** Production guard `578254f2`
prevents dry-run from invoking global guestfs cleanup; producer fix `a8ddedb7` sets generated cloud-init ISOs
to `0600` before protected import. Both ISO behavioral Go subcases failed against the old producer; scoped
Go 1.26.3 formatting, vet and complete cloudinit/API tests passed after correction. Built and installed only
inside `mvmctl-030-test`, candidate `0.3.0-rc.preview-iso` has SHA-256
`6e9072ba462b538aa426fb1dc29f7fdf7219a1a3c00a6425e9566aa468b83c79`.
The actual old-candidate cache dry-run deleted the owned out-of-cache sentinel and failed the physical
identity assertion; that intentional RED run was admitted only after checking for unrelated guestfs
process, lock, socket and appliance targets. With the corrected candidate, both aggregate-preview cases
passed 2/2 twice in 1.42 and 1.58 seconds, with zero other outcomes and checked owned cleanup. Test commit
`073e5ec5`, exact file SHA `720e3eab5c2f9896b2556df9499f1314a89539fdf4175e01049e753c9bc999ad`.
Together with the prior selected eight cases, ten cache cases have bounded repeat evidence; thirteen
remain unqualified. The previously failing ISO-create selector passed 1/1 in 6.61 seconds. All four
resource inventories were empty and full configuration unchanged. Reports `qa-cache-720e-{red,green1,green2}`,
`qa-iso-mode-fixed` and `qa-preview-iso-gates.log` are in the runner's `qa-release-72696719/.tmp` checkout
and the ignored local evidence directory. This is not full current CI or whole lifecycle qualification.
The initial ISO failure left a zero-source/backing, reference-free `copying` record: producer normalization
does not retire that existing authority. Its bounded cancellation design remains awaiting approval.
Logs' corrected fixture now reaches creation/adoption/cleanup, but public inspection reports the console
relay stopped; logs and console qualification remain blocked on the separate console-authority decision.

**Volume-domain repeatability (2026-09-28):** Commit `f26bfab6` preserves all thirty-seven selected cases
while replacing unchecked cleanup and implicit large guests with exact ownership and bounded resources.
The frozen file passed 37/37 twice in 94.25 and 96.96 seconds inside only `mvmctl-030-test`;
strict reports have zero other outcomes. Exact file SHA-256 is
`eac82c8b80a93d5fd39b801cf5fda424642b3b1c27631b4ad4fbb2271145e9da`, candidate `c460278d`.
All four inventories were empty, full config JSON matched baseline, and free disk remained 2.4 GiB after
both runs. Reports `qa-volume-fc614785-pass{1,2}.json` are in `qa-release-0f95b7bc/.tmp`.
Public-CLI journeys retain raw-only admission, invalid/removed flags and sizes, growth/shrink checks,
attachment cycles, stopped-but-attached growth, running-VM force-removal refusal and stopped-VM removal.
All twelve nested creates use 512 MiB disk, 256 MiB RAM and one CPU, with at most two guests;
ordinary data volumes use 8–32 MiB. Dependency admission reads preseeded assets rather than pulling or
changing defaults/configuration. Every case and the shared-volume lifetime perform checked owned
VM/volume/key/network cleanup and baseline comparisons. Only kernel `updated_at` is excluded from
asset comparisons because public listing reconciles missing recorded files and refreshes that timestamp;
all other kernel fields, resource identities/defaults and config remain checked. The changed-source case
preserves the unrelated replacement and verifies/removes only its exact test-owned retained inode.
This closes the complete volume module's local repeat gate, not snapshot fault recovery, aggregate
storage quotas (still deliberately unresolved), full matrix qualification or release approval.

**Exact-source CLI-domain repeatability (2026-09-28):** The complete CLI domain collected 24 items and passed
24/24 twice in 0.36 seconds per run inside only `mvmctl-030-test`. Source checkout was clean exact commit
`0f95b7bc574f29b6c20285159d31cc7c4f9db008`; its Go source independently matched `19c55345`, and installed candidate
SHA remained `c460278d`. Both schema-1 reports have zero other outcomes, all four resource inventories stayed empty,
and source status stayed clean. Reports `qa-cli-0f95b7bc-pass1.json` and `qa-cli-0f95b7bc-pass2.json` are in
`/home/runner/mvmctl-030-ci-307e09d2/.tmp/worktrees/qa-release-0f95b7bc/.tmp/`.
This closes the named CLI domain's local repeated-run gate, not global structured-JSON invariants, cache-clean
failure safety, the other domains, remote CI, or release approval.

**Current-candidate CLI refresh (2026-10-08):** The complete `tests/system/cli/test_cli.py` module passed
24/24 once in 0.39 seconds inside `mvmctl-030-test`. Installed candidate
`0.3.0-rc.vmexport-journal-1a2506e6` retained SHA
`d13c2a25fefe65a592e773de099a23e5fcbd2ce3e35cee77948fef4aa967f706`; the nine current Python module/helper
inputs matched its clean source checkout byte-for-byte. Strict schema-1 JSON reports collected/passed24 with all
other outcomes zero; JUnit agrees at 24 tests with no failures, errors or skips. Independent POST and all 21
preservation checks passed, including candidate/artifact metadata, source cleanliness, persistent baseline,
controller/backing identity, boot/VMM identity, controls/events and empty VM/jobs inventories. The existing
contract used PRE/POST snapshots and a 90-second subprocess timeout, not live memory monitoring. No VM creation,
disk copy, build or installation occurred. This is one refreshed CLI-module pass, not a repeatability refresh,
global structured-JSON qualification, VM-export recovery, full current-source matrix or release sign-off.

**Measured-memory qualification follow-up (2026-09-28):** The revised three-case draft collected successfully;
the swap-absent sustained hold passed, but swap-present failed its real-pressure assertion. Guest availability was
15528 KiB out of 196992 KiB (over 92% occupancy), while actual leaf peak/current was 233127936 bytes, below high
234881024, with zero high/OOM events. Occupancy and read sweeps therefore do not guarantee host-cgroup pressure.
The strict report `qa-cgroup-working-set-530fa30b.json` records one pass, one failure and one unexecuted item counted
as an error; no complete module pass is claimed. Exact configuration, VM and test-owned swap cleanup passed,
including unchanged `guestfs_enabled`; all four inventories were empty and swap inactive. The draft remains only
in an ignored persistent worktree, not committed test source. The retry circuit breaker stopped further qualification
attempts: a workload design must produce measured pressure safely rather than drop the high-event assertion,
change production memory policy, or call occupancy alone a pressure pass.
A subsequent once-only guarded diagnostic allowed at most four extra 1 MiB guest writes, but initial staging already
produced 50 high events, peak 235139072 above high 234881024 and 5124096 swap bytes. It executed zero calibration
increments and completed exact cleanup. Report `memory-pressure-calibration-53ea1464.jsonl` is diagnostic only:
this establishes another bounded pressure observation, not the unexecuted calibration branch or a qualification fix.

**Durable exec-grant lifecycle checkpoint (2026-09-28):** One networkless two-VM journey passed 1/1 in 27.93
seconds inside only `mvmctl-030-test`. An exact non-root `runner` grant produced remote stdout and destination file
effects, survived typed destination and source stop/start and destination reboot without VM/policy recreation,
and admitted the new authenticated tasks and guest boot identities. Retained pidfds proved each old process exited;
stopped endpoints and revoked grants refused the command without its destination file effect. Checked teardown left
all four resource inventories and exec policies empty. Strict report `qa-exec-peer-lifecycle-959642ba.json` is under
the runner's `qa-consistency-7073592b/.tmp` directory; candidate/source remain `c460278d`/`19c55345`.
This closes the named VM restart/reboot journey, not host reboot, snapshot restore, peer-identity attacks,
capacity/deadline/cancellation, audit redaction, or complete matrix qualification.

**Private namespace and stale-IP reuse checkpoint (2026-09-28):** One installed-candidate journey passed 1/1
in 21.03 seconds inside only `mvmctl-030-test`. Root-owned topology receipts matched the named namespace and
Firecracker's actual network namespace; a concurrent control VM had a distinct live namespace. Real guest-to-gateway
HTTP warmed A's dynamic neighbor, typed removal retained that stale binding, and B reused the same IP with a new
MAC and endpoint identity. Before B's first HTTP probe the old MAC was absent; B and the control VM both reached
the gateway with their correct bindings. Checked teardown proved cleaned receipts, absent namespace/jail/link/rule
resources, and empty VM/network/volume/snapshot inventories. The first attempt hit a test-observation `KeyError`
because device-filtered `ip` JSON omits `dev`; only that query was corrected, retaining actual device evidence.
Strict report `qa-namespace-ip-reuse-correction-959642ba.json` is under the runner's
`qa-consistency-7073592b/.tmp` directory. Candidate/source remain `c460278d`/`19c55345`. Guest boot ARP may also
refresh the binding, so this does not independently attribute recovery to pre-exposure invalidation, or qualify
spoofing, prior-host-boot topology recovery, or the complete matrix.

**Bounded working-set diagnostic (2026-09-28):** A separate swap-present probe used at most twelve physical
16 MiB appends, a declared 16 MiB control reserve, and bounded guest read sweeps. It stopped after 16.04 seconds
with 145358848 physical tmpfs bytes, 145465344 guest Shmem bytes, and 15568896 available bytes out of 201719808
usable guest bytes. The leaf recorded 78 new high events, peak 235130880 above high 234881024, 8310784 swap bytes,
unchanged process/boot identity and working exec; visible OOM counters did not increase. VM, exact-owned swap and
configuration cleanup completed, leaving all four inventories empty and no active swap. The ignored diagnostic
source/report are not qualification tests: `memory-working-set-probe-959642ba.jsonl` explicitly reports
`release_qualification=false`. This provides a measured workload design, not a sustained-hold pass, full RAM
exhaustion, guest allocation-failure attribution, or host/ancestor OOM safety. Production memory policy is unchanged.

**Captured-epoch and competing-writer checkpoint (2026-09-28):** Both independent-volume restore variants passed
with one captured epoch across a mounted tmpfs RAM marker, durable rootfs marker, raw-volume prefix/hash, and guest
boot identity. All three markers were changed after capture; normal source removal and confirmed source crash/removal
each restored the original captured values. A separate public-CLI journey established two real writable VM references
and shared byte effects in both directions, required structured capture refusal with no published snapshot artifact,
then removed only the peer and successfully captured the same source/volume as a positive control. The first group
recorded two passes and one test-schema failure (numeric error class 1 was incorrectly expected as a string); after
that sole assertion correction, only the failed competing-writer case reran and passed 1/1 in 17.90 seconds.
Strict reports under `/home/runner/mvmctl-030-ci-307e09d2/.tmp/worktrees/qa-consistency-7073592b/.tmp/` are
`qa-snapshot-consistency-7073592b.json` and `qa-snapshot-competing-correction-7073592b.json`. Candidate/source remain
`c460278d`/`19c55345`; all four inventories were empty after both commands. This proves the named quiescent captured
epoch and competing-VM writer refusal, not application transaction consistency, interruption during capture,
untrusted host-writer exclusion, or complete fault/release qualification.

**Source-VM UDP policy checkpoint (2026-09-28):** The new installed-CLI journey passed 1/1 in 27.20 seconds in
only `mvmctl-030-test`, using three small mirrored Alpine VMs on one managed network. A real UDP responder records
unique packet nonces and sends exact acknowledgements; local readiness is checked before and after each probe, and
denial requires both timeout and absent destination delivery. It proves default deny, an exact source-VM/port grant,
sibling-source denial, adjacent-port denial, reverse denial, and fresh-flow denial after revocation. Strict report
`qa-traffic-source-vm-udp-7073592b.json` is in the runner's `qa-consistency-7073592b/.tmp` directory; all four resource
inventories and both traffic-policy lists were empty afterward. A transfer-only function-order correction retained
the executed source and independently matched all function ASTs before final collection; no test body changed.
This closes the named same-network UDP/source-VM/reverse journey, not spoofing, IP reuse, cross-network UDP, reboot,
firewall conflicts, or the complete current-source matrix.

**Snapshot flags and source-crash checkpoint (2026-09-28):** A ten-case group passed 10/10 in 120.56 seconds
inside only `mvmctl-030-test`: both `test_networkless_snapshot_restores_independent_data_volume` variants and the
complete eight-case `test_vm_snapshot_load.py` module. The crash variant authenticates the exact source process and
jail, proves exit through a retained pidfd, observes public crashed status and `ps` absence, removes the source VM and
volume, and restores the original guest-visible volume hash. The normal-removal variant retains the same assertions.
Restored volume identities are registered before readback assertions so checked teardown can remove them on failure.
The remaining four flag/inspect cases now use tiny function-owned VMs, versioned JSON, and checked cleanup; `--pause`
starts from running and retains the same paused process, while names and inspect metadata/resources match exact values.
Candidate SHA-256 remains `c460278d4329d6818c27e9ec82ce47d65d53b7da58e71a27c208c4c3509d0da6` (Go source
`19c55345`). Strict report
`/home/runner/mvmctl-030-ci-307e09d2/.tmp/worktrees/ci-cb0848ef/.tmp/qa-volume-crash-snapshot-flags-72c8ef25.json`
records ten collected/passed and zero other outcomes. All four inventories were empty afterward. This closes the
snapshot module and post-publication source-crash journey, not capture interruption, concurrent writers, whole-set
consistency under faults, managed endpoint conflicts, or the full release matrix.

**Firecracker 1.16.2 affected gate (2026-09-28):** The exact formerly failing pause/resume-vsock case passed 1/1,
then the complete affected group passed 15/15 in 79.83 seconds inside only `mvmctl-030-test`. The group covers nine
lifecycle items, four full-capture/restore owners, older-release launch refusal and missing-source-release restore
refusal. It proves pause/resume retains process/guest identity with working new vsock connections; three stop/start
cycles, both reboot modes, paused removal, cold disk capture/restore, crash recovery and typed state refusals; and
paused/running capture with default-paused/explicit-resumed restore and durable guest markers. Candidate SHA-256 is
`c460278d4329d6818c27e9ec82ce47d65d53b7da58e71a27c208c4c3509d0da6`; independently compared Go source matches
`19c55345`. Strict report
`/home/runner/mvmctl-030-ci-307e09d2/.tmp/worktrees/ci-cb0848ef/.tmp/qa-fc1162-lifecycle-snapshot.json`
records fifteen collected/passed and zero other outcomes. VM/network/volume/snapshot inventories were empty afterward.
The official mirrored 1.16.2 pair imported through the exact release-specific plain-GNU and bounded UID metadata
profiles, with no repacking or trusted-checksum bypass. The later checkpoint above qualifies the remaining flag/inspect
classes and one data-volume source-crash case; interruption and broader fault/whole-release gates remain open.

**Earlier lifecycle/snapshot attempt (superseded, 2026-09-28):** Two prepared, uncommitted owner-scoped QA deltas collected 13 cases in
only `mvmctl-030-test`. The first pause/resume case failed: the same Firecracker task resumed, but its new vsock
connection timed out waiting for Firecracker's `CONNECT` acknowledgement. The strict report
`/home/runner/mvmctl-030-ci-307e09d2/.tmp/worktrees/ci-cb0848ef/.tmp/qa-lifecycle-snapshot-jail-search.json`
records zero passes, one failure, and twelve unexecuted cases classified as errors by the outcome hook. A separate
small SSH-enabled diagnostic reproduced the failure with unchanged guest boot ID and agent PID; SSH remained usable.
Candidate SHA-256 is `f4d43b5cefe85f533677f685a87a8efce2319df277cf41cdcc91e27d09adbde5`, with Go source matching
`a1965a4d`. The defect matches [upstream #6100](https://github.com/firecracker-microvm/firecracker/pull/6100);
[Firecracker 1.16.2](https://github.com/firecracker-microvm/firecracker/releases/tag/v1.16.2) fixes it but changes
snapshot compatibility. The user approved updating the locked pair to 1.16.2; the corrected affected gate above passed.
No lifecycle or snapshot pass is claimed for this earlier attempt. The diagnostic VM/network were removed; all four inventories were empty. The
separate jail-sealing access/partial-cleanup regressions passed after reproducing old-code failures, along with
focused race checks and the complete root-effect Jailer Go package. Unpublished-capture interruption remains
unqualified; a transient progress-file observation is not a deterministic mid-copy fault checkpoint.

**Resource/config audit checkpoint (2026-09-28):** `TestVMConfigOptions` replaces 22 stale, duplicate, or shallow
probes with 15 installed-candidate cases. They observe guest CPU/RAM/root-disk size, custom boot arguments, both PCI
transports, logging and metrics enable/disable effects, configured/CLI CPU precedence with exact restoration, and
five typed invalid-resource refusals with unchanged VM inventory. Each VM is small, networkless, function-owned,
and removed through checked exact-name cleanup. The improved assertions exposed inactive logger intent and a
Jailer credential-observation startup race; both were fixed without weakening root admission. The complete group
passed 15/15 in only `mvmctl-030-test` against candidate SHA-256
`7a7121519c2984080eb8c464bb58f085b43ff5a54c5409120b8bc53f854528c2`; strict report
`/home/runner/mvmctl-030-ci-307e09d2/.tmp/worktrees/qa-crash-5b23766f/.tmp/qa-resource-config-0928-transition.json`
records zero other outcomes. Independent VM/network/volume/snapshot inventories were empty. The retired default-kernel
prefix probe could pass if its selector were ignored; the distinct imported-kernel/stopped-dependency case owns
explicit full-ID selection, not independent kernel-prefix resolution. The remaining lifecycle module and full matrix
are not qualified by this group.

**Kernel-import runner checkpoint (2026-09-27):** The complete eight-case
`tests/system/kernel/test_kernel_import.py` module passed against the installed 0.3.0 candidate in only
`mvmctl-030-test`. The strict report at
`/home/runner/mvmctl-030-ci-307e09d2/.tmp/worktrees/ci-856bf013/.tmp/qa-kernel-import-856bf013.json`
records eight collected/passed and zero failures, errors, skips, or deselections. VM and network inventories were
empty afterward, and only the pre-existing default kernel remained. This is local kernel-import evidence, not the
separate kernel-build domain, the full Python matrix, GitHub CI, or release approval.

**Stopped-VM kernel dependency checkpoint (2026-09-28):** The owner-scoped
`TestKernelStoppedVMDeletion::test_delete_kernel_used_by_stopped_vm_is_denied` case passed 1/1 with zero other
outcomes against the installed candidate in only the bridge-capable named runner. It imported a unique kernel,
created and stopped a VM using it, required `kernel.in_use` on ordinary removal, and verified both records remained.
Independent VM/network inventories were empty afterward, no test-owned kernel remained, and the original default
kernel remained. This does not qualify forced removal of a referenced kernel or the full kernel module.

**Listing/inspection audit checkpoint (2026-09-28):** Commit `abbac386` consolidates duplicate and vacuous
listing/inspection probes; a follow-up extends the same inspect owner to name, IPv4, and ID-prefix selection.
The final ten cases cover read-only empty JSON listing, ls/ps JSON, actual ls/ps text, inspect JSON/text, and
structured missing-VM console rejection. They share one explicitly small test-owned VM without unnecessary SSH
setup. All ten collected and passed in only `mvmctl-030-test`; strict report
`/home/runner/mvmctl-030-ci-307e09d2/.tmp/worktrees/qa-crash-5b23766f/.tmp/qa-list-inspect-selectors-0928.json`
records zero other outcomes. Independent VM/network inventories were empty. This qualifies that group, not the
remaining lifecycle cases. The empty-list case no longer attempts global VM deletion.

**Later saturation qualification attempt (2026-09-28):** A prepared, uncommitted cgroup QA draft observed the
swap-absent guest hold 85.04–85.05% of usable RAM for 15.34 seconds, with unchanged guest/process identity, working
fresh exec probes, historical memory peak above `memory.high`, increased high events, and unchanged visible OOM
counters. Current usage could fall below high after successful reclaim; remaining above high is not a valid survival
oracle. The swap-present setup eventually activated a test-owned, hole-free 512 MiB backing file, but the held guest's
availability moved to 33044 KiB, above the draft's fixed 32 MiB target band; sustained swap-present qualification did
not pass. A separate two-case existing-cgroup selection passed protected inspection but failed its pressure test:
a real 150 MiB guest allocation increased charge from 45305856 to 231022592 bytes, below high 234881024, so it did
not produce the required high event. No VM kill was observed in these failures. All four resource inventories were
empty, the temporary swap was inactive and removed, and configuration restoration completed.
Strict reports `qa-memory-peak-oracle-7073592b.json`, `qa-memory-swap-capacity-correction-7073592b.json`, and
`qa-cgroup-existing-7073592b.json` remain in the runner's `qa-consistency-7073592b/.tmp` directory; the first two also
have retained JUnit measurements. Small evidence copies are preserved in ignored local `.tmp/qa-evidence-20260928/`.
The draft was removed from the integration checkout only after exact parity with its persistent isolated worktree;
it is not committed or release-qualified. Retries stopped at the workload-design dependency. A revised oracle must
establish actual pressure despite boot/cache/reclaim variation without mistaking normal reclaim or auxiliary target
drift for a product failure. Guest allocation failure, original-incident attribution, ancestor/global OOM, and broader
fault/release qualification remain open.

**Earlier bounded pressure checkpoint (2026-09-28):** The existing
`TestVMCgroupEnvelope::test_guest_stays_responsive_under_pressure_only_memory_control` passed 1/1 against the
SHA-matched installed candidate after one reboot of only the empty named runner. Its real 150 MiB tmpfs load
increased observed memory charge and `memory.events.high`, left leaf `oom_kill` unchanged, and retained vsock exec.
Strict report `.tmp/qa-pressure-0928.json` is in the runner's `qa-crash-5b23766f` worktree. All four VM/network/volume/
snapshot inventories were empty afterward; the outer controller's max/OOM counters stayed zero after reboot.
The full current-candidate cgroup file was not rerun because remaining outer headroom was below the next case's
configured envelope. This bounded pass does not establish deterministic pressure across environments, resolve the
historical larger-allocation guest-agent EOF, or qualify sustained/ancestor-OOM survival.

**Confirmed-crash recovery checkpoint (2026-09-28):** Commit `a1430018` replaces the obsolete CLI `kill`
probe with one exact PID/start-tick/jail/executable-verified pidfd SIGKILL of a test-owned Firecracker inside only
`mvmctl-030-test`. The test waits for that retained pidfd to prove exit before invoking public status, then requires
`crashed` from `vm ls`/inspect, absence from `vm ps`, typed stop/start, successful vsock exec, and checked VM/network
removal. The installed candidate passed 1/1; strict report
`/home/runner/mvmctl-030-ci-307e09d2/.tmp/worktrees/qa-crash-5b23766f/.tmp/qa-crash-0928-exitproof.json`
records zero failures, errors, skips, or deselections. Independent VM/network lists were empty. This is not
guest-initiated poweroff, complete host-reboot qualification, or the whole lifecycle module. No production behavior
changed: an incomplete identity read before pidfd-proven exit remains unavailable under ADR-0016.

**Complete owned rootfs persistence repeat gate (2026-09-28):** Commit `c02b7749` retains the one
copy → full capture → source mutation/removal → independent restore journey while binding cleanup to
full VM and snapshot IDs, creation receipts and the pre-effect snapshot request handle. Read-only
preseed admission replaces shared asset preparation; partial publication cleanup and configuration/resource
baseline checks are explicit. The complete one-case file at SHA-256
`a644faa53d082433e5331b01f51e5d10b53abcb09c5353f52421130db68dcadb` passed twice in only
`mvmctl-030-test` against installed candidate `e23b1261` (15.80s and 16.76s), with zero other outcomes,
exact configuration restoration and empty VM/network/volume/snapshot inventories. Reports are
`qa-persistence-auto-a644-full1-pass{1,2}.{json,xml}`. This closes this file's repeat gate, not
interrupted capture, power-loss recovery, data-volume fault consistency or release qualification.

**Rootfs persistence runner checkpoint (2026-09-27):** The old two-case module copied a stopped VM's rootfs from a
caller-visible path and imported it as an image; the first case never created or restored a snapshot despite its name.
Commit `2772d7b5` replaces them with one function-scoped installed-candidate journey using `mvm cp`, full snapshot
capture, post-capture source mutation/removal, `snapshot restore --resume`, and guest readback. The strict report at
`/home/runner/mvmctl-030-ci-307e09d2/.tmp/worktrees/qa-rootfs-856bf013/.tmp/qa-rootfs-persistence-draft2.json`
records one collected/passed and zero failures, errors, skips, or deselections; VM and snapshot inventories were empty
afterward. This proves one networkless rootfs persistence/independence path, not the full snapshot fault matrix,
GitHub CI, or release approval.

**Managed snapshot DR checkpoint (2026-09-27):** Commit `dfeecd4d` replaces the permissive fallback in
`TestSnapshotDestroyRestore::test_snapshot_destroy_restore_workflow` with one bounded same-network journey: create,
guest exec, full capture, source removal, `restore --resume`, retained network/IP, guest exec, and exact teardown.
The installed candidate in only `mvmctl-030-test` passed this one case with a strict zero-skip report at
`/home/runner/mvmctl-030-ci-307e09d2/.tmp/worktrees/qa-snapshot-dr-856bf013/.tmp/qa-snapshot-dr-final.json`;
VM, network, and snapshot lists were empty afterward. This is not the complete snapshot module or fault matrix.
The original unbounded-default-size case timed out after 90 seconds: root had published a 912 MiB capture, but
`snapshot ls` returned an empty list. The exact QA artifact was authenticated through
`snapshot inspect <full-id>` and removed through `snapshot rm <full-id> --force`, recovering runner disk space.
The operator-facing interruption/reconciliation path remains unqualified; a fresh unprivileged client may not know
the full ID. Do not treat the successful bounded case as closing that gap.

**Snapshot recovery-handle checkpoint (2026-09-27):** Commit `487b4a0d` emits the full request ID to stderr before
root capture, including `--json`, while leaving the closed root inspect/remove authority unchanged. The strengthened
managed DR case failed on the preceding installed candidate because JSON-mode stderr was empty, then passed 1/1 on
the new installed runner candidate (SHA-256
`11a432dc425ddf56f7b9695efeea085541e3948a435cadf0a40d3f49da900c27`), with strict zero-skip report
`/home/runner/mvmctl-030-ci-307e09d2/.tmp/worktrees/qa-snapshot-dr-856bf013/.tmp/qa-snapshot-handle-green.json`.
VM, network, and snapshot lists were empty afterward. The test proves emission on success. A separate bounded manual
probe in the same named runner killed the CLI after the full ID was emitted and root capture progress existed;
`mvm snapshot inspect <full-id>` then succeeded and typed `snapshot rm --force` removed the exact protected artifact.
The probe log is `.tmp/snapshot-interrupt-probe.log` in that runner QA worktree. This is not an automated Python
system-test case or a complete interruption fault matrix; if stderr is lost, a root-published capture remains
undiscoverable from `snapshot ls`.

**Full Jailer-file runner checkpoint (2026-09-27):** The official 1.16.0 checksum sidecar was reachable by a bounded
direct HTTPS probe, which changed the earlier external-metadata blocker; the archive remained present in the configured
runner mirror. The two formerly blocked tests then passed 2/2, and the entire 13-case `tests/system/vm/test_jailer.py`
file passed against the installed candidate with strict report
`/home/runner/mvmctl-030-ci-307e09d2/.tmp/worktrees/ci-856bf013/.tmp/qa-jailer-full-856bf013.json`.
It records 13 collected/passed and zero failures, errors, skips, or deselections. VM, network, volume, and snapshot
inventories were empty afterward; 1.16.1 remained the Firecracker/Jailer default. The separate
`tests/system/vm/test_cgroup.py` pressure case, host-reboot recovery, full Python matrix, and release sign-off remain
unqualified.

**Exact-source local checkpoint (2026-09-27):** The clean `307e09d2` source passed pinned Go 1.26.3 tidy,
format, generation, vet, the selected unprivileged Go CI suite with coverage, and the complete root-effect Jailer
package inside only `mvmctl-030-test`. Its release build was installed there through `host install-system`; the
installed and built SHA-256 both equal `7ad64afbc20b10a8ad7bbd1086ff3793dc2a257f478db9a19e75acecfdb89a3e`.
Against that installed candidate, the volume module passed 37/37, network/nftables/traffic-policy files passed 47/47,
and the exec domain passed 7/7, all with strict zero-skip/deselection reports and checked empty resource inventories.
CLI 24/24, config 12/12, keys 28/28, and internal-service entry points 5/5 also passed. The prior same-version
installed binary had a different checksum and failed same-network policy; its live ruleset lacked the inet companion.
The exact-source rebuild passed that case and the full network selection. Do not combine the prior binary's results
with exact-source evidence.

The old invariants module exposed bare-JSON and vacuous/duplicate cases; a bounded run was interrupted before a large
image extraction filled the disposable runner. Its ten retained non-destructive cross-domain JSON/default/flag cases
then passed 10/10 after the test-audit cleanup, while two destructive cache-clean safety cases remain unqualified and
must move to their owning cache domain. The host module passed 23/27, with four legacy/admin cases still failing for
unprivileged invocation or obsolete envelope parsing; no host reset/clean success is claimed. These are concrete QA
blockers, not evidence of a product regression. The host module has since been narrowed to read-only checks and two
mutation guards; destructive clean/reset success remains unqualified and requires a dedicated clean-host context.
Full T1/T2, host-reboot and saturation journeys, GitHub CI, dedicated
clean-host Tier 3, and release approval remain open.

Historical pressure failures (before the later passing checkpoints above and below): the cgroup module's
envelope/inspection case passed, but that candidate's pressure run did not qualify the pressure threshold. A 150 MiB guest
allocation did not reach `memory.high`; larger 156/168 MiB attempts lost the guest exec response. Instrumented
156 MiB readback still found the VMM running, `memory.max=max`, no cgroup high/OOM events, and usage just below
`memory.high`. This does not prove sustained-pressure survival or a host cgroup kill; the guest/agent EOF needs a
separate bounded diagnosis. A later 156 MiB probe made the fill process the preferred guest-OOM victim
(`oom_score_adj=1000`), yet exec still ended with EOF and the agent did not answer a later probe. Firecracker remained
running; VM-leaf usage was 232,345,600 bytes below `memory.high=234,881,024`, and `high`, `oom`, and `oom_kill` stayed
zero. No OOM line appeared in the last 250 boot-log lines. This excludes a VM-leaf cgroup kill in that probe, not guest
OOM or an agent/relay failure; the larger-allocation guest-agent EOF remains unexplained.
Two follow-up probes unlinked the tmpfs file before the writer ran, so its pages were not retained by that file after
the writer exited. At 156 MiB, the writer reported 156 records out but the CLI exited 255; at 160 MiB, it exited 255
without a completed-record report. The agent answered a subsequent exec in both cases, while VM-leaf `high` and
`oom_kill` remained zero. Persistent guest tmpfs pages may contribute to the earlier agent loss, but these probes do
not prove guest OOM or exercise the cgroup pressure threshold. No further fill-size retry is planned without new
instrumentation.

The init module initially passed 8/8 but left a default network behind. Removing that network exposed a real
same-second identity collision: a new network hashed the same name, subnet, and second-precision timestamp to a
retired root ID. A focused network-recreation L2 case failed on the old installed binary, then passed after
`57176124` adds a creation nonce without relaxing the root removal receipt. The scoped candidate built from the
`307e09d2` source plus that Go fix passed the affected API Go checks and was installed only inside the named runner
(built/installed SHA-256 `9f46313d4279eccf0282f855fd184fe3bf2924f03b7de7bf8ee65f8d03bd3dba`).
With `7ac34d78`'s function-scoped exact-network cleanup, all eight init cases collected and passed with zero other
outcomes; the runner network inventory was empty afterward. This is a focused fixed-candidate gate, not clean
current-HEAD Go CI or full release qualification.

At clean commit `15611a9a`, pinned Go 1.26.3 tidy/no tracked diff, gofmt, golines, agent generation/no tracked
diff, full vet, the selected unprivileged CI tests with coverage, and the complete separate root-effect Jailer
package passed inside the named runner. An exact-commit release build (SHA-256
`bd1096b4dd979dfef96f4b2d587fac5539b94fa177ac899bffb1ab1a1731a1ba`) was installed only there; the
installed checksum matched. Network/nftables/traffic-policy plus self-cleaning init then passed 56/56 from that
same clean source with a strict zero-skip/deselection report and empty VM, volume, network, and traffic-policy
inventories. GitHub CI, the other Python domains, memory saturation, host reboot, and Tier 3 remain open.

With that same installed binary and unchanged Go source, subsequent full Python modules passed: volume 37/37, both
exec files 7/7, SSH 9/9, logs 11/11, and console 5/5. Their strict reports are respectively
`qa-volume-exact-15611a9a.json`, `qa-exec-exact-15611a9a.json`, `qa-ssh-alpine-clean.json`,
`qa-logs-historical.json`, and `qa-console-exact.json` under the named runner's
`/home/runner/mvmctl-030-ci-307e09d2/.tmp/`; each recorded zero failures, errors, skips, and deselections.
SSH fixtures now use the mirrored Alpine image and clean up exact resources on setup failure. The logs test confirms
retained historical output is readable by IP after VM stop; its earlier “must fail” assertion accepted both success
and failure and was not a valid contract. Console's duplicate state case was removed and its stopped-state assertion
now checks the schema-1 VM listing. VM and network inventories were empty after those modules; the baseline default
key remained. These results do not qualify the unrun Python domains, reboot recovery, pressure-only memory behavior,
GitHub CI, Tier 3, or the release.

The same candidate also passed all 15 copy-domain cases with zero failures, errors, skips, or deselections in
`/home/runner/mvmctl-030-ci-307e09d2/.tmp/qa-cp-full.json`. The suite exercises host-to-VM, VM-to-host,
multi-source, and VM-to-VM transfer; its Alpine verification account now matches the fixture, its throwaway
paths are unique, and its two-guest case uses 256 MiB per VM on this 2 GiB runner. VM/network inventories were
empty afterward and the baseline key remained. This is local copy-domain evidence, not a full Python matrix.

**Host-reboot recovery remains unqualified (2026-09-27):** A 256 MiB managed VM answered `mvm exec` before the
named runner reboot. After reboot, the installed candidate listed it as stopped and `vm ps` was empty. A forced stop
succeeded, but `vm start` failed after Firecracker exited; the public command surfaced a process-identity
failure. The preserved failed Firecracker log shows EACCES opening jailed `/dev/kvm` to create the KVM object, **not**
the previously reported `/dev/net/tun` failure. The first attempt's log was not preserved, so the exact first-error
sequence and root cause remain unproven. A fresh VM on the same network passed create, exec, and same-boot restart.
A second controlled journey with another 256 MiB managed VM passed pre-reboot exec, prior-boot stopped projection,
`vm stop --force`, relaunch, and post-reboot exec. Its jailed `/dev/kvm` and `/dev/net/tun` had mode 0600 and matched
the isolated Firecracker UID/GID before reboot; the old jail was absent after stop. Both journeys used only the named
runner, and all diagnostic VMs/networks were removed through the installed CLI. The original device-open failure is
real but intermittent in these probes. A successful repeat is not a fix or full reboot-recovery sign-off; retain
per-attempt device evidence and add an orchestrated regression before qualification. No observed failure was a vsock
handshake failure.

**Bounded repeat (2026-09-28):** After clearing the named runner's prior cgroup pressure by rebooting only that
runner, a fresh 256 MiB managed VM answered `mvm exec` before its host reboot. The next boot projected it as
stopped with an empty `vm ps`; `vm stop --force` removed the old jail, and one `vm start` attempt plus post-start
`mvm exec` succeeded. The new jail's `/dev/kvm` and `/dev/net/tun` were mode 0600 and owned by the isolated
Firecracker UID/GID. VM, network, volume, snapshot, and both policy inventories were empty after typed cleanup;
the old jail was absent. This repeat did not reproduce or explain the earlier EACCES, and it is not an orchestrated
host-reboot regression or release qualification.

**Runner evidence (2026-09-27):** In the named `mvmctl-030-test` runner, pinned Go 1.26.3 passed the complete
unprivileged CI selection with both coverage profiles and the separate full root-effect Jailer package on clean
source commit `8e0be4fe`. The installed `mvm 0.3.0-rc.root-ready` candidate passed 58 CLI/config/read-only-host,
9 JSON/flag-invariant, 26 non-VM key, and 5 internal-service entry-point Python cases (98 total); the focused
networkless create/stop/start/reboot/vsock case, five focused raw-volume/removed-format cases, a networkless
raw-volume guest-attachment/restart case, an isolated non-root Firecracker UID check, a direct-API-denial with
authorized CLI stop check, and a `vm ps`/inspect stop-start projection case also passed. The
installed binary predates `8e0be4fe`, but the intervening source change under `internal/`, `pkg/`, and `cmd/` was
test-only.
These are scoped results, **not** a full Python matrix or release sign-off. At that checkpoint, the runner kernel still rejected
bridge-family nftables tables, blocking network/policy qualification there. Volume/snapshot consistency, host-reboot,
and full Jailer system journeys also remained unqualified at that checkpoint; one guest-visible volume attach was
not snapshot-consistency qualification. The later focused checkpoint below addresses only the networkless
data-volume snapshot journey, not the broader release gaps.

**Fresh-runner bridge and QA checkpoint (2026-09-27):** The original named runner was preserved as a verified
repo-local backup and replaced, with user approval, by a fresh `mvmctl-030-test` on the bridge-capable 7.0.11
kernel. A temporary `bridge` nftables table could be created and removed. Exact source commit `91de873f` passed
the pinned Go 1.26.3 unprivileged CI selection, formatting, generation, vet, and the full root-effect Jailer
package inside that runner. The source-built `0.3.0-rc.91de873f` candidate was installed only there; its
staged/installed SHA-256 matched. A 256 MiB managed-network VM launched with the isolated Firecracker UID,
reported policy-v2 cgroup limits, exposed `eth0` and answered vsock exec; checked VM/network teardown left both
lists empty. After Python test-source corrections in `7b2ca346` and `93cb6053`, full system-test collection
found 571 cases, and focused managed Jailer isolation and no-prompt network-create cases each passed 1/1.
This is not the full Python network/policy matrix, memory-saturation qualification, GitHub CI, or release sign-off.

**Prior-boot network and focused policy checkpoint (2026-09-27):** After an outer-runner OOM and reboot,
the installed candidate built with `371e285b` removed two exact prior-boot test networks without re-creating
their bridges. The affected Go network package, focused cleaned-versus-active topology test, vet, formatting,
and golines passed with pinned Go 1.26.3 in the named runner. Earlier installed-candidate runs passed four
fixed-nftables cases and three network JSON/removed-NAT-flag cases. The focused cross-network TCP traffic-policy
case then passed 1/1 against the `371e285b` candidate with a strict zero-skip report. It proved default deny,
one exact destination-port allow, adjacent-port deny, and deny after revocation through guest TCP probes. Its
two nested guests used 256 MiB each; VM, network, and traffic-policy inventories were empty afterward. The
runner's old outer controller had OOM-killed its Firecracker when an earlier version of the case used two
default-size guests; this is not evidence of a candidate cgroup kill. At that checkpoint, same-network/source-VM
policy, spoofing, rebooted live-network reconciliation, full Python QA, remote CI, and release sign-off remained open.

**Exact-HEAD local Go gate (2026-09-27):** A clean detached checkout of `02a4a690` in only the named runner passed
offline Go 1.26.3 tidy with no tracked diff, whole-tree gofmt, golines, agent generation with no tracked diff,
full vet, and `scripts/run-go-ci-tests.sh` including the selected unprivileged Jailer contracts. The complete
root-effect Jailer package separately passed under runner root. The generated coverage artifact was moved into
ignored repo-local `.tmp`, leaving the checkout with no tracked or untracked source changes. This is local Go
evidence, not GitHub CI or the complete installed-candidate Python release matrix.

**Complete network-domain checkpoint (2026-09-27):** Installed `mvm 0.3.0-rc.d0db3be6` in only the named
`mvmctl-030-test` runner passed all 47 tests in `tests/system/network/` (network lifecycle, fixed nftables, and
same-/cross-network TCP traffic-policy journeys). The schema-1 report at
`/home/runner/mvmctl-030-ci-613b0cc4/.tmp/qa-network-domain-6270e5dd.json` recorded 47 collected/passed and zero
failed, errors, collection errors, skips, deselections, XFAIL, or XPASS. Installed CLI readback found empty VM,
network, exec-policy, and traffic-policy inventories afterward. This qualifies this local domain run, not VM-source
or UDP grants, adversarial firewall recovery, prior-boot live-network reconciliation, the full Python matrix,
GitHub CI, or the release.

**Same-network traffic-policy checkpoint (2026-09-27):** A live nft trace in the named runner proved that a
same-network SYN, despite an exact bridge allow, also reached inet forward with both logical interfaces equal to
the managed bridge and hit its default drop while `net.bridge.bridge-nf-call-iptables=1`. Commit `1a6255f3`
adds exact inet companions without changing that host setting or the bridge gate. The affected Go firewall package
and vet passed in the runner. The installed `0.3.0-rc.brnf` candidate then passed the new same-network and existing
cross-network source-network TCP cases separately, each 1/1 with no other pytest outcomes. Both cases proved
pre-grant deny, exact port allow, adjacent port deny, and post-revoke deny; VM, network, policy, and nftables
inventories were empty afterward. VM-source, UDP, spoof/reverse, reused-IP, reboot, and full-matrix qualification
remain open.

**Current CLI/config checkpoint (2026-09-27):** The installed `0.3.0-rc.exec-port` candidate in only
`mvmctl-030-test` passed the complete 24-case CLI module and 12-case config module from clean source commit
`0e315b04`, with zero skips. Both used the established isolated 0.3 runner cache; the runner's default cache is
an archived pre-0.3 database and correctly fails clean-install admission for non-exempt commands. The config
module restored its tested VM CPU and memory defaults. A combined runner-local schema-1 pytest report recorded
36 collected and passed, with zero errors, skips, deselections, XFAIL, or XPASS. This is direct focused evidence,
not an orchestrator domain run, full Python qualification, or a claim about the archived cache's compatibility.

**Current trusted-binary checkpoint (2026-09-27):** Against the same installed candidate and isolated runner cache,
the complete `bin/test_bin.py` module passed 11/11. Its runner-local schema-1 report recorded zero failures,
errors, collection errors, skips, deselections, XFAIL, or XPASS. The module includes malformed/retired selector
rejection, absent-slot no-op, remote metadata limiting, present 1.16.1 re-admission, mirrored absent 1.15.1
pull/default-transfer/checked removal, and a live networkless VM denying release removal while identical force-pull
preserved the active pair. Final readback showed no nested VM and only the original 1.16.0 and 1.16.1 pairs, with
1.16.1 still the Firecracker/Jailer default. This direct full-module pass is not an orchestrator domain run, proof
of 1.16.0 VM launch support, or full Python release qualification.

**Current networkless Jailer checkpoint (2026-09-27):** The complete eight-case
`TestNetworklessJailerLifecycle` class passed against installed `0.3.0-rc.exec-port` in the named runner. Its
schema-1 report recorded eight collected/passed and zero failures, errors, skips, or deselections. It covered
operator denial on the selected trusted release and Firecracker API, isolated VMM UID, public status/stop/start,
raw data-volume attachment, independent data-volume snapshot restore, missing-release restore cleanup, and
create/stop/start/VM-reboot vsock execution. Independent `vm ls --json`, `volume ls --json`, and `snapshot ls --json`
were empty afterward, and the original 1.16.1 pair remained default. This is direct class evidence, not a full
Jailer module/orchestrator domain pass, host-reboot reconciliation, 1.16.0 launch proof, or networked Jailer QA.

**Default deblob checkpoint (2026-09-27):** A new focused
`TestNetworklessJailerLifecycle::test_default_deblob_provisions_bootable_guest` case passed 1/1 against that
installed candidate in only `mvmctl-030-test`, with no skips or errors. It created an Alpine 3.23 VM without
`--skip-deblob`, observed a running guest through the public CLI, then read the guest's cloud-init disable marker
and Alpine OpenRC parallel-service setting through vsock. The test-owned VM was removed and the isolated VM list
was empty. This qualifies that one root-brokered default provisioning path, not all distro-specific deblob rules,
failure rollback, managed networking, or the full LoopMount system matrix.

**Volume-only checkpoint (2026-09-27):** Against the same installed candidate and isolated cache, all 25 selected
`TestVolumeLifecycle` cases passed with zero failures, errors, or skips. One managed-network VM-attachment case was
explicitly deselected because the runner kernel lacks bridge-family nftables; the schema-1 report records that
deselection. The selected cases covered raw creation and options, inspect/list, duplicate/invalid-size and removed
format rejection, resize/shrink behavior, removal, and partial removal. The isolated volume list was empty afterward.
This is not a full volume module, guest-visible option enforcement, or networked attach/detach qualification.

**Volume attach/detach checkpoint (2026-09-27):** The previously stale
`TestVolumeLifecycle::test_volume_invariants_available_attached_cycle` was corrected for schema-1 JSON and the
root-protected backing contract, then passed 1/1 against the installed candidate in `mvmctl-030-test`. A 16 MiB
raw volume moved available → attached → available around a networkless VM stop/start; the guest saw `vdb`, and
inspect retained the same volume ID and logical size. After launch, inspect correctly redacted the former caller
path and reported protected backing. The strict report had no skips/errors, and independent VM/volume inventories
were empty after teardown. This is one stopped-VM attachment cycle, not live hotplug or managed-network QA.

**QCOW2 image boot checkpoint (2026-09-27):** The existing `TestImageImportAdvanced::test_image_import_with_format_qcow2`
case was strengthened and passed 1/1 against the installed candidate in `mvmctl-030-test`. It converted the cached
Alpine raw image to QCOW2, imported it as an ext4 image, launched a networkless VM from that import, and read
`ID=alpine` inside the running guest through vsock. The strict report had no skips or errors; independent readback
found no VM, imported image, or test-owned conversion file afterward. This qualifies one QCOW2 image conversion
and boot path, not QCOW2 data-volume attachment or all image import formats and failure paths.

**Networkless copy checkpoint (2026-09-27):** A focused
`TestCpNetworklessRoundTrip::test_host_to_vm_and_back_without_network` case passed 1/1 against the installed
candidate in `mvmctl-030-test`, with no skips or errors. The installed CLI copied unique bytes from runner-local
repo scratch into a running networkless VM, guest vsock exec read the same bytes, and `mvm cp` copied them back
to the runner. Checked teardown left no VM or scratch directory. This qualifies one host↔VM file round-trip,
not directory/multi-source copy, VM↔VM copy, or traffic/exec policy across managed networks.

**Current cgroup checkpoint (2026-09-27):** The installed `0.3.0-rc.exec-port` candidate passed the complete
two-case `vm/test_cgroup.py` module in the named runner. Its schema-1 report recorded two collected/passed and
zero failures, errors, skips, or deselections. The cases checked root-protected pressure-only controls and
guest responsiveness after a 150 MiB tmpfs allocation. A strengthened same-owner case then passed 2/2 again while
proving actual `memory.high` events increased and leaf `oom_kill` did not. Independent readback found no nested VM
and the original 128 MiB VMM headroom restored. This does not prove sustained guest-memory saturation, absence of
host OOM under system-wide pressure, or host swap reclamation.

**Focused snapshot checkpoint (2026-09-27):** An installed `0.3.0-rc.snapshot-ref-fix` candidate inside only
`mvmctl-030-test` passed `TestNetworklessJailerLifecycle::test_networkless_snapshot_restores_independent_data_volume`
(1/1). It wrote a raw attached guest volume, captured a full networkless snapshot, changed and removed the source
volume/VM, restored an independent volume without an IP/MAC endpoint, compared the guest-visible bytes, ran vsock exec,
and checked VM/volume/snapshot teardown. A later run also rejected a managed-network override before creating a target
VM. Focused Go volume and full root-effect Jailer packages plus changed-package vet
passed in that runner. This does not qualify managed-network snapshots, concurrent writers, crash recovery, the full
Python matrix, or the release.

**Restore-failure regression (2026-09-27):** The single
`TestNetworklessJailerLifecycle::test_restore_missing_release_leaves_no_vm_projection` case passed against the
installed `0.3.0-rc.snapshot-receipt` candidate inside only `mvmctl-030-test`. It captured a networkless VM,
removed its source VM, made the captured release unavailable through the normal root-authorized `bin rm` route,
and proved restore returned an error with no target projection. Snapshot removal succeeded, proving no protected
restore reference remained. The case failed against the preceding `0.3.0-rc.snapshot-ref-fix` candidate for the
intended `partial` response and orphan VM without root authority; that exact test-owned orphan and snapshot were
removed after root/process/reference checks. The mirrored release and both original defaults were restored, the
current candidate was reinstalled, and the private VM/snapshot lists were empty. This is one pre-spawn failure
path, not post-spawn cleanup, transport-uncertainty, or full snapshot-domain qualification.

**Protected cgroup-inspection checkpoint (2026-09-27):** Commit `89c5137e` replaces the unprivileged direct cgroup
reader with an authenticated, fixed-path, descriptor-pinned root observation and removes the obsolete reader. Pinned
Go 1.26.3 affected-package tests, full root Jailer tests, vet, formatting, and one-pass read-only security review
passed in `mvmctl-030-test`. The runner-only installed `0.3.0-rc.cgroup-observe` candidate returned `enforced` with
matching policy-v2 limits and usage for a running networkless VM, and the updated `vm/test_cgroup.py` case passed 1/1
across create, operator-denied direct leaf read, root-brokered inspection, stop, restart, and checked removal. The first
test attempt failed on runner disk exhaustion during restart; clearing only its rebuildable Go build cache raised free
space from 419 MiB to 4.0 GiB, and the unchanged case then passed. This does not qualify memory saturation survival,
full Jailer system journeys, or the bridge-dependent network/policy matrix.

The quick candidate omitted the guest-agent embed. It was replaced inside only the runner through `host install-system`
with `0.3.0-rc.cgroup-full`, built by the repository release script under pinned Go 1.26.3. Against that full candidate,
the cgroup case and the existing networkless create/restart/reboot/vsock-exec case each passed 1/1, with no nested VM
left in the isolated cache. The outer controller was unchanged.

**Current-source Go CI (2026-09-27):** An isolated checkout of `b430a359` inside `mvmctl-030-test` passed the
repository CI sequence with pinned Go 1.26.3: tidy with no tracked diff, architecture check, gofmt and golines,
agent generation with no tracked diff, vet, every selected unprivileged package, the Jailer CI contracts, and both
atomic coverage profiles. The full root-effect Jailer package had already passed against the same production source
after `89c5137e`. This closes the local current-source Go CI gate, not the Python matrix, memory-saturation diagnosis,
bridge-dependent network qualification, or remote GitHub CI.

**Later exact-commit and pressure checkpoint (2026-09-27):** Clean runner worktree `fb3b967f` passed the same pinned
Go CI gates plus the separate full root-effect Jailer package. The installed `0.3.0-rc.snapshot-receipt` candidate
passed all seven `TestNetworklessJailerLifecycle` cases and both `TestVMCgroupEnvelope` cases against current Python
source. The new cgroup case checks that a real 150 MiB guest tmpfs allocation increases the root-brokered observed VM
memory charge by at least 96 MiB and leaves VM inspection and guest vsock exec responsive with no finite VM-leaf
memory maximum. In a separate bounded manual probe with zero VMM headroom, the same workload raised the leaf's
`memory.events.high` from 0 to 35 with `oom_kill=0`; the unprivileged Python test does not directly read root-only
event counters. The private runner VM/volume/snapshot lists were empty and the original headroom default was restored.
This does not qualify long-duration or host/ancestor OOM survival, the original reported kill, the complete Python
matrix, bridge-dependent policy, or remote CI. Matching Firecracker/Jailer 1.16.0 failed a networkless launch because
Jailer rejected the launcher's `/proc/self/ns/net` argument as a symbolic-link loop; runner provisioning still pins
1.16.0 in this historical run. The 0.3.0 contract now requires 1.16.1, and source now gates VM launch and runner
provisioning to that pair; the focused installed-candidate check below does not replace orchestrator qualification.

**Firecracker 1.16.1 launch-gate checkpoint (2026-09-27):** A full-agent
`0.3.0-rc.version-gate` candidate was installed through `host install-system` only in `mvmctl-030-test`; staged and
installed SHA-256 matched. A new installed-CLI L2 case first failed against the previous candidate with
`vm.create_failure`, then passed against the gated candidate: selecting a present 1.16.0 pair and creating a plain
networkless/no-PCI VM returned schema-1 `binary.version.gate` before a VM projection appeared. It restored both
1.16.1 defaults. A separate existing case successfully launched a 1.16.1 networkless VM and proved the operator
could not traverse its selected trusted release files. Each focused schema-1 report recorded one pass and zero
failure, error, skip, or deselection; independent readback found no VM afterward. This is not managed-network
qualification, all lifecycle routes, the complete Python matrix, or orchestrator/remote CI sign-off.

**Exact neighbor/current-candidate checkpoint (2026-09-27):** Clean source `659c8a51` passed offline tidy and
generation with no tracked diff, gofmt, golines, full vet, the unprivileged Go CI selection, and the complete
root-effect Jailer package inside only `mvmctl-030-test`. Its full-agent `0.3.0-rc.neighbor` build was installed
through `host install-system` only there, with matching built/installed SHA-256. Five existing networkless Python
cases passed against that candidate: reboot/vsock, status stop/start, direct API denial, raw-volume restart, and
consistent data-volume snapshot restore. The private VM, volume, and snapshot inventories were empty afterward.
Both existing cgroup cases also passed (2/2), proving protected inspection and a bounded charged guest allocation
with continued vsock responsiveness; the private VM list was empty afterward.
This is focused evidence, not the full Python matrix or reused-IP/network-policy qualification; the runner kernel
still lacks nftables bridge-family support. Sustained memory saturation and host/ancestor OOM survival remain open.

**Additional current-candidate QA (2026-09-27):** The same installed `0.3.0-rc.neighbor` passed 97 bounded
CLI/config/JSON-invariant/internal-entry/host-info/non-VM-key cases, two more networkless security cases proving
protected release files and isolated Firecracker UID, and two raw-volume grow/shrink cases. Commit `03e89ad3`
corrected the volume list/inspect tests to parse the schema-1 success envelope and removed their weaker duplicate
cases; both corrected cases passed and the volume module collected 36 cases. The private test-cache VM and volume
inventories were empty after checked teardown. These additions bring bounded current-candidate evidence to 110
distinct passing Python cases, not a full volume module, full system matrix, or release sign-off.

**Volume admission and rollback checkpoint (2026-09-27):** A later `0.3.0-rc.volume-rollback` build containing
the zero-capacity validation and pre-publication allocation-failure cleanup was installed only in `mvmctl-030-test`;
its staged and installed SHA-256 matched. The input regression failed before the fix for `0M` and a fractional size
rounded to zero, then the full input package, full core-volume package, and focused vet passed under pinned Go 1.26.3
in that runner; focused format checks passed as well. A separate core-volume regression failed before cleanup because
an allocation error left the just-created file, then passed after the fix. The installed candidate passed all 25 non-VM
`TestVolumeLifecycle` cases, including a schema-1 `validation.failed` response for `0M`, no new backing or record,
and checked test-owned teardown. One network/VM attachment case was explicitly deselected because the runner lacks
bridge-family nftables; at the time it also used the removed `network create --non-interactive` flag. Commit
`7b2ca346` corrected that test source, but the attachment case has not been rerun. The module collected
36 cases. Read-only inspection of the private cache after the pre-fix attempts showed two zero-length raw files
without volume rows; their cleanup/provenance is not qualified by the 25-case pass. This scoped build used copied
changed source, not a clean exact-HEAD Go CI run, full volume module, or release sign-off.

**Host-to-guest exec checkpoint (2026-09-27):** The runner-only installed `0.3.0-rc.exec-port` candidate was built
with the negative/overflow vsock-port input guard and its staged/installed SHA-256 matched. The six retained exec
cases use a checked networkless VM fixture and the exact installed CLI; interactive shell runs under a real PTY.
The negative-port case failed on the preceding installed candidate because it executed a guest `touch` command,
then passed on the fixed candidate while proving that neither negative nor overflowing ports left guest files. The
complete six-case module passed twice (6/6 each run), and separate private-cache `vm ls --json` and `vm ps --json`
were empty afterward.
The duplicate timeout-success and empty-interactive-input cases were removed: all captured-exec cases pass explicit
`--timeout 10`, while the retained interactive case covers the no-command route. This qualifies only host-to-guest
exec in the named runner, not typed VM-to-VM policy, managed-network placement, host-reboot recovery, or the full
Python matrix. A separate clean checkout at exact commit `0e315b04` subsequently passed the pinned offline Go CI
sequence and the complete root-authority Jailer package in the named runner; remote CI remains unqualified.

| v0.3.0 security capability | Status | Required release evidence |
|---|---|---|
| Installed system candidate and clean bootstrap | 🟡 Partial | Installed e034 candidate passed the clean-child enrollment/marker-only/reset/re-init and separate multioperator/active-network-refusal journeys. Current f909 candidate additionally passed real read-only first-init/reset failure, retries, same-ID pending-network activation and exact cleanup. Abrupt interruption, ambiguous-runtime recovery and consolidated current-domain qualification remain separate. |
| fd-0 privileged control transport | 🔴 Missing | Go L1 framing/EOF/peer/cancellation abuse coverage plus installed-CLI lifecycle integration. |
| Root VM authority and cross-UID denial | 🟡 Partial | A focused installed-candidate networkless case denies operator direct connection to the reported Firecracker API socket while `vm stop` succeeds; root-record/process/cache identity checks, constrained two-user denial, tombstone, and zero-leak assertions remain missing. |
| Independent trusted Firecracker/Jailer releases | 🔴 Missing | Root manifest/ELF/hash authority, atomic failure injection, and installed-CLI release lifecycle. |
| Descriptor-pinned resources and private mount namespace | 🔴 Missing | Type/owner/mode/link checks, replacement races, namespace mounts, cleanup, restart, and restore. |
| Mandatory per-VM network namespace | 🔴 Missing | Distinct namespace inodes/topology, root-record binding, rollback, reconciliation, and zero leaked links/TAPs. |
| nftables-only engine | 🟡 Partial | Four installed-candidate fixed-table cases passed in the bridge-capable named runner, covering modeled inet/bridge rules, nested-VM rule lifecycle, repeat sync, and NAT/conntrack readback. Full failure/reconciliation and clean-host qualification remain open. |
| Reused-IP bridge neighbor invalidation | 🟡 Partial | ADR-0021's owner-bound exact-IP rtnetlink operation is composed before host-veth exposure. Focused Go tests, a disposable real-kernel namespace probe, and the installed-candidate A-to-B stale-IP reuse journey with a concurrent control VM passed. The journey proves fresh gateway reachability and exact cleanup, but guest boot ARP prevents independent attribution of the pre-exposure ordering; spoofing and prior-host-boot recovery remain unqualified. |
| Typed `traffic` policy | 🟡 Partial | Installed-candidate same-/cross-network source-network TCP cases passed default deny, exact port allow, adjacent-port deny, and revoke. The 2026-09-28 acknowledged same-network source-VM UDP journey also passed exact allow, sibling/adjacent-port/reverse denial and fresh-flow revocation. Spoofing, IP reuse, cross-network UDP and reboot recovery remain unqualified. |
| Typed VM-to-VM `exec` policy | 🟡 Partial | Current f909 candidate has passes for all six registered VM-to-VM exec nodes across three bound invocations: directional same/cross/networkless placement, retained grants after endpoint restart/destination reboot, exact opt-in/nonroot user, streaming/redacted audit, interruption/destination-group cleanup and restored-endpoint/new-grant behavior. Complete private-backend cleanup, further peer/capacity/deadline/cancellation faults and host reboot remain unqualified; these scoped passes are not full release sign-off. |
| Cgroup/process identity lifecycle | 🟡 Partial | Isolated credentials and protected policy-v2 inspection are qualified locally. The three-case measured-pressure module passed twice: both swap variants held at least 80% usable guest-RAM occupancy for at least 15 seconds with fresh high events, unchanged task/boot identity and responsive exec. Historical guest-agent EOF/original incident, broader exact-identity cleanup and host-reboot binding remain unresolved; the bounded hold is not an ancestor/global-OOM guarantee. |
| Crash reconciliation and repeated zero-leak runs | 🔴 Missing | Every applicable domain twice with aggregated teardown and absence checks after failure and reboot. |

Specific legacy conflicts that must be rewritten before release:

- `network/test_policies.py` now covers same-network and cross-network source-network TCP grants; its former
  dual-backend and same-network rejection assumptions were removed. Additional typed policy variants remain open.
- `full_journeys/test_full_journeys.py` now proves same-network default denial, an exact source-VM TCP
  grant, echoed delivery and revocation. Its complete fifteen-case domain passed twice; additional policy
  variants and reboot/fault recovery remain separate gates.
- `exec/test_exec.py` covers host-to-VM execution through the exact installed CLI and a networkless fixture.
  `exec/test_vm_to_vm_exec.py` covers the positive and directional-denial VM-to-VM paths. Its current retained-CLI
  interruption boundary passed; other adversarial authority/cancellation, audit and reboot/restore boundaries
  remain separate release gates.
- `vm/test_jailer.py` observes process/chroot outcomes through user-visible state but does not qualify root authority or
  the fd-0 transport.
- Shared cleanup and several test-local teardowns still suppress errors or mutate host state with broad raw sudo.
- The old `run provision` help case was a false positive: Cobra displayed parent help for the removed route. The current
  entry-point test checks the public command listing for its absence and verifies active services reject missing identity.

## Table of Contents

- [v0.3.0 release-security qualification overlay](#v030-release-security-qualification-overlay)
- [Root CLI](#root-cli)
- [mvm self-update](#mvm-self-update)
- [mvm init](#mvm-init)
- [mvm config](#mvm-config)
- [mvm network](#mvm-network)
- [mvm vm](#mvm-vm)
- [mvm exec](#mvm-exec)
- [mvm snapshot](#mvm-snapshot-alias-ss)
- [mvm volume](#mvm-volume)
- [mvm key](#mvm-key)
- [mvm image](#mvm-image)
- [mvm kernel](#mvm-kernel)
- [mvm bin](#mvm-bin)
- [mvm ssh](#mvm-ssh)
- [mvm console](#mvm-console)
- [mvm logs](#mvm-logs)
- [mvm host](#mvm-host)
- [mvm cache](#mvm-cache)
- [mvm cp](#mvm-cp)
- [mvm env](#mvm-env)
- [mvm run](#mvm-run-internal-service-subprocesses)
- [mvm full-journeys](#mvm-full-journeys-end-to-end-workflows)
- [Summary Statistics](#summary-statistics)

---

## Root CLI

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `--version` | ⚡ Shallow | `cli/test_cli.py` | `TestVersionFlag` | Check: non-empty string with digits. |
| `--verbose` | ⚡ Shallow | `cli/test_cli.py` | `TestDebugFlagOutput` | Check: config get still works. |
| `--debug` | ⚡ Shallow | `cli/test_cli.py` | `TestDebugFlagOutput` | Check: stderr has DEBUG. | 
| `help` | ⚡ Shallow | `cli/test_cli.py` | `TestHelpCommand` | root, subcommand, subsubcommand, nonexistent, version |
| `help` (consistency) | ⚡ Shallow | `cli/test_cli.py` | `TestHelpOutputConsistentFormat`, `TestHelpSubcommandShowsCorrectly`, `TestHelpOutputShowsSubcommands` | Check: Usage:, Commands:, --help in every group, subcommands listed |
| `version` (command) | ⚡ Shallow | `cli/test_cli.py` | `TestHelpCommand` | L1: stdout has version-like content with digits |
| `completion bash\|zsh\|fish\|powershell` | ⚡ Shallow | `cli/test_cli.py` | `TestHelpCommand` | L1: stdout contains shell completion definitions |

---

## `mvm self-update`

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `self-update` (user-owned artifact) | 🔴 Missing | — | — | Success/update behavior for a user-owned executable is not covered at L2. |
| `self-update check` | 🔴 Missing | — | — | Release-check output is not covered at L2. |
| `self-update apply` (user-owned artifact) | 🔴 Missing | — | — | Successful replacement of a user-owned executable is not covered at L2. |
| `self-update apply --force` (system installation) | ✅ Deep | `host/test_system_install.py` | `TestSystemBinaryInstall` | L2 seeds the existing HTTP cache with a newer release, invokes exact `/usr/local/bin/mvm`, verifies refusal before update download, and proves the system-binary hash is unchanged. The cache entry is restored or removed exactly. |

---

## `mvm init`

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `init --non-interactive` (full) | ⚡ Shallow | `init/test_init.py` | `TestInitWizard` | Inside runner VM, no --skip-host |
| `init --non-interactive` (no skip) | ⚡ Shallow | `init/test_init.py` | `TestInitWizard` | Checks non-zero + sudo mention |
| `init` idempotent | ⚡ Shallow | `init/test_init.py` | `TestInitWizard` | Two runs succeed |
| `init --skip-network` | ⚡ Shallow | `init/test_init.py` | `TestInitEdgeCases` | L1: exit 0 with success message |
| `init --skip-host` | 🟡 Partial | `init/test_init.py` | `TestInitWizard` | Skips host-level init inside runner VM. Used by most tests. |

---

## `mvm config`

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `config get <cat> <key>` (existing) | ⚡ Shallow | `config/test_config.py` | `TestConfigLifecycle` | L2 — exact schema-1 scalar agrees with inventory and text output. |
| `config get <cat>` (category only) | ⚡ Shallow | `config/test_config.py` | `TestConfigEdgeCases` | L2 — exact category metadata agrees with complete inventory. |
| `config get <cat> <nonexistent>` | ⚡ Shallow | `config/test_config.py` | `TestConfigEdgeCasesExtended` | L2 — nonzero schema-1 error and unchanged inventory. |
| `config set <cat> <key> <val>` then get | ⚡ Shallow | `config/test_config.py` | `TestConfigLifecycle` | L2 — exact integer override/readback, exact prior override restored. |
| `config set` invalid category | ⚡ Shallow | `config/test_config.py` | `TestConfigEdgeCases` | L2 — nonzero error envelope and unchanged inventory. |
| `config set` invalid value type | ⚡ Shallow | `config/test_config.py` | `TestConfigEdgeCasesExtended` | L2 — nonzero error envelope and unchanged inventory. |
| `config reset <cat> <key>` | ⚡ Shallow | `config/test_config.py` | `TestConfigLifecycle` | L2 — null override, exact default scalar and baseline restoration. |
| `config reset <cat>` (category only) | ⚡ Shallow | `config/test_config.py` | `TestConfigEdgeCases` | L2 — all category overrides null, other categories unchanged, exact restoration. |
| `config reset` no args | ⚡ Shallow | `config/test_config.py` | `TestConfigEdgeCases` | Shows guidance, exit 0 |
| `config reset --all` | ⚡ Shallow | `config/test_config.py` | `TestConfigLifecycle`, `TestConfigEdgeCasesResetAllAfterSet` | L2 — all overrides null, metadata unchanged; restores the complete prior inventory. |
| `config reset --force` | ⚡ Shallow | `config/test_config.py` | `TestConfigEdgeCasesResetAllAfterSet` | Skipped confirmation |
| `config ls` | ⚡ Shallow | `config/test_config.py` | `TestConfigLifecycle` | L1 — checks [defaults.vm] in output |

---

## `mvm network`

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `network create <name> --subnet <cidr>` | ✅ Deep | `network/test_network.py` | `TestNetworkLifecycle` | L3: bridge exists, IP assigned, firewall rules |
| `network create` without --subnet | ⚡ Shallow | `network/test_network.py` | `TestNetworkEdgeCases` | L1: checks error message |
| `network create` invalid CIDR | ⚡ Shallow | `network/test_network.py` | `TestNetworkLifecycle` | L1 |
| `network create` /32 subnet | ⚡ Shallow | `network/test_network.py` | `TestNetworkLifecycle` | L1 |
| `network create` duplicate name | ⚡ Shallow | `network/test_network.py` | `TestNetworkLifecycle` | Installed CLI returns `network.already_exists` and retains one network record |
| `network create` duplicate subnet | ⚡ Shallow | `network/test_network.py` | `TestNetworkLifecycle` | L1 — two tests for this: test_overlapping_subnet_across_networks_rejected, test_overlapping_subnet_rejected |
| `network create --default` | ⚡ Shallow | `network/test_network.py` | `TestNetworkLifecycle` | L2 — is_default=true in ls JSON |
| `network create --no-nat` | ✅ Deep | `network/test_network.py` | `TestNetworkLifecycle` | Installed bridge exists and exact subnet/bridge masquerade rule is absent |
| `network create --ipv4-gateway` | ✅ Deep | `network/test_network.py` | `TestNetworkAdvancedCreate` | L2 — checks inspect JSON. Could be L3 (bridge IP). |
| `network create --nat-gateways` (removed) | ⚡ Shallow | `network/test_network.py` | `TestNetworkAdvancedCreate` | Installed candidate rejects the removed flag before effects; focused runner case passed. |
| `network create` invalid gateway | ⚡ Shallow | `network/test_network.py` | `TestNetworkAdvancedCreate` | L1 |
| `network create` without prompt flag | ⚡ Shallow | `network/test_network.py` | `TestNetworkEdgeCases` | L2: name is present in the schema-1 `ls --json` result; focused case passed 1/1 in `mvmctl-030-test` on candidate `0.3.0-rc.91de873f` |
| `network ls` | ⚡ Shallow | `network/test_network.py` | `TestNetworkLifecycle` | L1 |
| `network ls --json` | ⚡ Shallow | `network/test_network.py` | `TestNetworkLifecycle` | L2 — checks list, name, id, subnet |
| `network ls --json` empty | ⚡ Shallow | `network/test_network.py` | `TestNetworkLifecycle` | L2 — valid empty list |
| `network inspect <name>` | ⚡ Shallow | `network/test_network.py` | `TestNetworkLifecycle` | L1 |
| `network inspect <name> --json` | ⚡ Shallow | `network/test_network.py` | `TestNetworkLifecycle` | L2 — name, subnet, bridge |
| `network inspect <name>` | ⚡ Shallow | `network/test_network.py` | `TestNetworkInspectTree` | L1 |
| `network rm <name>` | ✅ Deep | `network/test_network.py` | `TestNetworkLifecycle` | L2 listing + bridge verification |
| `network rm <nonexistent>` | ⚡ Shallow | `network/test_network.py` | `TestNetworkLifecycle` | L1 |
| `network rm --force` | ✅ Deep | `network/test_network.py` | `TestNetworkRemoveForce` | L3: bridge interface gone after removal (ip link show) |
| `network rm <name1> <name2>` | ⚡ Shallow | `network/test_network.py` | `TestNetworkLifecycle` | L2 — listing check |
| `network default <name>` | ⚡ Shallow | `network/test_network.py` | `TestNetworkLifecycle` | L2 — is_default in JSON |
| `network default <nonexistent>` | ⚡ Shallow | `network/test_network.py` | `TestNetworkLifecycle` | L1 |
| `network sync` | ✅ Deep | `network/test_network.py` | `TestNetworkSync` | L3: bridge, IP, firewall rules |
| `network sync --json` | ⚡ Shallow | `network/test_network.py` | `TestNetworkSync` | L2 — result dict with per-network stats |
| `network sync <specific>` | ✅ Deep | `network/test_network.py` | `TestNetworkSync` | L3: bridge and rules |
| `network sync` idempotent | ✅ Deep | `network/test_network.py` | `TestNetworkSync` | Installed inet forward-rule generation is unchanged across repeated sync |
| Same-boot sync after bridge deletion | ✅ Deep | `network/test_network.py` | `TestNetworkSyncMissingBridge` | L2: missing recorded bridge is rejected, not silently re-created; exact removal cleans up |
| `network sync` conntrack rule | ✅ Deep | `network/test_network.py` | `TestNetworkSync` | Conntrack established/related accept rule |
| `network sync` nonexistent | ⚡ Shallow | `network/test_network.py` | `TestNetworkSync` | Error handling for nonexistent network |
| `network rm` rejects with active VM | ✅ Deep | `network/test_network.py` | `TestNetworkVMDependency` | L3: rm fails without --force when VM uses network; inspect shows VM count |
| Fixed nftables network/VM lifecycle | 🟡 Partial | `network/test_nftables.py` | `TestNFTablesFirewallBackend` | Focused installed-candidate case passed modeled bridge/inet rules, nested VM/SSH, and rule cleanup; no backend selector remains. |
| Fixed nftables sync | 🟡 Partial | `network/test_nftables.py` | `TestAtomicRuleSync` | Three focused installed-candidate cases passed conntrack, repeated generation equality, and NAT readback; crash/failure paths remain open. |
| Cross-network typed traffic grant | 🟡 Partial | `network/test_policies.py` | `test_cross_network_traffic_policy_exact_allow_and_revoke` | Focused installed-candidate case passed default deny, TCP port 8080 allow, port 8081 deny, and deny after revoke. Other policy variants remain open. |
| Same-network typed traffic grant | 🟡 Partial | `network/test_policies.py` | `test_same_network_traffic_policy_exact_allow_and_revoke` | Focused installed-candidate case passed default deny, TCP port 8080 allow, port 8081 deny, and deny after revoke with bridge and inet filtering active. VM-source and UDP remain open. |
| Directional VM-to-VM exec grant | 🟡 Partial | `exec/test_vm_to_vm_exec.py` | `test_directional_vm_to_vm_exec_policy` | Current installed-candidate same/cross/networkless journey passed exact nonroot grant, real destination effects and default/reverse/revoked denials. Separate current cases passed retained grants after endpoint restart/destination reboot, interruption cleanup and restored-endpoint/new-grant behavior. Further authority-abuse/capacity/deadline and host-reboot coverage remains open; exact source/outcome boundaries are in the qualification overlay. |
| Retained VM-to-VM exec interruption | ✅ Deep | `exec/test_vm_to_vm_exec.py` | `test_vm_to_vm_exec_interrupt_reaps_destination_group` | Current reset candidate passed pidfd SIGINT, exact destination tasks no longer live and a fresh authorized relay; strict 1/1, six preservation checks and QA worker/group cleanup. Full private-backend cleanup and other fault boundaries are not established by this case. |

---

## `mvm vm`

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `vm create` basic | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMListInspect` (via module_vm), `TestVMCreate` | L2 — status=running. Many shallow VM tests. |
| `vm create` with `--vcpu` | ✅ Deep | `vm/test_vm_lifecycle.py` | `TestVMConfigOptions::test_resource_allocation` | Installed-candidate L2 — guest nproc=2 plus create/inspect resource fields. |
| `vm create` with `--vcpu 0` | ✅ Deep | `vm/test_vm_lifecycle.py` | `TestVMConfigOptions::test_invalid_resource_rejected_before_vm_creation` | L2 — schema-1 class-1 `vm.create.failed`, unchanged VM inventory. |
| `vm create` with `--vcpu -1` | ✅ Deep | `vm/test_vm_lifecycle.py` | `TestVMConfigOptions::test_invalid_resource_rejected_before_vm_creation` | L2 — typed validation refusal and unchanged VM inventory. |
| `vm create` with `--mem` | ✅ Deep | `vm/test_vm_lifecycle.py` | `TestVMConfigOptions::test_resource_allocation` | L2 — numeric and human-readable 256 MiB allocation, bounded guest MemTotal and create/inspect fields. |
| `vm create` with `--mem 0` | ✅ Deep | `vm/test_vm_lifecycle.py` | `TestVMConfigOptions::test_invalid_resource_rejected_before_vm_creation` | L2 — typed validation refusal and unchanged VM inventory. |
| `vm create` with `--disk-size` | ✅ Deep | `vm/test_vm_lifecycle.py` | `TestVMConfigOptions::test_resource_allocation` | L2 — guest root block-device extent is exactly 512 MiB plus create/inspect fields. |
| `vm create` with `--disk-size 0` | ✅ Deep | `vm/test_vm_lifecycle.py` | `TestVMConfigOptions::test_invalid_resource_rejected_before_vm_creation` | L2 — typed validation refusal and unchanged VM inventory. |
| `vm create` with `--disk-size invalid` | ✅ Deep | `vm/test_vm_lifecycle.py` | `TestVMConfigOptions::test_invalid_resource_rejected_before_vm_creation` | L2 — schema-1 class-1 `validation.failed`, unchanged VM inventory. |
| `vm create` with `--kernel` | 🟡 Partial | `kernel/test_kernel.py` | `TestKernelStoppedVMDeletion` | Installed-candidate L2 — distinct imported kernel selected by full ID, retained while referenced by stopped VM. Independent kernel-prefix resolution remains unproved; retired default-prefix probe was vacuous. |
| `vm create` with `--boot-args` | ✅ Deep | `vm/test_vm_lifecycle.py` | `TestVMConfigOptions::test_boot_args_reach_guest` | L2 — unique custom marker and requested loglevel in guest /proc/cmdline. |
| `vm create` with `--ip` (static) | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMNetworkIntegration` | L2 — ipv4 field in ls JSON |
| `vm create` with invalid IP | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMNetworkIntegration` | L1 |
| `vm create` with `--mac` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMNetworkIntegration` | L2 — mac field |
| `vm create` with named `--network` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMNetworkIntegration` | L2 — network_id matches |
| `vm create --no-network`, stop/start/reboot, and vsock exec | 🟡 Partial | `vm/test_jailer.py` | `TestNetworklessJailerLifecycle` | Focused installed-candidate L2 case passed twice for restart and once with reboot in the named runner: no network identity or guest `eth0`, `lo` present, exec after restart and reboot, checked VM removal. Kernel `dummy0` may exist; namespace isolation, VM-to-VM authorization, and full domain QA remain. |
| `vm create` default (no console relay) | ✅ Deep | `vm/test_vm_lifecycle.py` | `TestVMConfigOptions::test_resource_allocation` | L2 — no console flag; inspect reports disabled console, relay_running=false and relay_pid=null. |
| `vm create` with `--console` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMCreateConsole` | L2 — enable_console=true in ls JSON |
| `vm create` default (PCI enabled) | ✅ Deep | `vm/test_vm_lifecycle.py` | `TestVMConfigOptions::test_pci_transport` | L2 — default PCI VM boots, stored transport matches, guest enumerates PCI devices. |
| `vm create` with `--no-pci` | ✅ Deep | `vm/test_vm_lifecycle.py` | `TestVMConfigOptions::test_pci_transport` | L2 — no-PCI VM boots, stored transport matches, no guest PCI devices. |
| `vm create` with `--enable-logging` | ✅ Deep | `vm/test_vm_lifecycle.py` | `TestVMConfigOptions::test_logging_output` | L2 — configured logging enabled, vsock exec works, fresh log exists and is nonempty. |
| `vm create` with `--disable-logging` | ✅ Deep | `vm/test_vm_lifecycle.py` | `TestVMConfigOptions::test_logging_output` | L2 — configured logging disabled, vsock exec works, fresh log exists and is empty. |
| `vm create` with `--enable-metrics` | ✅ Deep | `vm/test_vm_lifecycle.py` | `TestVMConfigOptions::test_metrics_output` | L2 — fresh metrics file contains complete nonempty JSON records within a bounded wait. |
| `vm create` with `--disable-metrics` | ✅ Deep | `vm/test_vm_lifecycle.py` | `TestVMConfigOptions::test_metrics_output` | L2 — configured metrics disabled and fresh metrics file absent. |
| `vm create` with `--cloudinit-config` | 🟡 Partial | `vm/test_vm_lifecycle.py` | `TestVMCloudInit` | L1/L2 for most modes, L3 for cloudinit-config script (checks seed dir via SSH). DNS test skips often. |
| `vm create` with `--cloud-init-mode inject` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMCloudInit` | L2 — status=running only |
| `vm create` with `--cloud-init-mode iso` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMCloudInitModes` | L2 — status=running |
| `vm create` with `--cloud-init-mode net` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMCloudInitModes` | L2 — status=running |
| `vm create` with `--cloud-init-mode off` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMCloudInitModes` | L2 — status=running |
| `vm create` with `--nocloud-net-port` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMNocloudNetPort`, `TestVMCloudInit` | L2 — nocloud_net_port in inspect |
| `vm create` with `--count N` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMCreateCount` | L2 — N VMs in ls --json |
| `vm create` with `--atomic --count N` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMCreateAtomicCount` | L2 — all VMs running or all rolled back |
| `vm create --count` with `--ip` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMCreateCountIP` | L1 — rejected, mutually exclusive with --count > 1 |
| `vm create --count` with `--mac` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMCreateCountMAC` | L1 — rejected, mutually exclusive with --count > 1 |
| `vm create --count -1` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMCreateCountNegative` | L1 — non-zero exit with error |
| `vm create` with `--volume` | 🟡 Partial | `vm/test_vm_lifecycle.py`, `vm/test_jailer.py` | `TestVMVolumeIntegration`, `TestNetworklessJailerLifecycle` | Focused installed-candidate networkless raw attach passed: root-brokered volume status/VM ID, guest `vdb` before and after restart, checked removal. Networked and snapshot journeys remain unqualified. |
| `vm create` with `--volume <id-prefix>` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMVolumeIntegration` | L2 — volume status=attached |
| `vm create` with `--ssh-key <key>` | 🟡 Partial | `vm/test_vm_lifecycle.py` | `TestVMSSHIntegration` | L3 — SSH reachable if key works |
| `vm create` with `--ssh-key <filepath>` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMAdvancedCreateFlags` | L2 — key injected from file path |
| `vm create` with `--user` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMAdvancedCreateFlags` | L2 — user field in inspect |
| `vm create` with `--force` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMCreateForce` | L2 — creates VM without confirmation prompt |
| `vm create` with `--vsock-port` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMCreateVsockPort` | L2 — vsock socket file created |
| `vm create` with `--lsm-flags` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMAdvancedCreateFlags` | L2 — LSM flags in inspect output |
| `vm create` with `--skip-cleanup` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMAdvancedCreateFlags` | L1 — VM created successfully |
| `vm create` with default deblob (Alpine) | 🟡 Partial | `vm/test_jailer.py` | `TestNetworklessJailerLifecycle` | Focused installed-candidate L2: guest booted and exposed deblob-created cloud-init and OpenRC settings. Other distros, cleanup rules, and failure rollback remain unqualified. |
| `vm create` with `--skip-deblob` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMAdvancedCreateFlags` | L2 — VM created with skip-deblob |
| `vm create` with unqualified Firecracker/Jailer pair | 🟡 Partial | `vm/test_jailer.py` | `TestNetworklessJailerLifecycle::test_unqualified_release_is_rejected_before_plain_launch` | Installed-candidate L2: present 1.16.0 pair rejected as `binary.version.gate` before a plain networkless/no-PCI VM projection; defaults restored. A separate 1.16.1 case launched successfully. Start/reboot/restore public paths and orchestrator run remain unqualified. |
| `vm create` with `--nested-virt` | ✅ Deep | `vm/test_vm_nested_isolated.py` | `TestNestedIsolated` | L3 — cpu-config in Firecracker JSON, boot args validated |
| `vm create` with `--cpu-template` | ✅ Deep | `vm/test_vm_nested_isolated.py` | `TestNestedIsolated` | L3 — merged cpu-config in Firecracker JSON |
| `vm create` with `--no-nested-virt` | ✅ Deep | `vm/test_vm_nested_isolated.py` | `TestNestedIsolated` | L3 — no cpu-config, no nested boot args |
| `vm ls` | 🟡 Partial | `vm/test_vm_lifecycle.py`, `vm/test_jailer.py` | `TestVMListInspect`, `TestNetworklessJailerLifecycle`, `TestVMStateTransitions` | Root-authority running/stopped projection and confirmed external Firecracker exit (`crashed`) passed in focused installed-candidate cases. Guest-initiated exit and complete host-reboot qualification remain open. |
| `vm ls --json` | 🟡 Partial | `vm/test_vm_lifecycle.py` | `TestVMListInspect::test_listing_json[ls]`, `TestVMStateTransitions::test_crashed_vm_status_and_restart` | Installed-candidate L2 proves schema-1 running identity/resource fields and `crashed` projection after pidfd-confirmed exit. Guest-initiated exit remains open. |
| `vm ls --json` empty | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMListEmpty` | Installed-candidate L2 passed a read-only versioned empty-list assertion on the clean runner; no global deletion. |
| `vm ps` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMListInspect::test_listing_text[ps]` | Installed-candidate L2 passed actual human output containing the exact VM name, IPv4, and headers. |
| `vm ps --json` | 🟡 Partial | `vm/test_vm_lifecycle.py`, `vm/test_jailer.py` | `TestVMListInspect`, `TestNetworklessJailerLifecycle`, `TestVMStateTransitions` | Focused installed-candidate cases prove inclusion while running and after restart, exclusion after CLI stop and confirmed external Firecracker exit. Guest-initiated exit and complete host-reboot qualification remain open. |
| `vm inspect <name>` | 🟡 Partial | `vm/test_vm_lifecycle.py`, `vm/test_jailer.py` | `TestVMListInspect::test_inspect_tree`, `TestNetworklessJailerLifecycle` | Installed-candidate L2 passed the actual text route with exact name, full ID, IPv4, and running status. Root-brokered cgroup detail is qualified through `--json` below. |
| `vm inspect <name> --json` | 🟡 Partial | `vm/test_vm_lifecycle.py`, `vm/test_jailer.py`, `vm/test_cgroup.py` | `TestVMListInspect`, `TestNetworklessJailerLifecycle`, `TestVMCgroupEnvelope`, `TestVMStateTransitions` | Focused installed-candidate cases prove running/stopped/running and confirmed-exit `crashed` projection, plus root-brokered cgroup limits/usage. Guest-initiated exit and complete host-reboot qualification remain open. |
| `vm inspect` by IP | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMListInspect::test_inspect_json[ipv4]` | Installed-candidate L2 passed the direct IPv4 selector with exact VM ID, allocation, and networking readback. |
| `vm inspect` by ID prefix | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMListInspect::test_inspect_json[id_prefix]` | Installed-candidate L2 passed the direct unique ID-prefix selector with exact full-ID readback; ambiguous-prefix behavior is not proved here. |
| `vm start` | ✅ Deep | `vm/test_vm_lifecycle.py` | `TestVMStateTransitions` | L3 — PID alive in /proc after start |
| `vm start` on running | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMStateTransitionErrors` | L1 — exit 0 |
| `vm stop` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMStateTransitions` | L2 — status=stopped |
| `vm stop --force` | ✅ Deep | `vm/test_vm_lifecycle.py` | `TestVMStateTransitions` | L3 — PID no longer alive |
| `vm stop` on stopped | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMStateTransitionErrors` | L1 — exit 0 |
| `vm stop` by IP | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMStateTransitions` | L2 |
| `vm stop` graceful (no --force) | ✅ Deep | `vm/test_vm_lifecycle.py` | `TestVMStateTransitions` | L3 — PID gone from /proc after graceful stop |
| `vm pause` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMStateTransitions` | L2 — status=paused |
| `vm pause` on stopped | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMStateTransitionErrors` | L1 — non-zero |
| `vm resume` | ✅ Deep | `vm/test_vm_lifecycle.py` | `TestVMStateTransitions` | L3 — PID alive after resume |
| `vm resume` on running | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMStateTransitionErrors` | L1 — exit 0 |
| `vm reboot` | ✅ Deep | `vm/test_vm_lifecycle.py` | `TestVMStateTransitions` | L3 — PID changed (proves restart) and alive in /proc |
| `vm reboot --force` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMStateTransitions` | L2 — status=running |
| `vm rm` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMStateTransitions` (crash tests) | L2 — not in listing |
| `vm rm --force` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | Various | L2 |
| `vm rm <name1> <name2>` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMDestructiveRmMultiple` | L2 |
| `vm rm <nonexistent>` | ⚡ Shallow | `cli/test_cli.py` | `TestErrorMessageIsActionable` | L1 |
| `volume attach` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMVolumeIntegration` | L2 — status=attached |
| `volume attach` on running | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMVolumeIntegration` | L1 — non-zero |
| `volume attach` nonexistent | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMVolumeIntegration` | L1 — non-zero |
| `volume detach` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMVolumeIntegration` | L2 — status=available |
| `volume detach` on running | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMVolumeIntegration` | L1 — non-zero |
| `volume detach` nonexistent | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMVolumeIntegration` | L1 — non-zero |
| vm rm with attached volume | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMVolumeIntegration` | L2 — status=available after rm |
| Crashed firecracker recovery | 🟡 Partial | `vm/test_vm_lifecycle.py` | `TestVMStateTransitions::test_crashed_vm_status_and_restart` | Installed-candidate L2 proves exact pidfd-bound crash/exit, public ls/ps/inspect reconciliation, typed stop/start, vsock exec, and checked removal. Guest poweroff and complete host-reboot qualification remain open. |
| Config chain precedence | ✅ Deep | `vm/test_vm_lifecycle.py` | `TestVMConfigOptions::test_config_chain_precedence` | L2 — guest inherits configured two CPUs, then explicit one-CPU CLI value wins; prior override or override absence restored exactly. |
| Volume persists stop/start | ✅ Deep | `vm/test_vm_lifecycle.py` | `TestVMVolumeIntegration` | L3 — SSH in, check /dev/vdb |
| Volume mountable in guest | ✅ Deep | `vm/test_vm_lifecycle.py` | `TestVMVolumeIntegration` | L3 — mkfs.ext4 + mount + write + read |
| DNS resolution in guest | 🟡 Partial | `vm/test_vm_lifecycle.py` | `TestVMCloudInit` | L3 — often skips if DNS unavailable |
| Boot time within limits | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMStateTransitions` | L1 — elapsed < 30s |
| `vm create` fresh_env full pipeline | ✅ Deep | `vm/test_vm_fresh_env.py` | `TestFreshEnvVM` | ubuntu:noble, official:7.0.11 kernel with features, --nested-virt, 6vcpu/4g/8g, L3 SSH + nested KVM verify |
| `vm create` + volume attach + verify attached | ✅ Deep | `vm/test_vm_fresh_env.py` | `TestFreshEnvVM` | Stop → attach → start → verify vol status=attached via JSON |
| `vm create` specs verified (--vcpu, --mem, -s, --nested-virt) | ✅ Deep | `vm/test_vm_fresh_env.py` | `TestFreshEnvVM` | ls --json + inspect --json + firecracker.json cpu-config verify |
| `host status --json` inside nested guest | ✅ Deep | `vm/test_vm_nested_isolated.py` | `TestNestedIsolated` | Binary copied into guest, runs inside via SSH, verifies kvm_accessible |
| `config set/get/reset` inside nested guest | ⚡ Shallow | `vm/test_vm_nested_isolated.py` | `TestNestedIsolated` | Isolated config roundtrip inside guest |
| `key create/ls/rm` inside nested guest | ⚡ Shallow | `vm/test_vm_nested_isolated.py` | `TestNestedIsolated` | Key lifecycle inside isolated guest |
| `volume create/resize/rm` inside nested guest | ⚡ Shallow | `vm/test_vm_nested_isolated.py` | `TestNestedIsolated` | Volume full lifecycle inside isolated guest |
| `network create/ls/rm` inside nested guest | 🟡 Partial | `vm/test_vm_nested_isolated.py` | `TestNestedIsolated` | Skips if guest lacks iptables/nftables |
| `vm create` inside nested guest (triple nesting) | 🟡 Partial | `vm/test_vm_nested_isolated.py` | `TestNestedIsolated` | Skips if /dev/kvm unavailable in guest |

---

## `mvm exec`

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `exec <id> -- <cmd>` (command) | ⚡ Shallow | `exec/test_exec.py` | `TestExecBasic::test_exec_basic_command` | L2 — stdout contains command output |
| `exec <id>` (interactive shell via stdin) | ⚡ Shallow | `exec/test_exec.py` | `TestExecBasic::test_exec_interactive_shell` | L2 — real PTY, command output, and clean exit |
| `exec` with `--port` | ⚡ Shallow | `exec/test_exec.py` | `TestExecFlags::test_exec_with_port` | L2 — explicit port wiring |
| `exec` with `--timeout` | ⚡ Shallow | `exec/test_exec.py` | `TestExecBasic`, `TestExecFlags` | Captured-exec helper passes explicit `--timeout 10`; delayed-agent deadline behavior remains unqualified |
| `exec` with `--user` | ⚡ Shallow | `exec/test_exec.py` | `TestExecFlags::test_exec_with_user` | L2 — whoami returns user name |
| `exec` nonexistent VM | ⚡ Shallow | `exec/test_exec.py` | `TestExecErrorCases::test_exec_nonexistent_vm` | L1 — "not found" in error |
| `exec` invalid port | ⚡ Shallow | `exec/test_exec.py` | `TestExecErrorCases::test_exec_invalid_port` | L2 — negative and overflowing ports reject before guest side effects; red-green installed candidate |
| `exec` empty command | ⚡ Shallow | `exec/test_exec.py` | `TestExecBasic::test_exec_interactive_shell` | No command selects interactive mode; duplicate newline-only case removed |

---

## `mvm snapshot` (alias: `ss`)

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `snapshot create <vm>` | 🟡 Partial | `vm/test_vm_snapshot_load.py`, `vm/test_jailer.py` | `TestVMSnapshot`, `TestSnapshotDestroyRestore`, `TestNetworklessJailerLifecycle` | Focused installed-candidate networkless full capture with an attached raw volume and one managed-network DR capture passed. The CLI emits the full request ID before capture, including in JSON mode; one manual CLI-kill/typed-inspect/remove journey passed. Automated interruption QA and `snapshot ls` discovery of unindexed root captures remain open. |
| `snapshot create --name` | ⚡ Shallow | `vm/test_vm_snapshot_load.py` | `TestSnapshotCreateFlags::test_snapshot_create_with_name` | L2 — exact unique caller name in schema-1 list and inspect; checked cleanup, qualified in named runner. |
| `snapshot create --pause` | ✅ Deep | `vm/test_vm_snapshot_load.py` | `TestSnapshotCreateFlags::test_snapshot_create_pause_flag` | L2 — running source becomes paused with unchanged PID/start identity; resume preserves guest boot/marker and working vsock. Qualified in named runner. |
| `snapshot create` on stopped VM | ✅ Deep | `vm/test_vm_lifecycle.py` | `TestVMStateTransitions::test_stopped_snapshot_captures_and_restores_disk` | Installed 1.16.2 candidate passed disk-only capture, source removal and cold restore with durable marker readback and changed guest boot ID. No running-memory capture is claimed by this row. |
| `snapshot ls` | ⚡ Shallow | `vm/test_vm_snapshot_load.py` | `TestVMSnapshot`, `TestSnapshotDestroyRestore` | L2 — JSON listing |
| `snapshot ls --json` | ⚡ Shallow | `vm/test_vm_snapshot_load.py` | `TestVMSnapshot`, `TestSnapshotDestroyRestore` | L2 — JSON list |
| `snapshot inspect <id>` | ⚡ Shallow | `vm/test_vm_snapshot_load.py` | `TestSnapshotInspect::test_snapshot_inspect` | L2 — exact text capture/source identities, base directory, and 1-vCPU/256-MiB/512-MiB resources; qualified in named runner. |
| `snapshot inspect <id> --json` | ⚡ Shallow | `vm/test_vm_snapshot_load.py` | `TestSnapshotInspect::test_snapshot_inspect_json` | L2 — exact schema-1 nested snapshot metadata and resource values; qualified in named runner. |
| `snapshot restore <id> <name>` | 🟡 Partial | `vm/test_vm_snapshot_load.py`, `vm/test_jailer.py` | `TestVMSnapshot`, `TestSnapshotDestroyRestore`, `TestCanonicalJailerLaunch`, `TestNetworklessJailerLifecycle` | Focused networkless restore passed with no endpoint, independent attached-volume bytes, vsock exec, and checked teardown. Same-network managed restore preserved network/IP and guest exec after source deletion; an explicit captured-network selection also succeeded after a different-network refusal. A red-green L2 case proves pre-spawn missing-release failure leaves no VM projection or snapshot reference; endpoint conflicts, post-spawn, and transport-uncertain paths remain unqualified. |
| `snapshot restore --resume` | 🟡 Partial | `vm/test_vm_snapshot_load.py`, `vm/test_jailer.py` | `TestVMSnapshot`, `TestSnapshotDestroyRestore`, `TestNetworklessJailerLifecycle` | Focused networkless restore resumed, preserved absent endpoint, restored independent data-volume bytes, and allowed vsock exec. One same-network managed restore resumed with retained IP and working guest exec. Other placements and interruption paths remain unqualified. |
| `snapshot restore --network` | 🟡 Partial | `vm/test_jailer.py` | `TestCanonicalJailerLaunch`, `TestNetworklessJailerLifecycle` | In the named runner, a managed snapshot refused an existing different network with `snapshot.restore_failed` and no target VM; explicit selection of its captured network then restored successfully with `--resume`. A separate networkless snapshot rejected a managed-network override. Endpoint-conflict and interrupted-restore paths remain unqualified. |
| `snapshot rm <id>` | ⚡ Shallow | `vm/test_vm_snapshot_load.py` | `TestSnapshotDestroyRestore` | L2 — cleanup via --force |
| `snapshot rm --force` | ⚡ Shallow | `vm/test_vm_snapshot_load.py` | `TestSnapshotDestroyRestore` | L2 — forced removal |

---

## `mvm volume`

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `volume create <size>` (raw-only) | 🟡 Partial | `volume/test_volume.py`, `vm/test_jailer.py` | `TestRawVolumeAuthoritativeStorage`, `TestNetworklessJailerLifecycle`, `TestVolumeLifecycle` | The installed-candidate volume-only lifecycle selection passed 25/25, including metadata options, resize/shrink, and removal. Separate focused cases proved guest-visible networkless attach/restart and independent snapshot-volume bytes. Guest-visible option enforcement and networked attach/detach remain unqualified. |
| `volume create --format` (removed) | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle::test_volume_create_removed_format_flag_rejected_without_side_effects` | Installed-candidate raw/QCOW2/VMDK variants passed 3/3: unknown flag, no new volume record or backing file, checked sentinel cleanup. QCOW2 **image import** remains supported. |
| `volume create` invalid size | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle` | L1 |
| `volume create` duplicate name | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle` | L1 |
| `volume create` negative size | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle` | L1 |
| `volume create` zero size | 🟡 Partial | `volume/test_volume.py` | `TestVolumeLifecycle::test_zero_size_volume` | Installed candidate returned schema-1 `validation.failed` with no new record or backing; a clean-cache absence audit and disk-full failure remain open. |
| `volume create --read-only` | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle` | L2 — is_read_only=true in ls JSON, inspect JSON |
| `volume create --shareable` | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle::test_volume_create_shareable` | L2 — is_shareable=true in ls JSON |
| `volume ls` | 🟡 Partial | `volume/test_volume.py` | `TestVolumeLifecycle::test_volume_ls_empty` | Empty-cache human output passed in the selected volume-only batch; nonempty human output remains unqualified. |
| `volume ls --json` | 🟡 Partial | `volume/test_volume.py` | `TestVolumeLifecycle::test_volume_list_json` | Installed candidate returned a schema-1 list containing the test-owned volume; other list variants remain unqualified. |
| `volume ls` empty | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle` | L1 |
| `volume inspect` | 🔴 Missing | `volume/test_volume.py` | — | Human-readable inspection is not currently exercised by this module. |
| `volume inspect --json` | 🟡 Partial | `volume/test_volume.py` | `TestVolumeLifecycle::test_volume_inspect_json` | Installed candidate returned schema-1 volume name, size, format, status, and path for one test-owned volume. |
| `volume inspect` nonexistent | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle` | L1 |
| `volume rm` | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle` | L2 |
| `volume rm --force` | 🟡 Partial | `volume/test_volume.py` | `TestVolumeLifecycle`, `TestVolumeRunningVMDependency::test_force_remove_requires_stopped_vm` | Current installed-candidate L2 passed running-VM refusal and stopped-VM force removal with checked owned cleanup and baseline preservation. Further volume lifecycle cases remain separately qualified. |
| `volume rm` nonexistent | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle` | L1 |
| `volume rm <name1> <name2>` | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle` | L2 |
| `volume rm` partial failure | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle` | L2 — one exists, one doesn't |
| `volume resize` | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle` | L2 — size_bytes matches |
| `volume resize` nonexistent | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle` | L1 |
| `volume resize` shrink | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle::test_volume_resize_rejects_shrink_without_changing_backing` | Focused installed-candidate 16→8 MiB rejection, successful same-size control, and unchanged authoritative size passed in the named runner. |
| Volume invariants (available→attached→available) | 🟡 Partial | `volume/test_volume.py` | `TestVolumeLifecycle::test_volume_invariants_available_attached_cycle` | Focused installed-candidate networkless L2 passed: same volume ID and 16 MiB logical size across stopped-VM attach/detach, guest-visible `vdb` after start, and protected backing with caller path redacted after launch. Live hotplug, guest data persistence, and managed-network attachment remain unqualified. |
| Cross-VM attach rejection | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeCrossVM` | L2 — attached volume rejects second VM |
| Volume rm with running VM | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeRunningVMDependency` | L1 |
| Volume rm --force with running VM | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeRunningVMDependency` | L2 — refusal while running; stop required before checked force removal. |
| Volume resize with running VM | 🟡 Partial | `volume/test_volume.py` | `TestVolumeRunningVMDependency::test_grow_attached_volume_requires_stopped_vm` | Current installed-candidate L2 passed running-VM refusal and successful stopped-but-attached data-volume growth without detach, with checked cleanup and preserved baseline. This does not qualify live resize or root-filesystem growth. |
| Volume hotplug (attach to running) | 🟡 Partial | `volume/test_volume_hotplug.py` | `TestVolumeHotplug` | Current direct named-runner cases passed initial guest discovery and format/mount/write/read through vsock. Live reattachment failed and remains unqualified; no host-direct Tier 3 claim. |
| Volume hotunplug (detach from running) | 🟡 Partial | `volume/test_volume_hotplug.py` | `TestVolumeHotplug` | Current direct named-runner live unplug case passed guest device disappearance and attachment-state checks. Subsequent live reattachment failed; no full-module or host-direct Tier 3 claim. |
| Volume attach/detach lifecycle (stop→detach→reattach→start) | ✅ Deep | `volume/test_volume.py` | `TestVolumeAttachDetach` | L3: full lifecycle including attach-to-stopped-then-start (Bug #7), detach-reattach-verify |
| Volume attach/detach nonexistent | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeNegativeFailure` | L2: attach/detach nonexistent volume to running VM, verify clear error, no state corruption |
| Volume cold-attach (stopped VM) | ✅ Deep | `volume/test_volume_hotplug.py` | `TestVolumeHotplugVersionGate` | Current direct named-runner cold attach and detach cases passed with exact owned cleanup. The installed release was Firecracker 1.16.2; this does not prove all other versions. |
| Volume hotplug destructive (force-remove, double-attach) | ✅ Deep | `volume/test_volume_hotplug.py` | `TestVolumeHotplugDestructive` | Current force-removal and duplicate-attach cases passed. Running force-removal preserves backing/attachment until stopped; duplicate attach retains one guest device and one VM volume reference. Live reattachment remains separately failed. |

---

## `mvm key`

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `key create --algorithm ed25519` | ⚡ Shallow | `keys/test_keys.py` | `TestKeyLifecycle` | L2 |
| `key create --algorithm rsa` | ⚡ Shallow | `keys/test_keys.py` | `TestKeyLifecycle` | L2 |
| `key create --algorithm ecdsa` | ⚡ Shallow | `keys/test_keys.py` | `TestKeyLifecycle` | L2 |
| `key create --bits` | ⚡ Shallow | `keys/test_keys.py` | `TestKeyCreateAdvanced` | L2 |
| `key create --comment` | ⚡ Shallow | `keys/test_keys.py` | `TestKeyCreateAdvanced` | L2 — comment in inspect |
| `key create --out` | ⚡ Shallow | `keys/test_keys.py` | `TestKeyCreateAdvanced` | L2 — exact private/public output paths and regular-file identities, checked owned removal. |
| `key create --default` | ⚡ Shallow | `keys/test_keys.py` | `TestKeyCreateAdvanced` | L2 — is_default |
| `key create --force` (overwrite) | ⚡ Shallow | `keys/test_keys.py` | `TestKeyCreateAdvanced` | L2 |
| `key add <name> <pubkey>` | ⚡ Shallow | `keys/test_keys.py` | `TestKeyLifecycle` | L2 |
| `key add` duplicate | ⚡ Shallow | `keys/test_keys.py` | `TestKeyLifecycle` | L1 |
| `key add --force` (overwrite) | ⚡ Shallow | `keys/test_keys.py` | `TestKeyImportOverwrite` | L2 |
| `key import <name> <path>` | ⚡ Shallow | `keys/test_keys.py` | `TestKeyLifecycle::test_key_import_shows_fingerprint` | L2 — fingerprint visible in ls --json |
| `key ls` | ⚡ Shallow | `keys/test_keys.py` | `TestKeyLifecycle` | L1 |
| `key ls --json` | ⚡ Shallow | `keys/test_keys.py` | `TestKeyLifecycle`, `TestEmptyKeyListing` | L2 — schema-1 envelope and fresh-cache empty array |
| `key inspect` | ⚡ Shallow | `keys/test_keys.py` | `TestKeyLifecycle` | L1 |
| `key inspect --json` | ⚡ Shallow | `keys/test_keys.py` | `TestKeyLifecycle` | L2 |
| `key inspect` | ⚡ Shallow | `keys/test_keys.py` | `TestKeyInspectTree` | L1 |
| `key rm` | ⚡ Shallow | `keys/test_keys.py` | `TestKeyLifecycle` | L2 |
| `key rm` nonexistent | ⚡ Shallow | `keys/test_keys.py` | `TestKeyLifecycle` | L1 |
| `key rm` with running guest | ⚡ Shallow | `ssh/test_ssh.py` | `TestKeyRunningVMDependency` | L2 — cached key removed without force; same VM remains running and exported private key still authenticates. Not a guest credential-revocation test. |
| `key rm <name1> <name2>` | ⚡ Shallow | `keys/test_keys.py` | `TestKeyLifecycle` | L2 |
| `key default` | ⚡ Shallow | `keys/test_keys.py` | `TestKeyLifecycle` | L2 |
| `key default --clear` | ⚡ Shallow | `keys/test_keys.py` | `TestKeyLifecycle` | L2 |
| `key export <name> <directory>` | ⚡ Shallow | `keys/test_keys.py` | `TestKeyLifecycle` | L2 — exact exported private/public bytes match cached originals and owned files are removed. |
| `key export` overwrite | ⚡ Shallow | `keys/test_keys.py` | `TestKeyExportForce` | L1 |
| `key export --force` | ⚡ Shallow | `keys/test_keys.py` | `TestKeyExportForce` | L2 — ordinary overwrite refused; forced export replaces tampered owned bytes with the original public key. |
| Multiple defaults | ⚡ Shallow | `ssh/test_ssh.py` | `TestKeyDefaults` | L2 — VM consumes both defaults; inspect, guest authorized_keys and actual SSH with each key agree; prior defaults restored. |
| Delete default key when only key | ⚡ Shallow | `keys/test_keys.py` | `TestKeyLifecycle` | L2 |

---

## `mvm image`

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `image pull <type>:<version>` | ⚡ Shallow | `images/test_images.py` | `TestImagePull::test_image_pull[slug]` | Current installed f909: cached slug returns the exact prepared Alpine record; no remote acquisition. |
| `image pull --force` | 🟡 Partial | `images/test_images.py` | `TestImagePullAdvanced::test_pull_cached_image_with_force_redownloads` | Installed a3d7 L2: mirrored replacement, sole-default transfer and old-backing retirement passed for `[force]`; three option variants remain unqualified. |
| `image pull --default` | ⚡ Shallow | `images/test_images.py` | `TestImagePullAdvanced::test_pull_cached_image_with_default_sets_default` | Current installed f909: cached pull keeps the exact prepared Alpine as the sole default; checked restoration. |
| `image pull --skip-optimization` | ⏭️ Skip | `images/test_images.py` | `TestImagePullSkipOptimization` | L1 — often skips |
| `image pull --type override` | ⚡ Shallow | `images/test_images.py` | `TestImagePull::test_image_pull[type-override]` | Current installed f909: type override resolves an unrelated selector to the exact cached Alpine record. |
| `image pull --version` | ⚡ Shallow | `images/test_images.py` | `TestImagePull::test_image_pull[version]` | Current installed f909: explicit version resolves the exact cached Alpine record. |
| `image pull nonexistent` | ⚡ Shallow | `images/test_images.py` | `TestImagePullAdvanced` | L1 — no skip |
| `image pull --disable-detector` | ⏭️ Skip | `images/test_images.py` | `TestImageAdvancedFlags` | L1 — often skips |
| `image pull` arch auto-detection | ⏭️ Skip | `images/test_images.py` | `TestImagePullArchFlag` | Network-dependent. L1: success message. No `--arch` flag — arch is auto-detected by Go CLI. |
| `image pull --no-cache` | ⏭️ Skip | `images/test_images.py` | `TestImagePullNoCache` | Network-dependent. L1: success message |
| `image pull --type <type>` (explicit) | ⚡ Shallow | `images/test_images.py` | `TestImagePull::test_image_pull[type-override]` | Same current cached type-override case above; not a separate node or remote acquisition proof. |
| `image ls` | ⚡ Shallow | `images/test_images.py` | `TestImageList` | Installed rc.7 human table: headers plus the seeded Alpine ID and name. |
| `image ls --json` | ⚡ Shallow | `images/test_images.py` | `TestImageList` | Installed rc.7 schema-1 array and seeded Alpine identity/presence. |
| `image ls --remote --no-cache` | 🔴 Missing | — | — | The former local `--no-cache` test asserted only exit 0; this flag affects remote resolution only. |
| `image ls --remote --type` | 🔴 Missing | — | — | The former local filter test swallowed parse failures; type filtering applies only to remote resolution. |
| `image ls --remote` | ⏭️ Skip | `images/test_images.py` | `TestImageList` | L1 — skips on network |
| `image inspect` | ⚡ Shallow | `images/test_images.py` | `TestImageList` | Installed rc.7 human renderer shows exact seeded Alpine ID, name, and path. |
| `image inspect --json` | ⚡ Shallow | `images/test_images.py` | `TestImageList` | Installed rc.7 grouped image/storage data matches the list record. |
| `image inspect --tree` | 🔴 Missing | — | — | Tree-view inspect subcommand does not exist in CLI. Not a valid test target. |
| `image default` | ⚡ Shallow | `images/test_images.py` | `TestImageDefaults::test_image_set_default` | Current installed f909: reselects the prepared Alpine default and checks it is sole; fresh-image selection is separately covered by import --default. |
| `image default` nonexistent | ⚡ Shallow | `images/test_images.py` | `TestImageDefaults::test_set_default_nonexistent_image_fails` | Current installed f909: schema-1 image.not_found error, no data and unchanged image listing/defaults. |
| `image rm` | ⚡ Shallow | `images/test_images.py` | `TestImageRemove::test_image_remove_with_fixture[normal]` | Current installed f909 L2: unused imported image removed; owned backing and inventory cleanup checked. |
| `image rm --force` | 🟡 Partial | `images/test_images.py` | `TestImageRemove::test_image_remove_with_fixture[force]` | Current installed f909 L2: unused imported image removed with force and checked cleanup; in-use force behavior remains separate. |
| `image rm` blocked by VM dependency | ✅ Deep | `images/test_images.py` | `TestImageDependencyDeletion::test_delete_image_used_by_stopped_vm_fails` | Current installed d13 L2: Running/Stopped variants returned image.in_use, preserving record/backing identity/VM state; exact owned cleanup and original/prepared inventories passed. Force removal and default-promotion behavior remain separate. |
| `image rm` after explicitly replacing its default | ✅ Deep | `images/test_images.py` | `TestImageDependencyDeletion::test_delete_default_image_promotes_other_or_clears` | Current installed d13 L2: replacement selected before deleting the formerly-default owned import; retained replacement and exact original zero-default cleanup passed. Historical test name does not prove automatic promotion. |
| `image warm` | ⚡ Shallow | `images/test_images.py` | `TestImageWarm` | Installed rc.7: schema-1 path is a regular ready-pool file of the image's uncompressed size. |
| `image warm --all` | ⚡ Shallow | `images/test_images.py` | `TestImageWarm` | Installed rc.7: warmed/ready message for cached image. |
| `image warm` by ID prefix | ⚡ Shallow | `images/test_images.py` | `TestImageWarm` | Installed rc.7: prefix resolves and reports warmed/ready. |
| `image warm` nonexistent | ⚡ Shallow | `images/test_images.py` | `TestImageWarm` | Installed rc.7: command rejects missing image. |
| `image import` | ⚡ Shallow | `images/test_images.py` | `TestImageImport::test_image_import_local_file` | Current installed f909: cached Alpine raw import, ext4/Alpine record and checked removal; this case does not boot the image. |
| `image import --format qcow2` | 🟡 Partial | `images/test_images.py` | `TestImageImportAdvanced::test_image_import_with_format_qcow2` | Installed current-candidate L2: cached Alpine raw→QCOW2→ext4 import booted as a networkless VM; guest OS identity read through vsock and checked VM/image/temp-file removal. Other formats and failure paths remain unqualified; QCOW2 data volumes are unsupported. |
| `image import --root-partition` | ⏭️ Skip | `images/test_images.py` | `TestImageImportAdvanced` | L2 — skips on qemu-img |
| `image import --force` | ⏭️ Skip | `images/test_images.py` | `TestImageImportAdvanced` | L2 — may skip |
| `image import --default` | ⚡ Shallow | `images/test_images.py` | `TestImageImportSetDefault::test_image_import_with_set_default` | Current installed f909 L2: fresh raw ext4 import selects the returned ID as sole default; owned image removal and prepared-default restoration checked. Does not prove boot. |
| `image import` arch auto-detection | ⏭️ Skip | `images/test_images.py` | `TestImageImportArch` | L2 — skips on qemu-img. No `--arch` flag — arch is auto-detected by Go CLI. |
| `image import` auto-detect | ⏭️ Skip | `images/test_images.py` | `TestImageImportAdvanced` | L2 — may skip |
| `image import --skip-optimization` | 🟡 Partial | `images/test_images.py` | `TestImageImportAdvanced` | L2 — imported with skip-optimization flag, may skip |
| `image import --disable-detector` | 🟡 Partial | `images/test_images.py` | `TestImageImportAdvanced` | L2 — imported with --disable-detector arch, may skip on missing cached image |
| `image import --version` | ⚡ Shallow | `images/test_images.py` | `TestImageImportAdvanced::test_image_import_with_version` | L2 — version field matches in ls --json |
| `image import` nonexistent path | ⚡ Shallow | `images/test_images.py` | `TestImageImport::test_import_from_nonexistent_path_fails` | Current installed f909: schema-1 `image.import.failed`, source-not-found message and unchanged image inventory. |
| Full import→VM-create end-to-end | 🟡 Partial | `images/test_images.py` | `TestImageImportCreateVM` | Current named-runner L2 passed protected-rootfs imports from Running, Paused and Stopped sources, preserved each source state and booted independent clones with exact guest-written bytes. Export interruption/recovery and the remaining class cases are separately unqualified. |
| Default migrates on force re-pull | ⏭️ Skip | `images/test_images.py` | `TestImageDefaultMigration` | L2 — many skip points |

---

## `mvm kernel`

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `kernel ls --json` | ⚡ Shallow | `kernel/test_kernel.py` | `TestKernelLifecycle` | Installed rc.7: strict schema-1 list with a present registered kernel. |
| `kernel ls --json` empty | ⚡ Shallow | `kernel/test_kernel.py` | `TestKernelLifecycle` | Named-runner d13/e2fd: registered singleton passed strict1/1; private initialized cache returns schema-1 success with `data: []`, typed cleanup and original-state preservation passed. |
| `kernel ls --remote` | ⚡ Shallow | `kernel/test_kernel.py` | `TestKernelLifecycle` | L1: exit 0, non-empty JSON list |
| `kernel pull --type firecracker` | ⚡ Shallow | `kernel/test_kernel.py` | `TestKernelLifecycle` | L1 |
| `kernel pull --type official` | ⏭️ Skip | `kernel/test_kernel.py` | `TestKernelLifecycle` | L1 — marked kernel_build (can skip) |
| `kernel pull` with `--version` | ⚡ Shallow | `kernel/test_kernel.py` | `TestKernelPullWithVersion` | L2: pull specific firecracker versions v1.15, v1.14, verify via ls --json |
| `kernel pull` arch auto-detection | ⏭️ Skip | `kernel/test_kernel.py` | `TestKernelPullArch` | Network-dependent. L2: ls --json. No `--arch` flag — arch is auto-detected by Go CLI. |
| `kernel pull` advanced flags | 🟡 Partial | `kernel/test_kernel.py` | `TestKernelPullAdvancedFlags` | L2: pull with --type official, verify arch auto-detection |
| `kernel pull --jobs` | 🟡 Partial | `kernel/test_kernel.py` | `TestKernelBuild` | kernel_build marker, may skip. L1: success |
| `kernel pull --keep-build-dir` | 🟡 Partial | `kernel/test_kernel.py` | `TestKernelBuild` | kernel_build marker, may skip. L1: success |
| `kernel pull --clean-build` | 🟡 Partial | `kernel/test_kernel.py` | `TestKernelBuild` | kernel_build marker, may skip. L2: listing check |
| `kernel pull --config` | 🟡 Partial | `kernel/test_kernel.py` | `TestKernelBuild` | L2 — kernel_build marker, may skip |
| `kernel pull --features` | 🟡 Partial | `kernel/test_kernel.py` | `TestKernelBuild` | L1 — kernel_build marker, may skip |
| `kernel ls --no-cache` | ⚡ Shallow | `kernel/test_kernel.py` | `TestKernelLifecycle` | L1 — exit 0 |
| `kernel inspect` | ⚡ Shallow | `kernel/test_kernel.py` | `TestKernelInspect` | Installed rc.7: human output names the selected present kernel and version. |
| `kernel inspect --json` | ⚡ Shallow | `kernel/test_kernel.py` | `TestKernelInspect` | Installed rc.7: schema-1 inspect identity and storage path match the listed kernel. |
| `kernel default` | ⚡ Shallow | `kernel/test_kernel.py` | `TestKernelLifecycle` | Installed rc.7: transfer to a test-owned import, then restore prior default and remove only that import. |
| `kernel rm <id>` | ⚡ Shallow | `kernel/test_kernel.py` | `TestKernelRemove` | Installed rc.7: remove only a uniquely imported ID prefix; verify absence and preserve the pre-seeded source. |
| `kernel rm --force` on referenced kernel | 🔴 Missing | — | — | Prior test selected an unreferenced kernel, so it did not test force semantics; a VM-owned reference case remains unqualified. |
| `kernel rm <nonexistent>` | ⚡ Shallow | `kernel/test_kernel.py` | `TestKernelRemove` | L1 — non-zero exit, error message |
| `kernel rm` blocked by stopped VM | 🟡 Partial | `kernel/test_kernel.py` | `TestKernelStoppedVMDeletion` | Focused installed-candidate L2 passed on the bridge-capable named runner: `kernel.in_use` preserved the stopped VM and its test-owned kernel; checked teardown left no test resource. Forced removal of a referenced kernel is separate. |
| `kernel pull` then `rm` lifecycle | 🔴 Missing | — | — | Former case removed the first available kernel and unrelated referencing VMs; a safely owned pull/remove journey remains to be written. |
| `kernel import` | ⚡ Shallow | `kernel/test_kernel_import.py` | `TestKernelImportLifecycle` | Installed rc.7: mirrored vmlinux import, exact copied bytes, schema-1 list/inspect, checked test-owned removal; VM boot remains unproved. |
| `kernel import --version` | ⚡ Shallow | `kernel/test_kernel_import.py` | `TestKernelImportLifecycle` | Installed rc.7: explicit version matches list and inspect. |
| `kernel import` arch in listing | ⚡ Shallow | `kernel/test_kernel_import.py` | `TestKernelImportLifecycle` | Installed rc.7: imported arch appears in list and inspect; no `--arch` flag. |
| `kernel import` version auto-detect | ⚡ Shallow | `kernel/test_kernel_import.py` | `TestKernelImportAutoVersion` | Installed rc.7: version from exact `vmlinux-6.1-x86_64` filename matches import/list/inspect; copied file exists; no VM boot. |
| `kernel import` error and repeat paths | ⚡ Shallow | `kernel/test_kernel_import.py` | `TestKernelImportError` | Installed rc.7: nonexistent path and empty name reject; same bytes/version re-import resolves to one content ID. |
| `kernel rm` test-owned imported ID | ⚡ Shallow | `kernel/test_kernel_import.py` | Import cases | Installed rc.7: each focused import removes only its returned ID and checks absence; pre-seeded source remains. Broad all-custom-kernel cleanup removed. |
| `kernel import --default` | ⚡ Shallow | `kernel/test_kernel_import.py` | `TestKernelImportDefault` | Installed rc.7: sole default transfers to imported ID and is restored to the previous ID after checked removal. |

---

## `mvm bin`

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `bin ls --json` | ⚡ Shallow | `bin/test_bin.py` | `TestBinLifecycle` | L2 — version, id, is_present |
| `bin ls --json` empty | ❌ Missing | — | — | The existing list case requires a present pair; an isolated empty-cache projection case remains unqualified. |
| `bin ls --remote` | ⚡ Shallow | `bin/test_bin.py` | `TestBinaryEdges::test_bin_ls_remote_limit_applies_to_json` | Installed candidate fetches schema-1 remote release metadata in the networked runner; no asset archive is downloaded. |
| `bin ls --remote --limit` | ⚡ Shallow | `bin/test_bin.py` | `TestBinaryEdges::test_bin_ls_remote_limit_applies_to_json` | Focused L2: three JSON entries versus more than three without the limit; versions and Firecracker metadata checked. |
| `bin pull <version>` | 🟡 Partial | `bin/test_bin.py` | `TestBinaryEdges::test_bin_pull_present_exact_version_from_mirror`, `TestBinaryPullAndLifecycle::test_bin_pull_and_set_default` | Installed candidate re-admits a present pair and installs a mirrored release into an absent root slot; different-payload replacement and well-formed missing-version behavior remain unqualified. |
| `bin pull --force` | 🟡 Partial | `bin/test_bin.py` | `TestBinaryLiveVMReleaseLease::test_force_remove_denied_and_identical_pull_keeps_live_release` | Focused L2: an identical mirrored force-pull preserves the live pair; genuinely different-payload replacement remains unqualified. |
| `bin pull --default` | ⚡ Shallow | `bin/test_bin.py` | `TestBinaryPullAndLifecycle::test_bin_pull_and_set_default` | Focused L2: both defaults transfer to a fresh exact pair, then the prior pair is restored; independent default selection is not claimed. |
| Removed pull flags | ⚡ Shallow | `bin/test_bin.py` | `TestBinaryEdges::test_bin_pull_removed_git_ref_rejected_before_effects` | Installed candidate rejects `--git-ref` before changing the release listing; other retired selector coverage remains open. |
| `bin pull` malformed version | ⚡ Shallow | `bin/test_bin.py` | `TestBinaryEdges::test_bin_pull_invalid_version_rejected_before_effects` | Installed candidate returns typed version validation error before network or release changes. |
| `bin pull` well-formed absent version | ❌ Missing | — | — | The former case only exercised a removed `--version` flag; an absent exact slot requires a controlled missing-asset source, not an unmirrored download attempt. |
| `bin rm <exact-version>` | 🟡 Partial | `bin/test_bin.py` | `TestBinaryPullAndLifecycle::test_bin_pull_and_set_default`, `TestBinaryLiveVMReleaseLease::test_force_remove_denied_and_identical_pull_keeps_live_release` | Focused L2: typed root removal of a test-owned present slot and conflict for a live referenced slot; broader removal paths remain unqualified. |
| Removed rm selectors | ⚡ Shallow | `bin/test_bin.py` | `TestBinaryEdges::test_bin_rm_removed_version_flag_rejected_before_effects`, `TestBinaryPullAndLifecycle::test_bin_rm_rejects_binary_id_without_effects` | Installed candidate rejects retired `--version` and binary-ID selectors without changing the release listing. |
| `bin rm <absent exact-version>` | ⚡ Shallow | `bin/test_bin.py` | `TestBinaryEdges::test_bin_rm_exact_absent_version_is_skipped` | Installed candidate returns typed `binary.remove_absent`/`skipped` and leaves the release listing unchanged. |
| Root reference denial | ⚡ Shallow | `bin/test_bin.py` | `TestBinaryLiveVMReleaseLease::test_force_remove_denied_and_identical_pull_keeps_live_release` | Installed candidate denies exact release removal with `--force` while a networkless VM is running; unchanged release listing and VM state checked. An identical force-pull is permitted without changing the pair. Cleaned stopped VMs are not root-authority references. |
| `bin default <id>` | 🟡 Partial | `bin/test_bin.py` | `TestBinaryPullAndLifecycle::test_bin_pull_and_set_default` | Focused L2: both original pair defaults restored by exact IDs after fresh install; independent default-selection behavior remains unqualified. |
| `bin default nonexistent` | ⚡ Shallow | `bin/test_bin.py` | `TestBinaryEdges::test_bin_default_missing_id_rejected_before_effects` | Installed candidate returns typed `binary.default_set_failed` and preserves the release listing and defaults. |
| Release survives cache clean | ❌ Missing | — | — | The obsolete service-symlink XFAIL was removed; it did not test the root-owned release store. Run this destructive cache case only in a dedicated disposable runner. |

---

## `mvm ssh`

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `ssh <vm> --cmd <cmd>` | ⚡ Shallow | `ssh/test_ssh.py` | `TestSSHConnect` | Installed-candidate L2: stored-key authentication and exact guest command output; whole domain passed twice. |
| `ssh <vm> -u <user> --cmd` | ⚡ Shallow | `ssh/test_ssh.py` | `TestSSHConnect` | L2: explicit user and IP selection return exact root identity. |
| `ssh <vm> --cmd exit` | ⚡ Shallow | `ssh/test_ssh.py` | `TestSSHConnect` | L2: named-key authentication exits successfully, then a separate command proves guest output. |
| `ssh <vm> --key <name>` | ⚡ Shallow | `ssh/test_ssh.py` | `TestSSHConnect` | L2: authorized named key succeeds; no conditional pass or skip. |
| `ssh <vm> --key <path>` | ⚡ Shallow | `ssh/test_ssh.py` | `TestSSHConnect` | L2: exact exported private bytes authenticate; an unauthorized key is refused while valid access remains healthy. |
| `ssh <vm> --timeout <sec>` | ⚡ Shallow | `ssh/test_ssh.py` | `TestSSHConnect` | L2: a three-second guest command succeeds with a two-second connect budget; unknown selectors fail before probing. |

---

## `mvm console`

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `console <vm> --state` | ✅ Deep | `console/test_console.py` | `test_console_active_relay_journey`, `test_console_pid_mirror_cannot_redirect_discovery_or_stop` | Installed-candidate L2: root/inspect agreement, relay identity and caller PID-mirror independence. |
| `console <vm> --kill` | ✅ Deep | `console/test_console.py` | `test_console_active_relay_journey`, `test_console_pid_mirror_cannot_redirect_discovery_or_stop` | Installed-candidate L2: real relay exits; pinned Firecracker and forged-PID decoy survive; fresh exec succeeds. |
| `console <nonexistent> --state` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMListInspect::test_console_state_nonexistent_vm` | Installed-candidate L2 passed schema-1 `vm.not_found` rejection with no result data. |
| `console <ip> --state` | ✅ Deep | `console/test_console.py` | `test_console_active_relay_journey` | Real IPv4 selector reports the same relay before and after VM restart. |
| `console lifecycle: state → kill → state` | ✅ Deep | `console/test_console.py` | `test_console_active_relay_journey` | Relay-only stop, repeated-stop rejection, inactive attach refusal and renewed relay after VM restart. |
| `console on stopped VM` | ✅ Deep | `console/test_console.py` | `test_console_active_relay_journey` | Root-observed inactive state and actual attachment refusal after typed VM stop. |
| `console live PTY attachment` | ✅ Deep | `console/test_console.py` | `test_console_tty_attachment_serial_bytes` | Unique guest serial delivery, detach, terminal restoration and preserved VM liveness. |

---

## `mvm logs`

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `logs <vm>` | ⚡ Shallow | `logs/test_logs.py` | `TestLogsBasic` | L1 — non-empty stdout |
| `logs <vm> --os` | ⚡ Shallow | `logs/test_logs.py` | `TestLogsBasic` | L1 — non-empty OS log output |
| `logs <vm> --lines 5` | ⚡ Shallow | `logs/test_logs.py` | `TestLogsBasic` | L1 — at most 5 lines |
| `logs <ip>` (by IP) | ⚡ Shallow | `logs/test_logs.py` | `TestVMLogsByIdentifier` | L1 — returncode 0 |
| `logs --follow` / `-f` | ⚡ Shallow | `logs/test_logs.py` | `TestVMLogs` | L1: brief timeout, verifies no crash |
| `logs --os --follow` | ⚡ Shallow | `logs/test_logs.py` | `TestVMLogs` | Combined flags |
| `logs <nonexistent>` | ⚡ Shallow | `logs/test_logs.py` | `TestVMLogs` | Error handling |
| `logs --lines multiple values` | ⚡ Shallow | `logs/test_logs.py` | `TestVMLogs` | Tested with --lines 5 and --lines 50 |
| `logs by IP fails for stopped VM` | ⚡ Shallow | `logs/test_logs.py` | `TestVMLogs` | Stopped VM edge case |

---

## `mvm host`

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `host info` | ⚡ Shallow | `host/test_host.py` | `TestHostInfo` | L2 — human-readable sections plus a separate fresh-cache first-read capacity scenario |
| `host info --json` | ✅ Deep | `host/test_host.py` | `TestHostInfo`, `TestHostStatusEnhanced` | L2 — schema-1 success envelope; CPU, virtualization, hugepages, dependencies (nftables, cloud-localds, TUN), system, memory, storage, limits, and capacity; no caller-cache-derived `setup` verdict |
| `host info --refresh` | ⚡ Shallow | `host/test_host.py` | `TestHostInfo` | L2 — explicit re-detection through the installed candidate |
| `host info --refresh --json` | ✅ Deep | `host/test_host.py` | `TestHostInfo`, `TestHostStatusEnhanced` | L2 — timestamp and typed nested sections through the schema-1 success envelope |
| `host status` | ⚡ Shallow | `host/test_host.py` | `TestHostStatus` | L2 — human-readable KVM and forwarding checks |
| `host status --json` | ✅ Deep | `host/test_host.py` | `TestHostStatus`, `TestHostStatusEnhanced` | L2 — schema-1 success envelope; `kvm_accessible`, `missing_binaries`, forwarding, live `resources`, and null `state` rather than a user-SQLite setup claim |
| `host clean --force` success | 🔴 Gap | — | — | Requires a dedicated clean-host context; the shared runner must not erase its installed candidate or root state. |
| `host clean` blocked by running VM | ⚡ Shallow | `host/test_host.py` | `TestHostMutationGuards` | L2 creates one bounded VM, proves it is live, and checks refusal without deleting it. |
| `host reset --force` success | ✅ Deep | `host/test_host_admin.py` | `TestCleanHostAdministration` | Installed e034 candidate passed corrected quiescent reset/re-init/same-ID pending-network retry and separate second-operator/active-network reset refusal, with checked cleanup and preservation. Filesystem-retry and abrupt-interruption cases remain unqualified. The shared runner is not reset. |
| unprivileged `host reset --force` | ⚡ Shallow | `host/test_host.py` | `TestHostMutationGuards` | L2 checks administrator refusal and unchanged host-status fields. |
| `host install-system` | ✅ Deep | `host/test_system_install.py` | `TestSystemBinaryInstall` | The Python runner stages the RC, invokes the real administrator installer, and then all `_run_mvm` calls use exact `/usr/local/bin/mvm`. L2 verifies root ownership, mode, trusted ancestors, content hash, no temporary sibling, and idempotence. |
| `host install-system` rejection cases | ✅ Deep | `host/test_system_install.py` | `TestSystemBinaryInstall` | L2 verifies unprivileged and malformed invocations preserve the target, and an existing target symlink is rejected without changing its destination. Symlink-test restoration is mandatory and asserted. |
| `host install-system` pre-rename failure | ✅ Deep | `host/test_system_install.py`, `internal/core/host/system_binary_test.go` | `TestSystemBinaryInstall`, `TestInstallSystemBinary_PreRenameFailuresPreserveTarget` | L1 covers each injected pre-rename stage. L2 bind-remounts `/usr/local/bin` read-only, invokes the exact staged candidate, verifies the public failure and unchanged canonical metadata/hash, then requires rw remount, unmount, no mount leak, and executable restoration. |
| `host install-system` remaining partial failures | ⚡ Shallow | `internal/core/host/system_binary_test.go`, `pkg/api/host_system_binary_test.go` | `TestInstallSystemBinary_PreRenameFailuresPreserveTarget`, `TestInstallSystemBinary_ReportsPostRenameAndCleanupFailures`, `TestHostInstallSystemBinary_PreservesPartialSuccess` | L1 deterministically covers write/metadata/fsync/close/rename failures, cleanup failure, and post-rename durability reporting. No safe deterministic public-boundary L2 injection exists for those individual stages; uncontrolled disk filling, timing kills, and production test hooks are explicitly rejected. |
| `host init` | 🟡 Partial | `host/test_system_install.py` | `TestSystemBinaryInstall` | L2 invokes exact `sudo /usr/local/bin/mvm host init` in a runner that already has the `mvm` group and membership. It proves happy-path sudoers reconciliation and invoking-user cache/config ownership. Task 13 gaps: group creation/membership, root-owned journal, rollback, and fail-closed mutation errors. |

---

## `mvm cache`

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `cache init` | ⚡ Shallow | `cache/test_cache.py` | `TestCacheInit` | L1 |
| `cache init` idempotent | ⚡ Shallow | `cache/test_cache.py` | `TestCacheInit` | L1 |
| Enabled GuestFS appliance build/reuse/repair and selected-cache pruning | ✅ Deep | `cache/test_guestfs_appliance.py` | `test_enabled_build_reuse_and_incomplete_rebuild` | Installed 562b38de: strict 1/1 in 68.18s, real construction/launch/reuse/repair, typed cleanup and unrelated-state preservation passed in the named runner. Fresh reusable-base qualification remains separate. |
| `cache prune` (no resource) | ⚡ Shallow | `cache/test_cache.py` | `TestCacheEdgeCases` | L1 |
| `cache prune vm --force` | ⚡ Shallow | `cache/test_cache.py` | `TestCacheEdgeCases` | L2 — VM gone after prune |
| `cache prune --all --dry-run` | ⚡ Shallow | `cache/test_cache.py` | `TestCachePruneDryRun` | L1 |
| `cache clean --dry-run` | ⚡ Shallow | `cache/test_cache.py` | `TestCacheClean` | L1 |
| `cache clean --force` | ⏭️ Skip | `cache/test_cache.py` | `TestCacheClean` | L1 — may skip if destructive |
| Cache prune edge cases | ⚡ Shallow | `cache/test_cache.py` | `TestCachePruneEdgeCases` | L1: nonexistent category, prune without --all fails with guidance |
| Cache prune misc (temp files) | ⚡ Shallow | `cache/test_cache.py` | `TestCachePruneActual` | L1: prune misc cache with/without --force, verify cache init still works |
| Cache prune non-dry-run (network, kernel, binary, image) | ✅ Deep | `cache/test_cache.py` | `TestCachePruneNonDryRun` | L3: prune network (bridge removal), kernel (file removal), binary (file removal), image --all |
| Cache prune --all | ⚡ Shallow | `cache/test_cache.py` | `TestCachePruneAll` | L2: prune --all --force, verify VMs/images empty |
| Cache clean destructive (DB destroy + recovery) | ✅ Deep | `cache/test_cache.py` | `TestZzzDestructive`, `TestCacheCleanActual` | L3: clean --force destroys SQLite DB + assets, re-init + re-pull recovers |
| `cache clean --force` destructive recovery | ✅ Deep | `cache/test_cache.py` | `TestCacheCleanActual` | L3: clean --force, verify recovery of DB, binary default, kernel default, image pull, network |

---

## `mvm cp`

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `cp <src> <dst>` (host→VM, file) | ✅ Deep | `cp/test_cp.py` | `TestCpHostToVm`, `TestCpNetworklessRoundTrip` | Focused current-candidate networkless L2 copied exact bytes into the guest and read them through vsock; existing managed-network case remains separately unqualified on this runner. |
| `cp <src> <dst>` (host→VM, dir) | ✅ Deep | `cp/test_cp.py` | `TestCpHostToVm` | L3: dir + nested files exist on VM via SSH |
| `cp <src> <dst>` (VM→host, file) | ✅ Deep | `cp/test_cp.py` | `TestCpVmToHost`, `TestCpNetworklessRoundTrip` | Focused current-candidate networkless L2 copied the guest file back and matched exact bytes; existing managed-network case remains separately unqualified on this runner. |
| `cp <src> <dst>` (VM→host, dir) | ✅ Deep | `cp/test_cp.py` | `TestCpVmToHost` | L3: dir exists on host with contents verified |
| `cp <src> <dst>` nonexistent source | ⚡ Shallow | `cp/test_cp.py` | `TestCpEdgeCases` | L1: "not found" in error |
| `cp <src> <dst>` with `--force` | ✅ Deep | `cp/test_cp.py` | `TestCpEdgeCases` | L3: content changed after overwrite, verified via SSH |
| `cp <src> <dst>` no `--force` dest exists | ⚡ Shallow | `cp/test_cp.py` | `TestCpEdgeCases` | L1: non-zero exit, error mentions exists/force |
| `cp <src> <dst>` (multi-source, two files) | ✅ Deep | `cp/test_cp.py` | `TestCpMultiSource` | L3: both files exist on VM with correct content via SSH |
| `cp <src> <dst>` (multi-source, file+dir) | ✅ Deep | `cp/test_cp.py` | `TestCpMultiSource` | L3: file and directory (with nested content) exist on VM via SSH |
| `cp <src> <dst>` (multi-source, single arg) | ✅ Deep | `cp/test_cp.py` | `TestCpMultiSource` | L3: file transferred correctly via SSH — backward compat with multi-source path |
| `cp <src> <src> <local-dest>` (multi-source rejects non-VM) | ⚡ Shallow | `cp/test_cp.py` | `TestCpMultiSource` | L1: non-zero exit, error mentions multi-source requires VM dest |
| `cp vm1:/src vm2:/dst` (VM→VM) | 🟡 Partial | `cp/test_cp.py` | `TestCpVmToVm` | L3: file on VM2 via SSH, may skip if SSH unavailable |

> **Note:** `cp` uses vsock binary protocol, not SSH. The `--user` and `--key` flags do not exist on `cp` (they belong on `ssh`).

---

## `mvm env` (alias: `up`/`down`)

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `env apply <spec>` (basic net+key) | ✅ Deep | `env/test_env.py` | `TestEnvApply::test_apply_basic_spec` | L3 — resources verified via `ls --json` |
| `env apply` re-apply (idempotent) | ✅ Deep | `env/test_env.py` | `TestEnvApply::test_apply_reapply` | L3 — two applies succeed |
| `env apply` nonexistent spec | ⚡ Shallow | `env/test_env.py` | `TestEnvApply::test_apply_nonexistent_spec` | Error: "not found" |
| `env apply` invalid YAML | ⚡ Shallow | `env/test_env.py` | `TestEnvApply::test_apply_invalid_yaml` | Error: invalid YAML |
| `env apply` empty spec | ⚡ Shallow | `env/test_env.py` | `TestEnvApply::test_apply_empty_spec` | Error: "no resources" |
| `env ls` (empty) | ⚡ Shallow | `env/test_env.py` | `TestEnvLs::test_ls_empty` | "No saved environments found" |
| `env ls` (after apply) | ⚡ Shallow | `env/test_env.py` | `TestEnvLs::test_ls_after_apply` | Lists WF ID + spec path |
| `env ls` (after destroy) | ⚡ Shallow | `env/test_env.py` | `TestEnvLs::test_ls_after_destroy` | Empty after destroy |
| `env diff` (no diff) | ✅ Deep | `env/test_env.py` | `TestEnvDiff::test_diff_after_apply` | "No differences" |
| `env diff` (drifted spec) | ✅ Deep | `env/test_env.py` | `TestEnvDiff::test_diff_drifted` | Drifted resource detected |
| `env diff` (new resource) | ✅ Deep | `env/test_env.py` | `TestEnvDiff::test_diff_new_resource` | New + Existing listed |
| `env diff` (removed resource) | ✅ Deep | `env/test_env.py` | `TestEnvDiff::test_diff_removed_resource` | Removed + Existing listed |
| `env diff` nonexistent spec | ⚡ Shallow | `env/test_env.py` | `TestEnvDiff::test_diff_nonexistent_spec` | Error |
| `env destroy <spec-path>` | ✅ Deep | `env/test_env.py` | `TestEnvDestroy::test_destroy_by_spec_path` | L3 — resources gone after destroy |
| `env destroy <wf-id>` | ✅ Deep | `env/test_env.py` | `TestEnvDestroy::test_destroy_by_workflow_id` | L3 — destroy by parsed WF ID |
| `env destroy` nonexistent | ⚡ Shallow | `env/test_env.py` | `TestEnvDestroy::test_destroy_nonexistent` | "no saved workflow state found" |
| `env destroy` twice | ⚡ Shallow | `env/test_env.py` | `TestEnvDestroy::test_destroy_twice` | First succeeds, second fails |
| `env --help` | ⚡ Shallow | `env/test_env.py` | `TestEnvHelp::test_env_help` | Shows Usage, apply, ls, diff, destroy |
| `env apply --help` | ⚡ Shallow | `env/test_env.py` | `TestEnvHelp::test_env_apply_help` | Shows Usage |
| `env destroy --help` | ⚡ Shallow | `env/test_env.py` | `TestEnvHelp::test_env_destroy_help` | Shows Usage |
| `env diff --help` | ⚡ Shallow | `env/test_env.py` | `TestEnvHelp::test_env_diff_help` | Shows Usage |
| Full lifecycle (apply→verify→diff→destroy→verify) | ✅ Deep | `env/test_env.py` | `TestEnvLifecycle::test_full_lifecycle` | L3 — complete end-to-end cycle |

---

## `mvm run` (internal service subprocesses)

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `run nocloudnet serve --help` and missing required flags | ⚡ Shallow | `run/test_run.py` | `TestRunEntryPoints` | L2 — installed CLI help and required-identity rejection |
| `run console relay --help` and missing required flags | ⚡ Shallow | `run/test_run.py` | `TestRunEntryPoints` | L2 — installed CLI help and required-identity rejection |
| Retired `run provision` absent | ⚡ Shallow | `run/test_run.py` | `TestRunEntryPoints::test_run_help` | L2 — public command listing omits the removed route |
| Jailer launch/cleanup through public VM lifecycle | 🟡 Partial | `vm/test_jailer.py` | `TestCanonicalJailerLaunch`, `TestNetworklessJailerLifecycle` | The full 13-case Jailer module passed in the bridge-capable named runner, including managed launch, isolated process identity, stop/start/reboot, and networkless restore. Host-reboot and broader failure-path reconciliation remain open. |

> Internal service subprocesses are tested indirectly through VM operations (console relay, nocloudnet HTTP, volume provisioning).

---

## `mvm full-journeys` (end-to-end workflows)

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| Quick-start journey (create key+net+VM, SSH, cleanup) | ⚡ Shallow | `full_journeys/test_full_journeys.py` | `TestQuickStartJourney` | Installed-candidate L2: create owned key/network/VM, authenticated SSH, checked cleanup and baseline preservation. |
| Network→VM journey | ⚡ Shallow | `full_journeys/test_full_journeys.py` | `TestNetworkVMJourney` | L2: create network, VM on it, verify via ls --json |
| Key→VM journey | ⚡ Shallow | `full_journeys/test_full_journeys.py` | `TestKeyVMJourney` | L2: create key, VM with key, verify via ls --json |
| VM state journey (create→pause→resume→stop→start) | ⚡ Shallow | `full_journeys/test_full_journeys.py` | `TestVMStateJourney` | L2: full state transitions, ls --json status at each step |
| IP journey (explicit --ip + SSH) | ⚡ Shallow | `full_journeys/test_full_journeys.py` | `TestIPJourney` | Installed-candidate L2: requested IP in public/guest readback and SSH; two distinct peer IPs and independent keyed SSH. |
| SSH journey (command + reboot) | ⚡ Shallow | `full_journeys/test_full_journeys.py` | `TestSSHJourney` | Installed-candidate L2: SSH execution before/after reboot, fresh process/start ticks and boot ID, retained VM/IP. |
| Multi-key VM journey | ⚡ Shallow | `full_journeys/test_full_journeys.py` | `TestMultiKeyJourney` | L2: one comma-separated --ssh-key argument, both names in inspect, actual SSH using each key. |
| Inter-VM communication | ⚡ Shallow | `full_journeys/test_full_journeys.py` | `TestInterVMCommunication` | L2: same-network TCP default denial, exact source-VM grant and echoed payload, revoke denial with listener still healthy. Historical ping selector is retained; ICMP is not the v0.3.0 policy contract. |
| Create with all flags | ⚡ Shallow | `full_journeys/test_full_journeys.py` | `TestCreateWithAllFlags` | L2: --vcpu, --mem, -s, --enable-logging, --enable-metrics |
| Multiple volumes | ⚡ Shallow | `full_journeys/test_full_journeys.py` | `TestMultipleVolumes` | L2: three 16 MiB raw volumes, exact inspect bindings and guest vdb/vdc/vdd extents, SSH and checked cleanup. |
| Stress create/destroy | ⚡ Shallow | `full_journeys/test_full_journeys.py` | `TestStressCreateDestroy` | L2: 5 sequential create→destroy cycles |
| Export/import config journey | ⚡ Shallow | `full_journeys/test_full_journeys.py` | `TestExportImport` | L2: inspect --json schema verification |
| Concurrent VM creation | ⚡ Shallow | `full_journeys/test_full_journeys.py` | `TestConcurrentVMCreation` | Installed-candidate L2: ten distinct creations in concurrent cohorts of 3/3/3/1, running-ID convergence and keyed SSH for every VM, checked cohort removal before reuse. This does not prove ten simultaneous live guests. |
| Canonical Jailer lifecycle | 🟡 Partial | `vm/test_jailer.py` | `TestCanonicalJailerLaunch`, `TestNetworklessJailerLifecycle` | Canonical cases are selected once in the Jailer domain, not re-exported by full_journeys. The full 13-case Jailer module passed in the bridge-capable named runner, covering managed launch/stop/start/reboot and networkless identity, volume, snapshot, and missing-release checks. Host-reboot and broader failure-path reconciliation remain open. |
| Typed cgroup-v2 envelope | 🟡 Partial | `vm/test_cgroup.py`, `vm/test_jailer.py` | `TestVMCgroupEnvelope`, `TestCanonicalJailerLaunch` | Installed-candidate L2: protected policy-v2 inspection, direct-read denial and sustained measured-pressure survival with host swap absent/present; complete cgroup module passed twice with exact cleanup. Literal memory.max=max and unchanged other controls. Snapshot policy, controller-unavailable admission, guest allocation failure, host/global OOM safety and broader identity cleanup remain unqualified. |

---

## Summary Statistics

| Category | Total Scenarios | ✅ Deep | ⚡ Shallow | 🟡 Partial | 🔴 Missing | ⏭️ Skip |
|----------|----------------|---------|-------------|------------|-------------|----------|
| Root CLI | 7 | 0 | 7 | 0 | 0 | 0 |
| self-update | 4 | 1 | 0 | 0 | 3 | 0 |
| init | 5 | 0 | 4 | 1 | 0 | 0 |
| config | 12 | 0 | 12 | 0 | 0 | 0 |
| network | 35 | 14 | 21 | 0 | 0 | 0 |
| vm | 94 | 18 | 71 | 5 | 0 | 0 |
| exec | 8 | 0 | 8 | 0 | 0 | 0 |
| snapshot | 13 | 1 | 12 | 0 | 0 | 0 |
| volume | 35 | 4 | 29 | 2 | 0 | 0 |
| key | 28 | 0 | 28 | 0 | 0 | 0 |
| image | 41 | 0 | 16 | 3 | 3 | 19 |
| kernel | 29 | 0 | 18 | 7 | 2 | 2 |
| bin | 17 | 2 | 6 | 1 | 0 | 8 |
| ssh | 6 | 0 | 6 | 0 | 0 | 0 |
| console | 6 | 0 | 6 | 0 | 0 | 0 |
| logs | 9 | 0 | 9 | 0 | 0 | 0 |
| host | 15 | 6 | 7 | 1 | 0 | 1 |
| cache | 13 | 3 | 9 | 0 | 0 | 1 |
| cp | 12 | 8 | 3 | 1 | 0 | 0 |
| env | 22 | 9 | 13 | 0 | 0 | 0 |
| run | 4 | 1 | 3 | 0 | 0 | 0 |
| full-journeys | 15 | 0 | 15 | 0 | 0 | 0 |
| **Total** | **430** | **67** | **303** | **21** | **8** | **31** |

**Current-row coverage health:**
- ✅ Deep (L3): 67/430 = 15.6%
- ⚡ Shallow (L0-L2): 303/430 = 70.5%
- 🟡 Partial: 21/430 = 4.9%
- 🔴 Missing: **8/430 = 1.9%** — three user-owned `self-update` success/check paths, two remote image-list
  flag cases, the phantom `image inspect --tree` entry, referenced-force removal,
  and safely owned pull/remove lifecycle
- ⏭️ Skip-prone: 31/430 = 7.2%

These counts inventory rows for the transitional CLI only. They do not include the separate v0.3.0 security blockers in
the qualification overlay. Successful user-owned `self-update` check/apply behavior remains unqualified at L2. The
`image inspect --tree` missing entry is a phantom command because the CLI has no such flag.

---

**QA audit — 2026-08-23:**

- Recounted the current command rows directly: 431 total, with the exact status totals above.
- Added the v0.3.0 security overlay so legacy CLI breadth is not represented as clean-break release evidence.
- Confirmed that Jailer/cgroup tests do not yet verify root records, exact process identity, cross-UID denial, private
  mount/network namespaces, or complete cleanup.
- Confirmed that policy/full-journey tests still encode superseded dual-firewall and implicit same-network behavior.
- Confirmed that VM-to-VM typed exec, final marker-only sudoers denial, repeated zero-leak runs, and aggregated teardown
  failure reporting remain missing.

**System test machine requirements:**
To run the full system test suite with zero skips, the dedicated test machine must have:

| Dependency | Required By | Install |
|---|---|---|
| `qemu-img` | Image import tests (qcow2) | `apt install qemu-utils` |
| `mkfs.ext4` | Image import tests (ext4 formatting) | `apt install e2fsprogs` |
| `truncate` (coreutils) | Image import tests (sparse file) | `apt install coreutils` |
| `zstd` | Image decompression tests | `apt install zstd` |
| `gcc`, `make`, kernel headers | Kernel build tests (`kernel_build` marker) | `apt install build-essential linux-headers-$(uname -r)` |
| Network access | Image pull, bin pull, remote listing | Required for HTTP downloads |
| `/dev/kvm` | All VM creation tests | KVM-capable CPU + `kvm` kernel module |
| `mvm` group membership | All privileged operations | `sudo usermod -aG mvm $USER` |
| Exact `/usr/local/bin/mvm` plus separate candidate | v0.3 qualification | Install the release candidate through `host install-system`; keep the outer controller separate. |

**Prerequisite behavior on dedicated vs developer machines:**
- **Dedicated qualification machine:** every prerequisite is mandatory; release evidence requires zero skips, XFAILs,
  XPASSes, deselections, collection errors, or omitted domains.
- **Developer machine:** a focused run may fail prerequisite admission when dependencies are absent. Such a run is useful
  for diagnosis but cannot serve as release evidence.

**430 transitional CLI scenarios are documented. Eight rows are marked missing; the v0.3.0 qualification overlay lists
additional release-security gaps that must close before signoff.**
