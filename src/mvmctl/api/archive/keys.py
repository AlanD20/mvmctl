"""SSH key registry API — add, create, remove, list, inspect, defaults."""

from __future__ import annotations

import os
from pathlib import Path

from mvmctl.api._internal._key_manager import KeyManager
from mvmctl.api._internal._resolvers import KeyResolver
from mvmctl.db.models import SSHKey
from mvmctl.exceptions import MVMKeyError
from mvmctl.models import KeyCreateInput

# Backward compatibility alias
KeyInfo = SSHKey

__all__ = [
    "SSHKey",
    "KeyInfo",
    "list_keys",
    "get_key",
    "add_key",
    "create_key",
    "remove_key",
    "inspect_key",
    "export_key",
    "set_default_keys",
    "get_default_keys",
    "clear_default_keys",
    "resolve_key_inputs",
]


def list_keys() -> list[SSHKey]:
    """List all keys in the cache."""
    return KeyManager("dummy").list_keys()


def get_key(name: str) -> SSHKey | None:
    """Get a key by name."""
    try:
        manager = KeyManager(name)
        return manager.inspect()
    except Exception:
        return None


def add_key(name: str, pub_key_path: str | Path, overwrite: bool = False) -> SSHKey:
    """Add an existing SSH key to the registry."""
    path_obj = Path(pub_key_path)

    if not path_obj.exists():
        raise MVMKeyError(f"File not found: {pub_key_path}")

    if not str(pub_key_path).endswith(".pub"):
        pub_path = Path(str(pub_key_path) + ".pub")
        if pub_path.exists():
            raise MVMKeyError(
                f"File does not appear to be a public key: {pub_key_path}. Did you mean: {pub_path}"
            )
        raise MVMKeyError(
            f"File does not appear to be a public key: {pub_key_path}. "
            "Public keys typically end in .pub"
        )

    if not os.access(path_obj, os.R_OK):
        raise MVMKeyError(f"Cannot read file: {pub_key_path}")

    result = KeyManager("dummy").add_key(name, pub_key_path, overwrite)

    from mvmctl.utils.audit import log_audit

    log_audit("key.add", f"name={result.name}")
    return result


def create_key(input: KeyCreateInput) -> tuple[SSHKey, Path]:
    """Create a new SSH keypair and add it to the registry."""
    result = KeyManager("dummy").create_key(
        input.name, input.output_dir, input.comment, input.overwrite
    )

    from mvmctl.utils.audit import log_audit

    log_audit("key.create", f"name={input.name}")
    return result


def export_key(
    name: str,
    destination: str | Path | None = None,
    overwrite: bool = False,
) -> tuple[Path, Path]:
    """Export a key from the registry to a destination."""
    manager = KeyManager(name)
    result = manager.export(destination, overwrite)

    from mvmctl.utils.audit import log_audit

    log_audit("key.export", f"name={name}")
    return result


def remove_key(name: str) -> None:
    """Remove a key from the registry."""
    manager = KeyManager(name)
    manager.remove()

    from mvmctl.utils.audit import log_audit

    log_audit("key.remove", f"name={name}")


def set_default_keys(names: list[str]) -> None:
    """Set default SSH keys."""
    KeyManager("dummy").set_default_keys(names)

    from mvmctl.utils.audit import log_audit

    log_audit("key.set_defaults", f"count={len(names)}")


def get_default_keys() -> list[str]:
    """Get default SSH keys."""
    return KeyManager("dummy").get_default_keys()


def clear_default_keys() -> None:
    """Clear all default SSH keys."""
    KeyManager("dummy").clear_default_keys()

    from mvmctl.utils.audit import log_audit

    log_audit("key.clear_defaults")


def inspect_key(name: str) -> SSHKey:
    """Inspect a key and return detailed information."""
    manager = KeyManager(name)
    return manager.inspect()


def resolve_key_inputs(inputs: list[str]) -> list[str]:
    """Resolve multiple key inputs to their content."""
    resolver = KeyResolver()
    result = resolver.resolve_many(inputs)
    if result.errors:
        raise MVMKeyError(result.errors[0])
    return result.items
