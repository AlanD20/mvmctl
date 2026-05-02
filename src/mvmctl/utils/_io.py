"""Console output and logging utilities."""

from __future__ import annotations

import logging
import os
import re
from typing import Any

__all__ = [
    "console",
    "_PlainConsole",
    "_strip_markup",
    "print_table",
    "print_error",
    "print_success",
    "print_warning",
    "print_info",
    "print_section_header",
    "print_key_value",
    "print_inspect_header",
    "get_state_marker",
    "get_combined_marker",
    "setup_logging",
    "get_logger",
    "log_exception",
]


# ==================== Console utilities ====================


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


def print_table(
    columns: list[str], rows: list[list[str]], title: str | None = None
) -> None:
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
            str(cells[i]).ljust(col_widths[i])
            if i < len(cells)
            else " " * col_widths[i]
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


def print_key_value(
    key: str, value: str, indent: int = 2, key_width: int = 12
) -> None:
    """
    Print a key-value pair with consistent padding.

    Args:
        key: The key name (e.g., "Name")
        value: The value to display
        indent: Number of spaces to indent (default 2)
        key_width: Width for key column, left-aligned (default 12)

    """
    print(f"{' ' * indent}{key + ':':<{key_width}} {value}")


def print_inspect_header(title: str, subtitle: str = "") -> None:
    """
    Print inspect header with underline.

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


def get_state_marker(is_missing: bool) -> str:
    """
    Get the state marker prefix.

    Returns:
        "X " if resource is missing, "  " (two spaces) if present

    """
    return "X " if is_missing else "  "


def get_combined_marker(is_default: bool, is_missing: bool) -> str:
    """
    Get combined default and existence marker.

    Combines default marker (* ) with existence marker (X ) into a single
    3-character prefix for display in listing tables.

    Returns:
        "*X " - File missing + default
        "X "  - File missing + not default (with leading space for alignment)
        "* "  - File exists + default (with trailing space for alignment)
        "  "  - File exists + not default

    """
    if is_default and is_missing:
        return "*X "
    elif is_missing:
        return " X "  # Leading space for alignment with "*X "
    elif is_default:
        return "*  "  # Trailing space for alignment
    else:
        return "   "  # Three spaces for alignment


# ==================== Logging utilities ====================


def setup_logging(*, verbose: bool = False, debug: bool = False) -> None:
    """
    Configure root logger with console and file handlers.

    Console handler respects the configured level (DEBUG/INFO/WARNING).
    File handler always logs at DEBUG level to {cache_dir}/mvmctl.log
    for persistent debugging without requiring --debug flags.

    Priority (highest first):
    1. ``debug=True``  → DEBUG
    2. ``verbose=True`` → INFO
    3. ``MVM_LOG_LEVEL`` env var → parsed level (default WARNING)

    Args:
        verbose: Force INFO level on console.
        debug: Force DEBUG level on console.

    """
    if debug:
        level = logging.DEBUG
    elif verbose:
        level = logging.INFO
    else:
        env_level = os.environ.get("MVM_LOG_LEVEL", "WARNING").upper()
        level = getattr(logging, env_level, logging.WARNING)

    root = logging.getLogger()

    # Prevent duplicate handler setup on repeated calls
    if root.handlers:
        return

    formatter = logging.Formatter("%(levelname)s: %(name)s: %(message)s")

    # Console handler at configured level
    console = logging.StreamHandler()
    console.setLevel(level)
    console.setFormatter(formatter)
    root.addHandler(console)

    # File handler always at DEBUG — captures everything without --debug flags
    from logging.handlers import RotatingFileHandler

    from mvmctl.utils.common import CacheUtils

    log_path = CacheUtils.get_log_path()
    file_handler = RotatingFileHandler(
        str(log_path), maxBytes=10_485_760, backupCount=3
    )
    file_handler.setLevel(logging.DEBUG)
    file_handler.setFormatter(formatter)
    root.addHandler(file_handler)

    # Root must be at lowest level so individual handlers can filter up
    root.setLevel(logging.DEBUG)


def get_logger(name: str) -> logging.Logger:
    """
    Return a logger instance with the given name.

    Thin wrapper around :func:`logging.getLogger` for consistency.
    """
    return logging.getLogger(name)


def log_exception(logger: logging.Logger, msg: str, exc: Exception) -> None:
    """
    Log an exception respecting the configured log level.

    When DEBUG is enabled, logs the full traceback via
    :meth:`logging.Logger.exception`. Otherwise logs a single-line
    ERROR message without traceback.
    """
    if logger.isEnabledFor(logging.DEBUG):
        logger.exception("%s: %s", msg, exc)
    else:
        logger.error("%s: %s", msg, exc)
