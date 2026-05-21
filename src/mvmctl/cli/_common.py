"""CLI-layer helpers shared across command modules."""

from __future__ import annotations

from collections.abc import Callable
from dataclasses import dataclass
from typing import Any

from mvmctl.utils.cli import mvm_cli


@dataclass
class ListingColumn:
    """A column in a listing table.

    The order of ``ListingColumn`` entries in the list determines both the
    short and long display order.  Columns with ``long_only=True`` are hidden
    in short mode.
    """

    header: str
    extract: Callable[[Any], str]
    long_only: bool = False


def render_listing(
    items: list[Any],
    columns: list[ListingColumn],
    style: str,
    *,
    title: str | None = None,
) -> None:
    """Build and print a listing table from column specs.

    Args:
        items: Domain items to render (VMInstanceItem, NetworkItem, …).
        columns: All possible columns.  Order in the list determines
            display order.
        style: ``"short"`` or ``"long"`` — ``long_only`` columns are
            included only in long mode.
        title: Optional table title.
    """
    visible = (
        columns if style == "long" else [c for c in columns if not c.long_only]
    )
    headers = [c.header for c in visible]
    rows: list[list[str]] = [
        [c.extract(item) for c in visible] for item in items
    ]
    mvm_cli.table(columns=headers, rows=rows, title=title)


def resolve_listing_style(long_output: bool) -> str:
    """Resolve ``"short"`` or ``"long"`` from ``--long`` flag or user config.

    Each ``ls`` command accepts a ``--long`` flag.  When not set, the default
    is read from ``settings.listing_style`` in the DB.  Falls back to
    ``"short"`` when the config key is missing or unset.
    """
    if long_output:
        return "long"
    try:
        from mvmctl.api import ConfigOperation as _cfg

        value = _cfg.get("settings", "listing_style")  # type: ignore[attr-defined]
        if isinstance(value, str):
            return value
    except Exception:
        pass
    return "short"
