# Phase 6 Status

## Summary
Status: Complete
Started: 2026-03-24
Completed: 2026-03-24

## Requirements

| # | Requirement | Status | Notes |
|---|---|---|---|
| 1 | Default config changes (assets_dir, bridge CIDR, bridge name) | Complete | DEFAULT_NETWORK_CIDR=172.35.0.0/24, DEFAULT_NETWORK_GATEWAY=172.35.0.1, DEFAULT_BRIDGE_NAME=mvm-bridge, assets_dir defaults to cache dir |
| 2 | Config get/set API + CLI | Complete | `mvm config get/set <key> [value]` via core/user_config.py and cli/config.py |
| 3 | Firecracker binary path in config | Complete | cli state tracks active binary path; `mvm bin use` updates cli-state.json |
| 4 | CI_VERSION as global state | Complete | `mvm bin use` sets ci_version in cli-state.json; kernel fetch uses it |
| 5 | `mvm bin ls --remote` sort by semver desc | Complete | list_remote_versions now sorts by semver descending |
| 6 | Default `mvm bin ls --remote --limit` = 5 | Complete | Changed default from 10 to 5 |
| 7 | Missing subcommand shows help | Complete | kernel_app, image_app, bin_app all use invoke_without_command callback |
| 8 | Asset commands auto-create dirs | Complete | kernel_ls, image_ls create dirs if missing |
| 9 | Kernel download checksum fix | Complete | fetch_kernel_sha256 fetches .sha256 from kernel.org |
| 10 | Kernel builds at /tmp/firecracker-manager/ | Complete | build_dir defaults to /tmp/firecracker-manager/build-{uuid} |
| 11 | --keep-build-dir flag | Complete | build_kernel_pipeline accepts keep_build_dir parameter |
| 12 | Kernel subcommand overhaul | Complete | `mvm kernel fetch --type firecracker|official`; FC kernel from S3; metadata JSON; set-default |
| 13 | Image subcommand overhaul | Complete | image ls with OS/pulled_at/fs_type, --remote flag, codename support, set-default |
| 14 | `mvm key add` private key detection | Complete | key_manager detects private keys and shows friendly error |
| 15 | Network device naming (mvm-<name>) | Complete | _bridge_name_for uses mvm-<name> for all networks |
| 16 | TAP naming (mvm-<net>-<vm>-<rand3>) | Complete | _generate_tap_name in vm_lifecycle.py; tap_device persisted in VMInstance |
| 17 | `mvm vm ps` alias | Complete | ps command added as alias for ls |
| 18 | `mvm vm prune` | Complete | cleanup renamed to prune; cleanup kept as hidden deprecated alias |
| 19 | `mvm vm ssh` default keys from ~/.ssh | Complete | _resolve_ssh_key_for_vm checks mvm cache then ~/.ssh |
| 20 | `--key` accepts folder | Complete | _find_ssh_key_from_path handles both file and directory |
| 21 | `--image` optional when default set | Complete | create resolves default image from cli state |
| 22 | `--kernel` optional when default set | Complete | create resolves default kernel from kernel metadata |
| 23 | `--firecracker-bin` from active binary | Complete | _resolve_active_firecracker_bin uses cli state and binary manager |
| 24 | Unified metadata.json for kernels/images/binaries | Complete | src/mvm/core/metadata.py; cache_dir/metadata.json replaces per-file JSON sidecars; legacy migration on first list |
| 25 | Kernel not default on download | Complete | get_default_kernel_path returns None if no explicit default set; set-default subcommand marks kernel entry with metadata.json `is_default=1` |
| 26 | config.json paths + defaults section | Complete | initialize_default_config writes assets paths; image/kernel/binary defaults are metadata-backed via `metadata.json` `is_default` markers |
| 27 | VM configuration file (--output-config / --import-config) | Complete | VMCreateConfigFile model + api/vm_config.py; mvm vm create --output-config PATH writes config + creates VM; --import-config PATH reads params from file, CLI flags override |

## Decisions

- DEFAULT_KERNEL_VERSION set to "6.1.9" (spec said "6.19.9" which appears to be a typo; 6.1.9 is a real LTS kernel version consistent with the 6.1.x series used elsewhere)
- TAP device names truncated to 15 chars (Linux interface name limit): `mvm-{net[:3]}-{vm[:3]}-{rand3}`
- Kernel build dir uses UUID suffix to ensure uniqueness: `/tmp/firecracker-manager/build-{uuid8}`
- user_config.py uses mvm_CONFIG env var (same as the CLI config override) for test isolation
- CI_VERSION stored as major.minor (e.g., "1.12") derived from full version (e.g., "1.12.0")
