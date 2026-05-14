# Active Improvements

> **STATUS: Current — active tracking document.** All status markers accurately reflect the current codebase.

> ## Status Overview
>
> This document tracks incomplete improvements and planned features.
> For completed items, see [archives/IMPROVEMENTS_001_ARCHIVED.md](archives/IMPROVEMENTS_001_ARCHIVED.md).
>
> **Last verified:** 2026-05-13

## Implementation Reviews

- [ ] The `mvm config set|get` must modify values coming from constants.py file! any constants defined in the constants.py file, their value can be override by using `mvm config set|get <config_key>` where <config_key> is the variable defined in constants.py but in lowercase. These overrides are done in $MVM_CONFIG_DIR/config.json
    - **🔶 PARTIAL** — `ConfigOperation` class exists. Commands exist and modify `OVERRIDABLE_DEFAULTS` (~25 settings). **UPDATED:** Stored in SQLite (via `SettingsService` + `SettingsRepository`), NOT config.json. Only `OVERRIDABLE_DEFAULTS` subset, not ALL constants.

## Core

- [ ] repl-like guestfs filesystem access to rootfs
    - **❌ NOT IMPLEMENTED** — Guestfs exists but batch-provisioning only. No interactive rootfs CLI.
- [x] reconcile VM from stopped state
    - ✅ **IMPLEMENTED** — `VMOperation.start()` handles stopped VMs via respawn flow.
- [ ] add is_present checks for vms
    - **❌ NOT IMPLEMENTED** — All other domains (images, kernels, binaries, networks, keys) have `is_present` field. `vm_instances` table does NOT have one. VM presence is inferred from process status.
- [x] add --timeout for mvm ssh
    - ✅ **IMPLEMENTED** — `--timeout/-t` → `-o ConnectTimeout=N` in SSH args.
- [x] proper exceptions per domain
    - ✅ **IMPLEMENTED** — All domains have dedicated exception classes in `exceptions.py` (25+ exception classes).

## Networking

- [ ] fully isolated bridge networking for VMs
    - **❌ NOT IMPLEMENTED** — Plain bridge+TAP only. No VLANs, namespaces, ebtables, or isolation mode.
- [x] constrain TAP FORWARD rules to bridge subnet
    - ✅ **IMPLEMENTED** — `ensure_tap()` now uses bridge subnet for src/dst, matching `ensure_nat()`.

## Networking (historical iptables output — kept for reference)
```text
Chain MVM-FORWARD (1 references)
 pkts bytes target     prot opt in     out     source               destination
    0     0 ACCEPT     all  --  mvm-default wlo1    172.35.0.0/24        0.0.0.0/0
    0     0 ACCEPT     all  --  wlo1   mvm-default  0.0.0.0/0            172.35.0.0/24
    0     0 ACCEPT     all  --  mvm-default mvm-def-p2-mpw  0.0.0.0/0            0.0.0.0/0
    0     0 ACCEPT     all  --  mvm-def-p2-mpw mvm-default  0.0.0.0/0            0.0.0.0/0
```


## Codebase Maintainability

- [x] replace yaml_id references with internal id
    - ✅ **IMPLEMENTED** — Zero matches found. Clean: YAML slug → SHA256 hash.
- [ ] centralize all tool usage (ip, iptables, etc.) in utility files
    - **❌ (~25%)** — Read-only ip/iptables queries centralized in `NetworkUtils`. Destructive ops + blkid/sfdisk/losetup/mount/visudo still scattered as direct subprocess calls across 6+ files.

## CLI

- [x] add confirmation to mvm cache prune .... including all sub commands.
    - ✅ **IMPLEMENTED** (2026-05-07) — All 6 prune subcommands now prompt `typer.confirm("Continue?", default=True)`. `--force/-f` bypasses. Existing `--all` and `clean` prompts also updated to `default=True`.
- [ ] --debug propagates -v to subprocess tools
    - **❌ NOT IMPLEMENTED** — `--debug` sets Python logging but zero propagation to ssh/ip/iptables subprocess calls.
- [x] rename configure.py to init.py
    - ✅ **IMPLEMENTED** — Already renamed. `cli/init.py` registered as `init` command.

## Security

- [x] change the db to user only and read for group only
    - ✅ **IMPLEMENTED** (2026-05-07) — Added `CONST_FILE_PERMS_DB = 0o640`. `_db.py` now chmods the DB file to 0o640 on every connection and migration.
- [x] change cache folder permission!
    - ✅ **IMPLEMENTED** (2026-05-07) — Added `mode=CONST_DIR_PERMS_CACHE (0o700)` to all 8 `mkdir()` calls in `CacheUtils`.

## Volume Domain (moved from IMPROVEMENTS_003)

- [x] volume domain implementation
    - ✅ **IMPLEMENTED** — Volume domain is fully implemented:
        - `core/volume/` with `_controller.py`, `_repository.py`, `_resolver.py`, `_service.py`
        - `api/volume_operations.py` with `VolumeOperation`
        - `api/inputs/_volume_input.py` and `_volume_create_input.py`
        - `models/volume.py` with `VolumeItem`, `VolumeStatus`
        - `cli/volume.py` registered in `main.py`
        - DB schema in `001_initial_schema.sql` (volumes table)
        - VM integration via `volume_ids` and `extra_drives` wiring

---

## Notes

- Items marked with ~~strikethrough~~ are removed/deferred
- Items marked with [x] should be moved to IMPROVEMENTS_ARCHIVE.md when verified complete
- Keep this file focused on active work only
