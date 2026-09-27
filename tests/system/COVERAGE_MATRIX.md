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

The command tables below describe tests for the currently implemented CLI, including transitional behavior that v0.3.0
will remove. A `✅ Deep` row proves only the behavior named in that row; it is not evidence that the clean-break security
architecture is release-qualified. The authoritative release gate is `tasks/todo.md` Task 17 plus `docs/RC_QA.md`.

**Runner evidence (2026-09-27):** In the named `mvmctl-030-test` runner, pinned Go 1.26.3 passed the complete
unprivileged CI selection with both coverage profiles and the separate full root-effect Jailer package on clean
source commit `8e0be4fe`. The installed `mvm 0.3.0-rc.root-ready` candidate passed 58 CLI/config/read-only-host,
9 JSON/flag-invariant, 26 non-VM key, and 5 internal-service entry-point Python cases (98 total); the focused
networkless create/stop/start/reboot/vsock case, five focused raw-volume/removed-format cases, a networkless
raw-volume guest-attachment/restart case, an isolated non-root Firecracker UID check, a direct-API-denial with
authorized CLI stop check, and a `vm ps`/inspect stop-start projection case also passed. The
installed binary predates `8e0be4fe`, but the intervening source change under `internal/`, `pkg/`, and `cmd/` was
test-only.
These are scoped results, **not** a full Python matrix or release sign-off. The current runner kernel still rejects
bridge-family nftables tables, blocking network/policy qualification there. Volume/snapshot consistency, host-reboot,
and full Jailer system journeys also remained unqualified at that checkpoint; one guest-visible volume attach was
not snapshot-consistency qualification. The later focused checkpoint below addresses only the networkless
data-volume snapshot journey, not the broader release gaps.

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
1.16.0, so that supported-version decision and its correction remain a release blocker.

| v0.3.0 security capability | Status | Required release evidence |
|---|---|---|
| Installed system candidate and clean bootstrap | 🟡 Partial | Installed-candidate and happy-path init coverage exists; clean group/membership setup, root journal, rollback, fail-closed host mutation, and marker-only sudoers denial remain missing. |
| fd-0 privileged control transport | 🔴 Missing | Go L1 framing/EOF/peer/cancellation abuse coverage plus installed-CLI lifecycle integration. |
| Root VM authority and cross-UID denial | 🟡 Partial | A focused installed-candidate networkless case denies operator direct connection to the reported Firecracker API socket while `vm stop` succeeds; root-record/process/cache identity checks, constrained two-user denial, tombstone, and zero-leak assertions remain missing. |
| Independent trusted Firecracker/Jailer releases | 🔴 Missing | Root manifest/ELF/hash authority, atomic failure injection, and installed-CLI release lifecycle. |
| Descriptor-pinned resources and private mount namespace | 🔴 Missing | Type/owner/mode/link checks, replacement races, namespace mounts, cleanup, restart, and restore. |
| Mandatory per-VM network namespace | 🔴 Missing | Distinct namespace inodes/topology, root-record binding, rollback, reconciliation, and zero leaked links/TAPs. |
| nftables-only engine | 🔴 Missing | Atomic bridge-family generation and removal of iptables/backend-selection tests and product paths. |
| Reused-IP bridge neighbor invalidation | 🟡 Partial | ADR-0021's owner-bound exact-IP rtnetlink operation is composed before host-veth exposure. Focused Go tests and a disposable real-kernel namespace probe passed; a two-VM reused-IP installed-candidate journey on a bridge-capable runner remains missing. |
| Typed `traffic` policy | 🔴 Missing | Same/cross-network default deny and exact source/protocol/port allows plus spoof/reverse/IP-reuse denials. |
| Typed VM-to-VM `exec` policy | 🔴 Missing | Directional non-root authorization for same/cross/no-IP networks plus identity, bounds, and redaction denials. |
| Cgroup/process identity lifecycle | 🟡 Partial | Focused installed-candidate cases prove the Firecracker process UID differs from root and the operator, and that an unprivileged caller receives `enforced` policy-v2 limits and usage through authenticated root inspection while direct leaf read is denied. A bounded guest tmpfs load remained responsive and increased observed VM memory charge; root-only `memory.high` events were observed separately. Per-VM uniqueness, root-record, pidfd, broader mount/cgroup cleanup, sustained saturation survival, and host-reboot reconciliation binding remain unqualified. |
| Crash reconciliation and repeated zero-leak runs | 🔴 Missing | Every applicable domain twice with aggregated teardown and absence checks after failure and reboot. |

Specific legacy conflicts that must be rewritten before release:

- `network/test_policies.py` still exercises both firewall backends and rejects same-network policy creation.
- `full_journeys/test_full_journeys.py` still expects same-network VM traffic without an allow policy.
- `exec/test_exec.py` covers host-to-VM execution only; two cases still use a PATH-selected `mvm`.
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
| `config get <cat> <key>` (existing) | ⚡ Shallow | `config/test_config.py` | `TestConfigLifecycle` | L2 via regex parse of output — fragile. |
| `config get <cat>` (category only) | ⚡ Shallow | `config/test_config.py` | `TestConfigEdgeCases` | Checks multiple keys listed |
| `config get <cat> <nonexistent>` | ⚡ Shallow | `config/test_config.py` | `TestConfigEdgeCasesExtended` | Exit 0, guidance returned |
| `config set <cat> <key> <val>` then get | ⚡ Shallow | `config/test_config.py` | `TestConfigLifecycle` | L2 roundtrip |
| `config set` invalid category | ⚡ Shallow | `config/test_config.py` | `TestConfigEdgeCases` | Non-zero exit |
| `config set` invalid value type | ⚡ Shallow | `config/test_config.py` | `TestConfigEdgeCasesExtended` | Non-zero exit |
| `config reset <cat> <key>` | ⚡ Shallow | `config/test_config.py` | `TestConfigLifecycle` | L2 — value not in output after reset |
| `config reset <cat>` (category only) | ⚡ Shallow | `config/test_config.py` | `TestConfigEdgeCases` | L2 — value not in output |
| `config reset` no args | ⚡ Shallow | `config/test_config.py` | `TestConfigEdgeCases` | Shows guidance, exit 0 |
| `config reset --all` | ⚡ Shallow | `config/test_config.py` | `TestConfigLifecycle`, `TestConfigEdgeCasesResetAllAfterSet` | Multiple overrides then reset --all |
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
| `network create` duplicate name | ⚡ Shallow | `network/test_network.py` | `TestNetworkLifecycle` | L1 |
| `network create` duplicate subnet | ⚡ Shallow | `network/test_network.py` | `TestNetworkLifecycle` | L1 — two tests for this: test_overlapping_subnet_across_networks_rejected, test_overlapping_subnet_rejected |
| `network create --default` | ⚡ Shallow | `network/test_network.py` | `TestNetworkLifecycle` | L2 — is_default=true in ls JSON |
| `network create --no-nat` | ✅ Deep | `network/test_network.py` | `TestNetworkLifecycle` | L3: bridge exists, _assert_no_masquerade_rule confirms no MASQUERADE rule exists |
| `network create --ipv4-gateway` | ✅ Deep | `network/test_network.py` | `TestNetworkAdvancedCreate` | L2 — checks inspect JSON. Could be L3 (bridge IP). |
| `network create --nat-gateways` | ⚡ Shallow | `network/test_network.py` | `TestNetworkAdvancedCreate` | L2 — checks inspect JSON |
| `network create` invalid gateway | ⚡ Shallow | `network/test_network.py` | `TestNetworkAdvancedCreate` | L1 |
| `network create --non-interactive` | ⚡ Shallow | `network/test_network.py` | `TestNetworkEdgeCases` | L2: ls --json confirms network created |
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
| `network sync` idempotent | ✅ Deep | `network/test_network.py` | `TestNetworkSync` | L3: rule count unchanged |
| Sync after bridge deletion | ✅ Deep | `network/test_network.py` | `TestNetworkSyncAfterReboot` | L3: bridge recreated, IP reassigned, rules restored |
| `network sync` conntrack rule | ✅ Deep | `network/test_network.py` | `TestNetworkSync` | Conntrack established/related accept rule |
| `network sync` nonexistent | ⚡ Shallow | `network/test_network.py` | `TestNetworkSync` | Error handling for nonexistent network |
| `network rm` rejects with active VM | ✅ Deep | `network/test_network.py` | `TestNetworkVMDependency` | L3: rm fails without --force when VM uses network; inspect shows VM count |
| nftables firewall backend | ✅ Deep | `network/test_nftables.py` | `TestNFTablesFirewallBackend` | L3: set nftables backend, create VM, SSH, ping, verify nftables rules, cleanup, reset to iptables |
| nftables atomic rule sync | ✅ Deep | `network/test_nftables.py` | `TestAtomicRuleSync` | L3: batch_ensure_rules idempotent, conntrack rule present, rule count stable, MASQUERADE persists |
| Routed service-access policies | ✅ Deep | `network/test_policies.py` | `test_routed_service_access_policy_specification` | L2: nftables and iptables default deny, exact VM/port-range allow, NoCloud precedence, host deny, internet egress, sync recovery, and deletion invalidation |

---

## `mvm vm`

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `vm create` basic | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMListInspect` (via module_vm), `TestVMCreate` | L2 — status=running. Many shallow VM tests. |
| `vm create` with `--vcpu` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMConfigOptions` | L2 — vcpu_count in ls JSON |
| `vm create` with `--vcpu 0` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMConfigOptions` | L1 — non-zero exit |
| `vm create` with `--vcpu -1` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMConfigOptions` | L1 — non-zero exit |
| `vm create` with `--mem` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMConfigOptions` | L2 — mem_size_mib |
| `vm create` with `--mem 0` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMConfigOptions` | L1 |
| `vm create` with `--disk-size` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMConfigOptions` | L2 — disk_size_mib |
| `vm create` with `--disk-size 0` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMConfigOptions` | L1 |
| `vm create` with `--disk-size invalid` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMConfigOptions` | L1 |
| `vm create` with `--kernel` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMConfigOptions` | L2 — kernel_id starts with prefix |
| `vm create` with `--boot-args` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMConfigOptions` | L2 — boot_args in ls JSON |
| `vm create` with `--ip` (static) | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMNetworkIntegration` | L2 — ipv4 field in ls JSON |
| `vm create` with invalid IP | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMNetworkIntegration` | L1 |
| `vm create` with `--mac` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMNetworkIntegration` | L2 — mac field |
| `vm create` with named `--network` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMNetworkIntegration` | L2 — network_id matches |
| `vm create --no-network`, stop/start/reboot, and vsock exec | 🟡 Partial | `vm/test_jailer.py` | `TestNetworklessJailerLifecycle` | Focused installed-candidate L2 case passed twice for restart and once with reboot in the named runner: no network identity or guest `eth0`, `lo` present, exec after restart and reboot, checked VM removal. Kernel `dummy0` may exist; namespace isolation, VM-to-VM authorization, and full domain QA remain. |
| `vm create` default (no console relay) | ✅ Deep | `vm/test_vm_lifecycle.py` | `TestVMConfigOptions` | L3 — enable_console=false AND relay_pid is None/0. No `--console` flag passed. |
| `vm create` with `--console` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMCreateConsole` | L2 — enable_console=true in ls JSON |
| `vm create` default (PCI enabled) | ✅ Deep | `vm/test_vm_lifecycle.py` | `TestVMConfigOptions` | L3 — pci_enabled=true AND VM boots successfully. No `--no-pci` flag passed. |
| `vm create` with `--no-pci` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMConfigOptions` | L2 — pci_enabled=false |
| `vm create` with `--enable-logging` | ✅ Deep | `vm/test_vm_lifecycle.py` | `TestVMConfigOptions` | L3: firecracker.log file exists in vm_dir and is non-empty |
| `vm create` with `--disable-logging` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMConfigOptions` | L2 |
| `vm create` with `--enable-metrics` | ✅ Deep | `vm/test_vm_lifecycle.py` | `TestVMConfigOptions` | L3: firecracker.metrics file exists in vm_dir and is non-empty |
| `vm create` with `--disable-metrics` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMConfigOptions` | L2 |
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
| `vm create` with `--skip-deblob` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMAdvancedCreateFlags` | L2 — VM created with skip-deblob |
| `vm create` with `--nested-virt` | ✅ Deep | `vm/test_vm_nested_isolated.py` | `TestNestedIsolated` | L3 — cpu-config in Firecracker JSON, boot args validated |
| `vm create` with `--cpu-template` | ✅ Deep | `vm/test_vm_nested_isolated.py` | `TestNestedIsolated` | L3 — merged cpu-config in Firecracker JSON |
| `vm create` with `--no-nested-virt` | ✅ Deep | `vm/test_vm_nested_isolated.py` | `TestNestedIsolated` | L3 — no cpu-config, no nested boot args |
| `vm ls` | 🟡 Partial | `vm/test_vm_lifecycle.py`, `vm/test_jailer.py` | `TestVMListInspect`, `TestNetworklessJailerLifecycle` | Runtime status now reconciles through root authority; focused networkless cases cover running/stopped projection, not guest-initiated exit or host reboot. |
| `vm ls --json` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMListInspect` | L2 — checks many fields |
| `vm ls --json` empty | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMListEmpty` | L2 — empty list. Clears VMs first. |
| `vm ps` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMListInspect::test_ps_lists_running, test_ps_shows_running_vm_details` | L2 — name and column headers in table output |
| `vm ps --json` | 🟡 Partial | `vm/test_vm_lifecycle.py`, `vm/test_jailer.py` | `TestVMListInspect`, `TestNetworklessJailerLifecycle` | Focused installed-candidate networkless case proved inclusion while running, exclusion after CLI stop, and inclusion after restart. Guest-initiated exit and host-reboot reconciliation remain unqualified. |
| `vm inspect <name>` | 🟡 Partial | `vm/test_vm_lifecycle.py`, `vm/test_jailer.py` | `TestVMListInspect`, `TestNetworklessJailerLifecycle` | Runtime observation is privileged; focused networkless lifecycle covers the selector, while root-brokered cgroup detail is qualified through `--json` below. |
| `vm inspect <name> --json` | 🟡 Partial | `vm/test_vm_lifecycle.py`, `vm/test_jailer.py`, `vm/test_cgroup.py` | `TestVMListInspect`, `TestNetworklessJailerLifecycle`, `TestVMCgroupEnvelope` | Focused installed-candidate networkless cases proved running/stopped/running status projection and root-brokered `enforced` cgroup limits/usage to an unprivileged operator. Guest-initiated exit and host-reboot reconciliation remain unqualified. |
| `vm inspect` by IP | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMListInspect` | L2 — stop by IP test covers resolution |
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
| Crashed firecracker recovery | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMStateTransitions` | L2 — kill PID, stop succeeds, rm succeeds |
| Config chain precedence | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMConfigOptions` | L2 — vcpu=2 with --vcpu overrides config=4 |
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
| `exec <id>` (interactive shell via stdin) | ⚡ Shallow | `exec/test_exec.py` | `TestExecBasic::test_exec_interactive_shell` | L2 — piped command via stdin |
| `exec` with `--port` | ⚡ Shallow | `exec/test_exec.py` | `TestExecFlags::test_exec_with_port` | L2 — explicit port wiring |
| `exec` with `--timeout` | ⚡ Shallow | `exec/test_exec.py` | `TestExecFlags::test_exec_with_timeout` | L1 — --timeout flag accepted |
| `exec` with `--user` | ⚡ Shallow | `exec/test_exec.py` | `TestExecFlags::test_exec_with_user` | L2 — whoami returns user name |
| `exec` nonexistent VM | ⚡ Shallow | `exec/test_exec.py` | `TestExecErrorCases::test_exec_nonexistent_vm` | L1 — "not found" in error |
| `exec` invalid port | ⚡ Shallow | `exec/test_exec.py` | `TestExecErrorCases::test_exec_invalid_port` | L1 — non-zero exit |
| `exec` empty command | ⚡ Shallow | `exec/test_exec.py` | `TestExecErrorCases::test_exec_empty_command` | L1 — guidance instead of execution |

---

## `mvm snapshot` (alias: `ss`)

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `snapshot create <vm>` | 🟡 Partial | `vm/test_vm_snapshot_load.py`, `vm/test_jailer.py` | `TestVMSnapshot`, `TestSnapshotDestroyRestore`, `TestNetworklessJailerLifecycle` | Focused installed-candidate networkless full capture with an attached raw volume passed; managed-network and fault paths remain unqualified. |
| `snapshot create --name` | ⚡ Shallow | `vm/test_vm_snapshot_load.py` | `TestSnapshotCreateFlags::test_snapshot_create_with_name` | L2 — custom name in ls --json |
| `snapshot create --pause` | ⚡ Shallow | `vm/test_vm_snapshot_load.py` | `TestSnapshotCreateFlags::test_snapshot_create_pause_flag` | L2 — VM status=paused after snapshot |
| `snapshot create` on stopped VM | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMStateTransitions` | L1 — non-zero, no snapshot files created |
| `snapshot ls` | ⚡ Shallow | `vm/test_vm_snapshot_load.py` | `TestVMSnapshot`, `TestSnapshotDestroyRestore` | L2 — JSON listing |
| `snapshot ls --json` | ⚡ Shallow | `vm/test_vm_snapshot_load.py` | `TestVMSnapshot`, `TestSnapshotDestroyRestore` | L2 — JSON list |
| `snapshot inspect <id>` | ⚡ Shallow | `vm/test_vm_snapshot_load.py` | `TestSnapshotInspect::test_snapshot_inspect` | L1 — inspect succeeds |
| `snapshot inspect <id> --json` | ⚡ Shallow | `vm/test_vm_snapshot_load.py` | `TestSnapshotInspect::test_snapshot_inspect_json` | L2 — JSON fields verified |
| `snapshot restore <id> <name>` | 🟡 Partial | `vm/test_vm_snapshot_load.py`, `vm/test_jailer.py` | `TestVMSnapshot`, `TestSnapshotDestroyRestore`, `TestNetworklessJailerLifecycle` | Focused networkless restore passed with no endpoint, independent attached-volume bytes, vsock exec, and checked teardown. A red-green installed-candidate L2 case proves pre-spawn missing-release failure leaves no VM projection or snapshot reference. Managed-network, post-spawn, and transport-uncertain paths remain unqualified. |
| `snapshot restore --resume` | 🟡 Partial | `vm/test_vm_snapshot_load.py`, `vm/test_jailer.py` | `TestVMSnapshot`, `TestSnapshotDestroyRestore`, `TestNetworklessJailerLifecycle` | Focused installed-candidate networkless restore resumed, preserved absent endpoint, restored independent data-volume bytes, and allowed vsock exec. Other placements remain unqualified. |
| `snapshot restore --network` | 🟡 Partial | `vm/test_vm_snapshot_load.py`, `vm/test_jailer.py` | `TestSnapshotRestoreNetwork`, `TestNetworklessJailerLifecycle` | Focused installed-candidate networkless snapshot rejected a managed-network override without creating a target VM. The legacy test expects a different network; that outdated positive contract still needs reconciliation, and managed-network override behavior remains unqualified. |
| `snapshot rm <id>` | ⚡ Shallow | `vm/test_vm_snapshot_load.py` | `TestSnapshotDestroyRestore` | L2 — cleanup via --force |
| `snapshot rm --force` | ⚡ Shallow | `vm/test_vm_snapshot_load.py` | `TestSnapshotDestroyRestore` | L2 — forced removal |

---

## `mvm volume`

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `volume create <size>` (raw-only) | 🟡 Partial | `volume/test_volume.py`, `vm/test_jailer.py` | `TestRawVolumeAuthoritativeStorage`, `TestNetworklessJailerLifecycle`, `TestVolumeLifecycle` | Focused installed-candidate 16 MiB create, inspect, 32 MiB grow, checked remove, guest-visible networkless attach/restart, and full snapshot/restore of independent data-volume bytes passed in the named runner; broader options remain unqualified. |
| `volume create --format` (removed) | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle::test_volume_create_removed_format_flag_rejected_without_side_effects` | Installed-candidate raw/QCOW2/VMDK variants passed 3/3: unknown flag, no new volume record or backing file, checked sentinel cleanup. QCOW2 **image import** remains supported. |
| `volume create` invalid size | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle` | L1 |
| `volume create` duplicate name | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle` | L1 |
| `volume create` negative size | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle` | L1 |
| `volume create` zero size | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle` | L1 |
| `volume create --read-only` | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle` | L2 — is_read_only=true in ls JSON, inspect JSON |
| `volume create --shareable` | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle::test_volume_create_shareable` | L2 — is_shareable=true in ls JSON |
| `volume ls` | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle` | L1 |
| `volume ls --json` | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle` | L2 |
| `volume ls` empty | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle` | L1 |
| `volume inspect` | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle` | L1 |
| `volume inspect --json` | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle` | L2 — name, size_bytes, format, status, path |
| `volume inspect` nonexistent | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle` | L1 |
| `volume rm` | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle` | L2 |
| `volume rm --force` | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle` | L2 |
| `volume rm` nonexistent | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle` | L1 |
| `volume rm <name1> <name2>` | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle` | L2 |
| `volume rm` partial failure | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle` | L2 — one exists, one doesn't |
| `volume resize` | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle` | L2 — size_bytes matches |
| `volume resize` nonexistent | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle` | L1 |
| `volume resize` shrink | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeLifecycle::test_volume_resize_rejects_shrink_without_changing_backing` | Focused installed-candidate 16→8 MiB rejection, successful same-size control, and unchanged authoritative size passed in the named runner. |
| Volume invariants (available→attached→available) | ✅ Deep | `volume/test_volume.py` | `TestVolumeLifecycle` | L3 — vm_id transitions, path exists on disk |
| Cross-VM attach rejection | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeCrossVM` | L2 — attached volume rejects second VM |
| Volume rm with running VM | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeRunningVMDependency` | L1 |
| Volume rm --force with running VM | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeRunningVMDependency` | L2 |
| Volume resize with running VM | ⚡ Shallow | `volume/test_volume.py` | `TestVolumeRunningVMDependency` | L2 |
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
| `key create --out` | ✅ Deep | `keys/test_keys.py` | `TestKeyCreateAdvanced` | L3 — files on disk |
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
| `key rm --force` | ⚡ Shallow | `keys/test_keys.py` | `TestKeyRunningVMDependency` | L2 |
| `key rm <name1> <name2>` | ⚡ Shallow | `keys/test_keys.py` | `TestKeyLifecycle` | L2 |
| `key default` | ⚡ Shallow | `keys/test_keys.py` | `TestKeyLifecycle` | L2 |
| `key default --clear` | ⚡ Shallow | `keys/test_keys.py` | `TestKeyLifecycle` | L2 |
| `key export --out` | ✅ Deep | `keys/test_keys.py` | `TestKeyLifecycle` | L3 — files on disk |
| `key export` overwrite | ⚡ Shallow | `keys/test_keys.py` | `TestKeyExportForce` | L1 |
| `key export --force` | ✅ Deep | `keys/test_keys.py` | `TestKeyExportForce` | L3 |
| Multiple defaults | ⚡ Shallow | `keys/test_keys.py` | `TestKeyDefaults` | L2 — is_default for both |
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
| `image import --format qcow2` | ⚡ Shallow | `images/test_images.py` | `TestImageImportAdvanced` | Installed rc.7: cached Alpine raw→QCOW2→ext4 import, detected distro, present record, checked removal; VM boot remains unproved. |
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
| `kernel rm` blocked by stopped VM | 🟡 Partial | `kernel/test_kernel.py` | `TestKernelStoppedVMDeletion` | Owner-scoped rejection case now requires `kernel.in_use` and preserves VM/kernel; not run because the runner lacks bridge nftables. |
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
| `ssh <vm> --cmd <cmd>` | ✅ Deep | `vm/test_vm_lifecycle.py` | `TestVMCloudInit`, `TestVMVolumeIntegration` | L3 — SSH connectivity verified |
| `ssh <vm> -u <user> --cmd` | ✅ Deep | `vm/test_vm_lifecycle.py` | `TestVMCloudInit` | L3 |
| `ssh <vm> --cmd exit` | ✅ Deep | `vm/test_vm_lifecycle.py` | `TestVMSSHIntegration` | L3 |
| `ssh <vm> --key <name>` | 🟡 Partial | `ssh/test_ssh.py` | `TestSSHConnect` | L3 — SSH with named key, skips if SSH unavailable |
| `ssh <vm> --key <path>` | ✅ Deep | `ssh/test_ssh.py` | `TestSSHConnect` | L3: SSH succeeds with exported key file path |
| `ssh <vm> --timeout <sec>` | ⚡ Shallow | `ssh/test_ssh.py` | `TestSSHConnect` | L1 — SSH with --timeout succeeds |

---

## `mvm console`

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `console <vm> --state` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMListInspect` | L1 — checks state |
| `console <vm> --kill` | ⚡ Shallow | `console/test_console.py` | `TestConsoleKill` | L1 — state check before/after kill |
| `console <nonexistent> --state` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMListInspect` | L1 |
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
| `host clean --force` | ⏭️ Skip | `host/test_host.py` | `TestHostCleanDestructive` | L1 — marked host_reset, excluded from default runs |
| `host clean` blocked by running VM | ⚡ Shallow | `host/test_host.py` | `TestHostCleanSafety` | L1 |
| `host reset --force` | ⚡ Shallow | `host/test_host.py` | `TestHostCleanDestructive` | L1 — exit 0, non-empty stdout. Marked host_reset (excluded from default runs) |
| `host reset` blocked by running VM | ⚡ Shallow | `host/test_host.py` | `TestHostResetSafety` | L1 |
| `host install-system` | ✅ Deep | `host/test_system_install.py` | `TestSystemBinaryInstall` | The Python runner stages the RC, invokes the real administrator installer, and then all `_run_mvm` calls use exact `/usr/local/bin/mvm`. L2 verifies root ownership, mode, trusted ancestors, content hash, no temporary sibling, and idempotence. |
| `host install-system` rejection cases | ✅ Deep | `host/test_system_install.py` | `TestSystemBinaryInstall` | L2 verifies unprivileged and malformed invocations preserve the target, and an existing target symlink is rejected without changing its destination. Symlink-test restoration is mandatory and asserted. |
| `host install-system` pre-rename failure | ✅ Deep | `host/test_system_install.py`, `internal/core/host/system_binary_test.go` | `TestSystemBinaryInstall`, `TestInstallSystemBinary_PreRenameFailuresPreserveTarget` | L1 covers each injected pre-rename stage. L2 bind-remounts `/usr/local/bin` read-only, invokes the exact staged candidate, verifies the public failure and unchanged canonical metadata/hash, then requires rw remount, unmount, no mount leak, and executable restoration. |
| `host install-system` remaining partial failures | ⚡ Shallow | `internal/core/host/system_binary_test.go`, `pkg/api/host_system_binary_test.go` | `TestInstallSystemBinary_PreRenameFailuresPreserveTarget`, `TestInstallSystemBinary_ReportsPostRenameAndCleanupFailures`, `TestHostInstallSystemBinary_PreservesPartialSuccess` | L1 deterministically covers write/metadata/fsync/close/rename failures, cleanup failure, and post-rename durability reporting. No safe deterministic public-boundary L2 injection exists for those individual stages; uncontrolled disk filling, timing kills, and production test hooks are explicitly rejected. |
| `host init` | 🟡 Partial | `host/test_system_install.py`, `host/test_host.py` | `TestSystemBinaryInstall`, `TestHostInit` | L2 invokes exact `sudo /usr/local/bin/mvm host init` in a runner that already has the `mvm` group and membership. It proves happy-path sudoers reconciliation and invoking-user cache/config ownership. Task 13 gaps: group creation/membership, root-owned journal, rollback, and fail-closed mutation errors. |

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
| `cp <src> <dst>` (host→VM, file) | ✅ Deep | `cp/test_cp.py` | `TestCpHostToVm` | L3: file exists on VM via SSH, content verified |
| `cp <src> <dst>` (host→VM, dir) | ✅ Deep | `cp/test_cp.py` | `TestCpHostToVm` | L3: dir + nested files exist on VM via SSH |
| `cp <src> <dst>` (VM→host, file) | ✅ Deep | `cp/test_cp.py` | `TestCpVmToHost` | L3: file exists on host, content matches round-trip |
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
| Jailer launch/cleanup through public VM lifecycle | 🟡 Partial | `vm/test_jailer.py` | `TestCanonicalJailerLaunch`, `TestNetworklessJailerLifecycle` | L2 — networkless installed-candidate lifecycle and missing-release restore proved; managed-network lifecycle awaits the bridge-capable runner kernel |

> Internal service subprocesses are tested indirectly through VM operations (console relay, nocloudnet HTTP, volume provisioning).

---

## `mvm full-journeys` (end-to-end workflows)

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| Quick-start journey (create key+net+VM, SSH, cleanup) | ✅ Deep | `full_journeys/test_full_journeys.py` | `TestQuickStartJourney` | L3: full quick-start from README — create→SSH→rm |
| Network→VM journey | ⚡ Shallow | `full_journeys/test_full_journeys.py` | `TestNetworkVMJourney` | L2: create network, VM on it, verify via ls --json |
| Key→VM journey | ⚡ Shallow | `full_journeys/test_full_journeys.py` | `TestKeyVMJourney` | L2: create key, VM with key, verify via ls --json |
| VM state journey (create→pause→resume→stop→start) | ⚡ Shallow | `full_journeys/test_full_journeys.py` | `TestVMStateJourney` | L2: full state transitions, ls --json status at each step |
| IP journey (explicit --ip + SSH) | ✅ Deep | `full_journeys/test_full_journeys.py` | `TestIPJourney` | L3: explicit IP, SSH, two VMs on same network both reachable |
| SSH journey (command + reboot) | ✅ Deep | `full_journeys/test_full_journeys.py` | `TestSSHJourney` | L3: SSH cmd execution, reboot chain, SSH after reboot |
| Multi-key VM journey | ⚡ Shallow | `full_journeys/test_full_journeys.py` | `TestMultiKeyJourney` | L2: create VM with two --ssh-key flags |
| Inter-VM communication | ✅ Deep | `full_journeys/test_full_journeys.py` | `TestInterVMCommunication` | L3: 2 VMs on same network, SSH VM-A, ping VM-B |
| Create with all flags | ⚡ Shallow | `full_journeys/test_full_journeys.py` | `TestCreateWithAllFlags` | L2: --vcpu, --mem, -s, --enable-logging, --enable-metrics |
| Multiple volumes | ⚡ Shallow | `full_journeys/test_full_journeys.py` | `TestMultipleVolumes` | L2: 3 volumes, VM with all 3 via --volume |
| Stress create/destroy | ⚡ Shallow | `full_journeys/test_full_journeys.py` | `TestStressCreateDestroy` | L2: 5 sequential create→destroy cycles |
| Export/import config journey | ⚡ Shallow | `full_journeys/test_full_journeys.py` | `TestExportImport` | L2: inspect --json schema verification |
| Concurrent VM creation | ✅ Deep | `full_journeys/test_full_journeys.py` | `TestConcurrentVMCreation` | L3: 10 concurrent VMs, all running via ls --json, SSH into each |
| Canonical Jailer lifecycle | 🟡 Partial | `vm/test_jailer.py`, `full_journeys/test_full_journeys.py` | `TestCanonicalJailerLaunch`, `TestNetworklessJailerLifecycle` | L2: networkless exact-pair launch with operator traversal denied, isolated VMM identity, lifecycle, volume, snapshot, and missing-release restore denial. Networked Jailer cases remain unqualified on the current runner kernel. |
| Typed cgroup-v2 envelope | 🟡 Partial | `vm/test_cgroup.py`, `vm/test_jailer.py` | `TestVMCgroupEnvelope`, `TestCanonicalJailerLaunch` | Focused L2: protected policy-v2 limit/usage inspection, operator direct-read denial, stop/start/remove cleanup, and charged guest tmpfs load with responsive exec and no VM-leaf hard maximum. Snapshot preservation, controller-unavailable admission, sustained saturation/host-OOM survival, and broader exact-identity cleanup remain unqualified. |

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
| key | 28 | 3 | 25 | 0 | 0 | 0 |
| image | 41 | 0 | 16 | 3 | 3 | 19 |
| kernel | 29 | 0 | 18 | 7 | 2 | 2 |
| bin | 17 | 2 | 6 | 1 | 0 | 8 |
| ssh | 6 | 4 | 1 | 1 | 0 | 0 |
| console | 6 | 0 | 6 | 0 | 0 | 0 |
| logs | 9 | 0 | 9 | 0 | 0 | 0 |
| host | 15 | 6 | 7 | 1 | 0 | 1 |
| cache | 13 | 3 | 9 | 0 | 0 | 1 |
| cp | 12 | 8 | 3 | 1 | 0 | 0 |
| env | 22 | 9 | 13 | 0 | 0 | 0 |
| run | 4 | 1 | 3 | 0 | 0 | 0 |
| full-journeys | 15 | 7 | 8 | 0 | 0 | 0 |
| **Total** | **430** | **81** | **288** | **22** | **8** | **31** |

**Current-row coverage health:**
- ✅ Deep (L3): 81/430 = 18.8%
- ⚡ Shallow (L0-L2): 288/430 = 67.0%
- 🟡 Partial: 22/430 = 5.1%
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
