"""Rich console utilities."""

from __future__ import annotations

from typing import Any


class _LazyConsoleProxy:
    def __init__(self) -> None:
        self._console: Any | None = None

    def _get_console(self) -> Any:
        if self._console is None:
            from rich.console import Console

            self._console = Console()
        return self._console

    def print(self, *args: Any, **kwargs: Any) -> None:
        self._get_console().print(*args, **kwargs)

    def __getattr__(self, name: str) -> Any:
        return getattr(self._get_console(), name)


console = _LazyConsoleProxy()


def print_table(title: str, columns: list[str], rows: list[list[str]]) -> None:
    from rich.table import Table

    table = Table(title=title)
    for col in columns:
        table.add_column(col)
    for row in rows:
        table.add_row(*row)
    console.print(table)


def print_error(message: str) -> None:
    console.print(f"[red]Error: {message}[/red]", highlight=False)


def print_success(message: str) -> None:
    console.print(f"[green]{message}[/green]", highlight=False)


def print_warning(message: str) -> None:
    console.print(f"[yellow]{message}[/yellow]", highlight=False)


def print_info(message: str) -> None:
    console.print(f"[blue]{message}[/blue]", highlight=False)
