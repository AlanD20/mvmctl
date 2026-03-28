"""SSH key registry API — add, create, remove, list, inspect."""

from __future__ import annotations

from pathlib import Path

from mvmctl.core.key_manager import (
    KeyInfo,
    add_key,
    create_key,
    export_key,
    get_key,
    inspect_key,
    list_keys,
    remove_key,
)

__all__ = [
    "KeyInfo",
    "list_keys",
    "get_key",
    "add_key",
    "create_key",
    "remove_key",
    "inspect_key",
    "export_key",
]
