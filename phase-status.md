# Phase Status — Firecracker Manager CLI

**Last updated:** 2026-03-23
**Test results:** 605 passed
**mypy:** 0 errors
**ruff:** 0 errors

## Summary

| Phase | Status | Notes |
|-------|--------|-------|
| Phase 1 — Scaffold & VM lifecycle | ✅ Complete | All commands implemented and tested |
| Phase 2 — Full requirements | ✅ Complete | API layer, CI, docs all present |
| Phase 3 — Network, keys, configure | ✅ Complete | Named networks, key registry, guided wizard |
| Phase 4 — Refinements & distribution | ✅ Complete | All fixes applied this session |
| Phase 5 — Privilege model & cleanup commands | ✅ Complete | Group/sudoers, clean/reset, help command |

## Phase 5 changes

- Added privilege model: `fcm` group, sudoers drop-in, `check_privileges()` API
- New constants: PROJECT_GROUP, SUDOERS_DROP_IN_PATH, DEFAULT_NETWORK_*, BRIDGE_PREFIX, PRIVILEGED_BINARIES
- Changed TAP_PREFIX from "fcm" to "fcm-tap"
- Added `PrivilegeError(HostError)` exception
- Added group/sudoers helper functions to `core/host.py`
- Updated `init_host` with group/sudoers setup steps
- Added `clean_host` (network-only teardown) and `reset_host` (full rollback)
- Renamed CLI: `prune` -> `clean`, `restore` -> `reset` (old names kept as hidden deprecated aliases)
- Added top-level `fcm help` command with subcommand navigation
- Updated `configure.py` step 1 for sudo-once privilege model
- Updated README.md, CONTRIBUTING.md, docs/API.md

## Phase 4 fixes applied previously

- Removed `ssh-keygen -R` call from `vm delete` (Phase 4 §3 explicit prohibition)
- Fixed mypy dict type errors in `core/key_manager.py`
- Fixed `next(iter())` iterator error in `core/network_manager.py`
- Added `[project.optional-dependencies]` and `license` to `pyproject.toml`
- Added `--cov-fail-under=80` to CI workflow
- Implemented `src/fcm/api/` package (all 5 modules)
- Rewrote `docs/API.md` per Phase 4 §6 structure
- Implemented subcommand-level `help` (Phase 4 §5): `key add help`, `network create help`, etc.
- Added Build System section to `CONTRIBUTING.md` (Phase 4 §7)

## Detailed per-phase status

See `firecracker-manager/phase-N-status.md` files for requirement-by-requirement breakdown.
