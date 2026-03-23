"""Shared helpers for CLI command modules."""

import typer


def check_name_arg(ctx: typer.Context, name: str | None) -> str:
    """Guard for positional name arg: show help on ``"help"`` or ``None``, else return name."""
    if name == "help":
        typer.echo(ctx.get_help())
        raise typer.Exit()
    if name is None:
        typer.echo(ctx.get_help())
        raise typer.Exit(code=1)
    return name
