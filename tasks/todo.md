# Safe Jailer Remediation Tasks

This checklist implements `tasks/plan.md`. No production task begins until Task 1 is approved. Go production/L0/L1 work
belongs to the `engineer`; L2 and release verification belong to the `qa-engineer`; ADRs and documentation belong to the
`architect`.

## Phase 0: Preserve and specify

## Task 0: Preserve the prototype range and unrelated work

**Owner:** architect
**Description:** Record the reviewed base/head and preserve the dirty worktree before any history curation.

**Acceptance criteria:**
- [x] Base `f8ffa28b9d74cac2c9e15fa07a18389d51a3263e` and head `ee733232a2dc23c1edb372022a2add71af36f44c` remain reachable.
- [x] Existing modified/untracked user files are inventoried and unchanged.
- [x] No reset, rebase, squash, or force push occurs before the release gate and explicit approval.

**Verification:**
- [x] `git status --short` matches the preserved inventory plus intentional remediation files.
- [x] `git log --oneline --decorate -10` shows the prototype commits and preservation reference.

**Dependencies:** None
**Files likely touched:** None; Git references only after approval
**Estimated scope:** XS

## Task 1: Approve ADR-0016 and the privileged operation catalog

**Owner:** architect
**Description:** Record the threat model, root-owned system binary, early privileged dispatcher, state layout, release
trust, ownership rules, and complete
typed operation catalog. Supersede the conflicting portions of ADR-0002, ADR-0005, and ADR-0015.

**Acceptance criteria:**
- [x] ADR names every privileged action and rejects generic argv/command/path/cgroup extension points.
- [x] ADR defines bootstrap, upgrade, version negotiation, recovery, and rollback.
- [x] ADR maps every new interface/file to the existing CLI/API/core/service/lib layers and preserves dependency rules.
- [x] Human explicitly approves the four open decisions in `tasks/plan.md`.

**Verification:**
- [x] Cross-check every proposed action against two analogous typed interfaces in the repository.
- [x] Confirm `cmd/mvm` only routes, `pkg/api` remains the sole cross-core orchestrator, and no core domain imports a
  sibling core domain.
- [x] ADR-0005 explicitly marks its user-owned wildcard safety claim as pending replacement by ADR-0016.

**Dependencies:** Task 0
**Files likely touched:** `docs/adr/0016-*.md`, `docs/adr/0002-*.md`, `docs/adr/0005-*.md`, `docs/adr/0015-*.md`
**Estimated scope:** M

### Checkpoint A: Architecture

- [x] Task 0 and Task 1 complete.
- [x] Human approves ADR-0016 before Go implementation.
- [x] Abuse cases are listed as test requirements before production implementation.

## Phase 1: Replace the privilege boundary while preserving one binary

## Task 2: Atomically install the single root-owned system binary

**Owner:** engineer
**Description:** Add an administrator-only install/upgrade flow for the existing release artifact at
`/usr/local/bin/mvm`. Privileged dispatch validates executable ownership/mode and protocol version before any normal app
initialization.

**Acceptance criteria:**
- [ ] `/usr/local/bin/mvm` is installed as `root:root`, mode `0755`, under a root-owned non-writable directory chain.
- [ ] Installation is temporary-file + fsync + rename and refuses symlinked targets.
- [ ] A normal user update cannot replace the system binary; unsupported privileged protocol fails closed with an
  administrator upgrade instruction.

**Verification:**
- [ ] L1 install tests cover ownership, modes, symlink target, partial write, and version mismatch.
- [ ] Focused Go tests for system installation, early dispatch, and host initialization pass.

**Dependencies:** Task 1
**Files likely touched:** `cmd/mvm/main.go`, `internal/core/host/utils.go`, `internal/core/host/utils_test.go`,
`internal/infra/constants.go`, `scripts/build.sh`
**Estimated scope:** M

## Task 3: Define strict early privileged request parsing and caller identity

**Owner:** engineer
**Description:** Before `app.Initialize()` and Cobra, detect only the reserved privileged entry point, then implement
protocol-versioned, bounded, typed request decoding; sanitize environment; derive the caller from `SUDO_UID`/`SUDO_GID`;
reject rootless/direct misuse and all unknown or extra input.

**Acceptance criteria:**
- [ ] Each action has a distinct request type and handler; no `[]string` command or generic operation payload exists.
- [ ] Request body, string, list, and numeric limits are enforced before side effects.
- [ ] Caller UID/GID and system-binary ownership checks precede action dispatch.
- [ ] Ordinary CLI initialization and commands are unreachable after privileged mode is selected.

**Verification:**
- [ ] Table tests reject unknown actions/fields, duplicate JSON fields, oversized input, invalid IDs, missing sudo identity,
  and unsupported protocol versions.
- [ ] `go test` for privileged dispatch passes with `FakeRunner` and temporary roots.

**Dependencies:** Task 2
**Files likely touched:** `cmd/mvm/main.go`, `internal/service/privileged/protocol.go`, `entry.go`, `identity.go`, tests
**Estimated scope:** M

## Task 4: Migrate Jailer service calls to typed privileged actions

**Owner:** engineer
**Description:** Route install, launch, cleanup, snapshot exposure, volume exposure/removal, and release removal through
the privileged protocol. Remove the public `mvm run jailer` root entry point.

**Acceptance criteria:**
- [ ] Jailer caller methods retain typed names matching existing service conventions.
- [ ] The general CLI cannot directly dispatch privileged Jailer handlers.
- [ ] Unknown/unauthorized VM and release operations fail before filesystem/process changes.

**Verification:**
- [ ] L1 tests assert exact privileged requests for every Jailer method.
- [ ] `rg -n "run jailer|newJailerServiceCmd"` finds only migration/docs references.

**Dependencies:** Task 3
**Files likely touched:** `internal/service/jailer/spawn.go`, `internal/service/jailer/entry.go`, their tests,
`internal/cli/service.go`
**Estimated scope:** M

## Task 5: Constrain loopmount behind privileged dispatch

**Owner:** engineer
**Description:** Keep the typed provisioning wire operations but validate all roots and chroot targets through pinned
descriptors. Remove direct privileged `mvm run provision` and the arbitrary `--umount` shortcut.

**Acceptance criteria:**
- [ ] Provision operations are limited to the invoking user's pinned VM/image/volume roots.
- [ ] Unmount/process handling cannot target unrelated host mounts or PIDs.
- [ ] No caller-provided host command is executed outside the intended mounted guest root.

**Verification:**
- [ ] L1 abuse tests cover host path, symlink, mount swap, unrelated PID, and arbitrary unmount attempts.
- [ ] Existing loopmount L1 behavior remains green.

**Dependencies:** Task 3
**Files likely touched:** `internal/service/loopmount/entry.go`, `provisioner.go`, `spawn.go`, `wire.go`, tests
**Estimated scope:** M

## Task 6: Replace raw network-link sudo calls with typed privileged methods

**Owner:** engineer
**Description:** Add named bridge, TAP, address, route, and link lifecycle operations. Callers pass validated model data,
not raw `ip` argv.

**Acceptance criteria:**
- [ ] No unprivileged code can ask root-side code to run arbitrary `ip` arguments or `netns exec`.
- [ ] Operations validate interface names, addresses, owning network/VM IDs, and idempotent expected state.
- [ ] Existing network Service remains the intra-domain owner; API boundaries do not move.

**Verification:**
- [ ] `FakeRunner` tests assert exact root-side commands for each typed method and reject shell/argv injection.
- [ ] Network L1 lifecycle tests pass.

**Dependencies:** Task 3
**Files likely touched:** privileged network handlers, `internal/core/network/service.go`, typed model/request tests
**Estimated scope:** M

## Task 7: Replace raw firewall sudo calls with atomic typed batches

**Owner:** engineer
**Description:** Move iptables/nftables application into privileged actions that accept validated firewall models and render
complete atomic backend batches root-side.

**Acceptance criteria:**
- [ ] Caller cannot pass raw iptables/nft syntax, filenames, includes, or commands.
- [ ] Chain creation, parent jumps, ordering, and managed rules apply as one reported operation.
- [ ] Every runner/backend error propagates; initialization can no longer silently succeed.

**Verification:**
- [ ] Backend tests inject failure for every chain/jump/batch stage.
- [ ] Golden tests cover exact iptables-restore and nft batch output from typed models.

**Dependencies:** Task 3
**Files likely touched:** privileged firewall handlers, `internal/lib/firewall/tracker.go`, `iptables.go`, `nftables.go`, tests
**Estimated scope:** M

## Task 8: Replace raw host module/sysctl sudo calls

**Owner:** engineer
**Description:** Expose only the exact host initialization actions mvmctl needs. Module names, sysctl keys, and values
are compiled/root-policy allowlists, not caller-selected commands.

**Acceptance criteria:**
- [ ] Privileged dispatch supports only named host readiness/remediation operations from ADR-0016.
- [ ] Arbitrary modprobe config/path and arbitrary sysctl keys are impossible.
- [ ] Detection stays unprivileged; only mutation crosses the privileged boundary.

**Verification:**
- [ ] L1 tests reject all unlisted modules/keys/values and confirm supported host-init actions.
- [ ] Host probe tests remain green.

**Dependencies:** Task 3
**Files likely touched:** privileged host handlers, `internal/core/host/service.go`, `utils.go`, `probe.go`, tests
**Estimated scope:** M

## Task 9: Replace sudoers with the single-binary privileged policy

**Owner:** engineer
**Description:** After Tasks 4-8 migrate every callsite, atomically replace the wildcard CLI and raw system-binary
entries with the internal privileged entry point of root-owned `/usr/local/bin/mvm`.

**Acceptance criteria:**
- [ ] Generated sudoers contains no user-owned `mvm`, `ip`, `iptables`, `nft`, `modprobe`, or `sysctl` command.
- [ ] `visudo -c` validation occurs before activation and legacy insecure entries are detected.
- [ ] Failure preserves the prior file and reports that the host remains unsafe/uninitialized.

**Verification:**
- [ ] Unit golden test covers the exact sudoers content.
- [ ] L2 confirms every removed command fails under `sudo -n` while supported CLI flows still work.

**Dependencies:** Tasks 4, 5, 6, 7, 8
**Files likely touched:** `internal/core/host/utils.go`, `utils_test.go`, `internal/infra/constants.go`, host-init tests
**Estimated scope:** M

### Checkpoint B: Privilege boundary

- [ ] Tasks 2-9 complete.
- [ ] Passwordless sudo reaches only the immutable binary's early privileged entry point.
- [ ] User-owned binary replacement, raw system commands, and generic privileged requests are rejected in L1/L2.

## Phase 2: Trusted release and safe filesystem operations

## Task 10: Implement independently verified atomic release installation

**Owner:** engineer
**Description:** Move release authority inside privileged dispatch, harden archive extraction, and create an atomic root-owned
release manifest for the exact pair.

**Acceptance criteria:**
- [ ] Privileged code constructs the allowed version/architecture URL and independently obtains expected integrity metadata.
- [ ] Archive parser rejects traversal, links, devices, duplicates, unexpected members, and size/count overflow.
- [ ] Pair installation is atomic, fsynced, content-addressed, and cannot replace a referenced release.

**Verification:**
- [ ] Negative tests include a caller-authored archive plus matching caller hash and all archive abuse cases.
- [ ] Interrupted install leaves either the old complete pair or no pair, never a partial trusted directory.

**Dependencies:** Tasks 4 and 9
**Files likely touched:** `internal/service/jailer/entry.go`, privileged release handler/manifest, archive tests
**Estimated scope:** M

## Task 11: Introduce descriptor-relative managed-resource access

**Owner:** engineer
**Description:** Replace canonicalize/validate/reopen logic with pinned directory/file descriptors and fixed basenames.

**Acceptance criteria:**
- [ ] Managed roots use `openat2`/`openat` no-symlink/beneath resolution and `fstat` ownership/type checks.
- [ ] Privileged PID/status/manifest writes use root-owned fixed paths, not user-selected basenames.
- [ ] No privileged operation reopens a user-controlled absolute path after validation.

**Verification:**
- [ ] Race tests continuously rename/replace every user-owned parent with symlinks during privileged operations.
- [ ] Static review finds no `EvalSymlinks`-then-open security boundary in privileged/Jailer code.

**Dependencies:** Tasks 3 and 4
**Files likely touched:** privileged path module/tests, `internal/service/jailer/entry.go`, `manifest.go`
**Estimated scope:** M

## Task 12: Make launch mounts private and live mounts namespace-aware

**Owner:** engineer
**Description:** Unshare a private mount namespace before pre-launch mounts. For live mounts, verify the registered
process, pin source/target descriptors, enter its mount namespace, and apply restrictive mount flags.

**Acceptance criteria:**
- [ ] Launch mounts never appear in the original host namespace.
- [ ] Live volume/snapshot mounts are visible to jailed Firecracker and cannot target host paths through symlinks.
- [ ] VM exit destroys namespace mounts; cleanup never recursively traverses a mounted user directory.

**Verification:**
- [ ] L1 namespace-operation tests cover PID mismatch and target/source replacement.
- [ ] L2 asserts visibility inside the VM namespace and absence from host mountinfo before/after failure.

**Dependencies:** Task 11
**Files likely touched:** privileged namespace/mount modules and tests, `internal/service/jailer/entry.go`
**Estimated scope:** M

## Phase 3: Lifecycle, cgroups, and exact identity

## Task 13: Add root-owned instance records and per-VM locking

**Owner:** engineer
**Description:** Persist minimal UID/release/process/cleanup metadata under `/var/lib/mvmctl/instances` and serialize all
privileged VM actions with `/run/mvmctl` locks.

**Acceptance criteria:**
- [ ] Record creation/update is atomic, root-owned, and bound to `SUDO_UID`.
- [ ] Every launch, signal, setns, mount, cleanup, and release-reference check holds the VM lock.
- [ ] Other UIDs and PID/start-time/cgroup mismatches fail closed.

**Verification:**
- [ ] Tests cover lock contention, stale lock, cross-UID request, PID reuse, corrupt/truncated record, and atomic recovery.
- [ ] Root record contains no full user VM config or secrets.

**Dependencies:** Tasks 10, 11
**Files likely touched:** privileged instance record/lock/process modules and tests
**Estimated scope:** M

## Task 14: Make VM spawn and abort crash-consistent

**Owner:** engineer
**Description:** Persist a provisional VM before spawn, retain a lifecycle handle immediately after spawn, and route
every post-spawn failure through a typed verified abort.

**Acceptance criteria:**
- [ ] No error path after spawn can return without terminating or explicitly retaining a manageable registered VM.
- [ ] Abort verifies process identity, performs TERM/wait/KILL, then cgroup/chroot/record cleanup in order.
- [ ] Snapshot-restore batch rollback stops prior restored VMs before deleting DB/VM directories.

**Verification:**
- [ ] Failure injection covers relay start, API socket, DB writes, verification, snapshot load, and batch rollback.
- [ ] Each case asserts no live PID, cgroup, mount, jail, or orphaned root record.

**Dependencies:** Tasks 12 and 13
**Files likely touched:** `internal/core/vm/firecracker.go`, `pkg/api/vm.go`, `pkg/api/snapshot.go`, focused tests
**Estimated scope:** M

## Task 15: Add startup and pre-launch privileged reconciliation

**Owner:** engineer
**Description:** Reconcile instance records, verified processes, cgroups, and chroot directories after CLI interruption or
host reboot without trusting the user DB as the root authorization source.

**Acceptance criteria:**
- [ ] Reconcile distinguishes running, stopped-clean, stale, corrupt, and foreign-owner records.
- [ ] It never signals an unverified PID or recursively deletes a path that may contain a mount.
- [ ] Launch runs per-UID/per-VM reconciliation before creating new privileged state.

**Verification:**
- [ ] L1 state-machine tests cover every classification and idempotent retry.
- [ ] L2 kill/reboot scenarios converge to zero stale resources or a precise actionable error.

**Dependencies:** Tasks 13 and 14
**Files likely touched:** privileged reconciliation module/tests, Jailer service caller, host readiness integration
**Estimated scope:** M

## Task 16: Persist exact Firecracker/Jailer identity transactionally

**Owner:** engineer
**Description:** Add snapshot Jailer identity, uniqueness, transactional pair repository operations, and deterministic
legacy backfill. Compare user DB pair hashes with the root release manifest before launch.

**Acceptance criteria:**
- [ ] VM and snapshot always persist both exact IDs/hashes; restored/migrated VMs cannot retain an empty Jailer ID.
- [ ] Pair install/default/remove DB updates are transactional and return all errors.
- [ ] Ambiguous or unmatched legacy data fails closed instead of selecting an arbitrary same-version row.

**Verification:**
- [ ] Migration tests cover unique, duplicate, ambiguous, missing, referenced, and rollback cases.
- [ ] Restore-then-hotplug test sees the persisted Jailer identity.

**Dependencies:** Tasks 10 and 13
**Files likely touched:** new DB migration, binary/snapshot model, binary repository/service, migration tests
**Estimated scope:** M

## Task 17: Centralize jail-visible path translation and restore parity

**Owner:** engineer
**Description:** Put one typed translator in the VM spawner/controller seam and use it for config, rootfs, sockets,
vsock, snapshot create/restore, and live volume hotplug.

**Acceptance criteria:**
- [ ] Snapshot create passes/exposes its directory and uses `/snapshot/...`; restore uses the same mapping.
- [ ] Rootfs, API/vsock sockets, config, and volume paths have explicit host and jail representations.
- [ ] Restored jailed VMs use namespace-aware volume attach/detach.

**Verification:**
- [ ] L1 tests assert every exact Firecracker API path for create, load, and hotplug.
- [ ] Prepared L2 Jailer snapshot/volume tests pass unchanged except for fixture correctness.

**Dependencies:** Tasks 12, 14, 16
**Files likely touched:** `internal/core/vm/jailer.go`, `controller.go`, `pkg/api/snapshot.go`, tests
**Estimated scope:** M

## Task 18: Harden cgroup policy, readiness, and cleanup

**Owner:** engineer
**Description:** Keep the corrected leaf path, derive all values from typed VM/host policy, validate controller
enablement, and ensure process-aware removal. Add I/O limits if retained by the approved M2 specification.

**Acceptance criteria:**
- [ ] Privileged dispatch accepts VM resource intent, never raw cgroup key/value pairs, and applies host-policy bounds.
- [ ] Host readiness always reports missing cgroup v2/version/controllers/swap support as critical.
- [ ] Cleanup cannot remove or kill a cgroup belonging to an unverified instance.

**Verification:**
- [ ] L1 covers version zero, unavailable/unenableable controllers, mismatched values, foreign membership, and cleanup.
- [ ] L2 measures supported Firecracker versions before the memory-headroom default is approved.

**Dependencies:** Tasks 13 and 14
**Files likely touched:** privileged cgroup module, `internal/core/host/detector.go`, `probe.go`, model policy, tests
**Estimated scope:** M

### Checkpoint C: Jailer lifecycle parity

- [ ] Tasks 10-18 complete.
- [ ] All normal lifecycle and snapshot/hotplug L1 tests pass.
- [ ] Fault injection proves cleanup and reconciliation are idempotent.
- [ ] Exact release and owner identity are present on every VM/snapshot path.

## Phase 4: Fail-closed routed policy

## Task 19: Make firewall initialization observable and atomic

**Owner:** engineer
**Description:** Return and propagate errors for chain/jump setup and apply each complete managed generation through an
atomic backend batch.

**Acceptance criteria:**
- [ ] `Initialize` and jump ordering return typed failure; no runner result is silently discarded.
- [ ] Reconciliation verifies required parent jumps and default-deny placement before success.
- [ ] Partial backend application cannot be reported as successful.

**Verification:**
- [ ] Failure injection for every parent jump/reorder/batch command returns `CodePolicySyncFailed`.
- [ ] Both firewall backend test suites pass.

**Dependencies:** Task 7
**Files likely touched:** `internal/lib/firewall/tracker.go`, `iptables.go`, `nftables.go`, network policy tests
**Estimated scope:** M

## Task 20: Reorder policy/resource deletion around kernel reconciliation

**Owner:** engineer
**Description:** Apply the desired kernel generation before deleting policy/VM/network identity or releasing an IP;
restore the prior generation if the DB commit fails.

**Acceptance criteria:**
- [ ] Failed `policy rm` retains authoritative intent and retryability.
- [ ] Failed VM/network removal cannot release an address while stale allow rules remain.
- [ ] Derived firewall rows and intent transition transactionally after kernel success.

**Verification:**
- [ ] Unit tests inject kernel and DB failures at every boundary and assert rollback/recovery details.
- [ ] IP-reuse integration test proves no replacement VM inherits a stale allow.

**Dependencies:** Task 19
**Files likely touched:** `pkg/api/policy.go`, `pkg/api/vm.go`, network removal API, firewall repository, tests
**Estimated scope:** M

## Phase 5: System verification and documentation

## Task 21: Repair L2 fixture isolation and add privilege/fault coverage

**Owner:** qa-engineer
**Description:** Fix structured cgroup-setting restoration, then add real-host tests for immutable sudo, release trust,
multi-user isolation, path races, PID reuse, kill-at-stage recovery, namespace mounts, and IP reuse.

**Acceptance criteria:**
- [ ] Every modified setting is restored exactly or reset, and cleanup failures fail the test.
- [ ] New tests use Tier-2 fixtures and known-limitation conventions from the system-test architecture.
- [ ] Repeated suite execution leaves the runner with no leaked VM process, mount, cgroup, network, or config override.

**Verification:**
- [ ] Targeted L2 domains pass twice consecutively on the prepared runner VM.
- [ ] Post-suite leak audit is empty.

**Dependencies:** Tasks 9, 10-20
**Files likely touched:** `tests/system/vm/test_cgroup.py`, `test_jailer.py`, policy tests, shared fixtures, coverage matrix
**Estimated scope:** M

## Task 22: Run full CI, L2 release qualification, and resource measurements

**Owner:** qa-engineer
**Description:** Execute repository CI in its exact order, the full relevant L2 matrix, upgrade/rollback tests, and
versioned Firecracker overhead measurements.

**Acceptance criteria:**
- [ ] Tidy, format, golines, generate, vet, and full tests pass.
- [ ] L2 privilege/Jailer/cgroup/snapshot/volume/policy/upgrade suites pass on supported architectures.
- [ ] Memory headroom and I/O-limit decisions are backed by recorded measurements, not assumed constants.

**Verification:**
- [ ] Attach command output and runner environment to the release report.
- [ ] No critical/high review finding remains open or deferred without explicit release rejection.

**Dependencies:** Task 21
**Files likely touched:** Test reports/fixtures only
**Estimated scope:** S

## Task 23: Reconcile all documentation with verified behavior

**Owner:** architect
**Description:** Apply the documentation matrix from `tasks/plan.md`, using the final implementation and test evidence
as the source of truth.

**Acceptance criteria:**
- [ ] ADR statuses, state paths, system-install/privileged-mode setup, service list, cgroup paths, lifecycle, and
  limitations are accurate.
- [ ] Roadmap and changelog do not claim completion beyond executed evidence.
- [ ] Repository-wide searches find no stale wildcard-sudo, `/var/run` full-state, old cgroup path, or service-count claim.
- [ ] Documentation lands in reviewable slices: architecture/rules, user/operator guides, then testing/release records.

**Verification:**
- [ ] Architect reads the final implementation diff for interfaces, structs, imports, and state locations.
- [ ] Documentation link/path/command checks pass and QA verifies operator procedures on the runner.

**Dependencies:** Task 22
**Files likely touched:** Files in the documentation matrix; split into reviewable documentation-only commits
**Estimated scope:** M per documentation slice

## Task 24: Independently verify documentation accuracy

**Owner:** architect with qa-engineer verification
**Description:** Treat documentation as a release artifact. Trace each security- and operator-relevant claim to final
code, then execute every installation, upgrade, recovery, and troubleshooting procedure on the release candidate.

**Acceptance criteria:**
- [ ] Every changed behavior is described consistently across ADRs, `CONTEXT.md`, standards, runtime, operator docs,
  roadmap, test architecture, coverage matrix, and changelog.
- [ ] Current behavior, migration behavior, known limitations, and future work are clearly distinguished.
- [ ] No stale command/path/ownership/service-count/cgroup/snapshot/sudo claim remains anywhere in tracked Markdown.

**Verification:**
- [ ] Architect signs off after reading the final implementation diff rather than relying on implementation summaries.
- [ ] QA executes documented install, helper-mode, upgrade, rollback, reconcile, and troubleshooting commands on L2.
- [ ] A repository-wide terminology/path audit and Markdown link check pass.

**Dependencies:** Task 23
**Files likely touched:** Documentation corrections found by the audit; release verification record
**Estimated scope:** M

### Checkpoint D: Release

- [ ] Tasks 21-24 complete.
- [ ] Full CI and L2 release report pass.
- [ ] No leaked runtime resources after the fault suite.
- [ ] Architect and QA independently approve documentation accuracy.
- [ ] Human approves the final code, docs, migration, and release decision.

## Follow-up project: XDG state separation

Do not include this in the Jailer remediation. After release, write a separate ADR and migration plan to split:

- durable user metadata into `$XDG_STATE_HOME/mvmctl`;
- cacheable downloads into `$XDG_CACHE_HOME/mvmctl`;
- user sockets/PIDs into `$XDG_RUNTIME_DIR/mvmctl`;
- user configuration into `$XDG_CONFIG_HOME/mvmctl`.

The follow-up must preserve `MVM_CACHE_DIR` compatibility, migrate atomically, detect partial moves, and support rollback.
