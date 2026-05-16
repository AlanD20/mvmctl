# 0012 — Listing unification: `list_all(remote=False)` for remote-capable domains

**Status:** accepted

The project previously used three different method naming conventions for "list all entities" across domains: `list_all()` (VM, Network, Kernel), `list_local()` (Image, Binary), `list_keys()` (Key), and `list_()` (Volume, Image at the API layer). Remote listing was handled by separate methods (`list_remote()`) or flags with different signatures.

## What changed

Every domain now exposes `list_all()` on both the Service and Operation layers. Domains that support remote listing (image, kernel, binary) additionally accept `remote=False`:

| Domain | Before | After | `remote` param? |
|--------|--------|-------|-----------------|
| ImageService | `list_local()` | `list_all(remote=False)` | Yes |
| BinaryService | `list_local()` | `list_all(remote=False)` | Yes |
| KernelService | `list_all()` | `list_all()` | No (handled at operation layer) |
| KeyService | `list_keys()` | `list_all()` | No |
| ImageOperation | `list_()` | `list_all(remote=False)` | Yes |
| VolumeOperation | `list_()` | `list_all()` | No |
| BinaryOperation | `list_local()` + `list_remote()` | `list_all(remote=False)` | Yes |
| KernelOperation | `list_all()` | `list_all(remote=False)` | Yes |

When `remote=True`, the method fetches the version list from the remote source (GitHub API, HTML directory listing, etc.). When `remote=False` (default), it returns the locally cached entities.

Domains without a remote source (VM, network, volume, key, host, config) keep a simple `list_all()` with no `remote` parameter.

## Why

- Eliminates confusion between `list_()`, `list_all()`, `list_local()`, and `list_keys()` for the same concept
- Single consistent interface: callers always use `list_all()`
- CLI `--remote` flag uniformly maps to `remote=True` only on domains that support it
- Removes the unnatural trailing-underscore hack (`list_()` to avoid shadowing `list`)
