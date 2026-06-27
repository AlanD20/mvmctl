# --from-volume: Full-disk boot from a volume

> **STATUS: ⏳ Pending — not implemented.** No `--from-volume` code exists in the Go codebase. No `from_volume` field in `VMCreateInput` struct, no `--from-volume` flag on CLI. The volume domain (dependency) is complete, but the alternative VM creation path via volume has not been built.
>
> **Last verified:** 2026-06-27

**Phase:** After volume domain (✅ completed)
**Complexity:** Medium
**Depends on:** Volume domain ✅ (done), VM creation pipeline ✅ (exists)

## Goal

```bash
mvm vm create --name my-vm --from-volume my-provisioned-vol
```

Boot a VM directly from a pre-provisioned volume. The volume IS the root filesystem. No image download, no kernel resolution, no rootfs provisioning.

## What changes

This is essentially an alternative VM creation path. Currently `vm create` requires an image + kernel. With `--from-volume`, the volume provides both.

The volume must already contain:
- A bootable filesystem with kernel installed
- SSH keys injected, cloud-init configured, etc.

This means you'd typically:
1. Create a VM normally
2. Provision it (install packages, configure)
3. `mvm volume create data-vol --from-vm my-vm` (snapshot the VM's rootfs into a volume)
4. `mvm vm create --name cloned-vm --from-volume data-vol` — boot a clone

## Integration points

| Layer | Change |
|---|---|
| `VMCreateInput` | Add `from_volume: string` — mutually exclusive with `image` |
| `VMCreateRequest.Resolve()` | Skip image/kernel resolution. Resolve volume instead. Set `is_root_device=true` on volume drive. |
| `FirecrackerConfig` | Rootfs path comes from volume, not from warm-image pool |
| `VMOperation.Create()` | Short-circuit the image/kernel provisioning path |

## Constraints

- `--from-volume` and `--image` are mutually exclusive
- Volume must be in `available` status (not attached to another VM)
- Kernel must exist inside the volume (user's responsibility to install it)
