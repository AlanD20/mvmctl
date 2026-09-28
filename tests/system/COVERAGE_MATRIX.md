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

**Interrupted VM backing-import cleanup source checkpoint (2026-09-28):** Commit `19376ffa`
allows the existing typed VM removal to cancel only verified incomplete VM-owned rootfs/cloud-init
copies after retained root VM authority proves process/tree/grant absence. The original staging file
is untouched. Cancellation refreshes durable intent before exact unlink, syncs the empty backing
directory, and retains the initialized removed record to prevent re-import. Unknown, changed,
nonempty unrecorded, mounted or foreign-granted files fail closed. Pinned Go 1.26.3 scoped format,
vet and root behavioral regressions (0.233 seconds), including interruption replay and actual mount
refusal, passed in `mvmctl-030-test`; the root race selection passed in 1.522 seconds. Evidence is
`ci-import-06c6/.tmp/import-06c6-focused-root.log`. Tests construct admitted backing fixtures;
end-to-end failed-creation/public `VMRemove` and actual power-loss qualification remain open.

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
| Installed system candidate and clean bootstrap | 🟡 Partial | Installed-candidate and happy-path init coverage exists; clean group/membership setup, root journal, rollback, fail-closed host mutation, and marker-only sudoers denial remain missing. |
| fd-0 privileged control transport | 🔴 Missing | Go L1 framing/EOF/peer/cancellation abuse coverage plus installed-CLI lifecycle integration. |
| Root VM authority and cross-UID denial | 🟡 Partial | A focused installed-candidate networkless case denies operator direct connection to the reported Firecracker API socket while `vm stop` succeeds; root-record/process/cache identity checks, constrained two-user denial, tombstone, and zero-leak assertions remain missing. |
| Independent trusted Firecracker/Jailer releases | 🔴 Missing | Root manifest/ELF/hash authority, atomic failure injection, and installed-CLI release lifecycle. |
| Descriptor-pinned resources and private mount namespace | 🔴 Missing | Type/owner/mode/link checks, replacement races, namespace mounts, cleanup, restart, and restore. |
| Mandatory per-VM network namespace | 🔴 Missing | Distinct namespace inodes/topology, root-record binding, rollback, reconciliation, and zero leaked links/TAPs. |
| nftables-only engine | 🟡 Partial | Four installed-candidate fixed-table cases passed in the bridge-capable named runner, covering modeled inet/bridge rules, nested-VM rule lifecycle, repeat sync, and NAT/conntrack readback. Full failure/reconciliation and clean-host qualification remain open. |
| Reused-IP bridge neighbor invalidation | 🟡 Partial | ADR-0021's owner-bound exact-IP rtnetlink operation is composed before host-veth exposure. Focused Go tests, a disposable real-kernel namespace probe, and the installed-candidate A-to-B stale-IP reuse journey with a concurrent control VM passed. The journey proves fresh gateway reachability and exact cleanup, but guest boot ARP prevents independent attribution of the pre-exposure ordering; spoofing and prior-host-boot recovery remain unqualified. |
| Typed `traffic` policy | 🟡 Partial | Installed-candidate same-/cross-network source-network TCP cases passed default deny, exact port allow, adjacent-port deny, and revoke. The 2026-09-28 acknowledged same-network source-VM UDP journey also passed exact allow, sibling/adjacent-port/reverse denial and fresh-flow revocation. Spoofing, IP reuse, cross-network UDP and reboot recovery remain unqualified. |
| Typed VM-to-VM `exec` policy | 🟡 Partial | The installed candidate passed the full seven-case exec domain in `mvmctl-030-test`: same-network, cross-network and networkless directional grants and real effects. The later networkless lifecycle journey also passed retained grants across both endpoint restarts and destination VM reboot, with pidfd exit and new-task/boot evidence, stopped/revoked denial and checked cleanup. Peer-identity attacks, capacity/deadline/cancellation, audit redaction, host reboot and snapshot restore remain unqualified at L2. |
| Cgroup/process identity lifecycle | 🟡 Partial | Isolated credentials and protected policy-v2 inspection are qualified locally. The three-case measured-pressure module passed twice: both swap variants held at least 80% usable guest-RAM occupancy for at least 15 seconds with fresh high events, unchanged task/boot identity and responsive exec. Historical guest-agent EOF/original incident, broader exact-identity cleanup and host-reboot binding remain unresolved; the bounded hold is not an ancestor/global-OOM guarantee. |
| Crash reconciliation and repeated zero-leak runs | 🔴 Missing | Every applicable domain twice with aggregated teardown and absence checks after failure and reboot. |

Specific legacy conflicts that must be rewritten before release:

- `network/test_policies.py` now covers same-network and cross-network source-network TCP grants; its former
  dual-backend and same-network rejection assumptions were removed. Additional typed policy variants remain open.
- `full_journeys/test_full_journeys.py` now proves same-network default denial, an exact source-VM TCP
  grant, echoed delivery and revocation. Its complete fifteen-case domain passed twice; additional policy
  variants and reboot/fault recovery remain separate gates.
- `exec/test_exec.py` covers host-to-VM execution through the exact installed CLI and a networkless fixture.
  `exec/test_vm_to_vm_exec.py` covers the positive and directional-denial VM-to-VM paths; adversarial authority,
  cancellation, audit, and reboot/restore behavior remain separate release gates.
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
| Directional VM-to-VM exec grant | 🟡 Partial | `exec/test_vm_to_vm_exec.py` | `test_directional_vm_to_vm_exec_policy` | Installed-candidate same-network, cross-network, and networkless journeys passed exact non-root grant, real destination effect, default/reverse/revoked denials, and checked VM/network/policy teardown. Authority-abuse, cancellation, and reboot/restore cases remain open. |

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
| `volume rm --force` | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle` | L2 |
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
| Volume resize with running VM | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeRunningVMDependency` | L2 — refusal while running; stopped-but-attached volume grows without requiring detach. |
| Volume hotplug (attach to running) | 🟡 Partial | `volume/test_volume_hotplug.py` | `TestVolumeHotplug` | L3 — SSH check /dev/vdb appears. Requires firecracker_116 marker (excluded from default run) |
| Volume hotunplug (detach from running) | 🟡 Partial | `volume/test_volume_hotplug.py` | `TestVolumeHotplug` | L3 — SSH check /dev/vdb disappears. Requires firecracker_116 marker (excluded from default run) |
| Volume attach/detach lifecycle (stop→detach→reattach→start) | ✅ Deep | `volume/test_volume.py` | `TestVolumeAttachDetach` | L3: full lifecycle including attach-to-stopped-then-start (Bug #7), detach-reattach-verify |
| Volume attach/detach nonexistent | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeNegativeFailure` | L2: attach/detach nonexistent volume to running VM, verify clear error, no state corruption |
| Volume cold-attach (stopped VM) | ✅ Deep | `volume/test_volume_hotplug.py` | `TestVolumeHotplugVersionGate` | L3: cold-attach/detach to stopped VM works regardless of Firecracker version |
| Volume hotplug destructive (force-remove, double-attach) | ✅ Deep | `volume/test_volume_hotplug.py` | `TestVolumeHotplugDestructive` | L3: force-remove attached volume, double-attach rejected, guest sees device|

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
| `image pull <type>:<version>` | ⏭️ Skip | `images/test_images.py` | `TestImagePull` | L1 — often skips on network |
| `image pull --force` | ⏭️ Skip | `images/test_images.py` | `TestImagePullAdvanced` | L1 — often skips |
| `image pull --default` | ⏭️ Skip | `images/test_images.py` | `TestImagePullAdvanced` | L2 — often skips |
| `image pull --skip-optimization` | ⏭️ Skip | `images/test_images.py` | `TestImagePullSkipOptimization` | L1 — often skips |
| `image pull --type override` | ⏭️ Skip | `images/test_images.py` | `TestImagePullAdvanced` | L1 — often skips |
| `image pull --version` | ⏭️ Skip | `images/test_images.py` | `TestImagePullAdvanced` | L1 — often skips |
| `image pull nonexistent` | ⚡ Shallow | `images/test_images.py` | `TestImagePullAdvanced` | L1 — no skip |
| `image pull --disable-detector` | ⏭️ Skip | `images/test_images.py` | `TestImageAdvancedFlags` | L1 — often skips |
| `image pull` arch auto-detection | ⏭️ Skip | `images/test_images.py` | `TestImagePullArchFlag` | Network-dependent. L1: success message. No `--arch` flag — arch is auto-detected by Go CLI. |
| `image pull --no-cache` | ⏭️ Skip | `images/test_images.py` | `TestImagePullNoCache` | Network-dependent. L1: success message |
| `image pull --type <type>` (explicit) | ⚡ Shallow | `images/test_images.py` | `TestImagePullAdvancedFlags` | Explicit type flag |
| `image ls` | ⚡ Shallow | `images/test_images.py` | `TestImageList` | Installed rc.7 human table: headers plus the seeded Alpine ID and name. |
| `image ls --json` | ⚡ Shallow | `images/test_images.py` | `TestImageList` | Installed rc.7 schema-1 array and seeded Alpine identity/presence. |
| `image ls --remote --no-cache` | 🔴 Missing | — | — | The former local `--no-cache` test asserted only exit 0; this flag affects remote resolution only. |
| `image ls --remote --type` | 🔴 Missing | — | — | The former local filter test swallowed parse failures; type filtering applies only to remote resolution. |
| `image ls --remote` | ⏭️ Skip | `images/test_images.py` | `TestImageList` | L1 — skips on network |
| `image inspect` | ⚡ Shallow | `images/test_images.py` | `TestImageList` | Installed rc.7 human renderer shows exact seeded Alpine ID, name, and path. |
| `image inspect --json` | ⚡ Shallow | `images/test_images.py` | `TestImageList` | Installed rc.7 grouped image/storage data matches the list record. |
| `image inspect --tree` | 🔴 Missing | — | — | Tree-view inspect subcommand does not exist in CLI. Not a valid test target. |
| `image default` | ⚡ Shallow | `images/test_images.py` | `TestImageDefaults` | Installed rc.7: selected Alpine ID is the sole default in schema-1 listing. |
| `image default` nonexistent | ⚡ Shallow | `images/test_images.py` | `TestImageDefaults` | Installed rc.7: `image.not_found` structured error. |
| `image rm` | ⏭️ Skip | `images/test_images.py` | `TestImageRemove` | L2 — uses imported_prefix cleanup |
| `image rm --force` | ⏭️ Skip | `images/test_images.py` | `TestImageRemoveForce` | L2 — force remove, verify via ls --json |
| `image rm` blocked by VM dependency | 🟡 Partial | `images/test_images.py` | `TestImageDependencyDeletion` | L3: rm rejected when image used by stopped/running VM; formerly-default promotes another |
| `image warm` | ⚡ Shallow | `images/test_images.py` | `TestImageWarm` | Installed rc.7: schema-1 path is a regular ready-pool file of the image's uncompressed size. |
| `image warm --all` | ⚡ Shallow | `images/test_images.py` | `TestImageWarm` | Installed rc.7: warmed/ready message for cached image. |
| `image warm` by ID prefix | ⚡ Shallow | `images/test_images.py` | `TestImageWarm` | Installed rc.7: prefix resolves and reports warmed/ready. |
| `image warm` nonexistent | ⚡ Shallow | `images/test_images.py` | `TestImageWarm` | Installed rc.7: command rejects missing image. |
| `image import` | ⚡ Shallow | `images/test_images.py` | `TestImageImport` | Installed rc.7: cached Alpine raw import, exact ext4/Alpine record, checked removal; VM boot remains unproved. |
| `image import --format qcow2` | 🟡 Partial | `images/test_images.py` | `TestImageImportAdvanced::test_image_import_with_format_qcow2` | Installed current-candidate L2: cached Alpine raw→QCOW2→ext4 import booted as a networkless VM; guest OS identity read through vsock and checked VM/image/temp-file removal. Other formats and failure paths remain unqualified; QCOW2 data volumes are unsupported. |
| `image import --root-partition` | ⏭️ Skip | `images/test_images.py` | `TestImageImportAdvanced` | L2 — skips on qemu-img |
| `image import --force` | ⏭️ Skip | `images/test_images.py` | `TestImageImportAdvanced` | L2 — may skip |
| `image import --default` | ⏭️ Skip | `images/test_images.py` | `TestImageImportSetDefault` | L2 — may skip |
| `image import` arch auto-detection | ⏭️ Skip | `images/test_images.py` | `TestImageImportArch` | L2 — skips on qemu-img. No `--arch` flag — arch is auto-detected by Go CLI. |
| `image import` auto-detect | ⏭️ Skip | `images/test_images.py` | `TestImageImportAdvanced` | L2 — may skip |
| `image import --skip-optimization` | 🟡 Partial | `images/test_images.py` | `TestImageImportAdvanced` | L2 — imported with skip-optimization flag, may skip |
| `image import --disable-detector` | 🟡 Partial | `images/test_images.py` | `TestImageImportAdvanced` | L2 — imported with --disable-detector arch, may skip on missing cached image |
| `image import --version` | ⚡ Shallow | `images/test_images.py` | `TestImageImportAdvanced::test_image_import_with_version` | L2 — version field matches in ls --json |
| `image import` nonexistent path | ⚡ Shallow | `images/test_images.py` | `TestImageImport` | Installed rc.7: structured `image.import.failed`, no image record. |
| Full import→VM-create end-to-end | ⏭️ Skip | `images/test_images.py` | `TestImageImportCreateVM` | L2 — many skip points |
| Default migrates on force re-pull | ⏭️ Skip | `images/test_images.py` | `TestImageDefaultMigration` | L2 — many skip points |

---

## `mvm kernel`

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `kernel ls --json` | ⚡ Shallow | `kernel/test_kernel.py` | `TestKernelLifecycle` | Installed rc.7: strict schema-1 list with a present registered kernel. |
| `kernel ls --json` empty | ⚡ Shallow | `kernel/test_kernel.py` | `TestKernelLifecycle` | Installed rc.7: an isolated initialized cache returns schema-1 success with `data: []`. |
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
| `console <vm> --state` | ⚡ Shallow | `console/test_console.py` | `TestConsoleState` | L2 owner for successful state observation; the listing group covers only missing-VM rejection. |
| `console <vm> --kill` | ⚡ Shallow | `console/test_console.py` | `TestConsoleKill` | L1 — state check before/after kill |
| `console <nonexistent> --state` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMListInspect::test_console_state_nonexistent_vm` | Installed-candidate L2 passed schema-1 `vm.not_found` rejection with no result data. |
| `console <ip> --state` | ⚡ Shallow | `console/test_console.py` | `TestConsoleState` | Resolves by IP |
| `console lifecycle: state → kill → state` | ⚡ Shallow | `console/test_console.py` | `TestConsoleKill` | Full lifecycle verification |
| `console on stopped VM` | ⚡ Shallow | `console/test_console.py` | `TestConsoleOnStoppedVM` | Non-zero exit |

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
| `host reset --force` success | 🔴 Gap | — | — | Administrator reset requires separate clean-host qualification; no shared-runner success is claimed. |
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
