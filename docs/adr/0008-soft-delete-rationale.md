# Soft-Delete Rationale: When It Applies

**Status:** Active
**Date:** 2026-07-03

**Table of Contents**

- [Where soft-delete is used](#where-soft-delete-is-used)
- [Where it is NOT used](#where-it-is-not-used)
- [Rule](#rule)
- [Visibility and cleanup](#visibility-and-cleanup)

## Where soft-delete is used

The following repositories implement soft-delete (setting `deleted_at` timestamp):

- `image.Repository`
- `kernel.Repository`
- `binary.Repository`
- `network.Repository`

Soft-delete is triggered when `--force` is used to remove a resource that is
still referenced by VMs or snapshots. Instead of hard-deleting (which would
orphan the references), the row is marked with `deleted_at` and `is_present = 0`.

## Where it is NOT used

- **vm.Repository** — VM lifecycle is absolute (stop → optionally remove). Soft-delete would leave orphaned processes, TAP devices, and PID files. Hard delete forces proper cleanup.
- **volume.Repository** — Volumes map to actual disk files. A soft-deleted volume with no hard-delete path would leave orphaned disk files consuming space. The current design requires explicit `Delete()` which also removes the file.
- **key.Repository** — SSH keys are files on disk. Soft-delete would leave private key material accessible. Hard delete ensures key material is removed promptly.
- **host.Repository** — Host state is a singleton with dedicated `initialized` flag. The `host_state_changes` table uses a reverted flag instead of soft-delete.
- **SettingsRepository** — User settings are key-value pairs with no need for soft-delete.

## Rule

Soft-delete is used for **downloadable/cacheable assets** that can be re-fetched from a remote source (images, kernels, binaries). It is NOT used for:

1. **Runtime state** (VMs) — lifecycle is process-bound
2. **Local data** (volumes, keys) — files on disk with no remote source
3. **Configuration** (settings, host state) — singleton or key-value

## Visibility and cleanup

Soft-deleted resources are **visible in listings** with a `[x]` suffix in
red, so operators can see orphaned state. Previously these were hidden behind
`WHERE deleted_at IS NULL` in `ListAll` queries.

Cleanup options:
- **`mvm cache prune --all`** — removes all unused resources including orphaned
  soft-deleted records. Already handles networks; kernel/image/binary follow the
  same pattern.
- **`mvm net|image|kernel|bin rm <name>`** — the CLI sets `IncludeDeleted: true` on the
  input, so the resolver includes soft-deleted resources and hard-deletes the orphan.
