# Provisioning Backend Mutual Exclusion — LoopMount vs GuestFS

**Status:** accepted
**Date:** 2026-05-22

The project provides two independent provisioning backends for root filesystem operations (cloud-init injection, shrink, deblob, OS detection): **LoopMount** and **GuestFS**. These backends are **mutually exclusive** — a single VM or image operation uses exactly one backend, never a combination. The `guestfs_enabled` setting acts as a toggle selector, not a preference.

## Mutual Exclusion Rule

Only one backend is active for a given operation. The selection logic is:

1. If `guestfs_enabled` (from user settings) is `true` → use **GuestFS**.
2. Else if the loop-mount binary is available → use **LoopMount**.
3. Else → raise an error: no provisioner available.

This means `guestfs_enabled` is an **override**, not a fallback. When set to `true`, GuestFS is used even if the faster loop-mount binary is available. The setting is persisted in user settings and can be toggled via `mvm init` or `mvm config set settings guestfs_enabled true|false`.

## Independence

Both backends have separate code paths, separate dependencies, separate error handling, and separate test suites. They share `ProvisionerContent` builders (common data: cloud-init user-data templates, fstab content, etc.) but never share runtime state:

| Aspect | LoopMount | GuestFS |
|--------|-----------|---------|
| Mechanism | `losetup` + `mount` + `chroot` via compiled `mvm` binary (`mvm run provision`) | `libguestfs` via QEMU appliance |
| Dependencies | Compiled binary (same `mvm` binary) | `libguestfs`, `supermin`, QEMU (system packages) |
| Performance | ~200ms per VM | ~2600ms per VM |
| Privilege | `mvm run provision` in sudoers | `supermin` in sudoers |
| Implementation | `internal/lib/provisioner/loopmount/` (backend), `internal/service/loopmount/` (subprocess entry) | `internal/lib/provisioner/guestfs/` |
| Default | **Yes** (`guestfs_enabled = false`) | **No** (opt-in via `mvm init` prompt or config) |

## No Mixing

A VM or image is provisioned with a single backend from start to finish:
- **VM creation**: The provisioner is selected once in `VMCreateRequest.Resolve()` and passed through the entire create pipeline.
- **Image optimization**: The provisioner type is resolved inline in `ImageOperation.Pull()` and `ImageOperation.Import()` and used for both shrink and deblob.
- **Cache operations**: GuestFS appliance cleanup runs independently of loop-mount state.

Attempting to mix backends within a single operation is a bug.

## Why Not a Fallback Chain

The natural alternative would be a fallback chain: try loop-mount first, fall back to guestfs if unavailable. This was rejected because:
- **Performance expectations**: If a user explicitly enables GuestFS, they expect GuestFS behavior (slower but more capable OS detection, init-system-aware SSH setup). A silent fallback to loop-mount would violate the principle of least surprise.
- **Dependency clarity**: Each backend has different sudoers requirements. Mixing them in a single session means both sets of sudoers rules must be valid, increasing the privilege surface area.
- **Test isolation**: Each backend has independent test suites. A fallback chain would require testing all combinations, significantly increasing the test matrix.
- **Bug prevention**: An earlier version of the selection logic incorrectly described GuestFS as a "fallback," which led to regression bugs where a stale `guestfs_enabled = true` setting silently selected the slow backend during performance-sensitive operations.

## Resolution at Startup

The provisioner type is resolved **once at startup** in `api.NewOperation()` by reading `settings.guestfs_enabled`. All callers use `op.ProvisionerType` directly. This eliminates repeated Config.Get calls for the same setting.

## Related Decisions

- CONTEXT.md "Provisioner Backend" — mount/umount consolidated in `mvm run provision` subcommand (loop-mount path only).
