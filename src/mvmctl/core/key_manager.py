"""SSH key management — backward compatibility layer.

This module re-exports key management functionality for backward compatibility.
The actual implementation has been split into:
- KeyResolver: resolution/querying operations (in api/_internal/_resolvers/_key_resolver.py)
- KeyManager: lifecycle operations (in api/_internal/_key_manager.py)
"""

from __future__ import annotations

from pathlib import Path
from typing import TYPE_CHECKING

from mvmctl.api._internal._key_manager import KeyManager
from mvmctl.api._internal._resolvers._key_resolver import KeyResolver, KeyResolveResult
from mvmctl.core.mvm_db import MVMDatabase
from mvmctl.db.models import SSHKey
from mvmctl.utils.fs import get_keys_config_dir

# Backward compatibility alias
KeyInfo = SSHKey

if TYPE_CHECKING:
    pass

# Create singleton instances for backward-compatible function calls
_resolver: KeyResolver | None = None
_manager: KeyManager | None = None


def _get_resolver() -> KeyResolver:
    """Lazy initialization of KeyResolver singleton."""
    global _resolver
    if _resolver is None:
        _resolver = KeyResolver()
    return _resolver


def _get_manager() -> KeyManager:
    """Lazy initialization of KeyManager singleton."""
    global _manager
    if _manager is None:
        # Use a dummy key that exists or create with minimal setup
        # This is for backward-compatible function calls only
        _manager = KeyManager.__new__(KeyManager)
        _manager._db = MVMDatabase()
        _manager._key = None  # type: ignore
    return _manager


# Re-export dataclasses and resolver
__all__ = [
    "SSHKey",
    "KeyInfo",
    "KeyResolveResult",
    "KeyResolver",
    # Legacy function re-exports
    "add_key",
    "clear_default_keys",
    "create_key",
    "export_key",
    "get_default_keys",
    "get_key",
    "get_keys_config_dir",
    "get_public_key",
    "inspect_key",
    "list_keys",
    "remove_key",
    "resolve_key_input",
    "set_default_keys",
]


def add_key(name: str, pub_key_path: str | Path, overwrite: bool = False) -> SSHKey:
    """Add a public key to the cache."""
    return KeyManager("dummy").add_key(name, pub_key_path, overwrite)


def clear_default_keys() -> None:
    """Clear all default SSH keys."""
    return _get_manager().clear_default_keys()


def create_key(
    name: str,
    output_dir: str | Path | None = None,
    comment: str | None = None,
    overwrite: bool = False,
) -> tuple[SSHKey, Path]:
    """Generate a new ED25519 keypair."""
    return KeyManager("dummy").create_key(name, output_dir, comment, overwrite)


def export_key(
    name: str,
    destination: str | Path | None = None,
    overwrite: bool = False,
) -> tuple[Path, Path]:
    """Export a keypair from cache to destination directory."""
    return KeyManager(name).export(destination, overwrite)


def get_default_keys() -> list[str]:
    """Get the list of default SSH key names."""
    return _get_manager().get_default_keys()


def get_key(name: str) -> SSHKey | None:
    """Get a key by name, or None if not found."""
    try:
        return KeyManager(name).inspect()
    except Exception:
        return None


def get_public_key(name: str) -> str:
    """Get the public key content for a cached key by name."""
    return KeyManager.get_pubkey(name)


def inspect_key(name: str) -> SSHKey:
    """Return detailed info about a named key."""
    return KeyManager(name).inspect()


def list_keys() -> list[SSHKey]:
    """List all keys in the cache."""
    return _get_manager().list_keys()


def remove_key(name: str) -> None:
    """Remove a key from the cache."""
    KeyManager(name).remove()


def resolve_key_input(input_str: str) -> str:
    """Resolve a key name, file path, or fingerprint to a cached key name."""
    return _get_resolver().resolve(input_str).name


def set_default_keys(names: list[str]) -> None:
    """Set the default SSH keys list."""
    return _get_manager().set_default_keys(names)
