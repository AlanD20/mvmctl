# Provisioning Backend Mutual Exclusion — LoopMount vs GuestFS

**Status:** Active
**Date:** 2026-05-22
**Last Updated:** 2026-06-20 (Go implementation complete)

The project provides two independent provisioning backends for root filesystem operations (cloud-init injection, shrink, deblob, OS detection): **LoopMount** and **GuestFS**. These backends are **mutually exclusive** — a single VM or image operation uses exactly one backend, never a combination. The `guestfs_enabled` setting acts as a toggle selector, not a preference.

## Mutual Exclusion Rule

Only one backend is active for a given operation. The selection logic is:

1. If `guestfs_enabled` (from user settings) is `true` → use **GuestFS**.
2. Else → use **LoopMount**.

LoopMount is always available (it runs as the `mvm run provision` subprocess within the same binary, invoked via `system.SpawnService()` or directly via `runWireOp` in `internal/lib/provisioner/loopmount/backend.go`). There is no fallback chain — `guestfs_enabled` is an **override**, not a fallback. The setting is persisted in user settings and can be toggled via `mvm config set settings guestfs_enabled true|false`.

## Independence

Both backends have separate code paths, separate dependencies, separate error handling, and separate test suites. They share `ProvisionerContent` builders (common data: cloud-init user-data templates, fstab content, vsock agent injection, etc.) in `internal/infra/provcontent/` but never share runtime state:

| Aspect | LoopMount | GuestFS |
|--------|-----------|---------|
| Mechanism | `losetup` + `mount` + `chroot` via compiled `mvm` binary (`mvm run provision`) | `libguestfs` via QEMU appliance |
| Dependencies | Compiled binary (same `mvm` binary) | `libguestfs`, `supermin`, QEMU (system packages) |
| Performance (create → ready, seq) | 2–5s per VM (see benchmarks) | 9–14s per VM (see benchmarks) |
| Privilege | `mvm run provision` in sudoers | `supermin` in sudoers |
| Implementation | `internal/lib/provisioner/loopmount/` (backend), `internal/service/loopmount/` (subprocess entry) | `internal/lib/provisioner/guestfs/` |
| Default | **Yes** (`guestfs_enabled = false`) | **No** (opt-in via `mvm init` prompt or config) |

## No Mixing

A VM or image is provisioned with a single backend from start to finish:
- **VM creation**: The provisioner type is resolved once at startup in `api.NewOperation()` (`pkg/api/operation.go`, line 131-136) by reading `settings.guestfs_enabled`. All callers use `op.ProvisionerType` directly.
- **Image optimization**: The provisioner type flows through to image service methods via `provisioner.ProvisionerType` parameter.
- **Cache operations**: GuestFS appliance cleanup runs independently of loop-mount state.

Attempting to mix backends within a single operation is a bug.

## Why Not a Fallback Chain

The natural alternative would be a fallback chain: try loop-mount first, fall back to guestfs if unavailable. This was rejected because:
- **Performance expectations**: If a user explicitly enables GuestFS, they expect GuestFS behavior (slower but more capable OS detection, init-system-aware SSH setup). A silent fallback to loop-mount would violate the principle of least surprise.
- **Dependency clarity**: Each backend has different sudoers requirements. Mixing them in a single session means both sets of sudoers rules must be valid, increasing the privilege surface area.
- **Test isolation**: Each backend has independent test suites. A fallback chain would require testing all combinations, significantly increasing the test matrix.
- **Bug prevention**: An earlier version of the selection logic incorrectly described GuestFS as a "fallback," which led to regression bugs where a stale `guestfs_enabled = true` setting silently selected the slow backend during performance-sensitive operations.

## Resolution at Startup

The provisioner type is resolved **once at startup** in `api.NewOperation()` (`pkg/api/operation.go`, line 131-136) by reading `settings.guestfs_enabled`. All callers use `op.ProvisionerType` directly. This eliminates repeated Config.Get calls for the same setting.

```go
provisionerType := provisioner.ProvisionerLoopMount
guestfsEnabled, _ := s.Config.GetBool(ctx, "settings", "guestfs_enabled")
if guestfsEnabled {
    provisionerType = provisioner.ProvisionerGuestFS
}
```

## Performance Benchmark (Wall-clock, Sequential)

Measured from `mvm vm create` to first successful `vm exec echo ok` on a single host (x86_64, NVMe SSD). Each row is one VM, created sequentially.

| Image | LoopMount (create → ready) | GuestFS (create → ready) | Slowdown |
|---|---|---|---|
| alpine | 1.4s → **2.0s** | 8.4s → **9.5s** | **4.8x** |
| ubuntu:24.04 | 2.0s → **2.4s** | 9.5s → **10.1s** | **4.2x** |
| ubuntu-minimal:24.04 | 2.0s → **2.4s** | 10.7s → **10.7s** | **4.5x** |
| archlinux | 2.4s → **3.9s** | 13.7s → **13.7s** | **3.5x** |
| debian:12 | 4.3s → **4.6s** | 13.5s → **13.5s** | **2.9x** |
| firecracker:v1.15 | 1.9s → **2.1s** | 10.0s → **10.5s** | **5.0x** |

GuestFS is **3–5x slower** than LoopMount for VM creation. Each `guestfish` invocation spins up a full QEMU process with the libguestfs appliance to mount and modify the rootfs. Under **parallel** load, GuestFS degrades further (15–27s per VM) because concurrent QEMU instances contend for the libguestfs appliance lock, serializing on disk I/O.

The performance gap is an inherent property of the backend architecture, not an implementation issue. LoopMount modifies the rootfs via direct `mount` + `chroot`; GuestFS routes every filesystem operation through a QEMU-backed appliance.

## Related Decisions

- CONTEXT.md "Provisioner Backend" — mount/umount consolidated in `mvm run provision` subcommand (loop-mount path only).
- `benchmarks/results.json` — Full historical benchmark data for both backends.
