"""Generic ID prefix resolution and multi-strategy resolution helpers."""

from __future__ import annotations

from typing import Any

__all__ = [
    "find_by_id_prefix",
]


def find_by_id_prefix(
    items: list[Any], id_field: str, prefix: str, min_length: int = 3
) -> list[Any]:
    """Find items matching an ID prefix.

    Args:
        items: List of objects to search
        id_field: Name of the attribute containing the full ID (e.g., 'id', 'hash')
        prefix: The ID prefix to match against
        min_length: Minimum prefix length required (default 3)

    Returns:
        List of matching items (may be empty)
    """
    if len(prefix) < min_length:
        return []

    return [item for item in items if getattr(item, id_field, "").startswith(prefix)]
