"""
Typed field extraction helpers for YAML-parsed dictionaries.

These helpers validate and extract typed values from ``dict[str, Any]`` objects
produced by ``yaml.safe_load``.  They raise :class:`ValueError` on type or
presence failures so that callers can convert the error into whatever domain
exception is appropriate.
"""

from __future__ import annotations

from typing import Any


def require_str(data: dict[str, Any], key: str) -> str:
    """
    Return the value of *key* as a string, raising ``ValueError`` if absent or wrong-typed.

    Args:
        data: Mapping to read from.
        key: Field name.

    Returns:
        The string value.

    Raises:
        ValueError: If the key is missing or its value is not a :class:`str`.

    """
    value = data.get(key)
    if isinstance(value, str):
        return value
    raise ValueError(
        f"field '{key}' must be a string (got {type(value).__name__!r})"
    )


def optional_str(data: dict[str, Any], key: str) -> str | None:
    """
    Return the string value of *key*, or ``None`` if absent or non-string.

    Args:
        data: Mapping to read from.
        key: Field name.

    Returns:
        The string value, or ``None``.

    """
    value = data.get(key)
    return value if isinstance(value, str) else None


def optional_int(data: dict[str, Any], key: str) -> int | None:
    """
    Return the integer value of *key*, or ``None`` if absent or non-integer.

    Args:
        data: Mapping to read from.
        key: Field name.

    Returns:
        The integer value, or ``None``.

    """
    value = data.get(key)
    return value if isinstance(value, int) else None


def require_str_list(data: dict[str, Any], key: str) -> list[str]:
    """
    Return the value of *key* as a list of strings.

    An absent key is treated as an empty list.  A value that is a list but
    contains non-string items, or a value that is not a list at all, raises
    ``ValueError``.

    Args:
        data: Mapping to read from.
        key: Field name.

    Returns:
        List of strings (may be empty).

    Raises:
        ValueError: If the value is present but is not a list of strings.

    """
    value = data.get(key, [])
    if isinstance(value, list) and all(isinstance(item, str) for item in value):
        return list(value)
    raise ValueError(f"field '{key}' must be a list of strings")


def parse_set_val_list(data: dict[str, Any], key: str) -> list[tuple[str, str]]:
    """
    Return option/value pairs from *key* as a list of ``(option, value)`` tuples.

    Each entry in the YAML list may be either a ``{option: ..., value: ...}``
    mapping or a two-element sequence.  Any other shape raises ``ValueError``.

    An absent key is treated as an empty list.

    Args:
        data: Mapping to read from.
        key: Field name whose value is the list of option/value pairs.

    Returns:
        List of ``(option, value)`` tuples (may be empty).

    Raises:
        ValueError: If the value is not a list, or any entry has an unexpected shape.

    """
    items = data.get(key, [])
    if not isinstance(items, list):
        raise ValueError(f"field '{key}' must be a list")
    result: list[tuple[str, str]] = []
    for item in items:
        if isinstance(item, dict) and "option" in item and "value" in item:
            result.append((str(item["option"]), str(item["value"])))
        elif isinstance(item, (list, tuple)) and len(item) == 2:
            result.append((str(item[0]), str(item[1])))
        else:
            raise ValueError(
                f"field '{key}' entries must be {{option, value}} mappings or two-element lists"
            )
    return result
