# Active Improvements

This document tracks incomplete improvements and planned features.
For completed items, see [archives/IMPROVEMENTS_001_ARCHIVED.md](archives/IMPROVEMENTS_001_ARCHIVED.md).

## Project Guidelines

When making these changes, ensure there will be **NO DEPRECATION messages/codes left over**. This project is under active development and IS NOT READY FOR PRODUCTION YET. Any changes that cause regression (such as renaming a command) are fine to proceed so long as all references/tests/docs are updated. You do not have to add code that allows migration from old to new approach.

---

## Implementation Reviews

- [ ] The `mvm config set|get` must modify values coming from constants.py file! any constants defined in the constants.py file, their value can be override by using `mvm config set|get <config_key>` where <config_key> is the variable defined in constants.py but in lowercase. These overrides are done in $MVM_CONFIG_DIR/config.json
    - **🔶 PARTIAL** — Commands exist and modify `OVERRIDABLE_DEFAULTS` (~25 settings). But: stored in SQLite, not config.json; only subset of constants overridable.

## Core

- [ ] repl-like guestfs filesystem access to rootfs
    - **❌** — Guestfs exists but batch-provisioning only. No interactive rootfs CLI.
- [x] reconcile VM from stopped state
    - ✅ **Pre-existing** — `VMOperation.start()` → `_respawn_firecracker()` handles stopped VMs.
- [ ] add is_present checks for vms
    - **❌** — All other domains have it. `vm_instances` doesn't.
- [x] add --timeout for mvm ssh
    - **Done** — `--timeout/-t` → `-o ConnectTimeout=N` in SSH args.
- [x] proper exceptions per domain
    - ✅ **Pre-existing** — All domains have dedicated exception classes in `exceptions.py`.

## Networking

- [ ] fully isolated bridge networking for VMs
    - **❌** — Plain bridge+TAP only. No VLANs, namespaces, ebtables, or isolation mode.
- [x] constrain TAP FORWARD rules to bridge subnet
    - **Done** — `ensure_tap()` now uses bridge subnet for src/dst, matching `ensure_nat()`.

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
    - ✅ **Pre-existing** — Zero matches found. Clean: YAML slug → SHA256 hash.
- [ ] centralize all tool usage (ip, iptables, etc.) in utility files
    - **❌ (~25%)** — Read-only ip/iptables queries centralized in `NetworkUtils`. Destructive ops + blkid/sfdisk/losetup/mount/visudo still scattered as direct subprocess calls across 6+ files.

## CLI

- [x] add confirmation to mvm cache prune .... including all sub commands.
    - **Done 2026-05-07** — All 6 prune subcommands now prompt `typer.confirm("Continue?", default=True)`. `--force/-f` bypasses. Existing `--all` and `clean` prompts also updated to `default=True`.
- [ ] --debug propagates -v to subprocess tools
    - **❌** — `--debug` sets Python logging but zero propagation to ssh/ip/iptables subprocess calls.
- [x] rename configure.py to init.py
    - ✅ **Pre-existing** — Already renamed. `cli/init.py` registered as `init` command.

## Security

- [x] change the db to user only and read for group only
    - **Done 2026-05-07** — Added `CONST_FILE_PERMS_DB = 0o640`. `_db.py` now chmods the DB file to 0o640 on every connection and migration.
- [x] change cache folder permission!
    - **Done 2026-05-07** — Added `mode=CONST_DIR_PERMS_CACHE (0o700)` to all 8 `mkdir()` calls in `CacheUtils` (images, kernels, bin, logs, vms, keys, warm-images, resolve-dir).

---

## Notes

- Items marked with ~~strikethrough~~ are removed/deferred
- Items marked with [x] should be moved to IMPROVEMENTS_ARCHIVE.md when verified complete
- Keep this file focused on active work only

