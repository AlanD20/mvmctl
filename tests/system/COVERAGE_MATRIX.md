# System Test Coverage Matrix

**Purpose:** Accountability document. Every CLI command and flag must have
a documented test status. Update this matrix when CLI flags are added,
removed, or when test coverage changes.

**Legend:**

| Status | Meaning |
|--------|---------|
| ✅ Deep | L3 Option C verification |
| ⚡ Shallow | L0-L2 verification (returncode, output, JSON) |
| 🟡 Partial | Test exists but skips under some conditions |
| 🔴 Missing | No test exists |
| ⏭️ Skip | Test defined but always skips (broken setup) |
| **?** | Needs investigation — coverage unclear |

---

## Root CLI

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `--version` | ⚡ Shallow | `test_init.py` | `TestRootFlags` | Check: non-empty string with digits. Could be L2 by parsing version format. |
| `--verbose` | ⚡ Shallow | `test_init.py` | `TestRootFlags` | Check: config get still works. |
| `--debug` | ⚡ Shallow | `test_cli_edge_cases.py` | `TestDebugFlagOutput` | Check: stderr has DEBUG. | 
| `help` | ⚡ Shallow | `test_cli_edge_cases.py` | `TestHelpCommand` | root, subcommand, subsubcommand, nonexistent, version |
| `help` (consistency) | ⚡ Shallow | `test_cli_edge_cases.py` | `TestHelpOutputConsistentFormat`, `TestHelpSubcommandShowsCorrectly` | Check: Usage:, Commands:, --help in every group |
| `version` (command) | ⚡ Shallow | `test_cli_edge_cases.py` | `TestHelpCommand` | L1: stdout has version-like content with digits |
| `completion bash\|zsh\|fish` | ⚡ Shallow | `test_cli_edge_cases.py` | `TestHelpCommand` | L1: stdout contains shell completion definitions |

---

## `mvm init`

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `init --non-interactive --skip-host` | ⚡ Shallow | `test_init.py` | `TestInitWizard` | L1 only — checks output message |
| `init --non-interactive` (no skip) | ⚡ Shallow | `test_init.py` | `TestInitWizard` | Checks non-zero + sudo mention |
| `init` idempotent | ⚡ Shallow | `test_init.py` | `TestInitWizard` | Two runs succeed |
| `init --skip-network` | ⚡ Shallow | `test_cli_edge_cases.py` | `TestInitEdgeCases` | L1: exit 0 with success message |

---

## `mvm config`

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `config get <cat> <key>` (existing) | ⚡ Shallow | `test_config.py` | `TestConfigLifecycle` | L2 via regex parse of output — fragile. |
| `config get <cat>` (category only) | ⚡ Shallow | `test_config.py` | `TestConfigEdgeCases` | Checks multiple keys listed |
| `config get <cat> <nonexistent>` | ⚡ Shallow | `test_config.py` | `TestConfigEdgeCasesExtended` | Exit 0, guidance returned |
| `config set <cat> <key> <val>` then get | ⚡ Shallow | `test_config.py` | `TestConfigLifecycle` | L2 roundtrip |
| `config set` invalid category | ⚡ Shallow | `test_config.py` | `TestConfigEdgeCases` | Non-zero exit |
| `config set` invalid value type | ⚡ Shallow | `test_config.py` | `TestConfigEdgeCasesExtended` | Non-zero exit |
| `config reset <cat> <key>` | ⚡ Shallow | `test_config.py` | `TestConfigLifecycle` | L2 — value not in output after reset |
| `config reset <cat>` (category only) | ⚡ Shallow | `test_config.py` | `TestConfigEdgeCases` | L2 — value not in output |
| `config reset` no args | ⚡ Shallow | `test_config.py` | `TestConfigEdgeCases` | Shows guidance, exit 0 |
| `config reset --all` | ⚡ Shallow | `test_config.py` | `TestConfigLifecycle`, `TestConfigEdgeCasesResetAllAfterSet` | Multiple overrides then reset --all |
| `config ls` | ⚡ Shallow | `test_config.py` | `TestConfigLifecycle` | L1 — checks [defaults.vm] in output |

---

## `mvm network`

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `network create <name> --subnet <cidr>` | ✅ Deep | `test_network.py` | `TestNetworkLifecycle` | L3: bridge exists, IP assigned, firewall rules |
| `network create` without --subnet | ⚡ Shallow | `test_cli_edge_cases.py` | `TestNetworkEdgeCases` | L1: checks error message |
| `network create` invalid CIDR | ⚡ Shallow | `test_network.py` | `TestNetworkLifecycle` | L1 |
| `network create` /32 subnet | ⚡ Shallow | `test_network.py` | `TestNetworkLifecycle` | L1 |
| `network create` duplicate name | ⚡ Shallow | `test_network.py` | `TestNetworkLifecycle` | L1 |
| `network create` duplicate subnet | ⚡ Shallow | `test_network.py` | `TestNetworkLifecycle` | L1 — two tests for this: test_overlapping_subnet_across_networks_rejected, test_overlapping_subnet_rejected |
| `network create --default` | ⚡ Shallow | `test_network.py` | `TestNetworkLifecycle` | L2 — is_default=true in ls JSON |
| `network create --no-nat` | ✅ Deep | `test_network.py` | `TestNetworkLifecycle` | L3: bridge exists, _assert_no_masquerade_rule confirms no MASQUERADE rule exists |
| `network create --ipv4-gateway` | ✅ Deep | `test_network.py` | `TestNetworkAdvancedCreate` | L2 — checks inspect JSON. Could be L3 (bridge IP). |
| `network create --nat-gateways` | ⚡ Shallow | `test_network.py` | `TestNetworkAdvancedCreate` | L2 — checks inspect JSON |
| `network create` invalid gateway | ⚡ Shallow | `test_network.py` | `TestNetworkAdvancedCreate` | L1 |
| `network create --non-interactive` | ⚡ Shallow | `test_cli_edge_cases.py` | `TestNetworkEdgeCases` | L2: ls --json confirms network created |
| `network ls` | ⚡ Shallow | `test_network.py` | `TestNetworkLifecycle` | L1 |
| `network ls --json` | ⚡ Shallow | `test_network.py` | `TestNetworkLifecycle` | L2 — checks list, name, id, subnet |
| `network ls --json` empty | ⚡ Shallow | `test_network.py` | `TestNetworkLifecycle` | L2 — valid empty list |
| `network inspect <name>` | ⚡ Shallow | `test_network.py` | `TestNetworkLifecycle` | L1 |
| `network inspect <name> --json` | ⚡ Shallow | `test_network.py` | `TestNetworkLifecycle` | L2 — name, subnet, bridge |
| `network inspect <name>` | ⚡ Shallow | `test_network.py` | `TestNetworkInspectTree` | L1 |
| `network rm <name>` | ✅ Deep | `test_network.py` | `TestNetworkLifecycle` | L2 listing + bridge verification |
| `network rm <nonexistent>` | ⚡ Shallow | `test_network.py` | `TestNetworkLifecycle` | L1 |
| `network rm --force` | ✅ Deep | `test_network.py` | `TestNetworkRemoveForce` | L3: bridge interface gone after removal (ip link show) |
| `network rm <name1> <name2>` | ⚡ Shallow | `test_network.py` | `TestNetworkLifecycle` | L2 — listing check |
| `network default <name>` | ⚡ Shallow | `test_network.py` | `TestNetworkLifecycle` | L2 — is_default in JSON |
| `network default <nonexistent>` | ⚡ Shallow | `test_network.py` | `TestNetworkLifecycle` | L1 |
| `network sync` | ✅ Deep | `test_network.py` | `TestNetworkSync` | L3: bridge, IP, firewall rules |
| `network sync --json` | ⚡ Shallow | `test_network.py` | `TestNetworkSync` | L2 — result dict with per-network stats |
| `network sync <specific>` | ✅ Deep | `test_network.py` | `TestNetworkSync` | L3: bridge and rules |
| `network sync` idempotent | ✅ Deep | `test_network.py` | `TestNetworkSync` | L3: rule count unchanged |
| Sync after bridge deletion | ✅ Deep | `test_network.py` | `TestNetworkSyncAfterReboot` | L3: bridge recreated, IP reassigned, rules restored |
| `network sync` conntrack rule | ✅ Deep | `test_network.py` | `TestNetworkSync` | Conntrack established/related accept rule |
| `network sync` nonexistent | ⚡ Shallow | `test_network.py` | `TestNetworkSync` | Error handling for nonexistent network |

---

## `mvm vm`

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `vm create` basic | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMListInspect` (via module_vm), `TestVMCreate` | L2 — status=running. Many shallow VM tests. |
| `vm create` with `--vcpus` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMConfigOptions` | L2 — vcpu_count in ls JSON |
| `vm create` with `--vcpus 0` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMConfigOptions` | L1 — non-zero exit |
| `vm create` with `--vcpus -1` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMConfigOptions` | L1 — non-zero exit |
| `vm create` with `--mem` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMConfigOptions` | L2 — mem_size_mib |
| `vm create` with `--mem 0` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMConfigOptions` | L1 |
| `vm create` with `--disk-size` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMConfigOptions` | L2 — disk_size_mib |
| `vm create` with `--disk-size 0` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMConfigOptions` | L1 |
| `vm create` with `--disk-size invalid` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMConfigOptions` | L1 |
| `vm create` with `--kernel` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMConfigOptions` | L2 — kernel_id starts with prefix |
| `vm create` with `--boot-args` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMConfigOptions` | L2 — boot_args in ls JSON |
| `vm create` with `--ip` (static) | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMNetworkIntegration` | L2 — ipv4 field in ls JSON |
| `vm create` with invalid IP | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMNetworkIntegration` | L1 |
| `vm create` with `--mac` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMNetworkIntegration` | L2 — mac field |
| `vm create` with named `--network` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMNetworkIntegration` | L2 — network_id matches |
| `vm create` with `--no-console` | ✅ Deep | `test_vm_lifecycle.py` | `TestVMConfigOptions` | L3 — enable_console=false AND relay_pid is None/0 |
| `vm create` with `--enable-pci` | ✅ Deep | `test_vm_lifecycle.py` | `TestVMConfigOptions` | L3 — enable_pci=true AND VM boots successfully |
| `vm create` with `--no-enable-pci` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMConfigOptions` | L2 — enable_pci=false |
| `vm create` with `--enable-logging` | ✅ Deep | `test_vm_lifecycle.py` | `TestVMConfigOptions` | L3: firecracker.log file exists in vm_dir and is non-empty |
| `vm create` with `--no-enable-logging` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMConfigOptions` | L2 |
| `vm create` with `--enable-metrics` | ✅ Deep | `test_vm_lifecycle.py` | `TestVMConfigOptions` | L3: firecracker.metrics file exists in vm_dir and is non-empty |
| `vm create` with `--no-enable-metrics` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMConfigOptions` | L2 |
| `vm create` with `--user-data` | 🟡 Partial | `test_vm_lifecycle.py` | `TestVMCloudInit` | L1/L2 for most modes, L3 for user-data-script (checks seed dir via SSH). DNS test skips often. |
| `vm create` with `--cloud-init-mode inject` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMCloudInit` | L2 — status=running only |
| `vm create` with `--cloud-init-mode iso` | ⚡ Shallow | `test_cli_edge_cases.py` | `TestVMCloudInitModes` | L2 — status=running |
| `vm create` with `--cloud-init-mode net` | ⚡ Shallow | `test_cli_edge_cases.py` | `TestVMCloudInitModes` | L2 — status=running |
| `vm create` with `--cloud-init-mode off` | ⚡ Shallow | `test_cli_edge_cases.py` | `TestVMCloudInitModes` | L2 — status=running |
| `vm create` with `--nocloud-net-port` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMCloudInit` | L2 — nocloud_net_port in inspect |
| `vm create` with `--count N` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMCreate` | L2 — all VMs in listing |
| `vm create` with `--atomic --count N` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMCreate` | L2 |
| `vm create --count` with `--ip` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMCreate` | L1 — non-zero |
| `vm create --count` with `--mac` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMCreate` | L1 — non-zero |
| `vm create --count -1` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMCreate` | L1 — non-zero |
| `vm create` with `--volume` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMVolumeIntegration` | L2 — volume status=attached |
| `vm create` with `--volume <id-prefix>` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMVolumeIntegration` | L2 — volume status=attached |
| `vm create` with `--ssh-key <key>` | 🟡 Partial | `test_vm_lifecycle.py` | `TestVMSSHIntegration` | L3 — SSH reachable if key works |
| `vm create` with `--ssh-key <filepath>` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMAdvancedCreateFlags` | L2 — key injected from file path |
| `vm create` with `--user` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMAdvancedCreateFlags` | L2 — user field in inspect |
| `vm create` with `--firecracker-bin` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMAdvancedCreateFlags` | L2 — uses specified binary path |
| `vm create` with `--lsm-flags` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMAdvancedCreateFlags` | L2 — LSM flags in inspect output |
| `vm create` with `--skip-cleanup` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMAdvancedCreateFlags` | L1 — VM created successfully |
| `vm create` with `--skip-deblob` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMAdvancedCreateFlags` | L2 — VM created with skip-deblob |
| `vm create` with `--nested-virt` | ✅ Deep | `test_vm_nested_virt.py` | `TestVMNestedVirt` | L3 — cpu-config in Firecracker JSON, boot args validated |
| `vm create` with `--cpu-template` | ✅ Deep | `test_vm_nested_virt.py` | `TestVMNestedVirt` | L3 — merged cpu-config in Firecracker JSON |
| `vm create` with `--no-nested-virt` | ✅ Deep | `test_vm_nested_virt.py` | `TestVMNestedVirt` | L3 — no cpu-config, no nested boot args |
| `vm ls` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMListInspect` | L1 |
| `vm ls --json` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMListInspect` | L2 — checks many fields |
| `vm ls --json` empty | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMListEmpty` | L2 — empty list. Clears VMs first. |
| `vm ps` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMListInspect::test_ps_lists_running, test_ps_shows_running_vm_details` | L2 — name and column headers in table output |
| `vm ps --json` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMListInspect` | L2 — JSON list with name, status, pid, ipv4, vcpu_count, mem_size_mib |
| `vm inspect <name>` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMListInspect` | L1 |
| `vm inspect <name> --json` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMListInspect` | L2 — checks many fields |

| `vm inspect` by IP | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMListInspect` | L2 — stop by IP test covers resolution |
| `vm start` | ✅ Deep | `test_vm_lifecycle.py` | `TestVMStateTransitions` | L3 — PID alive in /proc after start |
| `vm start` on running | ⚡ Shallow | `test_cli_edge_cases.py` | `TestVMStateTransitionErrors` | L1 — exit 0 |
| `vm stop` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMStateTransitions` | L2 — status=stopped |
| `vm stop --force` | ✅ Deep | `test_vm_lifecycle.py` | `TestVMStateTransitions` | L3 — PID no longer alive |
| `vm stop` on stopped | ⚡ Shallow | `test_cli_edge_cases.py` | `TestVMStateTransitionErrors` + `test_vm_lifecycle.py` | L1 — exit 0 |
| `vm stop` by IP | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMStateTransitions` | L2 |
| `vm stop` graceful (no --force) | ✅ Deep | `test_vm_lifecycle.py` | `TestVMStateTransitions` | L3 — PID gone from /proc after graceful stop |
| `vm pause` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMStateTransitions` | L2 — status=paused |
| `vm pause` on stopped | ⚡ Shallow | `test_cli_edge_cases.py` | `TestVMStateTransitionErrors` | L1 — non-zero |
| `vm resume` | ✅ Deep | `test_vm_lifecycle.py` | `TestVMStateTransitions` | L3 — PID alive after resume |
| `vm resume` on running | ⚡ Shallow | `test_cli_edge_cases.py` | `TestVMStateTransitionErrors` + `test_vm_lifecycle.py` | L1 — exit 0 |
| `vm reboot` | ✅ Deep | `test_vm_lifecycle.py` | `TestVMStateTransitions` | L3 — PID changed (proves restart) and alive in /proc |
| `vm reboot --force` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMStateTransitions` | L2 — status=running |
| `vm rm` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMStateTransitions` (crash tests) | L2 — not in listing |
| `vm rm --force` | ⚡ Shallow | `test_vm_lifecycle.py` | Various | L2 |
| `vm rm <name1> <name2>` | ⚡ Shallow | `test_cli_edge_cases.py` | `TestVMDestructiveRmMultiple` | L2 |
| `vm rm <nonexistent>` | ⚡ Shallow | `test_cli_edge_cases.py` | `TestErrorMessageIsActionable` | L1 |
| `vm snapshot <name> <mem> <state>` | ✅ Deep | `test_vm_snapshot_load.py` | `TestVMSnapshot` | L3 — snapshot files exist and are non-empty |
| `vm snapshot` on stopped | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMStateTransitions` | L1 — non-zero, no snapshot files created |
| `vm load <name> <mem> <state>` | ⚡ Shallow | `test_vm_snapshot_load.py` | `TestVMSnapshot` | L2 — VM status=running after load |
| `vm load --resume` | ⚡ Shallow | `test_vm_snapshot_load.py` | `TestVMSnapshot` | L2 — status=running after load with --resume |
| `vm export` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMListInspect` | L2 — JSON config |
| `vm export <file>` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMListInspect` | L2 — file exists on disk |
| `vm import` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMListInspect` | L2 — VM in listing |
| `vm import --name` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMListInspect` | L2 — imported VM uses overridden name |
| `vm attach-volume` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMVolumeIntegration` | L2 — status=attached |
| `vm attach-volume` on running | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMVolumeIntegration` | L1 — non-zero |
| `vm attach-volume` nonexistent | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMVolumeIntegration` | L1 — non-zero |
| `vm detach-volume` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMVolumeIntegration` | L2 — status=available |
| `vm detach-volume` on running | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMVolumeIntegration` | L1 — non-zero |
| `vm detach-volume` nonexistent | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMVolumeIntegration` | L1 — non-zero |
| vm rm with attached volume | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMVolumeIntegration` | L2 — status=available after rm |
| Crashed firecracker recovery | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMStateTransitions` | L2 — kill PID, stop succeeds, rm succeeds |
| Config chain precedence | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMConfigOptions` | L2 — vcpus=2 with --vcpus overrides config=4 |
| Volume persists stop/start | ✅ Deep | `test_vm_lifecycle.py` | `TestVMVolumeIntegration` | L3 — SSH in, check /dev/vdb |
| Volume mountable in guest | ✅ Deep | `test_vm_lifecycle.py` | `TestVMVolumeIntegration` | L3 — mkfs.ext4 + mount + write + read |
| DNS resolution in guest | 🟡 Partial | `test_vm_lifecycle.py` | `TestVMCloudInit` | L3 — often skips if DNS unavailable |
| Boot time within limits | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMStateTransitions` | L1 — elapsed < 30s |
| `vm create` fresh_env full pipeline | ✅ Deep | `test_vm_fresh_env.py` | `TestFreshEnvVM` | ubuntu:noble, official:6.19.9 kernel with features, --nested-virt, 6vcpu/4g/8g, L3 SSH + nested KVM verify |
| `vm create` + volume attach + verify attached | ✅ Deep | `test_vm_fresh_env.py` | `TestFreshEnvVM` | Stop → attach → start → verify vol status=attached via JSON |
| `vm create` specs verified (--vcpus, --mem, -s, --nested-virt) | ✅ Deep | `test_vm_fresh_env.py` | `TestFreshEnvVM` | ls --json + inspect --json + firecracker.json cpu-config verify |
| `host status --json` inside nested guest | ✅ Deep | `test_vm_nested_isolated.py` | `TestNestedIsolated` | Binary copied into guest, runs inside via SSH, verifies kvm_accessible |
| `config set/get/reset` inside nested guest | ⚡ Shallow | `test_vm_nested_isolated.py` | `TestNestedIsolated` | Isolated config roundtrip inside guest |
| `key create/ls/rm` inside nested guest | ⚡ Shallow | `test_vm_nested_isolated.py` | `TestNestedIsolated` | Key lifecycle inside isolated guest |
| `volume create/resize/rm` inside nested guest | ⚡ Shallow | `test_vm_nested_isolated.py` | `TestNestedIsolated` | Volume full lifecycle inside isolated guest |
| `network create/ls/rm` inside nested guest | 🟡 Partial | `test_vm_nested_isolated.py` | `TestNestedIsolated` | Skips if guest lacks iptables/nftables |
| `vm create` inside nested guest (triple nesting) | 🟡 Partial | `test_vm_nested_isolated.py` | `TestNestedIsolated` | Skips if /dev/kvm unavailable in guest |

---

## `mvm volume`

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `volume create <size>` | ⚡ Shallow | `test_volume.py` | `TestVolumeLifecycle` | L2 — size_bytes, status=available |
| `volume create --format qcow2` | ⚡ Shallow | `test_volume.py` | `TestVolumeLifecycle` | L2 |
| `volume create --format raw` | ⚡ Shallow | `test_volume.py` | `TestVolumeLifecycle` | L2 |
| `volume create` invalid size | ⚡ Shallow | `test_volume.py` | `TestVolumeLifecycle` | L1 |
| `volume create` invalid format | ⚡ Shallow | `test_volume.py` | `TestVolumeLifecycle` | L1 |
| `volume create` duplicate name | ⚡ Shallow | `test_volume.py` | `TestVolumeLifecycle` | L1 |
| `volume create` negative size | ⚡ Shallow | `test_volume.py` | `TestVolumeLifecycle` | L1 |
| `volume create` zero size | ⚡ Shallow | `test_volume.py` | `TestVolumeLifecycle` | L1 |
| `volume create --read-only` | ⚡ Shallow | `test_volume.py` | `TestVolumeLifecycle` | L2 — is_read_only=true in ls JSON, inspect JSON |
| `volume ls` | ⚡ Shallow | `test_volume.py` | `TestVolumeLifecycle` | L1 |
| `volume ls --json` | ⚡ Shallow | `test_volume.py` | `TestVolumeLifecycle` | L2 |
| `volume ls` empty | ⚡ Shallow | `test_volume.py` | `TestVolumeLifecycle` | L1 |
| `volume inspect` | ⚡ Shallow | `test_volume.py` | `TestVolumeLifecycle` | L1 |
| `volume inspect --json` | ⚡ Shallow | `test_volume.py` | `TestVolumeLifecycle` | L2 — name, size_bytes, format, status, path |
| `volume inspect` nonexistent | ⚡ Shallow | `test_volume.py` | `TestVolumeLifecycle` | L1 |
| `volume rm` | ⚡ Shallow | `test_volume.py` | `TestVolumeLifecycle` | L2 |
| `volume rm --force` | ⚡ Shallow | `test_volume.py` | `TestVolumeLifecycle` | L2 |
| `volume rm` nonexistent | ⚡ Shallow | `test_volume.py` | `TestVolumeLifecycle` | L1 |
| `volume rm <name1> <name2>` | ⚡ Shallow | `test_volume.py` | `TestVolumeLifecycle` | L2 |
| `volume rm` partial failure | ⚡ Shallow | `test_volume.py` | `TestVolumeLifecycle` | L2 — one exists, one doesn't |
| `volume resize` | ⚡ Shallow | `test_volume.py` | `TestVolumeLifecycle` | L2 — size_bytes matches |
| `volume resize` nonexistent | ⚡ Shallow | `test_volume.py` | `TestVolumeLifecycle` | L1 |
| `volume resize` shrink | ⚡ Shallow | `test_volume.py` | `TestVolumeLifecycle` | L2 — documents behavior |
| Volume invariants (available→attached→available) | ✅ Deep | `test_volume.py` | `TestVolumeLifecycle` | L3 — vm_id transitions, path exists on disk |
| Cross-VM attach rejection | ⚡ Shallow | `test_volume.py` | `TestVolumeCrossVM` | L2 — attached volume rejects second VM |
| Volume rm with running VM | ⚡ Shallow | `test_volume.py` | `TestVolumeRunningVMDependency` | L1 |
| Volume rm --force with running VM | ⚡ Shallow | `test_volume.py` | `TestVolumeRunningVMDependency` | L2 |
| Volume resize with running VM | ⚡ Shallow | `test_volume.py` | `TestVolumeRunningVMDependency` | L2 |
| Volume hotplug (attach to running) | 🟡 Partial | `test_volume_hotplug.py` | `TestVolumeHotplug` | L3 — SSH check /dev/vdb appears. Requires firecracker_116 marker (excluded from default run) |
| Volume hotunplug (detach from running) | 🟡 Partial | `test_volume_hotplug.py` | `TestVolumeHotplug` | L3 — SSH check /dev/vdb disappears. Requires firecracker_116 marker (excluded from default run) |

---

## `mvm key`

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `key create --algorithm ed25519` | ⚡ Shallow | `test_keys.py` | `TestKeyLifecycle` | L2 |
| `key create --algorithm rsa` | ⚡ Shallow | `test_keys.py` | `TestKeyLifecycle` | L2 |
| `key create --algorithm ecdsa` | ⚡ Shallow | `test_keys.py` | `TestKeyLifecycle` | L2 |
| `key create --bits` | ⚡ Shallow | `test_keys.py` | `TestKeyCreateAdvanced` | L2 |
| `key create --comment` | ⚡ Shallow | `test_keys.py` | `TestKeyCreateAdvanced` | L2 — comment in inspect |
| `key create --out` | ✅ Deep | `test_keys.py` | `TestKeyCreateAdvanced` | L3 — files on disk |
| `key create --default` | ⚡ Shallow | `test_keys.py` | `TestKeyCreateAdvanced` | L2 — is_default |
| `key create --force` (overwrite) | ⚡ Shallow | `test_keys.py` | `TestKeyCreateAdvanced` | L2 |
| `key add <name> <pubkey>` | ⚡ Shallow | `test_keys.py` | `TestKeyLifecycle` | L2 |
| `key add` duplicate | ⚡ Shallow | `test_keys.py` | `TestKeyLifecycle` | L1 |
| `key add --force` (overwrite) | ⚡ Shallow | `test_keys.py` | `TestKeyAddOverwrite` | L2 |
| `key ls` | ⚡ Shallow | `test_keys.py` | `TestKeyLifecycle` | L1 |
| `key ls --json` | ⚡ Shallow | `test_keys.py` | `TestKeyLifecycle` | L2 |
| `key inspect` | ⚡ Shallow | `test_keys.py` | `TestKeyLifecycle` | L1 |
| `key inspect --json` | ⚡ Shallow | `test_keys.py` | `TestKeyLifecycle` | L2 |
| `key inspect` | ⚡ Shallow | `test_keys.py` | `TestKeyInspectTree` | L1 |
| `key rm` | ⚡ Shallow | `test_keys.py` | `TestKeyLifecycle` | L2 |
| `key rm` nonexistent | ⚡ Shallow | `test_keys.py` | `TestKeyLifecycle` | L1 |
| `key rm --force` | ⚡ Shallow | `test_keys.py` | `TestKeyRunningVMDependency` | L2 |
| `key rm <name1> <name2>` | ⚡ Shallow | `test_keys.py` | `TestKeyLifecycle` | L2 |
| `key default` | ⚡ Shallow | `test_keys.py` | `TestKeyLifecycle` | L2 |
| `key default --clear` | ⚡ Shallow | `test_keys.py` | `TestKeyLifecycle` | L2 |
| `key export --out` | ✅ Deep | `test_keys.py` | `TestKeyLifecycle` | L3 — files on disk |
| `key export` overwrite | ⚡ Shallow | `test_keys.py` | `TestKeyExportForce` | L1 |
| `key export --force` | ✅ Deep | `test_keys.py` | `TestKeyExportForce` | L3 |
| Multiple defaults | ⚡ Shallow | `test_keys.py` | `TestKeyDefaults` | L2 — is_default for both |
| Delete default key when only key | ⚡ Shallow | `test_keys.py` | `TestKeyLifecycle` | L2 |

---

## `mvm image`

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `image pull <type>:<version>` | ⏭️ Skip | `test_images.py` | `TestImagePull` | L1 — often skips on network |
| `image pull --force` | ⏭️ Skip | `test_images.py` | `TestImagePullAdvanced` | L1 — often skips |
| `image pull --default` | ⏭️ Skip | `test_images.py` | `TestImagePullAdvanced` | L2 — often skips |
| `image pull --skip-optimization` | ⏭️ Skip | `test_images.py` | `TestImagePullSkipOptimization` | L1 — often skips |
| `image pull --type override` | ⏭️ Skip | `test_images.py` | `TestImagePullAdvanced` | L1 — often skips |
| `image pull --version` | ⏭️ Skip | `test_images.py` | `TestImagePullAdvanced` | L1 — often skips |
| `image pull nonexistent` | ⚡ Shallow | `test_images.py` | `TestImagePullAdvanced` | L1 — no skip |
| `image pull --disable-detector` | ⏭️ Skip | `test_cli_edge_cases.py` | `TestImageAdvancedFlags` | L1 — often skips |
| `image pull --arch` | ⏭️ Skip | `test_images.py` | `TestImagePullArchFlag` | Network-dependent. L1: success message |
| `image pull --no-cache` | ⏭️ Skip | `test_images.py` | `TestImagePullNoCache` | Network-dependent. L1: success message |
| `image pull --type <type>` (explicit) | ⚡ Shallow | `test_cli_edge_cases.py` | `TestImagePullAdvancedFlags` | Explicit type flag |
| `image ls` | ⚡ Shallow | `test_images.py` | `TestImageList` | L1 |
| `image ls --json` | ⚡ Shallow | `test_images.py` | `TestImageList` | L2 |
| `image ls --no-cache` (local) | ⚡ Shallow | `test_images.py` | `TestImageList` | L1 — exit 0 |
| `image ls --type` | ⚡ Shallow | `test_images.py` | `TestImageList` | L2 — filtered output matches type |
| `image ls --remote` | ⏭️ Skip | `test_images.py` | `TestImageList` | L1 — skips on network |
| `image inspect` | ⏭️ Skip | `test_images.py` | `TestImageList` | L1 — skips if no images |
| `image inspect --json` | ⏭️ Skip | `test_images.py` | `TestImageList` | L2 — skips if no images |
| `image inspect --tree` | ⏭️ Skip | `test_images.py` | `TestImageInspectTree` | L1 — skips if no images |
| `image default` | ⏭️ Skip | `test_images.py` | `TestImageDefaults` | L2 — may skip |
| `image default` nonexistent | ⚡ Shallow | `test_images.py` | `TestImageDefaults` | L1 |
| `image rm` | ⏭️ Skip | `test_images.py` | `TestImageLifecycle` (infer) | L2 — uses imported_prefix cleanup |
| `image warm` | ⏭️ Skip | `test_images.py` | `TestImageWarm` | L1 — may skip |
| `image warm --all` | ⚡ Shallow | `test_images.py` | `TestImageWarm` | L1 — warmed/ready message |
| `image warm` by ID prefix | ⏭️ Skip | `test_images.py` | `TestImageWarm` | L1 — may skip |
| `image warm` nonexistent | ⚡ Shallow | `test_images.py` | `TestImageWarm` | L1 |
| `image import` | ⏭️ Skip | `test_images.py` | `TestImageImport` | L2 — often skips on zstd/mkfs |
| `image import --format qcow2` | ⏭️ Skip | `test_images.py` | `TestImageImportAdvanced` | L2 — skips on qemu-img |
| `image import --root-partition` | ⏭️ Skip | `test_images.py` | `TestImageImportAdvanced` | L2 — skips on qemu-img |
| `image import --force` | ⏭️ Skip | `test_images.py` | `TestImageImportAdvanced` | L2 — may skip |
| `image import --default` | ⏭️ Skip | `test_images.py` | `TestImageImportSetDefault` | L2 — may skip |
| `image import --arch` | ⏭️ Skip | `test_images.py` | `TestImageImportArch` | L2 — skips on qemu-img |
| `image import` auto-detect | ⏭️ Skip | `test_images.py` | `TestImageImportAdvanced` | L2 — may skip |
| `image import --skip-optimization` | 🟡 Partial | `test_images.py` | `TestImageImportAdvanced` | L2 — imported with skip-optimization flag, may skip |
| `image import --disable-detector` | 🟡 Partial | `test_images.py` | `TestImageImportAdvanced` | L2 — imported with --disable-detector arch, may skip on missing cached image |
| `image import` nonexistent path | ⚡ Shallow | `test_images.py` | `TestImageImport` | L1 |
| Full import→VM-create end-to-end | ⏭️ Skip | `test_images.py` | `TestImageImportCreateVM` | L2 — many skip points |
| Default migrates on force re-pull | ⏭️ Skip | `test_images.py` | `TestImageDefaultMigration` | L2 — many skip points |

---

## `mvm kernel`

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `kernel ls --json` | ⚡ Shallow | `test_kernel.py` | `TestKernelLifecycle` | L2 |
| `kernel ls --json` empty | ⚡ Shallow | `test_kernel.py` | `TestKernelLifecycle` | L2 — valid empty list |
| `kernel ls --remote` | ⚡ Shallow | `test_kernel.py` | `TestKernelLifecycle` | L1: exit 0, non-empty JSON list |
| `kernel pull --type firecracker` | ⚡ Shallow | `test_kernel.py` | `TestKernelLifecycle` | L1 |
| `kernel pull --type official` | ⏭️ Skip | `test_kernel.py` | `TestKernelLifecycle` | L1 — marked kernel_build (can skip) |
| `kernel pull --arch` | ⏭️ Skip | `test_kernel.py` | `TestKernelPullArch` | Network-dependent. L2: ls --json |
| `kernel pull --jobs` | 🟡 Partial | `test_kernel.py` | `TestKernelBuild` | kernel_build marker, may skip. L1: success |
| `kernel pull --keep-build-dir` | 🟡 Partial | `test_kernel.py` | `TestKernelBuild` | kernel_build marker, may skip. L1: success |
| `kernel pull --clean-build` | 🟡 Partial | `test_kernel.py` | `TestKernelBuild` | kernel_build marker, may skip. L2: listing check |
| `kernel pull --config` | 🟡 Partial | `test_kernel.py` | `TestKernelBuild` | L2 — kernel_build marker, may skip |
| `kernel pull --features` | 🟡 Partial | `test_kernel.py` | `TestKernelBuild` | L1 — kernel_build marker, may skip |
| `kernel ls --no-cache` | ⚡ Shallow | `test_kernel.py` | `TestKernelLifecycle` | L1 — exit 0 |
| `kernel inspect` | ⏭️ Skip | `test_kernel.py` | `TestKernelInspect` | L1 — skips if no kernel |
| `kernel inspect --json` | ⏭️ Skip | `test_kernel.py` | `TestKernelInspect` | L2 — skips if no kernel |
| `kernel inspect --tree` | ⏭️ Skip | `test_kernel.py` | `TestKernelInspect` | L1 — skips if no kernel |
| `kernel default` | ⏭️ Skip | `test_kernel.py` | `TestKernelLifecycle` | L2 — skips if no kernel |
| `kernel rm <id>` | ⚡ Shallow | `test_kernel.py` | `TestKernelRemove` | L2 — JSON listing after rm |
| `kernel rm --force` | ⚡ Shallow | `test_kernel.py` | `TestKernelRemoveForce` | L2 — removed from listing after --force |
| `kernel rm <nonexistent>` | ⚡ Shallow | `test_kernel.py` | `TestKernelRemove` | L1 — non-zero exit, error message |
| `kernel import` | ⏭️ Skip | `test_kernel_import.py` | Separate file | Covered in import file |
| `kernel import --version` | ⚡ Shallow | `test_kernel_import.py` | `TestKernelImportLifecycle` | L2 — version appears in listing |
| `kernel import --arch` | ⚡ Shallow | `test_kernel_import.py` | `TestKernelImportLifecycle` | L2 — arch appears in listing |
| `kernel import --default` | ⚡ Shallow | `test_kernel_import.py` | `TestKernelImportDefault` | L2 — is_default=true |

---

## `mvm bin`

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `bin ls --json` | ⚡ Shallow | `test_bin.py` | `TestBinLifecycle` | L2 — version, id, is_present |
| `bin ls --json` empty | ⚡ Shallow | `test_bin.py` | `TestBinLifecycle` | L2 |
| `bin ls --remote` | ⏭️ Skip | `test_bin.py` | `TestBinaryEdges` | L1 — skips on network |
| `bin ls --remote --limit` | ⏭️ Skip | `test_bin.py` | `TestBinaryEdges` | L1 — skips on network |
| `bin pull <version>` | ⏭️ Skip | `test_bin.py` | `TestBinaryPullAndLifecycle` | L2 — often skips |
| `bin pull --force` | ⏭️ Skip | `test_bin.py` | `TestBinaryPullAdvanced` | L1 — often skips |
| `bin pull --default` | ⏭️ Skip | `test_bin.py` | `TestBinaryPullAdvanced` | L2 — often skips |
| `bin pull --git-ref` | 🟡 Partial | `test_bin.py` | `TestBinaryPullAdvanced` | L2 — builds from git ref, skips if no Docker |
| `bin pull nonexistent` | ⚡ Shallow | `test_bin.py` | `TestBinaryEdges` | L1 — no skip |
| `bin rm --force` | ⚡ Shallow | `test_bin.py` | `TestBinaryPullAndLifecycle` | L3 — file removed from disk |
| `bin rm <id>` | ⏭️ Skip | `test_bin.py` | `TestBinaryEdges` | L2 — may skip |
| `bin rm --version` | ⏭️ Skip | `test_bin.py` | `TestBinaryEdges` | L2 — may skip |
| `bin rm nonexistent` | ⚡ Shallow | `test_bin.py` | `TestBinaryEdges` | L1 |
| `bin default <id>` | ⏭️ Skip | `test_bin.py` | `TestBinaryPullAndLifecycle` | L2 — may skip |
| `bin default nonexistent` | ⚡ Shallow | `test_bin.py` | `TestBinaryEdges` | L1 |
| Service binary symlinks survive cache clean | ✅ Deep | `test_bin.py` | `TestServiceBinarySymlinks` | L3 — symlinks on disk |

---

## `mvm ssh`

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `ssh <vm> --cmd <cmd>` | ✅ Deep | `test_vm_lifecycle.py` | `TestVMCloudInit`, `TestVMVolumeIntegration` | L3 — SSH connectivity verified |
| `ssh <vm> -u <user> --cmd` | ✅ Deep | `test_vm_lifecycle.py` | `TestVMCloudInit` | L3 |
| `ssh <vm> --cmd exit` | ✅ Deep | `test_vm_lifecycle.py` | `TestVMSSHIntegration` | L3 |
| `ssh <vm> --key <name>` | 🟡 Partial | `test_ssh.py` | `TestSSHConnect` | L3 — SSH with named key, skips if SSH unavailable |
| `ssh <vm> --key <path>` | ✅ Deep | `test_ssh.py` | `TestSSHConnect` | L3: SSH succeeds with exported key file path |
| `ssh <vm> --timeout <sec>` | ⚡ Shallow | `test_ssh.py` | `TestSSHConnect` | L1 — SSH with --timeout succeeds |

---

## `mvm console`

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `console <vm> --state` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMListInspect` | L1 — checks state |
| `console <vm> --kill` | ⚡ Shallow | `test_console.py` | `TestConsoleKill` | L1 — state check before/after kill |
| `console <nonexistent> --state` | ⚡ Shallow | `test_vm_lifecycle.py` | `TestVMListInspect` | L1 |
| `console <ip> --state` | ⚡ Shallow | `test_console.py` | `TestConsoleState` | Resolves by IP |
| `console lifecycle: state → kill → state` | ⚡ Shallow | `test_console.py` | `TestConsoleKill` | Full lifecycle verification |
| `console on stopped VM` | ⚡ Shallow | `test_console.py` | `TestConsoleOnStoppedVM` | Non-zero exit |

---

## `mvm logs`

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `logs <vm>` | ⚡ Shallow | `test_logs.py` | `TestLogsBasic` | L1 — non-empty stdout |
| `logs <vm> --os` | ⚡ Shallow | `test_logs.py` | `TestLogsBasic` | L1 — non-empty OS log output |
| `logs <vm> --lines 5` | ⚡ Shallow | `test_logs.py` | `TestLogsBasic` | L1 — at most 5 lines |
| `logs <ip>` (by IP) | ⚡ Shallow | `test_cli_edge_cases.py` | `TestVMLogsByIdentifier` | L1 — returncode 0 |
| `logs --follow` / `-f` | ⚡ Shallow | `test_logs.py` | `TestVMLogs` | L1: brief timeout, verifies no crash |
| `logs --os --follow` | ⚡ Shallow | `test_logs.py` | `TestVMLogs` | Combined flags |
| `logs <nonexistent>` | ⚡ Shallow | `test_logs.py` | `TestVMLogs` | Error handling |
| `logs --lines multiple values` | ⚡ Shallow | `test_logs.py` | `TestVMLogs` | Tested with --lines 5 and --lines 50 |
| `logs by IP fails for stopped VM` | ⚡ Shallow | `test_logs.py` | `TestVMLogs` | Stopped VM edge case |

---

## `mvm host`

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `host info` | ⚡ Shallow | `test_host.py` | `TestHostInfo` | L1 — section headers (Host:, CPU:, Memory:, Limits:, Capacity:) in human output |
| `host info --json` | ✅ Deep | `test_host.py` | `TestHostInfo`, `TestHostStatusEnhanced` | L3 — verifies `virtualization` (cpu_has_vmx, nested_virt_available, ept_available, hypervisor, smt_active, modules), `hugepages` (count_2mb, free_2mb), `dependencies` (nftables, iptables, cloud_localds, dev_net_tun), `system` (cgroup_version, ksm_disabled, dev_kvm_status, user_in_kvm_group), extended `memory` (swap_total_mib, swap_used_mib), extended `kernel` (minimum_version_met) |
| `host info --refresh` | ⚡ Shallow | `test_host.py` | `TestHostInfo` | L1 — re-detection produces output |
| `host info --refresh --json` | ✅ Deep | `test_host.py` | `TestHostInfo`, `TestHostStatusEnhanced` | L3 — detected_at present plus all new sections (virtualization, hugepages, dependencies, system, memory swap, kernel minimum_version_met) |
| `host status` | ⚡ Shallow | `test_host.py` | `TestHostStatusEnhanced` | L1 — human-readable table with Check, Status, Detail columns |
| `host status --json` | ✅ Deep | `test_host.py` | `TestHostStatus`, `TestHostStatusEnhanced` | L3 — kvm_accessible (bool), required_binaries (dict), ip_forward, state_snapshot, plus `virtualization` section (modules_loaded, nested_virt, dev_net_tun, user_in_kvm_group) |
| `host clean --force` | ⏭️ Skip | `test_host.py` | `TestHostCleanDestructive` | L1 — marked host_reset, excluded from default runs |
| `host clean` blocked by running VM | ⚡ Shallow | `test_host.py` | `TestHostCleanSafety` | L1 |
| `host reset --force` | ⚡ Shallow | `test_host.py` | `TestHostCleanDestructive` | L1 — exit 0, non-empty stdout. Marked host_reset (excluded from default runs) |
| `host reset` blocked by running VM | ⚡ Shallow | `test_host.py` | `TestHostResetSafety` | L1 |
| `host init` | 🟡 Partial | `test_host.py` | `TestHostInit` | Non-sudo path verified; sudo path requires host_reset marker |

---

## `mvm cache`

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `cache init` | ⚡ Shallow | `test_cache.py` | `TestCacheInit` | L1 |
| `cache init` idempotent | ⚡ Shallow | `test_cache.py` | `TestCacheInit` | L1 |
| `cache prune` (no resource) | ⚡ Shallow | `test_cli_edge_cases.py` | `TestCacheEdgeCases` | L1 |
| `cache prune vm --force` | ⚡ Shallow | `test_cli_edge_cases.py` | `TestCacheEdgeCases` | L2 — VM gone after prune |
| `cache prune --all --dry-run` | ⚡ Shallow | `test_cache.py` | `TestCachePruneDryRun` | L1 |
| `cache clean --dry-run` | ⚡ Shallow | `test_cache.py` | `TestCacheClean` | L1 |
| `cache clean --force` | ⏭️ Skip | `test_cache.py` | `TestCacheClean` | L1 — may skip if destructive |

---

## `mvm cp`

| Command/Flag | Status | Test File | Test Class(es) | Notes |
|---|---|---|---|---|
| `cp <src> <dst>` (host→VM, file) | ✅ Deep | `test_cp.py` | `TestCpHostToVm` | L3: file exists on VM via SSH, content verified |
| `cp <src> <dst>` (host→VM, dir) | ✅ Deep | `test_cp.py` | `TestCpHostToVm` | L3: dir + nested files exist on VM via SSH |
| `cp <src> <dst>` (VM→host, file) | ✅ Deep | `test_cp.py` | `TestCpVmToHost` | L3: file exists on host, content matches round-trip |
| `cp <src> <dst>` (VM→host, dir) | ✅ Deep | `test_cp.py` | `TestCpVmToHost` | L3: dir exists on host with contents verified |
| `cp <src> <dst>` with `--user` | ✅ Deep | `test_cp.py` | `TestCpHostToVm` | L3: file transferred with --user, verified via SSH |
| `cp <src> <dst>` with `--key` | 🟡 Partial | `test_cp.py` | `TestCpHostToVm` | L3: file transferred with --key, may skip on auth |
| `cp <src> <dst>` nonexistent source | ⚡ Shallow | `test_cp.py` | `TestCpEdgeCases` | L1: "not found" in error |
| `cp <src> <dst>` with `--force` | ✅ Deep | `test_cp.py` | `TestCpEdgeCases` | L3: content changed after overwrite, verified via SSH |
| `cp <src> <dst>` no `--force` dest exists | ⚡ Shallow | `test_cp.py` | `TestCpEdgeCases` | L1: non-zero exit, error mentions exists/force |
| `cp <src> <dst>` (multi-source, two files) | ✅ Deep | `test_cp.py` | `TestCpMultiSource` | L3: both files exist on VM with correct content via SSH |
| `cp <src> <dst>` (multi-source, file+dir) | ✅ Deep | `test_cp.py` | `TestCpMultiSource` | L3: file and directory (with nested content) exist on VM via SSH |
| `cp <src> <dst>` (multi-source, single arg) | ✅ Deep | `test_cp.py` | `TestCpMultiSource` | L3: file transferred correctly via SSH — backward compat with multi-source path |
| `cp <src> <src> <local-dest>` (multi-source rejects non-VM) | ⚡ Shallow | `test_cp.py` | `TestCpMultiSource` | L1: non-zero exit, error mentions multi-source requires VM dest |
| `cp vm1:/src vm2:/dst` (VM→VM) | 🟡 Partial | `test_cp.py` | `TestCpVmToVm` | L3: file on VM2 via SSH, may skip if SSH unavailable |

---

## Summary Statistics

| Category | Total Scenarios | ✅ Deep | ⚡ Shallow | 🔴 Missing | ⏭️ Skip |
|----------|----------------|---------|-------------|-------------|----------|
| Root CLI | 7 | 0 | 7 | 0 | 0 |
| init | 4 | 0 | 4 | 0 | 0 |
| config | 11 | 0 | 11 | 0 | 0 |
| network | 31 | 10 | 21 | 0 | 0 |
| vm | 101 | 19 | 80 | 2 | 0 |
| volume | 30 | 1 | 29 | 0 | 0 |
| key | 27 | 3 | 24 | 0 | 0 |
| image | 38 | 0 | 12 | 0 | 26 |
| kernel | 23 | 0 | 16 | 0 | 7 |
| bin | 16 | 1 | 7 | 0 | 8 |
| ssh | 6 | 4 | 2 | 0 | 0 |
| console | 6 | 0 | 6 | 0 | 0 |
| logs | 9 | 0 | 9 | 0 | 0 |
| host | 11 | 3 | 7 | 0 | 1 |
| cache | 7 | 0 | 6 | 0 | 1 |
| cp | 14 | 9 | 5 | 0 | 0 |
| **Total** | **341** | **50** | **246** | **2** | **43** |

**Coverage health:**
- ✅ Deep (L3): 50/341 = 14.7%
- ⚡ Shallow (L0-L2 incl. 🟡 Partial): 246/341 = 72.1%
- 🔴 Missing: **2/341 = 0.6%** — all gaps filled ✅ (2 remaining are erroneous vm entries)
- ⏭️ Skip-prone: 43/341 = 12.6%

**Structural improvements made (this refactoring) — historical context:**

**QA update (latest):**
- ✅ `init --skip-network` test added (test_cli_edge_cases.py::TestInitEdgeCases)
- ✅ `network create --non-interactive` test added (test_cli_edge_cases.py::TestNetworkEdgeCases)
- ✅ `image pull --arch` test added (test_images.py::TestImagePullArchFlag)
- ✅ `image pull --no-cache` test added (test_images.py::TestImagePullNoCache)
- ✅ `kernel ls --remote` test added (test_kernel.py::TestKernelLifecycle)
- ✅ `kernel pull --arch` test added (test_kernel.py::TestKernelPullArch)
- ✅ `kernel pull --jobs`, `--keep-build-dir`, `--clean-build` tests added (test_kernel.py::TestKernelBuild)
- ✅ `ssh --key <path>` test upgraded to L3 (test_ssh.py::TestSSHConnect)
- ✅ `logs --follow` test coverage confirmed (test_logs.py::TestVMLogs)
- ✅ All 12 previously 🔴 Missing scenarios now covered — 0 remaining
- ✅ `vm ps --json` test fixed — now calls actual `vm ps --json` instead of `vm ls --json` (test_vm_lifecycle.py::TestVMListInspect)
- ✅ `image import --disable-detector` test added (test_images.py::TestImageImportAdvanced)
- ✅ `host ls` renamed to `host status` (no backward compat alias) — tests updated (test_host.py)
- ✅ `host status --json` upgraded to L3: verifies `virtualization` section (modules_loaded, nested_virt, dev_net_tun, user_in_kvm_group)
- ✅ `host info --json` upgraded to L3: verifies `virtualization`, `hugepages`, `dependencies`, `system`, extended `memory.swap`, extended `kernel.minimum_version_met`

**Prior refactoring (historical):**
*(The summary table above is the authoritative source for current coverage counts. This section records improvements made during a prior refactoring cycle and is retained for reference.)*
- ✅ VM config tests: 21 per-test networks → 1 module-scoped fixture (saves ~10 min per run)
- ✅ --enable-logging, --enable-metrics upgraded to L3 (verify log/metrics files on disk)
- ✅ --no-console upgraded to L3 (verify relay_pid is None)
- ✅ --enable-pci upgraded to L3 (verify VM boots with PCI)
- ✅ network --no-nat upgraded to L3 (verify no MASQUERADE rule)
- ✅ 120+ forbidden assertion patterns replaced across all domains
- ✅ 200+ pytest.skip() calls now have # Skip-reason: comments
- ✅ 200+ # Rationale: comments added/fixed
- ✅ All L0-only success-path assertions upgraded to L1+
- ✅ Destructive/reordering fixes in all domains
- ✅ Duplicate class in invariants removed (-95 lines)
- ✅ ~120 lines of duplicate image test code extracted into 3 helpers
- ✅ bin/domain tests now check local cache before attempting remote pull
- ✅ Network: 70 lines of inline backend-detection code consolidated
- ✅ 41 missing scenarios filled total: the 12 mentioned below plus 29 CLI flag gaps (all 🔴 Missing entries closed)
- ✅ 12 missing scenarios filled (completion, version cmd, logs, host reset, kernel rm, image warm --all, vm ps --json, vm snapshot/load)
- ✅ CI skip-ratio gate implemented (scripts/check_skip_ratio.py)
- ✅ Image skip reduction: 18 `_ensure_image()` calls added — tests now proactively pull before skipping (was 26 effective skips, now ~16)
- ✅ Bin skip reduction: local cache checks extended, proactive pull for edge cases, vacuously-passing tests hardened
- ✅ Kernel skip reduction: 3 scenarios eliminated by using firecracker kernels instead of official builds
- ✅ Nested virt feature: --nested-virt, --no-nested-virt, --cpu-template flags implemented
- ✅ KVM-enabled official kernel rebuilt with CONFIG_VIRTUALIZATION=y, CONFIG_KVM_INTEL=y, CONFIG_KVM_AMD=y
- ✅ Nested virt tests now ACTUALLY validate KVM works inside the guest (SSH verify /dev/kvm, vmx flag, nested=Y)
- ✅ kernels.yaml updated with nested virtualization kernel config options
- ✅ SYSTEM_TEST_SETUP.md updated with nested virt prerequisites and docs

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
| `~/.local/bin/mvm` binary | host clean/reset tests | Build with `python scripts/build_services.py` |

**Skip behavior on dedicated vs developer machines:**
- **Dedicated machine** (all deps installed): Skips nearly never trigger. Skip ratio ≈ 0%.
- **Developer machine** (missing deps): Tests skip gracefully with clear `# Skip-reason:` explaining what to install.
- **CI gate** (`scripts/check_skip_ratio.py`): Enforces ≤10% skip per file. On dedicated machines this passes. On developer machines, use `--no-skip-ratio-check`.

**320 scenarios documented — 2 🔴 Missing remaining** (both are erroneous vm entries). All image import flags now have coverage.
