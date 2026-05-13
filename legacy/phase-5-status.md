> **⚠️ ARCHIVED — Historical document from an earlier phase.**
> The project has evolved significantly. See [CONTEXT.md](../CONTEXT.md) for current domain language,
> [docs/PROJECT_ARCHITECTURE.md](../docs/PROJECT_ARCHITECTURE.md) for the current architecture,
> and [docs/API.md](../docs/API.md) for the current API reference.
> This file is kept for historical reference only.

# Phase 5 Status -- Privilege Model, Clean/Reset, Help Command

**Status:** Complete
**Date:** 2026-03-23

## Requirement Status

| # | Requirement | Status | Notes |
|---|-------------|--------|-------|
| 1 | New constants in `constants.py` | Done | PROJECT_GROUP, SUDOERS_DROP_IN_PATH, DEFAULT_NETWORK_*, BRIDGE_PREFIX, FIRECRACKER_*_TIMEOUT, PRIVILEGED_BINARIES; TAP_PREFIX changed to "mvm-tap" |
| 2 | `PrivilegeError(HostError)` in `exceptions.py` | Done | |
| 3 | Group/sudoers helpers in `core/host.py` | Done | _get_current_user, _group_exists, _user_in_group, _create_group, _add_user_to_group, _validate_sudoers_binaries, _generate_sudoers_content, _write_sudoers, _remove_sudoers, _remove_group |
| 4 | `init_host` updated with group/sudoers steps | Done | Runs after binary check, before IP forwarding |
| 5 | `clean_host` function | Done | Network-only teardown, no sysctl/group/sudoers |
| 6 | `reset_host` function | Done | Full rollback including sysctl, sudoers, group |
| 7 | `check_privileges` in `api/host.py` | Done | Checks binary exists + user is root or in mvm group |
| 8 | CLI `host clean` command | Done | Refuses if VMs running, requires --force or confirmation |
| 9 | CLI `host reset` command | Done | Refuses if VMs running, requires --force or confirmation |
| 10 | Deprecated `host prune` alias (hidden) | Done | Shows deprecation warning |
| 11 | Deprecated `host restore` alias (hidden) | Done | Shows deprecation warning |
| 12 | `host init` logout notice | Done | Prints ACTION REQUIRED message after successful init |
| 13 | Top-level `mvm help` command | Done | Navigates Click command tree for subcommand help |
| 14 | `configure.py` updated for privilege model | Done | Step 1 explains sudo-once model, offers sudo mvm host init |
| 15 | Tests for new constants | Done | test_constants.py updated |
| 16 | Tests for PrivilegeError | Done | test_phase5.py |
| 17 | Tests for check_privileges | Done | test_phase5.py |
| 18 | Tests for clean_host / reset_host | Done | test_phase5.py |
| 19 | Tests for CLI clean/reset commands | Done | test_cli_host.py and test_phase5.py |
| 20 | Tests for deprecated aliases | Done | test_phase5.py |
| 21 | Tests for help command | Done | test_phase5.py |
| 22 | README.md updated | Done | Quickstart, host commands table, prerequisites |
| 23 | CONTRIBUTING.md updated | Done | Privileged operations section |
| 24 | docs/API.md updated | Done | check_privileges, clean_host, reset_host, PrivilegeError |

## Test Results

- 605 passed
- mypy: 0 errors
- ruff: 0 errors
