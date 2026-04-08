"""SSH key registry API — add, create, remove, list, inspect, defaults."""

from __future__ import annotations

import os
from pathlib import Path

from mvmctl.core.key_manager import (
    KeyInfo,
    get_key,
    inspect_key,
    list_keys,
    resolve_key_input,
)
from mvmctl.core.key_manager import (
    add_key as _core_add_key,
)
from mvmctl.core.key_manager import (
    clear_default_keys as _core_clear_default_keys,
)
from mvmctl.core.key_manager import (
    create_key as _core_create_key,
)
from mvmctl.core.key_manager import (
    export_key as _core_export_key,
)
from mvmctl.core.key_manager import (
    get_default_keys as _core_get_default_keys,
)
from mvmctl.core.key_manager import (
    remove_key as _core_remove_key,
)
from mvmctl.core.key_manager import (
    set_default_keys as _core_set_default_keys,
)
from mvmctl.exceptions import MVMKeyError
from mvmctl.models import KeyCreateInput

__all__ = [
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


def add_key(name: str, pub_key_path: str | Path, overwrite: bool = False) -> KeyInfo:
    """Add an existing SSH key to the registry.

    Validates the public key file path before importing:
    - Checks file existence
    - Checks .pub extension (with suggestion if .pub variant exists)
    - Checks file readability

    Raises:
        MVMKeyError: If file doesn't exist, isn't readable, or lacks .pub extension.
    """
    path_obj = Path(pub_key_path)

    # Validate file existence
    if not path_obj.exists():
        raise MVMKeyError(f"File not found: {pub_key_path}")

    # Validate .pub extension (with suggestion logic)
    if not str(pub_key_path).endswith(".pub"):
        pub_path = Path(str(pub_key_path) + ".pub")
        if pub_path.exists():
            raise MVMKeyError(
                f"File does not appear to be a public key: {pub_key_path}. Did you mean: {pub_path}"
            )
        else:
            raise MVMKeyError(
                f"File does not appear to be a public key: {pub_key_path}. "
                "Public keys typically end in .pub"
            )

    # Validate file readability
    if not os.access(path_obj, os.R_OK):
        raise MVMKeyError(f"Cannot read file: {pub_key_path}")

    result = _core_add_key(name, pub_key_path, overwrite)

    from mvmctl.utils.audit import log_audit

    log_audit("key.add", f"name={result.name}")

    return result


def create_key(input: KeyCreateInput) -> tuple[KeyInfo, Path]:
    """Create a new SSH keypair and add it to the registry.

    Args:
        input: KeyCreateInput containing name, output_dir, comment, and overwrite.

    Returns:
        Tuple of (KeyInfo, Path) for the created key.
    """
    result = _core_create_key(input.name, input.output_dir, input.comment, input.overwrite)

    from mvmctl.utils.audit import log_audit

    log_audit("key.create", f"name={input.name}")

    return result


def export_key(
    name: str,
    destination: str | Path | None = None,
    overwrite: bool = False,
) -> tuple[Path, Path]:
    """Export a key from the registry to a destination."""
    result = _core_export_key(name, destination, overwrite)

    from mvmctl.utils.audit import log_audit

    log_audit("key.export", f"name={name}")

    return result


def remove_key(name: str) -> None:
    """Remove a key from the registry."""
    _core_remove_key(name)

    from mvmctl.utils.audit import log_audit

    log_audit("key.remove", f"name={name}")


def set_default_keys(names: list[str]) -> None:
    _core_set_default_keys(names)

    from mvmctl.utils.audit import log_audit

    log_audit("key.set_defaults", f"count={len(names)}")


def get_default_keys() -> list[str]:
    return _core_get_default_keys()


def clear_default_keys() -> None:
    _core_clear_default_keys()

    from mvmctl.utils.audit import log_audit

    log_audit("key.clear_defaults")


def resolve_key_inputs(inputs: list[str]) -> list[str]:
    return [resolve_key_input(inp) for inp in inputs]
