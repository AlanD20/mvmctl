"""Rich console utilities."""

from rich.console import Console
from rich.table import Table

console = Console()


def print_table(title: str, columns: list[str], rows: list[list[str]]) -> None:
    table = Table(title=title)
    for col in columns:
        table.add_column(col)
    for row in rows:
        table.add_row(*row)
    console.print(table)


def print_error(message: str) -> None:
    console.print(f"[red]Error: {message}[/red]")


def print_success(message: str) -> None:
    console.print(f"[green]{message}[/green]")


def print_warning(message: str) -> None:
    console.print(f"[yellow]{message}[/yellow]")


def print_info(message: str) -> None:
    console.print(f"[blue]{message}[/blue]")
