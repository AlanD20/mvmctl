"""CLI utilities — domain-agnostic helpers for Typer commands."""

from __future__ import annotations

import re
import sqlite3
from collections.abc import Callable
from datetime import datetime, timezone
from functools import wraps
from typing import Any, TypeVar

import click
import typer
from rich import box
from rich.console import Console
from rich.table import Table
from rich.tree import Tree

from mvmctl.exceptions import MVMError, PrivilegeError
from mvmctl.utils._io import get_logger, log_exception
from mvmctl.utils.common import CommonUtils
from mvmctl.utils.crypto import HashGenerator

F = TypeVar("F", bound=Callable[..., object])

# Prettification patterns for print_dict_tree keys
_PRETTIFY_PATTERNS: list[tuple[re.Pattern[str], str]] = [
    (re.compile(r"\bId\b"), "ID"),
    (re.compile(r"\bSsh\b"), "SSH"),
    (re.compile(r"\bIpv"), "IPv"),
    (re.compile(r"\bMac\b"), "MAC"),
    (re.compile(r"\bPid\b"), "PID"),
    (re.compile(r"\bUuid\b"), "UUID"),
    (re.compile(r"\bNat\b"), "NAT"),
    (re.compile(r"\bTap\b"), "TAP"),
    (re.compile(r"\bVms?\b"), "VM"),
    (re.compile(r"\bCpus?\b"), "CPU"),
    (re.compile(r"\bKvm\b"), "KVM"),
    (re.compile(r"\bOs\b"), "OS"),
    (re.compile(r"\bPci\b"), "PCI"),
    (re.compile(r"\bTmpfs\b"), "TMPFS"),
    (re.compile(r"\bFs\b"), "FS"),
]


def _prettify_key(key: str) -> str:
    """Convert snake_case key to Title Case with acronym normalization."""
    s = key.replace("_", " ").title()
    for pattern, replacement in _PRETTIFY_PATTERNS:
        s = pattern.sub(replacement, s)
    return s


class MVMCli:
    """
    Centralized display output for the CLI.

    Owns two Rich Console instances (stdout and stderr) and provides
    methods for all structured output: tables, trees, key-value pairs,
    headers, and formatted values.
    """

    def __init__(self) -> None:
        self._console = Console()
        self._err_console = Console(stderr=True)

    # ── Display methods ──────────────────────────────────────────────

    def error(self, message: str, *, is_unexpected: bool = False) -> None:
        """Print an error message to stderr."""
        if is_unexpected:
            self._err_console.print(f"[yellow]⚠ Unexpected Error:[/] {message}")
        else:
            self._err_console.print(f"[red]✗ Error:[/] {message}")

    def success(self, message: str) -> None:
        """Print a success message to stdout."""
        self._console.print(f"[green]✓ {message}[/]")

    def warning(self, message: str) -> None:
        """Print a warning message to stderr."""
        self._err_console.print(f"[yellow]! {message}[/]")

    def info(self, message: str) -> None:
        """Print an info/dim message to stdout."""
        self._console.print(f"[dim]  {message}[/]")

    def section_header(self, title: str) -> None:
        """Print a bold section title."""
        self._console.print(f"\n[bold]{title}[/]")

    def inspect_header(self, title: str, subtitle: str = "") -> None:
        """Print an inspect-style header with underline."""
        if subtitle:
            full = f"{title} ({subtitle})"
            self._console.print(f"\n[bold]{full}[/]")
            self._console.print("=" * len(full))
        else:
            self._console.print(f"\n[bold]{title}[/]")
            self._console.print("=" * len(title))

    def key_value(
        self, key: str, value: str, indent: int = 2, key_width: int = 12
    ) -> None:
        """Print a key-value pair with consistent padding."""
        padding = " " * indent
        self._console.print(f"{padding}{key + ':':<{key_width}} {value}")

    def table(
        self,
        columns: list[str],
        rows: list[list[str]],
        title: str | None = None,
    ) -> None:
        """Print a Rich table with SIMPLE box style."""
        rich_table = Table(
            *columns,
            title=title,
            title_justify="left",
            box=box.SIMPLE,
            header_style="bold",
        )
        for row in rows:
            rich_table.add_row(*row)
        self._console.print(rich_table)

    def print_dict_tree(
        self, data: dict[str, Any] | list[Any], title: str = ""
    ) -> None:
        """Print a nested dict/list as a Rich tree."""
        tree = Tree(title) if title else Tree("")
        self._build_tree(data, tree)
        if tree.children:
            self._console.print(tree)

    def _build_tree(self, data: Any, tree: Tree) -> None:
        """Recursively build a rich.tree.Tree from nested dict/lists."""
        if isinstance(data, dict):
            for key, value in data.items():
                pretty = _prettify_key(key)
                if isinstance(value, dict):
                    branch = tree.add(f"[bold]{pretty}[/]")
                    self._build_tree(value, branch)
                elif isinstance(value, list):
                    if value and isinstance(value[0], dict):
                        branch = tree.add(f"[bold]{pretty}[/]")
                        for i, item in enumerate(value):
                            item_branch = branch.add(f"[dim]#{i + 1}[/]")
                            self._build_tree(item, item_branch)
                    else:
                        items_str = (
                            ", ".join(str(v) for v in value) if value else "-"
                        )
                        tree.add(f"{pretty}: {items_str}")
                else:
                    display = self._format_leaf_value(key, value)
                    tree.add(f"{pretty}: {display}")
        elif isinstance(data, list):
            for i, item in enumerate(data):
                if isinstance(item, dict):
                    branch = tree.add(f"[dim]#{i + 1}[/]")
                    self._build_tree(item, branch)
                else:
                    tree.add(str(item))

    # ── Leaf value formatting for tree display ──────────────────────

    @staticmethod
    def _format_leaf_value(key: str, value: Any) -> str:
        """Format a leaf value for tree display.

        Auto-converts ``*_at`` timestamp suffixes from ISO to human-readable.
        """
        if value is None:
            return "-"
        if isinstance(value, str) and key.endswith("_at"):
            formatted = MVMCli.format_timestamp(value, "full")
            if formatted != value:
                return formatted
        return str(value)

    # ── Static format methods ────────────────────────────────────────

    @staticmethod
    def check_name_arg(ctx: typer.Context, name: str | None) -> str:
        """
        Guard for positional name arg: show help on ``"help"`` or ``None``, else return name.

        Args:
            ctx: Typer context for help output.
            name: The positional argument value.

        Returns:
            The validated name string.

        Raises:
            typer.Exit: If name is "help" (shows help) or None (shows help with error code).

        """
        if name == "help":
            typer.echo(ctx.get_help())
            raise typer.Exit()
        if name is None:
            typer.echo(ctx.get_help())
            raise typer.Exit(code=1)
        return name

    @staticmethod
    def format_timestamp(
        iso_string: str | None, style: str = "relative"
    ) -> str:
        """
        Format an ISO timestamp as relative or full date string.

        Args:
            iso_string: ISO format timestamp string.
            style: ``"relative"`` (default) or ``"full"`` (YYYY-MM-DD HH:MM:SS).

        Returns:
            Formatted string, or ``"-"`` if input is None.

        """
        if iso_string is None:
            return "-"
        try:
            dt = datetime.fromisoformat(str(iso_string).replace("Z", "+00:00"))
        except (ValueError, AttributeError):
            return str(iso_string)

        # Make dt timezone-aware for comparison with UTC now
        if dt.tzinfo is None:
            dt = dt.replace(tzinfo=timezone.utc)

        if style == "full":
            return dt.strftime("%Y-%m-%d %H:%M:%S")

        # Relative style
        now = datetime.now(timezone.utc)
        delta = now - dt
        total_seconds = int(delta.total_seconds())
        if total_seconds < 0:
            return "just now"
        if total_seconds < 60:
            return f"{total_seconds}s ago"
        minutes = total_seconds // 60
        if minutes < 60:
            return f"{minutes}m ago"
        hours = minutes // 60
        if hours < 24:
            return f"{hours}h ago"
        days = hours // 24
        if days < 7:
            return f"{days}d ago"
        weeks = days // 7
        if weeks < 5:
            return f"{weeks}w ago"
        months = days // 30
        if months < 12:
            return f"{months}mo ago"
        years = days // 365
        return f"{years}y ago"

    @staticmethod
    def format_size(size_bytes: int | None) -> str:
        """Format bytes as human-readable size, or ``"-"`` if None."""
        if size_bytes is None:
            return "-"
        return CommonUtils.format_bytes_human_readable(size_bytes)

    @staticmethod
    def format_id(id_string: str) -> str:
        """Return first 6 characters of a hash for display."""
        return HashGenerator.shorten(id_string, length=6)

    @staticmethod
    def format_marker(is_default: bool) -> str:
        """Return ``"*"`` if default, else empty string."""
        return "*" if is_default else ""

    @staticmethod
    def format_name(name: str, is_missing: bool) -> str:
        """Return name with Rich red markup if missing."""
        if is_missing:
            return f"[red]{name}[/]"
        return name


# Module-level singleton
mvm_cli = MVMCli()


# ── Error handler decorator ─────────────────────────────────────────


def handle_errors(func: F) -> F:
    """
    Decorator for CLI commands — catches all exceptions cleanly.

    Catches MVMError (and all subclasses) and unexpected exceptions,
    prints a clean user-friendly message, and exits with code 1.
    No Python tracebacks are shown to the user.

    Usage:
        @image_app.command(name="rm")
        @handle_errors
        def image_rm(...) -> None:
            ...
    """

    @wraps(func)
    def wrapper(*args: object, **kwargs: object) -> object:
        logger = get_logger(func.__module__)
        try:
            return func(*args, **kwargs)
        except typer.Exit:
            raise
        except click.exceptions.Abort:
            raise typer.Exit(code=130)
        except KeyboardInterrupt:
            raise typer.Exit(code=130)
        except BrokenPipeError:
            import sys

            try:
                sys.stderr.close()
            except BrokenPipeError:
                pass
            raise typer.Exit(code=0)
        except PrivilegeError as e:
            logger.debug("Privilege error in CLI command: %s", e, exc_info=True)
            mvm_cli.error(str(e))
            if e.details:
                detail_msg = e.details.get("message", "")
                if detail_msg:
                    mvm_cli.warning(f"Details: {detail_msg}")
                mvm_cli.info("Options:")
                for suggestion in e.details.get("suggestions", []):
                    mvm_cli.info(f"  - {suggestion}")
            raise typer.Exit(code=1) from e
        except MVMError as e:
            logger.debug(
                "%s in CLI command: %s", e.__class__.__name__, e, exc_info=True
            )
            mvm_cli.error(str(e))
            raise typer.Exit(code=1) from e
        except sqlite3.OperationalError as e:
            logger.debug("Database error in CLI command: %s", e, exc_info=True)
            msg = str(e)
            if "no such table" in msg:
                mvm_cli.error(
                    "Database schema not initialized. "
                    "Run 'mvm init' first to create the database."
                )
            else:
                mvm_cli.error(f"Database error: {e}")
            raise typer.Exit(code=1) from e
        except Exception as e:
            logger.debug("Full traceback:", exc_info=True)
            log_exception(logger, "Unexpected error in CLI command", e)
            mvm_cli.error(f"{e.__class__.__name__}: {e}", is_unexpected=True)
            raise typer.Exit(code=1) from e

    return wrapper  # type: ignore[return-value]
