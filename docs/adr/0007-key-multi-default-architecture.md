# Key Domain Multi-Default Architecture

**Status:** accepted
**Date:** 2026-05-22

The SSH key domain is the only entity in the project that supports multiple simultaneous defaults. All other domains (image, kernel, binary, network) enforce a singleton default — setting a new default atomically clears the old one.

## What is different

| Aspect | Other domains | Key domain |
|--------|---------------|------------|
| Default count | Exactly 1 | 0 or more |
| `GetDefault()` | Returns `*Item` (single) | `GetDefaults()` returns `[]*model.SSHKeyItem` |
| `SetDefault()` | Clears all others atomically | Sets one key, does not clear others |
| Storage | Single `is_default` column, 0/1 | Same column, but multiple rows can have `is_default=1` |

## Why

A VM can be configured with multiple SSH keys. When the authorized_keys file is generated during cloud-init, all default keys are injected. This is intentional — restricting to a single default key would break the multi-key workflow.

## Repository methods

- `GetDefaults(ctx) ([]*model.SSHKeyItem, error)` — returns all keys with `is_default=1`
- `SetDefault(ctx, name) error` — sets `is_default=1` for the named key (no transaction, does not clear others)
- `ClearDefaults(ctx) error` — sets `is_default=0` for all keys
