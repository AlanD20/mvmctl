"""Plain-text console utilities — no Rich markup or ANSI codes."""

from __future__ import annotations

import re
from typing import Any


def _strip_markup(text: str) -> str:
    """Remove Rich markup tags such as [green], [/green], [bold], [dim], etc."""
    return re.sub(r"\[/?[a-zA-Z][^\[\]]*\]", "", text)


class _PlainConsole:
    """Minimal drop-in shim for the Rich Console API; emits plain text only."""

    def print(self, *args: Any, **kwargs: Any) -> None:
        # Discard Rich-specific kwargs (highlight, markup, style, …).
        text = " ".join(str(a) for a in args)
        print(_strip_markup(text))

    def __getattr__(self, name: str) -> Any:
        # Silently swallow Rich-specific attribute access (status, rule, …).
        def _noop(*a: Any, **k: Any) -> None:
            pass

        return _noop


console = _PlainConsole()


def print_table(columns: list[str], rows: list[list[str]], title: str | None = None) -> None:
    """Print a plain-text, column-aligned table."""
    if title:
        print(title)
        print("-" * len(title))

    col_widths = [len(c) for c in columns]
    for row in rows:
        for i, cell in enumerate(row):
            if i < len(col_widths):
                col_widths[i] = max(col_widths[i], len(str(cell)))

    def _fmt_row(cells: list[str]) -> str:
        return "  ".join(
            str(cells[i]).ljust(col_widths[i]) if i < len(cells) else " " * col_widths[i]
            for i in range(len(columns))
        )

    print(_fmt_row(columns))
    print("  ".join("-" * w for w in col_widths))
    for row in rows:
        print(_fmt_row([str(c) for c in row]))


def print_error(message: str) -> None:
    print(f"Error: {message}")


def print_success(message: str) -> None:
    print(f"✓ {message}")


def print_warning(message: str) -> None:
    print(f"! {message}")


def print_info(message: str) -> None:
    print(f"  {message}")


def print_section_header(title: str) -> None:
    """Print a section header like 'BASIC INFO'."""
    print(f"\n{title}")


def print_key_value(key: str, value: str, indent: int = 2, key_width: int = 12) -> None:
    """Print a key-value pair with consistent padding.

    Args:
        key: The key name (e.g., "Name")
        value: The value to display
        indent: Number of spaces to indent (default 2)
        key_width: Width for key column, left-aligned (default 12)
    """
    print(f"{' ' * indent}{key + ':':<{key_width}} {value}")


def format_timestamp(iso_timestamp: str | None) -> str:
    """Format ISO timestamp to 'YYYY/MM/DD HH:MM:SS'."""
    from datetime import datetime

    if not iso_timestamp:
        return "-"
    try:
        dt = datetime.fromisoformat(str(iso_timestamp).replace("Z", "+00:00"))
        return dt.strftime("%Y/%m/%d %H:%M:%S")
    except (ValueError, AttributeError):
        return str(iso_timestamp)


def print_inspect_header(title: str, subtitle: str = "") -> None:
    """Print inspect header with underline.

    Example:
        VM: myvm (running)
        ==================
    """
    if subtitle:
        header = f"{title} ({subtitle})"
    else:
        header = title
    print(f"\n{header}")
    print("=" * len(header))
