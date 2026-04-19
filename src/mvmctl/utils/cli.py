"""CLI utilities — domain-agnostic helpers for Typer commands."""

from __future__ import annotations

import typer


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
