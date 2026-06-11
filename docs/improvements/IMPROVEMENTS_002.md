# Active Improvements

> **STATUS: Current — active tracking document.** All status markers reflect the Go codebase.
>
> **Last verified:** 2026-06-10

## Implementation Reviews

- [ ] `mvm config set|get` must modify values from `internal/infra/constants.go`
    - **🔶 PARTIAL** — `ConfigOperation` exists. Commands modify `OverridableDefaults` (49 settings). Stored in SQLite (via `SettingsService` + `SettingsRepository`), NOT config.json. Only `OverridableDefaults` subset, not ALL constants.

## Core

- [ ] repl-like guestfs filesystem access to rootfs
    - **❌ NOT IMPLEMENTED** — Guestfs exists but batch-provisioning only. No interactive rootfs CLI.
- [x] reconcile VM from stopped state
    - ✅ **IMPLEMENTED** — `VMOperation.Start()` handles stopped VMs via respawn flow.
- [ ] add is_present checks for VMs
    - **❌ NOT IMPLEMENTED** — All other domains (images, kernels, binaries, networks, keys) have `is_present` field. `vm_instances` table does NOT have one. VM presence is inferred from process status.
- [x] add --timeout for mvm ssh
    - ✅ **IMPLEMENTED** — `--timeout/-t` → `-o ConnectTimeout=N` in SSH args.
- [x] proper exceptions per domain
    - ✅ **IMPLEMENTED** — All domains use `pkg/errs.DomainError` with domain-specific error codes.

## Networking

- [ ] fully isolated bridge networking for VMs
    - **❌ NOT IMPLEMENTED** — Plain bridge+TAP only. No VLANs, namespaces, ebtables, or isolation mode.
- [x] constrain TAP FORWARD rules to bridge subnet
    - ✅ **IMPLEMENTED** — `EnsureTAP()` uses bridge subnet for src/dst, matching `EnsureNAT()`.

## Codebase Maintainability

- [x] replace yaml_id references with internal id
    - ✅ **IMPLEMENTED** — Zero matches found. Clean: YAML slug → SHA256 hash.
- [ ] centralize all tool usage (ip, iptables, etc.) in utility files
    - **❌ (~25%)** — Read-only ip/iptables queries centralized in `internal/lib/network/`. Destructive ops + blkid/sfdisk/losetup/mount/visudo still scattered as direct subprocess calls across 6+ files.

## CLI

- [x] add confirmation to mvm cache prune including all sub commands
    - ✅ **IMPLEMENTED** — All 6 prune subcommands prompt `common.Cli.PromptConfirm("Continue?", true)`. `--force/-f` bypasses.
- [ ] --debug propagates -v to subprocess tools
    - **❌ NOT IMPLEMENTED** — `--debug` sets Go slog level but zero propagation to ssh/ip/iptables subprocess calls.
- [x] rename configure.py to init
    - ✅ **IMPLEMENTED** — `internal/cli/init.go` registered as `init` command.

## Security

- [x] change the db to user only and read for group only
    - ✅ **IMPLEMENTED** — `DBFilePerm = 0640`. DB file is chmod'd on every connection.
- [x] change cache folder permission
    - ✅ **IMPLEMENTED** — `CacheDirPerm = 0700` on all cache directory creation.

## Volume Domain

- [x] volume domain implementation
    - ✅ **IMPLEMENTED** — Volume domain is fully implemented:
        - `internal/core/volume/` with controller, repository, resolver, service, sqlite, utils
        - `pkg/api/volume.go` with `VolumeOperation`
        - `pkg/api/inputs/volume_input.go`
        - `internal/lib/model/volume.go` with `VolumeItem`, `VolumeStatus`
        - `internal/cli/volume.go` registered in root command
        - DB schema in migrations (volumes table)
        - VM integration via `volume_ids` and `extra_drives` wiring

---

## Notes

- Items marked with [x] are complete and should be moved to archive when verified
- Keep this file focused on active work only
