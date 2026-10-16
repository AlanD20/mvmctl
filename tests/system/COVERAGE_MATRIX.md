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

**Checkpoint (2026-10-16):** the candidate-bound **528/528** evidence join passed:
526 ordinary nodes plus both actual host-reboot phases, with strict native/JSON/JUnit/node accounting
and checked owned cleanup/preservation. Collection alone and failed attempts add no passing credit.
v0.3.0 remains unreleased; remote CI, aarch64 runtime qualification and final release approval remain open.

Candidate: `mvm 0.3.0-rc.kernel-details-ce5b0913`, producer
`ce5b09131d2bb58d30fca100edf46648a6fbc4bd`, SHA-256
`565be5a66432788f405e64337e6902a573da0dafe9efd0f4ef46480c5593060a`.

| Accepted scope | Nodes |
|---|---:|
| Tier 1 | 238 |
| Tier 2 | 193 |
| Tier 3 | 95 |
| Actual host reboot PRE/POST | 2 |

The closed `rc030-candidate565-oct16` profile verifies the following original execution bindings.
It is a mixed-source join, not one full latest-HEAD invocation or permission for general source carry.

| Original source | Accepted nodes | Collection receipt prefix |
|---|---:|---|
| `7f49cd0a1213b96bbe66c4e5b12454d2d7207f09` | 344 | `45567a44` |
| `0574702f3b6052b78d1996978e2daf2225f6f0cc` | 34 | `34d66323` |
| `4764bb0871b626695198248bb8369bca4500a4e4` | 118 | `38ede600` |
| `c4acd1c112f98980cd3c6f2666dd6fe8c08bc7d9` | 8 | `1938de73` |
| `a33e6623dfcaf80bfb78a891e6ef84e831f2e496` | 22 kernel + 2 reboot | `285d7ff9` |

The native-zero finalizer records `verdict: PASS`, retaining 504 original-source cases and 22 fresh
kernel cases plus stock a33 PRE/reboot/POST. Verifier `1122be66`, qualified source a33/tree
`32cb1a84de0ef3c5fe803e5813a3e3ae7420df0f`, and documentation checkpoint `df00fc57` remain distinct.
Reboot proof covers changed host boot, stale-running reconciliation, same-ID restart, retained-marker exec,
inventory preservation and typed cleanup. Temporary QA read authority and the owned setup network were removed.

Proof identifiers:

- Finalizer output SHA-256: `b63c505097e7e7699b41b3c4d78f8efe00f8b4ce48ad1f247f00d99ee3f981c0`.
- Reboot POST terminal SHA-256: `f0783c8e9508fc60a2c4042f62090b23733f21b20a97060e04876e3a64a8c4ad`.
- [Finalization contract](../../docs/development/HOW_TO_RUN_SYSTEM_TESTS.md#63-join-verified-receipts-read-only).

### Remaining evidence limits

Passing every registered node does not upgrade Partial/Missing rows below or establish unlisted fault behavior.
The CLI tables retain their detailed command/flag evidence and gaps. Additional limits from prior qualification:

- Original exec/cache transients and the original saturation incident have not been attributed to a diagnosed
  production cause. Later passes do not explain earlier failures. Pressure-only `memory.max=max` does not
  guarantee survival under host/ancestor/global OOM or guest OOM.
- Public lifecycle passes do not establish exhaustive fd-0 framing/EOF/peer/cancellation abuse, cross-UID/root-record
  replacement races, trusted-release manifest/ELF/hash failure injection, private mount/resource substitution,
  namespace rollback or zero-leak recovery. These authority/fault requirements need their own direct evidence;
  aggregate node counts do not close them.
- Neighbor-reuse reachability passed, but guest boot ARP prevents independent attribution of pre-exposure
  invalidation ordering. Spoofing and prior-host-boot neighbor recovery remain unqualified. Traffic-policy
  spoofing, IP reuse, cross-network UDP and policy-specific reboot/fault recovery are not established by the
  generic host-reboot journey.
- Directional VM-to-VM exec passes do not qualify complete private-backend cleanup or every peer/capacity/deadline/
  cancellation fault. Running each applicable domain twice with aggregated post-failure/reboot absence checks
  remains additional evidence, not a property inferred from this join.
- Guest-initiated poweroff, broader snapshot interruption/post-spawn/transport-uncertain recovery and other
  command-specific gaps remain as listed below. Unknown ownership or durability/cleanup outcomes stay explicit;
  snapshot recovery may require the emitted full ID.
- Bounded snapshot/data-volume consistency is not application transaction atomicity or exclusion of every possible
  host writer. VM-to-image import is a root-disk copy, not a full RAM/data-volume snapshot. Release-derived device
  budgets, the 10,000,000-byte complete vmstate limit and exact trusted-RAM extent still apply; no physical-storage
  reservation or new per-volume/aggregate quota is promised.
- Performance measurements predating the isolated root-broker runtime do not establish current release timing.
  Cross-compilation does not qualify aarch64 runtime; remote CI and the final release decision remain separate.

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
| `vm ls` | 🟡 Partial | `vm/test_vm_lifecycle.py`, `vm/test_jailer.py`, `host/test_host_reboot.py` | `TestVMListInspect`, `TestNetworklessJailerLifecycle`, `TestVMStateTransitions`, `test_host_reboot_post` | Root-authority running/stopped projection and confirmed external Firecracker exit (`crashed`) passed. Actual host-reboot stale-running reconciliation and restart also passed. Guest-initiated exit remains open. |
| `vm ls --json` | 🟡 Partial | `vm/test_vm_lifecycle.py` | `TestVMListInspect::test_listing_json[ls]`, `TestVMStateTransitions::test_crashed_vm_status_and_restart` | Installed-candidate L2 proves schema-1 running identity/resource fields and `crashed` projection after pidfd-confirmed exit. Guest-initiated exit remains open. |
| `vm ls --json` empty | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMListEmpty` | Installed-candidate L2 passed a read-only versioned empty-list assertion on the clean runner; no global deletion. |
| `vm ps` | ⚡ Shallow | `vm/test_vm_lifecycle.py` | `TestVMListInspect::test_listing_text[ps]` | Installed-candidate L2 passed actual human output containing the exact VM name, IPv4, and headers. |
| `vm ps --json` | 🟡 Partial | `vm/test_vm_lifecycle.py`, `vm/test_jailer.py`, `host/test_host_reboot.py` | `TestVMListInspect`, `TestNetworklessJailerLifecycle`, `TestVMStateTransitions`, `test_host_reboot_post` | Installed cases prove inclusion while running and after restart, exclusion after CLI stop and confirmed external Firecracker exit. Actual host-reboot stale-state exclusion and restart passed. Guest-initiated exit remains open. |
| `vm inspect <name>` | 🟡 Partial | `vm/test_vm_lifecycle.py`, `vm/test_jailer.py` | `TestVMListInspect::test_inspect_tree`, `TestNetworklessJailerLifecycle` | Installed-candidate L2 passed the actual text route with exact name, full ID, IPv4, and running status. Root-brokered cgroup detail is qualified through `--json` below. |
| `vm inspect <name> --json` | 🟡 Partial | `vm/test_vm_lifecycle.py`, `vm/test_jailer.py`, `vm/test_cgroup.py`, `host/test_host_reboot.py` | `TestVMListInspect`, `TestNetworklessJailerLifecycle`, `TestVMCgroupEnvelope`, `TestVMStateTransitions`, `test_host_reboot_post` | Installed cases prove running/stopped/running and confirmed-exit `crashed` projection, plus root-brokered cgroup limits/usage. Actual host-reboot reconciliation and same-ID restart passed. Guest-initiated exit remains open. |
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
| Crashed firecracker recovery | 🟡 Partial | `vm/test_vm_lifecycle.py`, `host/test_host_reboot.py` | `TestVMStateTransitions::test_crashed_vm_status_and_restart`, `test_host_reboot_post` | Installed-candidate L2 proves exact pidfd-bound crash/exit, public ls/ps/inspect reconciliation, typed stop/start, vsock exec, and checked removal. Separate actual host-reboot recovery passed; guest poweroff remains open. |
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
| `kernel pull --type official` | ⚡ Shallow | `kernel/test_kernel.py` | `TestKernelLifecycle` | Installed c54/57c60574: completed official-eight sequence, strict 8/8 zero-other outcomes; this case checks successful official pull and a present listing. Exact owned retirement and baseline preservation passed. |
| `kernel pull` with `--version` | ⚡ Shallow | `kernel/test_kernel.py` | `TestKernelPullWithVersion` | L2: pull specific firecracker versions v1.15, v1.14, verify via ls --json |
| `kernel pull` arch auto-detection | ⏭️ Skip | `kernel/test_kernel.py` | `TestKernelPullArch` | Network-dependent. L2: ls --json. No `--arch` flag — arch is auto-detected by Go CLI. |
| `kernel pull` advanced flags | ⚡ Shallow | `kernel/test_kernel.py` | `TestKernelPullAdvancedFlags` | Official-eight Tier 2 case passed on c54/57c60574: successful official pull, present listing and x86_64 publication receipt. No explicit `--arch` flag or cross-architecture runtime claim. |
| `kernel pull --jobs` | ⚡ Shallow | `kernel/test_kernel.py` | `TestKernelBuild` | Official-eight separately required Tier 3 case passed: `--jobs 2`, successful publication/listing and typed retirement. Compiler execution or actual parallelism is not proved by this flag/cache-reuse result. |
| `kernel pull --keep-build-dir` | ⚡ Shallow | `kernel/test_kernel.py` | `TestKernelBuild` | Official-eight separately required Tier 3 case passed: flag acceptance, publication/listing and typed retirement. Build-directory retention and compiler execution are not inferred from success alone. |
| `kernel pull --clean-build` | ⚡ Shallow | `kernel/test_kernel.py` | `TestKernelBuild` | Official-eight separately required Tier 3 case passed: successful clean-build publication, present backing file and retained producer-log receipt; typed retirement preserved the baseline. No boot qualification. |
| `kernel pull --config` | ⚡ Shallow | `kernel/test_kernel.py` | `TestKernelBuild` | Official-eight separately required Tier 3 case passed: custom fragment, publication/backing-file checks and retained producer-log receipt. No booted config/feature assertion. |
| `kernel pull --features` | ⚡ Shallow | `kernel/test_kernel.py` | `TestKernelBuild` | Official-eight separately required Tier 3 case passed: `--features kvm`, successful publication and retained producer-log receipt. Guest KVM capability is not established by this pull case. |
| `kernel pull --default` | ⚡ Shallow | `kernel/test_kernel.py` | `TestKernelRemoveAndPull` | Official-eight separately required Tier 3 case passed: authoritative publication reports `is_default: true`, the listing contains a present official default, and typed retirement/final cleanup preserve original defaults and inventories. |
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
| Jailer launch/cleanup through public VM lifecycle | 🟡 Partial | `vm/test_jailer.py` | `TestCanonicalJailerLaunch`, `TestNetworklessJailerLifecycle` | The full 13-case Jailer module passed in the bridge-capable named runner, including managed launch, isolated process identity, stop/start/reboot, and networkless restore. The separate actual host-reboot journey passed; broader failure-path reconciliation remains open. |

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
| Canonical Jailer lifecycle | 🟡 Partial | `vm/test_jailer.py` | `TestCanonicalJailerLaunch`, `TestNetworklessJailerLifecycle` | Canonical cases are selected once in the Jailer domain, not re-exported by full_journeys. The full 13-case Jailer module passed in the bridge-capable named runner, covering managed launch/stop/start/reboot and networkless identity, volume, snapshot, and missing-release checks. The separate actual host-reboot journey passed; broader failure-path reconciliation remains open. |
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
