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


def print_table(title: str, columns: list[str], rows: list[list[str]]) -> None:
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
