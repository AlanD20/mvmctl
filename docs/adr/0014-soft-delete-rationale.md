# 0014 — Soft-delete rationale: when it applies

**Status:** accepted

## Where soft-delete is used

The following repositories implement soft-delete (setting `deleted_at` timestamp + filtering by `deleted_at IS NULL`):

- ImageRepository
- KernelRepository
- BinaryRepository
- NetworkRepository

## Where it is NOT used

- **VMRepository** — VM lifecycle is absolute (stop → optionally remove). Soft-delete would leave orphaned processes, TAP devices, and PID files. Hard delete forces proper cleanup.
- **VolumeRepository** — Volumes map to actual disk files. A soft-deleted volume with no hard-delete path would leave orphaned disk files consuming space. The current design requires explicit `delete()` which also removes the file.
- **KeyRepository** — SSH keys are files on disk. Soft-delete would leave private key material accessible. Hard delete ensures key material is removed promptly.
- **HostRepository** — Host state is a singleton with dedicated `initialized` flag. The state_changes table uses a reverted flag instead of soft-delete.
- **SettingsRepository** — User settings are key-value pairs with no need for soft-delete.

## Rule

Soft-delete is used for **downloadable/cacheable assets** that can be re-fetched from a remote source (images, kernels, binaries). It is NOT used for:

1. **Runtime state** (VMs) — lifecycle is process-bound
2. **Local data** (volumes, keys) — files on disk with no remote source
3. **Configuration** (settings, host state) — singleton or key-value
