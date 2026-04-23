"""CLI utilities — domain-agnostic helpers for Typer commands."""

from __future__ import annotations

from functools import wraps
from typing import Callable, TypeVar

import typer
from rich.console import Console

from mvmctl.exceptions import MVMError
from mvmctl.utils.logging import get_logger, log_exception

_err_console = Console(stderr=True)

F = TypeVar("F", bound=Callable[..., object])


def handle_errors(func: F) -> F:
    """Decorator for CLI commands — catches all exceptions cleanly.

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
        except MVMError as e:
            _print_error(str(e))
            raise typer.Exit(code=1) from e
        except Exception as e:
            log_exception(logger, "Unexpected error in CLI command", e)
            _print_error(f"Unexpected error: {e}", is_unexpected=True)
            raise typer.Exit(code=1) from e

    return wrapper  # type: ignore[return-value]


def _print_error(message: str, *, is_unexpected: bool = False) -> None:
    """Print a colored single-line error to stderr."""
    emoji = "⚠" if is_unexpected else "✗"
    color = "yellow" if is_unexpected else "red"
    title = "Unexpected Error" if is_unexpected else "Error"
    _err_console.print(f"[{color}]{emoji} {title}:[/] {message}")


class CliUtils:
    """Domain-agnostic CLI helpers.

    All methods are static — no instance state needed.
    """

    @staticmethod
    def check_name_arg(ctx: typer.Context, name: str | None) -> str:
        """Guard for positional name arg: show help on ``"help"`` or ``None``, else return name.

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
