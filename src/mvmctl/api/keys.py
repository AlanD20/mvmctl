"""SSH key registry API — add, create, remove, list, inspect, defaults."""

from __future__ import annotations

from mvmctl.core.key_manager import (
    KeyInfo,
    add_key,
    clear_default_keys as _core_clear_default_keys,
    create_key,
    export_key,
    get_default_keys as _core_get_default_keys,
    get_key,
    inspect_key,
    list_keys,
    remove_key,
    resolve_key_input,
    set_default_keys as _core_set_default_keys,
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
    "set_default_keys",
    "get_default_keys",
    "clear_default_keys",
    "resolve_key_inputs",
]


def set_default_keys(names: list[str]) -> None:
    _core_set_default_keys(names)


def get_default_keys() -> list[str]:
    return _core_get_default_keys()


def clear_default_keys() -> None:
    _core_clear_default_keys()


def resolve_key_inputs(inputs: list[str]) -> list[str]:
    return [resolve_key_input(inp) for inp in inputs]
