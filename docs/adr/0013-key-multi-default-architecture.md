# 0013 — Key domain multi-default architecture

**Status:** accepted

The SSH key domain is the only entity in the project that supports multiple simultaneous defaults. All other domains (image, kernel, binary, network) enforce a singleton default — setting a new default atomically clears the old one.

## What is different

| Aspect | Other domains | Key domain |
|--------|---------------|------------|
| Default count | Exactly 1 | 0 or more |
| `get_default()` | Returns `Optional[Item]` (single) | `get_defaults()` returns `list[SSHKeyItem]` |
| `set_default()` | Clears all others atomically | Sets one key, does not clear others |
| Storage | Single `is_default` column, 0/1 | Same column, but multiple rows can have `is_default=1` |

## Why

A VM can be configured with multiple SSH keys. When the authorized_keys file is generated during cloud-init, all default keys are injected. This is intentional — restricting to a single default key would break the multi-key workflow.

## Repository methods

- `get_defaults() -> list[SSHKeyItem]` — returns all keys with `is_default=1`
- `set_default(name) -> None` — sets `is_default=1` for the named key (no transaction, does not clear others)
- `clear_defaults() -> None` — sets `is_default=0` for all keys
