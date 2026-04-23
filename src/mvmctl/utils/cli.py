"""CLI utilities — domain-agnostic helpers for Typer commands."""

from __future__ import annotations

import logging
from functools import wraps
from typing import Callable, TypeVar

import typer

from mvmctl.exceptions import MVMError
from mvmctl.utils.console import print_error

logger = logging.getLogger(__name__)

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
        try:
            return func(*args, **kwargs)
        except MVMError as e:
            print_error(str(e))
            raise typer.Exit(code=1) from e
        except Exception as e:
            logger.exception("Unexpected error in CLI command")
            print_error(f"Unexpected error: {e}")
            raise typer.Exit(code=1) from e

    return wrapper  # type: ignore[return-value]


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
