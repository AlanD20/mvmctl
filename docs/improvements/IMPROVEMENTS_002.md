# Active Improvements

> **STATUS: Current — active tracking document.** All status markers reflect the Go codebase.
>
> **Last verified:** 2026-06-27

## Implementation Reviews

- [ ] `mvm config set|get` must modify values from `internal/infra/constants.go`
    - **🔶 PARTIAL** — Settings implemented via `config.Service` + `SettingsRepository` (SQLite). Commands modify overridable settings derived from `infra.OverridableDefaults`, NOT config.json. Only the overridable subset is modifiable, not ALL constants.

## Core

- [ ] repl-like guestfs filesystem access to rootfs
    - **❌ NOT IMPLEMENTED** — Guestfs exists but batch-provisioning only. No interactive rootfs CLI.
- [x] reconcile VM from stopped state
    - ✅ **IMPLEMENTED** — `Operation.VMStart()` handles stopped VMs via respawn flow.
- [ ] add is_present checks for VMs
    - **❌ NOT IMPLEMENTED** — All other domains (images, kernels, binaries, networks, keys) have `is_present` field. `vm_instances` table does NOT have one. VM presence is inferred from process status.
    - Note: volume domain also does NOT use `is_present` — volumes are storage entities managed by lifecycle, not file-presence. This is consistent design, not an omission.
- [x] add --timeout for mvm ssh
    - ✅ **IMPLEMENTED** — `--timeout/-t` → `-o ConnectTimeout=N` in SSH args.
- [x] proper exceptions per domain
    - ✅ **IMPLEMENTED** — All domains use `pkg/errs.DomainError` with domain-specific error codes.

## Networking

- [ ] fully isolated bridge networking for VMs
    - **❌ NOT IMPLEMENTED** — Plain bridge+TAP only. No VLANs, namespaces, ebtables, or isolation mode.
- [x] constrain TAP FORWARD rules to bridge subnet
    - ✅ **IMPLEMENTED** — `AddTapFirewallRules()` via `NewTapForwardRules()` constrains FORWARD rules to bridge subnet, matching `EnsureNAT()`.

## Codebase Maintainability

- [x] replace yaml_id references with internal id
    - ✅ **IMPLEMENTED** — Zero matches found. Clean: YAML slug → SHA256 hash.
- [ ] centralize all tool usage (ip, iptables, etc.) in utility files
    - **❌ (~35%)** — Read-only ip/iptables queries centralized in `internal/lib/network/`. iptables rule management centralized in `internal/lib/firewall/`. blkid centralized in `internal/lib/system/block.go` (`DetectFilesystemType`, `DetectFilesystemUUID`). Destructive ops + sfdisk/losetup/mount/visudo still scattered as direct subprocess calls across 6+ files.

## CLI

- [x] add confirmation to mvm cache prune including all sub commands
    - ✅ **IMPLEMENTED** — All prune subcommands prompt `common.Cli.PromptConfirm(ctx, "Continue?", true)`. `--force/-f` bypasses.
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
        - `pkg/api/volume.go` with `Operation.Volume*` methods
        - `pkg/api/inputs/volume_input.go`
        - `internal/lib/model/volume.go` with `VolumeItem`, `VolumeStatus`
        - `internal/cli/volume.go` registered in root command
        - DB schema in migrations (volumes table)
        - VM integration via `volume_ids` and `extra_drives` wiring

---

## Notes

- Items marked with [x] are complete and should be moved to archive when verified
- Keep this file focused on active work only
