from __future__ import annotations

from pathlib import Path
from typing import Any, Callable


def resolve_single_by_id_prefix(
    prefix: str,
    find_fn: Callable[[Path, str], list[tuple[str, dict[str, Any]]]],
    cache_dir: Path,
    label: str | None = None,
) -> tuple[str, dict[str, Any]] | None:
    matches = find_fn(cache_dir, prefix)
    if len(matches) != 1:
        return None
    return matches[0]
